// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/wiki"
)

// The analysis of a Confluence extraction: spaces with their pages, blog posts, versions, comments, attachments,
// labels and restrictions, the space permissions, and the macros — converted with the load's own conversion, so
// that the macros it lists as unsupported are those the load keeps as unsupported blocks.

// unsupportedRawLimit is how much of the source an unsupported block keeps (see docmodel): longer macros are cut.
const unsupportedRawLimit = 1900

var (
	macroName    = regexp.MustCompile(`<ac:(?:structured-)?macro[^>]*\sac:name="([^"]+)"`)
	spaceKeyRefs = regexp.MustCompile(`ri:space-key="([^"]+)"`)
)

// Analyse reads the extraction (and the target installation, if given) and writes confluence-analysis.json and
// confluence-analysis.html into the work directory.
func (c *Confluence) Analyse(ctx context.Context, target *Target, language string) (*Analysis, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	ok, err := c.work.Read(&server, "confluence", "server.json")
	if err != nil {
		return nil, err
	}
	keys, err := c.work.spaceKeys()
	if err != nil {
		return nil, err
	}
	if !ok && len(keys) == 0 {
		return nil, fmt.Errorf("%s holds no Confluence extraction", c.work.Root)
	}
	a, err := newAnalysis(ctx, "confluence", strings.TrimRight(cmp.Or(server.BaseURL, c.opt.URL), "/"), c.work, language, target)
	if err != nil {
		return nil, err
	}
	if !ok {
		a.decide("extraction-unfinished", "high", a.l.t("The extraction did not finish (server.json is missing). Run it again: it resumes where it stopped."))
	}
	if sel := c.selected(); len(sel) > 0 {
		keys = sel
	}
	index, err := readContentIndex(c.work)
	if err != nil {
		return nil, err
	}
	ca := &confluenceAnalysis{analysis: a, c: c, keys: keys, index: index, users: index.users, groups: map[string]bool{}}
	if err := ca.run(); err != nil {
		return nil, err
	}
	return a.Analysis, a.finish()
}

type confluenceAnalysis struct {
	*analysis
	c      *Confluence
	keys   []string
	index  *contentIndex
	groups map[string]bool
	users  map[string]cUser // by user key
}

// cuser records a user of the content.
func (ca *confluenceAnalysis) cuser(u *cUser) {
	if u == nil || u.Type == "anonymous" {
		return
	}
	if u.Username == "" && u.UserKey != "" {
		if known, ok := ca.users[u.UserKey]; ok {
			u = &known
		}
	}
	ca.person(cmp.Or(u.Username, u.UserKey), u.DisplayName, "", nil)
}

