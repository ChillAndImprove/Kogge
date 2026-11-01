// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/notify"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/rank"
)

// Page states.
const (
	StatusDraft    = "draft"
	StatusCurrent  = "current"
	StatusTrashed  = "trashed"
	StatusArchived = "archived"
)

type pageRow struct {
	ID               int64
	SpaceID          int64
	ParentID         int64
	Kind             string
	Status           string
	Title            string
	Body             *docmodel.Doc
	Rank             string
	Version          int
	CreatorID        int64
	CreatedAt        time.Time
	UpdatedBy        int64
	UpdatedAt        time.Time
	PublishedAt      *time.Time
	ViewRestrictedBy []int64
}

const pageColumns = `p.id, p.space_id, coalesce(p.parent_id, 0), p.kind, p.status, p.title, p.body, p.rank, p.version,
	coalesce(p.creator_id, 0), p.created_at, coalesce(p.updated_by, 0), p.updated_at, p.published_at, p.view_restricted_by`

func scanPage(row pgx.Row) (*pageRow, error) {
	var pg pageRow
	var body []byte
	if err := row.Scan(&pg.ID, &pg.SpaceID, &pg.ParentID, &pg.Kind, &pg.Status, &pg.Title, &body, &pg.Rank, &pg.Version, &pg.CreatorID,
		&pg.CreatedAt, &pg.UpdatedBy, &pg.UpdatedAt, &pg.PublishedAt, &pg.ViewRestrictedBy); err != nil {
		return nil, err
	}
	pg.Body = &docmodel.Doc{}
	if err := json.Unmarshal(body, pg.Body); err != nil {
		return nil, err
	}
	return &pg, nil
}

func loadPage(ctx context.Context, q db.Querier, id int64, forUpdate bool) (*pageRow, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	pg, err := scanPage(q.QueryRow(ctx, "SELECT "+pageColumns+" FROM pages p WHERE p.id = $1"+lock, id))
	if db.IsNoRows(err) {
		return nil, notFoundPage()
	}
	return pg, err
}

// readablePage loads a page the principal may read. Drafts are visible to their creator only; trashed pages to
// those who may delete pages of the space.
func (s *Service) readablePage(ctx context.Context, q db.Querier, a *Access, id int64, forUpdate bool) (*pageRow, error) {
	pg, err := loadPage(ctx, q, id, forUpdate)
	if err != nil {
		return nil, err
	}
	ok, err := s.canView(ctx, q, a, pg)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFoundPage()
	}
	switch pg.Status {
	case StatusDraft:
		if pg.CreatorID != a.userID || a.userID == 0 {
			return nil, notFoundPage()
		}
	case StatusTrashed:
		if !a.Can(pg.SpaceID, PermDelete) && !(a.Can(pg.SpaceID, PermDeleteOwn) && pg.CreatorID == a.userID) {
			return nil, notFoundPage()
		}
	}
	return pg, nil
}

// SpaceRef is the short form of a space.
type SpaceRef struct {
	ID   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// PageRef is the short form of a page.
type PageRef struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Kind        string `json:"kind" enum:"page,blog"`
	SpaceKey    string `json:"spaceKey"`
	HasChildren bool   `json:"hasChildren"`
	Restricted  bool   `json:"restricted"`
	Status      string `json:"status" enum:"draft,current,trashed,archived"`
}

// PagePermissions says what the caller may do with a page.
type PagePermissions struct {
	Edit     bool `json:"edit"`
	Delete   bool `json:"delete"`
	Archive  bool `json:"archive"`
	Comment  bool `json:"comment"`
	Attach   bool `json:"attach"`
	Restrict bool `json:"restrict"`
	Export   bool `json:"export"`
	Admin    bool `json:"admin"`
}

// DraftInfo describes the caller's unpublished changes.
type DraftInfo struct {
	Updated     time.Time `json:"updated"`
	BaseVersion int       `json:"baseVersion"`
}

// Page is a page or blog post with its current version.
type Page struct {
	ID          int64             `json:"id"`
	Space       SpaceRef          `json:"space"`
	ParentID    int64             `json:"parentId,omitempty"`
	Kind        string            `json:"kind" enum:"page,blog"`
	Status      string            `json:"status" enum:"draft,current,trashed,archived"`
	Title       string            `json:"title"`
	Body        *docmodel.Doc     `json:"body"`
	Version     int               `json:"version" doc:"Number of the current version, 0 for unpublished drafts"`
	Ancestors   []PageRef         `json:"ancestors"`
	Labels      []string          `json:"labels"`
	Creator     *identity.UserRef `json:"creator,omitempty"`
	Created     time.Time         `json:"created"`
	UpdatedBy   *identity.UserRef `json:"updatedBy,omitempty"`
	Updated     time.Time         `json:"updated"`
	Published   *time.Time        `json:"published,omitempty"`
	Likes       int               `json:"likes"`
	Liked       bool              `json:"liked"`
	Watching    bool              `json:"watching"`
	Children    int               `json:"children"`
	Restricted  bool              `json:"restricted" doc:"The page or an ancestor has view restrictions"`
	EditLocked  bool              `json:"editLocked" doc:"The page has edit restrictions"`
	Draft       *DraftInfo        `json:"draft,omitempty"`
	Permissions PagePermissions   `json:"permissions"`
	IsHome      bool              `json:"isHome"`
}

func (s *Service) pagePermissions(ctx context.Context, q db.Querier, a *Access, pg *pageRow) (PagePermissions, error) {
	// Edit restrictions protect a page from every change (audit L19); archived and trashed pages do not change.
	passes, err := s.passesEditRestrictions(ctx, q, a, pg)
	if err != nil {
		return PagePermissions{}, err
	}
	edit := passes && a.Can(pg.SpaceID, PermEdit)
	open := pg.Status != StatusTrashed && pg.Status != StatusArchived
	own := pg.CreatorID == a.userID && a.userID != 0
	return PagePermissions{
		Edit:     edit && open,
		Delete:   passes && (a.Can(pg.SpaceID, PermDelete) || (own && a.Can(pg.SpaceID, PermDeleteOwn))),
		Archive:  passes && a.Can(pg.SpaceID, PermArchive),
		Comment:  a.Can(pg.SpaceID, PermComment) && pg.Status == StatusCurrent,
		Attach:   a.Can(pg.SpaceID, PermAttach) && edit && open,
		Restrict: a.Can(pg.SpaceID, PermRestrict),
		Export:   a.Can(pg.SpaceID, PermExport),
		Admin:    a.Can(pg.SpaceID, PermAdmin),
	}, nil
}

