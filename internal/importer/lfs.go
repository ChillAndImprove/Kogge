// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// Git LFS objects of migrated repositories: the extraction finds the pointer files among the blobs of a mirror and
// downloads the objects through the source's LFS batch API (the public Git LFS specification). They are stored in
// the work directory under their ids, once for all repositories.

const lfsMediaType = "application/vnd.git-lfs+json"

var lfsOID = regexp.MustCompile(`^[0-9a-f]{64}$`)

type lfsPointer struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// parseLFSPointer reads a pointer file.
func parseLFSPointer(b []byte) (lfsPointer, bool) {
	s := string(b)
	if !strings.HasPrefix(s, "version https://git-lfs.github.com/spec/v1\n") {
		return lfsPointer{}, false
	}
	var p lfsPointer
	sized := false
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "oid sha256:"); ok {
			p.OID = v
		} else if v, ok := strings.CutPrefix(line, "size "); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			p.Size, sized = n, err == nil && n >= 0
		}
	}
	return p, sized && lfsOID.MatchString(p.OID)
}

// lfsPointers lists the LFS objects that blobs of a bare repository point to, ordered by id.
func lfsPointers(ctx context.Context, gitDir string) ([]lfsPointer, error) {
	// Candidates are small blobs: pointer files have about 130 bytes.
	list := exec.CommandContext(ctx, "git", "--git-dir", gitDir, "cat-file", "--batch-all-objects", "--unordered",
		"--batch-check=%(objecttype) %(objectname) %(objectsize)")
	out, err := list.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	list.Stderr = &stderr
	if err := list.Start(); err != nil {
		return nil, err
	}
	var names bytes.Buffer
	count := 0
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 3 && f[0] == "blob" {
			if n, err := strconv.Atoi(f[2]); err == nil && n >= 100 && n <= 1024 {
				names.WriteString(f[1] + "\n")
				count++
			}
		}
	}
	if err := list.Wait(); err != nil {
		return nil, fmt.Errorf("git cat-file: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if count == 0 {
		return nil, nil
	}
	read := exec.CommandContext(ctx, "git", "--git-dir", gitDir, "cat-file", "--batch")
	read.Stdin = &names
	out, err = read.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := read.Start(); err != nil {
		return nil, err
	}
	found := map[string]lfsPointer{}
	r := bufio.NewReader(out)
	for range count {
		header, err := r.ReadString('\n')
		if err != nil {
			_ = read.Wait()
			return nil, fmt.Errorf("git cat-file: %w", err)
		}
		f := strings.Fields(header)
		n := 0
		if len(f) == 3 {
			n, _ = strconv.Atoi(f[2])
		}
		data := make([]byte, n+1) // the content and a newline
		if _, err := io.ReadFull(r, data); err != nil {
			_ = read.Wait()
			return nil, fmt.Errorf("git cat-file: %w", err)
		}
		if p, ok := parseLFSPointer(data[:n]); ok {
			found[p.OID] = p
		}
	}
	if err := read.Wait(); err != nil {
		return nil, err
	}
	pointers := make([]lfsPointer, 0, len(found))
	for _, p := range found {
		pointers = append(pointers, p)
	}
	slices.SortFunc(pointers, func(a, b lfsPointer) int { return strings.Compare(a.OID, b.OID) })
	return pointers, nil
}

// lfsFile is the place of an object in the work directory.
func lfsFile(prefix, oid string) []string {
	return []string{prefix, "lfs", oid[:2], oid[2:4], oid}
}

