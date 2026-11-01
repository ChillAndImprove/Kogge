// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package pgtest provides fresh, migrated PostgreSQL databases for tests. It uses the server named by
// KOGGE_TEST_DATABASE_URL (a URL whose user may create databases) or starts a Docker container
// (kogge-test-pg, postgres:18-alpine). Each test gets its own database, cloned from a migrated template.
package pgtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/migrations"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
)

const (
	containerName = "kogge-test-pg"
	containerPort = "55433"
	image         = "postgres:18-alpine"
)

var (
	setupOnce sync.Once
	serverURL string
	setupErr  error
)

// New returns the URL of a new migrated database that is dropped when the test ends. The test is skipped if
// no PostgreSQL server is available.
func New(t testing.TB) string {
	t.Helper()
	return create(t, true)
}

// NewEmpty returns the URL of a new database without any schema (for restores), dropped when the test ends.
func NewEmpty(t testing.TB) string {
	t.Helper()
	return create(t, false)
}

func create(t testing.TB, migrated bool) string {
	t.Helper()
	setupOnce.Do(func() { serverURL, setupErr = server() })
	if setupErr != nil {
		if os.Getenv("KOGGE_TEST_REQUIRE_DB") != "" {
			t.Fatalf("no test database: %v", setupErr)
		}
		t.Skipf("no test database (set KOGGE_TEST_DATABASE_URL or install Docker): %v", setupErr)
	}
	ctx := context.Background()
	create := `CREATE DATABASE %q`
	var args []any
	if migrated {
		tpl, err := template(ctx)
		if err != nil {
			t.Fatalf("test database template: %v", err)
		}
		create, args = `CREATE DATABASE %q TEMPLATE %q`, []any{tpl}
	}
	name := "t_" + strings.ToLower(crypto.RandomString(9))
	name = strings.NewReplacer("-", "_").Replace(name)
	admin, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, fmt.Sprintf(create, append([]any{name}, args...)...)); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), serverURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name))
	})
	return withDatabase(serverURL, name)
}

// Pool returns a connection pool on a new database.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), New(t), "kogge-test", 10)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func withDatabase(raw, name string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = "/" + name
	return u.String()
}

func server() (string, error) {
	if u := os.Getenv("KOGGE_TEST_DATABASE_URL"); u != "" {
		return u, nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("docker not found")
	}
	// Serialise container start-up across test processes (go test runs packages in parallel).
	lockPath := filepath.Join(os.TempDir(), "kogge-test-pg.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	running, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName).Output()
	switch strings.TrimSpace(string(running)) {
	case "true":
	case "false":
		if out, err := exec.Command("docker", "start", containerName).CombinedOutput(); err != nil {
			return "", fmt.Errorf("docker start: %v: %s", err, out)
		}
	default:
		out, err := exec.Command("docker", "run", "-d", "--name", containerName, "-e", "POSTGRES_USER=kogge",
			"-e", "POSTGRES_PASSWORD=kogge", "-e", "POSTGRES_DB=postgres", "-p", "127.0.0.1:"+containerPort+":5432",
			"--tmpfs", "/var/lib/postgresql:rw", image, "-c", "fsync=off", "-c", "synchronous_commit=off",
			"-c", "full_page_writes=off", "-c", "max_connections=300").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docker run: %v: %s", err, out)
		}
	}
	u := "postgres://kogge:kogge@127.0.0.1:" + containerPort + "/postgres?sslmode=disable"
	deadline := time.Now().Add(60 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c, err := pgx.Connect(ctx, u)
		if err == nil {
			err = c.Ping(ctx)
			c.Close(ctx)
		}
		cancel()
		if err == nil {
			return u, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("test database did not start: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

var (
	tplMu   sync.Mutex
	tplName string
)

// template creates (once per schema version, across processes) a migrated template database.
func template(ctx context.Context) (string, error) {
	tplMu.Lock()
	defer tplMu.Unlock()
	if tplName != "" {
		return tplName, nil
	}
	h := sha256.New()
	_ = fs.WalkDir(migrations.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := fs.ReadFile(migrations.FS, path)
			h.Write([]byte(path))
			h.Write(b)
		}
		return nil
	})
	name := "kogge_tpl_" + hex.EncodeToString(h.Sum(nil))[:16]

	admin, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		return "", err
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock(7366001)"); err != nil {
		return "", err
	}
	defer func() { _, _ = admin.Exec(ctx, "SELECT pg_advisory_unlock(7366001)") }()
	var ready bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1 AND datistemplate)",
		name).Scan(&ready); err != nil {
		return "", err
	}
	if !ready {
		_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name))
		if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
			return "", err
		}
		pool, err := db.Open(ctx, withDatabase(serverURL, name), "kogge-test-template", 4)
		if err != nil {
			return "", err
		}
		err = db.Migrate(ctx, pool, slog.New(slog.DiscardHandler))
		pool.Close()
		if err != nil {
			return "", err
		}
		if _, err := admin.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %q WITH IS_TEMPLATE true`, name)); err != nil {
			return "", err
		}
	}
	tplName = name
	return name, nil
}