// ancestors returns the pages above a page, the top first, with their status and view restrictions: the page tree
// shows the way to the page with them where its siblings are not loaded yet. Who may see a page may see all of
// them, as view restrictions apply to the pages below.
func (s *Service) ancestors(ctx context.Context, q db.Querier, pg *pageRow) ([]PageRef, error) {
	rows, err := q.Query(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, title, kind, 0 AS depth FROM pages WHERE id = $1
			UNION ALL SELECT p.id, p.parent_id, p.title, p.kind, up.depth + 1 FROM pages p JOIN up ON p.id = up.parent_id WHERE up.depth < 100)
		SELECT up.id, up.title, up.kind, s.key, pg.status, cardinality(pg.view_restricted_by) > 0 FROM up
		JOIN pages pg ON pg.id = up.id JOIN spaces s ON s.id = pg.space_id
		WHERE up.id <> $1 ORDER BY up.depth DESC`, pg.ID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PageRef, error) {
		var r PageRef
		err := row.Scan(&r.ID, &r.Title, &r.Kind, &r.SpaceKey, &r.Status, &r.Restricted)
		return r, err
	})
	return nonNil(out), err
}

func (s *Service) toPage(ctx context.Context, q db.Querier, a *Access, pg *pageRow) (*Page, error) {
	sp, err := spaceByID(ctx, q, pg.SpaceID)
	if err != nil {
		return nil, err
	}
	out := &Page{ID: pg.ID, Space: SpaceRef{ID: sp.ID, Key: sp.Key, Name: sp.Name}, ParentID: pg.ParentID, Kind: pg.Kind,
		Status: pg.Status, Title: pg.Title, Body: pg.Body, Version: pg.Version, Created: pg.CreatedAt, Updated: pg.UpdatedAt,
		Published: pg.PublishedAt, Restricted: len(pg.ViewRestrictedBy) > 0, IsHome: sp.HomePageID == pg.ID, Labels: []string{}}
	if out.Ancestors, err = s.ancestors(ctx, q, pg); err != nil {
		return nil, err
	}
	refs, err := identity.GetUserRefs(ctx, q, []int64{pg.CreatorID, pg.UpdatedBy})
	if err != nil {
		return nil, err
	}
	if r, ok := refs[pg.CreatorID]; ok {
		out.Creator = &r
	}
	if r, ok := refs[pg.UpdatedBy]; ok {
		out.UpdatedBy = &r
	}
	rows, err := q.Query(ctx, "SELECT label FROM page_labels WHERE page_id = $1 ORDER BY label", pg.ID)
	if err != nil {
		return nil, err
	}
	if out.Labels, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	out.Labels = nonNil(out.Labels)
	var draftUpdated *time.Time
	var draftBase *int
	// Children the reader may not see are not counted (audit L26).
	args := db.Args{pg.ID, a.userID}
	visible := a.VisibleSQL("c", &args)
	if err := q.QueryRow(ctx, `SELECT (SELECT count(*) FROM page_likes WHERE page_id = $1),
		EXISTS (SELECT 1 FROM page_likes WHERE page_id = $1 AND user_id = $2),
		EXISTS (SELECT 1 FROM watchers WHERE entity_type = 'page' AND entity_id = $1 AND user_id = $2),
		(SELECT count(*) FROM pages c WHERE c.parent_id = $1 AND c.status = 'current' AND `+visible+`),
		EXISTS (SELECT 1 FROM page_restrictions WHERE page_id = $1 AND kind = 'edit'),
		(SELECT updated_at FROM page_shared_drafts WHERE page_id = $1 AND changed),
		(SELECT base_version FROM page_shared_drafts WHERE page_id = $1 AND changed)`, args...).
		Scan(&out.Likes, &out.Liked, &out.Watching, &out.Children, &out.EditLocked, &draftUpdated, &draftBase); err != nil {
		return nil, err
	}
	out.Permissions, err = s.pagePermissions(ctx, q, a, pg)
	// Unpublished changes of the shared draft concern its editors.
	if draftUpdated != nil && out.Permissions.Edit {
		out.Draft = &DraftInfo{Updated: *draftUpdated, BaseVersion: *draftBase}
	}
	return out, err
}

// GetPage returns a page the principal may read and records the visit.
func (s *Service) GetPage(ctx context.Context, p *identity.Principal, id int64) (*Page, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pg, err := s.readablePage(ctx, s.pool, a, id, false)
	if err != nil {
		return nil, err
	}
	if a.userID != 0 && pg.Status == StatusCurrent {
		if _, err := s.pool.Exec(ctx, `INSERT INTO page_visits (user_id, page_id) VALUES ($1, $2)
			ON CONFLICT (user_id, page_id) DO UPDATE SET visited_at = now()`, a.userID, pg.ID); err != nil {
			return nil, err
		}
	}
	return s.toPage(ctx, s.pool, a, pg)
}

// insertPage adds a page at the end of its siblings.
func (s *Service) insertPage(ctx context.Context, tx pgx.Tx, spaceID, parentID int64, kind, status, title string, body *docmodel.Doc, uid int64, _ string) (int64, error) {
	var maxRank *string
	if err := tx.QueryRow(ctx, "SELECT max(rank) FROM pages WHERE space_id = $1 AND parent_id IS NOT DISTINCT FROM $2",
		spaceID, nullID(parentID)).Scan(&maxRank); err != nil {
		return 0, err
	}
	rk := rank.After("")
	if maxRank != nil && *maxRank != "" {
		rk = rank.After(*maxRank)
	}
	raw, err := docmodel.Marshal(body)
	if err != nil {
		return 0, err
	}
	var restricted []int64
	if parentID != 0 {
		if err := tx.QueryRow(ctx, "SELECT view_restricted_by FROM pages WHERE id = $1", parentID).Scan(&restricted); err != nil {
			return 0, err
		}
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO pages (space_id, parent_id, kind, status, title, body, body_text, rank, creator_id, updated_by,
		view_restricted_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10) RETURNING id`, spaceID, nullID(parentID), kind, status,
		title, raw, docmodel.PlainText(body), rk, uid, nonNil(restricted)).Scan(&id)
	if db.IsUniqueViolation(err, "pages_title_key") {
		return 0, titleTaken(title)
	}
	return id, err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func titleTaken(title string) error {
	return apperr.Conflict("page.title-taken", "A page titled %q already exists in this space.", title)
}

func (s *Service) refreshSearch(ctx context.Context, tx pgx.Tx, id int64) error {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE pages p SET search_tsv =
		setweight(to_tsvector($2::regconfig, p.title), 'A') ||
		setweight(to_tsvector($2::regconfig, p.body_text), 'B') ||
		setweight(to_tsvector($2::regconfig, coalesce((SELECT string_agg(label, ' ') FROM page_labels WHERE page_id = p.id), '')), 'A') ||
		setweight(to_tsvector($2::regconfig, coalesce((SELECT string_agg(c.body_text, ' ') FROM page_comments c WHERE c.page_id = p.id), '')), 'C')
		WHERE p.id = $1`, id, st.ContentLanguage)
	return err
}

// refreshIssueRefs records the issue keys a current page mentions.
func refreshIssueRefs(ctx context.Context, tx pgx.Tx, id int64, body *docmodel.Doc, current bool) error {
	if _, err := tx.Exec(ctx, "DELETE FROM page_issue_refs WHERE page_id = $1", id); err != nil {
		return err
	}
	if !current {
		return nil
	}
	keys := docmodel.References(body).IssueKeys
	if len(keys) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, "INSERT INTO page_issue_refs (page_id, issue_key) SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING", id, keys)
	return err
}

func (s *Service) docOptions(ctx context.Context) (docmodel.Options, error) {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return docmodel.Options{}, err
	}
	return docmodel.Options{AllowExternalImages: st.AllowExternalImages}, nil
}

func validateDoc(d *docmodel.Doc, opts docmodel.Options) (*docmodel.Doc, error) {
	if d == nil {
		return docmodel.Empty(), nil
	}
	if err := docmodel.Validate(d, opts); err != nil {
		return nil, apperr.Invalidf("page.body", "The page content is not valid: %v", err)
	}
	if raw, _ := docmodel.Marshal(d); len(raw) > docmodel.MaxBytes {
		return nil, apperr.Invalid("page.body", "The page is too large.")
	}
	return d, nil
}

// NewPage creates a draft page or blog post.
type NewPage struct {
	Title      string        `json:"title" maxLength:"255"`
	ParentID   int64         `json:"parentId,omitempty" doc:"Parent page (pages only); 0 = top level"`
	Kind       string        `json:"kind,omitempty" enum:"page,blog" default:"page"`
	TemplateID int64         `json:"templateId,omitempty"`
	Body       *docmodel.Doc `json:"body,omitempty" doc:"Initial content (e.g. a filled-in template)"`
}

// CreatePage starts a new page as a draft of the principal; it becomes visible to others when published.
func (s *Service) CreatePage(ctx context.Context, p *identity.Principal, spaceKey string, in NewPage) (*Page, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	sp, err := s.visibleSpace(ctx, a, spaceKey)
	if err != nil {
		return nil, err
	}
	if !a.Can(sp.ID, PermEdit) {
		return nil, apperr.Forbidden("page.create-denied", "You may not create pages in %s.", sp.Key)
	}
	kind := in.Kind
	if kind == "" {
		kind = "page"
	}
	opts, err := s.docOptions(ctx)
	if err != nil {
		return nil, err
	}
	body := in.Body
	var labels []string
	if in.TemplateID != 0 && body == nil {
		t, err := s.template(ctx, a, sp.ID, in.TemplateID)
		if err != nil {
			return nil, err
		}
		body, labels = t.Body, t.Labels
	}
	if body, err = validateDoc(body, opts); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if in.ParentID != 0 && kind == "page" {
			parent, err := s.readablePage(ctx, tx, a, in.ParentID, false)
			if err != nil {
				return err
			}
			if parent.SpaceID != sp.ID || parent.Kind != "page" || parent.Status != StatusCurrent {
				return apperr.Invalid("page.parent", "The parent must be a current page of the same space.")
			}
		}
		parent := in.ParentID
		if kind == "blog" {
			parent = 0
		}
		if id, err = s.insertPage(ctx, tx, sp.ID, parent, kind, StatusDraft, title, body, p.UserID(), ""); err != nil {
			return err
		}
		for _, l := range labels {
			if _, err := tx.Exec(ctx, "INSERT INTO page_labels (page_id, label) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, l); err != nil {
				return err
			}
		}
		// The new page's shared draft, which its creator edits until the page is published.
		doc, err := nodeOfDoc(body)
		if err != nil {
			return apperr.Invalidf("page.body", "The page content is not valid: %v", err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO page_shared_drafts (page_id, base_version, title, doc, updated_by) VALUES ($1, 0, $2, $3, $4)`,
			id, title, raw, p.UserID())
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetPage(ctx, p, id)
}

