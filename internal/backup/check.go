// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/platform/blob"
)

// CheckOptions select what fsck does besides comparing rows and files.
type CheckOptions struct {
	// Repair fixes what can be fixed without losing data: it adds missing blob references, removes leftovers
	// (unknown blob and log files, temporary files), moves unknown repositories to lost+found and marks missing
	// build logs as expired.
	Repair bool
	// Hashes reads every blob and compares its SHA-256.
	Hashes bool
	// Git runs `git fsck` on every repository.
	Git bool
}

// Problem is a finding of fsck.
type Problem struct {
	// Error means that data is missing or damaged; otherwise the problem is a leftover or an inconsistency that
	// does not lose data.
	Error    bool   `json:"error"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	Detail   string `json:"detail"`
	Repaired bool   `json:"repaired"`
}

// Report is the result of fsck.
type Report struct {
	Repositories int       `json:"repositories"`
	Blobs        int       `json:"blobs"`
	Logs         int       `json:"logs"`
	Problems     []Problem `json:"problems"`
}

// Errors returns the number of errors that were not repaired.
func (r *Report) Errors() int {
	n := 0
	for _, p := range r.Problems {
		if p.Error && !p.Repaired {
			n++
		}
	}
	return n
}

// Leftovers younger than this may belong to an upload or a push in progress.
const settleTime = time.Hour

type checker struct {
	o   Options
	co  CheckOptions
	r   *Report
	now time.Time
}

// Check compares the database with the data directory (`kogge admin fsck`): blobs and their references,
// repositories and build logs.
func Check(ctx context.Context, o Options, co CheckOptions) (*Report, error) {
	c := &checker{o: o, co: co, r: &Report{Problems: []Problem{}}, now: time.Now()}
	for _, step := range []func(context.Context) error{c.blobs, c.blobFiles, c.blobOwners, c.repositories, c.logs} {
		if err := step(ctx); err != nil {
			return c.r, err
		}
	}
	return c.r, nil
}

func (c *checker) add(p Problem) { c.r.Problems = append(c.r.Problems, p) }

func (c *checker) rel(path string) string {
	if rel, err := filepath.Rel(c.o.DataDir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

// blobs checks that every blob has its file, with the right size (and content).
func (c *checker) blobs(ctx context.Context) error {
	rows, err := c.o.Pool.Query(ctx, `SELECT b.sha256, b.size, EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256)
		FROM blobs b ORDER BY b.sha256`)
	if err != nil {
		return err
	}
	var unused []string
	for rows.Next() {
		var sha string
		var size int64
		var used bool
		if err := rows.Scan(&sha, &size, &used); err != nil {
			rows.Close()
			return err
		}
		c.r.Blobs++
		if !validSHA(sha) {
			c.add(Problem{Error: true, Kind: "blob.invalid", Subject: sha, Detail: "the blob's id is not a SHA-256"})
			continue
		}
		path := blob.FilePath(c.o.DataDir, sha)
		fi, err := os.Stat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist) && used:
			c.add(Problem{Error: true, Kind: "blob.missing", Subject: c.rel(path), Detail: "the file of a blob in use is missing"})
		case errors.Is(err, fs.ErrNotExist):
			c.add(Problem{Kind: "blob.missing-unused", Subject: c.rel(path), Detail: "the file of an unused blob is missing"})
			unused = append(unused, sha)
		case err != nil:
			rows.Close()
			return err
		case fi.Size() != size:
			c.add(Problem{Error: true, Kind: "blob.size", Subject: c.rel(path),
				Detail: fmt.Sprintf("the file has %d bytes instead of %d", fi.Size(), size)})
		case c.co.Hashes:
			if sum, err := hashFile(path); err != nil {
				rows.Close()
				return err
			} else if sum != sha {
				c.add(Problem{Error: true, Kind: "blob.corrupt", Subject: c.rel(path), Detail: "the content does not match its SHA-256"})
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if c.co.Repair && len(unused) > 0 {
		tag, err := c.o.Pool.Exec(ctx, `DELETE FROM blobs b WHERE sha256 = ANY($1)
			AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256)`, unused)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == int64(len(unused)) {
			c.markRepaired("blob.missing-unused")
		}
	}
	return nil
}

func (c *checker) markRepaired(kind string) {
	for i := range c.r.Problems {
		if c.r.Problems[i].Kind == kind {
			c.r.Problems[i].Repaired = true
		}
	}
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// blobFiles looks for files in the blob store that no blob refers to, and for old temporary files.
func (c *checker) blobFiles(ctx context.Context) error {
	root := filepath.Join(c.o.DataDir, "blobs")
	tops, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, top := range tops {
		if top.Name() == "tmp" && top.IsDir() {
			if err := c.leftovers(filepath.Join(root, "tmp"), "blob.temporary", "an interrupted upload left a temporary file"); err != nil {
				return err
			}
			continue
		}
		if !top.IsDir() || len(top.Name()) != 2 {
			continue
		}
		subs, err := os.ReadDir(filepath.Join(root, top.Name()))
		if err != nil {
			return err
		}
		for _, sub := range subs {
			if !sub.IsDir() {
				continue
			}
			dir := filepath.Join(root, top.Name(), sub.Name())
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			rows, err := c.o.Pool.Query(ctx, "SELECT sha256 FROM blobs WHERE sha256 = ANY($1)", names)
			if err != nil {
				return err
			}
			known, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			set := make(map[string]bool, len(known))
			for _, k := range known {
				set[k] = true
			}
			for _, e := range entries {
				if !set[e.Name()] {
					if err := c.leftover(filepath.Join(dir, e.Name()), e, "blob.orphan", "no blob refers to the file"); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// leftovers reports the files of a directory as leftovers.
func (c *checker) leftovers(dir, kind, detail string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := c.leftover(filepath.Join(dir, e.Name()), e, kind, detail); err != nil {
			return err
		}
	}
	return nil
}

// leftover reports a file nothing refers to; a repair removes it unless it is recent.
func (c *checker) leftover(path string, e fs.DirEntry, kind, detail string) error {
	info, err := e.Info()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if c.now.Sub(info.ModTime()) < settleTime {
		return nil
	}
	p := Problem{Kind: kind, Subject: c.rel(path), Detail: detail}
	if c.co.Repair && !e.IsDir() {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		p.Repaired = true
	}
	c.add(p)
	return nil
}

// Owners of blobs: every row with a blob needs it and a reference, without which the blob collection deletes it.
var blobOwners = []struct{ kind, table, id, sha string }{
	{"page-attachment", "page_attachments", "id", "sha256"},
	{"issue-attachment", "issue_attachments", "id", "sha256"},
	{"artifact", "artifacts", "id", "sha256"},
	{"build-cache", "build_caches", "id", "sha256"},
	{"lfs", "lfs_objects", "repository_id", "oid"},
	// Finished exports of the wiki (unfinished ones have no file yet).
	{"wiki-export", "wiki_exports", "id", "sha256"},
}

func (c *checker) blobOwners(ctx context.Context) error {
	for _, o := range blobOwners {
		rows, err := c.o.Pool.Query(ctx, fmt.Sprintf(`SELECT o.%[2]s, o.%[3]s, b.sha256 IS NOT NULL FROM %[1]s o
			LEFT JOIN blobs b ON b.sha256 = o.%[3]s
			WHERE o.%[3]s IS NOT NULL AND (b.sha256 IS NULL OR NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = o.%[3]s AND r.owner_type = $1 AND r.owner_id = o.%[2]s))`,
			o.table, o.id, o.sha), o.kind)
		if err != nil {
			return err
		}
		type finding struct {
			id     int64
			sha    string
			stored bool
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (finding, error) {
			var f finding
			err := r.Scan(&f.id, &f.sha, &f.stored)
			return f, err
		})
		if err != nil {
			return err
		}
		for _, f := range list {
			subject := o.kind + " " + strconv.FormatInt(f.id, 10)
			if !f.stored {
				c.add(Problem{Error: true, Kind: "blob.unknown", Subject: subject, Detail: "its blob " + f.sha + " does not exist"})
				continue
			}
			p := Problem{Error: true, Kind: "blob.unreferenced", Subject: subject,
				Detail: "its blob " + f.sha + " has no reference and would be collected"}
			if c.co.Repair {
				if _, err := c.o.Pool.Exec(ctx, `INSERT INTO blob_refs (sha256, owner_type, owner_id) VALUES ($1, $2, $3)
					ON CONFLICT DO NOTHING`, f.sha, o.kind, f.id); err != nil {
					return err
				}
				p.Repaired = true
			}
			c.add(p)
		}
	}
	return nil
}

// repositories checks that every repository has its directory, looks for directories of unknown repositories
// and runs git fsck if asked to.
func (c *checker) repositories(ctx context.Context) error {
	rows, err := c.o.Pool.Query(ctx, `SELECT r.id, p.key || '/' || r.slug FROM repositories r
		JOIN code_projects p ON p.id = r.project_id ORDER BY r.id`)
	if err != nil {
		return err
	}
	type repo struct {
		id   int64
		name string
	}
	repos, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (repo, error) {
		var x repo
		err := r.Scan(&x.id, &x.name)
		return x, err
	})
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, r := range repos {
		c.r.Repositories++
		dir := code.RepoDir(c.o.DataDir, r.id)
		known[dir] = true
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			c.add(Problem{Error: true, Kind: "repository.missing", Subject: r.name, Detail: c.rel(dir) + " does not exist"})
			continue
		}
		for _, part := range []string{"HEAD", "objects", "refs"} {
			if _, err := os.Stat(filepath.Join(dir, part)); err != nil {
				c.add(Problem{Error: true, Kind: "repository.damaged", Subject: r.name, Detail: c.rel(dir) + " has no " + part})
			}
		}
		if c.co.Git {
			cmd := exec.CommandContext(ctx, "git", "--git-dir="+dir, "fsck", "--connectivity-only", "--no-dangling", "--no-progress")
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "LC_ALL=C")
			if out, err := cmd.CombinedOutput(); err != nil {
				c.add(Problem{Error: true, Kind: "repository.corrupt", Subject: r.name, Detail: firstLines(string(out), 5)})
			}
		}
	}
	dirs, err := filepath.Glob(filepath.Join(c.o.DataDir, "repositories", "*", "*.git"))
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if known[dir] {
			continue
		}
		p := Problem{Kind: "repository.orphan", Subject: c.rel(dir), Detail: "no repository refers to the directory"}
		if c.co.Repair {
			target := filepath.Join(c.o.DataDir, "lost+found", "repositories", fmt.Sprintf("%s.%d", filepath.Base(dir), c.now.Unix()))
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			if err := os.Rename(dir, target); err != nil {
				return err
			}
			p.Repaired, p.Detail = true, p.Detail+"; moved to "+c.rel(target)
		}
		c.add(p)
	}
	return nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return strings.Join(lines, " | ")
}

// logs checks that the logs of jobs exist and looks for logs of jobs that do not.
func (c *checker) logs(ctx context.Context) error {
	rows, err := c.o.Pool.Query(ctx, "SELECT id FROM run_jobs WHERE log_size > 0 AND NOT log_expired ORDER BY id")
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	var missing []int64
	for _, id := range ids {
		path := builds.LogFile(c.o.DataDir, id)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			c.add(Problem{Kind: "log.missing", Subject: c.rel(path), Detail: "the log of job " + strconv.FormatInt(id, 10) + " is missing"})
			missing = append(missing, id)
		}
	}
	if c.co.Repair && len(missing) > 0 {
		if _, err := c.o.Pool.Exec(ctx, "UPDATE run_jobs SET log_expired = true WHERE id = ANY($1)", missing); err != nil {
			return err
		}
		c.markRepaired("log.missing")
	}
	files, err := filepath.Glob(filepath.Join(c.o.DataDir, "logs", "*", "*.log"))
	if err != nil {
		return err
	}
	c.r.Logs = len(files)
	byID := map[int64]string{}
	var list []int64
	for _, f := range files {
		id, err := strconv.ParseInt(strings.TrimSuffix(filepath.Base(f), ".log"), 10, 64)
		if err == nil {
			byID[id] = f
			list = append(list, id)
		}
	}
	if len(list) == 0 {
		return nil
	}
	rows, err = c.o.Pool.Query(ctx, "SELECT id FROM run_jobs WHERE id = ANY($1)", list)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range existing {
		delete(byID, id)
	}
	orphans := slices.Sorted(maps.Values(byID))
	for _, path := range orphans {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if err := c.leftover(path, fs.FileInfoToDirEntry(info), "log.orphan", "no job refers to the log"); err != nil {
			return err
		}
	}
	return nil
}
