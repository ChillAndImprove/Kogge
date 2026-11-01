// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package backup_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/backup"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// The fixture testdata/kogge-backup-00013.tar is a backup that an old Kogge wrote: the build of commit 0a8b7a5,
// the first with backups, whose schema ends with migration 00013 (River's schema: version 8). It was made like
// this, and can be made again the same way:
//
//	git archive 0a8b7a5 | tar -x -C old && cd old && go build -o kogge ./cmd/kogge && go build -o kogge-agent ./cmd/kogge-agent
//	kogge admin gen-master-key > master.key        # testdata/kogge-backup-00013.master-key
//	KOGGE_DATABASE_URL=… (an empty database) KOGGE_DATA_DIR=data KOGGE_MASTER_KEY_FILE=master.key KOGGE_AUTO_MIGRATE=true \
//	  KOGGE_BOOTSTRAP_ADMIN_USERNAME=admin KOGGE_BOOTSTRAP_ADMIN_PASSWORD=Fixture-Root-Secret-1 kogge serve
//
// Then, through that server's API (testdata/mkfixture.go does it): the user bob (password Fixture-User-Secret-1);
// the project SHOP (template scrum, the instance's language German) with bob as developer and the version 1.0; the
// task SHOP-1 with two attachments, fix version 1.0, assignee bob and the label payments set in one change, the
// transition "Bearbeitung beginnen", a comment of bob and a comment of admin restricted to the role Administratoren
// (it names "zanzibar"); the sub-task SHOP-2; the open space DOCS with the page Runbook published twice and bob's
// draft of a third version; the code project PLAT with the repository shop (with a README), whose commit of
// .kogge/pipelines.yml ("SHOP-1 Build the shop") started a run that the old agent ran; the automation rule "Note
// merges" of SHOP; last, the webhook "CI mirror" of the repository. The agent between "data" and "webhook":
//
//	kogge-agent register -server http://127.0.0.1:18130 -token TOKEN -name fixture-agent -shell -config agent.json
//	kogge-agent run -config agent.json               # until mkfixture.go runs reports succeeded, then stop it
//	kogge admin backup -o kogge-backup-00013.tar     # with the server's settings, while it runs
const oldBackup = "testdata/kogge-backup-00013.tar"

