// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/testutil/apitest"
)

func check(v *importer.Validation, id string) *importer.Check {
	for i := range v.Checks {
		if v.Checks[i].ID == id {
			return &v.Checks[i]
		}
	}
	return nil
}

// expectClean fails the test for every check that failed.
func expectClean(t *testing.T, v *importer.Validation) {
	t.Helper()
	for _, c := range v.Checks {
		if c.Status == importer.Fail {
			t.Errorf("%s failed: %s %v", c.ID, c.Message, c.Details)
		}
	}
	if v.Failed != 0 || v.Passed == 0 {
		t.Errorf("%d passed, %d failed", v.Passed, v.Failed)
	}
}

// expectFailure checks that a check failed with a detail that contains want.
func expectFailure(t *testing.T, v *importer.Validation, id, want string) {
	t.Helper()
	c := check(v, id)
	if c == nil || c.Status != importer.Fail || !strings.Contains(strings.Join(c.Details, "\n"), want) {
		t.Errorf("check %s did not fail with %q: %+v", id, want, c)
	}
}

func TestJiraValidation(t *testing.T) {
	env := apitest.New(t)
	env.CreateUser("mmuster")
	fake := fakeJira(t)
	work := t.TempDir()
	ctx := context.Background()
	j := importer.NewJira(importer.JiraOptions{URL: fake.URL, Token: jiraToken, Projects: []string{"SHOP"}, Workdir: work}, quiet())
	if err := j.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Log: quiet()}
	if _, err := j.Load(ctx, loader); err != nil {
		t.Fatal(err)
	}
	opt := importer.ValidateOptions{Language: importer.English, DataDir: env.App.Cfg.DataDir}
	v, err := j.Validate(ctx, loader, opt)
	if err != nil {
		t.Fatal(err)
	}
	expectClean(t, v)
	for _, id := range []string{"counts.issues", "counts.comments", "counts.issue-keys", "attachments.checksums", "links.integrity",
		"renderings.issues", "history.issues", "permissions.projects"} {
		if c := check(v, id); c == nil || c.Status != importer.Pass {
			t.Errorf("check %s: %+v", id, c)
		}
	}
	var stored importer.Validation
	if page := readReport(t, work, "jira-validation", &stored); !strings.Contains(page, "Validation of the Jira migration") || stored.Passed != v.Passed {
		t.Errorf("the report: %+v", stored)
	}

	// What the validation must catch: a missing comment, an issue with another reporter, a link to an issue that does
	// not exist, an extracted file that differs, and permissions that differ from the source's (here: the source
	// let only the Administrators role browse).
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.App.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("DELETE FROM issue_comments WHERE body_text LIKE 'Internal note%'")
	exec("UPDATE issues SET reporter_id = (SELECT id FROM users WHERE username = 'admin') WHERE key = 'SHOP-3'")
	exec(`UPDATE issues SET description = '{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"issue","attrs":{"key":"NOPE-1"}}]}]}'
		WHERE key = 'SHOP-1'`)
	file := filepath.Join(work, "jira", "attachments", "10500")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(original, '!'), 0o640); err != nil {
		t.Fatal(err)
	}
	scheme := filepath.Join(work, "jira", "projects", "SHOP", "permissionscheme.json")
	raw, err := os.ReadFile(scheme)
	if err != nil {
		t.Fatal(err)
	}
	only := strings.Replace(string(raw), `{"permission":"BROWSE_PROJECTS","holder":{"type":"applicationRole","parameter":""}}`,
		`{"permission":"BROWSE_PROJECTS","holder":{"type":"projectRole","parameter":"10002","projectRole":{"name":"Administrators"}}}`, 1)
	if only == string(raw) {
		t.Fatal("the permission scheme has changed")
	}
	if err := os.WriteFile(scheme, []byte(only), 0o640); err != nil {
		t.Fatal(err)
	}
	v, err = j.Validate(ctx, loader, opt)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, v, "counts.comments", "SHOP-2 (20002)")
	expectFailure(t, v, "history.issues", `SHOP-3: the reporter is "admin" in Kogge, "mmuster" in the source`)
	expectFailure(t, v, "links.integrity", "the issue NOPE-1 does not exist, in SHOP-1")
	expectFailure(t, v, "renderings.issues", "SHOP-1: 3 of the 3 words of the source's text are missing")
	expectFailure(t, v, "attachments.checksums", "screenshot.png (10500): the extracted file has the SHA-256")
	expectFailure(t, v, "permissions.projects", "mmuster may see the project SHOP in Kogge, not in the source")
	if v.Failed < 6 {
		t.Errorf("only %d checks failed", v.Failed)
	}

	// A file of the blob store that was damaged, and an attachment that is gone.
	if err := os.WriteFile(file, original, 0o640); err != nil {
		t.Fatal(err)
	}
	var sha string
	var id int64
	if err := env.App.Pool.QueryRow(ctx, "SELECT id, sha256 FROM issue_attachments WHERE filename = 'screenshot.png'").Scan(&id, &sha); err != nil {
		t.Fatal(err)
	}
	stored0 := blob.FilePath(env.App.Cfg.DataDir, sha)
	if err := os.Chmod(stored0, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored0, []byte("damaged"), 0o640); err != nil {
		t.Fatal(err)
	}
	v, err = j.Validate(ctx, loader, opt)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, v, "attachments.checksums", "screenshot.png (10500): the stored file is damaged")
	if _, err := env.App.Pool.Exec(ctx, "DELETE FROM issue_attachments WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	v, err = j.Validate(ctx, loader, opt)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, v, "attachments.checksums", "screenshot.png (10500): not in Kogge")
}

