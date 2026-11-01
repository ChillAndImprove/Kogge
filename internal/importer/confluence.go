// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/rank"
	"kogge.de/kogge/internal/wiki"
)

// --- Confluence REST shapes (only what the import uses) ---

type cUser struct {
	Type        string `json:"type"`
	Username    string `json:"username"`
	UserKey     string `json:"userKey"`
	DisplayName string `json:"displayName"`
}

func (u *cUser) ref() *jUser {
	if u == nil || u.Type == "anonymous" || (u.Username == "" && u.UserKey == "") {
		return nil
	}
	return &jUser{Name: u.Username, Key: u.UserKey, DisplayName: u.DisplayName}
}

type cList[T any] struct {
	Results []T `json:"results"`
	Size    int `json:"size"`
	Links   struct {
		Next string `json:"next"`
		Base string `json:"base"`
	} `json:"_links"`
}

type cLabel struct {
	Name string `json:"name"`
}

type cContent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	History *struct {
		CreatedBy   *cUser `json:"createdBy"`
		CreatedDate string `json:"createdDate"`
	} `json:"history"`
	Version *struct {
		By        *cUser `json:"by"`
		When      string `json:"when"`
		Number    int    `json:"number"`
		Message   string `json:"message"`
		MinorEdit bool   `json:"minorEdit"`
	} `json:"version"`
	Ancestors []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"ancestors"`
	Body *struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Metadata *struct {
		Labels    *cList[cLabel] `json:"labels"`
		MediaType string         `json:"mediaType"`
		Comment   string         `json:"comment"`
	} `json:"metadata"`
	Extensions *struct {
		Position         json.RawMessage `json:"position"`
		MediaType        string          `json:"mediaType"`
		Comment          string          `json:"comment"`
		FileSize         int64           `json:"fileSize"`
		Location         string          `json:"location"`
		InlineProperties *struct {
			MarkerRef         string `json:"markerRef"`
			OriginalSelection string `json:"originalSelection"`
		} `json:"inlineProperties"`
		Resolution *struct {
			Status string `json:"status"`
		} `json:"resolution"`
	} `json:"extensions"`
	Links struct {
		Download string `json:"download"`
	} `json:"_links"`
}

func (c *cContent) creator() (*cUser, string) {
	if c.History != nil {
		return c.History.CreatedBy, c.History.CreatedDate
	}
	return nil, ""
}

func (c *cContent) modifier() (*cUser, string, int) {
	if c.Version != nil {
		return c.Version.By, c.Version.When, c.Version.Number
	}
	return nil, "", 1
}

func (c *cContent) storage() string {
	if c.Body == nil {
		return ""
	}
	return c.Body.Storage.Value
}

// position is the manual order among siblings, or -1.
func (c *cContent) position() int {
	if c.Extensions == nil {
		return -1
	}
	var n int
	if json.Unmarshal(c.Extensions.Position, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(c.Extensions.Position, &s) == nil {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return -1
}

type cSpace struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Description *struct {
		Plain struct {
			Value string `json:"value"`
		} `json:"plain"`
	} `json:"description"`
	Homepage *struct {
		ID string `json:"id"`
	} `json:"homepage"`
	Links struct {
		Base string `json:"base"`
	} `json:"_links"`
}

type cSpacePermission struct {
	Operation struct {
		TargetType   string `json:"targetType"`
		OperationKey string `json:"operationKey"`
		Target       string `json:"target"`
		Key          string `json:"key"`
	} `json:"operation"`
	Subject *struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Username    string `json:"username"`
		UserKey     string `json:"userKey"`
		DisplayName string `json:"displayName"`
		Identifier  string `json:"identifier"`
		User        *cUser `json:"user"`
		Group       *struct {
			Name string `json:"name"`
		} `json:"group"`
	} `json:"subject"`
	Anonymous bool `json:"anonymousAccess"`
}

type cRestrictions map[string]struct {
	Restrictions struct {
		User struct {
			Results []cUser `json:"results"`
		} `json:"user"`
		Group struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		} `json:"group"`
	} `json:"restrictions"`
}

// confluencePermissions maps Confluence's space permissions (target type and operation) to Kogge's.
var confluencePermissions = map[string]string{
	"space/read": wiki.PermView, "space/administer": wiki.PermAdmin, "space/export": wiki.PermExport,
	"space/restrict_content": wiki.PermRestrict, "space/delete_own": wiki.PermDeleteOwn,
	"page/create": wiki.PermEdit, "blogpost/create": wiki.PermEdit, "page/delete": wiki.PermDelete, "blogpost/delete": wiki.PermDelete,
	"page/archive": wiki.PermArchive, "comment/create": wiki.PermComment, "comment/delete": wiki.PermCommentDelete,
	"attachment/create": wiki.PermAttach, "attachment/delete": wiki.PermAttachDelete,
}

// ConfluenceOptions select what to migrate.
type ConfluenceOptions struct {
	URL      string
	Token    string
	Spaces   []string
	Workdir  string
	JiraURL  string // links to issues of this Jira become issue references
	History  bool   // also migrate earlier versions of pages
	NoFiles  bool   // leave attachment files out of the extraction (for trial runs)
	PageSize int
	// Concurrency is how many pages' details (attachments, comments, restrictions, versions) and attachment files
	// are read at a time (default 4).
	Concurrency int
	// Restart begins the extraction afresh instead of resuming an interrupted one.
	Restart bool
}

// Confluence migrates spaces from Confluence Data Center.
type Confluence struct {
	opt    ConfluenceOptions
	client *Client
	work   Workdir
	log    *slog.Logger
}

// NewConfluence prepares a Confluence migration.
func NewConfluence(opt ConfluenceOptions, log *slog.Logger) *Confluence {
	if opt.PageSize <= 0 {
		opt.PageSize = 50
	}
	return &Confluence{opt: opt, client: NewClient(opt.URL, opt.Token, log), work: Workdir{Root: opt.Workdir}, log: log}
}

func (c *Confluence) selected() []string {
	var keys []string
	for _, k := range c.opt.Spaces {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// fileKey turns a space key (personal spaces start with ~) into a file name.
func fileKey(key string) string { return url.PathEscape(key) }

// spaceKeyPattern is what Kogge accepts as the key of a team space.
var spaceKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)

func groupName(g *struct {
	Name string `json:"name"`
}) string {
	if g == nil {
		return ""
	}
	return g.Name
}

// --- loading ---

// Load reads the work directory and loads the spaces. Loads can be repeated: entities loaded before are updated.
// Spaces are read element by element: a load keeps what it needs to order the pages, not their bodies.
func (c *Confluence) Load(ctx context.Context, l Loader) (*Report, error) {
	var server struct {
		BaseURL string `json:"baseUrl"`
	}
	if ok, err := c.work.Read(&server, "confluence", "server.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%s holds no Confluence extraction", c.work.Root)
	}
	base := strings.TrimRight(cmp.Or(server.BaseURL, c.opt.URL), "/")
	ld := &confluenceLoad{Confluence: c, l: l, source: "confluence:" + base, base: base, users: map[string]cUser{},
		byTitle: map[string]string{}, attachments: map[string][]attachmentInfo{}, spaces: map[string]string{}, groups: map[string]int64{},
		jr: &journal{}}
	ld.rep = newReport(ld.source)
	ld.us = newUsers(ld.rep, ld.jr)
	ld.f = l.failures(ld.rep, ld.jr)
	if _, err := c.work.Read(&ld.users, "confluence", "users.json"); err != nil {
		return nil, err
	}
	keys := c.selected()
	all, err := c.work.spaceKeys()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		keys = all
	}
	// An index of all extracted pages, for links between pages and spaces.
	for _, key := range all {
		err := eachContent(c.work, key, func(ct *cContent, _ itemAt) error {
			ld.spaces[ct.ID] = key
			if ct.Type == "page" {
				ld.byTitle[titleKey(key, ct.Title)] = ct.ID
			}
			var atts []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}
			if _, err := c.work.Read(&atts, "confluence", "content", ct.ID, "attachments.json"); err != nil {
				return err
			}
			for _, a := range atts {
				ld.attachments[ct.ID] = append(ld.attachments[ct.ID], attachmentInfo{id: a.ID, filename: a.Title})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, key := range keys {
		c.log.Info("loading space", "space", key)
		sum, serr := ld.space(ctx, key)
		if serr != nil {
			// A space that cannot be created (its key is taken, say) is reported; the others are loaded.
			if err = ld.f.record("space", key, key, serr); err != nil {
				break
			}
		}
		if sum == nil {
			continue
		}
		ld.rep.Spaces = append(ld.rep.Spaces, *sum)
		ld.rep.Counts["spaces"]++
		ld.rep.Counts["pages"] += sum.Pages
		ld.rep.Counts["blogPosts"] += sum.BlogPosts
		ld.rep.Counts["versions"] += sum.Versions
		ld.rep.Counts["comments"] += sum.Comments
		ld.rep.Counts["attachments"] += sum.Attachments
	}
	ld.rep.Counts["users"] = len(ld.rep.UsersMatched) + len(ld.rep.Placeholders)
	if werr := ld.rep.Write(c.work, "confluence-report.json"); err == nil {
		err = werr
	}
	return ld.rep, err
}

func titleKey(space, title string) string {
	return strings.ToUpper(space) + "\x00" + strings.ToLower(title)
}

type attachmentInfo struct {
	id       string
	filename string
}

// confluenceLoad is the state of one load.
type confluenceLoad struct {
	*Confluence
	l           Loader
	source      string
	base        string
	rep         *Report
	us          *users
	jr          *journal
	f           *failures
	users       map[string]cUser            // by user key
	byTitle     map[string]string           // source page id by space and title
	attachments map[string][]attachmentInfo // by source page id
	spaces      map[string]string           // space key by source content id
	groups      map[string]int64
	// The size of the attachment files loaded, and the time it took (for estimates of later loads).
	attachmentBytes int64
	attachmentTime  time.Duration
}

// inTx runs fn in a transaction and applies cache invalidation locally after the commit. In-memory state follows
// the transaction's outcome.
func (ld *confluenceLoad) inTx(ctx context.Context, fn func(tx pgx.Tx, im *wiki.Importer) error) error {
	var im *wiki.Importer
	err := ld.f.inTx(ctx, ld.l.Pool, func(tx pgx.Tx) error {
		im = ld.l.Wiki.NewImporter(tx, ld.source)
		return fn(tx, im)
	})
	if err == nil {
		im.Done()
	}
	return err
}

// user maps a Confluence user (by user key when the name is missing).
func (ld *confluenceLoad) user(ctx context.Context, q db.Querier, u *cUser) (int64, error) {
	if u != nil && u.Username == "" && u.UserKey != "" {
		if known, ok := ld.users[u.UserKey]; ok {
			u = &known
		}
	}
	return ld.us.get(ctx, q, u.ref())
}

// group maps a group with its extracted members.
func (ld *confluenceLoad) group(ctx context.Context, tx pgx.Tx, name string) (int64, error) {
	if id, ok := ld.groups[strings.ToLower(name)]; ok {
		return id, nil
	}
	id, kname, err := identity.ImportGroup(ctx, tx, name)
	if err != nil {
		return 0, err
	}
	ld.rep.groupRenamed(name, kname)
	var members []cUser
	if _, err := ld.work.Read(&members, "confluence", "groups", url.PathEscape(name)+".json"); err != nil {
		return 0, err
	}
	for i := range members {
		uid, err := ld.user(ctx, tx, &members[i])
		if err != nil {
			return 0, err
		}
		if uid != 0 {
			if err := identity.AddMember(ctx, tx, id, uid); err != nil {
				return 0, err
			}
		}
	}
	ld.groups[strings.ToLower(name)] = id
	ld.rep.Counts["groups"]++
	ld.jr.add(func() {
		delete(ld.groups, strings.ToLower(name))
		ld.rep.Counts["groups"]--
	})
	return id, nil
}

// spaceLoad is the state of loading one space.
type spaceLoad struct {
	*confluenceLoad
	key     string
	spaceID int64
	sum     *SpaceSummary
}

// pageMeta is what a load keeps of a page or blog post while it orders the space; the body is read again from
// the work directory when the page's content is loaded.
type pageMeta struct {
	id, typ, status, title, parent string
	position                       int
	by, mod                        *cUser
	created, updated               string
	at                             itemAt
}

func (ld *confluenceLoad) space(ctx context.Context, key string) (*SpaceSummary, error) {
	sl := &spaceLoad{confluenceLoad: ld, key: key, sum: &SpaceSummary{Key: key}}
	var sp cSpace
	if ok, err := ld.work.Read(&sp, "confluence", "spaces", fileKey(key), "space.json"); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.New("the space was not extracted")
	}
	var metas []*pageMeta
	var oldest time.Time
	err := eachContent(ld.work, key, func(ct *cContent, at itemAt) error {
		m := &pageMeta{id: ct.ID, typ: ct.Type, status: ct.Status, title: ct.Title, position: ct.position(), at: at}
		m.by, m.created = ct.creator()
		m.mod, m.updated, _ = ct.modifier()
		if n := len(ct.Ancestors); n > 0 {
			m.parent = ct.Ancestors[n-1].ID
		}
		if t := parseTime(m.created); t != nil && (oldest.IsZero() || t.Before(oldest)) {
			oldest = *t
		}
		metas = append(metas, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = ld.inTx(ctx, func(tx pgx.Tx, im *wiki.Importer) error {
		in := wiki.ImportSpace{SourceID: key, Key: key, Name: sp.Name, Personal: sp.Type == "personal", Archived: sp.Status == "archived",
			Created: time.Now()}
		if sp.Description != nil {
			in.Description = sp.Description.Plain.Value
		}
		if in.Personal {
			owner, err := ld.user(ctx, tx, &cUser{Username: strings.TrimPrefix(key, "~")})
			if err != nil {
				return err
			}
			in.OwnerID, in.CreatorID = owner, owner
		} else if !spaceKeyPattern.MatchString(key) {
			return fmt.Errorf("the space key %q is not valid in Kogge (letters and digits)", key)
		}
		if !oldest.IsZero() {
			in.Created = oldest
		}
		var err error
		if sl.spaceID, err = im.Space(in); err != nil {
			return err
		}
		return sl.permissions(ctx, tx, im, in.OwnerID)
	})
	if err != nil {
		return nil, err
	}

	order := pageOrder(metas)
	p := newProgress(ld.log, "pages of "+key, len(order))
	started := time.Now()
	pageIDs := map[string]int64{}
	for start := 0; start < len(order); start += 100 {
		batch := order[start:min(start+100, len(order))]
		err := ld.inTx(ctx, func(tx pgx.Tx, _ *wiki.Importer) error {
			for _, pp := range batch {
				m := pp.meta
				ok, err := ld.f.entity(ctx, tx, m.typ, m.id, m.title, func(sp pgx.Tx) error {
					im := ld.l.Wiki.NewImporter(sp, ld.source)
					creator, err := ld.user(ctx, sp, m.by)
					if err != nil {
						return err
					}
					modifier, err := ld.user(ctx, sp, m.mod)
					if err != nil {
						return err
					}
					parent := pageIDs[pp.parent]
					if pp.parent != "" && parent == 0 {
						ld.rep.warn("%s: the page %q is at the top of the space: its parent was not loaded", key, m.title)
					}
					in := wiki.ImportPage{SourceID: m.id, SpaceID: sl.spaceID, ParentID: parent, Kind: "page", Status: m.status,
						Title: m.title, CreatorID: creator, Created: timeOr(m.created, time.Now()), UpdatedBy: cmp.Or(modifier, creator), Rank: pp.rank}
					in.Updated = timeOr(m.updated, in.Created)
					if m.typ == "blogpost" {
						in.Kind = "blog"
					}
					id, title, err := im.Page(in)
					if err != nil {
						return err
					}
					if title != m.title {
						ld.rep.warn("%s: the page %q was renamed to %q: the title is taken", key, m.title, title)
					}
					pageIDs[m.id] = id
					ld.jr.add(func() { delete(pageIDs, m.id) })
					return nil
				})
				if err != nil {
					return err
				}
				if !ok {
					p.fail()
					continue
				}
				if m.typ == "blogpost" {
					sl.sum.BlogPosts++
					ld.jr.add(func() { sl.sum.BlogPosts-- })
				} else {
					sl.sum.Pages++
					ld.jr.add(func() { sl.sum.Pages-- })
				}
			}
			return nil
		})
		if err != nil {
			return sl.sum, err
		}
	}

	// Attachments, then the content (which shows them), comments, labels, restrictions and versions; a page per
	// transaction.
	for _, pp := range order {
		m := pp.meta
		pageID, ok := pageIDs[m.id]
		if !ok {
			continue // the page itself failed
		}
		err := ld.inTx(ctx, func(tx pgx.Tx, im *wiki.Importer) error {
			raw, err := readItem(m.at)
			if err != nil {
				return err
			}
			var ct cContent
			if err := json.Unmarshal(raw, &ct); err != nil {
				return err
			}
			if ct.Type == "" {
				ct.Type = m.typ
			}
			if err := sl.loadAttachments(ctx, tx, im, &ct, pageID); err != nil {
				return err
			}
			return sl.loadContent(ctx, tx, im, &ct, pageID)
		})
		if err != nil {
			if err := ld.f.record(m.typ, m.id, m.title, err); err != nil {
				return sl.sum, err
			}
			p.fail()
			continue
		}
		p.add(1)
	}
	p.finish()
	ld.rep.time("pages", len(pageIDs), 0, time.Since(started)-ld.attachmentTime)
	ld.rep.time("attachments", 0, ld.attachmentBytes, ld.attachmentTime)
	ld.attachmentBytes, ld.attachmentTime = 0, 0
	err = ld.inTx(ctx, func(_ pgx.Tx, im *wiki.Importer) error {
		var home int64
		if sp.Homepage != nil {
			home = pageIDs[sp.Homepage.ID]
		}
		return im.FinishSpace(ctx, sl.spaceID, home)
	})
	return sl.sum, err
}

// placedPage is a page in the order of the load, with its parent and its rank among its siblings.
type placedPage struct {
	meta   *pageMeta
	parent string
	rank   string
}

// pageOrder puts the pages of a space in tree order, so that parents exist before their children, with blog
// posts after them.
func pageOrder(metas []*pageMeta) []placedPage {
	byID := map[string]bool{}
	for _, m := range metas {
		byID[m.id] = true
	}
	children := map[string][]*pageMeta{}
	var blogs []*pageMeta
	for _, m := range metas {
		if m.typ == "blogpost" {
			blogs = append(blogs, m)
			continue
		}
		parent := ""
		if m.parent != "" && byID[m.parent] {
			parent = m.parent
		}
		children[parent] = append(children[parent], m)
	}
	var order []placedPage
	queue := []string{""}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		list := children[parent]
		// Manually ordered pages first (by position), then by title, as Confluence shows them.
		slices.SortStableFunc(list, func(a, b *pageMeta) int {
			pa, pb := a.position, b.position
			switch {
			case pa >= 0 && pb >= 0 && pa != pb:
				return pa - pb
			case pa >= 0 && pb < 0:
				return -1
			case pa < 0 && pb >= 0:
				return 1
			}
			return strings.Compare(strings.ToLower(a.title), strings.ToLower(b.title))
		})
		ranks := rank.Spread(len(list))
		for i, m := range list {
			order = append(order, placedPage{meta: m, parent: parent, rank: ranks[i]})
			queue = append(queue, m.id)
		}
	}
	for _, m := range blogs {
		order = append(order, placedPage{meta: m})
	}
	return order
}

// permissions loads the space permissions; without them, only the owner of a personal space gets access.
func (sl *spaceLoad) permissions(ctx context.Context, tx pgx.Tx, im *wiki.Importer, owner int64) error {
	var perms []cSpacePermission
	ok, err := sl.work.Read(&perms, "confluence", "spaces", fileKey(sl.key), "permissions.json")
	if err != nil {
		return err
	}
	if !ok {
		sl.rep.warn("%s: the space permissions were not extracted; only Kogge administrators can see the space until permissions are set",
			sl.key)
		if owner != 0 {
			var grants []wiki.ImportGrant
			for _, p := range wiki.SpacePermissions {
				grants = append(grants, wiki.ImportGrant{Permission: p, HolderType: "user", HolderID: owner})
			}
			return im.SpacePermissions(sl.spaceID, grants)
		}
		return nil
	}
	var grants []wiki.ImportGrant
	admin := false
	for _, p := range perms {
		op := cmp.Or(p.Operation.TargetType, p.Operation.Target) + "/" + cmp.Or(p.Operation.OperationKey, p.Operation.Key)
		perm, ok := confluencePermissions[op]
		if !ok {
			sl.rep.Unsupported["space permission "+op]++
			continue
		}
		g := wiki.ImportGrant{Permission: perm}
		s := p.Subject
		switch {
		case s == nil || s.Type == "anonymous" || p.Anonymous:
			if perm == wiki.PermAdmin {
				continue
			}
			g.HolderType = "anyone"
		case s.Type == "group" || s.Group != nil:
			g.HolderType = "group"
			if g.HolderID, err = sl.group(ctx, tx, cmp.Or(s.Name, groupName(s.Group), s.Identifier, s.DisplayName)); err != nil {
				return err
			}
		default:
			u := s.User
			if u == nil {
				u = &cUser{Username: s.Username, UserKey: cmp.Or(s.UserKey, s.Identifier), DisplayName: s.DisplayName}
			}
			g.HolderType = "user"
			if g.HolderID, err = sl.user(ctx, tx, u); err != nil {
				return err
			}
			if g.HolderID == 0 {
				continue
			}
		}
		admin = admin || perm == wiki.PermAdmin
		grants = append(grants, g)
	}
	if !admin {
		sl.rep.warn("%s: no user or group administers the space", sl.key)
	}
	return im.SpacePermissions(sl.spaceID, grants)
}

func (sl *spaceLoad) loadAttachments(ctx context.Context, tx pgx.Tx, im *wiki.Importer, ct *cContent, pageID int64) error {
	var atts []cContent
	if _, err := sl.work.Read(&atts, "confluence", "content", ct.ID, "attachments.json"); err != nil {
		return err
	}
	for _, a := range atts {
		if _, ok, err := im.Find("attachment", a.ID); err != nil {
			return err
		} else if ok {
			sl.sum.Attachments++
			sl.jr.add(func() { sl.sum.Attachments-- })
			continue
		}
		f, err := os.Open(sl.work.Path("confluence", "attachments", a.ID))
		if err != nil {
			sl.rep.warn("%s: the attachment %s of %q was not extracted", sl.key, a.Title, ct.Title)
			continue
		}
		started := time.Now()
		if st, err := f.Stat(); err == nil {
			sl.attachmentBytes += st.Size()
		}
		by, when, _ := a.modifier()
		author, err := sl.user(ctx, tx, by)
		if err == nil {
			contentType, comment := "", ""
			if a.Metadata != nil {
				contentType, comment = a.Metadata.MediaType, a.Metadata.Comment
			}
			if a.Extensions != nil {
				contentType, comment = cmp.Or(contentType, a.Extensions.MediaType), cmp.Or(comment, a.Extensions.Comment)
			}
			_, err = im.Attachment(ctx, pageID, a.ID, a.Title, contentType, comment, author, timeOr(when, time.Now()), f)
		}
		f.Close()
		sl.attachmentTime += time.Since(started)
		if err != nil {
			return fmt.Errorf("attachment %s (%s): %w", a.Title, a.ID, err)
		}
		sl.sum.Attachments++
		sl.jr.add(func() { sl.sum.Attachments-- })
	}
	return nil
}

// anchorID turns an inline comment marker of Confluence into an anchor id.
func anchorID(ref string) string {
	var b strings.Builder
	b.WriteString("c")
	for _, r := range ref {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// resolver resolves references in the storage format of a page or comment.
func (sl *spaceLoad) resolver(ctx context.Context, tx pgx.Tx, im *wiki.Importer, pageSrcID string) docmodel.ImportResolver {
	pageRef := func(srcID string) (int64, string) {
		id, ok, err := im.Find("page", srcID)
		if err != nil || !ok {
			return 0, ""
		}
		u, err := im.PageURL(id)
		if err != nil {
			return 0, ""
		}
		return id, u
	}
	return docmodel.ImportResolver{
		User: func(ref string) (int64, string) {
			u := &cUser{UserKey: ref}
			if known, ok := sl.users[ref]; ok {
				u = &known
			} else {
				u.Username = ref
			}
			id, err := sl.user(ctx, tx, u)
			if err != nil || id == 0 {
				return 0, ""
			}
			return id, sl.us.label(ctx, tx, id)
		},
		Page: func(title, space string) (int64, string) {
			src, ok := sl.byTitle[titleKey(cmp.Or(space, sl.key), title)]
			if !ok {
				return 0, ""
			}
			return pageRef(src)
		},
		Attachment: func(filename, title, space string) (int64, string) {
			owner := pageSrcID
			if title != "" {
				src, ok := sl.byTitle[titleKey(cmp.Or(space, sl.key), title)]
				if !ok {
					return 0, ""
				}
				owner = src
			}
			for _, a := range sl.attachments[owner] {
				if a.filename == filename {
					if id, ok, err := im.Find("attachment", a.id); err == nil && ok {
						return id, wiki.AttachmentURL(id)
					}
				}
			}
			return 0, ""
		},
		Anchor: anchorID,
		Link:   func(href string) (string, string, int64, string) { return sl.link(href, pageRef) },
	}
}

var (
	viewPagePattern = regexp.MustCompile(`^/pages/viewpage\.action\?(?:.*&)?pageId=(\d+)`)
	spacePagePath   = regexp.MustCompile(`^/spaces/[^/]+/pages/(\d+)`)
	displayPath     = regexp.MustCompile(`^/display/([^/]+)/([^?#]+)`)
)

// link rewrites links to the migrated Confluence and the migrated Jira.
func (sl *spaceLoad) link(href string, pageRef func(string) (int64, string)) (string, string, int64, string) {
	if jira := strings.TrimRight(sl.opt.JiraURL, "/"); jira != "" {
		if k, ok := strings.CutPrefix(href, jira+"/browse/"); ok {
			if k = strings.SplitN(k, "?", 2)[0]; docmodel.IsIssueKey(k) {
				return "", k, 0, ""
			}
		}
	}
	rest, ok := strings.CutPrefix(href, sl.base)
	if !ok {
		return "", "", 0, ""
	}
	if m := viewPagePattern.FindStringSubmatch(rest); m != nil {
		if _, u := pageRef(m[1]); u != "" {
			return u, "", 0, ""
		}
	}
	if m := spacePagePath.FindStringSubmatch(rest); m != nil {
		if _, u := pageRef(m[1]); u != "" {
			return u, "", 0, ""
		}
	}
	if m := displayPath.FindStringSubmatch(rest); m != nil {
		title, err := url.PathUnescape(strings.ReplaceAll(m[2], "+", " "))
		if err == nil {
			if src, ok := sl.byTitle[titleKey(m[1], title)]; ok {
				if _, u := pageRef(src); u != "" {
					return u, "", 0, ""
				}
			}
		}
	}
	return "", "", 0, ""
}

// convert converts storage format and counts the macros that could not be converted.
func (sl *spaceLoad) convert(storage string, r docmodel.ImportResolver) *docmodel.Doc {
	d, unsupported := docmodel.FromConfluence(storage, r)
	for name, n := range unsupported {
		sl.rep.Unsupported["macro "+name] += n
		sl.jr.add(func() { sl.rep.Unsupported["macro "+name] -= n })
	}
	return d
}

func (sl *spaceLoad) loadContent(ctx context.Context, tx pgx.Tx, im *wiki.Importer, ct *cContent, pageID int64) error {
	r := sl.resolver(ctx, tx, im, ct.ID)
	mod, when, version := ct.modifier()
	author, err := sl.user(ctx, tx, mod)
	if err != nil {
		return err
	}
	message := ""
	if ct.Version != nil {
		message = ct.Version.Message
	}
	if err := im.SetBody(pageID, sl.convert(ct.storage(), r), version, author, timeOr(when, time.Now()), message); err != nil {
		return err
	}
	sl.sum.Versions++
	sl.jr.add(func() { sl.sum.Versions-- })
	// Earlier versions.
	for n := 1; n < version; n++ {
		var v cContent
		if ok, err := sl.work.Read(&v, "confluence", "content", ct.ID, "versions", strconv.Itoa(n)+".json"); err != nil {
			return err
		} else if !ok {
			continue
		}
		by, at, _ := v.modifier()
		uid, err := sl.user(ctx, tx, by)
		if err != nil {
			return err
		}
		msg, minor := "", false
		if v.Version != nil {
			msg, minor = v.Version.Message, v.Version.MinorEdit
		}
		if err := im.Version(pageID, n, cmp.Or(v.Title, ct.Title), sl.convert(v.storage(), r), uid, timeOr(at, time.Now()), msg, minor); err != nil {
			return err
		}
		sl.sum.Versions++
		sl.jr.add(func() { sl.sum.Versions-- })
	}
	// Labels.
	if ct.Metadata != nil && ct.Metadata.Labels != nil {
		var labels []string
		for _, l := range ct.Metadata.Labels.Results {
			labels = append(labels, l.Name)
		}
		rejected, err := im.Labels(pageID, labels)
		if err != nil {
			return err
		}
		for _, l := range rejected {
			sl.rep.warn("%s: the label %q of %q is not valid in Kogge", sl.key, l, ct.Title)
		}
	}
	// Restrictions: read restrictions become view restrictions (inherited by child pages), update restrictions edit
	// restrictions.
	var rs cRestrictions
	if _, err := sl.work.Read(&rs, "confluence", "content", ct.ID, "restrictions.json"); err != nil {
		return err
	}
	for op, kind := range map[string]string{"read": "view", "update": "edit"} {
		var users, groups []int64
		for i := range rs[op].Restrictions.User.Results {
			uid, err := sl.user(ctx, tx, &rs[op].Restrictions.User.Results[i])
			if err != nil {
				return err
			}
			if uid != 0 {
				users = append(users, uid)
			}
		}
		for _, g := range rs[op].Restrictions.Group.Results {
			gid, err := sl.group(ctx, tx, g.Name)
			if err != nil {
				return err
			}
			groups = append(groups, gid)
		}
		restricted := len(rs[op].Restrictions.User.Results)+len(rs[op].Restrictions.Group.Results) > 0
		if restricted && len(users)+len(groups) == 0 {
			sl.rep.warn("%s: the restriction of %q names no known user or group; it is kept for nobody", sl.key, ct.Title)
		}
		if err := im.Restrictions(pageID, kind, users, groups); err != nil {
			return err
		}
	}
	// Comments: parents first.
	var comments []*cContent
	if _, err := sl.work.Read(&comments, "confluence", "content", ct.ID, "comments.json"); err != nil {
		return err
	}
	slices.SortStableFunc(comments, func(a, b *cContent) int { return len(a.Ancestors) - len(b.Ancestors) })
	for _, cm := range comments {
		by, created := cm.creator()
		mod, updated, _ := cm.modifier()
		uid, err := sl.user(ctx, tx, cmp.Or(by, mod))
		if err != nil {
			return err
		}
		in := wiki.ImportComment{SourceID: cm.ID, PageID: pageID, AuthorID: uid, Body: sl.convert(cm.storage(), r),
			Created: timeOr(created, time.Now())}
		in.Updated = timeOr(updated, in.Created)
		for i := len(cm.Ancestors) - 1; i >= 0; i-- {
			if a := cm.Ancestors[i]; a.Type == "comment" || a.Type == "" {
				if id, ok, err := im.Find("comment", a.ID); err == nil && ok {
					in.ParentID = id
					break
				}
			}
		}
		if e := cm.Extensions; e != nil {
			if e.InlineProperties != nil && e.InlineProperties.MarkerRef != "" {
				in.Anchor = anchorID(e.InlineProperties.MarkerRef)
				in.AnchorText = e.InlineProperties.OriginalSelection
			}
			in.Resolved = e.Resolution != nil && e.Resolution.Status == "resolved"
		}
		if _, err := im.Comment(in); err != nil {
			return err
		}
		sl.sum.Comments++
		sl.jr.add(func() { sl.sum.Comments-- })
	}
	return nil
}
