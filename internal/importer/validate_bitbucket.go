// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/blob"
)

// The validation of a Bitbucket load: besides counts, the branches and tags of every repository are compared
// with the extracted mirror, and the LFS objects with what Kogge stores.

// Validate compares the extraction with the load in Kogge and writes bitbucket-validation.json and
// bitbucket-validation.html into the work directory.
func (b *Bitbucket) Validate(ctx context.Context, l Loader, opt ValidateOptions) (*Validation, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := b.work.Read(&server, "bitbucket", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Bitbucket extraction", b.work.Root)
	}
	if l.Code == nil {
		return nil, fmt.Errorf("the validation needs the code module")
	}
	v := newValidation(ctx, "bitbucket", strings.TrimRight(cmp.Or(server.BaseURL, b.opt.URL), "/"), b.work, l, opt)
	bv := &bitbucketValidation{validation: v, b: b, projects: map[string]string{}, repos: map[string]string{}, prs: map[string]string{},
		comments: map[string]string{}, rules: map[string]string{}, emails: map[string]string{}, mirrors: map[string]string{},
		readers: map[string][]string{}, public: map[string]bool{}}
	if err := bv.run(); err != nil {
		return nil, err
	}
	return v.Validation, v.finish()
}

type bitbucketValidation struct {
	*validation
	b                                   *Bitbucket
	projects, repos, prs, comments      map[string]string // by source id
	rules                               map[string]string
	emails                              map[string]string // of the source's users, by lower-case name
	mirrors                             map[string]string // mirror directory by repository source id
	lfs                                 map[string][]lfsPointer
	readers                             map[string][]string // users with read access by direct grants, by KEY/slug
	public                              map[string]bool
	prList                              []string // "repoSrcID:number" for samples
	prDescriptions, prAuthors, prStates map[string]string
	prCreated                           map[string]int64
	prComments                          map[string]int
	names                               map[string]string // KEY/slug by repository source id
}

func (bv *bitbucketValidation) run() error {
	l := bv.l
	bv.lfs = map[string][]lfsPointer{}
	bv.prDescriptions, bv.prAuthors, bv.prStates = map[string]string{}, map[string]string{}, map[string]string{}
	bv.prCreated, bv.prComments, bv.names = map[string]int64{}, map[string]int{}, map[string]string{}
	var people []bbUser
	if _, err := bv.work.Read(&people, "bitbucket", "users.json"); err != nil {
		return err
	}
	for _, u := range people {
		bv.emails[strings.ToLower(cmp.Or(u.Name, u.Slug))] = u.EmailAddress
	}
	for _, key := range bv.work.List("bitbucket", "projects") {
		if len(bv.b.opt.Projects) > 0 && !slices.ContainsFunc(bv.b.opt.Projects, func(s string) bool { return strings.EqualFold(s, key) }) {
			continue
		}
		if err := bv.collect(key); err != nil {
			return fmt.Errorf("project %s: %w", key, err)
		}
	}
	for _, c := range []struct {
		id, label, sourceType, entityType string
		src                               map[string]string
	}{
		{"code-projects", l.t("Code projects"), "code-project", "code-project", bv.projects},
		{"repositories", l.t("Repositories"), "repository", "repository", bv.repos},
		{"pull-requests", l.t("Pull requests"), "pull-request", "pull-request", bv.prs},
		{"comments", l.t("Pull request comments"), "pull-request-comment", "pull-request-comment", bv.comments},
		{"branch-permissions", l.t("Branch permissions"), "branch-permission", "branch-permission", bv.rules},
	} {
		if _, err := bv.countCheck(c.id, c.label, c.src, c.sourceType, c.entityType); err != nil {
			return err
		}
	}
	if err := bv.repositories(); err != nil {
		return err
	}
	if err := bv.linkCheck("pull-request", "pull-request-comment"); err != nil {
		return err
	}
	if err := bv.samples(); err != nil {
		return err
	}
	return bv.permissions()
}