// Draft is the shared draft of a page, as its content (the form of the draft operations).
type Draft struct {
	Title       string        `json:"title"`
	Body        *docmodel.Doc `json:"body"`
	BaseVersion int           `json:"baseVersion"`
	Saved       bool          `json:"saved" doc:"Whether the draft has unpublished changes"`
	Updated     *time.Time    `json:"updated,omitempty"`
}

// GetDraft returns the shared draft of a page (editors of the page).
func (s *Service) GetDraft(ctx context.Context, p *identity.Principal, id int64) (*Draft, error) {
	session, err := s.JoinDraft(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return &Draft{Title: session.Title, Body: session.Doc, BaseVersion: session.BaseVersion, Saved: session.Changed,
		Updated: &session.Updated}, nil
}

// DraftInput replaces the content of a shared draft.
type DraftInput struct {
	Title       string        `json:"title" maxLength:"255"`
	Body        *docmodel.Doc `json:"body"`
	BaseVersion int           `json:"baseVersion" minimum:"0"`
}

// SaveDraft replaces the content of the shared draft: one change for everyone editing it.
func (s *Service) SaveDraft(ctx context.Context, p *identity.Principal, id int64, in DraftInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	opts, err := s.docOptions(ctx)
	if err != nil {
		return err
	}
	body, err := validateDoc(in.Body, opts)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.editablePage(ctx, tx, a, id, false)
		if err != nil {
			return err
		}
		d, err := s.ensureDraft(ctx, tx, pg, a.userID)
		if err != nil {
			return err
		}
		if in.BaseVersion != d.baseVersion {
			return conflict(pg.Version)
		}
		if err := s.replaceDraft(ctx, tx, d, body, strings.TrimSpace(in.Title), a.userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE page_shared_drafts SET changed = true, contributors = CASE WHEN $2 = ANY(contributors)
			THEN contributors ELSE contributors || $2::bigint END WHERE page_id = $1`, id, a.userID)
		return err
	})
}

// mayEdit reports whether the principal may edit (or, for unpublished drafts, publish) a page.
func (s *Service) mayEdit(ctx context.Context, a *Access, pg *pageRow) (bool, error) {
	if pg.Status == StatusTrashed || pg.Status == StatusArchived {
		return false, nil
	}
	if pg.Status == StatusDraft {
		return pg.CreatorID == a.userID && a.Can(pg.SpaceID, PermEdit), nil
	}
	return s.canEdit(ctx, s.pool, a, pg)
}

// DiscardDraft discards the unpublished changes of a page's shared draft, for everyone editing it; an unpublished
// page is deleted (by its creator).
func (s *Service) DiscardDraft(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.editablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if pg.Status == StatusDraft {
			_, err := tx.Exec(ctx, "DELETE FROM pages WHERE id = $1", id)
			return err
		}
		d, err := loadDraft(ctx, tx, id, true)
		if err != nil || d == nil || !d.changed {
			return err
		}
		if err := s.resetDraft(ctx, tx, d, pg.Version, pg.Body, pg.Title, a.userID); err != nil {
			return err
		}
		return s.notifyDraft(ctx, tx, id, draftEvent{Type: "discarded", User: &draftUser{ID: a.userID, Name: p.User.DisplayName}})
	})
}

// resetDraft makes a locked draft a version of the page again (after the version was written another way, or when
// the draft's changes are discarded).
func (s *Service) resetDraft(ctx context.Context, tx pgx.Tx, d *draftRow, version int, body *docmodel.Doc, title string, uid int64) error {
	if err := s.replaceDraft(ctx, tx, d, body, title, uid); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE page_shared_drafts SET base_version = $2, changed = false, contributors = '{}' WHERE page_id = $1",
		d.pageID, version)
	return err
}

// PublishInput publishes a new version: the shared draft as of a version, or a body.
type PublishInput struct {
	Title        string        `json:"title" minLength:"1" maxLength:"255"`
	Body         *docmodel.Doc `json:"body,omitempty" doc:"The content to publish (without draftVersion)"`
	DraftVersion *int          `json:"draftVersion,omitempty" minimum:"0" doc:"Publish the shared draft as of this version (its editors' changes) instead of a body"`
	BaseVersion  int           `json:"baseVersion" minimum:"0" doc:"The version the changes are based on"`
	Message      string        `json:"message,omitempty" maxLength:"500"`
	Minor        bool          `json:"minor,omitempty" doc:"A minor edit notifies nobody"`
}

// ConflictError is returned when a page changed after the draft's base version.
func conflict(current int) error {
	return apperr.Conflict("page.conflict", "The page was changed meanwhile (now version %d). Merge your changes with it and publish again.", current)
}

func draftChanged() error {
	return apperr.Conflict("page.draft-changed", "Others have unpublished changes in the page's shared draft: publish or discard them first.")
}

// Publish makes a new version of the page: the shared draft (with draftVersion) or the given body. A body is not
// published over unpublished changes of the shared draft; the draft then follows the new version.
func (s *Service) Publish(ctx context.Context, p *identity.Principal, id int64, in PublishInput) (*Page, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	opts, err := s.docOptions(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, apperr.Invalid("page.title", "The page needs a title.")
	}
	var body *docmodel.Doc
	if in.DraftVersion == nil {
		if body, err = validateDoc(in.Body, opts); err != nil {
			return nil, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.editablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if in.BaseVersion != pg.Version {
			return conflict(pg.Version)
		}
		d, err := loadDraft(ctx, tx, id, true)
		if err != nil {
			return err
		}
		var contributors []int64
		if in.DraftVersion != nil {
			if d == nil {
				return apperr.Conflict("draft.gone", "The draft was published or discarded meanwhile; load it again.")
			}
			if *in.DraftVersion != d.version {
				return behind(d.version)
			}
			doc, err := s.draftDocAt(ctx, tx, d)
			if err != nil {
				return err
			}
			if body, err = docOfNode(doc); err != nil {
				return err
			}
			if body, err = validateDoc(body, opts); err != nil {
				return err
			}
			contributors = d.contributors
		} else if d.changedByOthers(a.userID) {
			return draftChanged()
		}
		if err := s.writeVersion(ctx, tx, a, pg, title, body, in.Message, in.Minor, contributors); err != nil {
			return err
		}
		version := pg.Version + 1
		if d == nil {
			return nil
		}
		if in.DraftVersion != nil {
			if _, err := tx.Exec(ctx, `UPDATE page_shared_drafts SET base_version = $2, title = $3, changed = false, contributors = '{}'
				WHERE page_id = $1`, id, version, title); err != nil {
				return err
			}
		} else if err := s.resetDraft(ctx, tx, d, version, body, title, a.userID); err != nil {
			return err
		}
		return s.notifyDraft(ctx, tx, id, draftEvent{Type: "published", PageVersion: version, Title: title,
			User: &draftUser{ID: a.userID, Name: p.User.DisplayName}})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPage(ctx, p, id)
}

// writeVersion stores a new version of a locked page and notifies watchers; contributors are who wrote it with
// its author (in the shared draft).
func (s *Service) writeVersion(ctx context.Context, tx pgx.Tx, a *Access, pg *pageRow, title string, body *docmodel.Doc,
	message string, minor bool, contributors []int64) error {
	first := pg.Status == StatusDraft
	version := pg.Version + 1
	raw, err := docmodel.Marshal(body)
	if err != nil {
		return err
	}
	status := pg.Status
	if first {
		status = StatusCurrent
	}
	_, err = tx.Exec(ctx, `UPDATE pages SET title = $2, body = $3, body_text = $4, version = $5, status = $6, updated_by = $7,
		updated_at = now(), published_at = coalesce(published_at, CASE WHEN $6 = 'current' THEN now() END) WHERE id = $1`,
		pg.ID, title, raw, docmodel.PlainText(body), version, status, a.userID)
	if db.IsUniqueViolation(err, "pages_title_key") {
		return titleTaken(title)
	}
	if err != nil {
		return err
	}
	others := []int64{}
	for _, c := range contributors {
		if c != a.userID {
			others = append(others, c)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO page_versions (page_id, version, title, body, author_id, message, minor, contributors)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, pg.ID, version, title, raw, a.userID, message, minor, others); err != nil {
		return err
	}
	if err := s.refreshSearch(ctx, tx, pg.ID); err != nil {
		return err
	}
	if err := refreshIssueRefs(ctx, tx, pg.ID, body, status == StatusCurrent); err != nil {
		return err
	}
	if err := notify.Watch(ctx, tx, "page", pg.ID, a.userID); err != nil {
		return err
	}
	verb := "page.updated"
	if first {
		verb = "page.created"
		if pg.Kind == "blog" {
			verb = "blog.created"
		}
	}
	sp, err := spaceByID(ctx, tx, pg.SpaceID)
	if err != nil {
		return err
	}
	url := pageURL(sp.Key, pg.ID, title)
	actor := a.userID
	// The activity names the version: the stream shows the author's note on what changed.
	if err := notify.RecordActivity(ctx, tx, notify.Activity{ActorID: &actor, Verb: verb, EntityType: "page", EntityID: pg.ID,
		ContainerType: "space", ContainerID: pg.SpaceID, Title: title, URL: url, Data: map[string]any{"version": version}}); err != nil {
		return err
	}
	if !minor {
		mentioned := docmodel.References(body).Users
		if err := s.notifyPage(ctx, tx, pg, sp, title, url, verb, docmodel.Summary(body, 300), mentioned); err != nil {
			return err
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{ActorID: a.userID, Action: verb, TargetType: "page", TargetID: itoa(pg.ID),
		Details: map[string]any{"title": title, "version": version}}); err != nil {
		return err
	}
	return s.publish(ctx, tx, PageEvent{Name: verb, PageID: pg.ID, SpaceID: pg.SpaceID, ActorID: a.userID})
}

func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case r == 'ä':
			b.WriteString("ae")
		case r == 'ö':
			b.WriteString("oe")
		case r == 'ü':
			b.WriteString("ue")
		case r == 'ß':
			b.WriteString("ss")
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() > 80 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func pageURL(spaceKey string, id int64, title string) string {
	return "/wiki/spaces/" + spaceKey + "/pages/" + itoa(id) + "/" + slug(title)
}

// notifyPage notifies watchers of the page and the space, and mentioned users, who can read the page.
func (s *Service) notifyPage(ctx context.Context, tx pgx.Tx, pg *pageRow, sp *Space, title, url, event, summary string, mentioned []int64) error {
	recipients, err := notify.Watchers(ctx, tx, "page", pg.ID)
	if err != nil {
		return err
	}
	spaceWatchers, err := notify.Watchers(ctx, tx, "space", pg.SpaceID)
	if err != nil {
		return err
	}
	for _, u := range append(spaceWatchers, mentioned...) {
		if !slices.Contains(recipients, u) {
			recipients = append(recipients, u)
		}
	}
	visible := recipients[:0]
	for _, uid := range recipients {
		ok, err := s.userCanRead(ctx, tx, uid, pg)
		if err != nil {
			return err
		}
		if ok {
			visible = append(visible, uid)
		}
	}
	if len(visible) == 0 {
		return nil
	}
	actor := s.actorOf(ctx)
	return notify.Create(ctx, tx, visible, notify.Notification{Category: notify.CategoryWiki, Event: event, EntityType: "page",
		EntityID: itoa(pg.ID), ActorID: actor, Title: title, Summary: summary, URL: url, Data: map[string]any{"space": sp.Name}})
}

func (s *Service) actorOf(ctx context.Context) *int64 {
	if p := identity.From(ctx); p.Authenticated() {
		id := p.UserID()
		return &id
	}
	return nil
}

// userCanRead reports whether a user (by id, with their groups) can read a page.
func (s *Service) userCanRead(ctx context.Context, q db.Querier, uid int64, pg *pageRow) (bool, error) {
	var groups []int64
	rows, err := q.Query(ctx, "SELECT group_id FROM group_members WHERE user_id = $1", uid)
	if err != nil {
		return false, err
	}
	if groups, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return false, err
	}
	a, err := s.Access(ctx, &identity.Principal{User: &identity.User{ID: uid}, Groups: groups, Method: identity.MethodInternal})
	if err != nil {
		return false, err
	}
	return s.canView(ctx, q, a, pg)
}

// --- versions ---

// VersionInfo describes a version of a page.
type VersionInfo struct {
	Version      int                `json:"version"`
	Title        string             `json:"title"`
	Author       *identity.UserRef  `json:"author,omitempty" doc:"Who published the version"`
	Contributors []identity.UserRef `json:"contributors" doc:"Who else wrote the version (in the shared draft)"`
	Created      time.Time          `json:"created"`
	Message      string             `json:"message"`
	Minor        bool               `json:"minor"`
	contributors []int64
}

// withContributors fills in the contributors of versions.
func withContributors(ctx context.Context, q db.Querier, versions []*VersionInfo) error {
	var ids []int64
	for _, v := range versions {
		ids = append(ids, v.contributors...)
	}
	refs, err := identity.GetUserRefs(ctx, q, ids)
	if err != nil {
		return err
	}
	for _, v := range versions {
		v.Contributors = []identity.UserRef{}
		for _, id := range v.contributors {
			if r, ok := refs[id]; ok {
				v.Contributors = append(v.Contributors, r)
			}
		}
	}
	return nil
}

// PageVersion is a version with its content.
type PageVersion struct {
	VersionInfo
	Body *docmodel.Doc `json:"body"`
}

// PageVersionList is a page of the versions of a page, newest first.
type PageVersionList struct {
	Total int           `json:"total" doc:"Versions of the page, on all pages"`
	Items []VersionInfo `json:"items"`
}

// Versions lists a page of the versions of a page, newest first, with their total. Pages that scripts or imports
// change often have thousands of versions.
func (s *Service) Versions(ctx context.Context, p *identity.Principal, id int64, limit, offset int) (*PageVersionList, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if _, err := s.readablePage(ctx, s.pool, a, id, false); err != nil {
		return nil, err
	}
	list := &PageVersionList{}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM page_versions WHERE page_id = $1", id).Scan(&list.Total); err != nil {
		return nil, err
	}
	// The version numbers of a page are unique, so that pages do not overlap.
	rows, err := s.pool.Query(ctx, `SELECT v.version, v.title, v.author_id, u.username, u.display_name, v.created_at, v.message, v.minor,
		v.contributors FROM page_versions v LEFT JOIN users u ON u.id = v.author_id WHERE v.page_id = $1 ORDER BY v.version DESC
		LIMIT $2 OFFSET $3`, id, pageLimit(limit), max(0, offset))
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (VersionInfo, error) {
		var v VersionInfo
		var aid *int64
		var un, dn *string
		err := row.Scan(&v.Version, &v.Title, &aid, &un, &dn, &v.Created, &v.Message, &v.Minor, &v.contributors)
		if aid != nil {
			v.Author = &identity.UserRef{ID: *aid, Username: *un, DisplayName: *dn, Active: true}
		}
		return v, err
	})
	if err != nil {
		return nil, err
	}
	refs := make([]*VersionInfo, len(out))
	for i := range out {
		refs[i] = &out[i]
	}
	list.Items = nonNil(out)
	return list, withContributors(ctx, s.pool, refs)
}

// GetVersion returns a version of a page with its content.
func (s *Service) GetVersion(ctx context.Context, p *identity.Principal, id int64, version int) (*PageVersion, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if _, err := s.readablePage(ctx, s.pool, a, id, false); err != nil {
		return nil, err
	}
	var v PageVersion
	var aid *int64
	var un, dn *string
	var body []byte
	err = s.pool.QueryRow(ctx, `SELECT v.version, v.title, v.author_id, u.username, u.display_name, v.created_at, v.message, v.minor, v.body,
		v.contributors FROM page_versions v LEFT JOIN users u ON u.id = v.author_id WHERE v.page_id = $1 AND v.version = $2`, id, version).
		Scan(&v.Version, &v.Title, &aid, &un, &dn, &v.Created, &v.Message, &v.Minor, &body, &v.contributors)
	if db.IsNoRows(err) {
		return nil, apperr.NotFound("page.version-not-found", "Version %d does not exist.", version)
	}
	if err != nil {
		return nil, err
	}
	if aid != nil {
		v.Author = &identity.UserRef{ID: *aid, Username: *un, DisplayName: *dn, Active: true}
	}
	if err := withContributors(ctx, s.pool, []*VersionInfo{&v.VersionInfo}); err != nil {
		return nil, err
	}
	v.Body = &docmodel.Doc{}
	return &v, json.Unmarshal(body, v.Body)
}

// RestoreVersion publishes an old version as the newest one.
func (s *Service) RestoreVersion(ctx context.Context, p *identity.Principal, id int64, version int) (*Page, error) {
	old, err := s.GetVersion(ctx, p, id, version)
	if err != nil {
		return nil, err
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if ok, err := s.mayEdit(ctx, a, pg); err != nil {
			return err
		} else if !ok || pg.Status != StatusCurrent {
			return apperr.Forbidden("page.edit-denied", "You may not edit this page.")
		}
		d, err := loadDraft(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d.changedByOthers(a.userID) {
			return draftChanged()
		}
		if err := s.writeVersion(ctx, tx, a, pg, old.Title, old.Body, fmt.Sprintf("Restored version %d", version), false, nil); err != nil {
			return err
		}
		if d == nil {
			return nil
		}
		return s.resetDraft(ctx, tx, d, pg.Version+1, old.Body, old.Title, a.userID)
	})
	if err != nil {
		return nil, err
	}
	return s.GetPage(ctx, p, id)
}

// --- tree, moving and copying ---

// ChildPageList is a page of the child pages of a page in the page tree, in their order.
type ChildPageList struct {
	Total int       `json:"total" doc:"Current child pages the caller may see, on all pages"`
	Items []PageRef `json:"items"`
}

// Tree lists a page of the current child pages of a page (parent 0: the top-level pages of the space) in their
// order, with their total. After imports, a page often has thousands of children. Access is part of the query, so
// that pages are full and the total counts only pages the principal may see.
func (s *Service) Tree(ctx context.Context, p *identity.Principal, spaceKey string, parentID int64, limit, offset int) (*ChildPageList, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	sp, err := s.visibleSpace(ctx, a, spaceKey)
	if err != nil {
		return nil, err
	}
	var args db.Args
	// The top level and the children of a page are told apart here so that both read the tree's index
	// (pages_tree_idx) in the order of the ranks; IS NOT DISTINCT FROM cannot use an index.
	parent := "p.parent_id IS NULL"
	if parentID != 0 {
		parent = "p.parent_id = " + args.Add(parentID)
	}
	cond := "p.space_id = " + args.Add(sp.ID) + " AND " + parent + " AND p.kind = 'page' AND p.status = 'current' AND " +
		a.VisibleSQL("p", &args)
	list := &ChildPageList{Items: []PageRef{}}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM pages p WHERE "+cond, args...).Scan(&list.Total); err != nil {
		return nil, err
	}
	// A page has children in the tree if it would list some when opened: current pages of its space that the
	// principal may see. The space lets the check use the tree's index too. The check runs on the page of
	// children only: in the select list of the query that skips the offset, it would run for every skipped page.
	children := "EXISTS (SELECT 1 FROM pages c WHERE c.space_id = p.space_id AND c.parent_id = p.id AND c.kind = 'page' " +
		"AND c.status = 'current' AND " + a.VisibleSQL("c", &args) + ")"
	// Ranks of siblings may be equal; the id breaks ties, so that pages do not overlap.
	rows, err := s.pool.Query(ctx, "SELECT p.id, p.title, p.kind, cardinality(p.view_restricted_by) > 0, "+children+
		" FROM (SELECT p.id, p.space_id, p.title, p.kind, p.view_restricted_by, p.rank FROM pages p WHERE "+cond+
		" ORDER BY p.rank, p.id LIMIT "+args.Add(pageLimit(limit))+" OFFSET "+args.Add(max(0, offset))+") p ORDER BY p.rank, p.id", args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PageRef, error) {
		r := PageRef{SpaceKey: sp.Key, Status: StatusCurrent}
		err := row.Scan(&r.ID, &r.Title, &r.Kind, &r.Restricted, &r.HasChildren)
		return r, err
	})
	list.Items = nonNil(out)
	return list, err
}

// MoveInput moves a page in the tree.
type MoveInput struct {
	Space    string `json:"space,omitempty" doc:"Target space key (default: the page's space)"`
	ParentID int64  `json:"parentId,omitempty" doc:"New parent; 0 = top level"`
	Before   int64  `json:"before,omitempty" doc:"Put the page before this sibling"`
	After    int64  `json:"after,omitempty" doc:"Put the page after this sibling"`
}

// MovePage moves a page (with its descendants) to another parent, position or space.
func (s *Service) MovePage(ctx context.Context, p *identity.Principal, id int64, in MoveInput) (*Page, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if pg.Kind != "page" || pg.Status != StatusCurrent {
			return apperr.Invalid("page.move", "Only current pages can be moved.")
		}
		if ok, err := s.canEdit(ctx, tx, a, pg); err != nil {
			return err
		} else if !ok {
			return apperr.Forbidden("page.edit-denied", "You may not move this page.")
		}
		target := pg.SpaceID
		if in.Space != "" {
			sp, err := s.visibleSpace(ctx, a, in.Space)
			if err != nil {
				return err
			}
			target = sp.ID
		}
		if !a.Can(target, PermEdit) {
			return apperr.Forbidden("page.create-denied", "You may not add pages to the target space.")
		}
		if target != pg.SpaceID {
			if err := s.mayMoveOut(ctx, tx, a, pg); err != nil {
				return err
			}
		}
		parentID := in.ParentID
		if parentID != 0 {
			parent, err := s.readablePage(ctx, tx, a, parentID, false)
			if err != nil {
				return err
			}
			if parent.SpaceID != target || parent.Kind != "page" || parent.Status != StatusCurrent {
				return apperr.Invalid("page.parent", "The parent must be a current page of the target space.")
			}
			var cycle bool
			if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id FROM pages WHERE id = $1
				UNION ALL SELECT p.id, p.parent_id FROM pages p JOIN up ON p.id = up.parent_id)
				SELECT EXISTS (SELECT 1 FROM up WHERE id = $2)`, parentID, pg.ID).Scan(&cycle); err != nil {
				return err
			}
			if cycle {
				return apperr.Invalid("page.cycle", "A page cannot move below itself.")
			}
		}
		// Below another parent, other view restrictions apply: changing who may see a page is restricting it.
		if changed, err := restrictionsDiffer(ctx, tx, pg.ParentID, parentID); err != nil {
			return err
		} else if changed && !a.Can(target, PermRestrict) && !a.Can(target, PermAdmin) {
			return apperr.Forbidden("page.move-restricted", "There, other people may see the page; moving it needs the permission to restrict pages.")
		}
		// Position among the new siblings.
		var lo, hi string
		anchor := in.Before
		if anchor == 0 {
			anchor = in.After
		}
		if anchor != 0 {
			var ar string
			if err := tx.QueryRow(ctx, "SELECT rank FROM pages WHERE id = $1 AND parent_id IS NOT DISTINCT FROM $2::bigint AND space_id = $3",
				anchor, nullID(parentID), target).Scan(&ar); err != nil {
				if db.IsNoRows(err) {
					return apperr.Invalid("page.anchor", "The sibling is not a child of the new parent.")
				}
				return err
			}
			if in.Before != 0 {
				hi = ar
				if err := tx.QueryRow(ctx, `SELECT coalesce(max(rank), '') FROM pages WHERE space_id = $1 AND parent_id IS NOT DISTINCT FROM $2::bigint
					AND rank < $3 AND id <> $4`, target, nullID(parentID), ar, pg.ID).Scan(&lo); err != nil {
					return err
				}
			} else {
				lo = ar
				if err := tx.QueryRow(ctx, `SELECT coalesce(min(rank), '') FROM pages WHERE space_id = $1 AND parent_id IS NOT DISTINCT FROM $2::bigint
					AND rank > $3 AND id <> $4`, target, nullID(parentID), ar, pg.ID).Scan(&hi); err != nil {
					return err
				}
			}
		} else if err := tx.QueryRow(ctx, `SELECT coalesce(max(rank), '') FROM pages WHERE space_id = $1 AND parent_id IS NOT DISTINCT FROM $2::bigint
			AND id <> $3`, target, nullID(parentID), pg.ID).Scan(&lo); err != nil {
			return err
		}
		var rk string
		switch {
		case hi == "":
			rk = rank.After(lo)
		case lo == "":
			rk = rank.Before(hi)
		default:
			if rk, err = rank.Between(lo, hi); err != nil {
				return apperr.Internal(err)
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE pages SET parent_id = $2, rank = $3, space_id = $4 WHERE id = $1", pg.ID, nullID(parentID), rk, target); err != nil {
			if db.IsUniqueViolation(err, "pages_title_key") {
				return titleTaken(pg.Title)
			}
			return err
		}
		if target != pg.SpaceID {
			if _, err := tx.Exec(ctx, `WITH RECURSIVE down AS (SELECT id FROM pages WHERE parent_id = $1
				UNION ALL SELECT p.id FROM pages p JOIN down ON p.parent_id = down.id)
				UPDATE pages SET space_id = $2 WHERE id IN (SELECT id FROM down)`, pg.ID, target); err != nil {
				if db.IsUniqueViolation(err, "pages_title_key") {
					return apperr.Conflict("page.title-taken", "A page below this one has a title that exists in the target space.")
				}
				return err
			}
		}
		if err := recomputeRestrictions(ctx, tx, pg.ID); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: a.userID, Action: "page.move", TargetType: "page", TargetID: itoa(pg.ID),
			Details: map[string]any{"parent": parentID, "space": target}}); err != nil {
			return err
		}
		if err := s.publish(ctx, tx, PageEvent{Name: "page.moved", PageID: pg.ID, SpaceID: pg.SpaceID, ActorID: a.userID}); err != nil {
			return err
		}
		if target != pg.SpaceID {
			return s.publish(ctx, tx, PageEvent{Name: "page.moved", PageID: pg.ID, SpaceID: target, ActorID: a.userID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetPage(ctx, p, id)
}

// mayMoveOut checks a move of a page with its descendants to another space. It removes them from this space, and
// the target space's rules (its administrators pass all view restrictions) apply to them afterwards: the mover must
// be allowed to delete pages here, and to see and edit every page below, whatever its status.
func (s *Service) mayMoveOut(ctx context.Context, tx pgx.Tx, a *Access, pg *pageRow) error {
	if !a.Can(pg.SpaceID, PermDelete) {
		return apperr.Forbidden("page.move-denied", "You may not move pages out of this space.")
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE down AS (SELECT id FROM pages WHERE parent_id = $1
		UNION ALL SELECT p.id FROM pages p JOIN down ON p.parent_id = down.id) SELECT id FROM down`, pg.ID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		d, err := loadPage(ctx, tx, id, false)
		if err != nil {
			return err
		}
		view, err := s.canView(ctx, tx, a, d)
		if err != nil {
			return err
		}
		edit, err := s.canEdit(ctx, tx, a, d)
		if err != nil {
			return err
		}
		if !view || !edit {
			return apperr.Forbidden("page.move-restricted",
				"Some pages below this one are restricted for you; a space administrator can move it to another space.")
		}
	}
	return nil
}

// recomputeRestrictions refreshes the materialised view restrictions of a page and its descendants.
func recomputeRestrictions(ctx context.Context, tx pgx.Tx, id int64) error {
	if _, err := tx.Exec(ctx, `UPDATE pages p SET view_restricted_by =
		coalesce((SELECT pp.view_restricted_by FROM pages pp WHERE pp.id = p.parent_id), '{}') ||
		CASE WHEN EXISTS (SELECT 1 FROM page_restrictions r WHERE r.page_id = p.id AND r.kind = 'view') THEN ARRAY[p.id] ELSE '{}'::bigint[] END
		WHERE p.id = $1`, id); err != nil {
		return err
	}
	level := []int64{id}
	for depth := 0; len(level) > 0 && depth < 200; depth++ {
		rows, err := tx.Query(ctx, `UPDATE pages c SET view_restricted_by = p.view_restricted_by ||
			CASE WHEN EXISTS (SELECT 1 FROM page_restrictions r WHERE r.page_id = c.id AND r.kind = 'view') THEN ARRAY[c.id] ELSE '{}'::bigint[] END
			FROM pages p WHERE c.parent_id = p.id AND p.id = ANY($1) RETURNING c.id`, level)
		if err != nil {
			return err
		}
		if level, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
	}
	return nil
}

// restrictionsDiffer reports whether pages below two parents (0: the top level) are subject to different view
// restrictions.
func restrictionsDiffer(ctx context.Context, q db.Querier, from, to int64) (bool, error) {
	if from == to {
		return false, nil
	}
	var differ bool
	err := q.QueryRow(ctx, `SELECT NOT (a @> b AND a <@ b) FROM
		(SELECT coalesce((SELECT view_restricted_by FROM pages WHERE id = $1), '{}') AS a,
		        coalesce((SELECT view_restricted_by FROM pages WHERE id = $2), '{}') AS b) x`, from, to).Scan(&differ)
	return differ, err
}

// CopyInput copies a page.
type CopyInput struct {
	Title    string `json:"title,omitempty" maxLength:"255"`
	Space    string `json:"space,omitempty"`
	ParentID int64  `json:"parentId,omitempty"`
}

// CopyPage copies the current version of a page with its labels and attachments.
func (s *Service) CopyPage(ctx context.Context, p *identity.Principal, id int64, in CopyInput) (*Page, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	src, err := s.readablePage(ctx, s.pool, a, id, false)
	if err != nil {
		return nil, err
	}
	if src.Status != StatusCurrent {
		return nil, apperr.Invalid("page.copy", "Only current pages can be copied.")
	}
	target := src.SpaceID
	if in.Space != "" {
		sp, err := s.visibleSpace(ctx, a, in.Space)
		if err != nil {
			return nil, err
		}
		target = sp.ID
	}
	if !a.Can(target, PermEdit) {
		return nil, apperr.Forbidden("page.create-denied", "You may not add pages to the target space.")
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = fmt.Sprintf(defaultTexts.get("page.copyOf", st.DefaultLocale), src.Title)
	}
	parent := in.ParentID
	if in.ParentID == 0 && target == src.SpaceID {
		parent = src.ParentID
	}
	var newID int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if parent != 0 {
			pp, err := s.readablePage(ctx, tx, a, parent, false)
			if err != nil {
				return err
			}
			if pp.SpaceID != target || pp.Status != StatusCurrent {
				return apperr.Invalid("page.parent", "The parent must be a current page of the target space.")
			}
		}
		if newID, err = s.insertPage(ctx, tx, target, parent, src.Kind, StatusDraft, title, src.Body, a.userID, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO page_labels (page_id, label) SELECT $1, label FROM page_labels WHERE page_id = $2", newID, src.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `INSERT INTO page_attachments (page_id, filename, version, content_type, size, sha256, comment, author_id)
			SELECT $1, filename, version, content_type, size, sha256, comment, author_id FROM page_attachments WHERE page_id = $2
			RETURNING id, sha256`, newID, src.ID)
		if err != nil {
			return err
		}
		type ref struct {
			id  int64
			sha string
		}
		var refs []ref
		for rows.Next() {
			var r ref
			if err := rows.Scan(&r.id, &r.sha); err != nil {
				rows.Close()
				return err
			}
			refs = append(refs, r)
		}
		rows.Close()
		for _, r := range refs {
			if _, err := tx.Exec(ctx, "INSERT INTO blob_refs (sha256, owner_type, owner_id) VALUES ($1, 'page-attachment', $2) ON CONFLICT DO NOTHING", r.sha, r.id); err != nil {
				return err
			}
		}
		pg, err := loadPage(ctx, tx, newID, true)
		if err != nil {
			return err
		}
		return s.writeVersion(ctx, tx, a, pg, title, src.Body, "", true, nil)
	})
	if err != nil {
		return nil, err
	}
	return s.GetPage(ctx, p, newID)
}

// --- trash and archive ---

// TrashPage moves a page to the trash; its children move up to its parent.
func (s *Service) TrashPage(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		perms, err := s.pagePermissions(ctx, tx, a, pg)
		if err != nil {
			return err
		}
		if !perms.Delete || pg.Status == StatusTrashed || pg.Status == StatusDraft {
			return apperr.Forbidden("page.delete-denied", "You may not delete this page.")
		}
		var home bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM spaces WHERE home_page_id = $1)", id).Scan(&home); err != nil {
			return err
		}
		if home {
			return apperr.Conflict("page.home", "The home page of a space cannot be deleted; choose another home page first.")
		}
		var restricts bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM page_restrictions WHERE page_id = $1 AND kind = 'view')", id).Scan(&restricts); err != nil {
			return err
		}
		trashed := []int64{id}
		if restricts {
			// Its child pages go along: moved up a level, they would lose the restriction.
			descendants, err := s.trashWithChildren(ctx, tx, a, pg)
			if err != nil {
				return err
			}
			trashed = append(trashed, descendants...)
		} else {
			rows, err := tx.Query(ctx, "UPDATE pages SET parent_id = $2 WHERE parent_id = $1 AND status <> 'trashed' RETURNING id", id,
				nullID(pg.ParentID))
			if err != nil {
				return err
			}
			children, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				return err
			}
			for _, c := range children {
				if err := recomputeRestrictions(ctx, tx, c); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE pages SET status = 'trashed', trashed_at = now(), trashed_by = $2 WHERE id = $1", id, a.userID); err != nil {
			return err
		}
		for _, t := range trashed {
			if err := refreshIssueRefs(ctx, tx, t, nil, false); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: a.userID, Action: "page.trash", TargetType: "page", TargetID: itoa(id),
			Details: map[string]any{"title": pg.Title, "withChildren": len(trashed) - 1}}); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.trashed", PageID: id, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
}

// trashWithChildren moves the current descendants of a page to the trash with it; the caller must be allowed to delete
// each of them.
func (s *Service) trashWithChildren(ctx context.Context, tx pgx.Tx, a *Access, root *pageRow) ([]int64, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE d AS (SELECT id FROM pages WHERE parent_id = $1 AND status <> 'trashed'
		UNION ALL SELECT p.id FROM pages p JOIN d ON p.parent_id = d.id WHERE p.status <> 'trashed') SELECT id FROM d`, root.ID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	denied := apperr.Forbidden("page.delete-children", "This page restricts who may see its child pages, so they go to the trash with it — "+
		"and you may not delete all of them. Move them elsewhere first, or ask an administrator of the space.")
	for _, id := range ids {
		child, err := loadPage(ctx, tx, id, true)
		if err != nil {
			return nil, err
		}
		if ok, err := s.canView(ctx, tx, a, child); err != nil {
			return nil, err
		} else if !ok {
			return nil, denied
		}
		perms, err := s.pagePermissions(ctx, tx, a, child)
		if err != nil {
			return nil, err
		}
		if !perms.Delete {
			return nil, denied
		}
	}
	_, err = tx.Exec(ctx, `UPDATE pages SET status = 'trashed', trashed_at = now(), trashed_by = $2, trash_root_id = $3
		WHERE id = ANY($1)`, ids, a.userID, root.ID)
	return ids, err
}

// TrashList is a page of the trash of a space, the latest deleted first.
type TrashList struct {
	Total int           `json:"total" doc:"Trashed pages the caller may see, on all pages"`
	Items []TrashedPage `json:"items"`
}

// Trash lists a page of the trashed pages of a space, the latest deleted first, with their total.
func (s *Service) Trash(ctx context.Context, p *identity.Principal, spaceKey string, limit, offset int) (*TrashList, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	sp, err := s.visibleSpace(ctx, a, spaceKey)
	if err != nil {
		return nil, err
	}
	list := &TrashList{Items: []TrashedPage{}}
	if !a.Can(sp.ID, PermDelete) && !a.Can(sp.ID, PermDeleteOwn) {
		return list, nil
	}
	var args db.Args
	// Child pages that went to the trash with their page are listed with it; restricted pages only to who may see
	// them. Access is part of the query, so that pages are full and the total counts only what the principal may
	// see.
	where := "p.space_id = " + args.Add(sp.ID) + " AND p.status = 'trashed' AND p.trash_root_id IS NULL AND " + a.VisibleSQL("p", &args)
	if !a.Can(sp.ID, PermDelete) {
		where += " AND p.creator_id = " + args.Add(a.userID)
	}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM pages p WHERE "+where, args...).Scan(&list.Total); err != nil {
		return nil, err
	}
	// Pages deleted in one transaction share their time; the id breaks ties, so that pages do not overlap.
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.title, p.kind, p.trashed_at, u.id, u.username, u.display_name FROM pages p
		LEFT JOIN users u ON u.id = p.trashed_by WHERE `+where+" ORDER BY p.trashed_at DESC, p.id DESC LIMIT "+
		args.Add(pageLimit(limit))+" OFFSET "+args.Add(max(0, offset)), args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TrashedPage, error) {
		var t TrashedPage
		var uid *int64
		var un, dn *string
		err := row.Scan(&t.ID, &t.Title, &t.Kind, &t.Trashed, &uid, &un, &dn)
		if uid != nil {
			t.TrashedBy = &identity.UserRef{ID: *uid, Username: *un, DisplayName: *dn, Active: true}
		}
		return t, err
	})
	if err != nil {
		return nil, err
	}
	list.Items = nonNil(out)
	return list, s.trashedChildren(ctx, a, sp.ID, list.Items)
}

// trashedChildren counts the child pages that went to the trash with each of the trashed pages of a space, in one
// query for all of them: trash_root_id has no index of its own, so a count per page would read the pages of the
// space once per page. The children are in the space of their page. Children the principal may not see are not
// counted.
func (s *Service) trashedChildren(ctx context.Context, a *Access, spaceID int64, list []TrashedPage) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	at := make(map[int64]int, len(list))
	for i, t := range list {
		ids[i], at[t.ID] = t.ID, i
	}
	args := db.Args{ids, spaceID}
	rows, err := s.pool.Query(ctx, "SELECT c.trash_root_id, count(*) FROM pages c WHERE c.trash_root_id = ANY($1) AND c.space_id = $2 AND "+
		a.VisibleSQL("c", &args)+" GROUP BY c.trash_root_id", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var root int64
		var n int
		if err := rows.Scan(&root, &n); err != nil {
			return err
		}
		list[at[root]].Children = n
	}
	return rows.Err()
}

// TrashedPage is a page in the trash.
type TrashedPage struct {
	ID        int64             `json:"id"`
	Title     string            `json:"title"`
	Kind      string            `json:"kind"`
	Trashed   time.Time         `json:"trashed"`
	TrashedBy *identity.UserRef `json:"trashedBy,omitempty"`
	Children  int               `json:"children" doc:"Child pages that went to the trash with it (and come back with it) that the caller may see"`
}

// RestorePage takes a page out of the trash (at the top level if its parent is gone).
func (s *Service) RestorePage(ctx context.Context, p *identity.Principal, id int64) (*Page, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if pg.Status != StatusTrashed {
			return apperr.Conflict("page.not-trashed", "The page is not in the trash.")
		}
		var withParent bool
		if err := tx.QueryRow(ctx, "SELECT trash_root_id IS NOT NULL FROM pages WHERE id = $1", id).Scan(&withParent); err != nil {
			return err
		}
		if withParent {
			return apperr.Conflict("page.trashed-with-parent", "The page went to the trash with its parent page, which restricts who may see it; restore that page.")
		}
		var parentOK bool
		if pg.ParentID != 0 {
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pages WHERE id = $1 AND status = 'current')", pg.ParentID).Scan(&parentOK); err != nil {
				return err
			}
		}
		parent := pg.ParentID
		if !parentOK {
			parent = 0
		}
		title := pg.Title
		for i := 0; i < 20; i++ {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pages WHERE space_id = $1 AND lower(title) = lower($2)
				AND kind = 'page' AND status IN ('current', 'archived'))`, pg.SpaceID, title).Scan(&taken); err != nil {
				return err
			}
			if !taken || pg.Kind != "page" {
				break
			}
			title = fmt.Sprintf("%s (%d)", pg.Title, i+2)
		}
		if _, err := tx.Exec(ctx, `UPDATE pages SET status = 'current', trashed_at = NULL, trashed_by = NULL, parent_id = $2, title = $3
			WHERE id = $1`, id, nullID(parent), title); err != nil {
			return err
		}
		// Child pages that went to the trash with it come back with it.
		rows, err := tx.Query(ctx, `UPDATE pages SET status = 'current', trashed_at = NULL, trashed_by = NULL, trash_root_id = NULL
			WHERE trash_root_id = $1 RETURNING id`, id)
		if err != nil {
			return err
		}
		children, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if err := recomputeRestrictions(ctx, tx, id); err != nil {
			return err
		}
		if err := refreshIssueRefs(ctx, tx, id, pg.Body, true); err != nil {
			return err
		}
		for _, c := range children {
			child, err := loadPage(ctx, tx, c, false)
			if err != nil {
				return err
			}
			if err := refreshIssueRefs(ctx, tx, c, child.Body, true); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: a.userID, Action: "page.restore", TargetType: "page", TargetID: itoa(id)}); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.restored", PageID: id, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPage(ctx, p, id)
}

