// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/wiki"
)

// The wiki writes the output of its macros into exports itself; macros that show issues, code and builds get their
// data here, with the permissions of the person who exports.

type macroSource struct{ s *Service }

// MacroSource returns the data of the wiki's macros that show other products.
func (s *Service) MacroSource() wiki.MacroSource { return macroSource{s: s} }

// hidden reports whether an error says the reader cannot see something (or it does not exist).
func hidden(err error) bool {
	var e *apperr.Error
	return errors.As(err, &e) && e.Status >= 400 && e.Status < 500
}

func (m macroSource) Issue(ctx context.Context, p *identity.Principal, key string) (*wiki.MacroIssue, error) {
	i, err := m.s.issues.GetIssue(ctx, p, key)
	if hidden(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &wiki.MacroIssue{Key: i.Key, Summary: i.Summary}
	if i.Status != nil {
		out.Status, out.StatusCategory = i.Status.Name, i.Status.Category
	}
	if i.Assignee != nil {
		out.Assignee = i.Assignee.DisplayName
	}
	return out, nil
}

func (m macroSource) Issues(ctx context.Context, p *identity.Principal, query string, limit int) ([]wiki.MacroIssue, int, error) {
	res, err := m.s.issues.Search(ctx, p, query, limit, 0)
	if err != nil {
		return nil, 0, err
	}
	out := make([]wiki.MacroIssue, 0, len(res.Issues))
	for _, i := range res.Issues {
		mi := wiki.MacroIssue{Key: i.Key, Summary: i.Summary}
		if i.Status != nil {
			mi.Status, mi.StatusCategory = i.Status.Name, i.Status.Category
		}
		if i.Assignee != nil {
			mi.Assignee = i.Assignee.DisplayName
		}
		out = append(out, mi)
	}
	return out, res.Total, nil
}

// repository splits "KEY/slug".
func repository(name string) (string, string, bool) {
	key, slug, ok := strings.Cut(strings.TrimSpace(name), "/")
	return key, slug, ok && key != "" && slug != ""
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

func (m macroSource) File(ctx context.Context, p *identity.Principal, repo, ref, path string) (*wiki.MacroFile, error) {
	key, slug, ok := repository(repo)
	if !ok {
		return nil, nil
	}
	f, err := m.s.code.File(ctx, p, key, slug, ref, path)
	if hidden(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if f.Binary || f.LFS != nil {
		return nil, nil
	}
	// The lines are highlighted HTML: their text is what the export needs.
	lines := make([]string, len(f.Lines))
	for i, l := range f.Lines {
		lines[i] = html.UnescapeString(htmlTag.ReplaceAllString(l, ""))
	}
	u := "/code/" + strings.ToLower(key) + "/" + slug + "/src/" + strings.TrimPrefix(f.Path, "/")
	if ref != "" {
		u += "?at=" + url.QueryEscape(ref)
	}
	return &wiki.MacroFile{Path: f.Path, Ref: f.Ref, Language: f.Language, Lines: lines, Markdown: f.Markdown, URL: u}, nil
}

func (m macroSource) Readme(ctx context.Context, p *identity.Principal, repo, ref string) (*wiki.MacroFile, error) {
	key, slug, ok := repository(repo)
	if !ok {
		return nil, nil
	}
	t, err := m.s.code.Tree(ctx, p, key, slug, ref, "")
	if hidden(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Readme == "" {
		return nil, nil
	}
	return m.File(ctx, p, repo, ref, t.Readme)
}

func (m macroSource) LatestRun(ctx context.Context, p *identity.Principal, repo, pipeline, branch string) (*wiki.MacroRun, error) {
	key, slug, ok := repository(repo)
	if !ok || m.s.builds == nil {
		return nil, nil
	}
	runs, _, err := m.s.builds.Runs(ctx, p, key, slug, builds.RunFilter{Pipeline: pipeline, Branch: branch, Limit: 1})
	if hidden(err) {
		return nil, nil
	}
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	r := runs[0]
	return &wiki.MacroRun{Name: r.Name, Number: r.Number, State: r.State, Branch: r.Branch, Created: r.Created,
		URL: builds.RunPath(key, slug, r.Number)}, nil
}
