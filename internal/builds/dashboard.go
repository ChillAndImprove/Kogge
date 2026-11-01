// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
)

// BuildOverview is Helling's start page: what runs and waits now, how the last days went, and how the pipelines of
// each repository stand on its default branch. It covers the repositories the caller may read.
type BuildOverview struct {
	Running       int        `json:"running" doc:"Runs in progress"`
	Queued        int        `json:"queued" doc:"Runs not started yet"`
	PendingJobs   int        `json:"pendingJobs" doc:"Jobs waiting for an agent"`
	OldestPending *time.Time `json:"oldestPending,omitempty" doc:"Since when the longest-waiting job waits"`
	AgentsOnline  int        `json:"agentsOnline" doc:"Enabled agents seen in the last minute"`
	AgentsEnabled int        `json:"agentsEnabled"`
	Week          Week       `json:"week" doc:"The runs of the last seven days"`
	Days          []Day      `json:"days" doc:"Finished runs per day of the last 14 days, oldest first"`
	Pipelines     []Health   `json:"pipelines" doc:"Pipelines on default branches, most recently run first"`
}

// Week sums up runs.
type Week struct {
	Runs          int `json:"runs"`
	Succeeded     int `json:"succeeded"`
	Failed        int `json:"failed" doc:"Failed or broken"`
	MedianSeconds int `json:"medianSeconds" doc:"Median duration of finished runs"`
}

// Day counts the runs that finished on a day.
type Day struct {
	Day       string `json:"day" doc:"YYYY-MM-DD (UTC)"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Other     int    `json:"other" doc:"Cancelled"`
}

// Health is a pipeline of a repository on its default branch: its latest run and the states of the runs before.
type Health struct {
	Repository RunRepo    `json:"repository"`
	Pipeline   string     `json:"pipeline"`
	Name       string     `json:"name"`
	Branch     string     `json:"branch"`
	Latest     RunSummary `json:"latest"`
	History    []RunMark  `json:"history" doc:"Up to ten runs, oldest first, the latest last"`
}

// RunMark is a run in a history.
type RunMark struct {
	Number   int    `json:"number"`
	State    string `json:"state"`
	Seconds  int    `json:"seconds,omitempty" doc:"Duration of a finished run"`
	Finished string `json:"finished,omitempty"`
}

// dashboardRepos is the number of repositories whose pipelines the dashboard shows: the most recently active ones.
const dashboardRepos = 100

// Dashboard reads the state of the builds for the principal.
func (s *Service) Dashboard(ctx context.Context, p *identity.Principal) (*BuildOverview, error) {
	all, projects, repos, err := s.repos.Readable(ctx, p)
	if err != nil {
		return nil, err
	}
	var args db.Args
	readable := "TRUE"
	if !all {
		readable = "(r.project_id = ANY(" + args.Add(projects) + ") OR r.repository_id = ANY(" + args.Add(repos) + "))"
	}
	d := &BuildOverview{Days: []Day{}, Pipelines: []Health{}}
	var median *float64
	err = s.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE r.state = 'running'),
			count(*) FILTER (WHERE r.state = 'queued'),
			count(*) FILTER (WHERE r.created_at > now() - interval '7 days'),
			count(*) FILTER (WHERE r.created_at > now() - interval '7 days' AND r.state = 'succeeded'),
			count(*) FILTER (WHERE r.created_at > now() - interval '7 days' AND r.state IN ('failed', 'error')),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM r.finished_at - r.started_at))
				FILTER (WHERE r.created_at > now() - interval '7 days' AND r.finished_at IS NOT NULL AND r.started_at IS NOT NULL)
		FROM runs r WHERE `+readable+` AND (r.state IN ('queued', 'running') OR r.created_at > now() - interval '7 days')`, args...).
		Scan(&d.Running, &d.Queued, &d.Week.Runs, &d.Week.Succeeded, &d.Week.Failed, &median)
	if err != nil {
		return nil, err
	}
	if median != nil {
		d.Week.MedianSeconds = int(math.Round(*median))
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*), min(j.queued_at) FROM run_jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.state = 'pending' AND `+readable, args...).Scan(&d.PendingJobs, &d.OldestPending); err != nil {
		return nil, err
	}
	// Agents serve every repository; how many there are is no secret of any.
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE enabled AND last_seen_at > now() - interval '1 minute'),
		count(*) FILTER (WHERE enabled) FROM agents`).Scan(&d.AgentsOnline, &d.AgentsEnabled); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT to_char(date_trunc('day', r.finished_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
			count(*) FILTER (WHERE r.state = 'succeeded'), count(*) FILTER (WHERE r.state IN ('failed', 'error')),
			count(*) FILTER (WHERE r.state = 'cancelled')
		FROM runs r WHERE `+readable+` AND r.finished_at > date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '13 days'
		GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	counted, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Day, error) {
		var day Day
		err := row.Scan(&day.Day, &day.Succeeded, &day.Failed, &day.Other)
		return day, err
	})
	if err != nil {
		return nil, err
	}
	byDay := map[string]Day{}
	for _, c := range counted {
		byDay[c.Day] = c
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i := 13; i >= 0; i-- {
		key := today.AddDate(0, 0, -i).Format("2006-01-02")
		day := byDay[key]
		day.Day = key
		d.Days = append(d.Days, day)
	}
	if d.Pipelines, err = s.health(ctx, readable, args); err != nil {
		return nil, err
	}
	return d, nil
}

