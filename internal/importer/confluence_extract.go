// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"
)

// The extraction of Confluence. The content of a space is listed in pages of the source's
// answers, each stored as a chunk of its own (spaces/KEY/page-current/00000.json, …) together with the details of
// its pages — attachments, comments, restrictions and earlier versions —, several pages at a time. A checkpoint
// after every chunk lets an interrupted extraction resume there. The users and groups the content names are
// collected from the stored files at the end.

var userKeyPattern = regexp.MustCompile(`ri:userkey="([^"]+)"`)

// contentKind is a listing of a space: pages, archived pages, blog posts.
type contentKind struct{ typ, status string }

func (k contentKind) name() string { return k.typ + "-" + k.status }

var contentKinds = []contentKind{{"page", "current"}, {"page", "archived"}, {"blogpost", "current"}}

type confluenceExtraction struct {
	*Confluence
	cp *checkpoint
}

// list fetches all pages of a listing, following the next links.
func (c *Confluence) list(ctx context.Context, path string) ([]json.RawMessage, string, error) {
	var all []json.RawMessage
	base := ""
	for path != "" {
		raw, err := c.client.GetJSON(ctx, path)
		if err != nil {
			return nil, "", err
		}
		var page cList[json.RawMessage]
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, "", err
		}
		all = append(all, page.Results...)
		base = cmp.Or(base, page.Links.Base)
		path = nextPath(page, base)
	}
	return all, base, nil
}

// nextPath is the address of the next page of a listing, below the base URL; empty after the last page.
func nextPath(page cList[json.RawMessage], base string) string {
	next := page.Links.Next
	if len(page.Results) == 0 {
		return ""
	}
	if next != "" && !strings.HasPrefix(next, "/") {
		// An absolute link: keep its path below the base URL.
		if u, err := url.Parse(next); err == nil {
			next = u.RequestURI()
			if b, err := url.Parse(base); err == nil && b.Path != "" {
				next = strings.TrimPrefix(next, strings.TrimRight(b.Path, "/"))
			}
		}
	} else if next != "" {
		if b, err := url.Parse(base); err == nil && b.Path != "" && b.Path != "/" {
			next = strings.TrimPrefix(next, strings.TrimRight(b.Path, "/"))
		}
	}
	return next
}

func (c *Confluence) concurrency() int { return max(c.opt.Concurrency, 1) }

func (c *Confluence) save(ctx context.Context, path string, parts ...string) (json.RawMessage, error) {
	b, err := c.client.GetJSON(ctx, path)
	if err != nil {
		return nil, err
	}
	return b, c.work.Write(b, parts...)
}

// Extract reads the selected spaces (or all) through the REST API into the work directory. An interrupted
// extraction resumes where it stopped (unless Restart is set).
func (c *Confluence) Extract(ctx context.Context) error {
	if c.opt.Concurrency <= 0 {
		c.opt.Concurrency = 4
	}
	cp, err := openCheckpoint(c.work, "confluence", c.opt.Restart, map[string]any{"attachments": !c.opt.NoFiles, "history": c.opt.History},
		c.log)
	if err != nil {
		return err
	}
	defer cp.close()
	x := &confluenceExtraction{Confluence: c, cp: cp}
	keys := c.selected()
	if len(keys) == 0 {
		err := cp.part("spaces", func() error {
			spaces, _, err := c.list(ctx, "/rest/api/space?limit=100")
			if err != nil {
				return err
			}
			all := []string{}
			for _, raw := range spaces {
				var sp cSpace
				if err := json.Unmarshal(raw, &sp); err != nil {
					return err
				}
				all = append(all, sp.Key)
			}
			b, _ := json.Marshal(all)
			return c.work.Write(b, "confluence", "spaces.json")
		})
		if err != nil {
			return err
		}
		if _, err := c.work.Read(&keys, "confluence", "spaces.json"); err != nil {
			return err
		}
	}
	for _, key := range keys {
		if err := cp.part("space/"+key, func() error { return x.space(ctx, key) }); err != nil {
			return fmt.Errorf("space %s: %w", key, err)
		}
	}
	return x.people(ctx, keys)
}

