// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/netpolicy"
)

// testClient is a client whose pauses are recorded instead of waited for.
func testClient(t *testing.T, url string) (*Client, *[]time.Duration) {
	c := NewClient(url, "token", nil)
	var mu sync.Mutex
	var pauses []time.Duration
	c.pause = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		pauses = append(pauses, d)
		mu.Unlock()
		return ctx.Err()
	}
	return c, &pauses
}

func TestClientRetries(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/busy": // two busy answers, then the document
			if calls <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/limited":
			if calls == 1 {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/missing":
			http.NotFound(w, r)
		case "/broken": // an answer that breaks off: the body is shorter than announced
			w.Header().Set("Content-Length", "100")
			if calls == 1 {
				_, _ = w.Write([]byte(`{"o`))
				return
			}
			w.Header().Set("Content-Length", "11")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	c, pauses := testClient(t, srv.URL)
	if b, err := c.GetJSON(ctx, "/busy"); err != nil || string(b) != `{"ok":true}` || calls != 3 || len(*pauses) != 2 {
		t.Errorf("busy: %s %v after %d calls, pauses %v", b, err, calls, *pauses)
	}
	if (*pauses)[1] <= (*pauses)[0]/2 {
		t.Errorf("the pauses do not grow: %v", *pauses)
	}
	calls = 0
	c, pauses = testClient(t, srv.URL)
	if _, err := c.GetJSON(ctx, "/limited"); err != nil || len(*pauses) != 1 || (*pauses)[0] != 7*time.Second {
		t.Errorf("rate limit: %v, pauses %v", err, *pauses)
	}
	calls = 0
	c, pauses = testClient(t, srv.URL)
	if _, err := c.GetJSON(ctx, "/missing"); !gone(err) || calls != 1 || len(*pauses) != 0 {
		t.Errorf("a missing resource is tried again or not reported: %v after %d calls", err, calls)
	}
	calls = 0
	c, _ = testClient(t, srv.URL)
	if b, err := c.GetJSON(ctx, "/broken"); err != nil || string(b) != `{"ok":true}` || calls != 2 {
		t.Errorf("an answer that broke off: %s %v after %d calls", b, err, calls)
	}
	// The token goes to the source only.
	if _, err := c.GetJSON(ctx, "https://elsewhere.example.com/rest"); err == nil || !strings.Contains(err.Error(), "only sent to the source") {
		t.Errorf("another host: %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if d, ok := retryAfter("120", now); !ok || d != 2*time.Minute {
		t.Errorf("seconds: %v %v", d, ok)
	}
	if d, ok := retryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now); !ok || d != 30*time.Second {
		t.Errorf("a date: %v %v", d, ok)
	}
	if _, ok := retryAfter("soon", now); ok {
		t.Error("nonsense accepted")
	}
	c := NewClient("http://x", "", nil)
	c.MaxPause = 10 * time.Second
	for n := range 10 {
		if d := c.backoff(n); d > 10*time.Second || d <= 0 {
			t.Errorf("backoff %d = %v", n, d)
		}
	}
}

func TestDownload(t *testing.T) {
	var calls int
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/short": // the first answer is shorter than the attachment's size
			if calls == 1 {
				_, _ = w.Write([]byte("abc"))
				return
			}
			_, _ = w.Write([]byte("abcdef"))
		case "/stall": // the first answer stops sending
			if calls == 1 {
				w.Header().Set("Content-Length", "6")
				_, _ = w.Write([]byte("abc"))
				w.(http.Flusher).Flush()
				<-release
				return
			}
			_, _ = w.Write([]byte("abcdef"))
		case "/gone":
			http.Error(w, "gone", http.StatusGone)
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx := context.Background()
	dir := t.TempDir()

	c, _ := testClient(t, srv.URL)
	file := filepath.Join(dir, "a", "short")
	if err := c.Download(ctx, "/short", file, 6); err != nil || calls != 2 {
		t.Fatalf("short answer: %v after %d calls", err, calls)
	}
	if b, _ := os.ReadFile(file); string(b) != "abcdef" {
		t.Errorf("content: %q", b)
	}
	if _, err := os.Stat(file + ".part"); !os.IsNotExist(err) {
		t.Errorf("a part file is left: %v", err)
	}
	calls = 0
	c, _ = testClient(t, srv.URL)
	c.Stall = 200 * time.Millisecond
	file = filepath.Join(dir, "stall")
	if err := c.Download(ctx, "/stall", file, 6); err != nil || calls != 2 {
		t.Fatalf("stalled answer: %v after %d calls", err, calls)
	}
	calls = 0
	if err := c.Download(ctx, "/gone", filepath.Join(dir, "gone"), -1); !gone(err) || calls != 1 {
		t.Errorf("gone: %v after %d calls", err, calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone")); !os.IsNotExist(err) {
		t.Errorf("a file for a missing download: %v", err)
	}
}

// TestRedirectsStayOnTheSource: the importer follows redirects within the source, refuses those to another system
// (scheme, host or port) at once, naming where they lead, and does not connect to link-local or metadata addresses
// although sources may be internal.
func TestRedirectsStayOnTheSource(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(`{"secret":true}`))
	}))
	defer other.Close()
	var src *httptest.Server
	src = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/moved":
			http.Redirect(w, r, src.URL+"/rest/here", http.StatusFound)
		case "/rest/here":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default: // to another system
			http.Redirect(w, r, other.URL+"/latest/meta-data", http.StatusFound)
		}
	}))
	defer src.Close()
	ctx := context.Background()
	c, pauses := testClient(t, src.URL)
	if raw, err := c.GetJSON(ctx, "/rest/moved"); err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("a redirect within the source: %s %v", raw, err)
	}
	var re *RedirectError
	if _, err := c.GetJSON(ctx, "/rest/away"); !errors.As(err, &re) || re.To != other.URL+"/latest/meta-data" ||
		!strings.Contains(err.Error(), other.URL) {
		t.Errorf("a redirect to another system: %v", err)
	}
	if err := c.Download(ctx, "/files/away", filepath.Join(t.TempDir(), "f"), -1); !errors.As(err, &re) {
		t.Errorf("a download redirected to another system: %v", err)
	}
	if n := elsewhere.Load(); n != 0 || len(*pauses) != 0 {
		t.Errorf("the other system got %d requests; %d attempts were repeated", n, len(*pauses))
	}

	// Link-local and metadata addresses are refused before connecting, and not tried again.
	for _, u := range []string{"http://169.254.169.254", "http://[fd00:ec2::254]"} {
		c, pauses := testClient(t, u)
		if _, err := c.GetJSON(ctx, "/latest/meta-data"); !netpolicy.Forbidden(err) || len(*pauses) != 0 {
			t.Errorf("%s: %v after %d attempts", u, err, len(*pauses)+1)
		}
	}

	for _, s := range []struct {
		a, b string
		same bool
	}{
		{"https://Jira.example.com/a", "https://jira.example.com:443", true},
		{"http://jira.example.com:80/a", "http://jira.example.com", true},
		{"http://jira.example.com/a", "https://jira.example.com", false},
		{"https://jira.example.com:8443/a", "https://jira.example.com", false},
		{"https://jira.example.com.evil/a", "https://jira.example.com", false},
	} {
		if sameOrigin(s.a, s.b) != s.same {
			t.Errorf("sameOrigin(%s, %s) != %v", s.a, s.b, s.same)
		}
	}
}

