// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/testutil/apitest"
)

const ownersFile = `# Who owns what
*                @anna
/payments/       @"Payments Team"
*.md             nobody@example.com
/vendor/
/tools/          @carol
/secret/         @erin
`

// TestCodeOwners: the CODEOWNERS file of the target branch decides; the people who own changed files become
// reviewers and members of small owner groups hear of the pull request; with the setting, every changed file that
// has owners needs the approval of one of them other than the author, so unknown owners block until the file on the
// target is fixed.
func TestCodeOwners(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	users := map[string]*apitest.Client{}
	ids := map[string]int64{}
	for _, name := range []string{"anna", "ben", "carol", "erin", "olga"} {
		users[name] = env.CreateUser(name)
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		users[name].Get("/api/v1/me", &me)
		ids[name] = me.User.ID
	}
	var me struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	admin.Get("/api/v1/me", &me)
	var group struct {
		ID int64 `json:"id"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/admin/groups", map[string]any{"name": "Payments Team"}, &group)
	admin.MustDo(http.MethodPut, apitest.Fmt("/api/v1/admin/groups/%d/members/%d", group.ID, ids["ben"]), nil, nil)

	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "OWN", "name": "Owners"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/OWN/repos", map[string]any{"name": "shop", "readme": true}, nil)
	// erin is named in the file but cannot read the repository; olga reads it and owns nothing.
	admin.MustDo(http.MethodPut, "/api/v1/code/projects/OWN/permissions", []map[string]any{
		{"holder": map[string]any{"type": "user", "id": me.User.ID}, "permission": "codeproject.admin"},
		{"holder": map[string]any{"type": "user", "id": ids["carol"]}, "permission": "repo.write"},
		{"holder": map[string]any{"type": "user", "id": ids["anna"]}, "permission": "repo.read"},
		{"holder": map[string]any{"type": "user", "id": ids["olga"]}, "permission": "repo.read"},
		{"holder": map[string]any{"type": "group", "id": group.ID}, "permission": "repo.read"}}, nil)
	admin.MustDo(http.MethodPatch, "/api/v1/code/repos/OWN/shop", map[string]any{"settings": map[string]any{
		"mergeStrategies": []string{"merge"}, "requiredApprovals": 0, "noNeedsWork": false, "tasksResolved": false,
		"requiredBuilds": 0, "resetApprovals": false, "deleteSourceBranch": false, "defaultReviewers": []int64{},
		"codeOwnerApproval": true}}, nil)
	files := "/api/v1/code/repos/OWN/shop/files"
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "path": ".kogge/CODEOWNERS", "content": ownersFile}, nil)
	// carol's feature changes files of every owner — and the owners, which counts only after merging.
	carol := users["carol"]
	for i, change := range [][2]string{{"payments/pay.go", "package payments\n"}, {"README.md", "# Shop\n"}, {"vendor/lib.go", "package lib\n"},
		{"tools/run.sh", "echo run\n"}, {"secret/key.txt", "none\n"},
		{".kogge/CODEOWNERS", strings.Replace(ownersFile, "*                @anna", "*                @carol", 1)}} {
		body := map[string]any{"branch": "feature", "path": change[0], "content": change[1]}
		if i == 0 {
			body = map[string]any{"branch": "main", "newBranch": "feature", "path": change[0], "content": change[1]}
		}
		carol.MustDo(http.MethodPut, files, body, nil)
	}
	env.WorkJobs()
	carol.MustDo(http.MethodPost, "/api/v1/code/repos/OWN/shop/pull-requests", map[string]any{"title": "Feature", "source": "feature"}, nil)

	type owned struct {
		Line  int `json:"line"`
		Users []struct {
			Username string `json:"username"`
		} `json:"users"`
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
		Unknown    []string `json:"unknown"`
		Files      []string `json:"files"`
		FileCount  int      `json:"fileCount"`
		ApprovedBy []struct {
			Username string `json:"username"`
		} `json:"approvedBy"`
	}
	var pr struct {
		Reviewers []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
			CodeOwner bool `json:"codeOwner"`
		} `json:"reviewers"`
		CodeOwnersFile string  `json:"codeOwnersFile"`
		CodeOwners     []owned `json:"codeOwners"`
		Checks         []struct {
			Key    string         `json:"key"`
			OK     bool           `json:"ok"`
			Reason string         `json:"reason"`
			Values map[string]int `json:"values"`
		} `json:"checks"`
	}
	load := func() {
		t.Helper()
		pr.Reviewers, pr.CodeOwners, pr.Checks = nil, nil, nil
		admin.Get("/api/v1/code/repos/OWN/shop/pull-requests/1", &pr)
	}
	check := func() string {
		for _, c := range pr.Checks {
			if c.Key == "code-owners" {
				return c.Reason + ":" + apitest.Fmt("%d", c.Values["count"])
			}
		}
		return "missing"
	}
	summary := func() string {
		var out []string
		for _, o := range pr.CodeOwners {
			var who []string
			for _, u := range o.Users {
				who = append(who, "@"+u.Username)
			}
			for _, g := range o.Groups {
				who = append(who, "@"+g.Name)
			}
			who = append(who, o.Unknown...)
			var by []string
			for _, u := range o.ApprovedBy {
				by = append(by, u.Username)
			}
			out = append(out, apitest.Fmt("%d %s %s [%s]", o.Line, strings.Join(who, ","), strings.Join(o.Files, ","), strings.Join(by, ",")))
		}
		return strings.Join(out, " | ")
	}
	load()
	if pr.CodeOwnersFile != ".kogge/CODEOWNERS" {
		t.Errorf("file = %q", pr.CodeOwnersFile)
	}
	want := "2 @anna .kogge/CODEOWNERS [] | 3 @Payments Team payments/pay.go [] | 4 nobody@example.com README.md [] | " +
		"6 @carol tools/run.sh [] | 7 @erin secret/key.txt []"
	if got := summary(); got != want {
		t.Errorf("owners:\n%s\nwant\n%s", got, want)
	}
	var reviewers []string
	for _, r := range pr.Reviewers {
		reviewers = append(reviewers, r.User.Username+map[bool]string{true: "*"}[r.CodeOwner])
	}
	if strings.Join(reviewers, ",") != "anna*" {
		t.Errorf("reviewers = %v (the author and those who cannot read are left out)", reviewers)
	}
	if got := check(); got != "waiting:5" {
		t.Errorf("check = %s", got)
	}
	// The group's member heard of it; olga, who owns nothing, did not.
	inbox := func(name string) []string {
		var list []struct {
			Event string `json:"event"`
		}
		users[name].Get("/api/v1/notifications", &list)
		var out []string
		for _, n := range list {
			out = append(out, n.Event)
		}
		return out
	}
	if got := inbox("ben"); !slices.Contains(got, "pull-request.opened") {
		t.Errorf("ben's inbox = %v", got)
	}
	if got := inbox("olga"); len(got) != 0 {
		t.Errorf("olga's inbox = %v", got)
	}

	// anna approves her files, ben those of his group; the author cannot approve tools, nobody the README.
	review := func(name string) {
		t.Helper()
		users[name].MustDo(http.MethodPut, "/api/v1/code/repos/OWN/shop/pull-requests/1/review", map[string]any{"state": "approved"}, nil)
	}
	review("anna")
	review("ben")
	review("olga")
	load()
	if got := check(); got != "waiting:3" {
		t.Errorf("check after approvals = %s (%s)", got, summary())
	}
	for _, r := range pr.Reviewers {
		if r.CodeOwner != (r.User.Username != "olga") {
			t.Errorf("%s is code owner: %v", r.User.Username, r.CodeOwner)
		}
	}
	if r := carol.Do(http.MethodPost, "/api/v1/code/repos/OWN/shop/pull-requests/1/merge", map[string]any{"strategy": "merge"}); r.Status != http.StatusConflict ||
		!strings.Contains(string(r.Body), "3 files wait for the approval of a code owner") {
		t.Errorf("merge without the owners: %d %s", r.Status, r.Body)
	}

	// The file on the target is fixed: the pull request takes the new owners.
	admin.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "path": ".kogge/CODEOWNERS",
		"content": strings.NewReplacer("nobody@example.com", "@anna", "@carol", "@carol @olga", "@erin", "@olga").Replace(ownersFile)}, nil)
	env.WorkJobs()
	load()
	if got := check(); got != "approved:0" {
		t.Errorf("check after the fix = %s (%s)", got, summary())
	}
	carol.MustDo(http.MethodPost, "/api/v1/code/repos/OWN/shop/pull-requests/1/merge", map[string]any{"strategy": "merge"}, nil)

	// A draft asks nobody; once it is ready, the owners are asked (main's owners now: * @carol, /tools/ @carol @olga).
	carol.MustDo(http.MethodPut, files, map[string]any{"branch": "main", "newBranch": "draft", "path": "payments/refund.go",
		"content": "package payments\n"}, nil)
	carol.MustDo(http.MethodPut, files, map[string]any{"branch": "draft", "path": "tools/build.sh", "content": "echo build\n"}, nil)
	env.WorkJobs()
	before := len(inbox("ben"))
	carol.MustDo(http.MethodPost, "/api/v1/code/repos/OWN/shop/pull-requests", map[string]any{"title": "Refunds", "source": "draft",
		"draft": true}, nil)
	var second struct {
		Reviewers []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"reviewers"`
	}
	admin.Get("/api/v1/code/repos/OWN/shop/pull-requests/2", &second)
	if len(second.Reviewers) != 0 || len(inbox("ben")) != before {
		t.Errorf("a draft asked its owners: %+v, ben's inbox %v", second.Reviewers, inbox("ben"))
	}
	carol.MustDo(http.MethodPatch, "/api/v1/code/repos/OWN/shop/pull-requests/2", map[string]any{"draft": false}, nil)
	admin.Get("/api/v1/code/repos/OWN/shop/pull-requests/2", &second)
	if len(second.Reviewers) != 1 || second.Reviewers[0].User.Username != "olga" {
		t.Errorf("reviewers once ready = %+v", second.Reviewers)
	}
	for _, name := range []string{"ben", "olga"} {
		if got := inbox(name); !slices.Contains(got, "pull-request.code-owner") {
			t.Errorf("%s was not asked: %v", name, got)
		}
	}

	// The file as Kogge reads it, and the owners of a path.
	type ownersFileInfo struct {
		Path     string `json:"path"`
		Rules    []owned
		Problems []struct {
			Line    int               `json:"line"`
			Code    string            `json:"code"`
			Values  map[string]string `json:"values"`
			Message string            `json:"message"`
		} `json:"problems"`
		Owners *owned `json:"owners"`
	}
	var info ownersFileInfo
	admin.Get("/api/v1/code/repos/OWN/shop/code-owners?path=payments/pay.go&ref=feature", &info)
	if info.Path != ".kogge/CODEOWNERS" || len(info.Rules) != 6 || info.Owners == nil || len(info.Owners.Groups) != 1 ||
		len(info.Problems) != 1 || info.Problems[0].Line != 4 || info.Problems[0].Code != "unknown-owner" ||
		info.Problems[0].Values["name"] != "nobody@example.com" || !strings.Contains(info.Problems[0].Message, "nobody@example.com") {
		t.Errorf("CODEOWNERS of feature = %+v", info)
	}
	info = ownersFileInfo{}
	admin.Get("/api/v1/code/repos/OWN/shop/code-owners?path=vendor/lib.go", &info)
	if info.Owners != nil || len(info.Problems) != 0 {
		t.Errorf("vendor has no owners: %+v", info)
	}
	// Who cannot read the repository cannot tell it from one that does not exist.
	outsider := env.CreateUser("outsider")
	r1 := outsider.Do(http.MethodGet, "/api/v1/code/repos/OWN/shop/code-owners", nil)
	r2 := outsider.Do(http.MethodGet, "/api/v1/code/repos/OWN/nothing/code-owners", nil)
	if r1.Status != http.StatusNotFound || r1.Status != r2.Status || r1.Code() != r2.Code() {
		t.Errorf("outsider: %d %s, nothing: %d %s", r1.Status, r1.Code(), r2.Status, r2.Code())
	}
}
