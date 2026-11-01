// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package smtptest is an SMTP server for tests: it accepts every message (without TLS) and keeps it.
package smtptest

import (
	"bufio"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Message is a received message.
type Message struct {
	From string
	To   []string
	Data string
}

// Header returns the parsed message.
func (m Message) Parsed() (*mail.Message, error) {
	return mail.ReadMessage(strings.NewReader(m.Data))
}

// Server is a running test server.
type Server struct {
	Host string
	Port int

	ln       net.Listener
	mu       sync.Mutex
	messages []Message
	arrived  chan struct{}
}

// New starts a server on a free local port; it stops when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &Server{Host: "127.0.0.1", Port: addr.Port, ln: ln, arrived: make(chan struct{}, 1000)}
	go s.accept()
	t.Cleanup(func() { ln.Close() })
	return s
}

// Messages returns the messages received so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Wait waits until n messages arrived or the timeout passed, and returns the messages.
func (s *Server) Wait(n int, timeout time.Duration) []Message {
	deadline := time.After(timeout)
	for len(s.Messages()) < n {
		select {
		case <-s.arrived:
		case <-deadline:
			return s.Messages()
		}
	}
	return s.Messages()
}

func (s *Server) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(c)
	}
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Minute))
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	reply := func(code int, text string) {
		w.WriteString(strconv.Itoa(code) + " " + text + "\r\n")
		w.Flush()
	}
	reply(220, "smtptest ready")
	var msg Message
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			w.WriteString("250-smtptest\r\n250-AUTH PLAIN\r\n250 8BITMIME\r\n")
			w.Flush()
		case strings.HasPrefix(cmd, "HELO"):
			reply(250, "smtptest")
		case strings.HasPrefix(cmd, "AUTH"):
			reply(235, "authenticated")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			msg = Message{From: address(line[10:])}
			reply(250, "ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			msg.To = append(msg.To, address(line[8:]))
			reply(250, "ok")
		case cmd == "DATA":
			reply(354, "go ahead")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" || l == ".\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			msg.Data = data.String()
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.mu.Unlock()
			select {
			case s.arrived <- struct{}{}:
			default:
			}
			reply(250, "queued")
		case cmd == "RSET" || cmd == "NOOP":
			reply(250, "ok")
		case cmd == "QUIT":
			reply(221, "bye")
			return
		default:
			reply(502, "not implemented")
		}
	}
}

// address reads "<a@b> PARAM=…" from MAIL FROM and RCPT TO.
func address(arg string) string {
	arg = strings.TrimSpace(arg)
	if i := strings.IndexByte(arg, '<'); i >= 0 {
		if j := strings.IndexByte(arg[i:], '>'); j > 0 {
			return arg[i+1 : i+j]
		}
	}
	return strings.Fields(arg)[0]
}
