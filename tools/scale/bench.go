// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// operation is measured against a target for its 95th percentile; operations without a target are
// measured for information.
type operation struct {
	key    string // for -only
	name   string
	target time.Duration
	path   func(r *rand.Rand) string // a GET request as the reader
	// run measures one execution itself, for operations that are more than a request; setup prepares them once.
	run    func(r *rand.Rand) (time.Duration, error)
	setup  func() error
	n      int  // executions instead of -requests
	serial bool // one at a time instead of -concurrency
}

type result struct {
	p50, p95, p99 time.Duration
	errors        int
	sample        string
}

// bench holds what the operations share.
type bench struct {
	base   string
	m      Manifest
	client *http.Client
	git    *gitClient
	agent  string // kogge-agent binary
	work   string // a directory for clones and the agent
	stops  []func()
}

func benchCommand(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	base := fs.String("url", "http://localhost:8080", "base URL of the installation")
	manifestFile := fs.String("manifest", "scale.json", "manifest written by generate")
	requests := fs.Int("requests", 200, "measured requests per operation")
	concurrency := fs.Int("concurrency", 4, "requests in parallel")
	strict := fs.Bool("strict", false, "fail when an operation misses its target")
	only := fs.String("only", "", "measure only these operations (comma-separated keys: "+strings.Join(operationKeys, ",")+")")
	pushes := fs.Int("pushes", 20, "measured pushes of 100 commits")
	jobs := fs.Int("jobs", 40, "measured hand-overs of jobs to the agent")
	clones := fs.Int("clones", 40, "measured clones and fetches")
	agent := fs.String("agent", "bin/kogge-agent", "the build agent for the hand-over of jobs (built by make build-server)")
	gitBinary := fs.String("git", "git", "the Git client for pushes, clones and fetches")
	_ = fs.Parse(args)
	raw, err := os.ReadFile(*manifestFile)
	if err != nil {
		return err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if len(m.Projects) < 2 || len(m.Pages) == 0 || m.Repository == "" {
		return errors.New("the manifest is incomplete; generate the data first")
	}
	work, err := os.MkdirTemp("", "kogge-scale-bench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	b := &bench{base: strings.TrimRight(*base, "/"), m: m, agent: *agent, work: work,
		client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: max(*concurrency, 4)}}}
	b.git = &gitClient{binary: *gitBinary, base: b.base}
	ops := b.operations(*pushes, *jobs, *clones)
	if *only != "" {
		keep := strings.Split(*only, ",")
		ops = slices.DeleteFunc(ops, func(op operation) bool { return !slices.Contains(keep, op.key) })
		if len(ops) == 0 {
			return fmt.Errorf("-only names no operation; the keys are %s", strings.Join(operationKeys, ", "))
		}
	}
	defer func() {
		for _, stop := range b.stops {
			stop()
		}
	}()

	size := m.Size
	if m.Fraction > 0 && m.Fraction < 1 {
		size += fmt.Sprintf(" × %g", m.Fraction)
	}
	fmt.Printf("Size %s (%d projects, %d issues per project), %d requests per operation, %d in parallel, against %s\n", size,
		len(m.Projects), m.Issues, *requests, *concurrency, b.base)
	// Other work on the machine shows in every number; the load says how much there was.
	load := loadAverage()
	defer func() {
		if load != "" {
			fmt.Printf("\nLoad average of the machine: %s at the start, %s at the end\n", load, loadAverage())
		}
	}()
	fmt.Println()
	fmt.Printf("%-44s %9s %9s %9s %9s  %s\n", "Operation", "p50", "p95", "p99", "target", "")
	missed := 0
	for i, op := range ops {
		if op.setup != nil {
			if err := op.setup(); err != nil {
				missed++
				fmt.Printf("%-44s %s\n", op.name, "NOT MEASURED: "+err.Error())
				continue
			}
		}
		n, parallel := *requests, *concurrency
		if op.n > 0 {
			n = op.n
		}
		if op.serial {
			parallel = 1
		}
		do := op.run
		if op.path != nil {
			do = func(r *rand.Rand) (time.Duration, error) { return b.get(op.path(r)) }
		}
		res := measure(do, n, parallel, uint64(i+1))
		mark, target := "ok", ms(op.target)
		if op.target == 0 {
			mark, target = "", "-"
		}
		if (op.target > 0 && res.p95 > op.target) || res.errors > 0 {
			mark, missed = "MISSED", missed+1
		}
		fmt.Printf("%-44s %9s %9s %9s %9s  %s\n", op.name, ms(res.p50), ms(res.p95), ms(res.p99), target, mark)
		if res.errors > 0 {
			fmt.Printf("    %d errors, e.g. %s\n", res.errors, res.sample)
		}
	}
	if missed > 0 && *strict {
		return fmt.Errorf("%d operations missed their targets", missed)
	}
	return nil
}