// PurgePage deletes a trashed page for good (space administrators).
func (s *Service) PurgePage(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if pg.Status != StatusTrashed {
			return apperr.Conflict("page.not-trashed", "Only pages in the trash can be purged.")
		}
		if !a.Can(pg.SpaceID, PermAdmin) {
			return apperr.Forbidden("space.admin-required", "Only administrators of the space can purge the trash.")
		}
		// Child pages that went to the trash with it are purged with it (trash_root_id cascades).
		if _, err := tx.Exec(ctx, `DELETE FROM blob_refs WHERE owner_type = 'page-attachment' AND owner_id IN
			(SELECT id FROM page_attachments WHERE page_id = $1 OR page_id IN (SELECT id FROM pages WHERE trash_root_id = $1))`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM pages WHERE id = $1", id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: a.userID, Action: "page.purge", TargetType: "page", TargetID: itoa(id),
			Details: map[string]any{"title": pg.Title}})
	})
}

// PurgeTrash purges the pages that have been in the trash longer than the retention, with the
// child pages that went to the trash with them. It returns how many trashed pages it purged.
func (s *Service) PurgeTrash(ctx context.Context, olderThan time.Duration) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, title FROM pages WHERE status = 'trashed' AND trash_root_id IS NULL
		AND trashed_at < now() - make_interval(secs => $1) ORDER BY trashed_at LIMIT 1000`, olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	type trashed struct {
		ID    int64
		Title string
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[trashed])
	if err != nil {
		return 0, err
	}
	for _, t := range list {
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM blob_refs WHERE owner_type = 'page-attachment' AND owner_id IN
				(SELECT id FROM page_attachments WHERE page_id = $1 OR page_id IN (SELECT id FROM pages WHERE trash_root_id = $1))`, t.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "DELETE FROM pages WHERE id = $1 AND status = 'trashed'", t.ID); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{ActorID: identity.SystemUserID, Action: "page.purge", TargetType: "page",
				TargetID: itoa(t.ID), Details: map[string]any{"title": t.Title, "retention": true}})
		})
		if err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// SetArchived archives a page (read-only, hidden from the tree) or brings it back.
