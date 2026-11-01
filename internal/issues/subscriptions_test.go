// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	kconfig "kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/smtptest"
)

type subscription struct {
	ID       int64  `json:"id"`
	Filter   ref    `json:"filter"`
	User     *ref   `json:"user"`
	Group    *ref   `json:"group"`
	Freq     string `json:"frequency"`
	Minute   int    `json:"minute"`
	Weekdays []int  `json:"weekdays"`
	Hours    []int  `json:"hours"`
	TimeZone string `json:"timeZone"`
	Next     time.Time
	Last     *time.Time `json:"last"`
	Manage   bool       `json:"canManage"`
	CanMute  bool       `json:"canMute"`
	Muted    bool       `json:"muted"`
}

type subscriptions struct {
	Mail          bool           `json:"mail"`
	Subscriptions []subscription `json:"subscriptions"`
}

// received is an e-mail as its recipient reads it.
type received struct {
	To, Subject, Text, HTML string
}

// inbox returns the messages after the first n, decoded.
func inbox(t *testing.T, smtp *smtptest.Server, n int) []received {
	t.Helper()
	var out []received
	dec := new(mime.WordDecoder)
	for _, m := range smtp.Messages()[n:] {
		parsed, err := m.Parsed()
		if err != nil {
			t.Fatal(err)
		}
		subject, _ := dec.DecodeHeader(parsed.Header.Get("Subject"))
		r := received{To: m.To[0], Subject: subject}
		_, params, _ := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
		parts := multipart.NewReader(parsed.Body, params["boundary"])
		for {
			part, err := parts.NextPart()
			if err != nil {
				break
			}
			body, _ := io.ReadAll(part) // decoded from quoted-printable
			if strings.HasPrefix(part.Header.Get("Content-Type"), "text/html") {
				r.HTML = string(body)
			} else {
				r.Text = string(body)
			}
		}
		out = append(out, r)
	}
	return out
}

