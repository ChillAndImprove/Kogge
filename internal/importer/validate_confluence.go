// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
)

// The validation of a Confluence load.

// Validate compares the extraction with the load in Kogge and writes confluence-validation.json and
// confluence-validation.html into the work directory.
func (c *Confluence) Validate(ctx context.Context, l Loader, opt ValidateOptions) (*Validation, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := c.work.Read(&server, "confluence", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Confluence extraction", c.work.Root)
	}
	v := newValidation(ctx, "confluence", strings.TrimRight(cmp.Or(server.BaseURL, c.opt.URL), "/"), c.work, l, opt)
	cv := &confluenceValidation{validation: v, c: c}
	if err := cv.run(); err != nil {
		return nil, err
	}
	return v.Validation, v.finish()
}

// contentIndex is what the conversion of storage format needs to know of the extraction: the pages by title and
// the attachments of each page.
type contentIndex struct {
	byTitle     map[string]string          // page id by titleKey
	attachments map[string]map[string]bool // file names by page id
	users       map[string]cUser           // by user key
}

func readContentIndex(w Workdir) (*contentIndex, error) {
	x := &contentIndex{byTitle: map[string]string{}, attachments: map[string]map[string]bool{}, users: map[string]cUser{}}
	if _, err := w.Read(&x.users, "confluence", "users.json"); err != nil {
		return nil, err
	}
	keys, err := w.spaceKeys()
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		err := eachContent(w, key, func(ct *cContent, _ itemAt) error {
			if ct.Type == "page" {
				x.byTitle[titleKey(key, ct.Title)] = ct.ID
			}
			var atts []struct {
				Title string `json:"title"`
			}
			if _, err := w.Read(&atts, "confluence", "content", ct.ID, "attachments.json"); err != nil {
				return err
			}
			for _, a := range atts {
				if x.attachments[ct.ID] == nil {
					x.attachments[ct.ID] = map[string]bool{}
				}
				x.attachments[ct.ID][a.Title] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return x, nil
}

// resolver resolves what the load resolves: pages and attachments of the extraction, and users. The ids are
// stand-ins: the conversion is used for what is unsupported and for the text (user labels are left empty).
func (x *contentIndex) resolver(space, pageID string) docmodel.ImportResolver {
	return docmodel.ImportResolver{
		User: func(string) (int64, string) { return 1, "" },
		Page: func(title, sp string) (int64, string) {
			if _, ok := x.byTitle[titleKey(cmp.Or(sp, space), title)]; ok {
				return 1, "/wiki"
			}
			return 0, ""
		},
		Attachment: func(filename, title, sp string) (int64, string) {
			owner := pageID
			if title != "" {
				owner = x.byTitle[titleKey(cmp.Or(sp, space), title)]
			}
			if x.attachments[owner][filename] {
				return 1, "/wiki"
			}
			return 0, ""
		},
		Anchor: anchorID,
	}
}

type confluenceValidation struct {
	*validation
	c                       *Confluence
	index                   *contentIndex
	spaces, pages, comments map[string]string
	files                   []sourceFile
	pageKeys                []string            // "space\x00id", for samples
	ancestors               map[string][]string // page id → ancestor ids
	readers                 map[string]*holders // view restrictions by page id
	spaceReaders            map[string]*holders // read permission by space key
	members                 map[string][]cUser  // group members by name
	people                  []string            // users the permissions and restrictions name
	pageSpace               map[string]string   // space key by page id
	pageTitle               map[string]string   // title by page id
	content                 map[string]itemAt   // place in the extraction by page id
	extractedVersions       map[string]int      // earlier versions extracted, by page id
	commentCounts           map[string]int      // by page id
	permissionsMissing      []string            // spaces whose permissions were not extracted
}

// holders are who a permission or restriction names.
type holders struct {
	users, groups []string
	anyone        bool
}

func (h *holders) has(user string, members map[string][]cUser) bool {
	if h == nil {
		return false
	}
	if h.anyone || slices.ContainsFunc(h.users, func(u string) bool { return strings.EqualFold(u, user) }) {
		return true
	}
	for _, g := range h.groups {
		if slices.ContainsFunc(members[g], func(u cUser) bool { return strings.EqualFold(u.Username, user) }) {
			return true
		}
	}
	return false
}

func (cv *confluenceValidation) run() error {
	l := cv.l
	var err error
	if cv.index, err = readContentIndex(cv.work); err != nil {
		return err
	}
	cv.spaces, cv.pages, cv.comments = map[string]string{}, map[string]string{}, map[string]string{}
	cv.ancestors, cv.readers, cv.spaceReaders = map[string][]string{}, map[string]*holders{}, map[string]*holders{}
	cv.members, cv.pageSpace, cv.pageTitle = map[string][]cUser{}, map[string]string{}, map[string]string{}
	cv.content, cv.extractedVersions, cv.commentCounts = map[string]itemAt{}, map[string]int{}, map[string]int{}
	keys := cv.c.selected()
	if len(keys) == 0 {
		if keys, err = cv.work.spaceKeys(); err != nil {
			return err
		}
	}
	for _, key := range keys {
		if err := cv.collect(key); err != nil {
			return fmt.Errorf("space %s: %w", key, err)
		}
	}
	for _, c := range []struct {
		id, label, sourceType, entityType string
		src                               map[string]string
	}{
		{"spaces", l.t("Spaces"), "space", "space", cv.spaces},
		{"pages", l.t("Pages and blog posts"), "page", "page", cv.pages},
		{"comments", l.t("Comments"), "comment", "page-comment", cv.comments},
	} {
		if _, err := cv.countCheck(c.id, c.label, c.src, c.sourceType, c.entityType); err != nil {
			return err
		}
	}
	if err := cv.fileCheck(cv.files, "page-attachment"); err != nil {
		return err
	}
	if err := cv.linkCheck("page", "page-comment"); err != nil {
		return err
	}
	if err := cv.samples(); err != nil {
		return err
	}
	return cv.permissions(keys)
}

// collect reads what the extraction of a space holds.
func (cv *confluenceValidation) collect(key string) error {
	fk := fileKey(key)
	var sp cSpace
	if ok, err := cv.work.Read(&sp, "confluence", "spaces", fk, "space.json"); err != nil || !ok {
		return err
	}
	cv.spaces[key] = sp.Name
	var perms []cSpacePermission
	ok, err := cv.work.Read(&perms, "confluence", "spaces", fk, "permissions.json")
	if err != nil {
		return err
	}
	if !ok {
		// Created without permissions: only administrators see the space, and the owner a personal one.
		cv.permissionsMissing = append(cv.permissionsMissing, key)
		h := &holders{}
		if sp.Type == "personal" {
			h.users = []string{strings.TrimPrefix(key, "~")}
		}
		cv.spaceReaders[key] = h
	} else {
		h := &holders{}
		for _, p := range perms {
			op := cmp.Or(p.Operation.TargetType, p.Operation.Target) + "/" + cmp.Or(p.Operation.OperationKey, p.Operation.Key)
			if op != "space/read" {
				continue
			}
			s := p.Subject
			switch {
			case s == nil || s.Type == "anonymous" || p.Anonymous:
				h.anyone = true
			case s.Type == "group" || s.Group != nil:
				name := cmp.Or(s.Name, groupName(s.Group), s.Identifier, s.DisplayName)
				h.groups = append(h.groups, name)
				if err := cv.groupMembers(name); err != nil {
					return err
				}
			default:
				name := s.Username
				if s.User != nil {
					name = cmp.Or(s.User.Username, cv.index.users[s.User.UserKey].Username)
				}
				if name == "" {
					name = cv.index.users[cmp.Or(s.UserKey, s.Identifier)].Username
				}
				if name != "" {
					h.users = append(h.users, name)
					cv.people = append(cv.people, name)
				}
			}
		}
		cv.spaceReaders[key] = h
	}
	return eachContent(cv.work, key, func(ct *cContent, at itemAt) error {
		cv.pages[ct.ID] = key + ": " + ct.Title
		cv.pageKeys = append(cv.pageKeys, key+"\x00"+ct.ID)
		cv.pageSpace[ct.ID], cv.pageTitle[ct.ID], cv.content[ct.ID] = key, ct.Title, at
		for _, a := range ct.Ancestors {
			cv.ancestors[ct.ID] = append(cv.ancestors[ct.ID], a.ID)
		}
		var atts []cContent
		if _, err := cv.work.Read(&atts, "confluence", "content", ct.ID, "attachments.json"); err != nil {
			return err
		}
		for _, a := range atts {
			f := sourceFile{srcID: a.ID, name: key + ": " + ct.Title + ": " + a.Title}
			if cv.work.Size("confluence", "attachments", a.ID) >= 0 {
				f.path = cv.work.Path("confluence", "attachments", a.ID)
			}
			cv.files = append(cv.files, f)
		}
		var comments []cContent
		if _, err := cv.work.Read(&comments, "confluence", "content", ct.ID, "comments.json"); err != nil {
			return err
		}
		for _, cm := range comments {
			cv.comments[cm.ID] = key + ": " + ct.Title
		}
		cv.commentCounts[ct.ID] = len(comments)
		_, _, version := ct.modifier()
		for n := 1; n < version; n++ {
			if cv.work.Exists("confluence", "content", ct.ID, "versions", strconv.Itoa(n)+".json") {
				cv.extractedVersions[ct.ID]++
			}
		}
		var rs cRestrictions
		if _, err := cv.work.Read(&rs, "confluence", "content", ct.ID, "restrictions.json"); err != nil {
			return err
		}
		if r := rs["read"]; len(r.Restrictions.User.Results)+len(r.Restrictions.Group.Results) > 0 {
			h := &holders{}
			for _, u := range r.Restrictions.User.Results {
				name := cmp.Or(u.Username, cv.index.users[u.UserKey].Username)
				h.users = append(h.users, name)
				cv.people = append(cv.people, name)
			}
			for _, g := range r.Restrictions.Group.Results {
				h.groups = append(h.groups, g.Name)
				if err := cv.groupMembers(g.Name); err != nil {
					return err
				}
			}
			cv.readers[ct.ID] = h
		}
		return nil
	})
}

func (cv *confluenceValidation) groupMembers(name string) error {
	if _, ok := cv.members[name]; ok {
		return nil
	}
	var list []cUser
	if _, err := cv.work.Read(&list, "confluence", "groups", url.PathEscape(name)+".json"); err != nil {
		return err
	}
	for i := range list {
		if list[i].Username == "" {
			list[i].Username = cv.index.users[list[i].UserKey].Username
		}
		cv.people = append(cv.people, list[i].Username)
	}
	cv.members[name] = list
	return nil
}

// samples checks the renderings, history and authors of a sample of pages.
func (cv *confluenceValidation) samples() error {
	l := cv.l
	have, err := importedIDs(cv.ctx, cv.ld.Pool, cv.source, "page", "page")
	if err != nil {
		return err
	}
	render := Check{ID: "renderings.pages", Group: "renderings", Title: l.t("A sample of pages and blog posts")}
	history := Check{ID: "history.pages", Group: "history", Title: l.t("Authors, dates and versions of a sample of pages")}
	rendered, renderBad, checked, historyBad := 0, 0, 0, 0
	for _, k := range sample(cv.pageKeys, cv.opt.Sample) {
		space, id, _ := strings.Cut(k, "\x00")
		koggeID, ok := have[id]
		if !ok {
			continue
		}
		raw, err := readItem(cv.content[id])
		if err != nil {
			return err
		}
		var ct cContent
		if err := json.Unmarshal(raw, &ct); err != nil {
			return err
		}
		f, err := pageFactsOf(cv.ctx, cv.ld.Pool, koggeID)
		if err != nil {
			return err
		}
		name := space + ": " + ct.Title
		expected, _ := docmodel.FromConfluence(ct.storage(), cv.index.resolver(space, id))
		rendered++
		if problem := cv.rendering(f.body, expected); problem != "" {
			renderBad++
			render.detail(name + ": " + problem)
		}
		checked++
		var problems []string
		user := func(u *cUser) (string, error) {
			if u == nil || u.Type == "anonymous" {
				return "", nil
			}
			n := cmp.Or(u.Username, cv.index.users[u.UserKey].Username, u.UserKey)
			m, _, err := cv.expectedUser(n, "")
			return m.Username, err
		}
		by, created := ct.creator()
		mod, _, version := ct.modifier()
		wantCreator, err := user(by)
		if err != nil {
			return err
		}
		wantModifier, err := user(mod)
		if err != nil {
			return err
		}
		if wantCreator != "" && !strings.EqualFold(wantCreator, f.creator) {
			problems = append(problems, l.t("%s is %q in Kogge, %q in the source", l.t("the creator"), f.creator, wantCreator))
		}
		if wantModifier != "" && !strings.EqualFold(wantModifier, f.updatedBy) {
			problems = append(problems, l.t("%s is %q in Kogge, %q in the source", l.t("the last editor"), f.updatedBy, wantModifier))
		}
		if t := parseTime(created); t != nil && !sameTime(f.created, t) {
			problems = append(problems, l.t("created %s in Kogge, %s in the source", f.created.UTC().Format("2006-01-02 15:04:05"),
				t.UTC().Format("2006-01-02 15:04:05")))
		}
		if f.version != max(version, 1) {
			problems = append(problems, l.t("version %d in Kogge, %d in the source", f.version, version))
		}
		if want := 1 + cv.extractedVersions[id]; f.versions != want {
			problems = append(problems, l.t("%d versions in Kogge, %d extracted", f.versions, want))
		}
		if f.comments != cv.commentCounts[id] {
			problems = append(problems, l.t("%d comments in Kogge, %d in the source", f.comments, cv.commentCounts[id]))
		}
		if len(problems) > 0 {
			historyBad++
			for _, p := range problems {
				history.detail(name + ": " + p)
			}
		}
	}
	switch {
	case rendered == 0:
		render.Status, render.Message = Warn, l.t("No page of the sample is in Kogge.")
	case renderBad > 0:
		render.Status, render.Message = Fail, l.t("%d of %d pages do not render like the source.", renderBad, rendered)
	default:
		render.Status, render.Message = Pass, l.t("%d pages are valid documents, render, keep the source's text and have no unsupported blocks beyond those the analysis reported.", rendered)
	}
	cv.add(render)
	switch {
	case checked == 0:
		history.Status, history.Message = Warn, l.t("No page of the sample is in Kogge.")
	case historyBad > 0:
		history.Status, history.Message = Fail, l.t("%d of %d pages differ from the source.", historyBad, checked)
	default:
		history.Status, history.Message = Pass, l.t("%d pages have the creator, creation date, last editor, versions and comments of the source.", checked)
	}
	cv.add(history)
	return nil
}

// permissions checks who may see a sample of the spaces and of the restricted pages.
func (cv *confluenceValidation) permissions(keys []string) error {
	l := cv.l
	slices.Sort(cv.people)
	people := slices.Compact(cv.people)
	var cases []accessCase
	for _, key := range sample(keys, cv.opt.Sample) {
		h := cv.spaceReaders[key]
		for _, u := range sample(people, cv.opt.Sample) {
			cases = append(cases, accessCase{user: u, target: key, label: l.t("the space %s", key), want: h.has(u, cv.members)})
		}
	}
	if err := cv.accessCheck("spaces", l.t("Who may see the spaces"), cases, func(p *identity.Principal, key string) bool {
		_, err := cv.ld.Wiki.SpaceAddress(cv.ctx, p, key)
		return err == nil
	}); err != nil {
		return err
	}
	if len(cv.permissionsMissing) > 0 {
		c := Check{ID: "permissions.spaces-without-permissions", Group: "permissions", Title: l.t("Spaces without extracted permissions"),
			Status: Warn, Message: l.t("The permissions of %d spaces were not extracted: only Kogge administrators see them until permissions are set.", len(cv.permissionsMissing)),
			Details: cv.permissionsMissing}
		cv.add(c)
	}
	// Pages with view restrictions, theirs or their ancestors'.
	have, err := importedIDs(cv.ctx, cv.ld.Pool, cv.source, "page", "page")
	if err != nil {
		return err
	}
	var restricted []string
	for id := range cv.pages {
		if cv.readers[id] != nil || slices.ContainsFunc(cv.ancestors[id], func(a string) bool { return cv.readers[a] != nil }) {
			restricted = append(restricted, id)
		}
	}
	slices.Sort(restricted)
	cases = nil
	for _, id := range sample(restricted, cv.opt.Sample) {
		koggeID, ok := have[id]
		if !ok {
			continue
		}
		space := cv.spaceReaders[cv.pageSpace[id]]
		for _, u := range sample(people, cv.opt.Sample) {
			want := space.has(u, cv.members)
			for _, node := range append(slices.Clone(cv.ancestors[id]), id) {
				if r := cv.readers[node]; r != nil && !r.has(u, cv.members) {
					want = false
				}
			}
			cases = append(cases, accessCase{user: u, target: strconv.FormatInt(koggeID, 10),
				label: l.t("the page %q", cv.pageSpace[id]+": "+cv.pageTitle[id]), want: want})
		}
	}
	return cv.accessCheck("pages", l.t("Who may see restricted pages"), cases, func(p *identity.Principal, id string) bool {
		n, _ := strconv.ParseInt(id, 10, 64)
		_, err := cv.ld.Wiki.PageAddress(cv.ctx, p, n)
		return err == nil
	})
}
