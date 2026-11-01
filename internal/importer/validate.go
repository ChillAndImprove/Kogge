// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/blob"
)

// The validation compares an extraction with what a load made of it in Kogge: the
// counts of every kind of entity, the checksums of attachments, the links inside the migrated content, sample
// renderings, permission spot checks and the history and authors of samples. Each check passes, warns or fails;
// a validation with failures is not a basis for the cut-over.

// ValidateOptions configure a validation.
type ValidateOptions struct {
	Language string
	// Sample is the number of entities of each spot check (default 25).
	Sample int
	// DataDir is Kogge's data directory: the checksums of the stored files are computed from it.
	DataDir string
	// Concurrency is the number of files hashed at a time (default 4).
	Concurrency int
}

type validation struct {
	*Validation
	ctx    context.Context
	l      lang
	work   Workdir
	ld     Loader
	opt    ValidateOptions
	source string
	base   string
	users  map[string]identity.ImportMatch // expected Kogge users by source user name (lower case)
}

func newValidation(ctx context.Context, product, base string, w Workdir, l Loader, opt ValidateOptions) *validation {
	if opt.Sample <= 0 {
		opt.Sample = 25
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	return &validation{Validation: &Validation{Product: product, Source: base, Workdir: w.Root, Language: opt.Language,
		Generated: time.Now(), Checks: []Check{}}, ctx: ctx, l: lang(opt.Language), work: w, ld: l, opt: opt,
		source: product + ":" + base, base: base, users: map[string]identity.ImportMatch{}}
}

// finish writes the report into the work directory.
func (v *validation) finish() error {
	return writeReport(v.work, v.Product+"-validation", v.Validation, v.view())
}

// sample picks up to n of the keys: the same ones every time, spread over the list.
func sample(keys []string, n int) []string {
	type h struct {
		key  string
		hash uint64
	}
	list := make([]h, 0, len(keys))
	for _, k := range keys {
		f := fnv.New64a()
		f.Write([]byte(k))
		list = append(list, h{k, f.Sum64()})
	}
	slices.SortFunc(list, func(a, b h) int { return cmp.Or(cmp.Compare(a.hash, b.hash), cmp.Compare(a.key, b.key)) })
	out := make([]string, 0, min(n, len(list)))
	for _, x := range list[:min(n, len(list))] {
		out = append(out, x.key)
	}
	slices.Sort(out)
	return out
}

// countCheck compares the entities of a kind in the extraction (source id → name) with those the load recorded
// in Kogge, and returns the latter.
func (v *validation) countCheck(id, label string, src map[string]string, sourceType, entityType string) (map[string]int64, error) {
	l := v.l
	have, err := importedIDs(v.ctx, v.ld.Pool, v.source, sourceType, entityType)
	if err != nil {
		return nil, err
	}
	c := Check{ID: "counts." + id, Group: "counts", Title: label}
	missing := 0
	for _, srcID := range slices.Sorted(mapKeys(src)) {
		if _, ok := have[srcID]; !ok {
			missing++
			c.detail(keyed(src[srcID], srcID))
		}
	}
	switch {
	case missing == 0:
		c.Status, c.Message = Pass, l.t("%d of %d are in Kogge.", len(src), len(src))
	default:
		c.Status, c.Message = Fail, l.t("%d of %d are missing in Kogge (see the load's report for why).", missing, len(src))
	}
	v.add(c)
	return have, nil
}

// sourceFile is an attachment of the extraction.
type sourceFile struct {
	srcID, name string
	path        string // "" when the file was not extracted
}

// fileSHA256 hashes a file, reading it in pieces.
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// fileCheck compares the SHA-256 of the extracted attachment files with what Kogge records and with the files of
// its blob store, several files at a time.
func (v *validation) fileCheck(files []sourceFile, entityType string) error {
	l := v.l
	stored, err := importedFiles(v.ctx, v.ld.Pool, v.source, entityType)
	if err != nil {
		return err
	}
	c := Check{ID: "attachments.checksums", Group: "attachments", Title: l.t("Attachment checksums")}
	var mu sync.Mutex
	blobs := map[string]error{} // checked files of the blob store, by SHA-256
	var bad, absent, notExtracted, matched int
	g, ctx := errgroup.WithContext(v.ctx)
	g.SetLimit(v.opt.Concurrency)
	for _, f := range files {
		if f.path == "" {
			notExtracted++
			continue
		}
		st, ok := stored[f.srcID]
		if !ok {
			absent++
			c.detail(l.t("%s: not in Kogge", keyed(f.name, f.srcID)))
			continue
		}
		g.Go(func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sum, _, err := fileSHA256(f.path)
			if err != nil {
				return err
			}
			problem := ""
			if sum != st.sha256 {
				problem = l.t("%s: the extracted file has the SHA-256 %s, Kogge records %s", keyed(f.name, f.srcID), sum[:12], st.sha256[:min(12, len(st.sha256))])
			} else if v.opt.DataDir != "" {
				mu.Lock()
				berr, done := blobs[st.sha256]
				mu.Unlock()
				if !done {
					got, _, err := fileSHA256(blob.FilePath(v.opt.DataDir, st.sha256))
					switch {
					case os.IsNotExist(err):
						berr = fmt.Errorf("%s", l.t("the stored file is missing"))
					case err != nil:
						return err
					case got != st.sha256:
						berr = fmt.Errorf("%s", l.t("the stored file is damaged (its SHA-256 is %s)", got[:12]))
					}
					mu.Lock()
					blobs[st.sha256] = berr
					mu.Unlock()
				}
				if berr != nil {
					problem = keyed(f.name, f.srcID) + ": " + berr.Error()
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if problem != "" {
				bad++
				c.detail(problem)
			} else {
				matched++
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	switch {
	case bad+absent > 0:
		c.Status, c.Message = Fail, l.t("%d attachments are missing in Kogge or differ from the extracted files; %d match.", bad+absent, matched)
	case notExtracted > 0:
		c.Status, c.Message = Warn, l.t("%d attachments match; %d were not extracted and could not be compared.", matched, notExtracted)
	default:
		c.Status, c.Message = Pass, l.t("All %d attachments match: the SHA-256 of each extracted file is that of the file Kogge stores.", matched)
	}
	if v.opt.DataDir == "" && matched > 0 {
		c.Message += " " + l.t("The files of the blob store were not read (no data directory).")
	}
	v.add(c)
	return nil
}

var (
	browsePath    = regexp.MustCompile(`^/browse/([A-Z][A-Z0-9_]+-[1-9][0-9]*)$`)
	issueFilePath = regexp.MustCompile(`^/api/v1/issue-attachments/(\d+)`)
	wikiFilePath  = regexp.MustCompile(`^/api/v1/wiki/attachments/(\d+)`)
	wikiPagePath  = regexp.MustCompile(`^/wiki/spaces/([^/]+)/pages/(\d+)`)
	repoLinkPath  = regexp.MustCompile(`^/code/([^/?#]+)/([^/?#]+)`)
)

// refs are the references found in migrated documents, with where they were found.
type refs struct {
	issues                map[string][]string
	issueFiles, wikiFiles map[int64][]string
	pages                 map[int64][]string
	pageSpace             map[int64]string // the space a page link names
	users                 map[int64][]string
	repos                 map[string][]string
	unrewritten           []string
	links                 int
}

// add notes a reference with up to three places where it was found.
func (r *refs) add(m map[string][]string, k, where string) {
	if len(m[k]) < 3 {
		m[k] = append(m[k], where)
	}
}

func (r *refs) addID(m map[int64][]string, k int64, where string) {
	if len(m[k]) < 3 {
		m[k] = append(m[k], where)
	}
}

// scan collects the references of a document.
func (r *refs) scan(d *docmodel.Doc, where, base string) {
	docmodel.Walk(d, func(n *docmodel.Node) bool {
		switch n.Type {
		case "issue":
			if k, _ := n.Attrs["key"].(string); k != "" {
				r.links++
				r.add(r.issues, k, where)
			}
		case "image", "inlineImage":
			src, _ := n.Attrs["src"].(string)
			if id := numberAttr(n.Attrs["attachment"]); id > 0 {
				r.links++
				if strings.HasPrefix(src, "/api/v1/wiki/") {
					r.addID(r.wikiFiles, id, where)
				} else {
					r.addID(r.issueFiles, id, where)
				}
			} else if src != "" {
				r.link(src, where, base)
			}
		case "mention":
			if id := numberAttr(n.Attrs["user"]); id > 0 {
				r.addID(r.users, id, where)
			}
		case "macro", "inlineMacro":
			if p, ok := n.Attrs["params"].(map[string]any); ok {
				if id := numberAttr(p["page"]); id > 0 {
					r.links++
					r.addID(r.pages, id, where)
				}
				if k, _ := p["key"].(string); docmodel.IsIssueKey(k) {
					r.links++
					r.add(r.issues, k, where)
				}
			}
		case "text":
			for _, m := range n.Marks {
				if m.Type == "link" {
					if href, _ := m.Attrs["href"].(string); href != "" {
						r.link(href, where, base)
					}
				}
			}
		}
		return true
	})
}

func (r *refs) link(href, where, base string) {
	path, _, _ := strings.Cut(href, "#")
	switch {
	case browsePath.MatchString(path):
		r.links++
		r.add(r.issues, browsePath.FindStringSubmatch(path)[1], where)
	case issueFilePath.MatchString(path):
		r.links++
		id, _ := strconv.ParseInt(issueFilePath.FindStringSubmatch(path)[1], 10, 64)
		r.addID(r.issueFiles, id, where)
	case wikiFilePath.MatchString(path):
		r.links++
		id, _ := strconv.ParseInt(wikiFilePath.FindStringSubmatch(path)[1], 10, 64)
		r.addID(r.wikiFiles, id, where)
	case wikiPagePath.MatchString(path):
		r.links++
		sm := wikiPagePath.FindStringSubmatch(path)
		id, _ := strconv.ParseInt(sm[2], 10, 64)
		r.addID(r.pages, id, where)
		r.pageSpace[id] = sm[1]
	case repoLinkPath.MatchString(path):
		r.links++
		sm := repoLinkPath.FindStringSubmatch(path)
		r.add(r.repos, strings.ToLower(sm[1]+"/"+sm[2]), where)
	case base != "" && (path == base || strings.HasPrefix(path, base+"/")):
		r.links++
		if len(r.unrewritten) < maxDetails {
			r.unrewritten = append(r.unrewritten, where+": "+href)
		}
	}
}

func numberAttr(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

// linkCheck reads the migrated documents of the kinds (see docQueries) and checks that their references to
// issues, pages, attachments, repositories and users resolve in Kogge. Links that still point to the source
// warn: they work through the redirects only once the old host name points to Kogge, and only for what was
// migrated. References to issues that do not exist fail the validation of Jira; for the other products they
// warn: their projects may be migrated later (they show as their keys until then).
func (v *validation) linkCheck(kinds ...string) error {
	l := v.l
	r := &refs{issues: map[string][]string{}, issueFiles: map[int64][]string{}, wikiFiles: map[int64][]string{},
		pages: map[int64][]string{}, pageSpace: map[int64]string{}, users: map[int64][]string{}, repos: map[string][]string{}}
	docs := 0
	for _, kind := range kinds {
		err := eachImportedDoc(v.ctx, v.ld.Pool, v.source, kind, func(label string, raw []byte) error {
			var d docmodel.Doc
			if json.Unmarshal(raw, &d) != nil {
				return nil // reported by the renderings
			}
			docs++
			r.scan(&d, label, v.base)
			return nil
		})
		if err != nil {
			return err
		}
	}
	c := Check{ID: "links.integrity", Group: "links", Title: l.t("Links inside the migrated content")}
	broken := 0
	report := func(what string, wheres []string) {
		broken++
		c.detail(l.t("%s, in %s", what, strings.Join(wheres, "; ")))
	}
	keys := slices.Sorted(mapKeys(r.issues))
	have, err := existingIssueKeys(v.ctx, v.ld.Pool, keys)
	if err != nil {
		return err
	}
	var unknownIssues []string
	for _, k := range keys {
		switch {
		case have[k]:
		case v.Product == "jira":
			report(l.t("the issue %s does not exist", k), r.issues[k])
		default:
			unknownIssues = append(unknownIssues, l.t("%s, in %s", l.t("the issue %s does not exist", k), strings.Join(r.issues[k], "; ")))
		}
	}
	for _, x := range []struct {
		table, what string
		ids         map[int64][]string
	}{{"issue_attachments", l.t("the attachment %d does not exist"), r.issueFiles}, {"page_attachments", l.t("the attachment %d does not exist"), r.wikiFiles},
		{"pages", l.t("the page %d does not exist"), r.pages}, {"users", l.t("the mentioned user %d does not exist"), r.users}} {
		ids := slices.Sorted(mapKeysInt(x.ids))
		have, err := existingIDs(v.ctx, v.ld.Pool, x.table, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if !have[id] {
				report(fmt.Sprintf(x.what, id), x.ids[id])
			}
		}
	}
	spaces, err := pageSpaces(v.ctx, v.ld.Pool, slices.Collect(mapKeysInt(r.pageSpace)))
	if err != nil {
		return err
	}
	for id, key := range r.pageSpace {
		if got, ok := spaces[id]; ok && !strings.EqualFold(got, key) {
			report(l.t("the link names the page %d in the space %s, but it is in %s", id, key, got), r.pages[id])
		}
	}
	paths := slices.Sorted(mapKeys(r.repos))
	repos, err := existingRepositories(v.ctx, v.ld.Pool, paths)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if !repos[p] {
			report(l.t("the repository %s does not exist", p), r.repos[p])
		}
	}
	switch {
	case broken > 0:
		c.Status, c.Message = Fail, l.t("%d references in %d documents do not resolve in Kogge.", broken, docs)
	case len(unknownIssues) > 0 || len(r.unrewritten) > 0:
		c.Status = Warn
		var parts []string
		if len(unknownIssues) > 0 {
			parts = append(parts, l.t("%d issues the content refers to are not in Kogge (yet): migrate their projects, or they show as their keys.", len(unknownIssues)))
		}
		if len(r.unrewritten) > 0 {
			parts = append(parts, l.t("%d links still point to the source: they work through Kogge's redirects once the old host name points to Kogge, and only for what was migrated.", len(r.unrewritten)))
		}
		c.Message = strings.Join(parts, " ")
		for _, u := range append(unknownIssues, r.unrewritten...) {
			c.detail(u)
		}
	default:
		c.Status, c.Message = Pass, l.t("All %d references in %d documents resolve in Kogge.", r.links, docs)
	}
	v.add(c)
	return nil
}

func mapKeysInt[V any](m map[int64]V) func(func(int64) bool) {
	return func(yield func(int64) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// unsupportedNodes counts the unsupported blocks of a document by name.
func unsupportedNodes(d *docmodel.Doc) map[string]int {
	out := map[string]int{}
	docmodel.Walk(d, func(n *docmodel.Node) bool {
		if n.Type == "unsupported" {
			name, _ := n.Attrs["name"].(string)
			out[name]++
		}
		return true
	})
	return out
}

// rendering checks a migrated document: it is valid, renders, keeps the text of what the conversion of the
// source gives, and has no unsupported blocks beyond those (which the analysis reported). It returns a problem,
// or "".
func (v *validation) rendering(raw []byte, expected *docmodel.Doc) string {
	l := v.l
	d, err := docmodel.Parse(raw, docmodel.Options{})
	if err != nil {
		return l.t("the document is not valid: %s", err.Error())
	}
	html := docmodel.HTML(d, nil)
	want := strings.Fields(docmodel.PlainText(expected))
	got := strings.Fields(docmodel.PlainText(d))
	if len(want) > 0 && (html == "" || len(got) == 0) {
		return l.t("the document is empty, but the source has text")
	}
	// Words of the source's text that the document lacks (links may show another label: a few are allowed).
	have := map[string]int{}
	for _, w := range got {
		have[w]++
	}
	missing := 0
	for _, w := range want {
		if have[w] > 0 {
			have[w]--
		} else {
			missing++
		}
	}
	if missing > max(2, len(want)/10) {
		return l.t("%d of the %d words of the source's text are missing", missing, len(want))
	}
	have, allowed := unsupportedNodes(d), unsupportedNodes(expected)
	var extra []string
	for name, n := range have {
		if n > allowed[name] {
			extra = append(extra, fmt.Sprintf("%s (%d)", cmp.Or(name, "?"), n-allowed[name]))
		}
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		return l.t("unsupported blocks the analysis did not report: %s", strings.Join(extra, ", "))
	}
	return ""
}

// expectedUser is the Kogge user a user of the source became (by the load's rule: user name, then e-mail).
func (v *validation) expectedUser(name, email string) (identity.ImportMatch, bool, error) {
	k := strings.ToLower(name)
	if m, ok := v.users[k]; ok {
		return m, m.ID != 0, nil
	}
	m, found, err := identity.FindImportUser(v.ctx, v.ld.Pool, name, email)
	if err != nil {
		return m, false, err
	}
	v.users[k] = m
	return m, found, nil
}

// principalOf builds the principal of a Kogge user as if the user signed in: placeholders become their
// person's account at the first sign-in and keep what they hold, so they are checked like active users.
func (v *validation) principalOf(id int64) (*identity.Principal, bool, error) {
	u, err := identity.GetUser(v.ctx, v.ld.Pool, id)
	if err != nil {
		return nil, false, err
	}
	admin, err := isAdministrator(v.ctx, v.ld.Pool, id)
	if err != nil {
		return nil, false, err
	}
	groups, err := identity.UserGroups(v.ctx, v.ld.Pool, id)
	if err != nil {
		return nil, false, err
	}
	p := &identity.Principal{User: u, Method: identity.MethodInternal, Groups: []int64{}}
	for _, g := range groups {
		p.Groups = append(p.Groups, g.ID)
	}
	return p, admin, nil
}

// accessCase is one user's access to one project, space, repository or page, as the source's permissions give it.
type accessCase struct {
	user, email string
	target      string // project key, space key, KEY/slug, page id
	label       string
	want        bool
}

// accessCheck checks a sample of access cases with can (which asks Kogge's services).
func (v *validation) accessCheck(id, title string, cases []accessCase, can func(p *identity.Principal, target string) bool) error {
	l := v.l
	c := Check{ID: "permissions." + id, Group: "permissions", Title: title}
	if len(cases) == 0 {
		c.Status, c.Message = Warn, l.t("No permission data of the source to compare with.")
		v.add(c)
		return nil
	}
	checked, differ, skipped := 0, 0, 0
	for _, ac := range cases {
		m, found, err := v.expectedUser(ac.user, ac.email)
		if err != nil {
			return err
		}
		if !found {
			skipped++
			continue
		}
		p, admin, err := v.principalOf(m.ID)
		if err != nil {
			return err
		}
		if admin {
			skipped++ // administrators see more in Kogge by design
			continue
		}
		checked++
		if got := can(p, ac.target); got != ac.want {
			differ++
			if ac.want {
				c.detail(l.t("%s may see %s in the source, not in Kogge", ac.user, ac.label))
			} else {
				c.detail(l.t("%s may see %s in Kogge, not in the source", ac.user, ac.label))
			}
		}
	}
	switch {
	case differ > 0:
		c.Status, c.Message = Fail, l.t("%d of %d checked cases differ from the source's permissions.", differ, checked)
	case checked == 0:
		c.Status, c.Message = Warn, l.t("No case could be checked (%d users are not in Kogge or are administrators).", skipped)
	default:
		c.Status, c.Message = Pass, l.t("All %d checked cases match the source's permissions.", checked)
	}
	if skipped > 0 && checked > 0 {
		c.Message += " " + l.t("%d cases were skipped: users that are not in Kogge, or Kogge administrators.", skipped)
	}
	v.add(c)
	return nil
}

// sameTime reports whether two moments are the same to the second (the source's resolution).
func sameTime(a time.Time, b *time.Time) bool {
	return b != nil && a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}
