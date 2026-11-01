// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"runtime"
	"strings"
	"sync"
	"time"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
)

// Code search: the lines of a repository's files at a branch, tag or commit that contain a text or
// match an expression. Searching reads every file, so few run at once and none for long.

const (
	searchMaxMatches = 1000
	searchMaxPerFile = 50
	searchTimeout    = 20 * time.Second
)

var (
	searchSlots     chan struct{}
	searchSlotsOnce sync.Once
)

// SearchResult lists the lines of a repository's files that match a search, by file.
type SearchResult struct {
	Ref      string       `json:"ref"`
	Commit   string       `json:"commit"`
	Files    []SearchFile `json:"files"`
	Matches  int          `json:"matches"`
	Complete bool         `json:"complete" doc:"False when there were more matches than listed"`
}

// SearchFile is a file with matching lines.
type SearchFile struct {
	Path  string       `json:"path"`
	Lines []SearchLine `json:"lines"`
	More  bool         `json:"more" doc:"The file has more matching lines than listed"`
}

// SearchLine is a matching line.
type SearchLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SearchCode searches the text files of a repository (read access) at a ref (default: the default branch).
func (s *Service) SearchCode(ctx context.Context, p *identity.Principal, key, slug, ref, query string, regex, caseSensitive bool,
	path string) (*SearchResult, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 || len(query) > 200 || strings.ContainsAny(query, "\x00\n\r") {
		return nil, apperr.Invalid("search.query", "Search for two to 200 characters on one line.")
	}
	if regex {
		if _, err := regexp.Compile(query); err != nil {
			detail := err.Error()
			if se := (*syntax.Error)(nil); errors.As(err, &se) {
				detail = fmt.Sprintf("%s: `%s`", se.Code, se.Expr)
			}
			return nil, apperr.Invalid("search.expression", "The regular expression is not valid.",
				apperr.FieldError{Location: "query.q", Message: detail})
		}
	}
	if path != "" {
		if path, err = cleanRepoPath(path); err != nil {
			return nil, err
		}
	}
	if ref == "" {
		ref = r.DefaultBranch
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return nil, err
	}
	searchSlotsOnce.Do(func() { searchSlots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)/2)) })
	select {
	case searchSlots <- struct{}{}:
		defer func() { <-searchSlots }()
	default:
		return nil, apperr.TooManyRequests("search.busy", "Many searches are running. Try again in a moment.")
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	// One line more than listed per file tells whether a file has more.
	matches, complete, err := repo.Grep(ctx, commit, query, git.GrepOptions{Regex: regex, CaseSensitive: caseSensitive, Path: path,
		MaxMatches: searchMaxMatches, MaxPerFile: searchMaxPerFile + 1})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, apperr.Invalid("search.slow", "The search took too long: search for something more specific or below a path.")
	}
	if err != nil {
		if git.ExitCode(err) == 128 { // e.g. an expression that needs too many steps
			return nil, apperr.Invalid("search.unsearchable", "Git cannot search with this expression.")
		}
		return nil, err
	}
	out := &SearchResult{Ref: ref, Commit: commit, Files: []SearchFile{}, Complete: complete}
	index := map[string]int{}
	for _, m := range matches {
		i, ok := index[m.Path]
		if !ok {
			i = len(out.Files)
			index[m.Path] = i
			out.Files = append(out.Files, SearchFile{Path: m.Path, Lines: []SearchLine{}})
		}
		f := &out.Files[i]
		if len(f.Lines) == searchMaxPerFile {
			f.More = true
			continue
		}
		f.Lines = append(f.Lines, SearchLine{Line: m.Line, Text: m.Text})
		out.Matches++
	}
	return out, nil
}
