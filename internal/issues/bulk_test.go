// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

type bulkItem struct {
	Key     string `json:"key"`
	NewKey  string `json:"newKey"`
	Summary string `json:"summary"`
	Result  string `json:"result"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bulkChange struct {
	ID        int64      `json:"id"`
	Action    string     `json:"action"`
	State     string     `json:"state"`
	Mine      bool       `json:"mine"`
	Total     int        `json:"total"`
	Processed int        `json:"processed"`
	Done      int        `json:"done"`
	Skipped   int        `json:"skipped"`
	Failed    int        `json:"failed"`
	Notify    bool       `json:"notify"`
	Items     []bulkItem `json:"items"`
}

func (b bulkChange) item(t *testing.T, key string) bulkItem {
	t.Helper()
	for _, it := range b.Items {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("the bulk change has no item %s: %+v", key, b.Items)
	return bulkItem{}
}

// startBulk starts a bulk change (202) and returns it as queued.
func startBulk(t *testing.T, c *apitest.Client, body map[string]any) bulkChange {
	t.Helper()
	var b bulkChange
	r := c.Do(http.MethodPost, "/api/v1/issues/bulk", body)
	if r.Status != http.StatusAccepted {
		t.Fatalf("start bulk change: %d %s", r.Status, r.Body)
	}
	r.Decode(&b)
	return b
}

// runBulk starts a bulk change, runs its job and returns its results.
func (f *fixture) runBulk(t *testing.T, c *apitest.Client, body map[string]any) bulkChange {
	t.Helper()
	b := startBulk(t, c, body)
	f.env.WorkJobs()
	c.Get("/api/v1/issues/bulk/"+itoa(b.ID), &b)
	if b.State != "done" {
		t.Fatalf("the bulk change did not finish: %+v", b)
	}
	return b
}

type notification struct {
	Event      string         `json:"event"`
	EntityType string         `json:"entityType"`
	EntityID   string         `json:"entityId"`
	Title      string         `json:"title"`
	Summary    string         `json:"summary"`
	URL        string         `json:"url"`
	Data       map[string]any `json:"data"`
	Hidden     bool           `json:"hidden"`
}

func inboxOf(t *testing.T, c *apitest.Client) []notification {
	t.Helper()
	var list []notification
	c.Get("/api/v1/notifications?limit=200", &list)
	return list
}

type fullIssue struct {
	Key      string   `json:"key"`
	Labels   []string `json:"labels"`
	Priority *ref     `json:"priority"`
	Status   ref      `json:"status"`
	Type     ref      `json:"type"`
	Project  struct {
		Key string `json:"key"`
	} `json:"project"`
	Assignee *struct {
		ID int64 `json:"id"`
	} `json:"assignee"`
	Components  []ref `json:"components"`
	FixVersions []ref `json:"fixVersions"`
	Sprint      *ref  `json:"sprint"`
	Parent      *struct {
		Key string `json:"key"`
	} `json:"parent"`
	Subtasks []struct {
		Key string `json:"key"`
	} `json:"subtasks"`
	Fields   map[string]any `json:"fields"`
	Watching bool           `json:"watching"`
	Version  int64          `json:"version"`
}

func getIssue(t *testing.T, c *apitest.Client, key string) fullIssue {
	t.Helper()
	var i fullIssue
	c.Get("/api/v1/issues/"+key, &i)
	return i
}

type historyEntry struct {
	Items []struct {
		Field      string `json:"field"`
		FromString string `json:"fromString"`
		ToString   string `json:"toString"`
	} `json:"items"`
}

func historyHas(t *testing.T, c *apitest.Client, key, field, to string) bool {
	t.Helper()
	var h []historyEntry
	c.Get("/api/v1/issues/"+key+"/history", &h)
	for _, e := range h {
		for _, it := range e.Items {
			if it.Field == field && (to == "" || it.ToString == to) {
				return true
			}
		}
	}
	return false
}

// TestBulkEdit: a bulk edit sets fields of the issues one may change in bulk and skips the others with a reason, each
// changed issue gets its history entry, who would hear of the changes gets one summary instead of a notification per
// issue, the bulk change leaves one audit event, and who sees an issue it changed sees what changed.
func TestBulkEdit(t *testing.T) {
	f := setup(t)
	story, bug := f.cfg.typ("story"), f.cfg.typ("bug")
	var keys []string
	for _, s := range []string{"Checkout", "Invoices", "Coupons"} {
		keys = append(keys, f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": s}).Key)
	}
	sub := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Design", "parent": keys[0]})
	// OPS: dirk browses it as a User, so he may change nothing there.
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	f.lead.Post("/api/v1/projects/OPS/roles/"+itoa(f.cfg.Roles[2].ID)+"/members", map[string]any{"type": "user", "id": f.ids["dirk"]}, nil)
	ops := f.create(t, f.lead, map[string]any{"project": "OPS", "typeId": f.cfg.typ("task"), "summary": "Backups"})
	// Ute watches the SHOP issues: she hears of their changes.
	for _, k := range keys {
		f.user.MustDo(http.MethodPut, "/api/v1/issues/"+k+"/watchers", nil, nil)
	}
	// A field for bugs only.
	var severity struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/fields", map[string]any{"name": "Severity", "type": "text", "issueTypeIds": []int64{bug}}, &severity)
	severityKey := "cf_" + itoa(severity.ID)

	// The check: what may be changed, and which fields apply to all.
	var check struct {
		Total  int `json:"total"`
		Issues []struct {
			Key       string `json:"key"`
			Permitted bool   `json:"permitted"`
			Reason    string `json:"reason"`
		} `json:"issues"`
		CanMute bool `json:"canMute"`
		Fields  []struct {
			Key     string `json:"key"`
			Applies bool   `json:"applies"`
			Missing []struct {
				Key    string `json:"key"`
				Reason string `json:"reason"`
			} `json:"missing"`
		} `json:"fields"`
	}
	selection := append(append([]string{}, keys...), sub.Key, ops.Key, "NOPE-9")
	f.dev.Post("/api/v1/issues/bulk/check", map[string]any{"issues": selection, "action": "edit"}, &check)
	permitted := map[string]bool{}
	reasons := map[string]string{}
	for _, i := range check.Issues {
		permitted[i.Key], reasons[i.Key] = i.Permitted, i.Reason
	}
	if check.Total != 6 || !permitted[keys[0]] || !permitted[sub.Key] || permitted[ops.Key] || permitted["NOPE-9"] || check.CanMute {
		t.Fatalf("check: %+v", check)
	}
	if !strings.Contains(reasons[ops.Key], "in bulk") || !strings.Contains(reasons["NOPE-9"], "does not exist") {
		t.Errorf("reasons: %v", reasons)
	}
	fields := map[string]int{}
	for k, fl := range check.Fields {
		fields[fl.Key] = k
	}
	if fl := check.Fields[fields["priority"]]; !fl.Applies {
		t.Errorf("priority does not apply to all: %+v", fl)
	}
	if fl := check.Fields[fields["sprint"]]; fl.Applies || len(fl.Missing) != 1 || fl.Missing[0].Key != sub.Key {
		t.Errorf("the sprint applies to a sub-task: %+v", fl)
	}
	if fl := check.Fields[fields[severityKey]]; fl.Applies || len(fl.Missing) != 4 || !strings.Contains(fl.Missing[0].Reason, "does not exist") {
		t.Errorf("a field of bugs applies to stories: %+v", fl)
	}
	// German reasons for German readers.
	r := f.dev.MustDo(http.MethodPost, "/api/v1/issues/bulk/check", map[string]any{"issues": []string{ops.Key}, "action": "edit"}, nil,
		"Accept-Language", "de")
	if !strings.Contains(string(r.Body), "nicht gesammelt ändern") {
		t.Errorf("German check: %s", r.Body)
	}

	// Who sees none of the issues learns nothing; who may change none of them in bulk is refused.
	if r := f.other.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": keys, "action": "watch"}); r.Status != http.StatusNotFound {
		t.Errorf("an outsider started a bulk change: %d %s", r.Status, r.Body)
	}
	if r := f.user.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": keys, "action": "edit",
		"edit": map[string]any{"fields": map[string]any{"priority": f.cfg.Priorities[0].ID}}}); r.Status != http.StatusForbidden || r.Code() != "bulk.denied" {
		t.Errorf("a user without bulk change started one: %d %s", r.Status, r.Body)
	}
	// Leaving out the notifications is for administrators of all the projects.
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": keys, "action": "watch", "notify": false}); r.Code() != "bulk.mute-denied" {
		t.Errorf("a developer left out the notifications: %d %s", r.Status, r.Body)
	}
	// Fields that have modes, or are not offered, are refused before anything runs.
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": keys, "action": "edit",
		"edit": map[string]any{"fields": map[string]any{"labels": []string{"x"}}}}); r.Code() != "bulk.edit-field" {
		t.Errorf("labels as a plain field: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": keys, "action": "edit",
		"edit": map[string]any{"fields": map[string]any{"summary": "All the same"}}}); r.Code() != "bulk.edit-field" {
		t.Errorf("summary in bulk: %d %s", r.Status, r.Body)
	}

	high := f.cfg.Priorities[1]
	b := f.runBulk(t, f.dev, map[string]any{"issues": selection, "action": "edit", "edit": map[string]any{
		"fields": map[string]any{"priority": high.ID},
		"labels": map[string]any{"mode": "add", "values": []string{"release", "urgent"}}}})
	if b.Total != 6 || b.Done != 4 || b.Skipped != 2 || b.Failed != 0 || !b.Mine {
		t.Fatalf("results: %+v", b)
	}
	if it := b.item(t, ops.Key); it.Result != "skipped" || it.Code != "bulk.issue-denied" {
		t.Errorf("OPS issue: %+v", it)
	}
	if it := b.item(t, "NOPE-9"); it.Result != "skipped" || it.Code != "issue.not-found" {
		t.Errorf("unknown issue: %+v", it)
	}
	for _, k := range append(keys, sub.Key) {
		i := getIssue(t, f.dev, k)
		if i.Priority == nil || i.Priority.ID != high.ID || !slices.Equal(i.Labels, []string{"release", "urgent"}) {
			t.Errorf("%s after the bulk edit: %+v %v", k, i.Priority, i.Labels)
		}
		if !historyHas(t, f.dev, k, "priority", high.Name) || !historyHas(t, f.dev, k, "labels", "release urgent") {
			t.Errorf("%s has no history of the bulk edit", k)
		}
	}
	if i := getIssue(t, f.lead, ops.Key); i.Priority != nil && i.Priority.ID == high.ID {
		t.Error("the bulk edit changed an issue it was not permitted to")
	}
	// Removing a label, replacing the labels: issues that need no change are skipped.
	b = f.runBulk(t, f.dev, map[string]any{"issues": keys[:2], "action": "edit", "edit": map[string]any{
		"labels": map[string]any{"mode": "remove", "values": []string{"urgent"}}}})
	if b.Done != 2 || !slices.Equal(getIssue(t, f.dev, keys[0]).Labels, []string{"release"}) {
		t.Errorf("remove: %+v", b)
	}
	b = f.runBulk(t, f.dev, map[string]any{"issues": keys[:2], "action": "edit", "edit": map[string]any{
		"labels": map[string]any{"mode": "remove", "values": []string{"urgent"}}}})
	if b.Skipped != 2 || b.item(t, keys[0]).Code != "bulk.unchanged" {
		t.Errorf("nothing to remove: %+v", b)
	}

	// One summary for Ute instead of a notification per issue.
	inbox := inboxOf(t, f.user)
	summaries := 0
	for _, n := range inbox {
		if n.EntityType == "issue" && n.Event == "issue.updated" {
			t.Errorf("a notification per issue: %+v", n)
		}
		if n.EntityType == "issue-bulk" && n.Event == "issue.bulk-edited" {
			summaries++
			if !strings.Contains(n.Summary, keys[0]) || n.URL != "/issues/bulk/"+n.EntityID || n.Hidden {
				t.Errorf("summary: %+v", n)
			}
		}
	}
	if summaries != 2 {
		t.Errorf("Ute got %d summaries for the two bulk edits that changed her issues: %+v", summaries, inbox)
	}
	// Ute sees what changed of what she sees; others nothing.
	var seen bulkChange
	f.user.Get("/api/v1/issues/bulk/"+itoa(b.ID-1), &seen)
	if seen.Mine || len(seen.Items) != 2 || seen.Skipped != 0 {
		t.Errorf("the recipient's view: %+v", seen)
	}
	if r := f.other.Do(http.MethodGet, "/api/v1/issues/bulk/"+itoa(b.ID), nil); r.Status != http.StatusNotFound {
		t.Errorf("an outsider sees the bulk change: %d", r.Status)
	}
	var mine []bulkChange
	f.dev.Get("/api/v1/issues/bulk", &mine)
	if len(mine) != 3 || mine[0].ID != b.ID || mine[0].Skipped != 2 {
		t.Errorf("dirk's bulk changes: %+v", mine)
	}

	// One audit event per bulk change, with its scope.
	var events []struct {
		Action   string         `json:"action"`
		TargetID string         `json:"targetId"`
		Details  map[string]any `json:"details"`
	}
	f.admin.Get("/api/v1/admin/audit?action=issue.bulk-change", &events)
	if len(events) != 3 {
		t.Fatalf("audit events: %+v", events)
	}
	last := events[len(events)-1]
	if last.Details["action"] != "edit" || last.Details["issues"] != float64(5) || !strings.Contains(strings.ToUpper(
		strings.Join(toStrings(last.Details["keys"]), " ")), keys[0]) {
		t.Errorf("first audit event: %+v", last)
	}
}

func toStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// TestBulkTransition: issues go to a status by the transition their workflow has to it — with the values its screen
// asks for and its validators — and those without one are skipped; the permissions are checked when the job runs.
func TestBulkTransition(t *testing.T) {
	f := setup(t)
	story := f.cfg.typ("story")
	open, progress, done := f.cfg.status("todo")[0], f.cfg.status("inprogress")[0], f.cfg.status("done")[0]
	var keys []string
	for _, s := range []string{"Search", "Filters", "Sorting"} {
		keys = append(keys, f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": s}).Key)
	}
	f.transition(t, keys[2], "done")

	var check struct {
		Statuses []struct {
			Status ref `json:"status"`
			Issues []struct {
				Key        string `json:"key"`
				Transition int    `json:"transition"`
			} `json:"issues"`
		} `json:"statuses"`
	}
	f.dev.Post("/api/v1/issues/bulk/check", map[string]any{"issues": keys, "action": "transition"}, &check)
	var toProgress []string
	for _, st := range check.Statuses {
		if st.Status.ID == progress {
			for _, i := range st.Issues {
				toProgress = append(toProgress, i.Key)
			}
		}
	}
	if !slices.Equal(toProgress, keys[:2]) {
		t.Fatalf("issues that can go to In Progress: %v (%+v)", toProgress, check)
	}

	b := f.runBulk(t, f.dev, map[string]any{"issues": keys, "action": "transition", "transition": map[string]any{"status": progress}})
	if b.Done != 2 || b.Skipped != 1 || b.item(t, keys[2]).Code != "bulk.no-transition" {
		t.Fatalf("results: %+v", b)
	}
	for _, k := range keys[:2] {
		if i := getIssue(t, f.dev, k); i.Status.ID != progress || !historyHas(t, f.dev, k, "status", "") {
			t.Errorf("%s: %+v", k, i.Status)
		}
	}
	// Issues in the status already need no change.
	b = f.runBulk(t, f.dev, map[string]any{"issues": keys[:1], "action": "transition", "transition": map[string]any{"status": progress}})
	if b.Skipped != 1 || b.item(t, keys[0]).Code != "bulk.unchanged" {
		t.Errorf("already there: %+v", b)
	}

	// A workflow whose transition asks for an assignee and a comment.
	var wf struct {
		ID int64 `json:"id"`
	}
	f.admin.Post("/api/v1/issue-config/workflows", map[string]any{"name": "Handover", "definition": map[string]any{
		"statuses": []int64{open, progress, done}, "initial": open, "options": map[string]any{"autoResolution": true},
		"transitions": []map[string]any{
			{"id": 1, "name": "Hand over", "from": []int64{open}, "to": progress, "screen": []string{"assignee"},
				"validators": []map[string]any{{"type": "comment-required"}}},
			{"id": 2, "name": "Close", "from": "any", "to": done},
		}}}, &wf)
	f.lead.Post("/api/v1/projects", map[string]any{"key": "HAND", "name": "Handover", "template": "kanban"}, nil)
	f.lead.MustDo(http.MethodPatch, "/api/v1/projects/HAND", map[string]any{"defaultWorkflowId": wf.ID}, nil)
	f.lead.Post("/api/v1/projects/HAND/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["dirk"]}, nil)
	var hand []string
	for _, s := range []string{"Laptop", "Phone"} {
		hand = append(hand, f.create(t, f.dev, map[string]any{"project": "HAND", "typeId": f.cfg.typ("task"), "summary": s}).Key)
	}
	// Without the comment the validator refuses: the issues failed and stay as they were.
	b = f.runBulk(t, f.dev, map[string]any{"issues": hand, "action": "transition", "transition": map[string]any{"status": progress,
		"fields": map[string]any{"assignee": f.ids["dirk"]}}})
	if b.Failed != 2 || b.item(t, hand[0]).Code != "issue.comment-required" {
		t.Fatalf("without a comment: %+v", b)
	}
	if i := getIssue(t, f.dev, hand[0]); i.Status.ID != open || i.Assignee != nil {
		t.Errorf("a failed transition changed the issue: %+v", i)
	}
	b = f.runBulk(t, f.dev, map[string]any{"issues": hand, "action": "transition", "transition": map[string]any{"status": progress,
		"fields": map[string]any{"assignee": f.ids["dirk"]}, "comment": comment("Handed over to Dirk.")}})
	if b.Done != 2 {
		t.Fatalf("with a comment: %+v", b)
	}
	for _, k := range hand {
		i := getIssue(t, f.dev, k)
		r := f.dev.MustDo(http.MethodGet, "/api/v1/issues/"+k+"/comments", nil, nil)
		if i.Status.ID != progress || i.Assignee == nil || i.Assignee.ID != f.ids["dirk"] || !strings.Contains(string(r.Body), "Handed over") {
			t.Errorf("%s after the handover: %+v %s", k, i, r.Body)
		}
	}

	// Permissions count when the job gets to an issue: dirk leaves the Developers role in the meantime.
	queued := startBulk(t, f.dev, map[string]any{"issues": keys[:2], "action": "transition", "transition": map[string]any{"status": done}})
	f.lead.MustDo(http.MethodDelete, "/api/v1/projects/SHOP/roles/"+itoa(f.cfg.Roles[1].ID)+"/members?type=user&id="+itoa(f.ids["dirk"]), nil, nil)
	f.env.WorkJobs()
	var after bulkChange
	f.dev.Get("/api/v1/issues/bulk/"+itoa(queued.ID), &after)
	if after.State != "done" || after.Skipped != 2 || after.item(t, keys[0]).Code != "issue.not-found" && after.item(t, keys[0]).Code != "bulk.issue-denied" {
		t.Errorf("after losing the role: %+v", after)
	}
	if i := getIssue(t, f.lead, keys[0]); i.Status.ID != progress {
		t.Errorf("the job changed an issue its starter may no longer change: %+v", i.Status)
	}
}

// TestBulkDeleteAndWatch: deleting needs the permission to delete and the typed number of issues; sub-tasks go with
// their parents. Watching and stopping to watch need only that one sees the issues.
func TestBulkDeleteAndWatch(t *testing.T) {
	f := setup(t)
	story := f.cfg.typ("story")
	var keys []string
	for _, s := range []string{"Old A", "Old B"} {
		keys = append(keys, f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": s}).Key)
	}
	sub := f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("subtask"), "summary": "Old C", "parent": keys[1]})
	selection := append(append([]string{}, keys...), sub.Key)

	if r := f.lead.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": selection, "action": "delete", "confirm": 2}); r.Code() != "bulk.confirm" {
		t.Fatalf("delete with the wrong number: %d %s", r.Status, r.Body)
	}
	// Developers may change in bulk, but not delete.
	b := f.runBulk(t, f.dev, map[string]any{"issues": selection, "action": "delete", "confirm": 3})
	if b.Skipped != 3 || b.item(t, keys[0]).Code != "issue.delete-denied" {
		t.Fatalf("deleted by a developer: %+v", b)
	}
	b = f.runBulk(t, f.lead, map[string]any{"issues": selection, "action": "delete", "confirm": 3})
	if b.Done != 3 || !strings.Contains(b.item(t, sub.Key).Message, keys[1]) || !strings.Contains(b.item(t, keys[1]).Message, "1") {
		t.Fatalf("deleted by the lead: %+v", b)
	}
	for _, k := range selection {
		if r := f.lead.Do(http.MethodGet, "/api/v1/issues/"+k, nil); r.Status != http.StatusNotFound {
			t.Errorf("%s still exists: %d", k, r.Status)
		}
	}
	// Ute (role Users) has no bulk change permission; with it, she watches and stops watching.
	more := []string{f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": "Watch A"}).Key,
		f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": story, "summary": "Watch B"}).Key}
	if r := f.user.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": more, "action": "watch"}); r.Code() != "bulk.denied" {
		t.Fatalf("watch without bulk change: %d %s", r.Status, r.Body)
	}
	var schemes []struct {
		ID     int64            `json:"id"`
		Name   string           `json:"name"`
		Grants []map[string]any `json:"grants"`
	}
	f.admin.Get("/api/v1/issue-config/permission-schemes", &schemes)
	s := schemes[0]
	bulkGrants := 0
	for _, g := range s.Grants {
		if g["permission"] == "issue.bulk-change" {
			bulkGrants++
		}
	}
	if bulkGrants != 2 {
		t.Errorf("the default scheme grants bulk change %d times, want Developers and Administrators: %+v", bulkGrants, s.Grants)
	}
	s.Grants = append(s.Grants, map[string]any{"permission": "issue.bulk-change", "holderType": "role", "holderId": f.cfg.Roles[2].ID})
	f.admin.MustDo(http.MethodPut, "/api/v1/issue-config/permission-schemes/"+itoa(s.ID), map[string]any{"name": s.Name, "grants": s.Grants}, nil)
	b = f.runBulk(t, f.user, map[string]any{"issues": more, "action": "watch"})
	if b.Done != 2 || !getIssue(t, f.user, more[0]).Watching {
		t.Fatalf("watch: %+v", b)
	}
	b = f.runBulk(t, f.user, map[string]any{"issues": more, "action": "watch"})
	if b.Skipped != 2 {
		t.Errorf("watching twice: %+v", b)
	}
	b = f.runBulk(t, f.user, map[string]any{"issues": more, "action": "unwatch"})
	if b.Done != 2 || getIssue(t, f.user, more[1]).Watching {
		t.Errorf("unwatch: %+v", b)
	}
	// Ute may change none of them otherwise: a bulk edit skips them all.
	b = f.runBulk(t, f.user, map[string]any{"issues": more, "action": "edit", "edit": map[string]any{
		"fields": map[string]any{"priority": f.cfg.Priorities[0].ID}}})
	if b.Skipped != 2 || b.item(t, more[0]).Code != "issue.field-denied" {
		t.Errorf("an edit without issue.edit: %+v", b)
	}
}

// TestBulkSprintSchedulesInTheSprintsProject: a bulk edit of the sprint field needs scheduling in the sprint's
// project, as a single edit does (TestSprintField).
func TestBulkSprintSchedulesInTheSprintsProject(t *testing.T) {
	f := setup(t)
	f.lead.Post("/api/v1/projects", map[string]any{"key": "OPS", "name": "Operations", "template": "kanban"}, nil)
	f.lead.Post("/api/v1/projects/OPS/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["ute"]}, nil)
	ops := f.create(t, f.user, map[string]any{"project": "OPS", "typeId": f.cfg.typ("task"), "summary": "Backups"})
	var boards struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	f.lead.Get("/api/v1/boards?project=SHOP", &boards)
	var sp struct {
		ID int64 `json:"id"`
	}
	f.lead.Post("/api/v1/boards/"+itoa(boards.Items[0].ID)+"/sprints", map[string]any{"name": "Launch"}, &sp)
	edit := map[string]any{"issues": []string{ops.Key}, "action": "edit", "edit": map[string]any{"fields": map[string]any{"sprint": sp.ID}}}
	b := f.runBulk(t, f.user, edit)
	if b.Skipped != 1 || b.item(t, ops.Key).Code != "sprint.schedule-denied" {
		t.Errorf("into a sprint of a project its editor does not schedule in: %+v", b)
	}
	// Who schedules there, may.
	f.lead.Post("/api/v1/projects/SHOP/roles/"+itoa(f.cfg.Roles[1].ID)+"/members", map[string]any{"type": "user", "id": f.ids["ute"]}, nil)
	b = f.runBulk(t, f.user, edit)
	if i := getIssue(t, f.user, ops.Key); b.Done != 1 || i.Sprint == nil || i.Sprint.ID != sp.ID {
		t.Errorf("into the sprint: %+v, sprint %+v", b, i.Sprint)
	}
}

// TestBulkSelectionAndLimits: a query chooses the issues (its first 1000 results, in its order), keys and queries do
// not mix, a bulk change takes 1000 issues at most, and three may wait or run at a time per person.
func TestBulkSelectionAndLimits(t *testing.T) {
	f := setup(t)
	var keys []string
	for _, s := range []string{"Q1", "Q2", "Q3"} {
		keys = append(keys, f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": s}).Key)
	}
	var check struct {
		Total  int `json:"total"`
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
	}
	f.dev.Post("/api/v1/issues/bulk/check", map[string]any{"query": "project = SHOP ORDER BY key DESC", "action": "watch"}, &check)
	if check.Total != 3 || len(check.Issues) != 3 || check.Issues[0].Key != keys[2] {
		t.Fatalf("query selection: %+v", check)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk/check", map[string]any{"query": "project = SHOP", "issues": keys,
		"action": "watch"}); r.Code() != "bulk.selection" {
		t.Errorf("keys and a query: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"query": "project = SHOP AND summary ~ nothing",
		"action": "watch"}); r.Code() != "bulk.empty" {
		t.Errorf("an empty query: %d %s", r.Status, r.Body)
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk/check", map[string]any{"query": "project = ", "action": "watch"}); r.Status != http.StatusBadRequest {
		t.Errorf("an invalid query: %d %s", r.Status, r.Body)
	}
	many := make([]string, 1001)
	for k := range many {
		many[k] = "SHOP-" + itoa(int64(k+1))
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"issues": many, "action": "watch"}); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("1001 issues: %d %s", r.Status, r.Body)
	}
	// Three wait; the fourth must wait for them.
	for k := 0; k < 3; k++ {
		startBulk(t, f.dev, map[string]any{"query": "project = SHOP", "action": "watch"})
	}
	if r := f.dev.Do(http.MethodPost, "/api/v1/issues/bulk", map[string]any{"query": "project = SHOP", "action": "watch"}); r.Status != http.StatusTooManyRequests {
		t.Errorf("a fourth bulk change: %d %s", r.Status, r.Body)
	}
	f.env.WorkJobs()
	b := f.runBulk(t, f.dev, map[string]any{"query": "project = SHOP ORDER BY key", "action": "unwatch"})
	if b.Done != 3 || b.Items[0].Key != keys[0] {
		t.Errorf("after the others ran: %+v", b)
	}
}

// TestKQLAutocompletion: the catalogue names the fields with the operators the compiler takes, and suggestions show
// only values of what the caller may see.
func TestKQLAutocompletion(t *testing.T) {
	f := setup(t)
	// Custom fields of the kinds that take functions.
	for name, typ := range map[string]string{"Reviewer": "user", "Approvers": "multiuser", "Deadline": "date",
		"Planned for": "datetime", "Found in": "version", "Shipped in": "multiversion"} {
		f.admin.Post("/api/v1/issue-config/fields", map[string]any{"name": name, "type": typ}, nil)
	}
	var cat struct {
		Fields []struct {
			Name      string   `json:"name"`
			Aliases   []string `json:"aliases"`
			Custom    bool     `json:"custom"`
			Kind      string   `json:"kind"`
			Operators []string `json:"operators"`
		} `json:"fields"`
		Functions []struct {
			Name  string   `json:"name"`
			Arg   string   `json:"arg"`
			Kinds []string `json:"kinds"`
			List  bool     `json:"list"`
		} `json:"functions"`
		Keywords []string `json:"keywords"`
	}
	f.dev.Get("/api/v1/search/autocomplete", &cat)
	if len(cat.Fields) < 30 || len(cat.Functions) < 20 || !slices.Contains(cat.Keywords, "ORDER BY") {
		t.Fatalf("catalogue: %+v", cat)
	}
	// Every operator the catalogue names compiles for its field, every other one is refused (by the operator).
	all := []string{"=", "!=", ">", ">=", "<", "<=", "~", "!~", "in", "not in", "is", "is not"}
	values := map[string]string{"project": "SHOP", "key": "SHOP-1", "issuetype": "Story", "status": `"In Progress"`,
		"statuscategory": "Done", "priority": "High", "resolution": "Unresolved", "user": "dirk", "text": "word",
		"labels": "x", "component": "C", "version": "V", "sprint": "S", "issue": "SHOP-1", "date": "2026-10-01",
		"datetime": "2026-10-01", "duration": "1h", "number": "3", "securitylevel": "L", "filter": "F", "none": "x",
		"option": "O", "group": "G"}
	f.lead.Post("/api/v1/projects/SHOP/components", map[string]any{"name": "C"}, nil)
	f.lead.Post("/api/v1/projects/SHOP/versions", map[string]any{"name": "V"}, nil)
	f.create(t, f.dev, map[string]any{"project": "SHOP", "typeId": f.cfg.typ("task"), "summary": "Target"})
	for _, fl := range cat.Fields {
		if fl.Name == "rank" {
			continue
		}
		name := fl.Name
		if strings.Contains(name, " ") {
			name = `"` + name + `"`
		}
		for _, op := range all {
			v := values[fl.Kind]
			switch op {
			case "in", "not in":
				v = "(" + v + ")"
			case "is", "is not":
				v = "EMPTY"
			}
			q := name + " " + op + " " + v
			r := f.dev.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil)
			if r.Status >= 500 {
				t.Errorf("%s: %d %s", q, r.Status, r.Body)
			}
			refused := strings.Contains(string(r.Body), "cannot be used with the field") || strings.Contains(string(r.Body), "can only be compared") ||
				strings.Contains(string(r.Body), "cannot be compared with EMPTY") || strings.Contains(string(r.Body), "never empty") ||
				strings.Contains(string(r.Body), "can only be used in ORDER BY") || strings.Contains(string(r.Body), "cannot be empty")
			if want := slices.Contains(fl.Operators, op); want == refused {
				t.Errorf("%s: the catalogue says %v, the query answers %d %s", q, want, r.Status, r.Body)
			}
		}
	}
	// Every function compiles with the fields of the kinds the catalogue offers it for, system and custom ones.
	f.admin.Post("/api/v1/admin/groups", map[string]any{"name": "reviewers"}, nil)
	args := map[string]string{"group": "reviewers", "project": "SHOP", "issue": "SHOP-1", "increment": "-1d"}
	used := map[string]int{}
	for _, fn := range cat.Functions {
		for _, fl := range cat.Fields {
			if !slices.Contains(fn.Kinds, fl.Kind) {
				continue
			}
			used[fn.Name]++
			name, op := fl.Name, "="
			if strings.Contains(name, " ") {
				name = `"` + name + `"`
			}
			if fn.List {
				op = "in"
			}
			q := name + " " + op + " " + fn.Name + "(" + args[fn.Arg] + ")"
			if r := f.dev.Do(http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil); r.Status != http.StatusOK {
				t.Errorf("%s: %d %s", q, r.Status, r.Body)
			}
		}
	}
	for _, fn := range cat.Functions {
		if used[fn.Name] == 0 {
			t.Errorf("%s() is offered for no field", fn.Name)
		}
	}
	if used["currentUser"] < 7 || used["releasedVersions"] < 4 || used["now"] < 6 {
		t.Errorf("functions per field: %v", used)
	}

	suggest := func(c *apitest.Client, field, prefix string) []string {
		t.Helper()
		var list []struct {
			Value string `json:"value"`
		}
		c.Get("/api/v1/search/suggestions?field="+url.QueryEscape(field)+"&prefix="+url.QueryEscape(prefix), &list)
		out := []string{}
		for _, s := range list {
			out = append(out, s.Value)
		}
		return out
	}
	// A project the developer does not see, with a version and a component of the same initial.
	f.lead.Post("/api/v1/projects", map[string]any{"key": "SECRET", "name": "Secret", "template": "kanban"}, nil)
	f.lead.Post("/api/v1/projects/SECRET/versions", map[string]any{"name": "Venus"}, nil)
	f.lead.Post("/api/v1/projects/SECRET/components", map[string]any{"name": "Cloak"}, nil)
	f.create(t, f.lead, map[string]any{"project": "SECRET", "typeId": f.cfg.typ("task"), "summary": "Hidden", "fields": map[string]any{
		"labels": []string{"covert"}}})
	if got := suggest(f.dev, "project", ""); !slices.Equal(got, []string{"SHOP"}) {
		t.Errorf("projects: %v", got)
	}
	if got := suggest(f.lead, "project", "se"); !slices.Equal(got, []string{"SECRET"}) {
		t.Errorf("projects of the lead: %v", got)
	}
	if got := suggest(f.dev, "fixVersion", "v"); !slices.Equal(got, []string{"V"}) {
		t.Errorf("versions: %v", got)
	}
	if got := suggest(f.dev, "component", ""); !slices.Equal(got, []string{"C"}) {
		t.Errorf("components: %v", got)
	}
	if got := suggest(f.dev, "labels", "co"); len(got) != 0 {
		t.Errorf("labels of a hidden issue: %v", got)
	}
	if got := suggest(f.lead, "labels", "co"); !slices.Equal(got, []string{"covert"}) {
		t.Errorf("labels of the lead: %v", got)
	}
	if got := suggest(f.dev, "key", "SECRET"); len(got) != 0 {
		t.Errorf("hidden issues: %v", got)
	}
	var progress string
	for _, st := range f.cfg.Statuses {
		if st.Category == "inprogress" && progress == "" {
			progress = st.Name
		}
	}
	if got := suggest(f.dev, "status", progress[:4]); !slices.Contains(got, progress) {
		t.Errorf("statuses: %v", got)
	}
	if got := suggest(f.other, "status", ""); len(got) != 0 {
		t.Errorf("someone who sees no project gets statuses: %v", got)
	}
	if got := suggest(f.dev, "assignee", "lena"); !slices.Equal(got, []string{"lena"}) {
		t.Errorf("users: %v", got)
	}
	// Naming people needs the permission to browse users.
	f.admin.MustDo(http.MethodDelete, "/api/v1/admin/global-permissions?permission=user.browse&principalType=logged-in", nil, nil)
	if got := suggest(f.dev, "assignee", "lena"); len(got) != 0 {
		t.Errorf("users without user.browse: %v", got)
	}
	if got := suggest(f.dev, "unknown field", ""); len(got) != 0 {
		t.Errorf("an unknown field: %v", got)
	}
}
