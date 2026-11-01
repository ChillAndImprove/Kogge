// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package blob stores immutable, content-addressed files on the data volume. A blob is published by writing a
// temporary file, syncing and renaming it, and recording it in the blobs table; owners reference blobs in
// blob_refs. Unreferenced blobs are collected after a grace period longer than the backup interval.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/platform/db"
)

// ErrTooLarge is returned when a blob exceeds the allowed size.
var ErrTooLarge = errors.New("blob: too large")

// Ref identifies a stored blob.
type Ref struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Store is the blob store rooted at <data>/blobs.
type Store struct {
	root string
	pool *pgxpool.Pool
}

// New creates the store and its directories.
func New(dataDir string, pool *pgxpool.Pool) (*Store, error) {
	root := filepath.Join(dataDir, "blobs")
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o750); err != nil {
		return nil, err
	}
	return &Store{root: root, pool: pool}, nil
}

// Path returns the file path of a blob.
func (s *Store) Path(sha string) string { return pathIn(s.root, sha) }

// Dir returns the directory of the blobs (the file system whose space uploads use).
func (s *Store) Dir() string { return s.root }

// FilePath returns the file path of a blob in a data directory.
func FilePath(dataDir, sha string) string { return pathIn(filepath.Join(dataDir, "blobs"), sha) }

func pathIn(root, sha string) string { return filepath.Join(root, sha[:2], sha[2:4], sha) }

func lockKey(sha string) int64 {
	h := fnv.New64a()
	h.Write([]byte("blob:" + sha))
	return int64(h.Sum64())
}

// ErrMismatch: the content does not have the SHA-256 it should have.
var ErrMismatch = errors.New("blob: the content does not match its expected SHA-256")

// Put stores the content of r (at most maxSize bytes; 0 means unlimited) and returns its reference.
func (s *Store) Put(ctx context.Context, r io.Reader, maxSize int64, contentType string) (Ref, error) {
	return s.put(ctx, r, maxSize, contentType, "")
}

// PutVerified stores the content of r only if its SHA-256 is expect (hex); otherwise nothing of it is kept and the
// error is ErrMismatch.
func (s *Store) PutVerified(ctx context.Context, r io.Reader, expect string, maxSize int64, contentType string) (Ref, error) {
	return s.put(ctx, r, maxSize, contentType, expect)
}

func (s *Store) put(ctx context.Context, r io.Reader, maxSize int64, contentType, expect string) (Ref, error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "upload-*")
	if err != nil {
		return Ref{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	src := r
	if maxSize > 0 {
		src = io.LimitReader(r, maxSize+1)
	}
	n, err := io.Copy(io.MultiWriter(tmp, h), src)
	if err == nil && maxSize > 0 && n > maxSize {
		err = ErrTooLarge
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Ref{}, err
	}
	ref := Ref{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
	if expect != "" && ref.SHA256 != expect {
		return Ref{}, ErrMismatch // the temporary file is removed
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey(ref.SHA256)); err != nil {
			return err
		}
		final := s.Path(ref.SHA256)
		if !exists(final) {
			if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
				return err
			}
			if err := os.Rename(tmpName, final); err != nil {
				return err
			}
			if d, err := os.Open(filepath.Dir(final)); err == nil {
				_ = d.Sync()
				d.Close()
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO blobs (sha256, size, content_type) VALUES ($1, $2, NULLIF($3, ''))
			ON CONFLICT (sha256) DO UPDATE SET touched_at = now()`, ref.SHA256, ref.Size, contentType)
		return err
	})
	return ref, err
}

// exists reports whether a file is there. It opens the file rather than asking for its attributes: on NFS, which
// the nodes of an installation may share, an open asks the server, while attributes may come from the client's
// cache and still show a file that another node has just collected.
func exists(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// Open opens a blob for reading.
func (s *Store) Open(sha string) (*os.File, error) {
	if len(sha) != 64 {
		return nil, os.ErrNotExist
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return nil, os.ErrNotExist
	}
	return os.Open(s.Path(sha))
}

// AddRef records that an owner references a blob (in the owner's transaction).
func AddRef(ctx context.Context, q db.Querier, sha, ownerType string, ownerID int64) error {
	_, err := q.Exec(ctx, `INSERT INTO blob_refs (sha256, owner_type, owner_id) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, sha, ownerType, ownerID)
	return err
}

// RemoveRefs removes all references of an owner.
func RemoveRefs(ctx context.Context, q db.Querier, ownerType string, ownerID int64) error {
	_, err := q.Exec(ctx, "DELETE FROM blob_refs WHERE owner_type = $1 AND owner_id = $2", ownerType, ownerID)
	return err
}

// Collect deletes unreferenced blobs that were not touched within grace. It returns the number deleted.
func (s *Store) Collect(ctx context.Context, grace time.Duration) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT sha256 FROM blobs b
		WHERE touched_at < now() - $1::interval AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256)
		LIMIT 1000`, fmt.Sprintf("%d seconds", int64(grace.Seconds())))
	if err != nil {
		return 0, err
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, sha := range candidates {
		paused := false
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			// A running backup copies the blobs of its snapshot; collection waits for the next run.
			if running, err := db.BackupRunning(ctx, tx); err != nil || running {
				paused = running
				return err
			}
			if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey(sha)); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM blobs b WHERE sha256 = $1 AND touched_at < now() - $2::interval
				AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256)`,
				sha, fmt.Sprintf("%d seconds", int64(grace.Seconds())))
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			if err := os.Remove(s.Path(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			deleted++
			return nil
		})
		if err != nil || paused {
			return deleted, err
		}
	}
	return deleted, nil
}
