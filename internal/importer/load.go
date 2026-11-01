// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/wiki"
)

// Report summarises a load. The analysis before it and the validation after it have
// reports of their own (Analysis, Validation).
type Report struct {
	Source       string              `json:"source"`
	Started      time.Time           `json:"started"`
	Finished     time.Time           `json:"finished"`
	Counts       map[string]int      `json:"counts"`
	UsersMatched []string            `json:"usersMatched"`
	Placeholders []string            `json:"placeholderUsers"`
	Warnings     []string            `json:"warnings"`
	Unsupported  map[string]int      `json:"unsupported" doc:"Fields and macros that could not be converted, with counts"`
	Failures     []Failure           `json:"failures" doc:"Entities that could not be loaded; the load went on with the others"`
	Timing       map[string]Timing   `json:"timing,omitempty" doc:"Measured duration of the phases of the load"`
	Projects     []ProjectSummary    `json:"projects,omitempty"`
	Spaces       []SpaceSummary      `json:"spaces,omitempty"`
	Repositories []RepositorySummary `json:"repositories,omitempty"`
	Plans        []PlanSummary       `json:"plans,omitempty"`
}

// groupRenamed notes a source group that went into a group of another name, because Kogge has a group of its own
// with the source's name.
func (r *Report) groupRenamed(source, kogge string) {
	if !strings.EqualFold(source, kogge) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("Kogge has a group %q of its own; the members of the migrated group "+
			"are in %q. Merge them by hand if they belong together.", source, kogge))
	}
}

// ProjectSummary counts the migrated content of a project.
type ProjectSummary struct {
	Key         string `json:"key"`
	Issues      int    `json:"issues"`
	Comments    int    `json:"comments"`
	Attachments int    `json:"attachments"`
	Worklogs    int    `json:"worklogs"`
	Sprints     int    `json:"sprints"`
}

// RepositorySummary counts the migrated content of a repository.
type RepositorySummary struct {
	Project      string `json:"project"`
	Slug         string `json:"slug"`
	History      bool   `json:"history" doc:"The repository has the history of the source"`
	LFSObjects   int    `json:"lfsObjects"`
	PullRequests int    `json:"pullRequests"`
	Comments     int    `json:"comments"`
}

// SpaceSummary counts the migrated content of a space.
type SpaceSummary struct {
	Key         string `json:"key"`
	Pages       int    `json:"pages"`
	BlogPosts   int    `json:"blogPosts"`
	Versions    int    `json:"versions"`
	Comments    int    `json:"comments"`
	Attachments int    `json:"attachments"`
}

func newReport(source string) *Report {
	return &Report{Source: source, Started: time.Now(), Counts: map[string]int{}, Unsupported: map[string]int{}, UsersMatched: []string{},
		Placeholders: []string{}, Warnings: []string{}, Failures: []Failure{}}
}

func (r *Report) warn(format string, args ...any) {
	if len(r.Warnings) < 5000 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
	}
}

// Write stores the report as JSON in the work directory.
func (r *Report) Write(w Workdir, name string) error {
	r.Finished = time.Now()
	// Entities tried again after a rolled-back one may repeat a warning.
	seen := map[string]bool{}
	warnings := r.Warnings[:0]
	for _, msg := range r.Warnings {
		if !seen[msg] {
			seen[msg] = true
			warnings = append(warnings, msg)
		}
	}
	r.Warnings = warnings
	slices.Sort(r.UsersMatched)
	r.UsersMatched = slices.Compact(r.UsersMatched)
	slices.Sort(r.Placeholders)
	r.Placeholders = slices.Compact(r.Placeholders)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return w.Write(b, name)
}

// Loader holds what a load needs from the application.
type Loader struct {
	Pool   *pgxpool.Pool
	Issues *issues.Service
	Wiki   *wiki.Service
	Code   *code.Service
	Builds *builds.Service
	Log    *slog.Logger
	// StopOnError ends the load at the first entity that cannot be loaded. By default such entities are
	// recorded in the report's failures and the load goes on with the others.
	StopOnError bool
}

// failures returns the failure collection of a load.
func (l Loader) failures(rep *Report, jr *journal) *failures {
	return &failures{rep: rep, stop: l.StopOnError, log: l.Log, jr: jr}
}

// users maps source users to Kogge users, creating placeholders.
type users struct {
	byName map[string]int64
	labels map[int64]string
	report *Report
	jr     *journal
}

func newUsers(rep *Report, jr *journal) *users {
	return &users{byName: map[string]int64{}, labels: map[int64]string{}, report: rep, jr: jr}
}

// get returns the Kogge user of a source user, matching or creating one on first sight.
func (u *users) get(ctx context.Context, q db.Querier, ju *jUser) (int64, error) {
	if ju == nil || (ju.Name == "" && ju.Key == "") {
		return 0, nil
	}
	if id := u.lookup(ju.Name, ju.Key); id != 0 {
		return id, nil
	}
	name := ju.Name
	if name == "" {
		name = ju.Key
	}
	id, matched, err := identity.ImportUser(ctx, q, name, ju.DisplayName, ju.EmailAddress)
	if err != nil {
		return 0, err
	}
	// A placeholder created in a transaction that is rolled back does not exist: the cache forgets it then.
	for _, ref := range []string{ju.Name, ju.Key} {
		if k := strings.ToLower(ref); ref != "" {
			if _, ok := u.byName[k]; !ok {
				u.byName[k] = id
				u.jr.add(func() { delete(u.byName, k) })
			}
		}
	}
	list := &u.report.Placeholders
	if matched {
		list = &u.report.UsersMatched
	}
	n := len(*list)
	*list = append(*list, name)
	u.jr.add(func() { *list = (*list)[:n] })
	return id, nil
}

// lookup returns a user seen before by user name or key, without creating one.
func (u *users) lookup(refs ...string) int64 {
	for _, ref := range refs {
		if id, ok := u.byName[strings.ToLower(ref)]; ok && ref != "" {
			return id
		}
	}
	return 0
}

// label returns the display name of a Kogge user.
func (u *users) label(ctx context.Context, q db.Querier, id int64) string {
	if l, ok := u.labels[id]; ok {
		return l
	}
	l := ""
	if usr, err := identity.GetUser(ctx, q, id); err == nil {
		l = usr.DisplayName
	}
	u.labels[id] = l
	return l
}

// parseTime reads the time formats of the REST APIs.
func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05.000Z0700", time.RFC3339Nano, "2006-01-02T15:04:05-0700", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

func timeOr(s string, def time.Time) time.Time {
	if t := parseTime(s); t != nil {
		return *t
	}
	return def
}
