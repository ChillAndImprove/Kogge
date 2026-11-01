// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// SinkConfig describes where forwarded audit events go.
type SinkConfig struct {
	// URL is the target:
	//
	//	file:///PATH                         JSON lines appended to a file
	//	tcp://HOST:PORT, tls://HOST:PORT     JSON lines over a TCP connection
	//	syslog+udp://HOST[:514], syslog+tcp://HOST[:514], syslog+tls://HOST[:6514]
	//	                                     syslog messages (RFC 5424) whose message is the JSON object
	//	http://URL, https://URL              batches of JSON lines, posted as application/x-ndjson
	URL *url.URL
	// Authorization is the value of the Authorization header of HTTP requests, e.g. "Bearer TOKEN".
	Authorization string
	// CAFile holds the certificates of the authorities that sign the target's certificate, used instead of the
	// system's. CertFile and KeyFile are a client certificate (mutual TLS); they are read again for every
	// connection, so that a renewed certificate needs no restart.
	CAFile, CertFile, KeyFile string
	// Hostname is the HOSTNAME of syslog messages: the host of the public URL.
	Hostname  string
	UserAgent string
}

// Line is a forwarded event: its JSON object (without a newline), and its time and action for syslog headers.
type Line struct {
	JSON   []byte
	At     time.Time
	Action string
}

// Sink delivers forwarded events.
type Sink interface {
	// Send delivers the events in their order. nil means that all of them were delivered — as far as the transport
	// tells: HTTP confirms receipt, a TCP connection only that the data left.
	Send(ctx context.Context, lines []Line) error
	Close() error
}

// TargetName is a target as shown to administrators and in logs: without credentials and query.
func TargetName(u *url.URL) string {
	if u == nil {
		return ""
	}
	v := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return v.String()
}

// NewSink prepares the target of c. Connections are made when events are sent.
func NewSink(c SinkConfig) (Sink, error) {
	u := c.URL
	if u == nil {
		return nil, errors.New("audit forwarding: no target")
	}
	switch u.Scheme {
	case "file":
		return &fileSink{path: u.Path}, nil
	case "http", "https":
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if u.Scheme == "https" {
			tc, err := c.tlsConfig("")
			if err != nil {
				return nil, err
			}
			tr.TLSClientConfig = tc
		}
		return &httpSink{url: u.String(), authorization: c.Authorization, userAgent: c.UserAgent, client: &http.Client{
			Transport: tr, Timeout: time.Minute,
			// A redirect is an error: the events would go elsewhere, and the body would have to be sent again.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}}, nil
	case "tcp", "tls", "syslog+tcp", "syslog+tls":
		s := &streamSink{addr: hostPort(u, "514"), syslog: strings.HasPrefix(u.Scheme, "syslog+"), hostname: c.Hostname}
		if u.Scheme == "syslog+tls" {
			s.addr = hostPort(u, "6514")
		}
		if u.Scheme == "tls" || u.Scheme == "syslog+tls" {
			tc, err := c.tlsConfig(u.Hostname())
			if err != nil {
				return nil, err
			}
			s.tls = tc
		}
		return s, nil
	case "syslog+udp":
		return &udpSink{addr: hostPort(u, "514"), hostname: c.Hostname}, nil
	}
	return nil, fmt.Errorf("audit forwarding: unknown kind of target %q", u.Scheme)
}

func hostPort(u *url.URL, port string) string {
	if p := u.Port(); p != "" {
		port = p
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func (c SinkConfig) tlsConfig(serverName string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("audit forwarding: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("audit forwarding: %s holds no PEM certificates", c.CAFile)
		}
		cfg.RootCAs = pool
	}
	if c.CertFile != "" {
		// Read now, so that a mistake shows at start.
		if _, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile); err != nil {
			return nil, fmt.Errorf("audit forwarding: client certificate: %w", err)
		}
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		}
	}
	return cfg, nil
}

// syslogPriority is the facility "log audit" (13) with the severity "notice" (5).
const syslogPriority = 13*8 + 5

// syslogMessage formats a line as a syslog message (RFC 5424): the action is the MSGID, the JSON object the MSG.
func syslogMessage(hostname string, l Line) []byte {
	b := make([]byte, 0, len(l.JSON)+128)
	b = fmt.Appendf(b, "<%d>1 %s %s kogge - %s - ", syslogPriority, l.At.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		headerField(hostname, 255), headerField(l.Action, 32))
	return append(b, l.JSON...)
}

// headerField makes s a field of a syslog header: printable US-ASCII without spaces, at most n characters, "-" when
// empty.
func headerField(s string, n int) string {
	b := []byte(s)
	if len(b) > n {
		b = b[:n]
	}
	for i, c := range b {
		if c < 33 || c > 126 {
			b[i] = '_'
		}
	}
	if len(b) == 0 {
		return "-"
	}
	return string(b)
}

// fileSink appends JSON lines to a file. The file is opened for every batch, so that it can be rotated by
// renaming; a batch that cannot be written completely is removed again, so that no partial line remains.
type fileSink struct{ path string }

func (s *fileSink) Send(_ context.Context, lines []Line) error {
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		return err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err == nil {
		w := bufio.NewWriterSize(f, 64<<10)
		for _, l := range lines {
			_, _ = w.Write(l.JSON)
			_ = w.WriteByte('\n')
		}
		if err = w.Flush(); err == nil {
			err = f.Sync()
		}
		if err != nil {
			_ = f.Truncate(size)
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *fileSink) Close() error { return nil }

// streamSink writes JSON lines, or syslog messages with octet counting (RFC 6587, RFC 5425), to a TCP connection,
// which it keeps open between batches.
type streamSink struct {
	addr     string
	tls      *tls.Config
	syslog   bool
	hostname string
	conn     net.Conn
}

func (s *streamSink) Send(ctx context.Context, lines []Line) error {
	if s.conn != nil && !alive(s.conn) {
		_ = s.Close()
	}
	if s.conn == nil {
		d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		var err error
		if s.tls != nil {
			s.conn, err = (&tls.Dialer{NetDialer: d, Config: s.tls}).DialContext(ctx, "tcp", s.addr)
		} else {
			s.conn, err = d.DialContext(ctx, "tcp", s.addr)
		}
		if err != nil {
			s.conn = nil
			return err
		}
	}
	conn := s.conn
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()) })
	defer stop()
	w := bufio.NewWriterSize(conn, 64<<10)
	for _, l := range lines {
		if s.syslog {
			msg := syslogMessage(s.hostname, l)
			_, _ = w.WriteString(strconv.Itoa(len(msg)))
			_ = w.WriteByte(' ')
			_, _ = w.Write(msg)
		} else {
			_, _ = w.Write(l.JSON)
			_ = w.WriteByte('\n')
		}
	}
	if err := w.Flush(); err != nil {
		_ = s.Close()
		return err
	}
	return nil
}

