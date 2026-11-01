// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/platform/legacy"
	"kogge.de/kogge/internal/wiki"
)

// Redirects sends addresses of the migrated products to their counterparts in Kogge, so that
// bookmarks and links in e-mails and documents keep working once the old host names point to Kogge. Only what
// the principal may see is resolved; everything else is served by the web UI, which shows its "not found" page.
type Redirects struct {
	Pool     *pgxpool.Pool
	Issues   *issues.Service
	Wiki     *wiki.Service
	Code     *code.Service
	Fallback http.Handler
}

// Routes registers the old addresses (behind authentication).
func (h *Redirects) Routes(r chi.Router) {
	// Confluence
	r.Get("/display/{space}", h.space)
	r.Get("/display/{space}/*", h.display)
	r.Get("/pages/viewpage.action", h.viewPage)
	r.Get("/spaces/{space}", h.space)
	r.Get("/spaces/{space}/overview", h.space)
	r.Get("/spaces/{space}/pages/{id}", h.page)
	r.Get("/spaces/{space}/pages/{id}/*", h.page)
	r.Get("/spaces/{space}/blog/*", h.blog)
	r.Get("/x/{tiny}", h.tiny)
	r.Get("/download/attachments/{page}/{file}", h.wikiAttachment)
	// Jira
	r.Get("/browse/{key}", h.browse)
	r.Get("/projects/{key}/issues/{issue}", h.projectIssue)
	r.Get("/secure/RapidBoard.jspa", h.board)
	r.Get("/secure/attachment/{id}/*", h.issueAttachment)
	r.Get("/issues/", h.issueSearch)
	// Bitbucket (clone addresses, /scm/KEY/slug.git, are Kogge's own)
	r.Get("/projects/{key}/repos/{slug}", h.repository)
	r.Get("/projects/{key}/repos/{slug}/*", h.repository)
	// Bamboo: plans, their builds and jobs are below /browse/ like Jira's issues (see browse); the pages of a build
	// (log, tests, artifacts) below them.
	r.Get("/browse/{key}/*", h.browse)
	r.Get("/chain/viewChain.action", h.bambooPlanPage)
	r.Get("/chain/result/viewChainResult.action", h.bambooPlanPage)
	r.Get("/build/viewBuildResults.action", h.bambooPlanPage)
	r.Get("/chain/admin/config/*", h.bambooPlanSettings)
	r.Get("/deploy/viewDeploymentProjectEnvironments.action", h.bambooDeployment)
	r.Get("/deploy/viewEnvironment.action", h.bambooEnvironment)
}

