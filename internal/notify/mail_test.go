// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package notify_test

import (
	"context"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/smtptest"
)

func TestMailDelivery(t *testing.T) {
	smtp := smtptest.New(t)
	env := apitest.New(t, func(c *config.Config) {
		c.SMTP = config.SMTP{Host: smtp.Host, Port: smtp.Port, From: "Kogge <kogge@example.com>", TLS: "none"}
	})
	// This test is about composing and scheduling e-mails; its subjects are made up, so the re-check of who may see
	// them (TestNotificationsFollowAccess) is off.
	env.App.Mailer.Visible = nil
	ctx := context.Background()
	max := env.CreateUser("max")
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	max.Get("/api/v1/me", &me)
	uid := me.User.ID
	if _, err := env.App.Pool.Exec(ctx, "UPDATE users SET locale = 'de' WHERE id = $1", uid); err != nil {
		t.Fatal(err)
	}
	create := func(n notify.Notification) {
		t.Helper()
		if err := db.InTx(ctx, env.App.Pool, func(tx pgx.Tx) error { return notify.Create(ctx, tx, []int64{uid}, n) }); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args river.JobArgs) {
		t.Helper()
		if _, err := env.App.Jobs.Insert(ctx, args, nil); err != nil {
			t.Fatal(err)
		}
		env.WorkJobs()
	}
	age := func() {
		t.Helper()
		if _, err := env.App.Pool.Exec(ctx, "UPDATE notifications SET created_at = now() - interval '3 minutes'"); err != nil {
			t.Fatal(err)
		}
	}
	issue := notify.Notification{Category: notify.CategoryIssues, EntityType: "issue", EntityID: "SHOP-1", Title: "SHOP-1 Login prüfen",
		URL: "/browse/SHOP-1"}
	first, second := issue, issue
	first.Event, first.Summary = "issue.assigned", "Bitte übernehmen"
	second.Event, second.Summary = "issue.commented", "Erledigt <b>bald</b>"
	create(first)
	create(second)
	other := notify.Notification{Category: notify.CategoryWiki, Event: "page.updated", EntityType: "page", EntityID: "7", Title: "Urlaubsplan",
		URL: "/wiki/spaces/HR/pages/7"}
	create(other)

	// Not due yet: the entity is not quiet for long enough.
	run(notify.MailArgs{})
	if n := len(smtp.Messages()); n != 0 {
		t.Fatalf("mailed too early: %d", n)
	}
	age()
	run(notify.MailArgs{})
	msgs := smtp.Wait(2, 5*time.Second)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d", len(msgs))
	}
	dec := new(mime.WordDecoder)
	var issueMail string
	for _, m := range msgs {
		parsed, err := m.Parsed()
		if err != nil {
			t.Fatal(err)
		}
		subject, _ := dec.DecodeHeader(parsed.Header.Get("Subject"))
		if subject == "[Kogge] SHOP-1 Login prüfen" {
			issueMail = m.Data
			if parsed.Header.Get("In-Reply-To") != "<issue.SHOP-1@example.com>" || m.To[0] != "max@example.com" {
				t.Errorf("issue mail headers = %v", parsed.Header)
			}
		} else if subject != "[Kogge] Urlaubsplan" {
			t.Errorf("unexpected subject %q", subject)
		}
	}
	// Both changes of the issue in one German e-mail; HTML in summaries is escaped.
	for _, want := range []string{"hat den Vorgang zugewiesen", "hat kommentiert", "Erledigt &lt;b&gt;bald&lt;/b&gt;", "/browse/SHOP-1"} {
		if !strings.Contains(decodeQP(issueMail), want) {
			t.Errorf("issue mail lacks %q:\n%s", want, decodeQP(issueMail))
		}
	}
	run(notify.MailArgs{})
	if n := len(smtp.Messages()); n != 2 {
		t.Errorf("mailed twice: %d", n)
	}

	// Preferences: builds hourly, wiki off.
	max.MustDo(http.MethodPut, "/api/v1/me/notification-preferences", []map[string]string{
		{"category": "builds", "mode": "hourly"}, {"category": "wiki", "mode": "off"},
	}, nil)
	var prefs struct {
		Mail        bool `json:"mail"`
		Preferences []struct {
			Category string `json:"category"`
			Mode     string `json:"mode"`
		} `json:"preferences"`
	}
	max.Get("/api/v1/me/notification-preferences", &prefs)
	if !prefs.Mail || len(prefs.Preferences) != 5 || prefs.Preferences[3].Mode != "hourly" || prefs.Preferences[1].Mode != "off" {
		t.Errorf("preferences = %+v", prefs)
	}
	create(notify.Notification{Category: notify.CategoryBuilds, Event: "run.failed", EntityType: "run", EntityID: "9", Title: "SHOP/app #9 ci",
		URL: "/builds/shop/app/runs/9"})
	create(notify.Notification{Category: notify.CategoryWiki, Event: "page.updated", EntityType: "page", EntityID: "8", Title: "Off", URL: "/x"})
	age()
	run(notify.MailArgs{})
	if n := len(smtp.Messages()); n != 2 {
		t.Errorf("hourly or turned-off notifications were mailed at once: %d", n)
	}
	run(notify.DigestArgs{})
	msgs = smtp.Wait(3, 5*time.Second)
	if len(msgs) != 3 {
		t.Fatalf("digest not sent: %d messages", len(msgs))
	}
	digest := decodeQP(msgs[2].Data)
	if !strings.Contains(digest, "Ihre Benachrichtigungen (1)") || !strings.Contains(digest, "Der Lauf ist fehlgeschlagen.") || strings.Contains(digest, "Off") {
		t.Errorf("digest:\n%s", digest)
	}

	// The administrator's test e-mail.
	if _, err := env.App.Pool.Exec(ctx, "UPDATE users SET email = 'admin@example.com', email_verified = true WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	env.Admin().MustDo(http.MethodPost, "/api/v1/admin/mail/test", nil, nil)
	if n := len(smtp.Wait(4, 5*time.Second)); n != 4 {
		t.Errorf("test e-mail missing: %d", n)
	}
}

// decodeQP undoes quoted-printable soft line breaks and escapes well enough for assertions.
func decodeQP(s string) string {
	s = strings.ReplaceAll(s, "=\r\n", "")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '=' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			b.WriteByte(hexVal(s[i+1])<<4 | hexVal(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' }

func hexVal(c byte) byte {
	if c >= 'A' {
		return c - 'A' + 10
	}
	return c - '0'
}
