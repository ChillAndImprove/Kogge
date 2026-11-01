// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package feed holds the entries of activity streams: what happened recently in the products. Each
// product reads its own sources with its own permissions — issues and pages from the activity table, pushes and
// pull requests, runs and deployments from theirs — and package links merges them, newest first.
//
// Entries are ordered by time, then by source, then by id; a Cursor is a place in that order, so that a stream is
// read on from its last entry no matter how many entries arrived meanwhile.
package feed

import (
	"cmp"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// Sources of entries.
const (
	SourceActivity    = 1 // issues and pages (the activity table)
	SourcePush        = 2
	SourcePullRequest = 3 // events of pull requests
	SourceComment     = 4 // comments on pull requests
	SourceRun         = 5
	SourceDeployment  = 6
)

// Products of entries.
const (
	ProductKontor  = "kontor"
	ProductLogbuch = "logbuch"
	ProductHafen   = "hafen"
	ProductHelling = "helling"
)

// Entry is something that happened.
type Entry struct {
	Key       string            `json:"key" doc:"The entry's place in the stream: pass it as before to read the entries after it"`
	Product   string            `json:"product" enum:"kontor,logbuch,hafen,helling"`
	Verb      string            `json:"verb" doc:"What happened, e.g. issue.created, page.updated, pushed, pull-request.merged, run.failed, deployment.succeeded"`
	At        time.Time         `json:"at"`
	Actor     *identity.UserRef `json:"actor,omitempty"`
	Title     string            `json:"title" doc:"What it happened to: an issue (key and summary), a page, a pull request or run (number and title)"`
	URL       string            `json:"url"`
	Container Container         `json:"container"`
	Refs      []RefChange       `json:"refs,omitempty" doc:"Pushes: the branches and tags"`
	Detail    string            `json:"detail,omitempty" doc:"Runs: the branch or tag; deployments: the environment"`
	// What a change was. The products record it when it happens; entries recorded before they did lack it.
	Status   *StatusChange  `json:"status,omitempty" doc:"Status changes of issues: the status before and after"`
	Fields   []ChangedField `json:"fields,omitempty" doc:"Changes of issues: the fields changed"`
	Excerpt  string         `json:"excerpt,omitempty" doc:"Comments: the beginning of their text as it is now (none once deleted); new versions of pages: the author's note on what changed"`
	Commits  *PushCommits   `json:"commits,omitempty" doc:"Pushes: the commits they brought"`
	FromKey  string         `json:"fromKey,omitempty" doc:"Issues moved to another project: the issue's key before the move"`
	Pipeline string         `json:"pipeline,omitempty" doc:"Runs and deployments: the name of the pipeline"`
	Number   int            `json:"number,omitempty" doc:"Runs and deployments: the number of the run"`
	// Source and ID place the entry in the stream.
	Source int   `json:"-"`
	ID     int64 `json:"-"`
}

// StatusChange is the change of an issue's status.
type StatusChange struct {
	From IssueStatus `json:"from"`
	To   IssueStatus `json:"to"`
}

// IssueStatus is a status as it was named when the issue changed, with its category (for its colour).
type IssueStatus struct {
	Name     string `json:"name"`
	Category string `json:"category" enum:"todo,inprogress,done"`
}

// ChangedField is a field of an issue that changed.
type ChangedField struct {
	Key  string `json:"key" doc:"The field's key: summary, priority, assignee … or cf_<id> for custom fields"`
	Name string `json:"name,omitempty" doc:"Custom fields: the field's name"`
}

// PushCommits are the commits a push brought.
type PushCommits struct {
	Count  int          `json:"count" doc:"How many commits the push added to the repository"`
	Newest []PushCommit `json:"newest" doc:"The newest of them (up to three), newest first"`
}

// PushCommit is a commit of a push.
type PushCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject" doc:"The first line of the message"`
	URL     string `json:"url"`
}

// ExcerptLength is how many characters the excerpt of a comment has in a stream.
const ExcerptLength = 160

