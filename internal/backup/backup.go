// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package backup writes and restores backups of an installation and checks the data directory against the
// database: `kogge admin backup`, `kogge admin restore` and `kogge admin fsck`.
//
// A backup is one tar file, written in this order:
//
//	kogge-backup.json             the manifest: versions, master key ids, tables and sequences
//	repositories/NN/ID.git/...    the repositories, copied while Git writes are paused
//	database/TABLE/NNNNN.copy.gz  the rows of every table, from a snapshot taken while Git writes were paused
//	blobs/AB/CD/SHA256            the blobs of the snapshot (immutable, copied after writes resumed)
//	logs/N/ID.log                 the build logs
//	ssh/...                       the SSH host keys
//	kogge-backup-end.json         the summary; a backup without it is incomplete
//
// The database part does not depend on client tools such as pg_dump: Kogge copies its own tables with COPY and
// recreates the schema with its migrations when it restores. The master key is not part of the backup; it is
// kept separately, and a restore checks that the configured key is the one of the backup.
package backup

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/version"
)

const (
	manifestName = "kogge-backup.json"
	endName      = "kogge-backup-end.json"
	formatV1     = 1
	// pauseWait is how long a backup waits for running Git writes to finish.
	pauseWait = 10 * time.Minute
)

// Options locate the installation.
type Options struct {
	Pool    *pgxpool.Pool
	DataDir string
	// Keyring is the master key. A backup records the ids of its keys; a restore checks them.
	Keyring *crypto.Keyring
	Logger  *slog.Logger
	// Progress receives messages for the operator; nil discards them.
	Progress func(format string, args ...any)
}

func (o *Options) progress(format string, args ...any) {
	if o.Progress != nil {
		o.Progress(format, args...)
	}
}

func (o *Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// Manifest describes a backup; it is the first file of the archive.
type Manifest struct {
	Format  int         `json:"format"`
	Kogge   string      `json:"kogge"`
	Created time.Time   `json:"created"`
	Schema  db.Versions `json:"schema"`
	// MasterKeys are the ids of the master keys the installation used (the current key first).
	MasterKeys []string   `json:"masterKeys"`
	Tables     []Table    `json:"tables"`
	Sequences  []Sequence `json:"sequences"`
}

// Table is a table of the database and the columns its data has.
type Table struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// Sequence is the state of a sequence.
type Sequence struct {
	Name   string `json:"name"`
	Value  int64  `json:"value"`
	Called bool   `json:"called"`
}

// Summary closes a backup; it is the last file of the archive.
type Summary struct {
	Finished     time.Time        `json:"finished"`
	Rows         map[string]int64 `json:"rows"`
	Repositories int              `json:"repositories"`
	Blobs        int              `json:"blobs"`
	Logs         int              `json:"logs"`
	// Bytes is the size of the copied files and table data (uncompressed).
	Bytes int64 `json:"bytes"`
	// Missing lists files the database refers to that did not exist (`kogge admin fsck` explains them).
	Missing []string `json:"missing,omitempty"`
}

type backup struct {
	o     Options
	tw    *tar.Writer
	sum   Summary
	start time.Time
}

// Write writes a backup to path, which must not exist. Git writes (pushes, merges, edits) wait while the backup
// takes its database snapshot and copies the repositories; everything else keeps running. The file appears under
// its name only when it is complete.
func Write(ctx context.Context, o Options, path string) (*Summary, error) {
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("%s exists", path)
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = f.Close()
			_ = os.Remove(partial)
		}
	}()
	buf := bufio.NewWriterSize(f, 1<<20)
	b := &backup{o: o, tw: tar.NewWriter(buf), start: time.Now(), sum: Summary{Rows: map[string]int64{}}}
	if err := b.run(ctx); err != nil {
		return nil, err
	}
	b.sum.Finished = time.Now().UTC()
	if err := b.addJSON(endName, b.sum); err != nil {
		return nil, err
	}
	if err := b.tw.Close(); err != nil {
		return nil, err
	}
	if err := buf.Flush(); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(partial, path); err != nil {
		return nil, err
	}
	syncDir(filepath.Dir(path))
	complete = true
	return &b.sum, nil
}