func (s *streamSink) Close() error {
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

// alive tells whether the receiver still keeps a connection open. Receivers send nothing, so a read that times out
// means open; a closed connection reads EOF. Writing to it instead would succeed once and lose that batch.
func alive(c net.Conn) bool {
	_ = c.SetReadDeadline(time.Now().Add(time.Millisecond))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	var b [1]byte
	_, err := c.Read(b[:])
	var ne net.Error
	return err == nil || (errors.As(err, &ne) && ne.Timeout())
}

// udpSink sends one syslog message per datagram (RFC 5426). UDP confirms nothing: messages can be lost.
type udpSink struct {
	addr     string
	hostname string
	conn     net.Conn
}

func (s *udpSink) Send(ctx context.Context, lines []Line) error {
	if s.conn == nil {
		c, err := (&net.Dialer{}).DialContext(ctx, "udp", s.addr)
		if err != nil {
			return err
		}
		s.conn = c
	}
	for _, l := range lines {
		if _, err := s.conn.Write(syslogMessage(s.hostname, l)); err != nil {
			_ = s.Close()
			return err
		}
	}
	return nil
}

func (s *udpSink) Close() error {
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

// httpSink posts each batch as one request of JSON lines; any 2xx answer confirms it.
type httpSink struct {
	url           string
	authorization string
	userAgent     string
	client        *http.Client
}

func (s *httpSink) Send(ctx context.Context, lines []Line) error {
	var body bytes.Buffer
	for _, l := range lines {
		body.Write(l.JSON)
		body.WriteByte('\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("User-Agent", s.userAgent)
	if s.authorization != "" {
		req.Header.Set("Authorization", s.authorization)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// The error without the URL, which may carry credentials.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		text := []rune(strings.Join(strings.Fields(string(answer)), " "))
		if len(text) > 200 {
			text = append(text[:200], '…')
		}
		if len(text) > 0 {
			return fmt.Errorf("the target answered %s: %s", resp.Status, string(text))
		}
		return fmt.Errorf("the target answered %s", resp.Status)
	}
	return nil
}

func (s *httpSink) Close() error {
	s.client.CloseIdleConnections()
	return nil
}
