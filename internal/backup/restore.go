// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/platform/db"
)

// RestoreOptions control a restore.
type RestoreOptions struct {
	// IgnoreMasterKey restores although the master key is not the one of the backup. The secrets in the database
	// (webhook secrets, secret build variables) cannot be read then and must be entered again.
	IgnoreMasterKey bool
	// KeyFile is the master key file; it may be in the otherwise empty data directory.
	KeyFile string
}

// ReadManifest reads the manifest and the summary of a backup. It fails if the backup is incomplete or contains
// files that a backup does not have.
func ReadManifest(path string) (*Manifest, *Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return readManifest(f)
}

func readManifest(f *os.File) (*Manifest, *Summary, error) {
	// *os.File can seek, so tar skips the contents.
	tr := tar.NewReader(f)
	var m *Manifest
	var sum *Summary
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("the backup is damaged: %w", err)
		}
		if m == nil && hdr.Name != manifestName {
			return nil, nil, errors.New("not a Kogge backup (the manifest is missing)")
		}
		kind, _, err := entryKind(hdr)
		if err != nil {
			return nil, nil, fmt.Errorf("the backup is damaged: %w", err)
		}
		switch kind {
		case "manifest":
			if m != nil {
				return nil, nil, errors.New("the backup is damaged: two manifests")
			}
			m = &Manifest{}
			if err := json.NewDecoder(io.LimitReader(tr, 64<<20)).Decode(m); err != nil {
				return nil, nil, fmt.Errorf("the manifest is damaged: %w", err)
			}
			if m.Format != formatV1 {
				return nil, nil, fmt.Errorf("the backup has format %d, which this version of Kogge does not know", m.Format)
			}
		case "end":
			sum = &Summary{}
			if err := json.NewDecoder(io.LimitReader(tr, 64<<20)).Decode(sum); err != nil {
				return nil, nil, fmt.Errorf("the summary is damaged: %w", err)
			}
		default:
			if sum != nil {
				return nil, nil, errors.New("the backup is damaged: files after the summary")
			}
		}
	}
	if m == nil {
		return nil, nil, errors.New("not a Kogge backup (the manifest is missing)")
	}
	if sum == nil {
		return nil, nil, errors.New("the backup is incomplete (the summary at its end is missing)")
	}
	return m, sum, nil
}

// Restore restores a backup into an empty installation: an empty database and a data directory that contains at
// most the master key. It recreates the schema of the backup, loads the rows, extracts the files, migrates to
// the current version and finally checks the installation (see Check).
//
// If a restore fails, the database and data directory are in between; drop and recreate the database, empty
// the data directory and restore again.
func Restore(ctx context.Context, o Options, path string, ro RestoreOptions) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m, sum, err := readManifest(f)
	if err != nil {
		return nil, err
	}
	latest, err := db.LatestVersions(o.Pool)
	if err != nil {
		return nil, err
	}
	if m.Schema.Schema > latest.Schema || m.Schema.Jobs > latest.Jobs {
		return nil, fmt.Errorf("the backup was written by a newer version of Kogge (%s); restore it with that version or a later one", m.Kogge)
	}
	if !ro.IgnoreMasterKey {
		if err := checkKeys(o, m); err != nil {
			return nil, err
		}
	}
	if err := emptyDatabase(ctx, o); err != nil {
		return nil, err
	}
	if err := emptyDataDir(o.DataDir, ro.KeyFile); err != nil {
		return nil, err
	}

	o.progress("Restoring a backup of Kogge %s from %s", m.Kogge, m.Created.Format("2006-01-02 15:04:05 MST"))
	if err := db.MigrateTo(ctx, o.Pool, o.logger(), m.Schema); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	err = db.InTx(ctx, o.Pool, func(tx pgx.Tx) error {
		l, err := newLoader(ctx, tx, m)
		if err != nil {
			return err
		}
		if err := extract(ctx, o, f, l); err != nil {
			return err
		}
		return l.finish(ctx, m, sum)
	})
	if err != nil {
		return nil, err
	}
	o.progress("Restored %d repositories, %d blobs, %d build logs and the database", sum.Repositories, sum.Blobs, sum.Logs)
	if _, err := o.Pool.Exec(ctx, "ANALYZE"); err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, o.Pool, o.logger()); err != nil {
		return nil, err
	}
	o.progress("Checking the installation ...")
	return Check(ctx, o, CheckOptions{})
}

// checkKeys makes sure the configured master key can read the secrets of the backup.
func checkKeys(o Options, m *Manifest) error {
	if len(m.MasterKeys) == 0 {
		return nil
	}
	var have []string
	if o.Keyring != nil {
		have = o.Keyring.KeyIDs()
	}
	for _, id := range m.MasterKeys {
		if !slices.Contains(have, id) {
			return fmt.Errorf("the master key of the backup (key %s) is not configured: copy the master key file of the backed-up "+
				"installation into place, or restore with -ignore-master-key and enter webhook secrets and secret build variables again", id)
		}
	}
	return nil
}

func emptyDatabase(ctx context.Context, o Options) error {
	var n int
	if err := o.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_class
		WHERE relnamespace = current_schema()::regnamespace AND relkind IN ('r', 'p', 'v', 'm', 'S', 'f')`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("the database is not empty: restore into a new, empty database")
	}
	return nil
}

// emptyDataDir allows only the master key, empty directories and what Kogge recreates at start-up (sockets,
// hook scripts and SSH host keys, which the backup's replace).
func emptyDataDir(dir, keyFile string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case rel == ".":
			return nil
		case d.IsDir() && (rel == "run" || rel == "git-hooks" || rel == "ssh"):
			return fs.SkipDir
		case d.IsDir():
			return nil
		case keyFile != "" && sameFile(path, keyFile):
			return nil
		}
		return fmt.Errorf("the data directory %s is not empty (%s): restore into an empty data directory", dir, filepath.ToSlash(rel))
	})
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// extract writes the files of the backup into the data directory and loads the table data.
func extract(ctx context.Context, o Options, f *os.File, l *loader) error {
	tr := tar.NewReader(f)
	var gz *gzip.Reader
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		kind, table, err := entryKind(hdr)
		if err != nil {
			return err
		}
		switch kind {
		case "database":
			if gz == nil {
				gz, err = gzip.NewReader(tr)
			} else {
				err = gz.Reset(tr)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", hdr.Name, err)
			}
			if err := l.load(ctx, table, gz); err != nil {
				return err
			}
		case "data":
			if err := extractFile(o.DataDir, hdr, tr); err != nil {
				return err
			}
		}
	}
}

func extractFile(dataDir string, hdr *tar.Header, r io.Reader) error {
	target := filepath.Join(dataDir, filepath.FromSlash(strings.TrimSuffix(hdr.Name, "/")))
	if hdr.Typeflag == tar.TypeDir {
		return os.MkdirAll(target, 0o750)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	// Files keep at most their owner's and group's permissions (git's objects are read-only).
	mode := fs.FileMode(hdr.Mode).Perm()&0o750 | 0o400
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != hdr.Size {
		err = fmt.Errorf("%s: short file", hdr.Name)
	}
	return err
}