func (s *Service) SetArchived(ctx context.Context, p *identity.Principal, id int64, archived bool) (*Page, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		perms, err := s.pagePermissions(ctx, tx, a, pg)
		if err != nil {
			return err
		}
		if !perms.Archive {
			return apperr.Forbidden("page.archive-denied", "You may not archive this page.")
		}
		from, to := StatusCurrent, StatusArchived
		if !archived {
			from, to = StatusArchived, StatusCurrent
		}
		if pg.Status != from {
			if archived {
				return apperr.Conflict("page.state", "Only current pages can be archived.")
			}
			return apperr.Conflict("page.state", "The page is not archived.")
		}
		if _, err := tx.Exec(ctx, "UPDATE pages SET status = $2 WHERE id = $1", id, to); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.updated", PageID: id, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
	if err != nil {
		return nil, err
	}
	return s.GetPage(ctx, p, id)
}

// --- labels, likes, watching ---

// SetLabels replaces the labels of a page.
func (s *Service) SetLabels(ctx context.Context, p *identity.Principal, id int64, labels []string) ([]string, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	clean := []string{}
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if !validLabel(l) {
			return nil, apperr.Invalidf("page.label", "Labels are lower case without spaces or commas: %s", l)
		}
		if !slices.Contains(clean, l) {
			clean = append(clean, l)
		}
	}
	slices.Sort(clean)
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if ok, err := s.mayEdit(ctx, a, pg); err != nil {
			return err
		} else if !ok {
			return apperr.Forbidden("page.edit-denied", "You may not change the labels of this page.")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM page_labels WHERE page_id = $1", id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO page_labels (page_id, label) SELECT $1, unnest($2::text[])", id, clean); err != nil {
			return err
		}
		if err := s.refreshSearch(ctx, tx, id); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.labelled", PageID: id, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
	return clean, err
}

