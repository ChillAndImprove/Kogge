// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package links

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/search"
)

// Search across the products: the quick search (Ctrl+K) and the search page ask every
// product at once, each within a time budget, and merge what they find. Each product searches its own tables with
// the principal's permissions in its SQL, so nothing anybody may not see leaves the server — not even its title.

// Products of the search, in the order they are shown when they match equally well.
const (
	ProductKontor  = "kontor"
	ProductLogbuch = "logbuch"
	ProductHafen   = "hafen"
	ProductHelling = "helling"
	ProductPeople  = "people"
)

var productOrder = []string{ProductKontor, ProductLogbuch, ProductHafen, ProductHelling, ProductPeople}

// The time budgets of a product: the quick search answers with the products that answered in time and names the
// others as incomplete; the search page waits longer. Searches that run late are not cancelled (that would close
// their database connections) but finish in the background, within searchTimeout and in their slots, and are
// thrown away.
var (
	quickBudget   = 150 * time.Millisecond
	pageBudget    = 3 * time.Second
	searchTimeout = 10 * time.Second
)

// searchSlots is how many product searches and activity sources may run at once on the node, across all
// requests: a quarter of the database pool, at least minSearchSlots. Each of them runs its queries one after
// another, so the slots bound the connections that searches hold, and people typing in the quick search leave
// the rest of the pool to everything else.
func searchSlots(pool *pgxpool.Pool) int {
	if pool == nil {
		return minSearchSlots
	}
	return max(minSearchSlots, int(pool.Config().MaxConns)/4)
}

const minSearchSlots = 2

// errNoSlot is the answer of a product that found no free slot within its budget: it is late without having run.
var errNoSlot = errors.New("no free search slot")

// acquire waits for a free slot until the budget has expired or the request has ended, and reports whether it
// got one. A slot that comes free just then is left: nobody waits for the product any more.
func (s *Service) acquire(ctx context.Context, expired <-chan struct{}) bool {
	select {
	case s.slots <- struct{}{}:
	case <-expired:
		return false
	case <-ctx.Done():
		return false
	}
	select {
	case <-expired:
	case <-ctx.Done():
	default:
		return true
	}
	s.release()
	return false
}

// release gives a slot back.
func (s *Service) release() { <-s.slots }

// runInSlots calls fn for 0 … n-1 and returns when all calls have returned. The calls after the first run at once
// in the slots that are free now; the first, and those that find no free slot, run in the calling goroutine one
// after another. So a busy node answers more slowly, never incompletely, and nothing waits for a slot.
func (s *Service) runInSlots(n int, fn func(i int)) {
	var wg sync.WaitGroup
	var here []int
	for i := 1; i < n; i++ {
		select {
		case s.slots <- struct{}{}:
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer s.release()
				fn(i)
			}()
		default:
			here = append(here, i)
		}
	}
	if n > 0 {
		fn(0)
	}
	for _, i := range here {
		fn(i)
	}
	wg.Wait()
}

// finder searches one kind of thing.
type finder struct {
	product string
	kind    string
	quick   int // how many the quick search shows; 0: not in the quick search
	page    bool
	find    func(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error)
}

// finders lists the kinds of things found, in the order of the search page.
func (s *Service) finders() []finder {
	pages := func(kind string) func(context.Context, *identity.Principal, search.Request) (*search.Result, error) {
		return func(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
			return s.wiki.SearchPages(ctx, p, kind, r)
		}
	}
	return []finder{
		{ProductKontor, search.KindIssue, 5, true, s.issues.SearchIssues},
		{ProductKontor, search.KindProject, 3, true, s.issues.SearchProjects},
		// The quick search finds pages and blog posts together; the search page lists them apart.
		{ProductLogbuch, search.KindPage, 5, false, pages("")},
		{ProductLogbuch, search.KindPage, 0, true, pages("page")},
		{ProductLogbuch, search.KindBlog, 0, true, pages("blog")},
		{ProductLogbuch, search.KindSpace, 3, true, s.wiki.SearchSpaces},
		{ProductHafen, search.KindRepository, 4, true, s.code.SearchRepositories},
		{ProductHafen, search.KindPullRequest, 5, true, s.code.SearchPullRequests},
		{ProductHafen, search.KindCodeProject, 3, true, s.code.SearchCodeProjects},
		{ProductHelling, search.KindPipelines, 3, true, s.searchPipelines},
		{ProductPeople, search.KindUser, 5, true, func(ctx context.Context, _ *identity.Principal, r search.Request) (*search.Result, error) {
			return identity.SearchUsers(ctx, s.pool, r)
		}},
	}
}