// people stores the users the content names (users of earlier extractions are kept) and the members of the
// groups in permissions and restrictions.
func (x *confluenceExtraction) people(ctx context.Context, keys []string) error {
	refs, err := collectConfluence(x.work, keys)
	if err != nil {
		return err
	}
	known := map[string]cUser{}
	if _, err := x.work.Read(&known, "confluence", "users.json"); err != nil {
		return err
	}
	for k, u := range refs.users {
		known[k] = u
	}
	// Users named only in content (mentions).
	for _, key := range sortedKeys(refs.mentions) {
		if _, ok := known[key]; ok {
			continue
		}
		raw, err := x.client.GetJSON(ctx, "/rest/api/user?"+query(map[string]string{"key": key}))
		if err != nil {
			x.log.Warn("unknown user in content", "key", key, "error", err)
			continue
		}
		var u cUser
		if json.Unmarshal(raw, &u) == nil && u.UserKey != "" && u.Username != "" {
			known[u.UserKey] = u
		}
	}
	b, _ := json.Marshal(known)
	if err := x.work.Write(b, "confluence", "users.json"); err != nil {
		return err
	}
	for _, g := range sortedKeys(refs.groups) {
		err := x.cp.part("group/"+g, func() error {
			members, _, err := x.list(ctx, "/rest/api/group/"+url.PathEscape(g)+"/member?limit=200")
			if err != nil {
				return fmt.Errorf("members of group %s: %w", g, err)
			}
			if members == nil {
				members = []json.RawMessage{}
			}
			b, _ := json.Marshal(members)
			return x.work.Write(b, "confluence", "groups", url.PathEscape(g)+".json")
		})
		if err != nil {
			return err
		}
	}
	server := map[string]string{"baseUrl": cmp.Or(refs.base, strings.TrimRight(x.opt.URL, "/"))}
	b, _ = json.Marshal(server)
	if err := x.work.Write(b, "confluence", "server.json"); err != nil {
		return err
	}
	return x.cp.finish()
}