// SetLike likes or unlikes a page.
func (s *Service) SetLike(ctx context.Context, p *identity.Principal, id int64, on bool) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if _, err := s.readablePage(ctx, s.pool, a, id, false); err != nil {
		return err
	}
	if on {
		_, err = s.pool.Exec(ctx, "INSERT INTO page_likes (page_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, a.userID)
	} else {
		_, err = s.pool.Exec(ctx, "DELETE FROM page_likes WHERE page_id = $1 AND user_id = $2", id, a.userID)
	}
	return err
}

// WatchPage starts or stops watching a page.
func (s *Service) WatchPage(ctx context.Context, p *identity.Principal, id int64, on bool) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if _, err := s.readablePage(ctx, s.pool, a, id, false); err != nil {
		return err
	}
	if on {
		return notify.Watch(ctx, s.pool, "page", id, a.userID)
	}
	return notify.Unwatch(ctx, s.pool, "page", id, a.userID)
}

// --- restrictions ---

// Restrictions are the view and edit restrictions of a page.
type Restrictions struct {
	View      []Holder  `json:"view"`
	Edit      []Holder  `json:"edit"`
	Inherited []PageRef `json:"inherited" doc:"Ancestors whose view restrictions also apply"`
}

// GetRestrictions returns the restrictions of a page.
func (s *Service) GetRestrictions(ctx context.Context, p *identity.Principal, id int64) (*Restrictions, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pg, err := s.readablePage(ctx, s.pool, a, id, false)
	if err != nil {
		return nil, err
	}
	out := &Restrictions{View: []Holder{}, Edit: []Holder{}, Inherited: []PageRef{}}
	rows, err := s.pool.Query(ctx, `SELECT r.kind, r.holder_type, r.holder_id, coalesce(u.display_name, g.name, '')
		FROM page_restrictions r LEFT JOIN users u ON r.holder_type = 'user' AND u.id = r.holder_id
		LEFT JOIN groups g ON r.holder_type = 'group' AND g.id = r.holder_id WHERE r.page_id = $1 ORDER BY 1, 2, 4`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind string
		var h Holder
		if err := rows.Scan(&kind, &h.Type, &h.ID, &h.Name); err != nil {
			rows.Close()
			return nil, err
		}
		if kind == "view" {
			out.View = append(out.View, h)
		} else {
			out.Edit = append(out.Edit, h)
		}
	}
	rows.Close()
	for _, rid := range pg.ViewRestrictedBy {
		if rid == pg.ID {
			continue
		}
		var r PageRef
		if err := s.pool.QueryRow(ctx, "SELECT p.id, p.title, p.kind, s.key FROM pages p JOIN spaces s ON s.id = p.space_id WHERE p.id = $1", rid).
			Scan(&r.ID, &r.Title, &r.Kind, &r.SpaceKey); err == nil {
			out.Inherited = append(out.Inherited, r)
		}
	}
	return out, nil
}