// pipelinesConsidered is how many of the repositories found the pipelines search looks at.
const pipelinesConsidered = 100

// searchPipelines finds the pipelines of the repositories found (those with runs), with the state of their newest
// run: the pipelines of a repository are its page in Helling.
func (s *Service) searchPipelines(ctx context.Context, p *identity.Principal, r search.Request) (*search.Result, error) {
	out := &search.Result{Hits: []search.Hit{}}
	if s.builds == nil {
		return out, nil
	}
	repos, err := s.code.SearchRepositories(ctx, p, search.Request{Query: r.Query, Text: r.Text, Limit: pipelinesConsidered})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(repos.Hits))
	for _, h := range repos.Hits {
		ids = append(ids, h.ID)
	}
	newest, err := s.builds.NewestRuns(ctx, p, ids)
	if err != nil {
		return nil, err
	}
	var all []search.Hit
	for _, h := range repos.Hits {
		run, ok := newest[h.ID]
		if !ok {
			continue
		}
		key, slug, _ := strings.Cut(h.Key, "/")
		h.Kind, h.URL, h.State = search.KindPipelines, builds.PipelinesPath(key, slug), run.State
		all = append(all, h)
	}
	out.Total = len(all)
	from := min(max(r.Offset, 0), len(all))
	out.Hits = append(out.Hits, all[from:min(from+r.LimitOr(5, 100), len(all))]...)
	return out, nil
}

// mayFindPeople reports whether the principal may browse users, which finding people needs.
func (s *Service) mayFindPeople(ctx context.Context, p *identity.Principal) bool {
	if s.authz == nil || !p.Authenticated() {
		return false
	}
	ok, err := s.authz.HasGlobal(ctx, p, authz.UserBrowse)
	return err == nil && ok
}

// fanOut runs one function per product at once and collects the answers that come within the budget; late are
// the products that did not answer in time or failed (failures are logged).
//
// The products of all requests share the node's slots (searchSlots). A product waits for a free slot within the
// budget and is late without having run if it gets none, as if it had been slow. A product that started holds
// its slot until it ends, also when its request no longer waits for it, so that the slots really bound the
// connections of the searches.
func fanOut[T any](ctx context.Context, s *Service, budget time.Duration, products []string,
	run func(ctx context.Context, product string) (T, error)) (map[string]T, []string) {
	type answer struct {
		product string
		value   T
		err     error
	}
	expired := make(chan struct{})
	timer := time.AfterFunc(budget, func() { close(expired) })
	defer timer.Stop()
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchTimeout)
	answers := make(chan answer, len(products))
	var wg sync.WaitGroup
	for _, pr := range products {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !s.acquire(ctx, expired) {
				answers <- answer{product: pr, err: errNoSlot}
				return
			}
			defer s.release()
			v, err := run(work, pr)
			answers <- answer{pr, v, err}
		}()
	}
	go func() {
		wg.Wait()
		cancel()
	}()
	done := map[string]T{}
	for pending := len(products); pending > 0; pending-- {
		select {
		case a := <-answers:
			if a.err != nil {
				if !errors.Is(a.err, errNoSlot) && !errors.Is(a.err, context.Canceled) &&
					!errors.Is(a.err, context.DeadlineExceeded) {
					s.log.Error("search failed", "product", a.product, "error", a.err)
				}
				continue
			}
			done[a.product] = a.value
		case <-expired:
			pending = 0
		case <-ctx.Done():
			pending = 0
		}
	}
	late := []string{}
	for _, pr := range products {
		if _, ok := done[pr]; !ok {
			late = append(late, pr)
		}
	}
	return done, late
}

// QuickGroup is what the quick search found in one product.
type QuickGroup struct {
	Product string       `json:"product" enum:"kontor,logbuch,hafen,helling,people"`
	Hits    []search.Hit `json:"hits"`
}