func (h *Redirects) to(w http.ResponseWriter, r *http.Request, target string, err error) {
	if err != nil || target == "" {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// pageBySource redirects to the page migrated from a source page id.
func (h *Redirects) pageBySource(w http.ResponseWriter, r *http.Request, srcID string) {
	id, ok, err := legacy.Find(r.Context(), h.Pool, "page", srcID)
	if err != nil || !ok {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	target, err := h.Wiki.PageAddress(r.Context(), identity.From(r.Context()), id)
	h.to(w, r, target, err)
}

func (h *Redirects) space(w http.ResponseWriter, r *http.Request) {
	target, err := h.Wiki.SpaceAddress(r.Context(), identity.From(r.Context()), chi.URLParam(r, "space"))
	h.to(w, r, target, err)
}

var blogPath = regexp.MustCompile(`^\d{4}/\d{2}/\d{2}/(.+)$`)

// titleOf reads a page title of an address: "+" stands for a space.
func titleOf(s string) string {
	t, err := url.PathUnescape(strings.ReplaceAll(s, "+", " "))
	if err != nil {
		return s
	}
	return t
}

// display handles /display/SPACE/Title and /display/SPACE/YYYY/MM/DD/Blog+Title.
func (h *Redirects) display(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimSuffix(chi.URLParam(r, "*"), "/")
	blog := false
	if m := blogPath.FindStringSubmatch(rest); m != nil {
		rest, blog = m[1], true
	}
	target, err := h.Wiki.PageAddressByTitle(r.Context(), identity.From(r.Context()), chi.URLParam(r, "space"), titleOf(rest), blog)
	h.to(w, r, target, err)
}

func (h *Redirects) viewPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if id := q.Get("pageId"); id != "" {
		h.pageBySource(w, r, id)
		return
	}
	target, err := h.Wiki.PageAddressByTitle(r.Context(), identity.From(r.Context()), q.Get("spaceKey"), q.Get("title"), false)
	h.to(w, r, target, err)
}

func (h *Redirects) page(w http.ResponseWriter, r *http.Request) {
	h.pageBySource(w, r, chi.URLParam(r, "id"))
}

// blog handles /spaces/SPACE/blog/YYYY/MM/DD/ID/Title.
func (h *Redirects) blog(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(chi.URLParam(r, "*"), "/")
	if len(parts) >= 4 {
		if _, err := strconv.ParseInt(parts[3], 10, 64); err == nil {
			h.pageBySource(w, r, parts[3])
			return
		}
	}
	h.Fallback.ServeHTTP(w, r)
}

// tinyID decodes a short link of Confluence: the page id in little-endian bytes, base64 with an URL-safe alphabet
// and without padding.
func tinyID(s string) (int64, bool) {
	s = strings.NewReplacer("-", "/", "_", "+").Replace(s)
	for len(s)%4 != 0 {
		s += "A"
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return 0, false
	}
	var id int64
	for i := 0; i < len(b) && i < 8; i++ {
		id |= int64(b[i]) << (8 * i)
	}
	return id, id > 0
}

func (h *Redirects) tiny(w http.ResponseWriter, r *http.Request) {
	id, ok := tinyID(chi.URLParam(r, "tiny"))
	if !ok {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	h.pageBySource(w, r, strconv.FormatInt(id, 10))
}

func (h *Redirects) wikiAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok, err := legacy.Find(r.Context(), h.Pool, "page", chi.URLParam(r, "page"))
	if err != nil || !ok {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	target, err := h.Wiki.AttachmentAddress(r.Context(), identity.From(r.Context()), id, chi.URLParam(r, "file"))
	h.to(w, r, target, err)
}

// browse sends /browse/PROJECT to the Jira project migrated to Kogge (issue keys are pages of the web UI), and
// the plans, builds, jobs and projects of Bamboo to the pipelines of the repositories they went to.
func (h *Redirects) browse(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if chi.URLParam(r, "*") == "" {
		if upper := strings.ToUpper(key); docmodel.IsIssueKey(upper) {
			// The key an issue had before it moved to another project leads to its current address.
			current, err := h.Issues.CurrentKey(r.Context(), identity.From(r.Context()), upper)
			if err == nil && current != upper {
				h.to(w, r, "/browse/"+current, nil)
				return
			}
			h.Fallback.ServeHTTP(w, r)
			return
		}
		if p, err := h.Issues.GetProject(r.Context(), identity.From(r.Context()), strings.ToUpper(key)); err == nil {
			h.to(w, r, "/projects/"+p.Key, nil)
			return
		}
	}
	h.to(w, r, h.bambooTarget(r, key, ""), nil)
}

// bambooKey reads the key of a Bamboo address: a project (SHOP), a plan (SHOP-WEB), a build of it (SHOP-WEB-12),
// a job (SHOP-WEB-JOB1) or a build of a job (SHOP-WEB-JOB1-12).
var bambooKey = regexp.MustCompile(`^([A-Z][A-Z0-9]*)(?:-([A-Z][A-Z0-9]*))?(?:-([A-Z][A-Z0-9]*))?(?:-(\d+))?$`)

// bambooTarget is the place in Kogge of a Bamboo key that the reader may see, or "". Builds and jobs lead to the
// pipelines of the plan's repository: their history stays in Bamboo's summary.
func (h *Redirects) bambooTarget(r *http.Request, key, suffix string) string {
	m := bambooKey.FindStringSubmatch(strings.ToUpper(key))
	if m == nil {
		return ""
	}
	ctx := r.Context()
	if m[2] == "" {
		// A project, when its plans went to the repositories of one code project.
		id, ok, err := legacy.Find(ctx, h.Pool, "plan-project", m[1])
		if err != nil || !ok {
			return ""
		}
		key, err := codeProjectKey(ctx, h.Pool, id)
		if err != nil {
			return ""
		}
		p, err := h.Code.GetProject(ctx, identity.From(ctx), key)
		if err != nil {
			return ""
		}
		return "/builds/" + url.PathEscape(strings.ToLower(p.Key))
	}
	plan := m[1] + "-" + m[2]
	id, ok, err := legacy.Find(ctx, h.Pool, "plan", plan)
	if err == nil && !ok {
		// A branch of a plan: the plan's key with a number.
		if branch := strings.TrimRight(m[2], "0123456789"); branch != m[2] && branch != "" {
			id, ok, err = legacy.Find(ctx, h.Pool, "plan", m[1]+"-"+branch)
		}
	}
	if err != nil || !ok {
		return ""
	}
	return h.repositoryBuilds(r, id, suffix)
}

// repositoryBuilds is the address of the pipelines of a repository (with suffix) if the reader may see it.
func (h *Redirects) repositoryBuilds(r *http.Request, repoID int64, suffix string) string {
	key, slug, err := repositoryPath(r.Context(), h.Pool, repoID)
	if err != nil {
		return ""
	}
	repo, err := h.Code.GetRepository(r.Context(), identity.From(r.Context()), key, slug)
	if err != nil {
		return ""
	}
	return "/builds/" + url.PathEscape(strings.ToLower(repo.Project.Key)) + "/" + url.PathEscape(repo.Slug) + suffix
}

// bambooPlanPage handles the older addresses of plans and builds, which name the plan in the query.
func (h *Redirects) bambooPlanPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.to(w, r, h.bambooTarget(r, cmp.Or(q.Get("planKey"), q.Get("buildKey")), ""), nil)
}

// bambooPlanSettings sends the configuration pages of a plan to the settings of the pipelines.
func (h *Redirects) bambooPlanSettings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	key := cmp.Or(q.Get("buildKey"), q.Get("planKey"))
	if !strings.Contains(key, "-") {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	h.to(w, r, h.bambooTarget(r, key, "/settings"), nil)
}

// bambooDeployment sends a deployment project to the environments of the repository its plan went to.
func (h *Redirects) bambooDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	repo, ok, err := legacy.Find(r.Context(), h.Pool, "deployment-project", id)
	if err != nil || !ok {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	h.to(w, r, h.repositoryBuilds(r, repo, "/environments"), nil)
}

// bambooEnvironment sends an environment of a deployment project to the environments of its repository.
func (h *Redirects) bambooEnvironment(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	env, ok, err := legacy.Find(r.Context(), h.Pool, "deployment-environment", id)
	if err != nil || !ok {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	repo, err := environmentRepository(r.Context(), h.Pool, env)
	if err != nil {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	h.to(w, r, h.repositoryBuilds(r, repo, "/environments"), nil)
}

func (h *Redirects) projectIssue(w http.ResponseWriter, r *http.Request) {
	key := strings.ToUpper(chi.URLParam(r, "issue"))
	if !docmodel.IsIssueKey(key) {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	h.to(w, r, "/browse/"+key, nil)
}

func (h *Redirects) board(w http.ResponseWriter, r *http.Request) {
	id, ok, err := legacy.Find(r.Context(), h.Pool, "board", r.URL.Query().Get("rapidView"))
	if err != nil || !ok {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	p := identity.From(r.Context())
	b, err := h.Issues.GetBoard(r.Context(), p, id)
	if err == nil {
		_, err = h.Issues.GetProject(r.Context(), p, b.Project.Key)
	}
	if err != nil {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	target := "/projects/" + b.Project.Key + "/boards/" + strconv.FormatInt(id, 10)
	if r.URL.Query().Get("view") == "planning" || r.URL.Query().Get("view") == "planning.nodetail" {
		target += "/backlog"
	}
	h.to(w, r, target, nil)
}

// issueAttachment sends a Jira attachment to the download of the migrated file, which checks permissions.
func (h *Redirects) issueAttachment(w http.ResponseWriter, r *http.Request) {
	src := chi.URLParam(r, "id")
	if _, err := strconv.ParseInt(src, 10, 64); err != nil {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	id, ok, err := legacy.Find(r.Context(), h.Pool, "attachment", src)
	if err != nil || !ok {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	h.to(w, r, "/api/v1/issue-attachments/"+strconv.FormatInt(id, 10), nil)
}

// issueSearch sends Jira's issue navigator to the search; KQL reads most JQL.
func (h *Redirects) issueSearch(w http.ResponseWriter, r *http.Request) {
	jql := r.URL.Query().Get("jql")
	if jql == "" {
		h.to(w, r, "/issues/search", nil)
		return
	}
	h.to(w, r, "/issues/search?q="+url.QueryEscape(jql), nil)
}

// repository sends the addresses of Bitbucket repositories (/projects/KEY/repos/slug/…) to the repository.
func (h *Redirects) repository(w http.ResponseWriter, r *http.Request) {
	repo, err := h.Code.GetRepository(r.Context(), identity.From(r.Context()), strings.ToUpper(chi.URLParam(r, "key")),
		chi.URLParam(r, "slug"))
	if err != nil {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	h.to(w, r, repositoryTarget(repo.Project.Key, repo.Slug, chi.URLParam(r, "*"), r.URL.Query()), nil)
}

var commitID = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// repositoryTarget translates the rest of a repository address: files (browse, raw) at a ref, commits, pull
// requests (whose numbers the migration keeps), branches.
func repositoryTarget(key, slug, rest string, q url.Values) string {
	base := "/code/" + url.PathEscape(strings.ToLower(key)) + "/" + url.PathEscape(slug)
	ref := func(param string) string { return strings.TrimPrefix(q.Get(param), "refs/heads/") }
	at := func(target, param string) string {
		if r := ref(param); r != "" {
			return target + "?at=" + url.QueryEscape(r)
		}
		return target
	}
	section, sub, _ := strings.Cut(strings.Trim(rest, "/"), "/")
	switch section {
	case "", "browse":
		if sub == "" {
			return at(base, "at")
		}
		return at(base+"/src/"+escapePath(sub), "at")
	case "raw":
		target := "/api/v1/code/repos/" + url.PathEscape(key) + "/" + url.PathEscape(slug) + "/raw?path=" + url.QueryEscape(unescape(sub))
		if r := ref("at"); r != "" {
			target += "&ref=" + url.QueryEscape(r)
		}
		return target
	case "commits":
		if sha, _, _ := strings.Cut(sub, "/"); commitID.MatchString(sha) {
			return base + "/commits/" + sha
		}
		return at(base+"/commits", "until")
	case "pull-requests":
		if n, _, _ := strings.Cut(sub, "/"); n != "" {
			if _, err := strconv.Atoi(n); err == nil {
				return base + "/pull-requests/" + n
			}
		}
		return base + "/pull-requests"
	case "branches", "tags", "compare", "settings":
		return base + "/" + section
	}
	return base
}

func unescape(p string) string {
	if u, err := url.PathUnescape(p); err == nil {
		return u
	}
	return p
}

// escapePath escapes the segments of a path in a repository, keeping the slashes.
func escapePath(p string) string {
	parts := strings.Split(unescape(p), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
