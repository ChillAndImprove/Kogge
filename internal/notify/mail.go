// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package notify

import (
	"bytes"
	"context"
	"fmt"
	htmltemplate "html/template"
	"log/slog"
	nmail "net/mail"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/mail"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/settings"
)

// E-mail: notifications are mailed soon after they happen — changes of the same issue, page, pull
// request or run within two minutes become one e-mail, threaded per entity — or as hourly or daily digests, as
// each user chose per category.

const (
	quietPeriod = 2 * time.Minute  // a mail waits until its entity was quiet for this long …
	maxDelay    = 10 * time.Minute // … but not longer than this
	maxTries    = 5                // failed deliveries before a notification is no longer mailed
	digestHour  = 7                // daily digests go out at this hour of the instance time zone
)

// Modes of e-mail delivery.
var Modes = []string{"immediately", "hourly", "daily", "off"}

// Categories users choose modes for.
var Categories = []string{CategoryIssues, CategoryWiki, CategoryCode, CategoryBuilds, CategorySystem}

// Mailer delivers notifications by e-mail.
type Mailer struct {
	pool     *pgxpool.Pool
	sender   *mail.Sender
	settings *settings.Service
	public   *url.URL
	log      *slog.Logger
	now      func() time.Time
	// Visible re-checks the subjects of notifications before they are mailed.
	Visible Visibilities
}

// NewMailer creates the mailer.
func NewMailer(pool *pgxpool.Pool, sender *mail.Sender, st *settings.Service, public *url.URL, logger *slog.Logger) *Mailer {
	return &Mailer{pool: pool, sender: sender, settings: st, public: public, log: logger, now: time.Now}
}

// MailArgs mails the notifications that are due.
type MailArgs struct{}

// Kind implements river.JobArgs.
func (MailArgs) Kind() string { return "notify.mail" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (MailArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueMail} }

// DigestArgs mails the hourly and daily digests.
type DigestArgs struct{}

// Kind implements river.JobArgs.
func (DigestArgs) Kind() string { return "notify.digest" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (DigestArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueMail} }

type mailWorker struct {
	river.WorkerDefaults[MailArgs]
	m *Mailer
}

func (w *mailWorker) Work(ctx context.Context, _ *river.Job[MailArgs]) error {
	return w.m.DeliverDue(ctx)
}

type digestWorker struct {
	river.WorkerDefaults[DigestArgs]
	m *Mailer
}

func (w *digestWorker) Timeout(*river.Job[DigestArgs]) time.Duration { return 30 * time.Minute }

func (w *digestWorker) Work(ctx context.Context, _ *river.Job[DigestArgs]) error {
	return w.m.DeliverDigests(ctx)
}

// Register adds the jobs.
func (m *Mailer) Register(reg *jobs.Registry) {
	river.AddWorker(reg.Workers, &mailWorker{m: m})
	river.AddWorker(reg.Workers, &digestWorker{m: m})
	reg.Every(time.Minute, MailArgs{}, nil)
	reg.Every(time.Hour, DigestArgs{}, nil)
	reg.Every(24*time.Hour, MailLogCleanupArgs{}, nil)
	river.AddWorker(reg.Workers, &mailLogCleanupWorker{pool: m.pool})
}

// MailLogCleanupArgs deletes old entries of the mail log.
type MailLogCleanupArgs struct{}

// Kind implements river.JobArgs.
func (MailLogCleanupArgs) Kind() string { return "notify.mail-log-cleanup" }

type mailLogCleanupWorker struct {
	river.WorkerDefaults[MailLogCleanupArgs]
	pool *pgxpool.Pool
}

func (w *mailLogCleanupWorker) Work(ctx context.Context, _ *river.Job[MailLogCleanupArgs]) error {
	_, err := w.pool.Exec(ctx, "DELETE FROM mail_log WHERE created_at < now() - interval '30 days'")
	return err
}

