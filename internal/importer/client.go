// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package importer migrates data from Jira, Confluence, Bitbucket and Bamboo Data Center. It
// extracts through the documented REST APIs into a work directory, analyses, transforms and loads the data into
// the modules, keyed by source ids so that loads can be repeated, and validates the result.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"kogge.de/kogge/internal/platform/netpolicy"
)

// Client reads a Data Center REST API with a personal access token. Answers that say the source is busy (429,
// 5xx) and failed connections are tried again after growing pauses, or the pause the source asks for; answers
// that break off are read again. API requests have a time limit per attempt; downloads have none, but end when
// no data arrives for a while (Stall).
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client // API requests
	Files   *http.Client // downloads of attachment files
	Log     *slog.Logger
	// Attempts is how often a request is tried; MaxPause bounds the pause between two attempts.
	Attempts int
	MaxPause time.Duration
	// Stall ends a download when no data arrived for this long; the attempt is then repeated.
	Stall time.Duration
	// pause waits between attempts (tests replace it).
	pause func(ctx context.Context, d time.Duration) error
}

// NewClient creates a client for the base URL (e.g. https://jira.example.com).
func NewClient(baseURL, token string, log *slog.Logger) *Client {
	files := sourceTransport()
	files.ResponseHeaderTimeout = 2 * time.Minute
	base := strings.TrimRight(baseURL, "/")
	return &Client{BaseURL: base, Token: token,
		HTTP:  &http.Client{Transport: sourceTransport(), Timeout: 5 * time.Minute, CheckRedirect: sameSource(base)},
		Files: &http.Client{Transport: files, CheckRedirect: sameSource(base)}, Log: log, Attempts: 8, MaxPause: 5 * time.Minute,
		Stall: 2 * time.Minute, pause: sleep}
}

// sourceTransport connects to migration sources within their outbound policy (netpolicy.Internal): sources are
// usually internal systems, so private networks are fine, but neither a source's host names nor its redirects
// lead to link-local or cloud metadata addresses. A proxy that the environment names is used as before; behind it,
// the proxy decides.
func sourceTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = netpolicy.Internal().Dialer("", 30*time.Second).DialContext
	return t
}

// RedirectError is a redirect of the source to another system, which the importer does not follow: it reads only
// the source it was given, which the access token is for.
type RedirectError struct {
	From, To, Source string
}

func (e *RedirectError) Error() string {
	return fmt.Sprintf("the source redirected %s to %s, which is not on %s; the importer reads only the source it was given "+
		"(use the address the source redirects to as the source's URL if that is the same system)", e.From, e.To, e.Source)
}

// sameSource returns the redirect check of a client of the source at base: redirects stay on its scheme, host and
// port.
func sameSource(base string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("the source redirected more than 10 times")
		}
		if !sameOrigin(req.URL.String(), base) {
			return &RedirectError{From: via[len(via)-1].URL.String(), To: req.URL.String(), Source: base}
		}
		return nil
	}
}

// refused reports whether a request failed for a reason that trying again does not change: a redirect elsewhere,
// or an address the outbound policy refuses.
func refused(err error) bool {
	var re *RedirectError
	return errors.As(err, &re) || netpolicy.Forbidden(err)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// StatusError is a non-success answer of the source system.
type StatusError struct {
	URL    string
	Status int
	Body   string
	after  string // the Retry-After header
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s", e.URL, e.Status, strings.TrimSpace(e.Body))
}

// gone reports an answer that says the resource does not exist (any more): deleted after it was listed, or a
// file the source has lost. Extractions note such files and go on.
func gone(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Status == http.StatusNotFound || se.Status == http.StatusGone)
}

// retryable marks an error of reading an answer that is worth another attempt (a connection that broke off).
type retryable struct{ err error }

func (r retryable) Error() string { return r.err.Error() }
func (r retryable) Unwrap() error { return r.err }

// serverError reports an answer of the source that says it failed (after the attempts it was given).
func serverError(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status >= 500
}

type attemptsKey struct{}

// fewAttempts bounds the attempts of the requests made with the returned context. Extractions read the items of
// a page that failed one by one with it: the page's request has shown that the error persists, and an item that
// is broken in the source must not cost minutes of pauses.
func fewAttempts(ctx context.Context) context.Context {
	return context.WithValue(ctx, attemptsKey{}, 3)
}