// FirstBlocks returns the SQL expression of the first blocks of a document column: what a stream reads of a
// comment for its excerpt (ExcerptOf), rather than all of it.
func FirstBlocks(column string) string {
	return "jsonb_path_query_array(" + column + ", '$.content[0 to 7]')"
}

// ExcerptOf returns the excerpt of the first blocks of a document (JSON, as FirstBlocks reads them).
func ExcerptOf(blocks []byte) string {
	if len(blocks) == 0 {
		return ""
	}
	d := &docmodel.Doc{Type: "doc"}
	if json.Unmarshal(blocks, &d.Content) != nil {
		return ""
	}
	return docmodel.Excerpt(d, ExcerptLength)
}

// Container is where something happened: a project, a space or a repository.
type Container struct {
	Kind string `json:"kind" enum:"project,space,repository"`
	Key  string `json:"key" doc:"Project or space key, or the repository's path (KEY/slug)"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// RefChange is a branch or tag of a push.
type RefChange struct {
	Name   string `json:"name"`
	Tag    bool   `json:"tag,omitempty"`
	Change string `json:"change" enum:"created,updated,deleted"`
}

// Cursor is a place in a stream.
type Cursor struct {
	At     time.Time
	Source int
	ID     int64
}

// CursorOf returns the place of an entry.
func CursorOf(e Entry) Cursor { return Cursor{At: e.At, Source: e.Source, ID: e.ID} }

// String encodes the cursor for the API: microseconds since 1970, source and id.
func (c Cursor) String() string {
	return strconv.FormatInt(c.At.UnixMicro(), 10) + "-" + strconv.Itoa(c.Source) + "-" + strconv.FormatInt(c.ID, 10)
}

// ErrCursor is returned for cursors that no stream wrote.
var ErrCursor = errors.New("feed: not a cursor")

// ParseCursor decodes a cursor written by String.
func ParseCursor(s string) (Cursor, error) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return Cursor{}, ErrCursor
	}
	micro, err1 := strconv.ParseInt(parts[0], 10, 64)
	source, err2 := strconv.Atoi(parts[1])
	id, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || source < SourceActivity || source > SourceDeployment || id < 0 || micro < 0 {
		return Cursor{}, ErrCursor
	}
	return Cursor{At: time.UnixMicro(micro).UTC(), Source: source, ID: id}, nil
}

// After returns an SQL condition for the rows of a source that come after the cursor in the stream (older ones):
// at and id name the columns of the row's time and id. Without a cursor it is TRUE.
func (c *Cursor) After(source int, at, id string, args *db.Args) string {
	if c == nil {
		return "TRUE"
	}
	t := args.Add(c.At)
	switch {
	case source < c.Source:
		return at + " <= " + t
	case source > c.Source:
		return at + " < " + t
	}
	return "(" + at + ", " + id + ") < (" + t + ", " + args.Add(c.ID) + ")"
}

// Compare orders entries as streams show them: newest first.
func Compare(a, b Entry) int {
	if c := b.At.Compare(a.At); c != 0 {
		return c
	}
	if c := cmp.Compare(b.Source, a.Source); c != 0 {
		return c
	}
	return cmp.Compare(b.ID, a.ID)
}

// Query selects the entries of a stream.
type Query struct {
	Before *Cursor // nil: from the newest
	Limit  int
	// Mine keeps what the principal watches or is involved in: as reporter, assignee, author, reviewer, the one who
	// pushed, started or deployed.
	Mine bool
	// ActorID keeps what one person did.
	ActorID int64
}

// Merge combines the entries of several sources, each read with a limit of limit+1, into the first limit entries
// of the stream and sets their keys; more tells whether entries follow.
func Merge(lists [][]Entry, limit int) (entries []Entry, more bool) {
	all := []Entry{}
	for _, l := range lists {
		all = append(all, l...)
	}
	slices.SortFunc(all, Compare)
	if more = len(all) > limit; more {
		all = all[:limit]
	}
	for i := range all {
		all[i].Key = CursorOf(all[i]).String()
	}
	return all, more
}
