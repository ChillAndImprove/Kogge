// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Suggested changes: a comment on a line of the new side of a pull request's changes may carry a code block of
// the language "suggestion" — the lines that should replace the commented one (none: remove it). Whoever may
// write to the source branch applies it with a commit that names the reviewer as co-author, as long as the line
// is as it was when the comment was made.

// Suggestion is the change a line comment suggests.
type Suggestion struct {
	Lines   []string           `json:"lines" doc:"The lines that replace the commented line (none: remove it)"`
	Applied *AppliedSuggestion `json:"applied,omitempty"`
}

// AppliedSuggestion says when and by whom a suggestion was committed.
type AppliedSuggestion struct {
	Commit string            `json:"commit"`
	By     *identity.UserRef `json:"by,omitempty"`
	At     time.Time         `json:"at"`
}

// suggestionOf returns the lines of the first code block of the language "suggestion" in a comment.
func suggestionOf(body *docmodel.Doc) ([]string, bool) {
	if body == nil {
		return nil, false
	}
	for _, n := range body.Content {
		if n.Type != "codeBlock" || n.Attrs["language"] != "suggestion" {
			continue
		}
		var b strings.Builder
		for _, c := range n.Content {
			b.WriteString(c.Text)
		}
		if b.Len() == 0 {
			return []string{}, true
		}
		return strings.Split(b.String(), "\n"), true
	}
	return nil, false
}

// ApplySuggestion commits the suggestion of a line comment to the source branch of its open pull request.
func (s *Service) ApplySuggestion(ctx context.Context, p *identity.Principal, commentID int64) (*CommitInfo, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	c, r, pr, err := s.commentForWrite(ctx, a, commentID)
	if err != nil {
		return nil, err
	}
	if pr.State != StateOpen {
		return nil, prClosed(pr.State)
	}
	lines, ok := suggestionOf(c.Body)
	if !ok || c.ParentID != 0 || c.Line == 0 || c.Side != "new" {
		return nil, apperr.Invalid("suggestion.none", "The comment suggests no change of a line.")
	}
	if c.Suggestion != nil && c.Suggestion.Applied != nil {
		return nil, apperr.Conflict("suggestion.applied", "The suggestion was applied already.")
	}
	outdated := apperr.Conflict("suggestion.outdated", "The line changed since the suggestion was made; suggest again.")
	if c.Outdated {
		return nil, outdated
	}
	repo := s.open(r.ID)
	head, err := repo.ResolveBranch(ctx, pr.Source)
	if err != nil {
		return nil, apperr.Conflict("pull-request.source-gone", "The source branch %s does not exist any more.", pr.Source)
	}
	info, err := repo.Stat(ctx, head, c.Path)
	if errors.Is(err, git.ErrNotFound) {
		return nil, outdated
	}
	if err != nil {
		return nil, err
	}
	if info.Size > maxEditSize {
		return nil, apperr.Invalid("file.content", "Files changed in the browser are UTF-8 text of at most 2 MB.")
	}
	data, err := repo.ReadBlob(ctx, info.SHA, maxEditSize+1)
	if err != nil {
		return nil, err
	}
	content, err := replaceLine(string(data), c.Line, c.LineText, lines)
	if err != nil {
		return nil, outdated
	}
	message := "Apply suggestion to " + c.Path
	if c.Author != nil && c.Author.ID != p.UserID() {
		author, err := identity.GetUser(ctx, s.pool, c.Author.ID)
		if err == nil && author.Email != "" {
			message += fmt.Sprintf("\n\nSuggested by %s.\n\nCo-authored-by: %s <%s>", author.DisplayName, author.DisplayName, author.Email)
		}
	}
	commit, err := s.CommitFile(ctx, p, r.ProjectKey, r.Slug, FileChange{Branch: pr.Source, Path: c.Path, Content: content,
		Base: head, Message: message})
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE pull_request_comments SET applied_sha = $2, applied_by = $3, applied_at = now()
		WHERE id = $1 AND applied_sha IS NULL`, c.ID, commit.SHA, p.UserID()); err != nil {
		return nil, err
	}
	return commit, nil
}

// replaceLine replaces line number (from 1) of a text, which must still read old, with lines; the line endings
// of the text stay.
func replaceLine(text string, number int, old string, lines []string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("not UTF-8 text")
	}
	parts := strings.SplitAfter(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if number < 1 || number > len(parts) {
		return "", errors.New("no such line")
	}
	line := parts[number-1]
	ending := line[len(strings.TrimRight(line, "\r\n")):]
	if strings.TrimRight(line, "\r\n") != strings.TrimRight(old, "\r\n") {
		return "", errors.New("the line changed")
	}
	newline := ending
	if newline == "" { // the last line has none: the lines put in use the text's own
		newline = "\n"
		if strings.Contains(text, "\r\n") {
			newline = "\r\n"
		}
	}
	var b strings.Builder
	for _, p := range parts[:number-1] {
		b.WriteString(p)
	}
	for i, l := range lines {
		b.WriteString(l)
		if i < len(lines)-1 {
			b.WriteString(newline)
		} else {
			b.WriteString(ending)
		}
	}
	for _, p := range parts[number:] {
		b.WriteString(p)
	}
	return b.String(), nil
}

// commentForWrite loads a comment of a pull request whose repository the principal may write to.
func (s *Service) commentForWrite(ctx context.Context, a *Access, id int64) (*PRComment, *repoRow, *prRow, error) {
	notFound := apperr.NotFound("comment.not-found", "The comment does not exist.")
	var repoID, prID int64
	var number int
	err := s.pool.QueryRow(ctx, `SELECT pr.repository_id, pr.id, pr.number FROM pull_request_comments c
		JOIN pull_requests pr ON pr.id = c.pull_request_id WHERE c.id = $1`, id).Scan(&repoID, &prID, &number)
	if db.IsNoRows(err) {
		return nil, nil, nil, notFound
	}
	if err != nil {
		return nil, nil, nil, err
	}
	r, err := repoByID(ctx, s.pool, repoID)
	if err != nil {
		return nil, nil, nil, err
	}
	switch level := a.Level(r.ProjectID, r.ID); {
	case level < LevelRead:
		return nil, nil, nil, notFound
	case level < LevelWrite:
		return nil, nil, nil, apperr.Forbidden("suggestion.denied", "Only who can write to the repository applies suggestions.")
	}
	pr, err := s.loadPR(ctx, s.pool, r.ID, number, false)
	if err != nil {
		return nil, nil, nil, err
	}
	list, err := s.prComments(ctx, s.pool, a, r, prID, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(list) == 0 {
		return nil, nil, nil, notFound
	}
	return &list[0], r, pr, nil
}