// DeliverDue mails the notifications of users who want them immediately, one e-mail per entity.
func (m *Mailer) DeliverDue(ctx context.Context) error {
	rows, err := m.pool.Query(ctx, `SELECT n.user_id, n.entity_type, n.entity_id FROM notifications n
		LEFT JOIN notification_preferences p ON p.user_id = n.user_id AND p.category = n.category
		WHERE n.mail_state = 'pending' AND coalesce(p.mode, 'immediately') = 'immediately'
		GROUP BY n.user_id, n.entity_type, n.entity_id
		HAVING max(n.created_at) < $1 OR min(n.created_at) < $2
		ORDER BY min(n.created_at) LIMIT 200`, m.now().Add(-quietPeriod), m.now().Add(-maxDelay))
	if err != nil {
		return err
	}
	type group struct {
		UserID     int64
		EntityType string
		EntityID   string
	}
	groups, err := pgx.CollectRows(rows, pgx.RowToStructByPos[group])
	if err != nil {
		return err
	}
	for _, g := range groups {
		if err := m.deliver(ctx, g.UserID, false, "n.entity_type = $2 AND n.entity_id = $3 AND coalesce(p.mode, 'immediately') = 'immediately'",
			g.EntityType, g.EntityID); err != nil {
			return err
		}
	}
	return nil
}

// DeliverDigests mails the hourly digests, and at the digest hour also the daily ones.
func (m *Mailer) DeliverDigests(ctx context.Context) error {
	modes := []string{"hourly"}
	if st, err := m.settings.Get(ctx); err == nil {
		if loc, err := time.LoadLocation(st.DefaultTimeZone); err == nil && m.now().In(loc).Hour() == digestHour {
			modes = append(modes, "daily")
		}
	}
	rows, err := m.pool.Query(ctx, `SELECT DISTINCT n.user_id FROM notifications n
		JOIN notification_preferences p ON p.user_id = n.user_id AND p.category = n.category
		WHERE n.mail_state = 'pending' AND p.mode = ANY($1)`, modes)
	if err != nil {
		return err
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, uid := range users {
		if err := m.deliver(ctx, uid, true, "p.mode = ANY($2)", modes); err != nil {
			return err
		}
	}
	return nil
}

type recipient struct {
	Email    string
	Name     string
	Locale   string
	Verified bool
	Active   bool
}

type pendingMail struct {
	ID         int64
	Category   string
	Event      string
	EntityType string
	EntityID   string
	Actor      string
	Title      string
	Summary    string
	URL        string
	Created    time.Time
}

// deliver mails the pending notifications of a user that match the condition (on n and p, arguments from $2).
func (m *Mailer) deliver(ctx context.Context, userID int64, digest bool, cond string, args ...any) error {
	var r recipient
	err := m.pool.QueryRow(ctx, `SELECT coalesce(email, ''), display_name, coalesce(locale, ''), email_verified, status = 'active'
		FROM users WHERE id = $1`, userID).Scan(&r.Email, &r.Name, &r.Locale, &r.Verified, &r.Active)
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	rows, err := m.pool.Query(ctx, `SELECT n.id, n.category, n.event, n.entity_type, n.entity_id, coalesce(a.display_name, ''), n.title,
		n.summary, n.url, n.created_at FROM notifications n LEFT JOIN users a ON a.id = n.actor_id
		LEFT JOIN notification_preferences p ON p.user_id = n.user_id AND p.category = n.category
		WHERE n.user_id = $1 AND n.mail_state = 'pending' AND `+cond+` ORDER BY n.id LIMIT 200`, append([]any{userID}, args...)...)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pendingMail])
	if err != nil || len(list) == 0 {
		return err
	}
	ids := make([]int64, len(list))
	for i, n := range list {
		ids[i] = n.ID
	}
	if !r.Active || !r.Verified || r.Email == "" {
		_, err := m.pool.Exec(ctx, "UPDATE notifications SET mail_state = 'skipped' WHERE id = ANY($1)", ids)
		return err
	}
	// Digests are mailed hours after their notifications were written: what the user may no longer see is left
	// out (audit L37).
	if list, ids, err = m.stillVisible(ctx, userID, list, ids); err != nil || len(list) == 0 {
		return err
	}
	msg, err := m.compose(ctx, r, list, digest)
	if err != nil {
		return err
	}
	err = m.sender.Send(ctx, msg)
	metrics.Mail(err)
	if err != nil {
		m.log.Warn("e-mail not delivered", "user", userID, "error", err)
		_, _ = m.pool.Exec(ctx, "INSERT INTO mail_log (user_id, address, subject, state, error) VALUES ($1, $2, $3, 'failed', $4)",
			userID, r.Email, msg.Subject, truncate(err.Error(), 1000))
		_, err := m.pool.Exec(ctx, `UPDATE notifications SET mail_tries = mail_tries + 1,
			mail_state = CASE WHEN mail_tries + 1 >= $2 THEN 'skipped' ELSE mail_state END WHERE id = ANY($1)`, ids, maxTries)
		return err
	}
	if _, err := m.pool.Exec(ctx, "UPDATE notifications SET mail_state = 'sent' WHERE id = ANY($1)", ids); err != nil {
		return err
	}
	_, err = m.pool.Exec(ctx, "INSERT INTO mail_log (user_id, address, subject, state) VALUES ($1, $2, $3, 'sent')", userID, r.Email, msg.Subject)
	return err
}

