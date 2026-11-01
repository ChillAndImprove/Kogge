// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package migrations_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/backup"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/pgtest"
)

// fixtures are the schema versions of the rows in testdata/upgrade-NNNNN.sql: each file is written against the
// schema of its version and adds to the rows of the earlier ones, like an installation that was upgraded through
// them. Their secrets are sealed with testdata/upgrade.master-key; users have the password apitest.UserPassword.
var fixtures = []int64{9, 13, 24, 33, 40}

// TestUpgradeWithData: an installation of an older schema version with data comes to the current version, and the
// current code works with what it finds there — upgraded in place (kogge migrate) or restored from a backup taken
// at that version (kogge admin restore, which recreates the backup's schema, loads its rows and migrates). For every
// fixture version, an installation goes through the fixtures up to it (migrating to each, then adding its rows),
// comes to the current version and is checked through the API: the data is all there, and what the migrations
// since derived from it is right.
func TestUpgradeWithData(t *testing.T) {
	for _, start := range fixtures {
		for _, restore := range []bool{false, true} {
			name := fmt.Sprintf("migrate from %05d", start)
			if restore {
				name = fmt.Sprintf("restore from %05d", start)
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				upgrade(t, start, restore)
			})
		}
	}
}

// installation is an upgraded database with its data directory.
type installation struct {
	t     *testing.T
	url   string
	pool  *pgxpool.Pool
	dir   string
	key   string
	start int64
}

// has reports whether the rows of the fixture of a version are there.
func (in *installation) has(version int64) bool { return in.start >= version }

// grown returns base and what the fixtures of later versions add to it (by version), as far as this installation
// has them, sorted.
func (in *installation) grown(base []string, added map[int64][]string) []string {
	out := slices.Clone(base)
	for version, more := range added {
		if in.has(version) {
			out = append(out, more...)
		}
	}
	slices.Sort(out)
	return out
}

// open creates an empty database and data directory with the fixtures' master key.
func (in *installation) open() {
	t := in.t
	in.url, in.dir = pgtest.NewEmpty(t), t.TempDir()
	pool, err := db.Open(context.Background(), in.url, "kogge-upgrade-test", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	in.pool = pool
	in.key = filepath.Join(in.dir, "master.key")
	key, err := os.ReadFile("testdata/upgrade.master-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.key, key, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (in *installation) keyring() *crypto.Keyring {
	kr, err := crypto.LoadKeyring(in.key, in.dir)
	if err != nil {
		in.t.Fatal(err)
	}
	return kr
}

func upgrade(t *testing.T, start int64, restore bool) {
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)
	in := &installation{t: t, start: start}
	in.open()
	for _, version := range fixtures {
		if version > start {
			break
		}
		if err := db.MigrateTo(ctx, in.pool, logger, db.Versions{Schema: version}); err != nil {
			t.Fatalf("migrate to %d: %v", version, err)
		}
		if v, err := db.CurrentVersions(ctx, in.pool); err != nil || v.Schema != version {
			t.Fatalf("schema %+v, want %d (%v)", v, version, err)
		}
		sql, err := os.ReadFile(fmt.Sprintf("testdata/upgrade-%05d.sql", version))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := in.pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("fixture %05d: %v", version, err)
		}
		advanceSequences(t, in.pool)
	}
	in.files()

	if restore {
		// A backup of the installation at its old version, restored into an empty one of this version.
		file := filepath.Join(t.TempDir(), "kogge.tar")
		if _, err := backup.Write(ctx, backup.Options{Pool: in.pool, DataDir: in.dir, Keyring: in.keyring()}, file); err != nil {
			t.Fatalf("backup at %05d: %v", start, err)
		}
		if m, _, err := backup.ReadManifest(file); err != nil || m.Schema.Schema != start {
			t.Fatalf("manifest: %+v %v", m, err)
		}
		in.open()
		report, err := backup.Restore(ctx, backup.Options{Pool: in.pool, DataDir: in.dir, Keyring: in.keyring()}, file,
			backup.RestoreOptions{KeyFile: in.key})
		if err != nil {
			t.Fatalf("restore from %05d: %v", start, err)
		}
		if len(report.Problems) > 0 || report.Repositories != len(in.grown([]string{"shop", "legacy"}, map[int64][]string{24: {"mirror"}})) ||
			report.Logs != 1 {
			t.Fatalf("fsck after the restore: %+v", report)
		}
	} else if err := db.Migrate(ctx, in.pool, logger); err != nil {
		t.Fatalf("migrate from %d: %v", start, err)
	}
	current, err := db.CurrentVersions(ctx, in.pool)
	if err != nil {
		t.Fatal(err)
	}
	if latest, err := db.LatestVersions(in.pool); err != nil || current != latest {
		t.Fatalf("schema after the upgrade %+v, this version's %+v (%v)", current, latest, err)
	}

	env := apitest.New(t, func(c *config.Config) {
		c.DatabaseURL, c.DataDir, c.MasterKeyFile = in.url, in.dir, in.key
	})
	in.check09(env)
	for version, check := range map[int64]func(*apitest.Env){13: in.check13, 24: in.check24, 33: in.check33, 40: in.check40} {
		if in.has(version) {
			check(env)
		}
	}
}