// collect reads what the extraction of a project holds.
func (bv *bitbucketValidation) collect(key string) error {
	dir := []string{"bitbucket", "projects", key}
	var p bbProject
	if ok, err := bv.work.Read(&p, append(dir, "project.json")...); err != nil || !ok {
		return err
	}
	if p.Type == "PERSONAL" || strings.HasPrefix(p.Key, "~") {
		return nil // not migrated, and reported so
	}
	bv.projects[strconv.FormatInt(p.ID, 10)] = p.Key
	var projectReaders []string
	if err := bv.restrictions(p.Key, &projectReaders, dir...); err != nil {
		return err
	}
	for _, slug := range bv.work.List(append(dir, "repos")...) {
		rdir := append(slices.Clone(dir), "repos", slug)
		var r bbRepo
		if ok, err := bv.work.Read(&r, append(rdir, "repo.json")...); err != nil {
			return err
		} else if !ok {
			continue
		}
		id := strconv.FormatInt(r.ID, 10)
		name := p.Key + "/" + r.Slug
		bv.repos[id], bv.names[id] = name, name
		readers := slices.Clone(projectReaders)
		if err := bv.restrictions(name, &readers, rdir...); err != nil {
			return err
		}
		slices.Sort(readers)
		bv.readers[name] = slices.Compact(readers)
		bv.public[name] = r.Public || p.Public
		mirror := bv.work.Path("bitbucket", "git", p.Key, r.Slug+".git")
		if _, err := os.Stat(mirror); err == nil {
			bv.mirrors[id] = mirror
			var pointers []lfsPointer
			if _, err := bv.work.Read(&pointers, append(rdir, "lfs.json")...); err != nil {
				return err
			}
			bv.lfs[id] = pointers
		}
		if !bv.work.Exists(append(rdir, "pull-requests.json")...) {
			continue
		}
		err := eachItem([]string{bv.work.Path(append(rdir, "pull-requests.json")...)}, func(raw json.RawMessage, _ itemAt) error {
			var pr bbPullRequest
			if err := json.Unmarshal(raw, &pr); err != nil {
				return err
			}
			srcID := id + ":" + strconv.Itoa(pr.ID)
			label := fmt.Sprintf("%s#%d", name, pr.ID)
			bv.prs[srcID] = label
			bv.prList = append(bv.prList, srcID)
			bv.prDescriptions[srcID], bv.prAuthors[srcID], bv.prCreated[srcID] = pr.Description, cmp.Or(pr.Author.User.Name, pr.Author.User.Slug), pr.CreatedDate
			state := prStates[pr.State]
			if from := pr.FromRef.Repository; from.ID != 0 && from.ID != r.ID && state == "open" {
				state = "declined" // open pull requests from forks are declined by the load
			}
			bv.prStates[srcID] = cmp.Or(state, "declined")
			var activities []bbActivity
			if _, err := bv.work.Read(&activities, append(rdir, "activities", strconv.Itoa(pr.ID)+".json")...); err != nil {
				return err
			}
			var walk func(c *bbComment)
			walk = func(c *bbComment) {
				bv.comments["comment:"+strconv.FormatInt(c.ID, 10)] = label
				bv.prComments[srcID]++
				for i := range c.Comments {
					walk(&c.Comments[i])
				}
			}
			for _, a := range activities {
				if a.Action == "COMMENTED" && a.CommentAction == "ADDED" && a.Comment != nil {
					walk(a.Comment)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// restrictions notes the branch permissions the load migrates, and the users the scope's permissions grant read
// access directly.
func (bv *bitbucketValidation) restrictions(scope string, readers *[]string, dir ...string) error {
	var list []bbRestriction
	if _, err := bv.work.Read(&list, append(dir, "restrictions.json")...); err != nil {
		return err
	}
	for _, r := range list {
		if _, ok := restrictionKinds[r.Type]; ok && (r.Matcher.Type.ID == "BRANCH" || r.Matcher.Type.ID == "PATTERN" || r.Matcher.Type.ID == "ANY_REF") {
			bv.rules["restriction:"+strconv.FormatInt(r.ID, 10)] = scope + " " + r.Type + " " + r.Matcher.DisplayID
		}
	}
	var users []bbUserPermission
	if _, err := bv.work.Read(&users, append(dir, "permissions-users.json")...); err != nil {
		return err
	}
	for _, p := range users {
		if _, ok := projectPermissions[p.Permission]; ok {
			*readers = append(*readers, cmp.Or(p.User.Name, p.User.Slug))
		} else if _, ok := repoPermissions[p.Permission]; ok {
			*readers = append(*readers, cmp.Or(p.User.Name, p.User.Slug))
		}
	}
	return nil
}

// mirrorRefs lists the branches and tags of a mirror.
func mirrorRefs(ctx context.Context, dir string) (map[string]string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "--git-dir", dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/", "refs/tags/")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git for-each-ref in %s: %w", dir, err)
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok {
			refs[name] = sha
		}
	}
	return refs, nil
}

// repositories compares the branches and tags of every repository with its mirror, and its LFS objects.
func (bv *bitbucketValidation) repositories() error {
	l := bv.l
	have, err := importedIDs(bv.ctx, bv.ld.Pool, bv.source, "repository", "repository")
	if err != nil {
		return err
	}
	refs := Check{ID: "repositories.refs", Group: "repositories", Title: l.t("Branches and tags")}
	lfs := Check{ID: "repositories.lfs", Group: "repositories", Title: l.t("LFS objects")}
	checked, differ, objects, lfsBad, notExtracted := 0, 0, 0, 0, 0
	for _, id := range slices.Sorted(mapKeys(bv.mirrors)) {
		repoID, ok := have[id]
		if !ok {
			continue // counted as missing
		}
		name := bv.names[id]
		want, err := mirrorRefs(bv.ctx, bv.mirrors[id])
		if err != nil {
			return err
		}
		got, err := bv.ld.Code.ImportedRefs(bv.ctx, repoID)
		if err != nil {
			return err
		}
		checked++
		bad := 0
		for _, ref := range slices.Sorted(mapKeys(want)) {
			switch sha, ok := got[ref]; {
			case !ok:
				bad++
				refs.detail(l.t("%s: %s is missing", name, ref))
			case sha != want[ref]:
				bad++
				refs.detail(l.t("%s: %s points to %s, in the source to %s", name, ref, sha[:min(12, len(sha))], want[ref][:min(12, len(want[ref]))]))
			}
		}
		if bad > 0 {
			differ++
		}
		stored, err := lfsObjectsOf(bv.ctx, bv.ld.Pool, repoID)
		if err != nil {
			return err
		}
		for _, p := range bv.lfs[id] {
			file := bv.work.Path(lfsFile("bitbucket", p.OID)...)
			if _, err := os.Stat(file); err != nil {
				notExtracted++
				continue
			}
			objects++
			switch {
			case !stored[p.OID]:
				lfsBad++
				lfs.detail(l.t("%s: the LFS object %s is missing", name, p.OID[:12]))
			case bv.opt.DataDir != "":
				sum, _, err := fileSHA256(blob.FilePath(bv.opt.DataDir, p.OID))
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				if sum != p.OID {
					lfsBad++
					lfs.detail(l.t("%s: the stored file of the LFS object %s is missing or damaged", name, p.OID[:12]))
				}
			}
		}
	}
	switch {
	case checked == 0:
		refs.Status, refs.Message = Warn, l.t("No repository history was extracted: nothing to compare.")
	case differ > 0:
		refs.Status, refs.Message = Fail, l.t("%d of %d repositories have other branches or tags than the extracted mirror.", differ, checked)
	default:
		refs.Status, refs.Message = Pass, l.t("The %d repositories have the branches and tags of the extracted mirrors, pointing to the same commits.", checked)
	}
	bv.add(refs)
	switch {
	case lfsBad > 0:
		lfs.Status, lfs.Message = Fail, l.t("%d of %d LFS objects are missing in Kogge or damaged.", lfsBad, objects)
	case notExtracted > 0:
		lfs.Status, lfs.Message = Warn, l.t("%d LFS objects are in Kogge; %d were not extracted (the source lacks them, or the extraction left them out).", objects, notExtracted)
	default:
		lfs.Status, lfs.Message = Pass, l.t("All %d LFS objects are in Kogge.", objects)
	}
	bv.add(lfs)
	return nil
}

// samples checks the descriptions, authors, dates, states and comments of a sample of pull requests.
func (bv *bitbucketValidation) samples() error {
	l := bv.l
	have, err := importedIDs(bv.ctx, bv.ld.Pool, bv.source, "pull-request", "pull-request")
	if err != nil {
		return err
	}
	render := Check{ID: "renderings.pull-requests", Group: "renderings", Title: l.t("Descriptions of a sample of pull requests")}
	history := Check{ID: "history.pull-requests", Group: "history", Title: l.t("Authors, dates, states and comments of a sample of pull requests")}
	rendered, renderBad, checked, historyBad := 0, 0, 0, 0
	for _, srcID := range sample(bv.prList, bv.opt.Sample) {
		id, ok := have[srcID]
		if !ok {
			continue
		}
		f, err := pullRequestFactsOf(bv.ctx, bv.ld.Pool, id)
		if err != nil {
			return err
		}
		label := bv.prs[srcID]
		if desc := bv.prDescriptions[srcID]; desc != "" {
			rendered++
			var raw []byte
			if err := bv.ld.Pool.QueryRow(bv.ctx, "SELECT description FROM pull_requests WHERE id = $1", id).Scan(&raw); err != nil {
				return err
			}
			if problem := bv.rendering(raw, bbMarkdown(desc)); problem != "" {
				renderBad++
				render.detail(label + ": " + problem)
			}
		}
		checked++
		var problems []string
		author := bv.prAuthors[srcID]
		m, _, err := bv.expectedUser(author, bv.emails[strings.ToLower(author)])
		if err != nil {
			return err
		}
		if author != "" && !strings.EqualFold(m.Username, f.author) {
			problems = append(problems, l.t("%s is %q in Kogge, %q in the source", l.t("the author"), f.author, m.Username))
		}
		if created := ms(bv.prCreated[srcID]); bv.prCreated[srcID] > 0 && !sameTime(f.created, &created) {
			problems = append(problems, l.t("created %s in Kogge, %s in the source", f.created.UTC().Format("2006-01-02 15:04:05"),
				created.UTC().Format("2006-01-02 15:04:05")))
		}
		if f.state != bv.prStates[srcID] {
			problems = append(problems, l.t("%s in Kogge, %s in the source", f.state, bv.prStates[srcID]))
		}
		if f.comments != bv.prComments[srcID] {
			problems = append(problems, l.t("%d comments in Kogge, %d in the source", f.comments, bv.prComments[srcID]))
		}
		if len(problems) > 0 {
			historyBad++
			for _, p := range problems {
				history.detail(label + ": " + p)
			}
		}
	}
	switch {
	case rendered == 0:
		render.Status, render.Message = Warn, l.t("No pull request of the sample has a description.")
	case renderBad > 0:
		render.Status, render.Message = Fail, l.t("%d of %d descriptions do not render like the source.", renderBad, rendered)
	default:
		render.Status, render.Message = Pass, l.t("%d descriptions are valid documents, render and keep the source's text.", rendered)
	}
	bv.add(render)
	switch {
	case checked == 0:
		history.Status, history.Message = Warn, l.t("No pull request of the sample is in Kogge.")
	case historyBad > 0:
		history.Status, history.Message = Fail, l.t("%d of %d pull requests differ from the source.", historyBad, checked)
	default:
		history.Status, history.Message = Pass, l.t("%d pull requests have the author, creation date, state and comments of the source.", checked)
	}
	bv.add(history)
	return nil
}

// permissions checks that the users the permissions name directly may read the repositories. Bitbucket's API does
// not list the members of groups, so access through groups cannot be compared.
func (bv *bitbucketValidation) permissions() error {
	l := bv.l
	var cases []accessCase
	var all []string
	for _, readers := range bv.readers {
		all = append(all, readers...)
	}
	slices.Sort(all)
	all = slices.Compact(all)
	for _, name := range sample(slices.Sorted(mapKeys(bv.readers)), bv.opt.Sample) {
		for _, u := range sample(all, bv.opt.Sample) {
			direct := slices.ContainsFunc(bv.readers[name], func(r string) bool { return strings.EqualFold(r, u) })
			if !direct && !bv.public[name] {
				continue // perhaps through a group
			}
			cases = append(cases, accessCase{user: u, email: bv.emails[strings.ToLower(u)], target: name, label: l.t("the repository %s", name),
				want: true})
		}
	}
	return bv.accessCheck("repositories", l.t("Who may read the repositories"), cases, func(p *identity.Principal, name string) bool {
		key, slug, _ := strings.Cut(name, "/")
		_, err := bv.ld.Code.GetRepository(bv.ctx, p, key, slug)
		return err == nil
	})
}
