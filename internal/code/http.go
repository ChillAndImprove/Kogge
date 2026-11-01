// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/httpx"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

// Handler serves Git over HTTP (smart protocol) and the raw content of repositories.
type Handler struct {
	S *Service
}

// Routes registers the routes (behind authentication).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/scm/{key}/{repo}/info/refs", h.infoRefs)
	r.Post("/scm/{key}/{repo}/git-upload-pack", h.rpc("upload-pack"))
	r.Post("/scm/{key}/{repo}/git-receive-pack", h.rpc("receive-pack"))
	r.Post("/scm/{key}/{repo}/info/lfs/objects/batch", h.lfsBatch)
	r.Get("/scm/{key}/{repo}/info/lfs/objects/{oid}", h.lfsDownload)
	r.Put("/scm/{key}/{repo}/info/lfs/objects/{oid}", h.lfsUpload)
	r.Post("/scm/{key}/{repo}/info/lfs/objects/{oid}/verify", h.lfsVerify)
	r.Get("/api/v1/code/repos/{key}/{slug}/raw", h.raw)
	r.Get("/api/v1/code/repos/{key}/{slug}/archive", h.archive)
}

var gitProtocolPattern = regexp.MustCompile(`^[a-z0-9=:.,_-]{0,100}$`)

// gitPrincipal authenticates a Git client: Basic credentials (user name and access token), or the session.
func (h *Handler) gitPrincipal(r *http.Request) (*identity.Principal, error) {
	if user, secret, ok := r.BasicAuth(); ok {
		return h.S.identity.AuthenticateBasic(r.Context(), user, secret)
	}
	return identity.From(r.Context()), nil
}

func gitError(w http.ResponseWriter, r *http.Request, p *identity.Principal, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) && (ae.Status == http.StatusNotFound || ae.Status == http.StatusForbidden || ae.Status == http.StatusUnauthorized) &&
		(p == nil || !p.Authenticated()) {
		// Ask anonymous clients for credentials instead of revealing whether the repository exists.
		w.Header().Set("WWW-Authenticate", `Basic realm="Kogge", charset="UTF-8"`)
		http.Error(w, "Authentication required: use your user name and a personal access token.", http.StatusUnauthorized)
		return
	}
	if errors.As(err, &ae) {
		http.Error(w, ae.Detail, ae.Status)
		return
	}
	httpx.WriteProblem(w, r, err)
}

// gitRepo finds the repository of a Git request and checks the access the service needs.
func (h *Handler) gitRepo(r *http.Request, service string) (*identity.Principal, *repoRow, error) {
	// Build jobs clone with their job token (read access to their repository only).
	if _, secret, ok := r.BasicAuth(); ok && strings.HasPrefix(secret, "kgj_") && h.S.jobAuth != nil {
		repoID, valid := h.S.jobAuth(r.Context(), secret)
		repo, err := repoByPath(r.Context(), h.S.pool, chi.URLParam(r, "key"), chi.URLParam(r, "repo"))
		if !valid || err != nil || repo.ID != repoID || service != "upload-pack" {
			return nil, nil, apperr.Forbidden("auth.job-token", "The job token is not valid for this repository.")
		}
		return nil, repo, nil
	}
	p, err := h.gitPrincipal(r)
	if err != nil {
		return nil, nil, err
	}
	a, err := h.S.Access(r.Context(), p)
	if err != nil {
		return p, nil, err
	}
	level := LevelRead
	scope := identity.ScopeRead
	if service == "receive-pack" {
		level, scope = LevelWrite, identity.ScopeWrite
	}
	if p.Authenticated() && !p.AllowsScope(scope) {
		return p, nil, apperr.Forbidden("auth.token-scope", "The access token lacks the %s scope.", scope)
	}
	repo, err := h.S.repo(r.Context(), a, chi.URLParam(r, "key"), chi.URLParam(r, "repo"), level)
	return p, repo, err
}

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func (h *Handler) infoRefs(w http.ResponseWriter, r *http.Request) {
	service := strings.TrimPrefix(r.URL.Query().Get("service"), "git-")
	if service != "upload-pack" && service != "receive-pack" {
		http.Error(w, "Only the smart HTTP protocol is supported; please update your Git client.", http.StatusForbidden)
		return
	}
	p, repo, err := h.gitRepo(r, service)
	if err != nil {
		gitError(w, r, p, err)
		return
	}
	protocol := r.Header.Get("Git-Protocol")
	if !gitProtocolPattern.MatchString(protocol) {
		protocol = ""
	}
	w.Header().Set("Content-Type", "application/x-git-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	if !strings.Contains(protocol, "version=2") {
		_, _ = io.WriteString(w, pktLine("# service=git-"+service+"\n")+"0000")
	}
	if service == "receive-pack" {
		// A push refused before it starts sends nothing: git shows the reason as "remote error: …".
		if refusal, err := h.S.pushRefusal(r.Context(), repo); err != nil || refusal != "" {
			if err != nil {
				h.S.log.Error("checking the space for a push", "repository", repo.ID, "error", err)
				refusal = "Kogge cannot accept pushes now; try again later."
			}
			_, _ = io.WriteString(w, pktLine("ERR "+refusal+"\n"))
			return
		}
	}
	cmd := h.S.git.Command(r.Context(), "", []string{"GIT_PROTOCOL=" + protocol}, service, "--stateless-rpc", "--advertise-refs", h.S.repoPath(repo.ID))
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		h.S.log.Warn("git advertisement failed", "repository", repo.ID, "error", err)
	}
}