// stillVisible drops (and marks skipped) the pending mails about subjects the user may no longer see.
func (m *Mailer) stillVisible(ctx context.Context, userID int64, list []pendingMail, ids []int64) ([]pendingMail, []int64, error) {
	if len(m.Visible) == 0 {
		return list, ids, nil
	}
	p, err := identity.LoadPrincipal(ctx, m.pool, userID, identity.MethodInternal)
	if err != nil {
		return nil, nil, err
	}
	subjects := make([]subject, len(list))
	for i, n := range list {
		subjects[i] = subject{n.EntityType, n.EntityID}
	}
	seen, err := m.Visible.visible(ctx, p, subjects)
	if err != nil {
		return nil, nil, err
	}
	var keep []pendingMail
	var keepIDs, drop []int64
	for i, n := range list {
		if seen[subjects[i]] {
			keep, keepIDs = append(keep, n), append(keepIDs, n.ID)
		} else {
			drop = append(drop, n.ID)
		}
	}
	if len(drop) > 0 {
		if _, err := m.pool.Exec(ctx, "UPDATE notifications SET mail_state = 'skipped' WHERE id = ANY($1)", drop); err != nil {
			return nil, nil, err
		}
	}
	return keep, keepIDs, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

var threadUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// compose renders an e-mail about one entity (threaded with the earlier ones about it) or a digest.
func (m *Mailer) compose(ctx context.Context, r recipient, list []pendingMail, digest bool) (mail.Message, error) {
	st, _ := m.settings.Get(ctx)
	lang := r.Locale
	if lang != "de" && lang != "en" {
		lang = st.DefaultLocale
	}
	if lang != "de" {
		lang = "en"
	}
	tr := texts[lang]
	base := strings.TrimRight(m.public.String(), "/")
	data := mailData{Instance: st.Name, Greeting: fmt.Sprintf(tr["greeting"], r.Name), Footer: tr["footer"],
		Settings: base + "/profile?tab=notifications", SettingsLabel: tr["settings"], Open: tr["open"]}
	for _, n := range list {
		phrase, ok := tr[n.Event]
		if !ok {
			phrase = n.Event
		}
		line := phrase
		if n.Actor != "" && !strings.HasPrefix(phrase, "!") {
			line = n.Actor + " " + phrase
		}
		line = strings.TrimPrefix(line, "!")
		link := n.URL
		if strings.HasPrefix(link, "/") {
			link = base + link
		}
		data.Items = append(data.Items, mailItem{Title: n.Title, Line: line, Summary: n.Summary, Link: link,
			When: n.Created.In(location(st.DefaultTimeZone)).Format("02.01.2006 15:04")})
	}
	msg := mail.Message{To: nmail.Address{Name: r.Name, Address: r.Email}, Headers: map[string]string{"X-Kogge-Category": list[0].Category}}
	if digest {
		data.Heading = fmt.Sprintf(tr["digest"], len(list))
		msg.Subject = fmt.Sprintf("[%s] %s", st.Name, data.Heading)
	} else {
		last := list[len(list)-1]
		data.Heading = last.Title
		msg.Subject = fmt.Sprintf("[%s] %s", st.Name, last.Title)
		thread := threadUnsafe.ReplaceAllString(last.EntityType+"."+last.EntityID, "-") + "@" + m.sender.Domain()
		msg.InReplyTo, msg.References = thread, []string{thread}
	}
	var text, html bytes.Buffer
	if err := textMail.Execute(&text, data); err != nil {
		return msg, err
	}
	if err := htmlMail.Execute(&html, data); err != nil {
		return msg, err
	}
	msg.Text, msg.HTML = text.String(), html.String()
	return msg, nil
}

func location(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

type mailItem struct {
	Title, Line, Summary, Link, When string
}

type mailData struct {
	Instance, Heading, Greeting, Footer, Settings, SettingsLabel, Open string
	Items                                                              []mailItem
}

var textMail = template.Must(template.New("text").Parse(`{{.Greeting}}
{{range .Items}}
{{.Title}}
{{.Line}} · {{.When}}
{{if .Summary}}{{.Summary}}
{{end}}{{$.Open}}: {{.Link}}
{{end}}
--
{{.Footer}}
{{.SettingsLabel}}: {{.Settings}}
`))

// No external resources, no tracking: inline styles only.
var htmlMail = htmltemplate.Must(htmltemplate.New("html").Parse(`<!DOCTYPE html>
<html><body style="margin:0;padding:24px;background:#f4f6f8;font-family:Segoe UI,Helvetica,Arial,sans-serif;color:#1f2933">
<div style="max-width:640px;margin:0 auto;background:#ffffff;border:1px solid #dde3e8;border-radius:8px;padding:24px">
<div style="font-size:12px;color:#6b7785;margin-bottom:8px">{{.Instance}}</div>
<div style="font-size:18px;font-weight:600;margin-bottom:16px">{{.Heading}}</div>
<p style="margin:0 0 16px 0">{{.Greeting}}</p>
{{range .Items}}<div style="border-top:1px solid #e6eaee;padding:12px 0">
<div style="font-weight:600"><a href="{{.Link}}" style="color:#0b6e75;text-decoration:none">{{.Title}}</a></div>
<div style="font-size:13px;color:#4a5562;margin-top:2px">{{.Line}} · {{.When}}</div>
{{if .Summary}}<div style="font-size:14px;margin-top:6px;white-space:pre-wrap">{{.Summary}}</div>{{end}}
<div style="margin-top:8px"><a href="{{.Link}}" style="color:#0b6e75">{{$.Open}}</a></div>
</div>{{end}}
<div style="border-top:1px solid #e6eaee;padding-top:12px;margin-top:8px;font-size:12px;color:#6b7785">{{.Footer}}
<a href="{{.Settings}}" style="color:#6b7785">{{.SettingsLabel}}</a></div>
</div></body></html>
`))

// texts of the e-mails. Event phrases follow the actor's name; phrases starting with ! are sentences of their own.
var texts = map[string]map[string]string{
	"en": {
		"greeting": "Hello %s,",
		"footer":   "You receive this e-mail because of your notification settings.",
		"settings": "Change notification settings",
		"open":     "Open",
		"digest":   "Your notifications (%d)",

		"issue.created":        "created the issue",
		"issue.updated":        "changed the issue",
		"issue.assigned":       "assigned the issue",
		"issue.transitioned":   "changed the status",
		"issue.resolved":       "resolved the issue",
		"issue.commented":      "commented",
		"issue.comment-edited": "edited a comment",
		"issue.work-logged":    "logged work",
		"issue.deleted":        "deleted the issue",
		"issue.moved":          "moved the issue",

		"issue.bulk-edited":       "changed several issues at once",
		"issue.bulk-transitioned": "changed the status of several issues at once",
		"issue.bulk-moved":        "moved several issues to another project",

		"page.created":   "created the page",
		"page.updated":   "updated the page",
		"page.commented": "commented on the page",
		"blog.created":   "published a blog post",

		"pull-request.opened":               "opened the pull request",
		"pull-request.commented":            "commented on the pull request",
		"pull-request.merged":               "merged the pull request",
		"pull-request.declined":             "declined the pull request",
		"pull-request.reopened":             "reopened the pull request",
		"pull-request.rescoped":             "pushed new commits",
		"pull-request.reviewer-added":       "asks you for a review",
		"pull-request.code-owner":           "asks you to review files you own",
		"pull-request.reviewed":             "reviewed the pull request",
		"pull-request.auto-merge-enabled":   "turned on auto-merge: the pull request merges as soon as every merge check passes",
		"pull-request.auto-merge-cancelled": "!Auto-merge is off; the pull request page says why.",
		"pull-request.auto-merge-blocked":   "!Auto-merge waits for someone to act: the pull request page says what blocks it.",
		"pull-request.auto-merged":          "!Auto-merge merged the pull request.",

		"run.failed":          "!The run failed.",
		"run.fixed":           "!The run succeeded again.",
		"deployment.approval": "asks you to approve the deployment",
	},
	"de": {
		"greeting": "Hallo %s,",
		"footer":   "Sie erhalten diese E-Mail wegen Ihrer Benachrichtigungseinstellungen.",
		"settings": "Benachrichtigungseinstellungen ändern",
		"open":     "Öffnen",
		"digest":   "Ihre Benachrichtigungen (%d)",

		"issue.created":        "hat den Vorgang angelegt",
		"issue.updated":        "hat den Vorgang geändert",
		"issue.assigned":       "hat den Vorgang zugewiesen",
		"issue.transitioned":   "hat den Status geändert",
		"issue.resolved":       "hat den Vorgang erledigt",
		"issue.commented":      "hat kommentiert",
		"issue.comment-edited": "hat einen Kommentar bearbeitet",
		"issue.work-logged":    "hat Arbeit erfasst",
		"issue.deleted":        "hat den Vorgang gelöscht",
		"issue.moved":          "hat den Vorgang verschoben",

		"issue.bulk-edited":       "hat mehrere Vorgänge auf einmal geändert",
		"issue.bulk-transitioned": "hat den Status mehrerer Vorgänge auf einmal geändert",
		"issue.bulk-moved":        "hat mehrere Vorgänge in ein anderes Projekt verschoben",

		"page.created":   "hat die Seite angelegt",
		"page.updated":   "hat die Seite geändert",
		"page.commented": "hat die Seite kommentiert",
		"blog.created":   "hat einen Blogbeitrag veröffentlicht",

		"pull-request.opened":               "hat den Pull-Request geöffnet",
		"pull-request.commented":            "hat den Pull-Request kommentiert",
		"pull-request.merged":               "hat den Pull-Request gemergt",
		"pull-request.declined":             "hat den Pull-Request abgelehnt",
		"pull-request.reopened":             "hat den Pull-Request wieder geöffnet",
		"pull-request.rescoped":             "hat neue Commits gepusht",
		"pull-request.reviewer-added":       "bittet Sie um ein Review",
		"pull-request.code-owner":           "bittet Sie um ein Review von Dateien, für die Sie verantwortlich sind",
		"pull-request.reviewed":             "hat den Pull-Request begutachtet",
		"pull-request.auto-merge-enabled":   "hat Auto-Merge eingeschaltet: Der Pull-Request wird gemergt, sobald alle Merge-Prüfungen erfüllt sind",
		"pull-request.auto-merge-cancelled": "!Auto-Merge ist ausgeschaltet; die Seite des Pull-Requests nennt den Grund.",
		"pull-request.auto-merge-blocked":   "!Auto-Merge wartet darauf, dass jemand handelt: Die Seite des Pull-Requests nennt, was es aufhält.",
		"pull-request.auto-merged":          "!Auto-Merge hat den Pull-Request gemergt.",

		"run.failed":          "!Der Lauf ist fehlgeschlagen.",
		"run.fixed":           "!Der Lauf ist wieder erfolgreich.",
		"deployment.approval": "bittet Sie, das Deployment freizugeben",
	},
}