// health reads the latest runs of the pipelines of the most recently active repositories on their default
// branches.
func (s *Service) health(ctx context.Context, readable string, args db.Args) ([]Health, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.repository_id FROM runs r WHERE `+readable+`
		GROUP BY r.repository_id ORDER BY max(r.created_at) DESC LIMIT `+strconv.Itoa(dashboardRepos), args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	repoIDs := []int64{}
	branches := []string{}
	info := map[int64]*Repo{}
	for _, id := range ids {
		repo, err := s.repos.Repo(ctx, id)
		if err != nil || repo.DefaultBranch == "" {
			continue // deleted meanwhile, or empty
		}
		info[id] = repo
		repoIDs = append(repoIDs, id)
		branches = append(branches, repo.DefaultBranch)
	}
	if len(repoIDs) == 0 {
		return []Health{}, nil
	}
	rows, err = s.pool.Query(ctx, `SELECT `+runColumns+` FROM (
			SELECT r.*, row_number() OVER (PARTITION BY r.repository_id, r.pipeline ORDER BY r.created_at DESC, r.id DESC) AS n
			FROM runs r JOIN unnest($1::bigint[], $2::text[]) AS d(repo, branch) ON d.repo = r.repository_id AND r.branch = d.branch
			WHERE r.event <> 'pull-request'
		) runs WHERE n <= 10 ORDER BY repository_id, pipeline, n`, repoIDs, branches)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*runRow, error) { return scanRun(r) })
	if err != nil {
		return nil, err
	}
	// The latest runs come first in each group; their summaries carry the people.
	var latest []*runRow
	for i, r := range list {
		if i == 0 || r.RepoID != list[i-1].RepoID || r.Pipeline != list[i-1].Pipeline {
			latest = append(latest, r)
		}
	}
	summaries, err := s.summaries(ctx, latest)
	if err != nil {
		return nil, err
	}
	out := make([]Health, 0, len(latest))
	k := -1
	for i, r := range list {
		if i == 0 || r.RepoID != list[i-1].RepoID || r.Pipeline != list[i-1].Pipeline {
			k++
			repo := info[r.RepoID]
			out = append(out, Health{Repository: RunRepo{ProjectKey: repo.ProjectKey, Slug: repo.Slug, Name: repo.Name},
				Pipeline: r.Pipeline, Name: r.Name, Branch: repo.DefaultBranch, Latest: summaries[k], History: []RunMark{}})
		}
		mark := RunMark{Number: r.Number, State: r.State}
		if r.Started != nil && r.Finished != nil {
			mark.Seconds = int(r.Finished.Sub(*r.Started).Round(time.Second).Seconds())
			mark.Finished = r.Finished.UTC().Format(time.RFC3339)
		}
		h := &out[k]
		h.History = append([]RunMark{mark}, h.History...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Latest.Created.After(out[j].Latest.Created) })
	return out, nil
}