// brokenItem reports whether a request for an item failed because of the item: the source answered with a server
// error but still answers probe (a request that is known to work). Otherwise the source itself fails — it is
// down or overloaded — and the extraction stops, to resume later, instead of leaving items out.
func (c *Client) brokenItem(ctx context.Context, err error, probe string) bool {
	if !serverError(err) {
		return false
	}
	_, perr := c.GetJSON(ctx, probe)
	return perr == nil
}

// busy reports answers after which the source should be asked again.
func busy(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// retryAfter reads the pause a source asks for: seconds or an HTTP date.
func retryAfter(h string, now time.Time) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if s, err := strconv.Atoi(h); err == nil && s >= 0 {
		return time.Duration(s) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// backoff is the pause before attempt n+1: it doubles from a second, varies by a quarter (so that parallel
// requests do not return together) and is at most MaxPause.
func (c *Client) backoff(n int) time.Duration {
	d := time.Duration(1<<min(n, 20)) * time.Second
	d = time.Duration(float64(d) * (0.75 + rand.Float64()/2))
	return min(d, c.maxPause())
}

func (c *Client) maxPause() time.Duration {
	if c.MaxPause <= 0 {
		return 5 * time.Minute
	}
	return c.MaxPause
}

// resolve makes an address absolute. Absolute addresses must point to the source system: the token is never
// sent elsewhere.
func (c *Client) resolve(u string) (string, error) {
	if strings.HasPrefix(u, "/") {
		return c.BaseURL + u, nil
	}
	if !sameOrigin(u, c.BaseURL) {
		return "", fmt.Errorf("%s is not on %s; the access token is only sent to the source system", u, c.BaseURL)
	}
	return u, nil
}

// do performs a GET and hands a successful answer to consume. Busy answers, failed connections and answers that
// break off (consume returns a retryable error) are tried again.
func (c *Client) do(ctx context.Context, hc *http.Client, u, accept string, consume func(*http.Response) error) error {
	u, err := c.resolve(u)
	if err != nil {
		return err
	}
	attempts := max(c.Attempts, 1)
	if n, ok := ctx.Value(attemptsKey{}).(int); ok {
		attempts = min(attempts, n)
	}
	pause := c.pause
	if pause == nil {
		pause = sleep
	}
	var last error
	for n := 0; n < attempts; n++ {
		if n > 0 {
			wait := c.backoff(n - 1)
			var se *StatusError
			if errors.As(last, &se) && se.Status == http.StatusTooManyRequests {
				// Rate limits end when the source says so.
				if d, ok := se.retryAfter(); ok {
					wait = min(d, c.maxPause())
				}
			}
			if c.Log != nil {
				c.Log.Debug("trying again", "url", u, "attempt", n+1, "pause", wait.Round(time.Millisecond), "error", last)
			}
			if err := pause(ctx, wait); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", accept)
		res, err := hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if refused(err) {
				return err
			}
			last = err
			continue
		}
		if res.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
			res.Body.Close()
			se := &StatusError{URL: u, Status: res.StatusCode, Body: string(b), after: res.Header.Get("Retry-After")}
			if !busy(res.StatusCode) {
				return se
			}
			last = se
			continue
		}
		err = consume(res)
		res.Body.Close()
		var r retryable
		if err != nil && errors.As(err, &r) && ctx.Err() == nil {
			last = r.err
			continue
		}
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return last
}

func (e *StatusError) retryAfter() (time.Duration, bool) { return retryAfter(e.after, time.Now()) }

// sameOrigin reports whether two addresses are of the same scheme, host and port (the default port of the scheme
// when none is named).
func sameOrigin(a, b string) bool {
	oa, ok := origin(a)
	if !ok {
		return false
	}
	ob, ok := origin(b)
	return ok && oa == ob
}

func origin(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port, true
}

// maxJSON bounds an API answer.
const maxJSON = 512 << 20

// GetJSON fetches a JSON document and returns it raw.
func (c *Client) GetJSON(ctx context.Context, path string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, c.HTTP, path, "application/json", func(res *http.Response) error {
		b, err := io.ReadAll(io.LimitReader(res.Body, maxJSON+1))
		if err != nil {
			return retryable{err}
		}
		if len(b) > maxJSON {
			return fmt.Errorf("%s: the answer is larger than %d MiB", path, maxJSON>>20)
		}
		if !json.Valid(b) {
			return fmt.Errorf("%s: the answer is not JSON", path)
		}
		out = b
		return nil
	})
	return out, err
}

// GetText fetches a document of another type (e.g. an XML export) and returns it.
func (c *Client) GetText(ctx context.Context, path, accept string, limit int64) ([]byte, error) {
	var out []byte
	err := c.do(ctx, c.HTTP, path, accept, func(res *http.Response) error {
		b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil {
			return retryable{err}
		}
		if int64(len(b)) > limit {
			return fmt.Errorf("%s: the answer is larger than %d bytes", path, limit)
		}
		out = b
		return nil
	})
	return out, err
}

// Download stores a binary resource in a file, streaming it to disk. A size of 0 or more is checked: a shorter
// or longer answer is downloaded again. The file appears only when it is complete; a partial download (a .part
// file of an earlier attempt or run) is started again.
func (c *Client) Download(ctx context.Context, u, file string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	tmp := file + ".part"
	err := c.do(ctx, c.files(), u, "*/*", func(res *http.Response) error {
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		n, err := copyWithStall(f, res.Body, c.Stall)
		if cerr := f.Close(); err == nil && cerr != nil {
			return cerr
		}
		if err != nil {
			return retryable{err}
		}
		if size >= 0 && n != size {
			return retryable{fmt.Errorf("%s: %d of %d bytes arrived", u, n, size)}
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}

func (c *Client) files() *http.Client {
	if c.Files != nil {
		return c.Files
	}
	return &http.Client{Transport: sourceTransport(), CheckRedirect: sameSource(c.BaseURL)}
}

// errStalled ends a download that receives no data.
var errStalled = errors.New("no data arrived for too long")

// copyWithStall copies like io.Copy, but fails when no data arrives for the time stall (no limit when 0): the
// body is then closed, which ends the read that waits.
func copyWithStall(w io.Writer, body io.ReadCloser, stall time.Duration) (int64, error) {
	if stall <= 0 {
		return io.Copy(w, body)
	}
	var stalled atomic.Bool
	t := time.AfterFunc(stall, func() {
		stalled.Store(true)
		body.Close()
	})
	defer t.Stop()
	n, err := io.Copy(w, readerFunc(func(p []byte) (int, error) {
		n, err := body.Read(p)
		t.Reset(stall)
		return n, err
	}))
	if stalled.Load() {
		return n, errStalled
	}
	return n, err
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// Workdir stores extracted data verbatim.
type Workdir struct {
	Root string
}

// Path returns a path below the work directory. Parts come from the source system (keys, ids, file names), so each
// becomes one path element: separators, "." and ".." are escaped and cannot leave the directory.
func (w Workdir) Path(parts ...string) string {
	elems := make([]string, 0, len(parts)+1)
	elems = append(elems, w.Root)
	for _, p := range parts {
		elems = append(elems, pathElement(p))
	}
	return filepath.Join(elems...)
}

// pathElement makes a part of a work directory path one harmless path element; distinct parts stay distinct.
func pathElement(p string) string {
	switch p {
	case "":
		return "" // left out by filepath.Join
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	}
	if !strings.ContainsAny(p, "/\\%\x00") {
		return p
	}
	var b strings.Builder
	for _, r := range p {
		switch r {
		case '/', '\\', '%', 0:
			fmt.Fprintf(&b, "%%%02X", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Write stores a JSON document. The file is replaced at once: an interrupted write leaves the old one.
func (w Workdir) Write(data json.RawMessage, parts ...string) error {
	return writeFile(w.Path(parts...), data)
}

// Read decodes a stored JSON document; missing files leave v unchanged and report false.
func (w Workdir) Read(v any, parts ...string) (bool, error) {
	b, err := os.ReadFile(w.Path(parts...))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return true, fmt.Errorf("%s: %w", w.Path(parts...), err)
	}
	return true, nil
}

// List returns the names of the entries of a directory in ascending order (none if it does not exist).
func (w Workdir) List(parts ...string) []string {
	entries, err := os.ReadDir(w.Path(parts...))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".part") && !strings.HasSuffix(e.Name(), ".tmp") {
			names = append(names, e.Name())
		}
	}
	return names
}

// Exists reports whether a file was extracted.
func (w Workdir) Exists(parts ...string) bool {
	_, err := os.Stat(w.Path(parts...))
	return err == nil
}

// Size returns the size of an extracted file, or -1.
func (w Workdir) Size(parts ...string) int64 {
	st, err := os.Stat(w.Path(parts...))
	if err != nil || !st.Mode().IsRegular() {
		return -1
	}
	return st.Size()
}

// writeFile replaces a file at once: an interrupted write leaves the old one (or none), never a part.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func query(params map[string]string) string {
	v := url.Values{}
	for k, val := range params {
		v.Set(k, val)
	}
	return v.Encode()
}
