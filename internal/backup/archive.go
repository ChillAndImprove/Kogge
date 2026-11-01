// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// addJSON adds a small JSON file.
func (b *backup) addJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return b.addBytes(name, data)
}

func (b *backup) addBytes(name string, data []byte) error {
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: 0o600, ModTime: time.Now()}
	if err := b.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := b.tw.Write(data)
	return err
}

// name returns the name of a file of the data directory in the archive.
func (b *backup) name(path string) (string, error) {
	rel, err := filepath.Rel(b.o.DataDir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s is not in the data directory", path)
	}
	return filepath.ToSlash(rel), nil
}

// file adds a regular file of the data directory, as far as it is written when the copy starts.
func (b *backup) file(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	name, err := b.name(path)
	if err != nil {
		return err
	}
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: fi.Size(), Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime()}
	if err := b.tw.WriteHeader(hdr); err != nil {
		return err
	}
	n, err := io.CopyN(b.tw, f, fi.Size())
	b.sum.Bytes += n
	if err != nil {
		return fmt.Errorf("copy %s: %w", path, err)
	}
	return nil
}

// tree adds a directory with its subdirectories (also empty ones: git needs them) and regular files. skip leaves
// out entries; other kinds of files are left out with a warning.
func (b *backup) tree(root string, skip func(fs.DirEntry) bool) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if path != root && skip != nil && skip(d) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.IsDir():
			name, err := b.name(path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return b.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o750, ModTime: info.ModTime()})
		case d.Type().IsRegular():
			err := b.file(path)
			if errors.Is(err, fs.ErrNotExist) {
				return nil // a temporary file of git that went away
			}
			return err
		default:
			b.o.progress("warning: %s is not a regular file; it is not part of the backup", path)
			return nil
		}
	})
}

// chunkSize is the amount of table data per archive file. Files end at row boundaries, so each can be loaded
// with a COPY of its own.
const chunkSize = 32 << 20

// chunker receives the COPY data of a table and adds it to the archive as gzip files of whole rows.
type chunker struct {
	b     *backup
	table string
	n     int
	raw   int64
	buf   bytes.Buffer
	gz    *gzip.Writer
}

func (c *chunker) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if c.raw >= chunkSize {
			// In COPY's text format, rows end with a newline and newlines in values are escaped.
			if i := bytes.IndexByte(p, '\n'); i >= 0 {
				if err := c.write(p[:i+1]); err != nil {
					return 0, err
				}
				p = p[i+1:]
				if err := c.flush(); err != nil {
					return 0, err
				}
				continue
			}
		}
		if err := c.write(p); err != nil {
			return 0, err
		}
		break
	}
	return n, nil
}

func (c *chunker) write(p []byte) error {
	if c.raw == 0 {
		c.buf.Reset()
		if c.gz == nil {
			c.gz = gzip.NewWriter(&c.buf)
		} else {
			c.gz.Reset(&c.buf)
		}
	}
	c.raw += int64(len(p))
	_, err := c.gz.Write(p)
	return err
}

// flush adds the data received so far.
func (c *chunker) flush() error {
	if c.raw == 0 {
		return nil
	}
	if err := c.gz.Close(); err != nil {
		return err
	}
	c.n++
	c.b.sum.Bytes += c.raw
	c.raw = 0
	return c.b.addBytes(fmt.Sprintf("database/%s/%05d.copy.gz", c.table, c.n), c.buf.Bytes())
}

// Top-level directories of the data directory in a backup.
var dataDirs = map[string]bool{"repositories": true, "blobs": true, "logs": true, "ssh": true}

// entryKind classifies a file of the archive: "manifest", "end", "database" (with the table) or "data".
func entryKind(hdr *tar.Header) (kind, table string, err error) {
	name := hdr.Name
	switch name {
	case manifestName:
		return "manifest", "", nil
	case endName:
		return "end", "", nil
	}
	clean := path.Clean(name)
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || clean == ".." ||
		strings.HasPrefix(clean, "../") || clean != strings.TrimSuffix(name, "/") {
		return "", "", fmt.Errorf("unsafe name %q", name)
	}
	parts := strings.Split(clean, "/")
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
		return "", "", fmt.Errorf("%s: unsupported kind of file", name)
	}
	if parts[0] == "database" {
		if len(parts) != 3 || hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(parts[2], ".copy.gz") {
			return "", "", fmt.Errorf("%s: unexpected file", name)
		}
		return "database", parts[1], nil
	}
	if !dataDirs[parts[0]] {
		return "", "", fmt.Errorf("%s: unexpected file", name)
	}
	return "data", "", nil
}