type lfsBatchObject struct {
	OID     string `json:"oid"`
	Size    int64  `json:"size"`
	Actions struct {
		Download *struct {
			Href   string            `json:"href"`
			Header map[string]string `json:"header"`
		} `json:"download"`
	} `json:"actions"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// lfsTransport reaches LFS servers within the outbound policy of sources. Objects download of any size: there is
// no overall timeout, only one for the answer to start.
var lfsTransport = func() *http.Transport {
	t := sourceTransport()
	t.ResponseHeaderTimeout = 2 * time.Minute
	return t
}()

// lfsClient downloads objects, from wherever the source's LFS server sends the importer (storage of its own, for
// one): an object counts only if its content has its checksum.
var lfsClient = &http.Client{Transport: lfsTransport}

// fetchLFS downloads the objects the work directory does not have yet from the LFS server of a repository (whose
// clone address is repoURL; auth is the Authorization header for the source), several at a time. It returns the
// ids of the objects the source does not have.
func fetchLFS(ctx context.Context, w Workdir, prefix, repoURL, auth string, objects []lfsPointer, concurrency int,
	log *slog.Logger) (missing []string, err error) {
	var todo []lfsPointer
	for _, o := range objects {
		if w.Size(lfsFile(prefix, o.OID)...) != o.Size {
			todo = append(todo, o)
		}
	}
	p := newProgress(log, "LFS objects of "+repoURL, len(todo))
	defer p.finish()
	for len(todo) > 0 {
		batch := todo[:min(100, len(todo))]
		todo = todo[len(batch):]
		body, err := json.Marshal(map[string]any{"operation": "download", "transfers": []string{"basic"}, "objects": batch})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, repoURL+"/info/lfs/objects/batch", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", lfsMediaType)
		req.Header.Set("Content-Type", lfsMediaType)
		req.Header.Set("Authorization", auth)
		// The batch API is the source's own: it does not send the importer (and the credentials) elsewhere.
		res, err := (&http.Client{Transport: lfsTransport, CheckRedirect: sameSource(repoURL)}).Do(req)
		if err != nil {
			return nil, err
		}
		var answer struct {
			Objects []lfsBatchObject `json:"objects"`
		}
		if res.StatusCode == http.StatusNotFound {
			// LFS is switched off for the repository: its pointer files stay without content.
			res.Body.Close()
			for _, p := range append(batch, todo...) {
				missing = append(missing, p.OID)
			}
			return missing, nil
		}
		if res.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
			res.Body.Close()
			return nil, &StatusError{URL: req.URL.String(), Status: res.StatusCode, Body: string(b)}
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&answer)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("LFS batch answer: %w", err)
		}
		answered := map[string]bool{}
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(max(concurrency, 1))
		for _, o := range answer.Objects {
			i := slices.IndexFunc(batch, func(p lfsPointer) bool { return p.OID == o.OID })
			if i < 0 || answered[o.OID] {
				continue
			}
			answered[o.OID], o.Size = true, batch[i].Size // the size of the pointer file, not the answer's
			if o.Error != nil || o.Actions.Download == nil {
				missing = append(missing, o.OID)
				continue
			}
			g.Go(func() error {
				var err error
				// Downloads that break off (or stall) are tried again; the address stays valid for a while.
				for attempt := 0; attempt < 3; attempt++ {
					if err = downloadLFS(gctx, w.Path(lfsFile(prefix, o.OID)...), repoURL, auth, o); err == nil || gctx.Err() != nil {
						break
					}
					var se *StatusError
					if errors.As(err, &se) && !busy(se.Status) || refused(err) {
						break
					}
				}
				if err != nil {
					return fmt.Errorf("LFS object %s: %w", o.OID, err)
				}
				p.add(1)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		for _, p := range batch {
			if !answered[p.OID] {
				missing = append(missing, p.OID)
			}
		}
	}
	return missing, nil
}

// readCloser reads from r and closes c.
type readCloser struct {
	io.Reader
	c io.Closer
}

func (r readCloser) Close() error { return r.c.Close() }

// downloadLFS stores an object after checking its size and checksum. The source's credentials are only sent to the
// source; other addresses (e.g. a storage service) get the headers of the batch answer alone.
func downloadLFS(ctx context.Context, file, repoURL, auth string, o lfsBatchObject) error {
	href := o.Actions.Download.Href
	if u, err := url.Parse(href); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("unusable download address %q", href)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return err
	}
	for k, v := range o.Actions.Download.Header {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Authorization") == "" && sameOrigin(href, repoURL) {
		req.Header.Set("Authorization", auth)
	}
	res, err := lfsClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		return &StatusError{URL: href, Status: res.StatusCode, Body: string(b)}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	tmp := file + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := copyWithStall(io.MultiWriter(f, h), readCloser{io.LimitReader(res.Body, o.Size+1), res.Body}, 2*time.Minute)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != o.Size || hex.EncodeToString(h.Sum(nil)) != o.OID) {
		err = errors.New("the downloaded content does not match the object's size and checksum")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}