// advanceSequences moves the identity sequences past the rows the fixtures inserted with their ids.
func advanceSequences(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `DO $$
DECLARE r record; m bigint;
BEGIN
    FOR r IN SELECT c.relname, a.attname FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
        WHERE a.attidentity <> '' AND c.relnamespace = current_schema()::regnamespace LOOP
        EXECUTE format('SELECT max(%I) FROM %I', r.attname, r.relname) INTO m;
        IF m IS NOT NULL THEN
            PERFORM setval(pg_get_serial_sequence(quote_ident(r.relname), r.attname), greatest(m,
                (SELECT last_value FROM pg_sequences WHERE schemaname = current_schema()
                    AND sequencename = split_part(pg_get_serial_sequence(quote_ident(r.relname), r.attname), '.', 2))));
        END IF;
    END LOOP;
END $$`)
	if err != nil {
		t.Fatalf("sequences: %v", err)
	}
}

// files creates what the data directory of the fixtures' installation holds: the repositories with the commits
// the rows name (made the same way every time, so their ids are always the same) and the log of the finished job.
func (in *installation) files() {
	t := in.t
	type commit struct {
		ref, author, email, date, message string
		files                             map[string]string
		parent                            string
	}
	repos := map[int64][]commit{
		1: {
			{ref: "refs/heads/main", author: "Alice Adler", email: "alice@example.com", date: "2026-01-05T10:00:00Z",
				message: "Initial commit", files: map[string]string{"README.md": "# shop\n"}},
			{ref: "refs/heads/feature/SHOP-1", author: "Bob Builder", email: "bob@example.com", date: "2026-01-06T11:00:00Z",
				message: "SHOP-1 Adapter for the second provider", parent: "refs/heads/main", files: map[string]string{"README.md": "# shop\n",
					"adapter.go": "package shop\n\n// Adapter talks to the second payment provider.\ntype Adapter struct{}\n"}},
		},
		2: {
			{ref: "refs/heads/main", author: "Alice Adler", email: "alice@example.com", date: "2026-01-04T10:00:00Z",
				message: "Legacy build", files: map[string]string{"README.md": "# legacy\n"}},
		},
	}
	want := map[string]string{"1 refs/heads/main": "bb4378685a62737ca0e66461a4d5b5ba9bc98bde",
		"1 refs/heads/feature/SHOP-1": "b1a580b7116917a5b73fbd6a4b8b9e117f971def", "2 refs/heads/main": "ba7925faee16cd1ce32c6b54511fa13c6899bd0d"}
	for id, commits := range repos {
		dir := code.RepoDir(in.dir, id)
		git := func(stdin string, env []string, args ...string) string {
			t.Helper()
			cmd := exec.Command("git", append([]string{"--git-dir=" + dir}, args...)...)
			cmd.Env = append(os.Environ(), env...)
			cmd.Stdin = strings.NewReader(stdin)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
			return strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("git", "init", "--quiet", "--bare", "--initial-branch=main", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		for _, c := range commits {
			var tree strings.Builder
			names := make([]string, 0, len(c.files))
			for name := range c.files {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				fmt.Fprintf(&tree, "100644 blob %s\t%s\n", git(c.files[name], nil, "hash-object", "-w", "--stdin"), name)
			}
			env := []string{"GIT_AUTHOR_NAME=" + c.author, "GIT_AUTHOR_EMAIL=" + c.email, "GIT_AUTHOR_DATE=" + c.date,
				"GIT_COMMITTER_NAME=" + c.author, "GIT_COMMITTER_EMAIL=" + c.email, "GIT_COMMITTER_DATE=" + c.date}
			args := []string{"commit-tree", git(tree.String(), nil, "mktree"), "-m", c.message}
			if c.parent != "" {
				args = append(args, "-p", git("", nil, "rev-parse", c.parent))
			}
			sha := git("", env, args...)
			if w := want[fmt.Sprint(id, " ", c.ref)]; sha != w {
				t.Fatalf("repository %d: %s is %s, the fixtures name %s", id, c.ref, sha, w)
			}
			git("", nil, "update-ref", c.ref, sha)
		}
	}
	if in.has(24) {
		// The imported repository, still empty.
		if out, err := exec.Command("git", "init", "--quiet", "--bare", "--initial-branch=main", code.RepoDir(in.dir, 3)).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	path := builds.LogFile(in.dir, 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("go test ./...\nok  shop 1s\n"), 0o640); err != nil {
		t.Fatal(err)
	}
}

func search(t *testing.T, c *apitest.Client, kql string) []string {
	t.Helper()
	var res struct {
		Issues []struct {
			Key string `json:"key"`
		} `json:"issues"`
	}
	c.Get("/api/v1/search?q="+apitest.Query(kql), &res)
	keys := []string{}
	for _, i := range res.Issues {
		keys = append(keys, i.Key)
	}
	return keys
}

// check09 checks the rows of the fixture of version 9 through the current API.
func (in *installation) check09(env *apitest.Env) {
	t := in.t
	ctx := context.Background()
	alice := env.Login("alice", apitest.UserPassword)
	bob := env.Login("bob", apitest.UserPassword)
	carol := env.Login("carol", apitest.UserPassword)
	if r := env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "dave",
		"password": apitest.UserPassword}); r.Status != http.StatusUnauthorized {
		t.Errorf("the disabled dave signs in: %d %s", r.Status, r.Body)
	}

	// SHOP-1 with its fields and its history.
	var issue struct {
		Summary string `json:"summary"`
		Status  struct {
			Name string `json:"name"`
		} `json:"status"`
		Resolution *struct {
			Name string `json:"name"`
		} `json:"resolution"`
		Assignee struct {
			Username string `json:"username"`
		} `json:"assignee"`
		Labels     []string `json:"labels"`
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
		FixVersions []struct {
			Name string `json:"name"`
		} `json:"fixVersions"`
		TimeSpent     int64          `json:"timeSpent"`
		Fields        map[string]any `json:"fields"`
		Votes         int            `json:"votes"`
		ClosedSprints []struct {
			Name string `json:"name"`
		} `json:"closedSprints"`
		Links []struct {
			Issue struct {
				Key string `json:"key"`
			} `json:"issue"`
		} `json:"links"`
	}
	bob.Get("/api/v1/issues/SHOP-1", &issue)
	var fixes []string
	for _, v := range issue.FixVersions {
		fixes = append(fixes, v.Name)
	}
	if issue.Summary != "Zweiten Zahlungsanbieter anbinden" || issue.Status.Name != "Erledigt" || issue.Resolution == nil ||
		issue.Resolution.Name != "Erledigt" || issue.Assignee.Username != "bob" || !slices.Equal(issue.Labels, []string{"payments"}) ||
		!slices.Equal(fixes, []string{"1.0", "2.0"}) || issue.TimeSpent != 7200 || issue.Fields["cf_1"] != 5.0 || issue.Votes != 1 ||
		len(issue.ClosedSprints) != 1 || issue.ClosedSprints[0].Name != "Sprint 1" || len(issue.Links) != 1 ||
		len(issue.Components) != 1 || issue.Components[0].Name != "Kasse" {
		t.Errorf("SHOP-1 = %+v", issue)
	}
	var history []struct {
		Actor struct {
			Username string `json:"username"`
		} `json:"actor"`
		Items []struct {
			Field      string `json:"field"`
			FromString string `json:"fromString"`
			ToString   string `json:"toString"`
		} `json:"items"`
	}
	bob.Get("/api/v1/issues/SHOP-1/history", &history)
	var changes []string
	for _, h := range history {
		for _, it := range h.Items {
			changes = append(changes, h.Actor.Username+" "+it.Field+" "+it.FromString+"→"+it.ToString)
		}
	}
	for _, want := range []string{"alice assignee →Bob Builder", "alice fixVersions →1.0", "bob status Offen→In Arbeit",
		"alice fixVersions 1.0→1.0, 2.0", "bob status In Arbeit→Erledigt", "bob resolution →Erledigt"} {
		if !slices.Contains(changes, want) {
			t.Errorf("the history %q lacks %q", changes, want)
		}
	}

	// The restricted comment: bob is in its role, carol not; since 00017 its words find nothing, not even for bob.
	var comments []struct {
		VisibilityName string `json:"visibilityName"`
	}
	bob.Get("/api/v1/issues/SHOP-1/comments", &comments)
	if len(comments) != 2 {
		t.Errorf("bob's comments = %+v", comments)
	}
	carol.Get("/api/v1/issues/SHOP-1/comments", &comments)
	if len(comments) != 1 {
		t.Errorf("carol's comments = %+v", comments)
	}
	for _, c := range []*apitest.Client{bob, carol} {
		if keys := search(t, c, `text ~ "zanzibar"`); len(keys) != 0 {
			t.Errorf("%s finds the restricted comment's word in %v", c.Username, keys)
		}
	}
	if keys := search(t, carol, `text ~ "Adapter"`); !slices.Equal(keys, []string{"SHOP-1"}) {
		t.Errorf("the public comment's word finds %v", keys)
	}

	// SHOP-2 is secured; its sub-task SHOP-3, created without a level, took its parent's (00021).
	var sub struct {
		SecurityLevel *struct {
			Name string `json:"name"`
		} `json:"securityLevel"`
		Parent struct {
			Key string `json:"key"`
		} `json:"parent"`
	}
	bob.Get("/api/v1/issues/SHOP-3", &sub)
	if sub.SecurityLevel == nil || sub.SecurityLevel.Name != "Nur Team" || sub.Parent.Key != "SHOP-2" {
		t.Errorf("SHOP-3 = %+v", sub)
	}
	for _, key := range []string{"SHOP-2", "SHOP-3"} {
		if r := carol.Do(http.MethodGet, "/api/v1/issues/"+key, nil); r.Status != http.StatusNotFound {
			t.Errorf("carol sees the secured %s: %d", key, r.Status)
		}
	}
	if keys := search(t, carol, "project = SHOP ORDER BY key"); !slices.Equal(keys, in.grown([]string{"SHOP-1"},
		map[int64][]string{24: {"SHOP-4"}, 40: {"SHOP-6"}})) {
		t.Errorf("carol finds %v", keys)
	}

	// WAS works on the old history once the spans are derived (00040 leaves that to the background).
	if r := bob.Do(http.MethodGet, "/api/v1/search?q="+apitest.Query(`status WAS "In Arbeit"`), nil); r.Status < 400 {
		t.Errorf("WAS before the spans are derived: %d %s", r.Status, r.Body)
	}
	if err := env.App.Issues.DeriveSpans(ctx); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		`status WAS "In Arbeit" BY bob`: in.grown([]string{"SHOP-1"}, map[int64][]string{24: {"SHOP-4"}}),
		`status WAS "Offen"`:            in.grown([]string{"SHOP-1", "SHOP-2", "SHOP-3"}, map[int64][]string{24: {"SHOP-4", "SHOP-5"}, 40: {"SHOP-6"}}),
		// SHOP-4 had bob from its creation by alice.
		`assignee WAS bob BY alice`: in.grown([]string{"SHOP-1"}, map[int64][]string{24: {"SHOP-4"}}),
		`fixVersion WAS "1.0"`:      {"SHOP-1"},
		`fixVersion WAS "2.0"`:      in.grown([]string{"SHOP-1"}, map[int64][]string{24: {"SHOP-4"}, 40: {"SHOP-6"}}),
		`resolution WAS "Erledigt"`: {"SHOP-1"},
		`status CHANGED BY bob`:     in.grown([]string{"SHOP-1"}, map[int64][]string{24: {"SHOP-4"}}),
	} {
		got := search(t, bob, q+" ORDER BY key")
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", q, got, want)
		}
	}

	// The board with its sprints, and the saved filter.
	var sprints []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	bob.Get("/api/v1/boards/1/sprints", &sprints)
	if len(sprints) != 2 {
		t.Errorf("sprints = %+v", sprints)
	}
	var report map[string]any
	bob.Get("/api/v1/sprints/1/report", &report)
	var filter struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}
	alice.Get("/api/v1/filters/1", &filter)
	if filter.Name != "Offene Fehler" {
		t.Errorf("filter = %+v", filter)
	}
	if keys := search(t, alice, filter.Query); !slices.Equal(keys, []string{"SHOP-2"}) {
		t.Errorf("the saved filter finds %v", keys)
	}

	// The wiki: versions, the restricted page, the trash, and the drafts. The newest personal draft of the
	// current version became the shared draft (alice's); bob's stays his, as does carol's of an older version.
	var page struct {
		Version int      `json:"version"`
		Title   string   `json:"title"`
		Labels  []string `json:"labels"`
	}
	carol.Get("/api/v1/wiki/pages/2", &page)
	if page.Version != 2 || page.Title != "Runbook" || !slices.Equal(page.Labels, []string{"betrieb"}) {
		t.Errorf("page = %+v", page)
	}
	var versions struct {
		Items []struct {
			Message string `json:"message"`
		} `json:"items"`
	}
	carol.Get("/api/v1/wiki/pages/2/versions", &versions)
	if v := versions.Items; len(v) != 2 || v[0].Message != "Who to call" {
		t.Errorf("versions = %+v", versions)
	}
	if r := bob.Do(http.MethodGet, "/api/v1/wiki/pages/4", nil); r.Status != http.StatusNotFound {
		t.Errorf("bob sees the restricted page: %d", r.Status)
	}
	alice.Get("/api/v1/wiki/pages/4", &page)
	var trash struct {
		Items []struct {
			Title    string `json:"title"`
			Children int    `json:"children"`
		} `json:"items"`
	}
	alice.Get("/api/v1/wiki/spaces/DOCS/trash", &trash)
	var trashed []string
	for _, p := range trash.Items {
		trashed = append(trashed, fmt.Sprintf("%s (%d)", p.Title, p.Children))
	}
	slices.Sort(trashed)
	if want := in.grown([]string{"Altes Konzept (0)"}, map[int64][]string{33: {"Interna (1)"}}); !slices.Equal(trashed, want) {
		t.Errorf("trash = %v, want %v", trashed, want)
	}
	type session struct {
		BaseVersion  int  `json:"baseVersion"`
		Changed      bool `json:"changed"`
		Contributors []struct {
			Username string `json:"username"`
		} `json:"contributors"`
		Doc struct {
			Content []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		} `json:"doc"`
		EarlierDraft *struct {
			Title       string `json:"title"`
			BaseVersion int    `json:"baseVersion"`
		} `json:"earlierDraft"`
	}
	var s session
	alice.Get("/api/v1/wiki/pages/2/edit", &s)
	if s.BaseVersion != 2 || !s.Changed || len(s.Contributors) != 1 || s.Contributors[0].Username != "alice" ||
		len(s.Doc.Content) != 3 || s.Doc.Content[2].Content[0].Text != "Alice: and the provider." || s.EarlierDraft != nil {
		t.Errorf("alice's session = %+v", s)
	}
	s = session{}
	bob.Get("/api/v1/wiki/pages/2/edit", &s)
	if s.EarlierDraft == nil || s.EarlierDraft.BaseVersion != 2 {
		t.Errorf("bob's session = %+v", s)
	}
	s = session{}
	carol.Get("/api/v1/wiki/pages/2/edit", &s)
	if s.EarlierDraft == nil || s.EarlierDraft.Title != "Runbook (Carol)" || s.EarlierDraft.BaseVersion != 1 {
		t.Errorf("carol's session = %+v", s)
	}
	// The page search finds pages by their search text of that time, the restricted page only for alice.
	pageSearch := func(c *apitest.Client, q string) []string {
		t.Helper()
		var res struct {
			Pages []struct {
				Title string `json:"title"`
			} `json:"pages"`
		}
		c.Get("/api/v1/wiki/search?q="+apitest.Query(q), &res)
		titles := []string{}
		for _, p := range res.Pages {
			titles = append(titles, p.Title)
		}
		return titles
	}
	for _, c := range []struct {
		who  *apitest.Client
		q    string
		want []string
	}{{carol, "restart", []string{"Runbook"}}, {carol, "Danke", []string{"Runbook"}}, {carol, "alice", []string{}},
		{alice, "alice", []string{"Zugangsdaten"}}} {
		if got := pageSearch(c.who, c.q); !slices.Equal(got, c.want) {
			t.Errorf("%s searching pages for %q finds %v, want %v", c.who.Username, c.q, got, c.want)
		}
	}

	// Code: the repositories, the moved one by its old address, the pull request with its diff.
	var repos struct {
		Items []struct {
			Slug string `json:"slug"`
		} `json:"items"`
	}
	bob.Get("/api/v1/code/repos", &repos)
	var slugs []string
	for _, r := range repos.Items {
		slugs = append(slugs, r.Slug)
	}
	slices.Sort(slugs)
	if !slices.Equal(slugs, in.grown([]string{"legacy", "shop"}, map[int64][]string{24: {"mirror"}})) {
		t.Errorf("bob's repositories = %v", slugs)
	}
	if r := bob.Do(http.MethodGet, "/api/v1/code/repos/PLAT/shop/raw?ref=main&path=README.md", nil); r.Status != http.StatusOK ||
		string(r.Body) != "# shop\n" {
		t.Errorf("README: %d %s", r.Status, r.Body)
	}
	var pr struct {
		Title string `json:"title"`
		State string `json:"state"`
	}
	bob.Get("/api/v1/code/repos/PLAT/shop/pull-requests/1", &pr)
	if pr.State != "open" {
		t.Errorf("pull request = %+v", pr)
	}
	if r := bob.Do(http.MethodGet, "/api/v1/code/repos/PLAT/shop/pull-requests/1/diff", nil); r.Status != http.StatusOK ||
		!strings.Contains(string(r.Body), "adapter.go") {
		t.Errorf("diff: %d %s", r.Status, r.Body)
	}
	var keys []struct {
		Fingerprint string `json:"fingerprint"`
	}
	bob.Get("/api/v1/me/ssh-keys", &keys)
	if len(keys) != 1 || keys[0].Fingerprint != "SHA256:NqRVVI6Y/5RhjJ65+xKwHZu5jcB71ypSJHwCi6FG+fA" {
		t.Errorf("ssh keys = %+v", keys)
	}
	// The access token of that time still works.
	token := env.WithToken("kgp_fixture-token-of-bob")
	token.Get("/api/v1/issues/SHOP-1", &issue)

	// Builds: the run of the moved repository belongs to its project now (00025): carol, who may read OLD only,
	// no longer sees it.
	type runs struct {
		Items []struct {
			ID         int64  `json:"id"`
			State      string `json:"state"`
			Repository struct {
				Slug string `json:"slug"`
			} `json:"repository"`
		} `json:"items"`
	}
	var r runs
	bob.Get("/api/v1/builds/runs", &r)
	if want := len(in.grown([]string{"shop #1", "legacy #1"}, map[int64][]string{24: {"shop #2"}, 33: {"shop #3"}, 40: {"legacy #2"}})); len(r.Items) != want {
		t.Errorf("bob's runs = %+v, want %d", r, want)
	}
	r = runs{}
	carol.Get("/api/v1/builds/runs", &r)
	if len(r.Items) != 0 {
		t.Errorf("carol's runs = %+v", r)
	}
	var log struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	bob.Get("/api/v1/builds/jobs/1/log", &log)
	if !log.Done || log.Text != "go test ./...\nok  shop 1s\n" {
		t.Errorf("log = %+v", log)
	}
	var agents []struct {
		Name     string `json:"name"`
		Approved bool   `json:"approved"`
	}
	alice.Get("/api/v1/admin/agents", &agents)
	if i := slices.IndexFunc(agents, func(a struct {
		Name     string `json:"name"`
		Approved bool   `json:"approved"`
	}) bool {
		return a.Name == "build-01"
	}); i < 0 || !agents[i].Approved {
		t.Errorf("agents = %+v", agents)
	}
	var sealed string
	if err := in.pool.QueryRow(ctx, "SELECT sealed FROM build_variables WHERE name = 'DEPLOY_TOKEN'").Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if v, err := in.keyring().Open(sealed, "build-secret:repository:1:DEPLOY_TOKEN"); err != nil || string(v) != "deploy-token-123" {
		t.Errorf("secret variable: %q %v", v, err)
	}

	// The administration: settings keep their values and take defaults for keys added later; the group a
	// migration created is marked as imported (00027); the audit log has the old events.
	var settings struct {
		Name         string `json:"name"`
		MaxTokenDays int    `json:"maxTokenDays"`
	}
	alice.Get("/api/v1/admin/settings", &settings)
	if settings.Name != "Werft" || settings.MaxTokenDays != 365 {
		t.Errorf("settings = %+v", settings)
	}
	var imported bool
	if err := in.pool.QueryRow(ctx, "SELECT imported FROM groups WHERE name = 'Altbestand'").Scan(&imported); err != nil || !imported {
		t.Errorf("Altbestand imported: %v %v", imported, err)
	}
	var audit []struct {
		Action string `json:"action"`
	}
	alice.Get("/api/v1/admin/audit?action=project.create", &audit)
	if len(audit) != 1 {
		t.Errorf("audit = %+v", audit)
	}

	// Kogge goes on from the old rows: new issues get the next numbers, the old issue changes, pages get versions.
	var created struct {
		Key string `json:"key"`
	}
	bob.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": 3, "summary": "Nach dem Upgrade"}, &created)
	if created.Key != fmt.Sprintf("SHOP-%d", 4+in.issuesAdded()) {
		t.Errorf("new issue %s", created.Key)
	}
	var transitions []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	bob.Get("/api/v1/issues/SHOP-1/transitions", &transitions)
	for _, tr := range transitions {
		if tr.Name == "Wieder öffnen" {
			bob.MustDo(http.MethodPost, "/api/v1/issues/SHOP-1/transitions", map[string]any{"transitionId": tr.ID}, nil)
		}
	}
	var reopened struct {
		Status struct {
			Name string `json:"name"`
		} `json:"status"`
		Resolution *struct{} `json:"resolution"`
	}
	bob.Get("/api/v1/issues/SHOP-1", &reopened)
	if reopened.Status.Name != "Offen" || reopened.Resolution != nil {
		t.Errorf("SHOP-1 after reopening = %+v", reopened)
	}
	alice.MustDo(http.MethodPost, "/api/v1/wiki/pages/2/publish", map[string]any{"title": "Runbook",
		"body": map[string]any{"type": "doc", "version": 1, "content": []any{map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": "After the upgrade."}}}}}, "baseVersion": 2}, nil)
}

// issuesAdded counts the issues of SHOP that fixtures after version 9 add.
func (in *installation) issuesAdded() int {
	return len(in.grown(nil, map[int64][]string{24: {"SHOP-4", "SHOP-5"}, 40: {"SHOP-6"}}))
}

// check13 checks the rows of the fixture of version 13.
func (in *installation) check13(env *apitest.Env) {
	t := in.t
	alice := env.Login("alice", apitest.UserPassword)
	bob := env.Login("bob", apitest.UserPassword)

	// The webhook and the automation rule act for who created them (00022, 00023). The delivery from before
	// deliveries recorded their subject keeps its payload to itself.
	var hooks []struct {
		ID    int64 `json:"id"`
		Owner *struct {
			Username string `json:"username"`
		} `json:"owner"`
	}
	alice.Get("/api/v1/webhooks?scope=repository&key=PLAT/shop", &hooks)
	if len(hooks) != 1 || hooks[0].Owner == nil || hooks[0].Owner.Username != "alice" {
		t.Errorf("webhooks = %+v", hooks)
	}
	var deliveries []struct {
		State         string          `json:"state"`
		Payload       json.RawMessage `json:"payload"`
		PayloadHidden bool            `json:"payloadHidden"`
	}
	alice.Get("/api/v1/webhooks/1/deliveries", &deliveries)
	if len(deliveries) != 1 || deliveries[0].State != "succeeded" || !deliveries[0].PayloadHidden || deliveries[0].Payload != nil {
		t.Errorf("deliveries = %+v", deliveries)
	}
	var rules []struct {
		Name  string `json:"name"`
		Owner *struct {
			Username string `json:"username"`
		} `json:"owner"`
		LastRuns []struct {
			Result string `json:"result"`
		} `json:"lastRuns"`
	}
	alice.Get("/api/v1/projects/SHOP/automation", &rules)
	if len(rules) != 1 || rules[0].Owner == nil || rules[0].Owner.Username != "alice" || len(rules[0].LastRuns) != 1 {
		t.Errorf("automation rules = %+v", rules)
	}

	// The project's space, and bob's e-mail preferences.
	var space struct {
		Space *struct {
			Key string `json:"key"`
		} `json:"space"`
	}
	bob.Get("/api/v1/projects/SHOP/space", &space)
	if space.Space == nil || space.Space.Key != "DOCS" {
		t.Errorf("project space = %+v", space)
	}
	var prefs struct {
		Preferences []struct {
			Category string `json:"category"`
			Mode     string `json:"mode"`
		} `json:"preferences"`
	}
	bob.Get("/api/v1/me/notification-preferences", &prefs)
	modes := map[string]string{}
	for _, p := range prefs.Preferences {
		modes[p.Category] = p.Mode
	}
	if modes["issues"] != "daily" || modes["builds"] != "off" {
		t.Errorf("preferences = %+v", prefs)
	}
}

// check24 checks the rows of the fixture of version 24.
func (in *installation) check24(env *apitest.Env) {
	t := in.t
	alice := env.Login("alice", apitest.UserPassword)
	bob := env.Login("bob", apitest.UserPassword)
	carol := env.Login("carol", apitest.UserPassword)

	// The agent that registered after approvals came waits for one; the overview asks for it.
	var agents []struct {
		Name     string `json:"name"`
		Approved bool   `json:"approved"`
	}
	alice.Get("/api/v1/admin/agents", &agents)
	var waiting []string
	for _, a := range agents {
		if !a.Approved {
			waiting = append(waiting, a.Name)
		}
	}
	if !slices.Equal(waiting, []string{"build-02"}) {
		t.Errorf("agents = %+v", agents)
	}
	type attention struct {
		Key   string `json:"key"`
		Count int64  `json:"count"`
	}
	var overview struct {
		Attention []attention `json:"attention"`
	}
	alice.Get("/api/v1/admin/overview", &overview)
	if !slices.Contains(overview.Attention, attention{Key: "agents-awaiting-approval", Count: 1}) {
		t.Errorf("attention = %+v", overview.Attention)
	}

	// The imported repository, the issues of that time and the sub-task with its parent's level.
	var repo struct {
		Import *struct {
			URL   string `json:"url"`
			State string `json:"state"`
		} `json:"import"`
	}
	bob.Get("/api/v1/code/repos/PLAT/mirror", &repo)
	if repo.Import == nil || repo.Import.State != "done" || repo.Import.URL != "https://git.example.com/team/mirror.git" {
		t.Errorf("mirror = %+v", repo)
	}
	var sub struct {
		SecurityLevel *struct {
			Name string `json:"name"`
		} `json:"securityLevel"`
	}
	bob.Get("/api/v1/issues/SHOP-5", &sub)
	if sub.SecurityLevel == nil || sub.SecurityLevel.Name != "Nur Team" {
		t.Errorf("SHOP-5 = %+v", sub)
	}
	if r := carol.Do(http.MethodGet, "/api/v1/issues/SHOP-5", nil); r.Status != http.StatusNotFound {
		t.Errorf("carol sees SHOP-5: %d", r.Status)
	}
	var hooks []struct {
		Name  string `json:"name"`
		Owner *struct {
			Username string `json:"username"`
		} `json:"owner"`
	}
	alice.Get("/api/v1/webhooks?scope=project&key=SHOP", &hooks)
	if len(hooks) != 1 || hooks[0].Name != "Chat" || hooks[0].Owner == nil || hooks[0].Owner.Username != "alice" {
		t.Errorf("project webhooks = %+v", hooks)
	}
}

// check33 checks the rows of the fixture of version 33.
func (in *installation) check33(env *apitest.Env) {
	t := in.t
	alice := env.Login("alice", apitest.UserPassword)
	bob := env.Login("bob", apitest.UserPassword)

	type named struct {
		Name string `json:"name"`
	}
	var templates []named
	bob.Get("/api/v1/builds/templates", &templates)
	if !slices.Contains(templates, named{Name: "Go-Dienst"}) {
		t.Errorf("templates = %+v", templates)
	}
	var triggers []struct {
		Description string `json:"description"`
	}
	alice.Get("/api/v1/code/repos/PLAT/shop/triggers", &triggers)
	if len(triggers) != 1 || triggers[0].Description != "Release" {
		t.Errorf("triggers = %+v", triggers)
	}
	var run struct {
		Event   string `json:"event"`
		Trigger string `json:"trigger"`
		State   string `json:"state"`
	}
	bob.Get("/api/v1/code/repos/PLAT/shop/runs/3", &run)
	if run.Event != "api" || run.Trigger != "Release" || run.State != "succeeded" {
		t.Errorf("run #3 = %+v", run)
	}
	var deployments struct {
		Items []struct {
			State string `json:"state"`
		} `json:"items"`
	}
	bob.Get("/api/v1/code/repos/PLAT/shop/environments/production/deployments", &deployments)
	if d := deployments.Items; len(d) != 1 || d[0].State != "succeeded" {
		t.Errorf("deployments = %+v", deployments)
	}
	var statuses map[string][]struct {
		Context string `json:"context"`
		State   string `json:"state"`
	}
	bob.Get("/api/v1/code/repos/PLAT/shop/statuses?sha=b1a580b7116917a5b73fbd6a4b8b9e117f971def", &statuses)
	if s := statuses["b1a580b7116917a5b73fbd6a4b8b9e117f971def"]; len(s) != 1 || s[0].State != "skipped" {
		t.Errorf("statuses = %+v", statuses)
	}
	type agent struct {
		Name      string `json:"name"`
		Protected bool   `json:"protected"`
	}
	var agents []agent
	alice.Get("/api/v1/admin/agents", &agents)
	if !slices.Contains(agents, agent{Name: "deploy-01", Protected: true}) {
		t.Errorf("agents = %+v", agents)
	}
	// The restricted page and its child stay out of bob's sight in the trash.
	var trash struct {
		Items []struct {
			Title string `json:"title"`
		} `json:"items"`
	}
	if r := bob.Do(http.MethodGet, "/api/v1/wiki/spaces/DOCS/trash", nil); r.Status == http.StatusOK {
		r.Decode(&trash)
		for _, p := range trash.Items {
			if strings.HasPrefix(p.Title, "Interna") {
				t.Errorf("bob sees %q in the trash", p.Title)
			}
		}
	}
}

// check40 checks the rows of the fixture of version 40.
func (in *installation) check40(env *apitest.Env) {
	t := in.t
	ctx := context.Background()
	alice := env.Login("alice", apitest.UserPassword)
	bob := env.Login("bob", apitest.UserPassword)

	// grace's second factor: her password alone is not enough, with a code of her secret it is.
	r := env.Anonymous().Do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "grace", "password": apitest.UserPassword})
	if r.Status != http.StatusUnauthorized || r.Code() != "auth.second-factor-required" {
		t.Errorf("grace without a code: %d %s", r.Status, r.Body)
	}
	env.LoginWithCode("grace", apitest.UserPassword, crypto.TOTPCode([]byte("12345678901234567890"), crypto.TOTPStep(time.Now())))
	var overview struct {
		People struct {
			TwoFactor int64 `json:"twoFactor"`
		} `json:"people"`
	}
	alice.Get("/api/v1/admin/overview", &overview)
	if overview.People.TwoFactor != 1 {
		t.Errorf("people = %+v", overview.People)
	}

	// The page version with a contributor, and the shared draft bob writes.
	var versions struct {
		Items []struct {
			Message      string `json:"message"`
			Contributors []struct {
				Username string `json:"username"`
			} `json:"contributors"`
		} `json:"items"`
	}
	bob.Get("/api/v1/wiki/pages/1/versions", &versions)
	if v := versions.Items; len(v) != 2 || v[0].Message != "Rufbereitschaft" || len(v[0].Contributors) != 1 ||
		v[0].Contributors[0].Username != "bob" {
		t.Errorf("versions of the home page = %+v", versions)
	}
	var draft struct {
		BaseVersion  int  `json:"baseVersion"`
		Changed      bool `json:"changed"`
		Contributors []struct {
			Username string `json:"username"`
		} `json:"contributors"`
	}
	alice.Get("/api/v1/wiki/pages/1/edit", &draft)
	if draft.BaseVersion != 2 || !draft.Changed || len(draft.Contributors) != 1 || draft.Contributors[0].Username != "bob" {
		t.Errorf("draft of the home page = %+v", draft)
	}

	// The run that the release started, and the applied suggestion.
	var run struct {
		Event    string `json:"event"`
		Upstream *struct {
			Repository struct {
				Slug string `json:"slug"`
			} `json:"repository"`
			Number int `json:"number"`
		} `json:"upstream"`
	}
	bob.Get("/api/v1/code/repos/PLAT/legacy/runs/2", &run)
	if run.Event != "pipeline" || run.Upstream == nil || run.Upstream.Repository.Slug != "shop" || run.Upstream.Number != 3 {
		t.Errorf("legacy #2 = %+v", run)
	}
	var timeline []struct {
		Comment *struct {
			Suggestion *struct {
				Applied *struct {
					Commit string `json:"commit"`
				} `json:"applied"`
			} `json:"suggestion"`
		} `json:"comment"`
	}
	bob.Get("/api/v1/code/repos/PLAT/shop/pull-requests/1/timeline", &timeline)
	applied := 0
	for _, a := range timeline {
		if a.Comment != nil && a.Comment.Suggestion != nil && a.Comment.Suggestion.Applied != nil &&
			a.Comment.Suggestion.Applied.Commit == "b1a580b7116917a5b73fbd6a4b8b9e117f971def" {
			applied++
		}
	}
	if applied != 1 {
		t.Errorf("timeline = %+v", timeline)
	}

	// The spans SHOP-6 had are kept; those of the older issues were derived (check09).
	var spans int
	if err := in.pool.QueryRow(ctx, "SELECT count(*) FROM issue_field_spans WHERE issue_id = 6").Scan(&spans); err != nil || spans != 6 {
		t.Errorf("spans of SHOP-6: %d %v", spans, err)
	}
}
