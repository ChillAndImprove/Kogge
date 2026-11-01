// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
)

// Downloads offers the agent binaries that come with the installation (the image has them in
// /usr/share/kogge/agents), so that build hosts without internet access get the agent from Kogge.
type Downloads struct {
	Dir string

	mu   sync.Mutex
	sums map[string]checksum
}

type checksum struct {
	size    int64
	modTime time.Time
	sha256  string
}

// AgentDownload is a binary of the agent.
type AgentDownload struct {
	Platform string `json:"platform" doc:"Operating system and architecture, e.g. linux-amd64"`
	File     string `json:"file"`
	URL      string `json:"url" doc:"Download address, relative to Kogge's address"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

var agentFile = regexp.MustCompile(`^kogge-agent-([a-z0-9]+)-([a-z0-9]+)(\.exe)?$`)

// List returns the binaries, sorted by platform.
func (d *Downloads) List() ([]AgentDownload, error) {
	list := []AgentDownload{}
	if d.Dir == "" {
		return list, nil
	}
	entries, err := os.ReadDir(d.Dir)
	if os.IsNotExist(err) {
		return list, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		m := agentFile.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		sum, err := d.checksum(e.Name(), info)
		if err != nil {
			return nil, err
		}
		list = append(list, AgentDownload{Platform: m[1] + "-" + m[2], File: e.Name(), URL: "/downloads/" + e.Name(),
			Size: info.Size(), SHA256: sum})
	}
	slices.SortFunc(list, func(a, b AgentDownload) int { return strings.Compare(a.Platform, b.Platform) })
	return list, nil
}

// checksum returns the SHA-256 of a file, computed once per version of the file.
func (d *Downloads) checksum(name string, info os.FileInfo) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.sums[name]; ok && c.size == info.Size() && c.modTime.Equal(info.ModTime()) {
		return c.sha256, nil
	}
	f, err := os.Open(filepath.Join(d.Dir, name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if d.sums == nil {
		d.sums = map[string]checksum{}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	d.sums[name] = checksum{size: info.Size(), modTime: info.ModTime(), sha256: sum}
	return sum, nil
}

// Routes serves the binaries and their checksums (SHA256SUMS) without sign-in: build hosts download them with
// curl, and the agent is no secret.
func (d *Downloads) Routes(r chi.Router) {
	r.Get("/downloads/{file}", d.serve)
}

func (d *Downloads) serve(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "file")
	list, err := d.List()
	if err != nil {
		http.Error(w, "downloads unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if name == "SHA256SUMS" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, a := range list {
			fmt.Fprintf(w, "%s  %s\n", a.SHA256, a.File)
		}
		return
	}
	i := slices.IndexFunc(list, func(a AgentDownload) bool { return a.File == name })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(d.Dir, list[i].File))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+list[i].File+`"`)
	http.ServeContent(w, r, list[i].File, info.ModTime(), f)
}

// RegisterDownloads adds the API operation that lists the agent binaries.
func RegisterDownloads(a huma.API, d *Downloads) {
	huma.Register(a, api.Op("list-agent-downloads", http.MethodGet, "/api/v1/builds/agent-downloads",
		"Agent binaries offered for download", tagAgents),
		func(ctx context.Context, _ *struct{}) (*body[[]AgentDownload], error) {
			if !identity.From(ctx).Authenticated() {
				return nil, apperr.Unauthorized("auth.required", "Sign in to see the agent downloads.")
			}
			return out(d.List())
		})
}
