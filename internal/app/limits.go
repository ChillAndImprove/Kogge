// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package app

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kogge.de/kogge/internal/platform/httpx"
)

// The time limits of requests: an ordinary request of the API or the web application sends its
// body within KOGGE_REQUEST_TIMEOUT (a minute), ends its work within it — its database queries are canceled then,
// and it answers request.timeout — and has written its answer two minutes later. A slow client cannot hold a
// connection and a goroutine for long. Routes that legitimately take longer get limits of their own below, each
// with its reason. The operations of the API have, besides, the limits of their bodies (api.MaxBodyBytes, read
// within api.BodyReadTimeout).

// limitKind is a kind of request with its own limits.
type limitKind int

const (
	// ordinary requests: the default.
	ordinary limitKind = iota
	// transfer: uploads, downloads and Git move large amounts of data for as long as it takes, as long as data
	// keeps flowing (KOGGE_STALL_TIMEOUT); their work has no limit.
	transfer
	// stream: event streams stay open while a page is open and send at least every 25 seconds; they must not stall
	// either.
	stream
	// socket: WebSockets take over the connection and keep deadlines of their own (pings, read limits).
	socket
	// longPoll: agents wait up to 25 seconds for work (builds/protocol.PollTimeout).
	longPoll
	// slow: administrative operations that may work for many minutes (the directory sync reads the whole directory,
	// deleting a project or space deletes all it holds) within one transaction.
	slow
)

// routeLimits gives the kind of the routes, by method and pattern: of the operations of the API that are not
// ordinary, and of every other route, also when it is ordinary, so that none can be forgotten (TestRouteLimits).
var routeLimits = map[string]limitKind{
	// Git smart HTTP and LFS: clones and pushes of large repositories and large files.
	"GET /scm/{key}/{repo}/info/refs":                      transfer,
	"POST /scm/{key}/{repo}/git-upload-pack":               transfer,
	"POST /scm/{key}/{repo}/git-receive-pack":              transfer,
	"POST /scm/{key}/{repo}/info/lfs/objects/batch":        ordinary, // a JSON list of objects
	"GET /scm/{key}/{repo}/info/lfs/objects/{oid}":         transfer,
	"PUT /scm/{key}/{repo}/info/lfs/objects/{oid}":         transfer,
	"POST /scm/{key}/{repo}/info/lfs/objects/{oid}/verify": ordinary,
	// Large files: raw files and archives of repositories, attachments (also their uploads), exports of pages and spaces,
	// logs and artifacts of builds, the agent's binaries.
	"GET /api/v1/code/repos/{key}/{slug}/raw":     transfer,
	"GET /api/v1/code/repos/{key}/{slug}/archive": transfer,
	"POST /api/v1/issues/{key}/attachments":       transfer,
	"GET /api/v1/issue-attachments/{id}":          transfer,
	"POST /api/v1/wiki/pages/{id}/attachments":    transfer,
	"GET /api/v1/wiki/attachments/{id}":           transfer,
	"GET /api/v1/wiki/pages/{id}/export":          transfer,
	"GET /api/v1/wiki/exports/{id}/download":      transfer,
	"GET /api/v1/builds/jobs/{id}/log/raw":        transfer,
	"GET /api/v1/builds/artifacts/{id}":           transfer,
	"GET /downloads/{file}":                       transfer,
	// Event streams and the WebSocket of the editors of shared drafts.
	"GET /api/v1/live":                        stream,
	"GET /api/v1/builds/jobs/{id}/log/stream": stream,
	"GET /api/v1/wiki/pages/{id}/edit/socket": socket,
	// The agent protocol: the long-poll for work, uploads of logs, artifacts, caches and test reports, and
	// downloads of artifacts and caches.
	"POST /api/agent/v1/jobs/claim":                    longPoll,
	"POST /api/agent/v1/jobs/{id}/log":                 transfer,
	"PUT /api/agent/v1/jobs/{id}/artifacts/{name}":     transfer,
	"GET /api/agent/v1/jobs/{id}/artifacts/{artifact}": transfer,
	"GET /api/agent/v1/jobs/{id}/caches/{key}":         transfer,
	"PUT /api/agent/v1/jobs/{id}/caches/{key}":         transfer,
	"POST /api/agent/v1/jobs/{id}/reports/junit":       transfer,
	"POST /api/agent/v1/register":                      ordinary,
	"POST /api/agent/v1/jobs/{id}/heartbeat":           ordinary,
	"POST /api/agent/v1/jobs/{id}/complete":            ordinary,
	// Ordinary: status badges, the sign-on with the identity provider, health checks, the API's documentation and
	// the addresses of migrated tools (redirects).
	"GET /api/v1/code/repos/{key}/{slug}/badge.svg": ordinary,
	"GET /healthz":                                         ordinary,
	"GET /readyz":                                          ordinary,
	"GET /auth/oidc/login":                                 ordinary,
	"GET /auth/oidc/callback":                              ordinary,
	"POST /auth/oidc/backchannel-logout":                   ordinary,
	"GET /api/docs":                                        ordinary,
	"GET /api/v1/openapi.json":                             ordinary,
	"GET /api/v1/openapi.yaml":                             ordinary,
	"GET /api/v1/openapi-3.0.json":                         ordinary,
	"GET /api/v1/openapi-3.0.yaml":                         ordinary,
	"GET /api/*":                                           ordinary, // unknown paths of the API (404)
	"POST /api/*":                                          ordinary,
	"PUT /api/*":                                           ordinary,
	"PATCH /api/*":                                         ordinary,
	"DELETE /api/*":                                        ordinary,
	"QUERY /api/*":                                         ordinary,
	"GET /display/{space}":                                 ordinary,
	"GET /display/{space}/*":                               ordinary,
	"GET /pages/viewpage.action":                           ordinary,
	"GET /spaces/{space}":                                  ordinary,
	"GET /spaces/{space}/overview":                         ordinary,
	"GET /spaces/{space}/pages/{id}":                       ordinary,
	"GET /spaces/{space}/pages/{id}/*":                     ordinary,
	"GET /spaces/{space}/blog/*":                           ordinary,
	"GET /x/{tiny}":                                        ordinary,
	"GET /download/attachments/{page}/{file}":              ordinary,
	"GET /browse/{key}":                                    ordinary,
	"GET /projects/{key}/issues/{issue}":                   ordinary,
	"GET /secure/RapidBoard.jspa":                          ordinary,
	"GET /secure/attachment/{id}/*":                        ordinary,
	"GET /issues/":                                         ordinary,
	"GET /projects/{key}/repos/{slug}":                     ordinary,
	"GET /projects/{key}/repos/{slug}/*":                   ordinary,
	"GET /browse/{key}/*":                                  ordinary,
	"GET /build/viewBuildResults.action":                   ordinary,
	"GET /chain/admin/config/*":                            ordinary,
	"GET /chain/result/viewChainResult.action":             ordinary,
	"GET /chain/viewChain.action":                          ordinary,
	"GET /deploy/viewDeploymentProjectEnvironments.action": ordinary,
	"GET /deploy/viewEnvironment.action":                   ordinary,
	// Operations of the API that are not ordinary.
	"POST /api/v1/admin/directory-sync":      slow,
	"DELETE /api/v1/projects/{key}":          slow,
	"DELETE /api/v1/wiki/spaces/{key}":       slow,
	"DELETE /api/v1/code/repos/{key}/{slug}": slow,
}

