// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"kogge.de/kogge/internal/identity"
)

// Status badges: a small image with the state of a branch's pipelines, for READMEs and pages. Who may not read
// the repository gets "unknown", as for a repository that does not exist.

// badgeState is what a badge says, and its colour.
type badgeState struct {
	text, color string
}

var (
	badgePassing   = badgeState{"passing", "#2f9e44"}
	badgeFailing   = badgeState{"failing", "#e03131"}
	badgeRunning   = badgeState{"running", "#1c7ed6"}
	badgeCancelled = badgeState{"cancelled", "#868e96"}
	badgeNoRuns    = badgeState{"no runs", "#868e96"}
	badgeUnknown   = badgeState{"unknown", "#868e96"}
)

// BadgeState returns the state of the latest runs of a branch (default: the default branch) — of one pipeline, or
// of all: failing if one failed, running if one runs, passing if all succeeded. Pull request runs do not count.
func (s *Service) BadgeState(ctx context.Context, p *identity.Principal, key, slug, branch, pipeline string) badgeState {
	repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
	if err != nil {
		return badgeUnknown
	}
	if branch == "" {
		branch = repo.DefaultBranch
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (pipeline) state FROM runs
		WHERE repository_id = $1 AND branch = $2 AND event <> 'pull-request' AND ($3 = '' OR pipeline = $3)
		ORDER BY pipeline, created_at DESC, id DESC`, repo.ID, branch, pipeline)
	if err != nil {
		return badgeUnknown
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var state string
		if rows.Scan(&state) == nil {
			seen[state] = true
		}
	}
	switch {
	case rows.Err() != nil:
		return badgeUnknown
	case seen[RunFailed] || seen[RunError]:
		return badgeFailing
	case seen[RunRunning] || seen[RunQueued]:
		return badgeRunning
	case seen[RunCancelled]:
		return badgeCancelled
	case seen[RunSucceeded]:
		return badgePassing
	}
	return badgeNoRuns
}

func (h *WebHandler) badge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	label := strings.TrimSpace(q.Get("label"))
	if label == "" {
		label = "build"
	}
	if utf8.RuneCountInString(label) > 40 {
		label = string([]rune(label)[:40])
	}
	branch, pipeline := q.Get("branch"), q.Get("pipeline")
	if len(branch) > 255 || len(pipeline) > 100 {
		branch, pipeline = "", ""
	}
	state := h.S.BadgeState(r.Context(), identity.From(r.Context()), chi.URLParam(r, "key"), chi.URLParam(r, "slug"), branch, pipeline)
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// An image, not a document: nothing in it runs, also when it is opened on its own.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "no-cache, private")
	_, _ = w.Write([]byte(badgeSVG(label, state)))
}

// badgeSVG draws a badge: the label on a dark field, the state on a field of its colour.
func badgeSVG(label string, state badgeState) string {
	lw, sw := textWidth(label)+12, textWidth(state.text)+12
	total := lw + sw
	l, st := html.EscapeString(label), html.EscapeString(state.text)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`, total, l, st)
	fmt.Fprintf(&b, `<title>%s: %s</title>`, l, st)
	fmt.Fprintf(&b, `<clipPath id="r"><rect width="%d" height="20" rx="4"/></clipPath><g clip-path="url(#r)">`, total)
	fmt.Fprintf(&b, `<rect width="%d" height="20" fill="#3b4048"/><rect x="%d" width="%d" height="20" fill="%s"/></g>`, lw, lw, sw, state.color)
	b.WriteString(`<g fill="#fff" font-family="Inter,'DejaVu Sans',Verdana,sans-serif" font-size="11" text-anchor="middle">`)
	fmt.Fprintf(&b, `<text x="%d" y="14">%s</text><text x="%d" y="14" font-weight="600">%s</text></g></svg>`, lw/2, l, lw+sw/2, st)
	return b.String()
}

// textWidth estimates the width of a text at 11 pixels: badges are drawn without font metrics.
func textWidth(s string) int {
	w := 0.0
	for _, r := range s {
		switch {
		case strings.ContainsRune("iljtf.,:;|! '", r):
			w += 3.4
		case strings.ContainsRune("mwMW", r):
			w += 9.4
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			w += 7.2
		default:
			w += 6.3
		}
	}
	return int(w + 0.5)
}