func (ca *confluenceAnalysis) run() error {
	l := ca.l
	for _, u := range ca.users {
		ca.person(u.Username, u.DisplayName, "", nil)
	}
	extracted := map[string]bool{}
	all, err := ca.work.spaceKeys()
	if err != nil {
		return err
	}
	for _, key := range all {
		extracted[strings.ToUpper(key)] = true
	}
	counts := map[string]int{}
	var attBytes, fileBytes int64
	var files, missing int
	macros := map[string]int{}      // occurrences by name
	macroPages := map[string]int{}  // pages by name
	unsupported := map[string]int{} // by name
	cut := Decision{Code: "macro-content-cut", Severity: "medium", Message: l.t("These pages have unsupported macros with more content than Kogge keeps for an unsupported block (%d characters): their content would be cut. Replace the macros before the load, or rework the pages after it.", unsupportedRawLimit)}
	badLabels := Decision{Code: "labels-invalid", Severity: "low", Message: l.t("Labels Kogge does not accept (with spaces, commas or semicolons, or longer than 100 characters) are left out.")}
	renamed := Decision{Code: "page-renamed", Severity: "low", Message: l.t("Pages whose titles differ only in case get a number in Kogge, where titles are unique regardless of case.")}
	spaces := Table{ID: "spaces", Title: l.t("Spaces"), Columns: []string{l.t("Space"), l.t("Type"), l.t("Pages"), l.t("Blog posts"),
		l.t("Permissions"), l.t("In Kogge")}}
	for _, key := range ca.keys {
		fk := fileKey(key)
		var sp cSpace
		if ok, err := ca.work.Read(&sp, "confluence", "spaces", fk, "space.json"); err != nil {
			return err
		} else if !ok {
			ca.decide("space-not-extracted", "high", l.t("The space %s was selected but not extracted: extract it again.", key))
			continue
		}
		counts["spaces"]++
		personal := sp.Type == "personal"
		if personal {
			counts["personalSpaces"]++
		}
		pages, blogs := 0, 0
		titles := map[string]string{}
		err := eachContent(ca.work, key, func(ct *cContent, _ itemAt) error {
			where := key + ": " + keyed(ct.Title, ct.ID)
			switch {
			case ct.Type == "blogpost":
				blogs++
				counts["blogPosts"]++
			case ct.Status == "archived":
				pages++
				counts["archivedPages"]++
			default:
				pages++
				counts["pages"]++
			}
			if ct.Type == "page" {
				t := strings.ToLower(ct.Title)
				if other, ok := titles[t]; ok && other != ct.Title {
					renamed.item(key + ": " + ct.Title + " / " + other)
				}
				titles[t] = ct.Title
			}
			by, _ := ct.creator()
			ca.cuser(by)
			mod, _, version := ct.modifier()
			ca.cuser(mod)
			counts["versions"]++
			if ct.Metadata != nil && ct.Metadata.Labels != nil {
				for _, lb := range ct.Metadata.Labels.Results {
					counts["labels"]++
					if !wiki.ImportableLabel(lb.Name) {
						badLabels.item(where + ": " + lb.Name)
					}
				}
			}
			ca.storage(key, ct.ID, where, ct.storage(), macros, macroPages, unsupported, &cut)
			for n := 1; n < version; n++ {
				var v cContent
				if ok, err := ca.work.Read(&v, "confluence", "content", ct.ID, "versions", strconv.Itoa(n)+".json"); err != nil {
					return err
				} else if ok {
					counts["versions"]++
					by, _, _ := v.modifier()
					ca.cuser(by)
				}
			}
			var atts []cContent
			if _, err := ca.work.Read(&atts, "confluence", "content", ct.ID, "attachments.json"); err != nil {
				return err
			}
			for i := range atts {
				at := &atts[i]
				counts["attachments"]++
				mod, _, _ := at.modifier()
				ca.cuser(mod)
				if at.Extensions != nil {
					attBytes += at.Extensions.FileSize
				}
				if size := ca.work.Size("confluence", "attachments", at.ID); size >= 0 {
					files++
					fileBytes += size
				} else {
					missing++
				}
			}
			var comments []cContent
			if _, err := ca.work.Read(&comments, "confluence", "content", ct.ID, "comments.json"); err != nil {
				return err
			}
			for i := range comments {
				cm := &comments[i]
				counts["comments"]++
				if cm.Extensions != nil && cm.Extensions.InlineProperties != nil {
					counts["inlineComments"]++
				}
				by, _ := cm.creator()
				ca.cuser(by)
				ca.storage(key, ct.ID, where, cm.storage(), macros, macroPages, unsupported, &cut)
			}
			var rs cRestrictions
			if _, err := ca.work.Read(&rs, "confluence", "content", ct.ID, "restrictions.json"); err != nil {
				return err
			}
			restricted := false
			for op, r := range rs {
				if len(r.Restrictions.User.Results)+len(r.Restrictions.Group.Results) == 0 {
					continue
				}
				if op == "read" {
					restricted = true
				}
				for i := range r.Restrictions.User.Results {
					ca.cuser(&r.Restrictions.User.Results[i])
				}
				for _, g := range r.Restrictions.Group.Results {
					ca.groups[g.Name] = true
				}
			}
			if restricted {
				counts["restrictedPages"]++
			}
			// Links to spaces that are not part of this migration stay links to the source.
			for _, m := range spaceKeyRefs.FindAllStringSubmatch(ct.storage(), -1) {
				if !extracted[strings.ToUpper(m[1])] {
					ca.find(l.t("link"), l.t("to the space %s, which is not migrated", m[1]), where, l.t("stays a link to the source"))
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("space %s: %w", key, err)
		}
		perms, err := ca.permissions(key, sp)
		if err != nil {
			return err
		}
		kogge, class, err := ca.spaceTarget(key, personal)
		if err != nil {
			return err
		}
		spaces.add(class, 0, keyed(sp.Name, key), map[bool]string{true: l.t("personal"), false: l.t("team")}[personal],
			l.n(int64(pages)), l.n(int64(blogs)), perms, kogge)
	}
	ca.Translation = append([]Table{spaces}, ca.Translation...)
	mt := Table{ID: "macros", Title: l.t("Macros"), Intro: l.t("Known macros become Kogge nodes and macros; the others are kept as unsupported blocks with their source and parameters, so that nothing is lost but they show as such."),
		Columns: []string{l.t("Macro"), l.t("Occurrences"), l.t("Pages"), l.t("In Kogge")}}
	for _, name := range slices.SortedFunc(mapKeys(macros), func(x, y string) int { return cmp.Or(cmp.Compare(macros[y], macros[x]), cmp.Compare(x, y)) }) {
		how, class := l.t("converted"), ""
		if unsupported[name] > 0 {
			how, class = l.t("unsupported block (%d of %d)", unsupported[name], macros[name]), Warn
		}
		mt.add(class, 0, name, l.n(int64(macros[name])), l.n(int64(macroPages[name])), how)
	}
	ca.Translation = append(ca.Translation, mt)
	if err := ca.groupTable(); err != nil {
		return err
	}
	ca.decision(cut)
	ca.decision(badLabels)
	ca.decision(renamed)
	for _, k := range []struct{ key, label string }{{"spaces", l.t("Spaces")}, {"pages", l.t("Pages")}, {"blogPosts", l.t("Blog posts")},
		{"attachments", l.t("Attachments")}, {"personalSpaces", l.t("Personal spaces")}, {"archivedPages", l.t("Archived pages")},
		{"versions", l.t("Versions")}, {"comments", l.t("Comments")}, {"inlineComments", l.t("Inline comments")}, {"labels", l.t("Labels")},
		{"restrictedPages", l.t("Pages with view restrictions")}} {
		ca.count(k.key, k.label, counts[k.key])
	}
	ca.size("attachmentBytes", l.t("Attachments (as the source lists them)"), attBytes, true)
	ca.size("attachmentFiles", l.t("Attachment files extracted"), int64(files), false)
	ca.size("attachmentFileBytes", l.t("Size of the extracted attachment files"), fileBytes, true)
	if missing > 0 {
		severity, msg := "medium", l.t("%d attachment files are missing from the work directory: the load leaves them out. Extract again (the extraction resumes) or check the extraction's notes.", missing)
		if st := ca.Extraction; st != nil && st.Options["attachments"] == false {
			severity, msg = "low", l.t("Attachment files were not extracted (trial run): the load leaves the %d attachments out.", missing)
		}
		ca.decide("attachments-missing", severity, msg)
	}
	nm := Table{ID: "not-migrated", Title: l.t("Not migrated"), Columns: []string{l.t("What"), l.t("Note")}}
	for _, row := range [][2]string{
		{l.t("Page templates and blueprints"), l.t("set them up again as Kogge templates")},
		{l.t("Watchers and likes of pages"), ""},
		{l.t("Attachment versions older than the current one"), ""},
		{l.t("Space categories, themes and look and feel"), ""},
		{l.t("Data of apps (macros of apps stay unsupported blocks, except the formulas of the LaTeX Math app)"), ""},
	} {
		nm.add("muted", 0, row[0], row[1])
	}
	ca.Translation = append(ca.Translation, nm)
	if err := ca.matchUsers(); err != nil {
		return err
	}
	return ca.estimate("confluence-report.json", phase{name: "pages", items: counts["pages"] + counts["archivedPages"] + counts["blogPosts"]},
		phase{name: "attachments", bytes: fileBytes})
}

// storage converts the storage format of a page or comment as the load would, and counts its macros.
func (ca *confluenceAnalysis) storage(space, pageID, where, storage string, macros, pages, unsupported map[string]int, cut *Decision) {
	if storage == "" {
		return
	}
	seen := map[string]bool{}
	for _, m := range macroName.FindAllStringSubmatch(storage, -1) {
		macros[m[1]]++
		if !seen[m[1]] {
			seen[m[1]] = true
			pages[m[1]]++
		}
	}
	d, counts := docmodel.FromConfluence(storage, ca.index.resolver(space, pageID))
	for name, n := range counts {
		unsupported[name] += n
		for range n {
			ca.find(ca.l.t("macro"), name, where, ca.l.t("kept as an unsupported block with its source"))
		}
	}
	docmodel.Walk(d, func(n *docmodel.Node) bool {
		if n.Type == "unsupported" {
			if raw, _ := n.Attrs["raw"].(string); len(raw) >= unsupportedRawLimit {
				name, _ := n.Attrs["name"].(string)
				cut.item(where + ": " + name)
			}
		}
		return true
	})
}

// permissions lists the permissions of a space and says how the space's access is translated.
func (ca *confluenceAnalysis) permissions(key string, sp cSpace) (string, error) {
	l := ca.l
	var perms []cSpacePermission
	ok, err := ca.work.Read(&perms, "confluence", "spaces", fileKey(key), "permissions.json")
	if err != nil {
		return "", err
	}
	if !ok {
		ca.decide("no-space-permissions", "medium", l.t("The permissions of the space %s were not extracted (Confluence before version 8, or a token without administrator rights): only Kogge administrators can see the space until permissions are set in its settings.", key))
		return l.t("not extracted"), nil
	}
	byOp := map[string][]string{}
	var order []string
	admin := false
	for _, p := range perms {
		op := cmp.Or(p.Operation.TargetType, p.Operation.Target) + "/" + cmp.Or(p.Operation.OperationKey, p.Operation.Key)
		holder := ""
		s := p.Subject
		switch {
		case s == nil || s.Type == "anonymous" || p.Anonymous:
			holder = l.t("anonymous users")
		case s.Type == "group" || s.Group != nil:
			name := cmp.Or(s.Name, groupName(s.Group), s.Identifier, s.DisplayName)
			ca.groups[name] = true
			holder = l.t("group %s", name)
		default:
			u := s.User
			if u == nil {
				u = &cUser{Username: s.Username, UserKey: cmp.Or(s.UserKey, s.Identifier), DisplayName: s.DisplayName}
			}
			ca.cuser(u)
			holder = l.t("user %s", cmp.Or(u.Username, u.DisplayName, u.UserKey))
		}
		if confluencePermissions[op] == wiki.PermAdmin && (s != nil && s.Type != "anonymous" && !p.Anonymous) {
			admin = true
		}
		if _, ok := byOp[op]; !ok {
			order = append(order, op)
		}
		byOp[op] = append(byOp[op], holder)
	}
	t := Table{ID: "permissions-" + key, Title: l.t("Permissions of the space %s", key), Columns: []string{l.t("Permission"), l.t("Holders"), l.t("In Kogge")}}
	for _, op := range order {
		kogge, ok := confluencePermissions[op]
		class := ""
		if !ok {
			kogge, class = l.t("not migrated"), Warn
			ca.find(l.t("space permission"), op, key, l.t("Kogge has no such permission; the grant is left out"))
		}
		t.add(class, 0, op, strings.Join(byOp[op], ", "), kogge)
	}
	ca.Translation = append(ca.Translation, t)
	if !admin {
		ca.decide("no-space-admin", "medium", l.t("No user or group administers the space %s: only Kogge administrators can change its settings after the load.", key))
	}
	return l.t("%d grants", len(perms)), nil
}

// spaceTarget says what becomes of a space in the target.
func (ca *confluenceAnalysis) spaceTarget(key string, personal bool) (string, string, error) {
	l := ca.l
	if !personal && !spaceKeyPattern.MatchString(key) {
		ca.decide("space-key-invalid", "high", l.t("The key of the space %s is not valid in Kogge (letters and digits): the space cannot be loaded. Change the key in Confluence, or leave the space out.", key))
		return l.t("the key is not valid"), Fail, nil
	}
	if ca.target == nil {
		return l.t("a new space with the key %s", key), "", nil
	}
	ours, loaded, err := ca.legacyEntity("space", key)
	if err != nil {
		return "", "", err
	}
	if loaded {
		return l.t("loaded before: is updated"), "", nil
	}
	if personal {
		owner := strings.TrimPrefix(key, "~")
		u := ca.users[owner]
		m, found, err := identityMatch(ca.analysis, cmp.Or(u.Username, owner))
		if err != nil {
			return "", "", err
		}
		if found {
			if other, err := personalSpaceOf(ca.ctx, ca.target.tx, m); err != nil {
				return "", "", err
			} else if other != "" {
				ca.decide("personal-space-exists", "high", l.t("The owner of the personal space %s already has the personal space %s in Kogge: the space cannot be loaded.", key, other))
				return l.t("the owner has a personal space"), Fail, nil
			}
		}
		return l.t("the personal space of its owner"), "", nil
	}
	id, err := spaceID(ca.ctx, ca.target.tx, key)
	if err != nil {
		return "", "", err
	}
	if id != 0 && id != ours {
		ca.decide("key-collision", "high", l.t("Kogge has a space with the key %s that was not created by this migration: the space cannot be loaded. Rename or delete the space in Kogge, or leave the space out.", key))
		return l.t("the key is taken"), Fail, nil
	}
	return l.t("a new space with the key %s", key), "", nil
}

// groupTable lists the groups of permissions and restrictions, and what they become in Kogge.
func (ca *confluenceAnalysis) groupTable() error {
	l := ca.l
	if len(ca.groups) == 0 {
		return nil
	}
	t := Table{ID: "groups", Title: l.t("Groups"), Intro: l.t("Groups are created in Kogge by name with their members."),
		Columns: []string{l.t("Group"), l.t("Members"), l.t("In Kogge")}}
	renamed := Decision{Code: "group-renamed", Severity: "medium", Message: l.t("Kogge has groups of its own with these names: the migrated groups get other names. Merge them by hand if they belong together.")}
	for _, g := range slices.Sorted(mapKeys(ca.groups)) {
		var members []cUser
		if _, err := ca.work.Read(&members, "confluence", "groups", url.PathEscape(g)+".json"); err != nil {
			return err
		}
		for i := range members {
			ca.cuser(&members[i])
		}
		how, other, err := ca.group(g)
		if err != nil {
			return err
		}
		class := ""
		if other {
			class = Warn
			renamed.item(g + ": " + how)
		}
		t.add(class, 0, g, l.n(int64(len(members))), how)
	}
	ca.Translation = append(ca.Translation, t)
	ca.decision(renamed)
	return nil
}
