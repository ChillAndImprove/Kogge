// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package mail sends e-mail through the configured SMTP relay: plain text and HTML alternatives,
// encoded headers, threading headers, STARTTLS or implicit TLS. It uses only the standard library.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Config is the relay.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // "Kogge <kogge@example.com>" or an address
	TLS      string // none, starttls (default), tls
	// HeloName is the name the client announces (default: the host part of From).
	HeloName string
}

// Message is an e-mail.
type Message struct {
	To         mail.Address
	Subject    string
	Text       string
	HTML       string // optional
	MessageID  string // without angle brackets; generated when empty
	InReplyTo  string // threads messages about the same entity
	References []string
	Headers    map[string]string
}

// Sender delivers messages.
type Sender struct {
	cfg  Config
	from *mail.Address
	now  func() time.Time
}

// New checks the configuration and creates a sender.
func New(cfg Config) (*Sender, error) {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("mail: invalid sender %q: %w", cfg.From, err)
	}
	if cfg.Host == "" {
		return nil, errors.New("mail: no SMTP host")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	switch cfg.TLS {
	case "":
		cfg.TLS = "starttls"
	case "none", "starttls", "tls":
	default:
		return nil, fmt.Errorf("mail: unknown TLS mode %q (none, starttls, tls)", cfg.TLS)
	}
	if cfg.HeloName == "" {
		if _, domain, ok := strings.Cut(from.Address, "@"); ok {
			cfg.HeloName = domain
		} else {
			cfg.HeloName = "localhost"
		}
	}
	return &Sender{cfg: cfg, from: from, now: time.Now}, nil
}

// Domain is the domain of the sender address (for message ids).
func (s *Sender) Domain() string {
	_, d, _ := strings.Cut(s.from.Address, "@")
	return d
}

// Send delivers one message.
func (s *Sender) Send(ctx context.Context, m Message) error {
	data, err := s.Render(m)
	if err != nil {
		return err
	}
	return s.deliver(ctx, m.To.Address, data)
}

func (s *Sender) deliver(ctx context.Context, to string, data []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var conn net.Conn
	var err error
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	if s.cfg.TLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: connect to %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: %w", err)
	}
	defer c.Close()
	if err := c.Hello(s.cfg.HeloName); err != nil {
		return fmt.Errorf("mail: HELO: %w", err)
	}
	if s.cfg.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mail: the relay does not offer STARTTLS (set KOGGE_SMTP_TLS=none for a relay without TLS)")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("mail: STARTTLS: %w", err)
		}
	}
	if s.cfg.Username != "" {
		// net/smtp refuses PLAIN authentication over unencrypted connections except to localhost.
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("mail: authentication: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mail: RCPT TO %s: %w", to, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	return c.Quit()
}

// NewMessageID returns a unique message id at the sender's domain.
func (s *Sender) NewMessageID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@" + s.Domain()
}

// Render formats a message (RFC 5322 with MIME).
func (s *Sender) Render(m Message) ([]byte, error) {
	if m.MessageID == "" {
		m.MessageID = s.NewMessageID()
	}
	var buf bytes.Buffer
	h := textproto.MIMEHeader{}
	h.Set("From", s.from.String())
	h.Set("To", m.To.String())
	h.Set("Subject", mime.QEncoding.Encode("utf-8", cleanHeader(m.Subject)))
	h.Set("Date", s.now().Format(time.RFC1123Z))
	h.Set("Message-ID", "<"+m.MessageID+">")
	if m.InReplyTo != "" {
		h.Set("In-Reply-To", "<"+m.InReplyTo+">")
	}
	if len(m.References) > 0 {
		refs := make([]string, len(m.References))
		for i, r := range m.References {
			refs[i] = "<" + r + ">"
		}
		h.Set("References", strings.Join(refs, " "))
	}
	h.Set("MIME-Version", "1.0")
	h.Set("Auto-Submitted", "auto-generated")
	for k, v := range m.Headers {
		h.Set(k, mime.QEncoding.Encode("utf-8", cleanHeader(v)))
	}
	if m.HTML == "" {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		writeHeader(&buf, h)
		if err := writeQP(&buf, m.Text); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	mw := multipart.NewWriter(&buf)
	h.Set("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)
	var head bytes.Buffer
	writeHeader(&head, h)
	for _, part := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		if err := writeQP(w, part.body); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(head.Bytes(), buf.Bytes()...), nil
}

// cleanHeader removes line breaks, which would start new headers.
func cleanHeader(v string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(v)), " ")
}

func writeHeader(w *bytes.Buffer, h textproto.MIMEHeader) {
	order := []string{"From", "To", "Subject", "Date", "Message-Id", "In-Reply-To", "References", "Mime-Version", "Auto-Submitted"}
	done := map[string]bool{}
	for _, k := range order {
		if v := h.Get(k); v != "" {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
			done[textproto.CanonicalMIMEHeaderKey(k)] = true
		}
	}
	for k, vs := range h {
		if done[k] {
			continue
		}
		for _, v := range vs {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
	w.WriteString("\r\n")
}

func writeQP(w io.Writer, text string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return err
	}
	return qp.Close()
}