// kindNames name the kinds of requests (RouteLimits).
var kindNames = map[limitKind]string{ordinary: "ordinary", transfer: "transfer", stream: "stream", socket: "socket",
	longPoll: "long-poll", slow: "slow"}

// RouteLimits returns the kinds of the routes that name theirs, by method and pattern (for tests that check every
// route).
func RouteLimits() map[string]string {
	out := map[string]string{}
	for route, kind := range routeLimits {
		out[route] = kindNames[kind]
	}
	return out
}

// limitsOf returns the limits of a kind of request.
func (a *App) limitsOf(k limitKind) httpx.Limits {
	work := a.Cfg.RequestTimeout
	switch k {
	case transfer, stream:
		return httpx.Limits{Stall: a.Cfg.StallTimeout}
	case socket:
		return httpx.Limits{}
	case longPoll:
		return httpx.Limits{Body: work, Work: 25*time.Second + work, Respond: 25*time.Second + work + 2*time.Minute}
	case slow:
		return httpx.Limits{Body: work, Work: 30 * time.Minute, Respond: 32 * time.Minute}
	}
	return httpx.Limits{Body: work, Work: work, Respond: work + 2*time.Minute}
}

// limits applies the limits of each request's route. It runs before the routing, so it looks the route up itself.
func (a *App) limits(mux *chi.Mux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.RawPath
			if path == "" {
				path = r.URL.Path
			}
			pattern := mux.Find(chi.NewRouteContext(), r.Method, path)
			kind := routeLimits[r.Method+" "+pattern]
			if pattern == "" && strings.HasPrefix(r.URL.Path, "/assets/") {
				kind = transfer // the web application's scripts are large for slow connections
			}
			a.limitsOf(kind).Serve(w, r, next)
		})
	}
}