func (b *backup) run(ctx context.Context) error {
	// A connection of its own holds the backup's session locks; closing it releases them, whatever happens.
	lc, err := pgx.ConnectConfig(ctx, b.o.Pool.Config().ConnConfig)
	if err != nil {
		return err
	}
	defer lc.Close(context.WithoutCancel(ctx))
	var free bool
	if err := lc.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", db.LockBackup).Scan(&free); err != nil {
		return err
	}
	if !free {
		return errors.New("another backup is running")
	}
	if err := keepAlive(ctx, lc); err != nil {
		return err
	}
	// Collection of blobs and build logs waits for the end of the backup.
	if err := lockWaiting(ctx, lc, db.LockFileDeletes, "running deletions of blobs and logs"); err != nil {
		return err
	}

	b.o.progress("Pausing Git writes ...")
	paused := time.Now()
	if err := lockWaiting(ctx, lc, db.LockGitWrites, "running Git writes"); err != nil {
		return err
	}
	snap, err := b.o.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = snap.Rollback(context.WithoutCancel(ctx)) }()
	m, err := snapshot(ctx, snap)
	if err != nil {
		return err
	}
	m.Kogge = version.Version
	m.Created = b.start.UTC()
	if b.o.Keyring != nil {
		m.MasterKeys = b.o.Keyring.KeyIDs()
	}
	if err := b.addJSON(manifestName, m); err != nil {
		return err
	}
	if err := b.repositories(ctx, snap); err != nil {
		return err
	}
	if err := resumeGitWrites(ctx, lc); err != nil {
		return err
	}
	b.o.progress("Git writes resumed after %s; %d repositories copied", time.Since(paused).Round(time.Millisecond), b.sum.Repositories)

	rows := int64(0)
	if err := b.database(ctx, snap, m); err != nil {
		return err
	}
	for _, n := range b.sum.Rows {
		rows += n
	}
	b.o.progress("Database: %d tables, %d rows", len(m.Tables), rows)
	if err := b.blobs(ctx, snap); err != nil {
		return err
	}
	b.o.progress("Blobs: %d", b.sum.Blobs)
	if err := snap.Rollback(ctx); err != nil {
		return err
	}
	if err := b.logs(); err != nil {
		return err
	}
	b.o.progress("Build logs: %d", b.sum.Logs)
	return b.tree(filepath.Join(b.o.DataDir, "ssh"), nil)
}

// repositories copies the repositories of the snapshot.
func (b *backup) repositories(ctx context.Context, snap pgx.Tx) error {
	rows, err := snap.Query(ctx, "SELECT id FROM repositories ORDER BY id")
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		dir := code.RepoDir(b.o.DataDir, id)
		if _, err := os.Stat(dir); err != nil {
			b.missing(dir)
			continue
		}
		if err := b.tree(dir, skipInRepository); err != nil {
			return err
		}
		b.sum.Repositories++
	}
	return nil
}

// skipInRepository leaves out what git writes temporarily: locks, objects of pushes still in quarantine and
// objects or packs being written.
func skipInRepository(d fs.DirEntry) bool {
	name := d.Name()
	return strings.HasSuffix(name, ".lock") || strings.HasPrefix(name, "tmp_") ||
		(d.IsDir() && (strings.HasPrefix(name, "incoming-") || strings.HasPrefix(name, "tmp_objdir")))
}

// blobs copies the blobs of the snapshot. Blobs are immutable and their collection waits for the backup.
func (b *backup) blobs(ctx context.Context, snap pgx.Tx) error {
	rows, err := snap.Query(ctx, "SELECT sha256 FROM blobs ORDER BY sha256")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return err
		}
		if !validSHA(sha) {
			b.missing("blobs/" + sha)
			continue
		}
		path := blob.FilePath(b.o.DataDir, sha)
		if err := b.file(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				b.missing(path)
				continue
			}
			return err
		}
		b.sum.Blobs++
	}
	return rows.Err()
}