// TestFilterSubscriptions: people subscribe to filters they see and owners subscribe groups they belong to; every
// recipient gets the issues they may see, in their language, and members of a group can stop its e-mails for
// themselves. Who no longer sees the filter receives nothing.
func TestFilterSubscriptions(t *testing.T) {
	smtp := smtptest.New(t)
	f := setup(t, func(c *kconfig.Config) {
		c.SMTP = kconfig.SMTP{Host: smtp.Host, Port: smtp.Port, From: "Kogge <kogge@example.com>", TLS: "none"}
	})
	ctx := context.Background()
	pool := f.env.App.Pool
	if _, err := pool.Exec(ctx, "UPDATE users SET email_verified = true"); err != nil {
		t.Fatal(err)
	}
	story := f.cfg.typ("story")
	f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": story, "summary": "Checkout <fast>"})
	f.create(t, f.lead, map[string]any{"project": "SHOP", "typeId": story, "summary": "Payment"})
	// A project of lena's alone: dirk never learns of its issue.
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations"}, nil)
	f.create(t, f.lead, map[string]any{"project": "OPS", "typeId": story, "summary": "Secret migration"})
	// Dirk reads German, lena English, otto the installation's language (German).
	f.dev.MustDo(http.MethodPatch, "/api/v1/me", map[string]any{"locale": "de"}, nil)
	f.lead.MustDo(http.MethodPatch, "/api/v1/me", map[string]any{"locale": "en"}, nil)

	var team, others ref
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "team"}, &team)
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "others"}, &others)
	for _, name := range []string{"lena", "dirk", "otto"} {
		f.admin.MustDo(http.MethodPut, apitest.Fmt("/api/v1/admin/groups/%d/members/%d", team.ID, f.ids[name]), nil, nil)
	}
	f.admin.MustDo(http.MethodPut, apitest.Fmt("/api/v1/admin/groups/%d/members/%d", others.ID, f.ids["ute"]), nil, nil)
	// Group membership is read at sign-in.
	f.lead, f.dev, f.other = f.env.Login("lena", apitest.UserPassword), f.env.Login("dirk", apitest.UserPassword),
		f.env.Login("otto", apitest.UserPassword)

	var filter, private ref
	f.lead.Post("/api/v1/filters", map[string]any{"name": "Open work", "query": "statusCategory != Done ORDER BY key",
		"shares": []map[string]any{{"type": "group", "id": team.ID}}}, &filter)
	f.lead.Post("/api/v1/filters", map[string]any{"name": "Private", "query": "project = SHOP"}, &private)
	subs := apitest.Fmt("/api/v1/filters/%d/subscriptions", filter.ID)

	// The owner sees with whom the filter is shared, by current names; the people it is shared with do not.
	var shared struct {
		Shares []struct {
			Type string `json:"type"`
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"shares"`
	}
	f.lead.Get(apitest.Fmt("/api/v1/filters/%d", filter.ID), &shared)
	if len(shared.Shares) != 1 || shared.Shares[0].Name != "team" {
		t.Errorf("shares for the owner: %+v", shared)
	}
	f.dev.Get(apitest.Fmt("/api/v1/filters/%d", filter.ID), &shared)
	if len(shared.Shares) != 0 {
		t.Errorf("shares for a viewer: %+v", shared)
	}

	// Lena subscribes herself on workdays at 08:30 (the installation's time zone) and the team every hour.
	var mine, group subscription
	f.lead.Post(subs, map[string]any{"frequency": "daily", "minute": 510, "weekdays": []int{5, 1, 2, 3, 4}}, &mine)
	if mine.User == nil || mine.User.ID != f.ids["lena"] || mine.TimeZone != "Europe/Berlin" || !mine.Manage ||
		!slices.Equal(mine.Weekdays, []int{1, 2, 3, 4, 5}) || !mine.Next.After(time.Now()) {
		t.Fatalf("lena's subscription: %+v", mine)
	}
	if loc, _ := time.LoadLocation("Europe/Berlin"); mine.Next.In(loc).Format("15:04") != "08:30" {
		t.Errorf("next: %s", mine.Next)
	}
	f.lead.Post(subs, map[string]any{"frequency": "hourly", "minute": 0, "weekdays": []int{1, 2, 3, 4, 5, 6, 7}, "groupId": team.ID},
		&group)
	if group.Group == nil || group.Group.Name != "team" || group.User != nil {
		t.Fatalf("group subscription: %+v", group)
	}

	for _, c := range []struct {
		who    *apitest.Client
		path   string
		body   map[string]any
		status int
		code   string
	}{
		// Only the filter's owner subscribes groups, groups they belong to, that the filter is shared with.
		{f.dev, subs, map[string]any{"frequency": "daily", "minute": 0, "weekdays": []int{1}, "groupId": team.ID}, 403, "subscription.group-denied"},
		{f.lead, subs, map[string]any{"frequency": "daily", "minute": 0, "weekdays": []int{1}, "groupId": others.ID}, 403, "subscription.group-member"},
		{f.lead, apitest.Fmt("/api/v1/filters/%d/subscriptions", private.ID), map[string]any{"frequency": "daily", "minute": 0,
			"weekdays": []int{1}, "groupId": team.ID}, 422, "subscription.not-shared"},
		// Schedules are checked.
		{f.lead, subs, map[string]any{"frequency": "hourly", "minute": 75, "weekdays": []int{1}}, 422, "subscription.minute"},
		{f.lead, subs, map[string]any{"frequency": "daily", "minute": 60}, 422, "subscription.weekdays"},
		{f.lead, subs, map[string]any{"frequency": "monthly", "minute": 60}, 422, "subscription.month-day"},
		{f.lead, subs, map[string]any{"frequency": "hourly", "minute": 5, "weekdays": []int{1}, "hours": []int{24}}, 422, "subscription.hours"},
		// Who does not see the filter cannot tell it exists.
		{f.user, subs, map[string]any{"frequency": "daily", "minute": 0, "weekdays": []int{1}}, 404, "filter.not-found"},
		// Members of the group see its subscription but do not change it.
		{f.dev, apitest.Fmt("/api/v1/filter-subscriptions/%d", group.ID), map[string]any{"frequency": "daily", "minute": 0,
			"weekdays": []int{1}}, 403, "subscription.manage-denied"},
		{f.dev, apitest.Fmt("/api/v1/filter-subscriptions/%d", mine.ID), map[string]any{"frequency": "daily", "minute": 0,
			"weekdays": []int{1}}, 404, "subscription.not-found"},
		{f.dev, apitest.Fmt("/api/v1/filter-subscriptions/%d/mute", mine.ID), nil, 404, "subscription.not-found"},
		{f.lead, apitest.Fmt("/api/v1/filter-subscriptions/%d/mute", mine.ID), nil, 422, "subscription.not-member"},
	} {
		method := http.MethodPost
		if strings.Contains(c.path, "filter-subscriptions") {
			method = http.MethodPut
		}
		if r := c.who.Do(method, c.path, c.body); r.Status != c.status || r.Code() != c.code {
			t.Errorf("%s %s as %s: %d %s, want %d %s", method, c.path, c.who.Username, r.Status, r.Body, c.status, c.code)
		}
	}
	if r := f.dev.Do(http.MethodDelete, apitest.Fmt("/api/v1/filter-subscriptions/%d", group.ID), nil); r.Code() != "subscription.manage-denied" {
		t.Errorf("dirk deleted the group subscription: %d %s", r.Status, r.Body)
	}

	// Everyone sees what they receive: dirk and otto the team's subscription, which they may mute.
	var list subscriptions
	f.dev.Get("/api/v1/filter-subscriptions", &list)
	if !list.Mail || len(list.Subscriptions) != 1 || list.Subscriptions[0].ID != group.ID || list.Subscriptions[0].Manage ||
		!list.Subscriptions[0].CanMute {
		t.Fatalf("dirk's subscriptions: %+v", list)
	}
	f.lead.Get(subs, &list)
	if len(list.Subscriptions) != 2 {
		t.Errorf("the filter's subscriptions for its owner: %+v", list)
	}
	f.user.Get("/api/v1/filter-subscriptions", &list)
	if len(list.Subscriptions) != 0 {
		t.Errorf("ute's subscriptions: %+v", list)
	}
	f.dev.MustDo(http.MethodPut, apitest.Fmt("/api/v1/filter-subscriptions/%d/mute", group.ID), nil, nil)
	f.dev.Get("/api/v1/filter-subscriptions", &list)
	if !list.Subscriptions[0].Muted {
		t.Errorf("not muted: %+v", list)
	}

	deliver := func() []received {
		t.Helper()
		before := len(smtp.Messages())
		if _, err := pool.Exec(ctx, "UPDATE filter_subscriptions SET next_run_at = now() - interval '1 minute'"); err != nil {
			t.Fatal(err)
		}
		if err := f.env.App.Issues.DeliverSubscriptions(ctx); err != nil {
			t.Fatal(err)
		}
		var due int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM filter_subscriptions WHERE next_run_at <= now()").Scan(&due); err != nil || due != 0 {
			t.Errorf("still due: %d %v", due, err)
		}
		return inbox(t, smtp, before)
	}
	byRecipient := func(mails []received) map[string][]received {
		out := map[string][]received{}
		for _, m := range mails {
			out[m.To] = append(out[m.To], m)
		}
		return out
	}

	// Lena gets both e-mails; dirk muted the team's, otto sees no issue of the filter (and empty results are not sent).
	got := byRecipient(deliver())
	if len(got) != 1 || len(got["lena@example.com"]) != 2 {
		t.Fatalf("first delivery: %+v", got)
	}
	m := got["lena@example.com"][0]
	if m.Subject != "[Kogge] Open work: 3 issues" {
		t.Errorf("subject: %q", m.Subject)
	}
	for _, want := range []string{"OPS-1", "SHOP-1", "Checkout <fast>", "The filter finds 3 issues.", "/browse/SHOP-2",
		"/issues/search?filter="} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("lena's e-mail lacks %q:\n%s", want, m.Text)
		}
	}
	if !strings.Contains(m.HTML, "Checkout &lt;fast&gt;") || strings.Contains(m.HTML, "<fast>") {
		t.Errorf("the summary is not escaped in HTML:\n%s", m.HTML)
	}

	// Dirk receives the team's e-mail again, in German, without the issue he may not see.
	f.dev.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/filter-subscriptions/%d/mute", group.ID), nil, nil)
	got = byRecipient(deliver())
	if len(got["lena@example.com"]) != 2 || len(got["dirk@example.com"]) != 1 || len(got) != 2 {
		t.Fatalf("second delivery: %+v", got)
	}
	m = got["dirk@example.com"][0]
	if m.Subject != "[Kogge] Open work: 2 Vorgänge" || strings.Contains(m.Text, "OPS-1") || strings.Contains(m.Text, "Secret") ||
		!strings.Contains(m.Text, "SHOP-2") || !strings.Contains(m.Text, "Mitglied der Gruppe team") ||
		!strings.Contains(m.HTML, "Bearbeitende Person") {
		t.Errorf("dirk's e-mail: %s\n%s", m.Subject, m.Text)
	}

	// With empty results wanted, otto hears that there is nothing.
	f.lead.MustDo(http.MethodPut, apitest.Fmt("/api/v1/filter-subscriptions/%d", group.ID), map[string]any{"frequency": "hourly",
		"minute": 30, "weekdays": []int{1, 2, 3, 4, 5}, "hours": []int{8, 12, 16}, "sendEmpty": true}, &group)
	if group.Minute != 30 || !slices.Equal(group.Hours, []int{8, 12, 16}) {
		t.Errorf("changed subscription: %+v", group)
	}
	got = byRecipient(deliver())
	if o := got["otto@example.com"]; len(o) != 1 || o[0].Subject != "[Kogge] Open work: keine Vorgänge" ||
		!strings.Contains(o[0].Text, "Der Filter findet keine Vorgänge.") {
		t.Errorf("otto's e-mail: %+v", got)
	}

	// Without the share, the team's members no longer receive anything; lena still does, as the owner.
	f.lead.MustDo(http.MethodPut, apitest.Fmt("/api/v1/filters/%d", filter.ID), map[string]any{"name": "Open work",
		"query": "statusCategory != Done ORDER BY key"}, nil)
	got = byRecipient(deliver())
	if len(got) != 1 || len(got["lena@example.com"]) != 2 {
		t.Errorf("after unsharing: %+v", got)
	}
	f.dev.Get("/api/v1/filter-subscriptions", &list)
	if len(list.Subscriptions) != 0 {
		t.Errorf("dirk still sees the subscription of a filter he cannot see: %+v", list)
	}

	// Send now: in a moment, then not again within a minute; the schedule stays.
	if _, err := pool.Exec(ctx, "UPDATE filter_subscriptions SET last_run_at = NULL"); err != nil {
		t.Fatal(err)
	}
	before := len(smtp.Messages())
	send := apitest.Fmt("/api/v1/filter-subscriptions/%d/send", mine.ID)
	f.lead.MustDo(http.MethodPost, send, nil, nil)
	f.env.WorkJobs()
	if mails := inbox(t, smtp, before); len(mails) != 1 || mails[0].To != "lena@example.com" {
		t.Errorf("sent now: %+v", mails)
	}
	if r := f.lead.Do(http.MethodPost, send, nil); r.Status != http.StatusTooManyRequests || r.Code() != "subscription.too-soon" {
		t.Errorf("sent twice within a minute: %d %s", r.Status, r.Body)
	}
	f.lead.Get(subs, &list)
	for _, s := range list.Subscriptions {
		if s.ID == mine.ID && (s.Last == nil || !s.Next.After(time.Now()) || s.Next.Equal(*s.Last)) {
			t.Errorf("after sending now: %+v", s)
		}
	}
	var logged int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM mail_log WHERE state = 'sent' AND subject LIKE '[Kogge] Open work%'").Scan(&logged); err != nil ||
		logged != len(smtp.Messages()) {
		t.Errorf("mail log: %d of %d %v", logged, len(smtp.Messages()), err)
	}

	// A person sets up at most 50 subscriptions.
	if _, err := pool.Exec(ctx, `INSERT INTO filter_subscriptions (filter_id, user_id, created_by, frequency, minute, weekdays, time_zone,
		next_run_at) SELECT $1, $2, $2, 'daily', 0, '{1}', 'UTC', now() + interval '1 day' FROM generate_series(1, 48)`, filter.ID,
		f.ids["lena"]); err != nil {
		t.Fatal(err)
	}
	if r := f.lead.Do(http.MethodPost, subs, map[string]any{"frequency": "daily", "minute": 0, "weekdays": []int{1}}); r.Code() != "subscription.limit" {
		t.Errorf("51st subscription: %d %s", r.Status, r.Body)
	}

	// Deleting: the subscription, and with the filter all of its subscriptions.
	f.lead.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/filter-subscriptions/%d", mine.ID), nil, nil)
	if r := f.lead.Do(http.MethodPost, send, nil); r.Status != http.StatusNotFound {
		t.Errorf("deleted subscription: %d", r.Status)
	}
	f.lead.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/filters/%d", filter.ID), nil, nil)
	f.lead.Get("/api/v1/filter-subscriptions", &list)
	if len(list.Subscriptions) != 0 {
		t.Errorf("subscriptions of a deleted filter: %+v", list)
	}
}

// TestFilterSubscriptionsWithoutMail: without e-mail, nobody subscribes, and the lists say why.
func TestFilterSubscriptionsWithoutMail(t *testing.T) {
	f := setup(t)
	var filter ref
	f.lead.Post("/api/v1/filters", map[string]any{"name": "Mine", "query": "project = SHOP"}, &filter)
	path := apitest.Fmt("/api/v1/filters/%d/subscriptions", filter.ID)
	if r := f.lead.Do(http.MethodPost, path, map[string]any{"frequency": "daily", "minute": 0, "weekdays": []int{1}}); r.Status != http.StatusConflict ||
		r.Code() != "subscription.no-mail" {
		t.Errorf("subscribed without e-mail: %d %s", r.Status, r.Body)
	}
	var list subscriptions
	f.lead.Get(path, &list)
	if list.Mail || len(list.Subscriptions) != 0 {
		t.Errorf("list: %+v", list)
	}
	if r := f.env.Anonymous().Do(http.MethodGet, "/api/v1/filter-subscriptions", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", r.Status)
	}
}