var operationKeys = []string{"issue", "kql", "board", "page", "quick-issues", "quick-pages", "file", "diff", "push", "handover",
	"clone", "fetch", "fetch-new"}

// operations lists what bench measures: the operations with performance targets, and Git clones and fetches over HTTP.
func (b *bench) operations(pushes, jobs, clones int) []operation {
	m := b.m
	repo := "/api/v1/code/repos/" + m.Repository
	labels := words[:30] // the generator's labels
	p := &pusher{b: b}
	h := &handover{b: b}
	f := &fetcher{b: b}
	b.stops = append(b.stops, h.stop)
	return []operation{
		{key: "issue", name: "Issue view", target: 150 * time.Millisecond, path: func(r *rand.Rand) string {
			return "/api/v1/issues/" + m.Projects[1+r.IntN(len(m.Projects)-1)] + "-" + strconv.Itoa(1+r.IntN(m.Issues))
		}},
		{key: "kql", name: "KQL search, first page", target: 300 * time.Millisecond, path: func(r *rand.Rand) string {
			q := fmt.Sprintf(`project = %s AND status != Done ORDER BY updated DESC`, m.Projects[1+r.IntN(len(m.Projects)-1)])
			if r.IntN(2) == 0 {
				q = fmt.Sprintf(`labels = "%s" AND assignee is not EMPTY ORDER BY created DESC`, labels[r.IntN(len(labels))])
			}
			return "/api/v1/search?limit=50&q=" + url.QueryEscape(q)
		}},
		{key: "board", name: "Board with 300 cards", target: 500 * time.Millisecond, path: func(*rand.Rand) string {
			return "/api/v1/boards/" + strconv.FormatInt(m.BoardID, 10) + "/view"
		}},
		{key: "page", name: "Page view", target: 150 * time.Millisecond, path: func(r *rand.Rand) string {
			return "/api/v1/wiki/pages/" + strconv.FormatInt(m.Pages[r.IntN(len(m.Pages))], 10)
		}},
		{key: "quick-issues", name: "Quick search: issues", target: 200 * time.Millisecond, path: func(r *rand.Rand) string {
			return "/api/v1/search?limit=10&q=" + url.QueryEscape(`text ~ "`+m.Words[r.IntN(len(m.Words))]+`" ORDER BY updated DESC`)
		}},
		{key: "quick-pages", name: "Quick search: pages", target: 200 * time.Millisecond, path: func(r *rand.Rand) string {
			return "/api/v1/wiki/search?limit=10&q=" + url.QueryEscape(m.Words[r.IntN(len(m.Words))])
		}},
		// The quick search of all products (Ctrl+K) as people type: beginnings of words (from two letters on), whole
		// words, a word and the beginning of the next, issue keys.
		{key: "quick-all", name: "Quick search: all products", target: 200 * time.Millisecond, path: func(r *rand.Rand) string {
			w := m.Words[r.IntN(len(m.Words))]
			var q string
			switch r.IntN(4) {
			case 0:
				q = w[:min(len(w), 2+r.IntN(4))]
			case 1:
				q = w
			case 2:
				v := m.Words[r.IntN(len(m.Words))]
				q = w + " " + v[:min(len(v), 2+r.IntN(3))]
			default:
				q = m.Projects[1+r.IntN(len(m.Projects)-1)] + "-" + strconv.Itoa(1+r.IntN(m.Issues))
			}
			return "/api/v1/quick-search?q=" + url.QueryEscape(q)
		}},
		// The search page and the activity stream have no targets of their own; they are held to the KQL
		// search's.
		{key: "search-all", name: "Search page, all products", target: 300 * time.Millisecond, path: func(r *rand.Rand) string {
			return "/api/v1/search-all?limit=5&q=" + url.QueryEscape(m.Words[r.IntN(len(m.Words))])
		}},
		{key: "activity", name: "Activity stream, first page", target: 300 * time.Millisecond, path: func(r *rand.Rand) string {
			if r.IntN(2) == 0 {
				return "/api/v1/activity-stream?limit=30&project=" + m.Projects[1+r.IntN(len(m.Projects)-1)]
			}
			return "/api/v1/activity-stream?limit=30"
		}},
		{key: "file", name: "File view", target: 200 * time.Millisecond, path: func(r *rand.Rand) string {
			return repo + "/file?ref=main&path=" + url.QueryEscape(m.Files[r.IntN(len(m.Files))])
		}},
		{key: "diff", name: "Pull request diff, 2,000 lines", target: 800 * time.Millisecond, path: func(*rand.Rand) string {
			return repo + "/pull-requests/" + strconv.Itoa(m.PullRequst) + "/diff"
		}},
		{key: "push", name: "Push of 100 commits to visibility in issues", target: 5 * time.Second, setup: p.prepare, run: p.visible,
			n: pushes, serial: true},
		{key: "handover", name: "Job handed to an idle agent", target: 2 * time.Second, setup: h.prepare, run: h.once, n: jobs, serial: true},
		{key: "clone", name: "Git clone over HTTP (200 files)", setup: f.prepare, run: f.clone, n: clones},
		{key: "fetch", name: "Git fetch over HTTP, up to date", setup: f.prepare, run: f.fetch, n: clones},
		{key: "fetch-new", name: "Git fetch over HTTP, 100 new commits", setup: p.prepareFollower, run: p.fetchNew, n: pushes, serial: true},
	}
}

