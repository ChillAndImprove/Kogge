// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package webui serves the web application: the built single-page application embedded in release builds
// (build tag webui), with long-lived caching for hashed assets and index.html for application routes.
package webui

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"kogge.de/kogge/internal/platform/httpx"
)

// Handler serves the application. Without embedded assets it serves a page that explains how to build them.
// externalImages tells whether documents may show images from other servers: the page's policy then lets the
// browser load them (it is asked for every page, so a change of the setting applies to the next page load).
func Handler(externalImages func(ctx context.Context) bool) http.Handler {
	policy := func(r *http.Request) string {
		if externalImages != nil && externalImages(r.Context()) {
			return httpx.AppCSPExternalImages
		}
		return httpx.AppCSP
	}
	assets := embedded()
	if assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", policy(r))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(devPage))
		})
	}
	index, _ := fs.ReadFile(assets, "index.html")
	started := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if f, err := assets.Open(name); err == nil {
				st, err := f.Stat()
				f.Close()
				if err == nil && !st.IsDir() {
					if strings.HasPrefix(name, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					} else {
						w.Header().Set("Cache-Control", "public, max-age=3600")
					}
					http.ServeFileFS(w, r, assets, name)
					return
				}
			}
			if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" && !strings.Contains(name, "/") {
				http.NotFound(w, r)
				return
			}
		}
		// Application route: the SPA's router takes over.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", policy(r))
		http.ServeContent(w, r, "index.html", started, bytes.NewReader(index))
	})
}

const devPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Kogge</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; line-height: 1.5">
<h1>Kogge</h1>
<p>The server is running, but this build does not contain the web application.</p>
<p>During development, run <code>make web-dev</code> and open <a href="http://localhost:5173">http://localhost:5173</a>.
Release builds embed the application (<code>make build</code>).</p>
<p>The API description is at <a href="/api/v1/openapi.json">/api/v1/openapi.json</a>.</p>
</body></html>`
