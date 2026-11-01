// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package webhooks_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
)

func TestMain(m *testing.M) {
	code.HookMain()
	m.Run()
}

type received struct {
	Event     string
	Signature string
	Body      []byte
}

type receiver struct {
	mu     sync.Mutex
	got    []received
	status int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.got = append(r.got, received{Event: req.Header.Get("X-Kogge-Event"), Signature: req.Header.Get("X-Kogge-Signature"), Body: b})
	status := r.status
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (r *receiver) all() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type hook struct {
	ID     int64  `json:"id"`
	Secret string `json:"secret"`
	Last   *struct {
		State string `json:"state"`
	} `json:"lastDelivery"`
}

type delivery struct {
	ID             int64  `json:"id"`
	Event          string `json:"event"`
	State          string `json:"state"`
	Attempts       int    `json:"attempts"`
	ResponseStatus *int   `json:"responseStatus"`
	Error          string `json:"error"`
}

func TestWebhooks(t *testing.T) {
	env := apitest.New(t, func(c *config.Config) { c.WebhookLoopback = true })
	admin := env.Admin()
	rcv := &receiver{}
	target := httptest.NewServer(rcv)
	defer target.Close()

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "WH", "name": "Hooks"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/WH/repos", map[string]any{"name": "Shop", "readme": true}, nil)

	// A repository webhook for pushes, with its own secret, and an instance webhook for everything.
	var repoHook, allHook hook
	admin.MustDo(http.MethodPost, "/api/v1/webhooks?scope=repository&key=WH/shop", map[string]any{
		"name": "CI", "url": target.URL + "/ci", "events": []string{"repository.pushed"}, "active": true, "secret": "s3cret",
	}, &repoHook)
	if repoHook.Secret != "" {
		t.Error("a chosen secret is returned")
	}
	admin.MustDo(http.MethodPost, "/api/v1/webhooks?scope=instance", map[string]any{
		"name": "Audit", "url": target.URL + "/all", "events": []string{}, "active": true,
	}, &allHook)
	if len(allHook.Secret) != 48 {
		t.Errorf("generated secret = %q", allHook.Secret)
	}
	// Events of other modules are not available for a repository.
	if res := admin.Do(http.MethodPost, "/api/v1/webhooks?scope=repository&key=WH/shop", map[string]any{
		"name": "x", "url": target.URL, "events": []string{"issue.created"}, "active": true,
	}); res.Code() != "webhook.event" {
		t.Errorf("issue event on a repository: %d %s", res.Status, res.Body)
	}
	for _, bad := range []string{"ftp://example.com", "http://user:pw@example.com/", "not a url"} {
		if res := admin.Do(http.MethodPost, "/api/v1/webhooks?scope=instance", map[string]any{"name": "x", "url": bad, "events": []string{},
			"active": true}); res.Code() != "webhook.url" {
			t.Errorf("url %q: %d %s", bad, res.Status, res.Body)
		}
	}

	// A commit in the browser is a push: both webhooks receive it, signed.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/WH/shop/files", map[string]any{"branch": "main", "path": "a.txt", "content": "a\n"}, nil)
	env.WorkJobs()
	got := rcv.all()
	if len(got) != 2 {
		t.Fatalf("deliveries = %d", len(got))
	}
	for _, r := range got {
		if r.Event != "repository.pushed" {
			t.Errorf("event = %s", r.Event)
		}
		var payload struct {
			Event      string `json:"event"`
			Repository struct {
				ProjectKey string `json:"projectKey"`
				Slug       string `json:"slug"`
			} `json:"repository"`
			Push struct {
				Updates []struct {
					Ref string `json:"ref"`
				} `json:"updates"`
			} `json:"push"`
			Actor struct {
				Username string `json:"username"`
			} `json:"actor"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Repository.Slug != "shop" || len(payload.Push.Updates) != 1 || payload.Push.Updates[0].Ref != "refs/heads/main" ||
			payload.Actor.Username != "admin" {
			t.Errorf("payload = %s", r.Body)
		}
		secret := "s3cret"
		if r.Signature != sign(secret, r.Body) {
			secret = allHook.Secret
		}
		if r.Signature != sign(secret, r.Body) {
			t.Errorf("signature %s does not match", r.Signature)
		}
	}

	// Ping; then a failing target: the delivery is retried later and can be delivered again.
	var started struct {
		DeliveryID int64 `json:"deliveryId"`
	}
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/webhooks/%d/ping", repoHook.ID), nil, &started)
	env.WorkJobs()
	if got := rcv.all(); len(got) != 3 || got[2].Event != "ping" {
		t.Fatalf("ping not delivered: %+v", got)
	}
	rcv.mu.Lock()
	rcv.status = http.StatusInternalServerError
	rcv.mu.Unlock()
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/webhooks/%d/ping", repoHook.ID), nil, &started)
	env.WorkJobs()
	var list []delivery
	admin.Get(apitest.Fmt("/api/v1/webhooks/%d/deliveries", repoHook.ID), &list)
	if len(list) != 3 || list[0].State != "pending" || list[0].Attempts < 1 || list[0].ResponseStatus == nil || *list[0].ResponseStatus != 500 ||
		list[0].Error != "The target answered 500." || list[1].State != "succeeded" {
		t.Errorf("deliveries = %+v", list)
	}
	rcv.mu.Lock()
	rcv.status = 0
	rcv.mu.Unlock()
	admin.MustDo(http.MethodPost, apitest.Fmt("/api/v1/webhooks/deliveries/%d/redeliver", list[0].ID), nil, &started)
	env.WorkJobs()
	admin.Get(apitest.Fmt("/api/v1/webhooks/%d/deliveries", repoHook.ID), &list)
	if list[0].ID != started.DeliveryID || list[0].State != "succeeded" {
		t.Errorf("redelivery = %+v", list[0])
	}
	var hooks []hook
	admin.Get("/api/v1/webhooks?scope=repository&key=WH/shop", &hooks)
	if len(hooks) != 1 || hooks[0].Last == nil || hooks[0].Last.State != "succeeded" || hooks[0].Secret != "" {
		t.Errorf("hooks = %+v", hooks)
	}

	// Others neither see nor change the webhooks.
	dev := env.CreateUser("dev")
	if res := dev.Do(http.MethodGet, "/api/v1/webhooks?scope=instance", nil); res.Status != http.StatusForbidden {
		t.Errorf("instance webhooks for a user: %d", res.Status)
	}
	if res := dev.Do(http.MethodGet, "/api/v1/webhooks?scope=repository&key=WH/shop", nil); res.Status != http.StatusNotFound {
		t.Errorf("repository webhooks for a user: %d", res.Status)
	}
	if res := dev.Do(http.MethodDelete, apitest.Fmt("/api/v1/webhooks/%d", repoHook.ID), nil); res.Status != http.StatusNotFound {
		t.Errorf("delete by a user: %d", res.Status)
	}
	admin.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/webhooks/%d", repoHook.ID), nil, nil)
}

// TestSecuredIssues: events of issues with a security level reach only webhooks whose owner sees the issue; a
// project administrator outside the level gets the project's other events.
func TestSecuredIssues(t *testing.T) {
	env := apitest.New(t, func(c *config.Config) { c.WebhookLoopback = true })
	admin := env.Admin()
	lead, boss := env.CreateUser("lead"), env.CreateUser("boss")
	idOf := func(c *apitest.Client) int64 {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		return me.User.ID
	}
	leadID, bossID := idOf(lead), idOf(boss)
	admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "project.create",
		"principalType": "user", "principalId": leadID}, nil)
	lead.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SEC", "name": "Secure", "template": "kanban"}, nil)
	var cfg struct {
		Roles []struct {
			ID int64 `json:"id"`
		} `json:"roles"`
	}
	admin.Get("/api/v1/issue-config", &cfg)
	lead.MustDo(http.MethodPost, apitest.Fmt("/api/v1/projects/SEC/roles/%d/members", cfg.Roles[0].ID), map[string]any{"type": "user",
		"id": bossID}, nil)
	var scheme struct {
		ID     int64 `json:"id"`
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issue-config/security-schemes", map[string]any{"name": "Board", "levels": []map[string]any{
		{"name": "Board only", "members": []map[string]any{{"holderType": "user", "holderId": bossID}}}}}, &scheme)
	admin.MustDo(http.MethodPatch, "/api/v1/projects/SEC", map[string]any{"securitySchemeId": scheme.ID}, nil)
	var schemes []struct {
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	admin.Get("/api/v1/issue-config/security-schemes", &schemes)

	rcv := &receiver{}
	target := httptest.NewServer(rcv)
	defer target.Close()
	var leadHook hook
	lead.MustDo(http.MethodPost, "/api/v1/webhooks?scope=project&key=SEC", map[string]any{"name": "Lead", "url": target.URL,
		"events": []string{"issue.created"}, "active": true}, &leadHook)
	// Personal spaces, which everybody can have, have no webhooks.
	var personal struct {
		Key string `json:"key"`
	}
	lead.MustDo(http.MethodPost, "/api/v1/wiki/spaces/personal", nil, &personal)
	if r := lead.Do(http.MethodPost, "/api/v1/webhooks?scope=space&key="+personal.Key, map[string]any{"name": "x", "url": target.URL,
		"events": []string{}, "active": true}); r.Code() != "space.personal" {
		t.Errorf("webhook of a personal space: %d %s", r.Status, r.Body)
	}

	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	lead.Get("/api/v1/projects/SEC/issue-types", &types)
	boss.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SEC", "typeId": types[0].ID, "summary": "Restructuring plan",
		"fields": map[string]any{"security": schemes[0].Levels[0].ID}}, nil)
	lead.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SEC", "typeId": types[0].ID, "summary": "Team offsite"}, nil)
	env.WorkJobs()

	// What targets answer is for Kogge's administrators only.
	var list []struct {
		ResponseStatus  *int   `json:"responseStatus"`
		ResponseExcerpt string `json:"responseExcerpt"`
	}
	lead.Get(apitest.Fmt("/api/v1/webhooks/%d/deliveries", leadHook.ID), &list)
	if len(list) != 1 || list[0].ResponseStatus == nil || list[0].ResponseExcerpt != "" {
		t.Errorf("deliveries for the project administrator: %+v", list)
	}
	got := rcv.all()
	if len(got) != 1 || !strings.Contains(string(got[0].Body), "Team offsite") {
		bodies := []string{}
		for _, r := range got {
			bodies = append(bodies, string(r.Body))
		}
		t.Fatalf("deliveries: %v", bodies)
	}
}

// TestWebhookOwnership: a webhook sends only what its owner may see — the person who created it or last changed its
// address. Redirecting someone else's webhook makes it one's own; delivery logs show payloads only to readers who
// may see them, also to Kogge's administrators; a webhook stops when its owner no longer administers its scope
// (audit H3).
func TestWebhookOwnership(t *testing.T) {
	env := apitest.New(t, func(c *config.Config) { c.WebhookLoopback = true })
	admin := env.Admin()
	lead, boss := env.CreateUser("lead"), env.CreateUser("boss")
	idOf := func(c *apitest.Client) int64 {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		c.Get("/api/v1/me", &me)
		return me.User.ID
	}
	leadID, bossID := idOf(lead), idOf(boss)
	admin.MustDo(http.MethodPost, "/api/v1/admin/global-permissions", map[string]any{"permission": "project.create",
		"principalType": "user", "principalId": leadID}, nil)
	lead.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SEC", "name": "Secure", "template": "kanban"}, nil)
	var cfg struct {
		Roles []struct {
			ID int64 `json:"id"`
		} `json:"roles"`
	}
	admin.Get("/api/v1/issue-config", &cfg)
	adminsRole := cfg.Roles[0].ID
	lead.MustDo(http.MethodPost, apitest.Fmt("/api/v1/projects/SEC/roles/%d/members", adminsRole), map[string]any{"type": "user",
		"id": bossID}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/issue-config/security-schemes", map[string]any{"name": "Board", "levels": []map[string]any{
		{"name": "Board only", "members": []map[string]any{{"holderType": "user", "holderId": bossID}}}}}, nil)
	var schemes []struct {
		ID     int64 `json:"id"`
		Levels []struct {
			ID int64 `json:"id"`
		} `json:"levels"`
	}
	admin.Get("/api/v1/issue-config/security-schemes", &schemes)
	admin.MustDo(http.MethodPatch, "/api/v1/projects/SEC", map[string]any{"securitySchemeId": schemes[0].ID}, nil)
	level := schemes[0].Levels[0].ID
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	lead.Get("/api/v1/projects/SEC/issue-types", &types)
	create := func(c *apitest.Client, summary string, secured bool) {
		t.Helper()
		fields := map[string]any{}
		if secured {
			fields["security"] = level
		}
		c.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SEC", "typeId": types[0].ID, "summary": summary,
			"fields": fields}, nil)
	}
	bossRcv, leadRcv := &receiver{}, &receiver{}
	bossTarget, leadTarget := httptest.NewServer(bossRcv), httptest.NewServer(leadRcv)
	defer bossTarget.Close()
	defer leadTarget.Close()
	bodies := func(r *receiver) string {
		var out []string
		for _, g := range r.all() {
			out = append(out, string(g.Body))
		}
		return strings.Join(out, "\n")
	}

	// The board member's webhook receives the secured issue.
	var h struct {
		ID    int64 `json:"id"`
		Owner struct {
			Username string `json:"username"`
		} `json:"owner"`
	}
	boss.MustDo(http.MethodPost, "/api/v1/webhooks?scope=project&key=SEC", map[string]any{"name": "Board", "url": bossTarget.URL,
		"events": []string{"issue.created"}, "active": true}, &h)
	if h.Owner.Username != "boss" {
		t.Fatalf("owner of a new webhook: %+v", h)
	}
	create(boss, "Restructuring plan", true)
	env.WorkJobs()
	if !strings.Contains(bodies(bossRcv), "Restructuring plan") {
		t.Fatalf("the owner's webhook did not get the secured issue: %s", bodies(bossRcv))
	}

	// Administering the project is not enough to read the payload, for the project's administrators and Kogge's.
	type logEntry struct {
		ID            int64           `json:"id"`
		Payload       json.RawMessage `json:"payload"`
		PayloadHidden bool            `json:"payloadHidden"`
	}
	for name, c := range map[string]*apitest.Client{"lead": lead, "admin": admin} {
		var log []logEntry
		c.Get(apitest.Fmt("/api/v1/webhooks/%d/deliveries", h.ID), &log)
		if len(log) != 1 || !log[0].PayloadHidden || len(log[0].Payload) != 0 {
			t.Fatalf("%s reads the payload of the secured issue: %+v", name, log)
		}
	}
	var log []logEntry
	boss.Get(apitest.Fmt("/api/v1/webhooks/%d/deliveries", h.ID), &log)
	if len(log) != 1 || log[0].PayloadHidden || !strings.Contains(string(log[0].Payload), "Restructuring plan") {
		t.Fatalf("the board member's log: %+v", log)
	}
	securedDelivery := log[0].ID

	// The lead redirects the webhook to himself: it becomes his and sends only what he may see.
	var changed struct {
		Owner struct {
			Username string `json:"username"`
		} `json:"owner"`
	}
	lead.MustDo(http.MethodPut, apitest.Fmt("/api/v1/webhooks/%d", h.ID), map[string]any{"name": "Board", "url": leadTarget.URL,
		"events": []string{"issue.created"}, "active": true}, &changed)
	if changed.Owner.Username != "lead" {
		t.Fatalf("owner after the address changed: %+v", changed)
	}
	if r := lead.Do(http.MethodPost, apitest.Fmt("/api/v1/webhooks/deliveries/%d/redeliver", securedDelivery), nil); r.Code() != "webhook.cannot-deliver" {
		t.Fatalf("redelivery of the secured issue to the new address: %d %s", r.Status, r.Body)
	}
	create(boss, "Layoffs", true)
	create(boss, "Team offsite", false)
	env.WorkJobs()
	if got := bodies(leadRcv); strings.Contains(got, "Layoffs") || strings.Contains(got, "Restructuring") || !strings.Contains(got, "Team offsite") {
		t.Fatalf("the redirected webhook sent: %s", got)
	}
	var events []struct {
		Action  string         `json:"action"`
		Details map[string]any `json:"details"`
	}
	admin.Get("/api/v1/admin/audit?action=webhook.update", &events)
	if len(events) == 0 || events[0].Details["ownerChanged"] != true || events[0].Details["previousUrl"] != bossTarget.URL {
		t.Fatalf("audit of the redirect: %+v", events)
	}

	// A webhook whose owner no longer administers the project stops.
	lead.MustDo(http.MethodDelete, apitest.Fmt("/api/v1/projects/SEC/roles/%d/members?type=user&id=%d", adminsRole, leadID), nil, nil)
	before := len(leadRcv.all())
	create(boss, "Parking spaces", false)
	env.WorkJobs()
	if len(leadRcv.all()) != before {
		t.Fatalf("a webhook of a former administrator still sends: %s", bodies(leadRcv))
	}
}
