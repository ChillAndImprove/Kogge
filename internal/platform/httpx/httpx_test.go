// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package httpx

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		remote, xff, want string
		trusted           []netip.Prefix
	}{
		{"192.0.2.1:1234", "", "192.0.2.1", trusted},
		{"192.0.2.1:1234", "203.0.113.9", "192.0.2.1", trusted},  // untrusted peer: header ignored
		{"10.0.0.5:1234", "203.0.113.9", "203.0.113.9", trusted}, // proxy
		{"10.0.0.5:1234", "198.51.100.7, 203.0.113.9, 10.1.1.1", "203.0.113.9", trusted},
		{"10.0.0.5:1234", "203.0.113.9", "10.0.0.5", nil}, // no trusted proxies configured
		{"[::ffff:192.0.2.1]:1234", "", "192.0.2.1", nil},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := resolveClientIP(r, c.trusted).String(); got != c.want {
			t.Errorf("%s / %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

// TestUntrustedProxyWarning: X-Forwarded-For from an address that is not a trusted proxy is logged once; requests of
// the trusted proxy and requests without the header are not.
func TestUntrustedProxyWarning(t *testing.T) {
	var logs strings.Builder
	h := Base(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), slog.New(slog.NewTextHandler(&logs, nil)),
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	serve := func(remote, xff string) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	serve("10.0.0.5:1234", "203.0.113.9")
	serve("192.0.2.1:1234", "")
	if strings.Contains(logs.String(), "not a trusted proxy") {
		t.Fatalf("warned about a trusted proxy or a direct client:\n%s", logs.String())
	}
	serve("192.0.2.1:1234", "203.0.113.9")
	serve("192.0.2.2:1234", "203.0.113.9")
	if n := strings.Count(logs.String(), "not a trusted proxy"); n != 1 || !strings.Contains(logs.String(), "peer=192.0.2.1") {
		t.Errorf("%d warnings, want 1 naming the first peer:\n%s", n, logs.String())
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(time.Hour, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("event %d denied", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("burst exceeded but allowed")
	}
	if !l.Allow("b") {
		t.Fatal("keys must be independent")
	}
}

// TestLimiterFailures: Blocked and Fail limit failed attempts only.
func TestLimiterFailures(t *testing.T) {
	l := NewLimiter(time.Hour, 2)
	for i := 0; i < 5; i++ {
		if l.Blocked("ip") {
			t.Fatalf("checking blocked after %d checks", i)
		}
	}
	// Checking keys costs no memory: probes with many user names do not fill the limiter.
	if len(l.buckets) != 0 {
		t.Fatalf("checks created %d buckets", len(l.buckets))
	}
	l.Fail("ip")
	if l.Blocked("ip") {
		t.Fatal("blocked after one of two failures")
	}
	l.Fail("ip")
	if !l.Blocked("ip") || l.Blocked("other") {
		t.Fatal("two failures must block this key only")
	}
	if l.Allow("ip") {
		t.Fatal("a blocked key allowed an event")
	}
	// Refill: one event per interval.
	r := NewLimiter(10*time.Millisecond, 1)
	r.Fail("k")
	if !r.Blocked("k") {
		t.Fatal("not blocked after the failure")
	}
	time.Sleep(25 * time.Millisecond)
	if r.Blocked("k") {
		t.Fatal("still blocked after the interval")
	}
}

// TestFileHeaders: uploaded files are served with types that cannot run in Kogge's pages.
func TestFileHeaders(t *testing.T) {
	for _, c := range []struct {
		uploaded, typ, disposition string
		download                   bool
	}{
		{"image/png", "image/png", "inline", false},
		{"IMAGE/PNG", "image/png", "inline", false},
		{"image/png", "image/png", "attachment", true},
		{"text/html", "text/plain; charset=utf-8", "inline", false},
		{"application/javascript", "application/octet-stream", "attachment", false},
		{"text/css", "text/plain; charset=utf-8", "inline", false},
		{"image/svg+xml", "application/octet-stream", "attachment", false},
		{"application/pdf; charset=x", "application/pdf", "inline", false},
		{"nonsense", "application/octet-stream", "attachment", false},
	} {
		w := httptest.NewRecorder()
		FileHeaders(w, c.uploaded, "f.bin", c.download)
		if got := w.Header().Get("Content-Type"); got != c.typ {
			t.Errorf("%s: type %q", c.uploaded, got)
		}
		if got := w.Header().Get("Content-Disposition"); !strings.HasPrefix(got, c.disposition) {
			t.Errorf("%s: disposition %q", c.uploaded, got)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != SandboxCSP {
			t.Errorf("%s: headers %v", c.uploaded, w.Header())
		}
	}
}
