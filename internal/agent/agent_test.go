// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"kogge.de/kogge/internal/builds/protocol"
)

func touch(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMatchFiles(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"dist/app.bin", "dist/sub/lib.so", "report.xml", "src/a_test.xml", "src/deep/b_test.xml", ".git/config", "keep.txt"} {
		touch(t, root, f, f)
	}
	cases := []struct {
		patterns []string
		want     []string
	}{
		{[]string{"dist/**"}, []string{"dist/app.bin", "dist/sub/lib.so"}},
		{[]string{"dist"}, []string{"dist/app.bin", "dist/sub/lib.so"}},
		{[]string{"./report.xml"}, []string{"report.xml"}},
		{[]string{"**/*_test.xml"}, []string{"src/a_test.xml", "src/deep/b_test.xml"}},
		{[]string{"src/*_test.xml", "keep.txt"}, []string{"keep.txt", "src/a_test.xml"}},
		{[]string{"**"}, []string{"dist/app.bin", "dist/sub/lib.so", "keep.txt", "report.xml", "src/a_test.xml", "src/deep/b_test.xml"}},
	}
	for _, c := range cases {
		got, err := matchFiles(root, c.patterns)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("matchFiles(%v) = %v, %v; want %v", c.patterns, got, err, c.want)
		}
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "a/../../b"} {
		if _, err := matchFiles(root, []string{bad}); err == nil {
			t.Errorf("pattern %q accepted", bad)
		}
	}
}

func TestZipRoundTripAndSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links")
	}
	src := t.TempDir()
	touch(t, src, "dist/app.bin", "binary")
	secret := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(secret, []byte("agent secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A job could link to files of the build host: the link is archived as a link, never followed.
	if err := os.Symlink(secret, filepath.Join(src, "dist", "leak")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app.bin", filepath.Join(src, "dist", "current")); err != nil {
		t.Fatal(err)
	}
	files, err := matchFiles(src, []string{"dist/**"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeZip(&buf, src, files); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("agent secret")) {
		t.Fatal("the archive contains the linked file")
	}
	archive := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	problems, err := extractZip(archive, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "outside of the workspace") {
		t.Errorf("problems = %v", problems)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "dist", "current")); err != nil || string(b) != "binary" {
		t.Errorf("relative link: %q, %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "dist", "leak")); err == nil {
		t.Error("the link to the host file was extracted")
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"../evil.txt", "ok/fine.txt", "/abs.txt"} {
		w, _ := zw.Create(name)
		_, _ = io.WriteString(w, "x")
	}
	_ = zw.Close()
	archive := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	dst := filepath.Join(parent, "ws")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZip(archive, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "evil.txt")); err == nil {
		t.Fatal("an entry escaped the workspace")
	}
	// Cleaned names stay inside: "../evil.txt" lands at the root of the workspace, "/abs.txt" too.
	for _, f := range []string{"ok/fine.txt", "evil.txt", "abs.txt"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(f))); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	if runtime.GOOS == "windows" {
		return
	}
	// A tar archive cannot write through a link it created before.
	outside := t.TempDir()
	var tbuf bytes.Buffer
	gz := gzip.NewWriter(&tbuf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: outside})
	_ = tw.WriteHeader(&tar.Header{Name: "link/pwned", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	ws := t.TempDir()
	problems, err := extractTarGz(&tbuf, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Fatal("the archive wrote outside of the workspace")
	}
	if len(problems) == 0 {
		t.Error("no problems reported")
	}
}

func TestTarRoundTrip(t *testing.T) {
	src := t.TempDir()
	touch(t, src, "deps/a/b.txt", "bee")
	touch(t, src, "deps/c.txt", "sea")
	files, _ := matchFiles(src, []string{"deps"})
	var buf bytes.Buffer
	if err := writeTarGz(&buf, src, files); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if problems, err := extractTarGz(&buf, dst); err != nil || len(problems) > 0 {
		t.Fatal(problems, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "deps", "a", "b.txt")); string(b) != "bee" {
		t.Errorf("content = %q", b)
	}
}

func TestCacheKey(t *testing.T) {
	cases := map[string]string{"Go Modules": "go-modules", "node_modules/v2": "node_modules-v2", "": "cache", "..x..": "x"}
	for in, want := range cases {
		if got := cacheKey(in, ""); got != want {
			t.Errorf("cacheKey(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cacheKey("deps", "abc"); got != "deps-abc" {
		t.Errorf("with hash: %q", got)
	}
	root := t.TempDir()
	touch(t, root, "go.sum", "v1")
	h1, n, _ := hashFiles(root, []string{"go.sum"})
	touch(t, root, "go.sum", "v2")
	h2, _, _ := hashFiles(root, []string{"go.sum"})
	if n != 1 || h1 == h2 || len(h1) != 16 {
		t.Errorf("hashes %q %q (%d files)", h1, h2, n)
	}
}

// logServer stores log chunks like Kogge: at an offset, refusing other offsets with 409.
type logServer struct {
	mu   sync.Mutex
	data []byte
	fail int // answer the next requests with 503
}

func (s *logServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail > 0 {
		s.fail--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	b, _ := io.ReadAll(r.Body)
	status := http.StatusOK
	if offset != int64(len(s.data)) {
		status = http.StatusConflict
	} else {
		s.data = append(s.data, b...)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.LogResponse{Size: int64(len(s.data))})
}

func TestJobLogMasksAndResumes(t *testing.T) {
	srv := &logServer{data: []byte("earlier attempt\n"), fail: 1}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	c := NewClient(ts.URL, "", "test", nil)
	jc := c.Job(&protocol.JobPayload{ID: 7, Token: "kgj_x"})
	cert := "-----BEGIN KEY-----\nAAAABBBBCCCCDDDD\n-----END KEY-----"
	l := newJobLog(jc, int64(len(srv.data)), []string{"hunter22", "abc", cert})
	l.Step("Build")
	_, _ = io.WriteString(l, "password is hun")
	_, _ = io.WriteString(l, "ter22 ok\nkey line AAAABBBBCCCCDDDD\nshort abc stays\nno newline at the end")
	l.Info("done in %d s", 3)
	l.Close()
	got := string(srv.data)
	want := "earlier attempt\n" + protocol.MarkStep + "Build\npassword is **** ok\nkey line ****\nshort abc stays\nno newline at the end\n" +
		protocol.MarkInfo + "done in 3 s\n"
	if got != want {
		t.Errorf("log =\n%q\nwant\n%q", got, want)
	}
}

// TestJobLogMasksLongLines: a secret in a line longer than the agent holds back is masked, also when the line
// arrives in pieces.
func TestJobLogMasksLongLines(t *testing.T) {
	srv := &logServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	jc := NewClient(ts.URL, "", "test", nil).Job(&protocol.JobPayload{ID: 7, Token: "kgj_x"})
	secret := strings.Repeat("s3cr3t", 2000) // longer than a held line
	l := newJobLog(jc, 0, []string{secret})
	_, _ = io.WriteString(l, strings.Repeat("x", logHoldLine-10)+secret[:5000])
	_, _ = io.WriteString(l, secret[5000:]+" end\n")
	l.Close()
	got := string(srv.data)
	if strings.Contains(got, "s3cr3ts3cr3t") || !strings.HasSuffix(got, "**** end\n") {
		t.Errorf("log (%d bytes) ends with %q", len(got), got[max(0, len(got)-60):])
	}
}

// TestJobLogMasksEncodedSecrets: secrets are masked also as programs print them encoded: in base64 with other
// text (basic authentication) and URL-encoded in addresses.
func TestJobLogMasksEncodedSecrets(t *testing.T) {
	srv := &logServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	jc := NewClient(ts.URL, "", "test", nil).Job(&protocol.JobPayload{ID: 7, Token: "kgj_x"})
	secret := "p@ss w0rd/2026!"
	l := newJobLog(jc, 0, []string{secret})
	basic := base64.StdEncoding.EncodeToString([]byte("deploy:" + secret))
	_, _ = io.WriteString(l, "> Authorization: Basic "+basic+"\n")
	_, _ = io.WriteString(l, "cloning https://deploy:"+url.QueryEscape(secret)+"@git.example.com/app.git\n")
	l.Close()
	got := string(srv.data)
	core := base64.StdEncoding.EncodeToString([]byte("deploy:" + secret))[12:24] // characters of the secret alone
	if strings.Contains(got, core) || strings.Contains(got, url.QueryEscape(secret)) || strings.Count(got, "****") != 2 {
		t.Errorf("log =\n%s", got)
	}
}

// TestUnpackLimit: archives cannot unpack to more than the limit.
func TestUnpackLimit(t *testing.T) {
	defer func(v int64) { maxUnpacked = v }(maxUnpacked)
	maxUnpacked = 1000
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "zeros"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	var tgz bytes.Buffer
	if err := writeTarGz(&tgz, src, []string{"zeros"}); err != nil {
		t.Fatal(err)
	}
	if _, err := extractTarGz(&tgz, dst); !errors.Is(err, errUnpackedTooLarge) {
		t.Errorf("tar: %v", err)
	}
	zipFile := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(zipFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeZip(f, src, []string{"zeros"}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := extractZip(zipFile, t.TempDir()); !errors.Is(err, errUnpackedTooLarge) {
		t.Errorf("zip: %v", err)
	}
}
