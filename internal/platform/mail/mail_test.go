// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package mail_test

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	kmail "kogge.de/kogge/internal/platform/mail"
	"kogge.de/kogge/internal/testutil/smtptest"
)

func TestSend(t *testing.T) {
	srv := smtptest.New(t)
	s, err := kmail.New(kmail.Config{Host: srv.Host, Port: srv.Port, TLS: "none", From: "Kogge <kogge@example.com>", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(context.Background(), kmail.Message{
		To:        mail.Address{Name: "Lena Müller", Address: "lena@example.com"},
		Subject:   "[Kogge] Größe prüfen\r\nBcc: evil@example.com",
		Text:      "Hallo Lena,\n.\nzwei Zeilen – mit Umlauten: äöü.",
		HTML:      "<p>Hallo <b>Lena</b></p>",
		InReplyTo: "issue-12@example.com",
		References: []string{
			"issue-12@example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := srv.Wait(1, 5*time.Second)
	if len(msgs) != 1 || msgs[0].From != "kogge@example.com" || msgs[0].To[0] != "lena@example.com" {
		t.Fatalf("messages = %+v", msgs)
	}
	m, err := msgs[0].Parsed()
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	subject, _ := dec.DecodeHeader(m.Header.Get("Subject"))
	if subject != "[Kogge] Größe prüfen Bcc: evil@example.com" || m.Header.Get("Bcc") != "" {
		t.Errorf("subject = %q (header injection must be impossible)", subject)
	}
	if m.Header.Get("In-Reply-To") != "<issue-12@example.com>" || !strings.HasSuffix(m.Header.Get("Message-Id"), "@example.com>") ||
		m.Header.Get("Auto-Submitted") != "auto-generated" {
		t.Errorf("headers = %v", m.Header)
	}
	to, _ := dec.DecodeHeader(m.Header.Get("To"))
	if !strings.Contains(to, "Lena Müller") {
		t.Errorf("to = %q", to)
	}
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextPart() // decodes quoted-printable
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(b))
	}
	if len(parts) != 2 || !strings.Contains(parts[0], "text/plain") || !strings.Contains(parts[0], "Umlauten: äöü.") ||
		!strings.Contains(parts[0], "\r\n.\r\n") || !strings.Contains(parts[1], "<b>Lena</b>") {
		t.Errorf("parts = %q", parts)
	}
}

func TestConfig(t *testing.T) {
	if _, err := kmail.New(kmail.Config{Host: "h", From: "not an address"}); err == nil {
		t.Error("invalid sender accepted")
	}
	if _, err := kmail.New(kmail.Config{Host: "h", From: "a@b.de", TLS: "ssl3"}); err == nil {
		t.Error("unknown TLS mode accepted")
	}
}