// logs copies the build logs. Logs only grow; a log that is being written is copied as far as it is written.
func (b *backup) logs() error {
	root := filepath.Join(b.o.DataDir, "logs")
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".log") {
			if err := b.file(path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil // deleted with its run
				}
				return err
			}
			b.sum.Logs++
		}
		return nil
	})
}

func (b *backup) missing(path string) {
	if rel, err := filepath.Rel(b.o.DataDir, path); err == nil {
		path = filepath.ToSlash(rel)
	}
	b.sum.Missing = append(b.sum.Missing, path)
	b.o.progress("warning: %s is missing", path)
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// syncDir makes a rename durable.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// PauseWrites pauses Git writes while fn runs. Installations that back up with file system or storage snapshots
// take the snapshot of the data directory in fn; at is the database time of the pause, to which a database
// backup with point-in-time recovery is restored.
func PauseWrites(ctx context.Context, o Options, fn func(ctx context.Context, at time.Time) error) error {
	lc, err := pgx.ConnectConfig(ctx, o.Pool.Config().ConnConfig)
	if err != nil {
		return err
	}
	defer lc.Close(context.WithoutCancel(ctx))
	if err := keepAlive(ctx, lc); err != nil {
		return err
	}
	if err := lockWaiting(ctx, lc, db.LockGitWrites, "running Git writes"); err != nil {
		return err
	}
	var at time.Time
	if err := lc.QueryRow(ctx, "SELECT now()").Scan(&at); err != nil {
		return err
	}
	o.progress("Git writes are paused (database time %s)", at.UTC().Format(time.RFC3339Nano))
	if err := fn(ctx, at); err != nil {
		return err
	}
	if err := resumeGitWrites(ctx, lc); err != nil {
		return err
	}
	o.progress("Git writes resumed")
	return nil
}

// keepAlive makes the server notice within about two minutes when the process dies without closing lc, so that
// the locks it holds are released.
func keepAlive(ctx context.Context, lc *pgx.Conn) error {
	for _, set := range []string{"SET tcp_keepalives_idle = 30", "SET tcp_keepalives_interval = 10", "SET tcp_keepalives_count = 6"} {
		if _, err := lc.Exec(ctx, set); err != nil {
			return err
		}
	}
	_, err := lc.Exec(ctx, "SELECT set_config('lock_timeout', $1, false)", strconv.FormatInt(pauseWait.Milliseconds(), 10))
	return err
}

// lockWaiting takes an advisory lock exclusively on lc, waiting at most pauseWait for its shared holders.
func lockWaiting(ctx context.Context, lc *pgx.Conn, key int64, holders string) error {
	if _, err := lc.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return fmt.Errorf("%s did not finish within %s; try again later", holders, pauseWait)
		}
		return err
	}
	return nil
}

// resumeGitWrites releases the Git write lock and fails if it was lost in between.
func resumeGitWrites(ctx context.Context, lc *pgx.Conn) error {
	var held bool
	if err := lc.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", db.LockGitWrites).Scan(&held); err != nil || !held {
		if err == nil {
			err = errors.New("the lock that paused Git writes was lost; writes may have happened during the pause")
		}
		return err
	}
	return nil
}

// FileName returns the name of a backup written at t: kogge-backup-YYYYMMDD-HHMMSS.tar, which sorts by time.
func FileName(t time.Time) string { return "kogge-backup-" + t.Format("20060102-150405") + ".tar" }

// Prune deletes all but the newest keep backups named by FileName in dir and returns the deleted files.
func Prune(dir string, keep int) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "kogge-backup-*.tar"))
	if err != nil || len(files) <= keep {
		return nil, err
	}
	slices.Sort(files)
	var deleted []string
	for _, f := range files[:len(files)-keep] {
		if err := os.Remove(f); err != nil {
			return deleted, err
		}
		deleted = append(deleted, f)
	}
	return deleted, nil
}