// RestrictionsInput replaces the restrictions of a page.
type RestrictionsInput struct {
	View []Holder `json:"view"`
	Edit []Holder `json:"edit"`
}

// SetRestrictions replaces the view and edit restrictions of a page. The caller keeps access.
func (s *Service) SetRestrictions(ctx context.Context, p *identity.Principal, id int64, in RestrictionsInput) (*Restrictions, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	clean := func(list []Holder) ([]Holder, error) {
		out := []Holder{}
		for _, h := range list {
			if (h.Type != "user" && h.Type != "group") || h.ID <= 0 {
				return nil, apperr.Invalid("page.restriction", "Restrictions name users or groups.")
			}
			if !slices.ContainsFunc(out, func(x Holder) bool { return x.Type == h.Type && x.ID == h.ID }) {
				out = append(out, Holder{Type: h.Type, ID: h.ID})
			}
		}
		return out, nil
	}
	view, err := clean(in.View)
	if err != nil {
		return nil, err
	}
	edit, err := clean(in.Edit)
	if err != nil {
		return nil, err
	}
	self := Holder{Type: "user", ID: a.userID}
	if len(view) > 0 && !a.passes(view) {
		view = append(view, self)
	}
	if len(edit) > 0 && !a.passes(edit) {
		edit = append(edit, self)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.readablePage(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if !a.Can(pg.SpaceID, PermRestrict) {
			return apperr.Forbidden("page.restrict-denied", "You may not restrict pages in this space.")
		}
		// Someone an edit restriction keeps out cannot lift it (space administrators can).
		if ok, err := s.canEdit(ctx, tx, a, pg); err != nil {
			return err
		} else if !ok {
			return apperr.Forbidden("page.restrict-denied", "You may not edit this page, so you may not change its restrictions.")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM page_restrictions WHERE page_id = $1", id); err != nil {
			return err
		}
		for kind, list := range map[string][]Holder{"view": view, "edit": edit} {
			for _, h := range list {
				if _, err := tx.Exec(ctx, "INSERT INTO page_restrictions (page_id, kind, holder_type, holder_id) VALUES ($1, $2, $3, $4)",
					id, kind, h.Type, h.ID); err != nil {
					return err
				}
			}
		}
		if err := recomputeRestrictions(ctx, tx, id); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: a.userID, Action: "page.restrict", TargetType: "page", TargetID: itoa(id),
			Details: map[string]any{"view": len(view), "edit": len(edit)}}); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.restricted", PageID: id, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
	if err != nil {
		return nil, err
	}
	return s.GetRestrictions(ctx, p, id)
}