// loadAverage is the load average of the last minute (Linux), or empty.
func loadAverage() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	return strings.Fields(string(data) + " ")[0]
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d.Microseconds())/1000, 'f', 1, 64) + " ms"
}

// get sends a GET request as the reader.
func (b *bench) get(path string) (time.Duration, error) {
	start := time.Now()
	err := b.call(http.MethodGet, path, b.m.Token, nil, nil)
	return time.Since(start), err
}

// call sends a request and decodes a JSON answer into out (if not nil).
func (b *bench) call(method, path, token string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(data))
	}
	req, err := http.NewRequest(method, b.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("%s %s: HTTP %d %s", method, path, res.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// measure warms the operation up, then runs it n times and computes the percentiles.
func measure(do func(r *rand.Rand) (time.Duration, error), n, parallel int, seed uint64) result {
	r := rand.New(rand.NewPCG(seed, 99))
	warm := min(10, max(1, n/10))
	for range warm {
		_, _ = do(r)
	}
	var mu sync.Mutex
	var took []time.Duration
	var res result
	work := make(chan *rand.Rand)
	var wg sync.WaitGroup
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for wr := range work {
				d, err := do(wr)
				mu.Lock()
				if err != nil {
					res.errors++
					res.sample = err.Error()
				} else {
					took = append(took, d)
				}
				mu.Unlock()
			}
		}()
	}
	for i := range n {
		// Each execution draws from its own generator, so that parallel workers do not share one.
		work <- rand.New(rand.NewPCG(seed, uint64(i)+r.Uint64()))
	}
	close(work)
	wg.Wait()
	slices.Sort(took)
	at := func(q float64) time.Duration {
		if len(took) == 0 {
			return 0
		}
		return took[min(len(took)-1, int(q*float64(len(took))))]
	}
	res.p50, res.p95, res.p99 = at(0.50), at(0.95), at(0.99)
	return res
}
