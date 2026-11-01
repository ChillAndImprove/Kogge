// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package backup_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/backup"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/testutil/apitest"
	"kogge.de/kogge/internal/testutil/pgtest"
)

const pipeline = `version: 1
pipelines:
  ci:
    on: { push: { branches: ["**"] } }
    stages:
      - name: Build
        jobs:
          - name: test
            runs-on: [shell]
            steps:
              - run: echo test
`

const logText = "hello log\n\x1b[32mok\x1b[0m\n"

// installation creates data of every module through the API: issues with attachments, a space (its home page
// and the space refer to each other) with an export, a repository with a pipeline run and a build log.
func installation(t *testing.T) (*apitest.Env, string) {
	t.Helper()
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/projects", map[string]any{"key": "SHOP", "name": "Shop", "template": "scrum"}, nil)
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	admin.Get("/api/v1/projects/SHOP/issue-types", &types)
	var issue struct {
		Key string `json:"key"`
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": types[0].ID,
		"summary": "Checkout: Grüße, \"quotes\", tabs\tand\nnew lines \\ backslashes"}, &issue)
	attach(t, admin, issue.Key, "notes.txt", "hello attachment")
	attach(t, admin, issue.Key, "more.txt", "another attachment")
	admin.MustDo(http.MethodPost, "/api/v1/wiki/spaces", map[string]any{"key": "DOCS", "name": "Docs", "open": false}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "PLAT", "name": "Platform"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/PLAT/repos", map[string]any{"name": "shop", "readme": true}, nil)
	admin.MustDo(http.MethodPut, "/api/v1/code/repos/PLAT/shop/files", map[string]any{"branch": "main", "path": ".kogge/pipelines.yml",
		"content": pipeline, "message": issue.Key + " Build the shop"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/wiki/spaces/DOCS/exports", map[string]any{"format": "markdown"}, nil)
	env.WorkJobs()

	// The job ran and wrote its log.
	var jobID int64
	if err := env.App.Pool.QueryRow(context.Background(), `UPDATE run_jobs SET state = 'succeeded', log_size = $1,
		started_at = now(), finished_at = now() RETURNING id`, len(logText)).Scan(&jobID); err != nil {
		t.Fatalf("job: %v", err)
	}
	path := builds.LogFile(env.App.Cfg.DataDir, jobID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(logText), 0o640); err != nil {
		t.Fatal(err)
	}
	return env, issue.Key
}

func attach(t *testing.T, c *apitest.Client, key, name, content string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = io.WriteString(fw, content)
	_ = mw.Close()
	if r := c.Do(http.MethodPost, "/api/v1/issues/"+key+"/attachments", &buf, "Content-Type", mw.FormDataContentType()); r.Status != http.StatusCreated {
		t.Fatalf("upload: %d %s", r.Status, r.Body)
	}
}

func options(env *apitest.Env) backup.Options {
	kr, err := crypto.LoadKeyring(env.App.Cfg.MasterKeyFile, env.App.Cfg.DataDir)
	if err != nil {
		env.T.Fatal(err)
	}
	return backup.Options{Pool: env.App.Pool, DataDir: env.App.Cfg.DataDir, Keyring: kr}
}

// TestBackupAndRestore: a backup restored into an empty installation has the same rows and files, and Kogge runs
// on it.
func TestBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	env, key := installation(t)
	src := options(env)

	// The position of the audit forwarding belongs to the source's database cluster.
	if err := audit.RewindForwarding(ctx, env.App.Pool, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "kogge.tar")
	sum, err := backup.Write(ctx, src, file)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if sum.Repositories != 1 || sum.Blobs != 3 || sum.Logs != 1 || len(sum.Missing) != 0 || sum.Rows["issues"] != 1 || sum.Rows["wiki_exports"] != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if _, err := os.Stat(file + ".partial"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("partial file left: %v", err)
	}
	if _, err := backup.Write(ctx, src, file); err == nil {
		t.Error("a backup overwrote a file")
	}
	m, _, err := backup.ReadManifest(file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.MasterKeys, src.Keyring.KeyIDs()) || m.Schema.Schema == 0 || m.Schema.Jobs == 0 {
		t.Errorf("manifest = %+v", m)
	}

	// The target: an empty database and a data directory with the master key only.
	dstURL := pgtest.NewEmpty(t)
	dstPool, err := db.Open(ctx, dstURL, "kogge-restore-test", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer dstPool.Close()
	dstDir := t.TempDir()
	keyFile := filepath.Join(dstDir, "master.key")
	key0, err := os.ReadFile(env.App.Cfg.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	dst := backup.Options{Pool: dstPool, DataDir: dstDir}

	// Without the master key of the backup, the restore refuses.
	other, _ := crypto.ParseKeyring(crypto.GenerateKey())
	dst.Keyring = other
	if _, err := backup.Restore(ctx, dst, file, backup.RestoreOptions{KeyFile: keyFile}); err == nil || !strings.Contains(err.Error(), "master key") {
		t.Fatalf("restore with another master key: %v", err)
	}
	if err := os.WriteFile(keyFile, key0, 0o600); err != nil {
		t.Fatal(err)
	}
	dst.Keyring = src.Keyring
	report, err := backup.Restore(ctx, dst, file, backup.RestoreOptions{KeyFile: keyFile})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(report.Problems) > 0 || report.Repositories != 1 || report.Blobs != 3 || report.Logs != 1 {
		t.Errorf("fsck after the restore: %+v", report)
	}
	if _, err := backup.Restore(ctx, dst, file, backup.RestoreOptions{KeyFile: keyFile}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("restore into a used installation: %v", err)
	}

	// Every table has the same rows, except the forwarding position, which a restored installation begins anew
	// with the events to come; restored audit events have the restore's transaction.
	for _, table := range tables(t, env.App.Pool) {
		if table == "audit_forwarding" {
			continue
		}
		if a, b := digest(t, env.App.Pool, table), digest(t, dstPool, table); a != b {
			t.Errorf("table %s differs:\n%s\n%s", table, a, b)
		}
	}
	var positions, transactions int
	if err := dstPool.QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_forwarding), (SELECT count(DISTINCT xact) FROM audit_events)").
		Scan(&positions, &transactions); err != nil || positions != 0 || transactions != 1 {
		t.Errorf("restored forwarding: %d positions, events of %d transactions, %v", positions, transactions, err)
	}
	// Sequences continue after the restored rows.
	var next, highest int64
	if err := dstPool.QueryRow(ctx, "SELECT nextval(pg_get_serial_sequence('issues', 'id')), (SELECT max(id) FROM issues)").Scan(&next, &highest); err != nil {
		t.Fatal(err)
	}
	if next <= highest {
		t.Errorf("next issue id %d, highest %d", next, highest)
	}

	// The files are back.
	var repoID, jobID int64
	if err := dstPool.QueryRow(ctx, "SELECT (SELECT id FROM repositories), (SELECT id FROM run_jobs)").Scan(&repoID, &jobID); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "--git-dir="+code.RepoDir(dstDir, repoID), "log", "--format=%s", "main").CombinedOutput()
	if err != nil || !strings.Contains(string(out), key+" Build the shop") {
		t.Errorf("git log: %v %s", err, out)
	}
	if b, err := os.ReadFile(builds.LogFile(dstDir, jobID)); err != nil || string(b) != logText {
		t.Errorf("log: %q %v", b, err)
	}

	// Kogge runs on the restored installation.
	restored := apitest.New(t, func(c *config.Config) {
		c.DatabaseURL, c.DataDir, c.MasterKeyFile = dstURL, dstDir, keyFile
	})
	admin := restored.Admin()
	var issue struct {
		Summary     string `json:"summary"`
		Attachments []struct {
			URL string `json:"url"`
		} `json:"attachments"`
	}
	admin.Get("/api/v1/issues/"+key, &issue)
	if !strings.HasPrefix(issue.Summary, "Checkout: Grüße") || len(issue.Attachments) != 2 {
		t.Fatalf("issue = %+v", issue)
	}
	contents := []string{string(admin.Do(http.MethodGet, issue.Attachments[0].URL, nil).Body),
		string(admin.Do(http.MethodGet, issue.Attachments[1].URL, nil).Body)}
	slices.Sort(contents)
	if !slices.Equal(contents, []string{"another attachment", "hello attachment"}) {
		t.Errorf("attachments = %q", contents)
	}
	if r := admin.Do(http.MethodGet, "/api/v1/code/repos/PLAT/shop/raw?ref=main&path=README.md", nil); r.Status != http.StatusOK || !strings.Contains(string(r.Body), "# shop") {
		t.Errorf("README: %d %s", r.Status, r.Body)
	}
	admin.MustDo(http.MethodPost, "/api/v1/issues", map[string]any{"project": "SHOP", "typeId": firstType(restored), "summary": "After the restore"}, nil)
}

func firstType(env *apitest.Env) int64 {
	var types []struct {
		ID int64 `json:"issueTypeId"`
	}
	env.Admin().Get("/api/v1/projects/SHOP/issue-types", &types)
	return types[0].ID
}

func tables(t *testing.T, pool *pgxpool.Pool) []string {
	rows, err := pool.Query(context.Background(), `SELECT relname FROM pg_class WHERE relnamespace = current_schema()::regnamespace
		AND relkind = 'r' AND relpersistence = 'p' AND relname NOT IN ('kogge_schema_version', 'river_migration') ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func digest(t *testing.T, pool *pgxpool.Pool, table string) string {
	var n int64
	var sum string
	from := pgx.Identifier{table}.Sanitize()
	if table == "audit_events" {
		// Restored events have the restore's transaction.
		from = "(SELECT id, at, actor_id, actor_ip, action, target_type, target_id, details FROM audit_events)"
	}
	q := "SELECT count(*), COALESCE(md5(string_agg(x::text, E'\\n' ORDER BY x::text)), '') FROM " + from + " x"
	if err := pool.QueryRow(context.Background(), q).Scan(&n, &sum); err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	return table + ": " + sum + " (" + strconv.FormatInt(n, 10) + " rows)"
}

// TestCheck: fsck finds missing and unreferenced blobs, leftovers and missing logs, and repairs what it can.
func TestCheck(t *testing.T) {
	ctx := context.Background()
	env, _ := installation(t)
	o := options(env)
	dir := env.App.Cfg.DataDir
	report, err := backup.Check(ctx, o, backup.CheckOptions{Hashes: true, Git: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Problems) > 0 {
		t.Fatalf("problems in a new installation: %+v", report.Problems)
	}

	// Damage: one attachment loses its file, the other its reference; leftovers appear; a log goes missing.
	var shas []string
	rows, _ := env.App.Pool.Query(ctx, "SELECT sha256 FROM issue_attachments ORDER BY id")
	shas, _ = pgx.CollectRows(rows, pgx.RowTo[string])
	if err := os.Remove(blob.FilePath(dir, shas[0])); err != nil {
		t.Fatal(err)
	}
	if _, err := env.App.Pool.Exec(ctx, "DELETE FROM blob_refs WHERE sha256 = $1", shas[1]); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	leftover := func(path string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	orphan := blob.FilePath(dir, strings.Repeat("ab", 32))
	leftover(orphan)
	leftover(filepath.Join(dir, "blobs", "tmp", "upload-1"))
	leftover(builds.LogFile(dir, 999999))
	leftover(filepath.Join(code.RepoDir(dir, 4242), "HEAD"))
	var jobID int64
	if err := env.App.Pool.QueryRow(ctx, "SELECT id FROM run_jobs").Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(builds.LogFile(dir, jobID)); err != nil {
		t.Fatal(err)
	}

	report, err = backup.Check(ctx, o, backup.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"blob.missing", "blob.orphan", "blob.temporary", "blob.unreferenced", "log.missing", "log.orphan", "repository.orphan"}
	if got := kinds(report); !slices.Equal(got, want) || report.Errors() != 2 {
		t.Fatalf("problems = %+v", report.Problems)
	}

	report, err = backup.Check(ctx, o, backup.CheckOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors() != 1 {
		t.Errorf("after the repair: %+v", report.Problems)
	}
	report, err = backup.Check(ctx, o, backup.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(report); !slices.Equal(got, []string{"blob.missing"}) {
		t.Errorf("left after the repair: %+v", report.Problems)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the orphaned blob file is still there")
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "lost+found", "repositories", "4242.git.*")); len(entries) != 1 {
		t.Errorf("lost+found = %v", entries)
	}
}

// TestCheckExports: finished exports of the wiki need their file and its reference; exports in progress have none.
func TestCheckExports(t *testing.T) {
	ctx := context.Background()
	env, _ := installation(t)
	o := options(env)
	env.Admin().MustDo(http.MethodPost, "/api/v1/wiki/spaces/DOCS/exports", map[string]any{"format": "html"}, nil)
	var done int64
	var sha string
	if err := env.App.Pool.QueryRow(ctx, "SELECT id, sha256 FROM wiki_exports WHERE state = 'done'").Scan(&done, &sha); err != nil {
		t.Fatal(err)
	}
	if report, err := backup.Check(ctx, o, backup.CheckOptions{Hashes: true}); err != nil || len(report.Problems) > 0 {
		t.Fatalf("an export done and one queued: %v %+v", err, report)
	}
	if _, err := env.App.Pool.Exec(ctx, "DELETE FROM blob_refs WHERE owner_type = 'wiki-export'"); err != nil {
		t.Fatal(err)
	}
	report, err := backup.Check(ctx, o, backup.CheckOptions{Repair: true})
	if err != nil || len(report.Problems) != 1 || report.Problems[0].Kind != "blob.unreferenced" ||
		report.Problems[0].Subject != "wiki-export "+strconv.FormatInt(done, 10) || !report.Problems[0].Repaired {
		t.Fatalf("an export without its reference: %v %+v", err, report)
	}
	var refs int
	if err := env.App.Pool.QueryRow(ctx, "SELECT count(*) FROM blob_refs WHERE owner_type = 'wiki-export' AND sha256 = $1", sha).Scan(&refs); err != nil || refs != 1 {
		t.Fatalf("references after the repair: %d %v", refs, err)
	}
}

func kinds(r *backup.Report) []string {
	var list []string
	for _, p := range r.Problems {
		list = append(list, p.Kind)
	}
	slices.Sort(list)
	return list
}

// TestPauseWrites: writers wait while the command of a storage snapshot runs.
func TestPauseWrites(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	gate, err := db.NewGate(pool, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	ran := false
	err = backup.PauseWrites(ctx, backup.Options{Pool: pool}, func(ctx context.Context, at time.Time) error {
		ran = true
		if time.Since(at) > time.Minute || time.Until(at) > time.Minute {
			t.Errorf("paused at %s", at)
		}
		if _, err := gate.Enter(ctx, 100*time.Millisecond); !errors.Is(err, db.ErrPaused) {
			t.Errorf("a writer entered during the pause: %v", err)
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("pause: %v %v", ran, err)
	}
	release, err := gate.Enter(ctx, time.Second)
	if err != nil {
		t.Fatalf("writer after the pause: %v", err)
	}
	release()
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	var names []string
	for i := range 5 {
		name := backup.FileName(start.AddDate(0, 0, i))
		names = append(names, name)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deleted, err := backup.Prune(dir, 2)
	if err != nil || len(deleted) != 3 || filepath.Base(deleted[0]) != names[0] {
		t.Fatalf("deleted %v: %v", deleted, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 3 || filepath.Base(left[0]) != "kogge-backup-20261004-020000.tar" {
		t.Errorf("left = %v", left)
	}
}

// TestBackupLocks: one backup at a time, and a backup waits for running deletions of blobs and logs.
func TestBackupLocks(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	o := backup.Options{Pool: pool, DataDir: t.TempDir()}
	dir := t.TempDir()

	other, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	if _, err := other.Exec(ctx, "SELECT pg_advisory_lock($1)", db.LockBackup); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Write(ctx, o, filepath.Join(dir, "a.tar")); err == nil || !strings.Contains(err.Error(), "another backup") {
		t.Fatalf("concurrent backup: %v", err)
	}
	if _, err := other.Exec(ctx, "SELECT pg_advisory_unlock($1)", db.LockBackup); err != nil {
		t.Fatal(err)
	}

	// A collector is deleting files.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if running, err := db.BackupRunning(ctx, tx); err != nil || running {
		t.Fatalf("running = %v, %v", running, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := backup.Write(ctx, o, filepath.Join(dir, "b.tar"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the backup did not wait for the collector: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("backup after the collector: %v", err)
	}
}
