// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package httpx contains Kogge's HTTP middleware and helpers shared by the API, Git and the web application.
package httpx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/crypto"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	clientIPKey
	requestInfoKey
)

// RequestInfo is mutable per-request information that inner handlers fill in for the access log.
type RequestInfo struct {
	mu     sync.Mutex
	UserID int64
}

// SetUser records the authenticated user for the access log.
func SetUser(ctx context.Context, userID int64) {
	if ri, ok := ctx.Value(requestInfoKey).(*RequestInfo); ok {
		ri.mu.Lock()
		ri.UserID = userID
		ri.mu.Unlock()
	}
}

// RequestID returns the id of the current request.
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

// ClientIP returns the client's address (after trusted proxies).
func ClientIP(ctx context.Context) netip.Addr {
	a, _ := ctx.Value(clientIPKey).(netip.Addr)
	return a
}

// WithClientIP sets the client address (used by the SSH server and tests).
func WithClientIP(ctx context.Context, ip netip.Addr) context.Context {
	return context.WithValue(ctx, clientIPKey, ip)
}

// Base wraps a handler with request ids, client address resolution, access logging and panic recovery.
func Base(next http.Handler, logger *slog.Logger, trusted []netip.Prefix) http.Handler {
	var warned atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 64 || !isTrustedPeer(r.RemoteAddr, trusted) {
			id = crypto.RandomString(12)
		}
		ip := resolveClientIP(r, trusted)
		// A reverse proxy that is not listed makes all clients look like one: rate limits then block them together
		// and the audit log shows the proxy. Said once, since anyone can send the header.
		if r.Header.Get("X-Forwarded-For") != "" && !isTrustedPeer(r.RemoteAddr, trusted) && warned.CompareAndSwap(false, true) {
			logger.Warn("a request carries X-Forwarded-For from an address that is not a trusted proxy, so the header is "+
				"ignored; if this is your reverse proxy, list it in KOGGE_TRUSTED_PROXIES, or rate limits and the audit log "+
				"see its address for all clients (logged once)", "peer", ip.String())
		}
		info := &RequestInfo{}
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, clientIPKey, ip)
		ctx = context.WithValue(ctx, requestInfoKey, info)
		w.Header().Set("X-Request-Id", id)
		rw := &ResponseWriter{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				logger.Error("panic in handler", "panic", fmt.Sprint(p), "stack", string(debug.Stack()),
					"request_id", id, "path", r.URL.Path)
				if !rw.wroteHeader {
					WriteProblem(rw, r, apperr.Internal(fmt.Errorf("panic: %v", p)))
				}
			}
			info.mu.Lock()
			uid := info.UserID
			info.mu.Unlock()
			level := slog.LevelInfo
			if rw.status >= 500 {
				level = slog.LevelError
			} else if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				level = slog.LevelDebug
			}
			logger.Log(ctx, level, "request", "method", r.Method, "path", r.URL.Path, "status", rw.Status(),
				"bytes", rw.bytes, "duration_ms", time.Since(start).Milliseconds(), "request_id", id,
				"client", ip.String(), "user_id", uid)
		}()
		next.ServeHTTP(rw, r.WithContext(ctx))
	})
}

func isTrustedPeer(remoteAddr string, trusted []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// resolveClientIP walks X-Forwarded-For from the right while the hops are trusted proxies.
func resolveClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	addr = addr.Unmap()
	if len(trusted) == 0 || !contains(trusted, addr) {
		return addr
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		hop = hop.Unmap()
		addr = hop
		if !contains(trusted, hop) {
			break
		}
	}
	return addr
}

func contains(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ResponseWriter records the status and size of a response.
type ResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

// WriteHeader records the status.
func (w *ResponseWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *ResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Status returns the response status (200 if none was written).
func (w *ResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// Flush implements http.Flusher (server-sent events, Git).
func (w *ResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wroteHeader {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

// Hijack implements http.Hijacker.
func (w *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijacking not supported")
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *ResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WriteProblem writes an error as problem details.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error) {
	p := apperr.From(err).Localize(apperr.Language(r.Header.Get("Accept-Language")))
	switch {
	case p.Status == http.StatusGatewayTimeout:
		slog.Default().Warn("a request was stopped at its time limit", "error", err, "request_id", RequestID(r.Context()),
			"path", r.URL.Path)
	case p.Status >= 500:
		slog.Default().Error("request failed", "error", err, "request_id", RequestID(r.Context()), "path", r.URL.Path)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON writes v as JSON with the status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// SecurityHeaders sets the headers every response carries. The CSP applies to HTML documents of the web
// application; API and file responses are not rendered as documents.
func SecurityHeaders(next http.Handler, hsts bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		// Answers of the API are personal: no browser or proxy keeps them (audit L2). Handlers that may be cached
		// (downloads) set their own Cache-Control.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// AppCSP is the Content Security Policy of the web application.
const AppCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'self'; " +
	"form-action 'self'; object-src 'none'"

// AppCSPExternalImages is AppCSP for instances whose documents may show images from other servers (over HTTPS).
var AppCSPExternalImages = strings.Replace(AppCSP, "img-src 'self' data: blob:;", "img-src 'self' data: blob: https:;", 1)

// SandboxCSP is applied to user-provided files that are displayed inline.
const SandboxCSP = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox"

// Files that users uploaded may be displayed inline if they are of these types; all are downloaded otherwise.
var inlineFileTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"application/pdf": true, "text/plain": true}

// servedFileTypes keep their type when served; the rest is application/octet-stream. Uploaders choose the type,
// and a file served as script or style would be one of Kogge's own (same origin), which the CSP allows.
var servedFileTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"image/avif": true, "image/bmp": true, "application/pdf": true, "text/plain": true, "application/zip": true,
	"application/gzip": true, "application/x-tar": true, "audio/mpeg": true, "audio/ogg": true, "audio/wav": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true}

// FileHeaders sets the headers of a file that a user uploaded: a type that cannot run in the browser, inline display
// only for harmless types (and not when download is asked for), the file name, and the sandbox CSP.
func FileHeaders(w http.ResponseWriter, contentType, filename string, download bool) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(mediaType)
	switch {
	case err != nil:
		mediaType = "application/octet-stream"
	case strings.HasPrefix(mediaType, "text/"):
		mediaType = "text/plain"
	case !servedFileTypes[mediaType]:
		mediaType = "application/octet-stream"
	}
	disposition := "attachment"
	if inlineFileTypes[mediaType] && !download {
		disposition = "inline"
	}
	ct := mediaType
	if mediaType == "text/plain" {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
	w.Header().Set("Content-Security-Policy", SandboxCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