func TestConfluenceValidation(t *testing.T) {
	env := apitest.New(t)
	env.CreateUser("mmuster")
	fake := fakeConfluence(t)
	work := t.TempDir()
	ctx := context.Background()
	c := importer.NewConfluence(importer.ConfluenceOptions{URL: fake.URL, Token: confluenceToken, Spaces: []string{"DOC"}, Workdir: work,
		JiraURL: "https://jira.example.com", History: true, PageSize: 2}, quiet())
	if err := c.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Wiki: env.App.Wiki, Log: quiet()}
	if _, err := c.Load(ctx, loader); err != nil {
		t.Fatal(err)
	}
	opt := importer.ValidateOptions{Language: importer.German, DataDir: env.App.Cfg.DataDir}
	v, err := c.Validate(ctx, loader, opt)
	if err != nil {
		t.Fatal(err)
	}
	expectClean(t, v)
	for _, id := range []string{"counts.pages", "attachments.checksums", "renderings.pages", "history.pages", "permissions.spaces", "permissions.pages"} {
		if c := check(v, id); c == nil || c.Status != importer.Pass {
			t.Errorf("check %s: %+v", id, c)
		}
	}
	// The issue SHOP-1 the install guide refers to is not in Kogge (Jira is not migrated here): links warn or fail.
	if c := check(v, "links.integrity"); c == nil {
		t.Error("no link check")
	}
	// The source lets mmuster read the restricted pages, Kogge does not.
	restrictions := filepath.Join(work, "confluence", "content", "1003", "restrictions.json")
	raw, err := os.ReadFile(restrictions)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(raw), `"results":[{"type":"known","username":"jdoe"`,
		`"results":[{"type":"known","username":"mmuster","userKey":"key-mmuster"},{"type":"known","username":"jdoe"`, 1)
	if changed == string(raw) {
		t.Fatalf("the restrictions have changed: %s", raw)
	}
	if err := os.WriteFile(restrictions, []byte(changed), 0o640); err != nil {
		t.Fatal(err)
	}
	v, err = c.Validate(ctx, loader, importer.ValidateOptions{Language: importer.English, DataDir: env.App.Cfg.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, v, "permissions.pages", `mmuster may see the page "DOC: Secret Plans" in the source, not in Kogge`)
}

func TestBitbucketValidation(t *testing.T) {
	env := apitest.New(t)
	env.CreateUser("mmuster")
	src := newBitbucket(t)
	work := t.TempDir()
	ctx := context.Background()
	bb := importer.NewBitbucket(importer.BitbucketOptions{URL: src.URL, Token: bbToken, Workdir: work}, quiet())
	if err := bb.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Code: env.App.Code, Log: quiet()}
	if _, err := bb.Load(ctx, loader); err != nil {
		t.Fatal(err)
	}
	opt := importer.ValidateOptions{Language: importer.English, DataDir: env.App.Cfg.DataDir}
	v, err := bb.Validate(ctx, loader, opt)
	if err != nil {
		t.Fatal(err)
	}
	expectClean(t, v)
	for _, id := range []string{"counts.repositories", "counts.pull-requests", "counts.comments", "counts.branch-permissions", "repositories.refs",
		"repositories.lfs", "history.pull-requests", "permissions.repositories"} {
		if c := check(v, id); c == nil || c.Status != importer.Pass {
			t.Errorf("check %s: %+v", id, c)
		}
	}
	// A branch of the source that moved after the load.
	bbGit(t, work, "--git-dir", "bitbucket/git/SHOP/web.git", "update-ref", "refs/heads/fix", src.commit("c1"))
	v, err = bb.Validate(ctx, loader, opt)
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, v, "repositories.refs", "SHOP/web: refs/heads/fix points to")
}

func TestBambooValidation(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SHOP", "name": "Shop"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SHOP/repos", map[string]any{"name": "web", "readme": true}, nil)
	src := fakeBamboo(t)
	work := t.TempDir()
	ctx := context.Background()
	bm := importer.NewBamboo(importer.BambooOptions{URL: src.URL, Token: bambooToken, Workdir: work,
		Repositories: map[string]string{"OPS-INFRA": "SHOP/web"}}, quiet())
	if err := bm.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Code: env.App.Code, Builds: env.App.Builds, Log: quiet()}
	if _, err := bm.Load(ctx, loader); err != nil {
		t.Fatal(err)
	}
	v, err := bm.Validate(ctx, loader, importer.ValidateOptions{Language: importer.English})
	if err != nil {
		t.Fatal(err)
	}
	expectClean(t, v)
	if c := check(v, "proposals.pipelines"); c == nil || c.Status != importer.Pass || !strings.Contains(c.Message, "The 2 plans") {
		t.Errorf("proposals: %+v", c)
	}
	if _, err := env.App.Pool.Exec(ctx, "DELETE FROM environments WHERE name = 'production'"); err != nil {
		t.Fatal(err)
	}
	v, err = bm.Validate(ctx, loader, importer.ValidateOptions{Language: importer.English})
	if err != nil {
		t.Fatal(err)
	}
	expectFailure(t, v, "proposals.environments", "SHOP/web: the environment production is missing")
}