// QuickResult is the answer of the quick search.
type QuickResult struct {
	Groups     []QuickGroup `json:"groups" doc:"The products that found something, the one with the best match first"`
	Incomplete []string     `json:"incomplete" doc:"Products that did not answer in time; what they would have found is missing"`
}

// QuickSearch finds what a text names, in every product: issues by key and summary, projects, pages, blog posts,
// spaces, repositories, pull requests, code projects, pipelines and people by key and title.
func (s *Service) QuickSearch(ctx context.Context, p *identity.Principal, text string) (*QuickResult, error) {
	q := search.Parse(text)
	out := &QuickResult{Groups: []QuickGroup{}, Incomplete: []string{}}
	if q.Empty() {
		return out, nil
	}
	byProduct := map[string][]finder{}
	people := s.mayFindPeople(ctx, p)
	for _, f := range s.finders() {
		if f.quick > 0 && (f.product != ProductPeople || people) {
			byProduct[f.product] = append(byProduct[f.product], f)
		}
	}
	products := []string{}
	for _, pr := range productOrder {
		if len(byProduct[pr]) > 0 {
			products = append(products, pr)
		}
	}
	done, late := fanOut(ctx, s, quickBudget, products, func(ctx context.Context, product string) ([]search.Hit, error) {
		hits := []search.Hit{}
		for _, f := range byProduct[product] {
			res, err := f.find(ctx, p, search.Request{Query: q, Limit: f.quick})
			if err != nil {
				return nil, err
			}
			hits = append(hits, res.Hits...)
		}
		search.Sort(hits)
		return hits, nil
	})
	for _, pr := range products {
		if hits := done[pr]; len(hits) > 0 {
			out.Groups = append(out.Groups, QuickGroup{Product: pr, Hits: hits})
		}
	}
	// The group with the best match comes first; equally good ones keep the products' order.
	slices.SortStableFunc(out.Groups, func(a, b QuickGroup) int { return search.Compare(a.Hits[0], b.Hits[0]) })
	out.Incomplete = late
	return out, nil
}

// SearchGroup is a kind of thing the search page found.
type SearchGroup struct {
	Product string       `json:"product" enum:"kontor,logbuch,hafen,helling,people"`
	Kind    string       `json:"kind" enum:"issue,project,page,blog,space,repository,pull-request,code-project,pipelines,user"`
	Total   int          `json:"total" doc:"How many were found in all"`
	Hits    []search.Hit `json:"hits"`
}

// SearchResult is the answer of the search page.
type SearchResult struct {
	Groups     []SearchGroup `json:"groups" doc:"Every kind searched, with what it found, in the order of the products"`
	Incomplete []string      `json:"incomplete" doc:"Products that did not answer in time; what they would have found is missing"`
}

// SearchAll is the search page: titles and texts of everything the principal may see. Without a
// kind it returns the first limit hits of every kind with their totals; with a kind, one page of that kind.
// People are searched only for those who may browse users.
func (s *Service) SearchAll(ctx context.Context, p *identity.Principal, text, kind string, limit, offset int) (*SearchResult, error) {
	q := search.Parse(text)
	out := &SearchResult{Groups: []SearchGroup{}, Incomplete: []string{}}
	if q.Empty() {
		return out, nil
	}
	people := s.mayFindPeople(ctx, p)
	byProduct := map[string][]finder{}
	for _, f := range s.finders() {
		if f.page && (kind == "" || f.kind == kind) && (f.product != ProductPeople || people) {
			byProduct[f.product] = append(byProduct[f.product], f)
		}
	}
	products := []string{}
	for _, pr := range productOrder {
		if len(byProduct[pr]) > 0 {
			products = append(products, pr)
		}
	}
	done, late := fanOut(ctx, s, pageBudget, products, func(ctx context.Context, product string) ([]SearchGroup, error) {
		var groups []SearchGroup
		for _, f := range byProduct[product] {
			res, err := f.find(ctx, p, search.Request{Query: q, Text: true, Limit: limit, Offset: offset, Count: true})
			if err != nil {
				return nil, err
			}
			groups = append(groups, SearchGroup{Product: f.product, Kind: f.kind, Total: res.Total, Hits: res.Hits})
		}
		return groups, nil
	})
	for _, pr := range products {
		out.Groups = append(out.Groups, done[pr]...)
	}
	out.Incomplete = late
	return out, nil
}
