// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package audit_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kogge.de/kogge/internal/platform/audit"
)

var at = time.Date(2026, 10, 7, 9, 30, 5, 123456000, time.FixedZone("CEST", 2*3600))

func lines(actions ...string) []audit.Line {
	var out []audit.Line
	for i, a := range actions {
		out = append(out, audit.Line{JSON: []byte(`{"id":` + strconv.Itoa(i+1) + `,"action":"` + a + `"}`), At: at, Action: a})
	}
	return out
}

func sink(t *testing.T, c audit.SinkConfig) audit.Sink {
	t.Helper()
	s, err := audit.NewSink(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func target(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestFileSink: lines are appended; a file renamed by a log rotation is followed by a new one.
func TestFileSink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	s := sink(t, audit.SinkConfig{URL: &url.URL{Scheme: "file", Path: path}})
	ctx := t.Context()
	if err := s.Send(ctx, lines("a", "b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, lines("c")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != `{"id":1,"action":"a"}`+"\n"+`{"id":2,"action":"b"}`+"\n"+`{"id":1,"action":"c"}`+"\n" {
		t.Errorf("file = %q", b)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v %v", info.Mode(), err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, lines("d")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"id":1,"action":"d"}`+"\n" {
		t.Errorf("after the rotation = %q", b)
	}
	// A directory that does not exist is an error, not a loss.
	missing := sink(t, audit.SinkConfig{URL: &url.URL{Scheme: "file", Path: filepath.Join(dir, "gone", "audit.jsonl")}})
	if err := missing.Send(ctx, lines("e")); err == nil {
		t.Error("writing into a missing directory succeeded")
	}
}

// accept serves one connection at a time on l and passes each to serve.
func accept(t *testing.T, l net.Listener, serve func(net.Conn)) {
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			serve(c)
		}
	}()
}

// TestTCPSink: JSON lines over TCP; a connection the receiver closed is noticed before the next batch, which then
// arrives complete over a new connection.
func TestTCPSink(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 10)
	accept(t, l, func(c net.Conn) {
		// Every connection is closed after its first batch.
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			got <- line
			if strings.Contains(line, `"id":2`) || strings.Contains(line, `"action":"c"`) {
				break
			}
		}
		_ = c.Close()
	})
	s := sink(t, audit.SinkConfig{URL: target(t, "tcp://"+l.Addr().String())})
	ctx := t.Context()
	if err := s.Send(ctx, lines("a", "b")); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"id":1,"action":"a"}` + "\n", `{"id":2,"action":"b"}` + "\n"}
	for _, w := range want {
		if line := <-got; line != w {
			t.Errorf("line = %q, want %q", line, w)
		}
	}
	time.Sleep(100 * time.Millisecond) // the receiver's close arrives
	if err := s.Send(ctx, lines("c")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-got:
		if line != `{"id":1,"action":"c"}`+"\n" {
			t.Errorf("line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the batch after the receiver closed the connection was lost")
	}
	_ = l.Close()
	time.Sleep(100 * time.Millisecond)
	if err := s.Send(ctx, lines("d")); err == nil {
		t.Error("sending to a closed port succeeded")
	}
}

// syslogFrame reads a message with octet counting (RFC 6587).
func syslogFrame(r *bufio.Reader) (string, error) {
	n, err := r.ReadString(' ')
	if err != nil {
		return "", err
	}
	size, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil {
		return "", err
	}
	b := make([]byte, size)
	_, err = io.ReadFull(r, b)
	return string(b), err
}

// TestSyslogTCPSink: RFC 5424 messages with octet counting; the facility is "log audit", the MSGID the action.
func TestSyslogTCPSink(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 10)
	accept(t, l, func(c net.Conn) {
		r := bufio.NewReader(c)
		for {
			msg, err := syslogFrame(r)
			if err != nil {
				return
			}
			got <- msg
		}
	})
	s := sink(t, audit.SinkConfig{URL: target(t, "syslog+tcp://"+l.Addr().String()), Hostname: "kogge.example.com"})
	ls := lines("auth.login", "a very long action name that is longer than thirty-two characters")
	if err := s.Send(t.Context(), ls); err != nil {
		t.Fatal(err)
	}
	if msg := <-got; msg != `<109>1 2026-10-07T07:30:05.123456Z kogge.example.com kogge - auth.login - {"id":1,"action":"auth.login"}` {
		t.Errorf("message = %q", msg)
	}
	if msg := <-got; !strings.HasPrefix(msg, `<109>1 2026-10-07T07:30:05.123456Z kogge.example.com kogge - a_very_long_action_name_that_is_ - {`) {
		t.Errorf("message = %q", msg)
	}
}

// TestSyslogUDPSink: one message per datagram.
func TestSyslogUDPSink(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	s := sink(t, audit.SinkConfig{URL: target(t, "syslog+udp://"+pc.LocalAddr().String()), Hostname: ""})
	if err := s.Send(t.Context(), lines("a", "b")); err != nil {
		t.Fatal(err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for _, want := range []string{
		`<109>1 2026-10-07T07:30:05.123456Z - kogge - a - {"id":1,"action":"a"}`,
		`<109>1 2026-10-07T07:30:05.123456Z - kogge - b - {"id":2,"action":"b"}`,
	} {
		buf := make([]byte, 65536)
		n, _, err := pc.ReadFrom(buf)
		if err != nil || string(buf[:n]) != want {
			t.Errorf("datagram = %q, %v; want %q", buf[:n], err, want)
		}
	}
}

// pki is a certificate authority with a server and a client certificate, as PEM files.
type pki struct {
	dir                                string
	ca, serverCert, clientCert, client string
	server                             tls.Certificate
	pool                               *x509.CertPool
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	p := &pki{dir: dir, pool: x509.NewCertPool()}
	p.pool.AddCert(caCert)
	write := func(name, kind string, der []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	p.ca = write("ca.pem", "CERTIFICATE", caDER)
	issue := func(serial int64, name string, usage x509.ExtKeyUsage) ([]byte, *ecdsa.PrivateKey) {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return der, key
	}
	der, key := issue(2, "localhost", x509.ExtKeyUsageServerAuth)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	p.server, err = tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	der, key = issue(3, "kogge", x509.ExtKeyUsageClientAuth)
	keyDER, _ = x509.MarshalECPrivateKey(key)
	p.clientCert = write("client.pem", "CERTIFICATE", der)
	p.client = write("client-key.pem", "EC PRIVATE KEY", keyDER)
	return p
}

// TestTLSSinks: JSON lines and syslog over TLS with the operator's certificate authority and a client certificate
// (mutual TLS); a target with a certificate of another authority is refused.
func TestTLSSinks(t *testing.T) {
	p := newPKI(t)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{p.server},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: p.pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 10)
	accept(t, l, func(c net.Conn) {
		go func() {
			defer c.Close()
			tc := c.(*tls.Conn)
			if err := tc.Handshake(); err != nil {
				got <- "handshake: " + err.Error()
				return
			}
			got <- "client " + tc.ConnectionState().PeerCertificates[0].Subject.CommonName
			b, _ := io.ReadAll(c)
			got <- string(b)
		}()
	})
	port := l.Addr().(*net.TCPAddr).Port
	for _, scheme := range []string{"tls", "syslog+tls"} {
		s := sink(t, audit.SinkConfig{URL: target(t, scheme+"://localhost:"+strconv.Itoa(port)), CAFile: p.ca,
			CertFile: p.clientCert, KeyFile: p.client, Hostname: "kogge.example.com"})
		if err := s.Send(t.Context(), lines("a")); err != nil {
			t.Fatalf("%s: %v", scheme, err)
		}
		if msg := <-got; msg != "client kogge" {
			t.Fatalf("%s: %s", scheme, msg)
		}
		_ = s.Close()
		want := `{"id":1,"action":"a"}` + "\n"
		if scheme == "syslog+tls" {
			msg := `<109>1 2026-10-07T07:30:05.123456Z kogge.example.com kogge - a - {"id":1,"action":"a"}`
			want = strconv.Itoa(len(msg)) + " " + msg
		}
		if msg := <-got; msg != want {
			t.Errorf("%s: received %q, want %q", scheme, msg, want)
		}
	}

	// Without the client certificate, or trusting other authorities, nothing is sent.
	s := sink(t, audit.SinkConfig{URL: target(t, "tls://localhost:"+strconv.Itoa(port)), CAFile: p.ca})
	if err := s.Send(t.Context(), lines("a")); err == nil {
		// TLS 1.3 reports a missing client certificate with the first read; the receiver saw the failed handshake.
		if msg := <-got; !strings.HasPrefix(msg, "handshake:") {
			t.Errorf("without a client certificate: %s", msg)
		}
	} else {
		<-got
	}
	other := newPKI(t)
	s = sink(t, audit.SinkConfig{URL: target(t, "tls://localhost:"+strconv.Itoa(port)), CAFile: other.ca,
		CertFile: p.clientCert, KeyFile: p.client})
	if err := s.Send(t.Context(), lines("a")); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("a server of another authority: %v", err)
	}

	// Mistakes in the files show when the sink is made.
	for _, c := range []audit.SinkConfig{
		{URL: target(t, "tls://localhost:1"), CAFile: filepath.Join(p.dir, "missing.pem")},
		{URL: target(t, "tls://localhost:1"), CAFile: p.client},
		{URL: target(t, "syslog+tls://localhost"), CertFile: p.clientCert, KeyFile: p.ca},
		{URL: target(t, "https://localhost"), CAFile: p.client},
	} {
		if _, err := audit.NewSink(c); err == nil {
			t.Errorf("%+v: no error", c)
		}
	}
}

// TestHTTPSink: a batch is one POST of JSON lines with the configured authorization; answers other than 2xx and
// redirects are errors that say what the target answered, without the URL's credentials.
func TestHTTPSink(t *testing.T) {
	type request struct {
		method, path, contentType, auth, agent, body string
		user                                         string
	}
	got := make(chan request, 10)
	var status atomic.Int32
	status.Store(http.StatusAccepted)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		user, _, _ := r.BasicAuth()
		got <- request{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("Authorization"),
			r.Header.Get("User-Agent"), string(b), user}
		code := int(status.Load())
		if code == http.StatusFound {
			http.Redirect(w, r, "/elsewhere", code)
			return
		}
		w.WriteHeader(code)
		if code >= 400 {
			_, _ = io.WriteString(w, "  index   closed\n for writes ")
		}
	}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	s := sink(t, audit.SinkConfig{URL: target(t, srv.URL+"/services/collector/raw?channel=kogge"),
		Authorization: "Splunk 0000-1111", CAFile: ca, UserAgent: "Kogge/1.2.3"})
	if err := s.Send(t.Context(), lines("a", "b")); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.method != http.MethodPost || r.path != "/services/collector/raw?channel=kogge" || r.contentType != "application/x-ndjson" ||
		r.auth != "Splunk 0000-1111" || r.agent != "Kogge/1.2.3" || r.body != `{"id":1,"action":"a"}`+"\n"+`{"id":2,"action":"b"}`+"\n" {
		t.Errorf("request = %+v", r)
	}

	status.Store(http.StatusServiceUnavailable)
	if err := s.Send(t.Context(), lines("c")); err == nil || err.Error() != "the target answered 503 Service Unavailable: index closed for writes" {
		t.Errorf("503: %v", err)
	}
	<-got
	status.Store(http.StatusFound)
	if err := s.Send(t.Context(), lines("c")); err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("redirect: %v", err)
	}
	<-got
	if len(got) != 0 {
		t.Error("the redirect was followed")
	}

	// Basic authentication from the URL; errors do not repeat it.
	status.Store(http.StatusOK)
	u := target(t, srv.URL)
	u.User = url.UserPassword("kogge", "s3cret")
	basic := sink(t, audit.SinkConfig{URL: u, CAFile: ca})
	if err := basic.Send(t.Context(), lines("d")); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.user != "kogge" {
		t.Errorf("basic authentication: %+v", r)
	}
	srv.Close()
	if err := basic.Send(t.Context(), lines("e")); err == nil || strings.Contains(err.Error(), "s3cret") ||
		strings.Contains(err.Error(), "https://") {
		t.Errorf("error after the target went away: %v", err)
	}
	// The system's authorities do not know the test server.
	srv2 := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv2.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv2.StartTLS()
	defer srv2.Close()
	if err := sink(t, audit.SinkConfig{URL: target(t, srv2.URL)}).Send(t.Context(), lines("f")); err == nil {
		t.Error("an unknown certificate was accepted")
	}
}

// TestTargetName: targets are shown without credentials and queries.
func TestTargetName(t *testing.T) {
	for raw, want := range map[string]string{
		"https://user:pw@collector.example.com:8088/raw?token=x": "https://collector.example.com:8088/raw",
		"syslog+tls://siem.example.com":                          "syslog+tls://siem.example.com",
		"file:///var/log/kogge/audit.jsonl":                      "file:///var/log/kogge/audit.jsonl",
	} {
		if got := audit.TargetName(target(t, raw)); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	if audit.TargetName(nil) != "" {
		t.Error("no target has a name")
	}
}