func (x *confluenceExtraction) space(ctx context.Context, key string) error {
	x.log.Info("extracting space", "space", key)
	fk := fileKey(key)
	err := x.cp.part("space/"+key+"/meta", func() error {
		if _, err := x.save(ctx, "/rest/api/space/"+url.PathEscape(key)+"?expand=description.plain,homepage", "confluence", "spaces", fk,
			"space.json"); err != nil {
			return err
		}
		// Space permissions (Confluence 8 and later; reading them needs an administrator's token).
		if _, err := x.save(ctx, "/rest/api/space/"+url.PathEscape(key)+"/permissions", "confluence", "spaces", fk, "permissions.json"); err != nil {
			x.log.Warn("the space permissions could not be read; the space will only be visible to Kogge administrators", "space", key,
				"error", err)
			if err := os.Remove(x.work.Path("confluence", "spaces", fk, "permissions.json")); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, kind := range contentKinds {
		if err := x.listing(ctx, key, kind); err != nil {
			return fmt.Errorf("%s: %w", kind.name(), err)
		}
	}
	return nil
}

func chunkFile(n int) string { return fmt.Sprintf("%05d.json", n) }

// listing reads a listing of a space chunk by chunk, each with the details of its pages. The chunks of an
// interrupted extraction are complete up to the first one not marked; each mark holds the address of the next.
func (x *confluenceExtraction) listing(ctx context.Context, key string, kind contentKind) error {
	fk := fileKey(key)
	prefix := "space/" + key + "/" + kind.name() + "/"
	dir := []string{"confluence", "spaces", fk, kind.name()}
	path := "/rest/api/content?" + query(map[string]string{"spaceKey": key, "type": kind.typ, "status": kind.status,
		"expand": "body.storage,version,ancestors,history,metadata.labels,extensions.position", "limit": strconv.Itoa(x.opt.PageSize)})
	n := 0
	for {
		v, ok := x.cp.isDone(prefix + strconv.Itoa(n))
		if !ok {
			break
		}
		path = v
		n++
	}
	if n > 0 && path == "" {
		return nil // complete
	}
	if n == 0 {
		// A new listing replaces the one of an earlier extraction (also its single file of older versions).
		if err := os.RemoveAll(x.work.Path(dir...)); err != nil {
			return err
		}
		if err := os.Remove(x.work.Path("confluence", "spaces", fk, kind.name()+".json")); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		for _, name := range x.work.List(dir...) {
			var i int
			if _, err := fmt.Sscanf(name, "%05d.json", &i); err == nil && i >= n {
				if err := os.Remove(x.work.Path(append(dir, name)...)); err != nil {
					return err
				}
			}
		}
	}
	p := newProgress(x.log, kind.name()+" of "+key, 0)
	if n > 0 {
		done, err := countItems(x.work.listingFiles(dir...))
		if err != nil {
			return err
		}
		p.done = done
	}
	defer p.finish()
	for path != "" {
		raw, err := x.client.GetJSON(ctx, path)
		var se *StatusError
		if kind.status == "archived" && n == 0 && errors.As(err, &se) && se.Status < 500 {
			return nil // archived pages need Confluence 7.10 or later
		}
		var page cList[json.RawMessage]
		var next string
		switch {
		case serverError(err):
			// One broken page (its content or history fails in the source) breaks the whole chunk: read it page
			// by page.
			x.log.Warn("a part of the listing cannot be read; reading it page by page", "space", key, "listing", kind.name(), "error", err)
			if page.Results, next, err = x.listingOneByOne(ctx, key, kind, path, err); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if err := json.Unmarshal(raw, &page); err != nil {
				return err
			}
			next = nextPath(page, page.Links.Base)
		}
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(x.concurrency())
		for _, item := range page.Results {
			g.Go(func() error {
				var ct cContent
				if err := json.Unmarshal(item, &ct); err != nil {
					return err
				}
				if err := x.content(gctx, &ct); err != nil {
					return fmt.Errorf("%s %q (%s): %w", kind.typ, ct.Title, ct.ID, err)
				}
				p.add(1)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
		results := page.Results
		if results == nil {
			results = []json.RawMessage{}
		}
		b, _ := json.Marshal(results)
		if err := x.work.Write(b, append(dir, chunkFile(n))...); err != nil {
			return err
		}
		if err := x.cp.markDone(prefix+strconv.Itoa(n), next); err != nil {
			return err
		}
		n++
		path = next
	}
	return nil
}

// listingOneByOne reads a chunk of a listing that the source cannot answer page by page, as the extraction of
// Jira does with its issues: a page that fails is noted and left out. When the source fails as a whole, or no page
// of the chunk can be told, the extraction stops (to resume later) instead. It returns the pages and the address
// of the next chunk ("" after the last).
func (x *confluenceExtraction) listingOneByOne(ctx context.Context, key string, kind contentKind, path string, failure error) (
	[]json.RawMessage, string, error) {
	u, err := url.Parse(path)
	if err != nil {
		return nil, "", err
	}
	q := u.Query()
	start, _ := strconv.Atoi(q.Get("start"))
	size, _ := strconv.Atoi(q.Get("limit"))
	size = cmp.Or(max(size, 0), x.opt.PageSize, 25)
	expand := q.Get("expand")
	at := func(ctx context.Context, i int, expand string) (cList[json.RawMessage], error) {
		q.Set("start", strconv.Itoa(i))
		q.Set("limit", "1")
		q.Set("expand", expand)
		u.RawQuery = q.Encode()
		var page cList[json.RawMessage]
		raw, err := x.client.GetJSON(ctx, u.RequestURI())
		if err == nil {
			err = json.Unmarshal(raw, &page)
		}
		return page, err
	}
	few := fewAttempts(ctx)
	items := []json.RawMessage{}
	var notes []ExtractionNote
	var unnamed []int // positions of pages that could not be named either
	told, end := false, false
	for i := start; i < start+size && !end; i++ {
		page, err := at(few, i, expand)
		if serverError(err) {
			if !x.client.brokenItem(ctx, err, "/rest/api/space/"+url.PathEscape(key)) {
				return nil, "", err
			}
			// Which page it is: its title alone can be read.
			one, terr := at(few, i, "")
			switch {
			case terr == nil && len(one.Results) == 0:
				end = true
			case terr == nil && len(one.Results) == 1:
				var ct cContent
				_ = json.Unmarshal(one.Results[0], &ct)
				told, end = true, one.Links.Next == ""
				notes = append(notes, ExtractionNote{Kind: kind.typ, ID: ct.ID, Name: cmp.Or(ct.Title, key+" #"+strconv.Itoa(i+1)),
					Note: err.Error()})
			default:
				failure = err
				unnamed = append(unnamed, i)
			}
			continue
		}
		if err != nil {
			return nil, "", err
		}
		told = true
		items = append(items, page.Results...)
		end = len(page.Results) == 0 || page.Links.Next == ""
	}
	if !told {
		// Not one page of the chunk can be told: which ones are missing is unknown.
		return nil, "", failure
	}
	for _, i := range unnamed {
		notes = append(notes, ExtractionNote{Kind: kind.typ, Name: key + " #" + strconv.Itoa(i+1), Note: failure.Error()})
	}
	for _, n := range notes {
		x.log.Warn("a page cannot be read; it is left out", "space", key, "page", n.Name, "id", n.ID, "error", n.Note)
		if err := x.cp.note(n); err != nil {
			return nil, "", err
		}
	}
	if end {
		return items, "", nil
	}
	q.Set("start", strconv.Itoa(start+size))
	q.Set("limit", strconv.Itoa(size))
	q.Set("expand", expand)
	u.RawQuery = q.Encode()
	return items, u.RequestURI(), nil
}

// attachmentMeta records which version of an attachment the extracted file is.
type attachmentMeta struct {
	Version int   `json:"version"`
	Size    int64 `json:"size"`
}

// attachmentCurrent reports whether the file of an attachment is the version the source lists, downloaded
// completely. Files of earlier extractions without a record count when their size is right.
func (x *confluenceExtraction) attachmentCurrent(a *cContent) bool {
	_, _, version := a.modifier()
	var size int64 = -1
	if a.Extensions != nil && a.Extensions.FileSize > 0 {
		size = a.Extensions.FileSize
	}
	have := x.work.Size("confluence", "attachments", a.ID)
	if have < 0 || (size >= 0 && have != size) {
		return false
	}
	var meta attachmentMeta
	if ok, err := x.work.Read(&meta, "confluence", "attachments", a.ID+".meta"); err == nil && ok {
		return meta.Version == version
	}
	b, _ := json.Marshal(attachmentMeta{Version: version, Size: have})
	return x.work.Write(b, "confluence", "attachments", a.ID+".meta") == nil
}

// content reads the attachments, comments, restrictions and (optionally) earlier versions of a page.
func (x *confluenceExtraction) content(ctx context.Context, ct *cContent) error {
	id := url.PathEscape(ct.ID)
	atts, _, err := x.list(ctx, "/rest/api/content/"+id+"/child/attachment?limit=100&expand=version,metadata,extensions")
	if err != nil {
		return err
	}
	if atts == nil {
		atts = []json.RawMessage{}
	}
	b, _ := json.Marshal(atts)
	if err := x.work.Write(b, "confluence", "content", ct.ID, "attachments.json"); err != nil {
		return err
	}
	for _, raw := range atts {
		var a cContent
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if x.opt.NoFiles || a.Links.Download == "" || x.attachmentCurrent(&a) {
			continue
		}
		var size int64 = -1
		if a.Extensions != nil {
			size = sizeOrUnknown(a.Extensions.FileSize)
		}
		err := x.client.Download(ctx, a.Links.Download, x.work.Path("confluence", "attachments", a.ID), size)
		if gone(err) {
			if err := x.cp.note(ExtractionNote{Kind: "attachment", ID: a.ID, Name: ct.Title + ": " + a.Title, Note: err.Error()}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("attachment %s: %w", a.Title, err)
		}
		_, _, version := a.modifier()
		b, _ := json.Marshal(attachmentMeta{Version: version, Size: x.work.Size("confluence", "attachments", a.ID)})
		if err := x.work.Write(b, "confluence", "attachments", a.ID+".meta"); err != nil {
			return err
		}
	}
	comments, _, err := x.list(ctx, "/rest/api/content/"+id+"/child/comment?"+query(map[string]string{"depth": "all", "limit": "100",
		"expand": "body.storage,version,history,ancestors,extensions.inlineProperties,extensions.resolution"}))
	if err != nil {
		return err
	}
	if comments == nil {
		comments = []json.RawMessage{}
	}
	b, _ = json.Marshal(comments)
	if err := x.work.Write(b, "confluence", "content", ct.ID, "comments.json"); err != nil {
		return err
	}
	if _, err := x.save(ctx, "/rest/api/content/"+id+"/restriction/byOperation?expand=restrictions.user,restrictions.group",
		"confluence", "content", ct.ID, "restrictions.json"); err != nil {
		return err
	}
	if x.opt.History {
		_, _, version := ct.modifier()
		for n := 1; n < version; n++ {
			// Earlier versions do not change: those extracted before are kept.
			if x.work.Exists("confluence", "content", ct.ID, "versions", strconv.Itoa(n)+".json") {
				continue
			}
			if _, err := x.save(ctx, "/rest/api/content/"+id+"?"+query(map[string]string{"status": "historical", "version": strconv.Itoa(n),
				"expand": "body.storage,version"}), "confluence", "content", ct.ID, "versions", strconv.Itoa(n)+".json"); err != nil {
				return fmt.Errorf("version %d: %w", n, err)
			}
		}
	}
	return nil
}

// confluenceRefs are the users and groups the extracted content of spaces names.
type confluenceRefs struct {
	users    map[string]cUser // by user key
	mentions map[string]bool  // user keys named in content
	groups   map[string]bool
	base     string
}

func (r *confluenceRefs) user(u *cUser) {
	if u != nil && u.UserKey != "" && u.Username != "" {
		r.users[u.UserKey] = *u
	}
}

func (r *confluenceRefs) storage(s string) {
	for _, m := range userKeyPattern.FindAllStringSubmatch(s, -1) {
		r.mentions[m[1]] = true
	}
}

// collectConfluence reads the users and groups that the stored content of the spaces names.
func collectConfluence(w Workdir, keys []string) (*confluenceRefs, error) {
	r := &confluenceRefs{users: map[string]cUser{}, mentions: map[string]bool{}, groups: map[string]bool{}}
	for _, key := range keys {
		var sp cSpace
		if _, err := w.Read(&sp, "confluence", "spaces", fileKey(key), "space.json"); err != nil {
			return nil, err
		}
		r.base = cmp.Or(r.base, strings.TrimRight(sp.Links.Base, "/"))
		var perms []cSpacePermission
		if _, err := w.Read(&perms, "confluence", "spaces", fileKey(key), "permissions.json"); err != nil {
			return nil, err
		}
		for _, p := range perms {
			if s := p.Subject; s != nil && (s.Type == "group" || s.Group != nil) {
				r.groups[cmp.Or(s.Name, groupName(s.Group), s.Identifier, s.DisplayName)] = true
			}
		}
		err := eachContent(w, key, func(ct *cContent, _ itemAt) error {
			by, _ := ct.creator()
			r.user(by)
			mod, _, version := ct.modifier()
			r.user(mod)
			r.storage(ct.storage())
			var atts, comments []cContent
			if _, err := w.Read(&atts, "confluence", "content", ct.ID, "attachments.json"); err != nil {
				return err
			}
			for i := range atts {
				mod, _, _ := atts[i].modifier()
				r.user(mod)
			}
			if _, err := w.Read(&comments, "confluence", "content", ct.ID, "comments.json"); err != nil {
				return err
			}
			for i := range comments {
				by, _ := comments[i].creator()
				r.user(by)
				r.storage(comments[i].storage())
			}
			var rs cRestrictions
			if _, err := w.Read(&rs, "confluence", "content", ct.ID, "restrictions.json"); err != nil {
				return err
			}
			for _, op := range rs {
				for i := range op.Restrictions.User.Results {
					r.user(&op.Restrictions.User.Results[i])
				}
				for _, g := range op.Restrictions.Group.Results {
					r.groups[g.Name] = true
				}
			}
			for n := 1; n < version; n++ {
				var v cContent
				if ok, err := w.Read(&v, "confluence", "content", ct.ID, "versions", strconv.Itoa(n)+".json"); err != nil {
					return err
				} else if ok {
					by, _, _ := v.modifier()
					r.user(by)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

// eachContent calls fn with every extracted page and blog post of a space, listing by listing (pages, archived
// pages, blog posts), with its place in the work directory.
func eachContent(w Workdir, key string, fn func(ct *cContent, at itemAt) error) error {
	for _, kind := range contentKinds {
		err := eachItem(w.listingFiles("confluence", "spaces", fileKey(key), kind.name()), func(raw json.RawMessage, at itemAt) error {
			var ct cContent
			if err := json.Unmarshal(raw, &ct); err != nil {
				return fmt.Errorf("%s: %w", at.file, err)
			}
			if ct.Type == "" {
				ct.Type = kind.typ
			}
			return fn(&ct, at)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// spaceKeys lists the extracted spaces.
func (w Workdir) spaceKeys() ([]string, error) {
	var keys []string
	for _, name := range w.List("confluence", "spaces") {
		key, err := url.PathUnescape(name)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}