// TestRestoreOldBackup: a backup written by an old version of Kogge restores into the current one (the schema of
// the backup, its rows and files, then the migrations since), fsck finds nothing, and the restored installation
// works with its data: what the migrations since then derive from it is there, and Git, issues and pages can be
// changed.
func TestRestoreOldBackup(t *testing.T) {
	ctx := context.Background()
	m, sum, err := backup.ReadManifest(oldBackup)
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema.Schema != 13 || m.Kogge != "dev" || sum.Repositories != 1 || sum.Blobs != 2 || sum.Logs != 1 {
		t.Fatalf("not the fixture described above: %+v %+v", m.Schema, sum)
	}

	dstURL := pgtest.NewEmpty(t)
	pool, err := db.Open(ctx, dstURL, "kogge-restore-test", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "master.key")
	key, err := os.ReadFile("testdata/kogge-backup-00013.master-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	kr, err := crypto.LoadKeyring(keyFile, dir)
	if err != nil {
		t.Fatal(err)
	}
	report, err := backup.Restore(ctx, backup.Options{Pool: pool, DataDir: dir, Keyring: kr}, oldBackup,
		backup.RestoreOptions{KeyFile: keyFile})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(report.Problems) > 0 || report.Repositories != 1 || report.Blobs != 2 || report.Logs != 1 {
		t.Fatalf("fsck after the restore: %+v", report)
	}
	current, err := db.CurrentVersions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if latest, err := db.LatestVersions(pool); err != nil || current != latest {
		t.Fatalf("restored schema %+v, this version's %+v (%v)", current, latest, err)
	}
	// A stricter fsck: the blobs' contents and the repository's objects are intact.
	if report, err := backup.Check(ctx, backup.Options{Pool: pool, DataDir: dir, Keyring: kr},
		backup.CheckOptions{Hashes: true, Git: true}); err != nil || len(report.Problems) > 0 {
		t.Fatalf("fsck -hashes -git: %+v %v", report, err)
	}

	env := apitest.New(t, func(c *config.Config) {
		c.DatabaseURL, c.DataDir, c.MasterKeyFile = dstURL, dir, keyFile
	})
	admin := env.Login("admin", "Fixture-Root-Secret-1")
	bob := env.Login("bob", "Fixture-User-Secret-1")

	// The issue with its attachments, its fields and its history.
	var issue struct {
		Summary string `json:"summary"`
		Status  struct {
			Name string `json:"name"`
		} `json:"status"`
		Assignee struct {
			Username string `json:"username"`
		} `json:"assignee"`
		Labels      []string `json:"labels"`
		FixVersions []struct {
			Name string `json:"name"`
		} `json:"fixVersions"`
		Subtasks []struct {
			Key string `json:"key"`
		} `json:"subtasks"`
		Attachments []struct {
			Filename string `json:"filename"`
			URL      string `json:"url"`
		} `json:"attachments"`
	}
	bob.Get("/api/v1/issues/SHOP-1", &issue)
	if !strings.HasPrefix(issue.Summary, "Checkout: Grüße, \"quotes\", tabs\tand\nnew lines \\ backslashes") ||
		issue.Status.Name != "In Arbeit" || issue.Assignee.Username != "bob" || !slices.Equal(issue.Labels, []string{"payments"}) ||
		len(issue.FixVersions) != 1 || issue.FixVersions[0].Name != "1.0" || len(issue.Subtasks) != 1 || issue.Subtasks[0].Key != "SHOP-2" {
		t.Errorf("SHOP-1 = %+v", issue)
	}
	var contents []string
	for _, a := range issue.Attachments {
		r := bob.Do(http.MethodGet, a.URL, nil)
		contents = append(contents, a.Filename+": "+string(r.Body))
	}
	slices.Sort(contents)
	if !slices.Equal(contents, []string{"more.txt: another attachment", "notes.txt: hello attachment"}) {
		t.Errorf("attachments = %q", contents)
	}
	var history []struct {
		Items []struct {
			Field    string `json:"field"`
			ToString string `json:"toString"`
		} `json:"items"`
	}
	bob.Get("/api/v1/issues/SHOP-1/history", &history)
	var changes []string
	for _, h := range history {
		for _, it := range h.Items {
			changes = append(changes, it.Field+"="+it.ToString)
		}
	}
	for _, want := range []string{"fixVersions=1.0", "assignee=Bob Builder", "labels=payments", "status=In Arbeit"} {
		if !slices.Contains(changes, want) {
			t.Errorf("history %q lacks %s", changes, want)
		}
	}

	// The comment restricted to the administrators: bob does not see it, and since migration 00017 its words no
	// longer find the issue (the old search text had them).
	var comments []struct {
		VisibilityType string `json:"visibilityType"`
	}
	bob.Get("/api/v1/issues/SHOP-1/comments", &comments)
	if len(comments) != 1 || comments[0].VisibilityType != "" {
		t.Errorf("bob's comments = %+v", comments)
	}
	admin.Get("/api/v1/issues/SHOP-1/comments", &comments)
	if len(comments) != 2 {
		t.Errorf("admin's comments = %+v", comments)
	}
	search := func(c *apitest.Client, kql string) []string {
		t.Helper()
		var res struct {
			Issues []struct {
				Key string `json:"key"`
			} `json:"issues"`
		}
		c.Get("/api/v1/search?q="+apitest.Query(kql), &res)
		var keys []string
		for _, i := range res.Issues {
			keys = append(keys, i.Key)
		}
		return keys
	}
	if keys := search(bob, `text ~ "zanzibar"`); len(keys) != 0 {
		t.Errorf("the restricted comment's word finds %v", keys)
	}
	if keys := search(bob, `text ~ "payment provider"`); !slices.Equal(keys, []string{"SHOP-1"}) {
		t.Errorf("the description's words find %v", keys)
	}

	// The field spans of KQL's WAS (migration 00040) are derived from the old history in the background.
	if r := bob.Do(http.MethodGet, "/api/v1/search?q="+apitest.Query(`status WAS "Offen"`), nil); r.Status != http.StatusBadRequest &&
		r.Status != http.StatusUnprocessableEntity {
		t.Errorf("WAS before the spans are derived: %d %s", r.Status, r.Body)
	}
	if err := env.App.Issues.DeriveSpans(ctx); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		`status WAS "Offen"`:              {"SHOP-1", "SHOP-2"},
		`status WAS "In Arbeit" BY admin`: {"SHOP-1"},
		`assignee WAS bob`:                {"SHOP-1"},
		// The old history names fix versions by their names.
		`fixVersion WAS "1.0"`: {"SHOP-1"},
	} {
		got := search(bob, q+" ORDER BY key")
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", q, got, want)
		}
	}

	// The page with its versions; bob's personal draft of the current version became the page's shared draft
	// (migration 00039).
	var pageID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM pages WHERE title = 'Runbook'").Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Version int `json:"version"`
		Body    struct {
			Content []any `json:"content"`
		} `json:"body"`
	}
	bob.Get(apitest.Fmt("/api/v1/wiki/pages/%d", pageID), &page)
	if page.Version != 2 || len(page.Body.Content) != 2 {
		t.Errorf("page = %+v", page)
	}
	var versions struct {
		Items []struct {
			Version int    `json:"version"`
			Message string `json:"message"`
		} `json:"items"`
	}
	bob.Get(apitest.Fmt("/api/v1/wiki/pages/%d/versions", pageID), &versions)
	if v := versions.Items; len(v) != 2 || v[0].Message != "Who to call" || v[1].Message != "First version" {
		t.Errorf("versions = %+v", versions)
	}
	var draft struct {
		BaseVersion int  `json:"baseVersion"`
		Changed     bool `json:"changed"`
		Doc         struct {
			Content []any `json:"content"`
		} `json:"doc"`
		Contributors []struct {
			Username string `json:"username"`
		} `json:"contributors"`
		EarlierDraft *struct{} `json:"earlierDraft"`
	}
	admin.Get(apitest.Fmt("/api/v1/wiki/pages/%d/edit", pageID), &draft)
	if draft.BaseVersion != 2 || !draft.Changed || len(draft.Doc.Content) != 3 || len(draft.Contributors) != 1 ||
		draft.Contributors[0].Username != "bob" || draft.EarlierDraft != nil {
		t.Errorf("shared draft = %+v", draft)
	}

	// The repository, the run the old agent ran and its log.
	if r := admin.Do(http.MethodGet, "/api/v1/code/repos/PLAT/shop/raw?ref=main&path=.kogge/pipelines.yml", nil); r.Status != http.StatusOK ||
		!strings.Contains(string(r.Body), "hello from the old agent") {
		t.Errorf("pipeline file: %d %s", r.Status, r.Body)
	}
	var runs struct {
		Items []struct {
			ID      int64  `json:"id"`
			State   string `json:"state"`
			Subject string `json:"subject"`
		} `json:"items"`
	}
	admin.Get("/api/v1/builds/runs", &runs)
	if len(runs.Items) != 1 || runs.Items[0].State != "succeeded" || runs.Items[0].Subject != "SHOP-1 Build the shop" {
		t.Fatalf("runs = %+v", runs)
	}
	var jobID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM run_jobs WHERE run_id = $1", runs.Items[0].ID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	var log struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	admin.Get(apitest.Fmt("/api/v1/builds/jobs/%d/log", jobID), &log)
	if !log.Done || !strings.Contains(log.Text, "hello from the old agent") {
		t.Errorf("log = %+v", log)
	}

	// What later migrations derived: the webhook and the automation rule act for their creator (00022, 00023); the
	// agent registered before approvals existed counts as approved (00024).
	var hooks []struct {
		Name  string `json:"name"`
		Owner struct {
			Username string `json:"username"`
		} `json:"owner"`
	}
	admin.Get("/api/v1/webhooks?scope=repository&key=PLAT/shop", &hooks)
	if len(hooks) != 1 || hooks[0].Name != "CI mirror" || hooks[0].Owner.Username != "admin" {
		t.Errorf("webhooks = %+v", hooks)
	}
	var rules []struct {
		Name  string `json:"name"`
		Owner struct {
			Username string `json:"username"`
		} `json:"owner"`
	}
	admin.Get("/api/v1/projects/SHOP/automation", &rules)
	if len(rules) != 1 || rules[0].Name != "Note merges" || rules[0].Owner.Username != "admin" {
		t.Errorf("automation rules = %+v", rules)
	}
	var agents []struct {
		Name     string `json:"name"`
		Approved bool   `json:"approved"`
	}
	admin.Get("/api/v1/admin/agents", &agents)
	if len(agents) != 1 || agents[0].Name != "fixture-agent" || !agents[0].Approved {
		t.Errorf("agents = %+v", agents)
	}
	// The webhook's secret was sealed with the old installation's master key, which the restored one reads.
	var hookID int64
	var sealed string
	if err := pool.QueryRow(ctx, "SELECT id, secret_sealed FROM webhooks").Scan(&hookID, &sealed); err != nil {
		t.Fatal(err)
	}
	if secret, err := kr.Open(sealed, apitest.Fmt("webhook-secret:%d", hookID)); err != nil || string(secret) != "fixture-webhook-secret" {
		t.Errorf("webhook secret: %q %v", secret, err)
	}

	// The restored installation takes changes: a commit through the API (Git writes with the hooks of this
	// version), an issue, a page version.
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PLAT/shop/files", map[string]any{"branch": "main", "path": "NOTES.md",
		"content": "restored\n", "message": "SHOP-2 After the restore"}, nil)
	var repoID int64
	if err := pool.QueryRow(ctx, "SELECT id FROM repositories").Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "--git-dir="+code.RepoDir(dir, repoID), "log", "--format=%s", "main").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "SHOP-2 After the restore\nSHOP-1 Build the shop\n") {
		t.Errorf("git log: %v %s", err, out)
	}
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	var created struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[2].ID, "summary": "After the restore"}, &created)
	if created.Key != "SHOP-3" {
		t.Errorf("new issue %s", created.Key)
	}
	bob.MustDo(http.MethodPost, apitest.Fmt("/api/v1/wiki/pages/%d/publish", pageID), map[string]any{"title": "Runbook",
		"body": map[string]any{"type": "doc", "version": 1, "content": []any{map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": "Restored."}}}}}, "baseVersion": 2}, nil)
}
