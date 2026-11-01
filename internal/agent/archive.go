// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package agent

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"kogge.de/kogge/internal/builds/spec"
)

// Artifacts, caches and reports are files of the workspace selected by glob patterns (relative, slash-separated;
// "*" within a path segment, "**" across segments; a pattern naming a directory selects everything below it).
// Symbolic links are never followed: a job could otherwise point them at files of the build host.

// cleanPattern normalises a pattern; it rejects patterns that leave the workspace.
func cleanPattern(p string) (string, error) {
	p = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "./")
	if p == "" || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || filepath.IsAbs(p) {
		return "", fmt.Errorf("the path %q is outside of the workspace", p)
	}
	return strings.TrimSuffix(p, "/"), nil
}

// matchFiles returns the files and symbolic links below root that the patterns select, sorted.
func matchFiles(root string, patterns []string) ([]string, error) {
	var clean []string
	for _, p := range patterns {
		c, err := cleanPattern(p)
		if err != nil {
			return nil, err
		}
		clean = append(clean, c)
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			for _, pat := range clean {
				if pat == rel || spec.MatchGlob(pat, rel) {
					// Everything below a selected directory.
					err := filepath.WalkDir(p, func(q string, e fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if !e.IsDir() {
							r, _ := filepath.Rel(root, q)
							out = append(out, filepath.ToSlash(r))
						}
						return nil
					})
					if err != nil {
						return err
					}
					return filepath.SkipDir
				}
			}
			return nil
		}
		for _, pat := range clean {
			if pat == rel || spec.MatchGlob(pat, rel) {
				out = append(out, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return compactSorted(out), nil
}

func compactSorted(list []string) []string {
	out := list[:0]
	for i, s := range list {
		if i == 0 || s != list[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// writeZip writes the files (relative to root) as a zip archive.
func writeZip(w io.Writer, root string, files []string) error {
	zw := zip.NewWriter(w)
	for _, rel := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		h.Name = rel
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			h.Method = zip.Store
			fw, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(fw, filepath.ToSlash(target)); err != nil {
				return err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		h.Method = zip.Deflate
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(fw, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// safeTarget returns where an archive entry goes below root, or an error if it would leave root or pass through a
// symbolic link.
func safeTarget(root, name string) (string, error) {
	name = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(name)), "/")
	if name == "" || name == "." {
		return "", fmt.Errorf("invalid entry %q", name)
	}
	parts := strings.Split(name, "/")
	cur := root
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return "", fmt.Errorf("entry %q passes through %s", name, cur)
		}
	}
	return filepath.Join(root, filepath.FromSlash(name)), nil
}

// linkInside reports whether a symbolic link at target pointing to dest stays below root.
func linkInside(root, target, dest string) bool {
	if filepath.IsAbs(dest) || strings.HasPrefix(filepath.ToSlash(dest), "/") {
		return false
	}
	resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(dest))
	rel, err := filepath.Rel(root, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// writeFile creates a regular file, replacing a symbolic link instead of writing through it.
func writeFile(target string, mode fs.FileMode, r io.Reader) error {
	if fi, err := os.Lstat(target); err == nil && (fi.Mode()&os.ModeSymlink != 0 || fi.IsDir()) {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeLink(root, target, dest string) error {
	if !linkInside(root, target, dest) {
		return fmt.Errorf("the link %s points outside of the workspace", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(target)
	return os.Symlink(filepath.FromSlash(dest), target)
}

// maxUnpacked bounds what one archive unpacks to: archives of caches and artifacts are written by jobs, and a
// small archive of zeros can fill a disk.
var maxUnpacked int64 = 32 << 30

var errUnpackedTooLarge = errors.New("the archive unpacks to more than the limit; extraction stopped")

// budget counts the bytes an archive has unpacked to.
type budget struct{ left int64 }

func (b *budget) reader(r io.Reader) io.Reader { return &budgetReader{r: r, b: b} }

type budgetReader struct {
	r io.Reader
	b *budget
}

func (br *budgetReader) Read(p []byte) (int, error) {
	if br.b.left <= 0 {
		return 0, errUnpackedTooLarge
	}
	if int64(len(p)) > br.b.left {
		p = p[:br.b.left]
	}
	n, err := br.r.Read(p)
	br.b.left -= int64(n)
	return n, err
}

// extractZip unpacks a zip archive below root; it returns the problems with single entries, which it skips.
func extractZip(archive, root string) ([]string, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var problems []string
	b := &budget{left: maxUnpacked}
	for _, f := range zr.File {
		target, err := safeTarget(root, f.Name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = os.MkdirAll(target, 0o755)
		case mode&os.ModeSymlink != 0:
			var rc io.ReadCloser
			if rc, err = f.Open(); err == nil {
				var b []byte
				b, err = io.ReadAll(io.LimitReader(rc, 4096))
				rc.Close()
				if err == nil {
					err = writeLink(root, target, string(b))
				}
			}
		default:
			var rc io.ReadCloser
			if rc, err = f.Open(); err == nil {
				err = writeFile(target, mode, b.reader(rc))
				rc.Close()
			}
		}
		if errors.Is(err, errUnpackedTooLarge) {
			return problems, err
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	return problems, nil
}

// writeTarGz writes the files (relative to root) as a gzip-compressed tar archive.
func writeTarGz(w io.Writer, root string, files []string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, rel := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		link := ""
		if fi.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !fi.Mode().IsRegular() {
			continue
		}
		h, err := tar.FileInfoHeader(fi, filepath.ToSlash(link))
		if err != nil {
			return err
		}
		h.Name = rel
		h.Uname, h.Gname, h.Uid, h.Gid = "", "", 0, 0
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// extractTarGz unpacks a gzip-compressed tar archive below root; problems with single entries are returned.
func extractTarGz(r io.Reader, root string) ([]string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var problems []string
	b := &budget{left: maxUnpacked}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return problems, nil
		}
		if err != nil {
			return problems, err
		}
		target, err := safeTarget(root, h.Name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeSymlink:
			err = writeLink(root, target, h.Linkname)
		case tar.TypeReg:
			err = writeFile(target, fs.FileMode(h.Mode), b.reader(tr))
		default:
			continue
		}
		if errors.Is(err, errUnpackedTooLarge) {
			return problems, err
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
}

// hashFiles returns a short hash over the paths and contents of the regular files the patterns select.
func hashFiles(root string, patterns []string) (string, int, error) {
	files, err := matchFiles(root, patterns)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n := 0
	for _, rel := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			return "", 0, err
		}
		io.WriteString(h, rel+"\x00")
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", 0, err
		}
		n++
	}
	return hex.EncodeToString(h.Sum(nil))[:16], n, nil
}

// cacheKey turns a cache definition into the key the server accepts ([a-z0-9._-], at most 200 characters).
func cacheKey(key, fileHash string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	k := strings.Trim(b.String(), "-.")
	if k == "" {
		k = "cache"
	}
	if fileHash != "" {
		k += "-" + fileHash
	}
	if len(k) > 200 {
		k = k[:200]
	}
	return k
}