func (h *Handler) rpc(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-git-"+service+"-request" {
			http.Error(w, "unexpected content type", http.StatusUnsupportedMediaType)
			return
		}
		p, repo, err := h.gitRepo(r, service)
		if err != nil {
			gitError(w, r, p, err)
			return
		}
		args := []string{service, "--stateless-rpc", h.S.repoPath(repo.ID)}
		var push *pushContext
		var rest io.Reader // what git leaves unread of a push it stops
		if service == "receive-pack" {
			st, err := h.S.settings.Get(r.Context())
			if err != nil {
				httpx.WriteProblem(w, r, err)
				return
			}
			if refusal, err := h.S.pushRefusal(r.Context(), repo); err != nil || refusal != "" {
				if err != nil {
					httpx.WriteProblem(w, r, err)
					return
				}
				http.Error(w, refusal, http.StatusInsufficientStorage)
				return
			}
			// git itself stops a pack over the maximum and tells the client why (receive.maxInputSize); the limit of
			// the body is the backstop for what git does not count, such as endless ref updates.
			limit := settings.MiB(st.MaxPushMB) + pushOverhead
			if r.ContentLength > limit {
				http.Error(w, fmt.Sprintf("The push is larger than the %s this installation accepts.", storage.Format(settings.MiB(st.MaxPushMB))),
					http.StatusRequestEntityTooLarge)
				return
			}
			// git answers a push it stops while the client still sends it. Its answer must not end the upload (full
			// duplex), and the rest of the upload, up to as much again, is read and dropped: otherwise the client
			// loses the connection instead of hearing why, and curl sends the rest of the pack as a request of its own.
			_ = http.NewResponseController(w).EnableFullDuplex()
			rest = io.LimitReader(r.Body, limit)
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			args = receivePackArgs(st, "--stateless-rpc", h.S.repoPath(repo.ID))
			push = &pushContext{repoID: repo.ID, principal: p, protocol: "http", maxFile: settings.MiB(st.MaxGitFileMB)}
		}
		body := io.Reader(r.Body)
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "bad gzip body", http.StatusBadRequest)
				return
			}
			defer gz.Close()
			body = gz
		}
		protocol := r.Header.Get("Git-Protocol")
		if !gitProtocolPattern.MatchString(protocol) {
			protocol = ""
		}
		env := []string{"GIT_PROTOCOL=" + protocol}
		if push != nil {
			token, hookEnv, err := h.S.hooks.register(push)
			if err != nil {
				httpx.WriteProblem(w, r, err)
				return
			}
			defer h.S.hooks.done(token)
			env = append(env, hookEnv...)
		}
		w.Header().Set("Content-Type", "application/x-git-"+service+"-result")
		w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
		cmd := h.S.git.Command(r.Context(), "", env, args...)
		cmd.Stdin = body
		cmd.Stdout = flushWriter{w}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		start := time.Now()
		err = cmd.Run()
		metrics.Git(service, "http", start, err)
		if rest != nil {
			_, _ = io.Copy(io.Discard, rest)
		}
		if err != nil && r.Context().Err() == nil {
			h.S.log.Warn("git "+service+" failed", "repository", repo.ID, "error", err, "stderr", stderr.String())
		}
	}
}

// flushWriter sends Git's output as it comes (progress, large packs).
type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

func (h *Handler) raw(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Images that pages embed (<img>) may show inline; opened as a page, SVG and the like are downloaded.
	view := RawDownload
	switch {
	case q.Get("download") != "":
	case r.Header.Get("Sec-Fetch-Dest") == "image":
		view = RawImage
	default:
		view = RawPage
	}
	err := h.S.Raw(r.Context(), identity.From(r.Context()), chi.URLParam(r, "key"), chi.URLParam(r, "slug"), q.Get("ref"), q.Get("path"), w,
		view)
	if err != nil {
		httpx.WriteProblem(w, r, err)
	}
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = "zip"
	}
	err := h.S.Archive(r.Context(), identity.From(r.Context()), chi.URLParam(r, "key"), chi.URLParam(r, "slug"), q.Get("ref"), format, w)
	if err != nil {
		httpx.WriteProblem(w, r, err)
	}
}