// TestMirrorRefusesRedirects: git, too, reads repositories only from the source, not from where it redirects.
func TestMirrorRefusesRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		http.NotFound(w, r)
	}))
	defer other.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer src.Close()
	b := NewBitbucket(BitbucketOptions{URL: src.URL, Token: "token", Workdir: t.TempDir()}, slog.New(slog.DiscardHandler))
	err := b.mirror(context.Background(), "SHOP", bbRepo{Slug: "web"}, []string{"bitbucket", "SHOP", "web"})
	if err == nil || !strings.Contains(err.Error(), "the source redirected the clone") {
		t.Fatalf("the mirror of a source that redirects: %v", err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the other system got %d requests", n)
	}
}

func TestCheckpoint(t *testing.T) {
	w := Workdir{Root: t.TempDir()}
	opts := map[string]any{"attachments": true}
	c, err := openCheckpoint(w, "jira", false, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.markDone("config", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.markDone("project/SHOP/issues/0", "100"); err != nil {
		t.Fatal(err)
	}
	if err := c.note(ExtractionNote{Kind: "attachment", ID: "1", Note: "gone"}); err != nil {
		t.Fatal(err)
	}
	_ = c.close()
	// A cut-off last line (a crash while writing) is skipped.
	f, err := os.OpenFile(w.Path("jira", "extraction.log"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"k":"project/SHO`)
	f.Close()

	// The same options resume.
	c, err = openCheckpoint(w, "jira", false, map[string]any{"attachments": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := c.isDone("project/SHOP/issues/0"); !ok || v != "100" || !c.resumed() {
		t.Errorf("not resumed: %v %q", ok, v)
	}
	if _, ok := c.isDone("project/SHOP/issues/1"); ok {
		t.Error("a part that was not done")
	}
	ran := false
	if err := c.part("config", func() error { ran = true; return nil }); err != nil || ran {
		t.Errorf("a done part ran again: %v", err)
	}
	if notes, err := readExtractionNotes(w, "jira"); err != nil || len(notes) != 1 {
		t.Errorf("notes: %v %v", notes, err)
	}
	_ = c.close()

	// Other options begin afresh, and so does a finished extraction or a restart.
	c, err = openCheckpoint(w, "jira", false, map[string]any{"attachments": false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.isDone("config"); ok || c.resumed() {
		t.Error("resumed with other options")
	}
	if notes, _ := readExtractionNotes(w, "jira"); len(notes) != 0 {
		t.Errorf("the notes of the earlier run are kept: %v", notes)
	}
	_ = c.markDone("config", "")
	if err := c.finish(); err != nil {
		t.Fatal(err)
	}
	st, err := readExtractionState(w, "jira")
	if err != nil || st == nil || st.Finished == nil {
		t.Fatalf("state: %+v %v", st, err)
	}
	c, err = openCheckpoint(w, "jira", false, map[string]any{"attachments": false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.isDone("config"); ok {
		t.Error("a finished extraction resumed")
	}
	_ = c.markDone("config", "")
	_ = c.close()
	c, err = openCheckpoint(w, "jira", true, map[string]any{"attachments": false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.isDone("config"); ok {
		t.Error("a restart resumed")
	}
	_ = c.close()
}

func TestEachItem(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	files := []string{
		write("a.json", "[\n  {\"id\": \"1\", \"t\": \"a, [b]\"},\n  {\"id\": \"2\"}\n]\n"),
		write("b.json", `null`),
		write("c.json", ``),
		write("d.json", `[{"id":"3","nested":[1,2,{"x":"]"}]}]`),
	}
	var ids []string
	var places []itemAt
	err := eachItem(files, func(raw json.RawMessage, at itemAt) error {
		var v struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		ids = append(ids, v.ID)
		places = append(places, at)
		return nil
	})
	if err != nil || strings.Join(ids, ",") != "1,2,3" {
		t.Fatalf("items: %v %v", ids, err)
	}
	// Each element is read again from its place.
	for i, at := range places {
		raw, err := readItem(at)
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &v); err != nil || v.ID != strconv.Itoa(i+1) {
			t.Errorf("element %d read again: %s %v", i, raw, err)
		}
	}
	if err := eachItem([]string{write("e.json", `{"not":"a list"}`)}, func(json.RawMessage, itemAt) error { return nil }); err == nil {
		t.Error("an object read as a list")
	}
	// Listings are chunks of a directory, or the single file of earlier extractions.
	w := Workdir{Root: dir}
	_ = os.MkdirAll(filepath.Join(dir, "chunks"), 0o750)
	write("chunks/00001.json", "[]")
	write("chunks/00000.json", "[]")
	write("chunks/00002.json.tmp", "[")
	write("single.json", "[]")
	if got := w.listingFiles("chunks"); len(got) != 2 || !strings.HasSuffix(got[0], "00000.json") {
		t.Errorf("chunks: %v", got)
	}
	if got := w.listingFiles("single"); len(got) != 1 {
		t.Errorf("single file: %v", got)
	}
	if got := w.listingFiles("none"); got != nil {
		t.Errorf("no listing: %v", got)
	}
}
