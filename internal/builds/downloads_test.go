// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/testutil/apitest"
)

// TestAgentDownloads: the agent binaries of the installation are listed for signed-in users and downloadable
// without sign-in; nothing else in the directory is served.
func TestAgentDownloads(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"kogge-agent-linux-amd64": "linux agent", "kogge-agent-windows-amd64.exe": "windows agent",
		"notes.txt": "not an agent"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := apitest.New(t, func(c *config.Config) { c.AgentDir = dir })
	anon := env.Anonymous()
	if r := anon.Do(http.MethodGet, "/api/v1/builds/agent-downloads", nil); r.Status != http.StatusUnauthorized {
		t.Errorf("anonymous list: %d", r.Status)
	}
	var list []struct {
		Platform string `json:"platform"`
		URL      string `json:"url"`
		Size     int64  `json:"size"`
		SHA256   string `json:"sha256"`
	}
	env.CreateUser("dev").Get("/api/v1/builds/agent-downloads", &list)
	sum := sha256.Sum256([]byte("linux agent"))
	if len(list) != 2 || list[0].Platform != "linux-amd64" || list[1].Platform != "windows-amd64" ||
		list[0].SHA256 != hex.EncodeToString(sum[:]) || list[0].Size != int64(len("linux agent")) {
		t.Fatalf("list = %+v", list)
	}

	r := anon.Do(http.MethodGet, list[0].URL, nil)
	if r.Status != http.StatusOK || string(r.Body) != "linux agent" || r.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "kogge-agent-linux-amd64") {
		t.Errorf("download: %d %q %v", r.Status, r.Body, r.Header)
	}
	for _, path := range []string{"/downloads/notes.txt", "/downloads/..%2Fkogge-agent-linux-amd64", "/downloads/kogge-agent-linux"} {
		if r := anon.Do(http.MethodGet, path, nil); r.Status != http.StatusNotFound {
			t.Errorf("%s: %d", path, r.Status)
		}
	}
	sums := string(anon.Do(http.MethodGet, "/downloads/SHA256SUMS", nil).Body)
	if !strings.Contains(sums, hex.EncodeToString(sum[:])+"  kogge-agent-linux-amd64\n") || strings.Contains(sums, "notes.txt") {
		t.Errorf("SHA256SUMS:\n%s", sums)
	}
}
