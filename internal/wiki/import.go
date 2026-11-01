// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/legacy"
	"kogge.de/kogge/internal/platform/live"
)

// Importer loads data of a migrated wiki. It acts for the system: there are no permission checks,
// no notifications, and authors and timestamps come from the source. Every entity is recorded in legacy_ids under
// its source id, so loads can be repeated.
type Importer struct {
	s      *Service
	tx     pgx.Tx
	source string
}

// NewImporter returns an importer working in a transaction. source identifies the migrated system, e.g.
// "confluence:https://wiki.example.com".
func (s *Service) NewImporter(tx pgx.Tx, source string) *Importer {
	return &Importer{s: s, tx: tx, source: source}
}

func (im *Importer) ctx() context.Context { return context.Background() }

// Find returns the Kogge entity of a source entity: type is space, page, attachment or comment.
func (im *Importer) Find(typ, srcID string) (int64, bool, error) {
	return legacy.Lookup(im.ctx(), im.tx, im.source, typ, srcID)
}

func (im *Importer) record(typ, srcID, entity string, id int64) error {
	return legacy.Record(im.ctx(), im.tx, im.source, typ, srcID, entity, id)
}

// ImportSpace describes a space to load.
type ImportSpace struct {
	SourceID    string
	Key         string // letters and digits; personal spaces get the key of their owner
	Name        string
	Description string
	Personal    bool
	OwnerID     int64
	CreatorID   int64
	Created     time.Time
	Archived    bool
}

// Space creates or updates a space (without the welcome page of new spaces).
func (im *Importer) Space(in ImportSpace) (int64, error) {
	ctx := im.ctx()
	typ := "team"
	var owner any
	if in.Personal {
		typ, owner = "personal", nullID(in.OwnerID)
	}
	if id, ok, err := im.Find("space", in.SourceID); err != nil || ok {
		if ok {
			_, err = im.tx.Exec(ctx, "UPDATE spaces SET name = $2, description = $3, archived = $4, updated_at = now() WHERE id = $1",
				id, in.Name, in.Description, in.Archived)
		}
		return id, err
	}
	key := strings.ToUpper(in.Key)
	if in.Personal {
		key = "~" + strings.TrimPrefix(key, "~")
	}
	var id int64
	err := im.tx.QueryRow(ctx, `INSERT INTO spaces (key, name, description, type, owner_id, created_by, created_at, updated_at, archived)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8) RETURNING id`, key, in.Name, in.Description, typ, owner, nullID(in.CreatorID), in.Created,
		in.Archived).Scan(&id)
	if db.IsUniqueViolation(err, "spaces_key_key") {
		return 0, fmt.Errorf("the space key %s is already used in Kogge", key)
	}
	if db.IsUniqueViolation(err, "spaces_personal_key") {
		return 0, fmt.Errorf("the owner of the personal space %s already has a personal space in Kogge", key)
	}
	if err != nil {
		return 0, err
	}
	return id, im.record("space", in.SourceID, "space", id)
}

// ImportGrant grants a space permission (see SpacePermissions) to a holder: user, group, logged-in or anyone.
type ImportGrant struct {
	Permission string
	HolderType string
	HolderID   int64
}

// SpacePermissions replaces the permissions of a space.
func (im *Importer) SpacePermissions(spaceID int64, grants []ImportGrant) error {
	ctx := im.ctx()
	if _, err := im.tx.Exec(ctx, "DELETE FROM space_permissions WHERE space_id = $1", spaceID); err != nil {
		return err
	}
	gs := make([]grant, 0, len(grants))
	for _, g := range grants {
		if !slices.Contains(SpacePermissions, g.Permission) {
			return fmt.Errorf("unknown space permission %s", g.Permission)
		}
		if g.HolderType == "logged-in" || g.HolderType == "anyone" {
			g.HolderID = 0
		}
		gs = append(gs, grant{g.Permission, g.HolderType, g.HolderID})
	}
	if err := insertGrants(ctx, im.tx, spaceID, gs); err != nil {
		return err
	}
	return live.InvalidateCache(ctx, im.tx, "authz")
}

// ImportPage describes a page or blog post. The body is set later (SetBody), when the pages it links to exist.
type ImportPage struct {
	SourceID  string
	SpaceID   int64
	ParentID  int64
	Kind      string // page or blog
	Status    string // current or archived
	Title     string
	CreatorID int64
	Created   time.Time
	UpdatedBy int64
	Updated   time.Time
	Rank      string
}

// Page creates or updates a page; a title that is taken in the space gets a suffix, which is returned.
func (im *Importer) Page(in ImportPage) (int64, string, error) {
	ctx := im.ctx()
	if in.Kind != "blog" {
		in.Kind = "page"
	}
	if in.Status != StatusArchived {
		in.Status = StatusCurrent
	}
	title := in.Title
	id, ok, err := im.Find("page", in.SourceID)
	if err != nil {
		return 0, "", err
	}
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			title = fmt.Sprintf("%s (%d)", in.Title, attempt)
		}
		// A savepoint keeps the transaction usable after a title conflict.
		sp, err := im.tx.Begin(ctx)
		if err != nil {
			return 0, "", err
		}
		// Pages being loaded are hidden (view restricted by the page 0, which nobody passes; space administrators see
		// them) until FinishSpace computes their restrictions: a restricted page must not be readable while its
		// restrictions are not loaded yet, nor forever if the load stops.
		if ok {
			_, err = sp.Exec(ctx, `UPDATE pages SET space_id = $2, parent_id = $3, kind = $4, status = $5, title = $6, rank = $7, creator_id = $8,
				created_at = $9, updated_by = $10, updated_at = $11, view_restricted_by = '{0}' WHERE id = $1`, id, in.SpaceID, nullID(in.ParentID),
				in.Kind, in.Status, title, in.Rank, nullID(in.CreatorID), in.Created, nullID(in.UpdatedBy), in.Updated)
		} else {
			raw, _ := docmodel.Marshal(docmodel.Empty())
			err = sp.QueryRow(ctx, `INSERT INTO pages (space_id, parent_id, kind, status, title, body, rank, creator_id, created_at, updated_by,
				updated_at, published_at, view_restricted_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $9, '{0}') RETURNING id`,
				in.SpaceID, nullID(in.ParentID), in.Kind, in.Status, title, raw, in.Rank, nullID(in.CreatorID), in.Created, nullID(in.UpdatedBy),
				in.Updated).Scan(&id)
		}
		if db.IsUniqueViolation(err, "pages_title_key") && attempt < 50 {
			if err := sp.Rollback(ctx); err != nil {
				return 0, "", err
			}
			continue
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			return 0, "", err
		}
		if err := sp.Commit(ctx); err != nil {
			return 0, "", err
		}
		break
	}
	if !ok {
		if err := im.record("page", in.SourceID, "page", id); err != nil {
			return 0, "", err
		}
	}
	return id, title, nil
}

// SetBody sets the current content of a page as its version number version.
func (im *Importer) SetBody(pageID int64, body *docmodel.Doc, version int, authorID int64, at time.Time, message string) error {
	ctx := im.ctx()
	if body == nil {
		body = docmodel.Empty()
	}
	raw, err := docmodel.Marshal(body)
	if err != nil {
		return err
	}
	version = max(version, 1)
	var title, status string
	if err := im.tx.QueryRow(ctx, `UPDATE pages SET body = $2, body_text = $3, version = $4 WHERE id = $1 RETURNING title, status`,
		pageID, raw, docmodel.PlainText(body), version).Scan(&title, &status); err != nil {
		return err
	}
	if err := im.Version(pageID, version, title, body, authorID, at, message, false); err != nil {
		return err
	}
	return refreshIssueRefs(ctx, im.tx, pageID, body, status == StatusCurrent)
}

// Version stores an earlier (or the current) version of a page.
func (im *Importer) Version(pageID int64, version int, title string, body *docmodel.Doc, authorID int64, at time.Time, message string, minor bool) error {
	raw, err := docmodel.Marshal(body)
	if err != nil {
		return err
	}
	_, err = im.tx.Exec(im.ctx(), `INSERT INTO page_versions (page_id, version, title, body, author_id, created_at, message, minor)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (page_id, version) DO UPDATE SET title = EXCLUDED.title, body = EXCLUDED.body,
		author_id = EXCLUDED.author_id, created_at = EXCLUDED.created_at, message = EXCLUDED.message, minor = EXCLUDED.minor`,
		pageID, version, title, raw, nullID(authorID), at, message, minor)
	return err
}

// ImportableLabel reports whether Labels accepts a label of a migrated page (the analysis of a migration lists the
// others).
func ImportableLabel(l string) bool { return validLabel(strings.ToLower(strings.TrimSpace(l))) }

// Labels replaces the labels of a page; labels Kogge does not accept are returned.
func (im *Importer) Labels(pageID int64, labels []string) ([]string, error) {
	var ok, rejected []string
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if validLabel(l) {
			ok = append(ok, l)
		} else if l != "" {
			rejected = append(rejected, l)
		}
	}
	if _, err := im.tx.Exec(im.ctx(), "DELETE FROM page_labels WHERE page_id = $1", pageID); err != nil {
		return nil, err
	}
	_, err := im.tx.Exec(im.ctx(), "INSERT INTO page_labels (page_id, label) SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING", pageID, nonNil(ok))
	return rejected, err
}

// Restrictions replaces the view or edit restrictions of a page. Call FinishSpace afterwards: it applies view
// restrictions to the descendants.
func (im *Importer) Restrictions(pageID int64, kind string, users, groups []int64) error {
	if kind != "view" && kind != "edit" {
		return fmt.Errorf("unknown restriction %s", kind)
	}
	ctx := im.ctx()
	if _, err := im.tx.Exec(ctx, "DELETE FROM page_restrictions WHERE page_id = $1 AND kind = $2", pageID, kind); err != nil {
		return err
	}
	for typ, ids := range map[string][]int64{"user": users, "group": groups} {
		if _, err := im.tx.Exec(ctx, `INSERT INTO page_restrictions (page_id, kind, holder_type, holder_id) SELECT $1, $2, $3, unnest($4::bigint[])
			ON CONFLICT DO NOTHING`, pageID, kind, typ, nonNil(ids)); err != nil {
			return err
		}
	}
	return nil
}

// Attachment stores a file of a page (the caller streams the content).
func (im *Importer) Attachment(ctx context.Context, pageID int64, srcID, filename, contentType, comment string, authorID int64, created time.Time,
	r io.Reader) (int64, error) {
	if id, ok, err := im.Find("attachment", srcID); err != nil || ok {
		return id, err
	}
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "." || filename == "/" || filename == "" {
		filename = "file"
	}
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = "application/octet-stream"
		if ct := mime.TypeByExtension(filepath.Ext(filename)); ct != "" {
			contentType = ct
		}
	}
	ref, err := im.s.blobs.Put(ctx, r, 0, contentType)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := im.tx.QueryRow(im.ctx(), `INSERT INTO page_attachments (page_id, filename, version, content_type, size, sha256, comment, author_id, created_at)
		VALUES ($1, $2, coalesce((SELECT max(version) + 1 FROM page_attachments WHERE page_id = $1 AND filename = $2), 1), $3, $4, $5, $6, $7, $8)
		RETURNING id`, pageID, filename, contentType, ref.Size, ref.SHA256, comment, nullID(authorID), created).Scan(&id); err != nil {
		return 0, err
	}
	if err := blob.AddRef(im.ctx(), im.tx, ref.SHA256, "page-attachment", id); err != nil {
		return 0, err
	}
	return id, im.record("attachment", srcID, "page-attachment", id)
}

// PageURL returns the address of a page.
func (im *Importer) PageURL(pageID int64) (string, error) {
	var key, title string
	err := im.tx.QueryRow(im.ctx(), "SELECT s.key, p.title FROM pages p JOIN spaces s ON s.id = p.space_id WHERE p.id = $1", pageID).Scan(&key, &title)
	return pageURL(key, pageID, title), err
}

// AttachmentURL is the address of an attachment's content.
func AttachmentURL(id int64) string { return attachmentURL(id) }

// ImportComment describes a page comment; inline comments name the anchor of the commented text.
type ImportComment struct {
	SourceID   string
	PageID     int64
	ParentID   int64
	AuthorID   int64
	Body       *docmodel.Doc
	Anchor     string
	AnchorText string
	Resolved   bool
	Created    time.Time
	Updated    time.Time
}

// Comment creates or updates a comment.
func (im *Importer) Comment(in ImportComment) (int64, error) {
	ctx := im.ctx()
	raw, err := docmodel.Marshal(in.Body)
	if err != nil {
		return 0, err
	}
	var resolved any
	if in.Resolved {
		resolved = in.Updated
	}
	id, ok, err := im.Find("comment", in.SourceID)
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = im.tx.Exec(ctx, `UPDATE page_comments SET body = $2, body_text = $3, anchor = NULLIF($4, ''), anchor_text = NULLIF($5, ''),
			resolved_at = $6, updated_at = $7 WHERE id = $1`, id, raw, docmodel.PlainText(in.Body), in.Anchor, in.AnchorText, resolved, in.Updated)
		return id, err
	}
	err = im.tx.QueryRow(ctx, `INSERT INTO page_comments (page_id, parent_id, author_id, body, body_text, anchor, anchor_text, resolved_at,
		created_at, updated_at, edited) VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9, $10, $11) RETURNING id`,
		in.PageID, nullID(in.ParentID), nullID(in.AuthorID), raw, docmodel.PlainText(in.Body), in.Anchor, in.AnchorText, resolved,
		in.Created, in.Updated, in.Updated.Sub(in.Created) > time.Minute).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, im.record("comment", in.SourceID, "page-comment", id)
}

// Likes adds likes of a page.
func (im *Importer) Likes(pageID int64, users []int64) error {
	_, err := im.tx.Exec(im.ctx(), "INSERT INTO page_likes (page_id, user_id) SELECT $1, unnest($2::bigint[]) ON CONFLICT DO NOTHING",
		pageID, nonNil(users))
	return err
}

// FinishSpace completes a loaded space: the home page, view restrictions down the page tree (which also makes the
// loaded pages visible, see Page) and the search index.
func (im *Importer) FinishSpace(ctx context.Context, spaceID, homePageID int64) error {
	if homePageID != 0 {
		if _, err := im.tx.Exec(ctx, "UPDATE spaces SET home_page_id = $2 WHERE id = $1", spaceID, homePageID); err != nil {
			return err
		}
	}
	rows, err := im.tx.Query(ctx, "SELECT id FROM pages WHERE space_id = $1 AND parent_id IS NULL", spaceID)
	if err != nil {
		return err
	}
	roots, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range roots {
		if err := recomputeRestrictions(ctx, im.tx, id); err != nil {
			return err
		}
	}
	rows, err = im.tx.Query(ctx, "SELECT id FROM pages WHERE space_id = $1", spaceID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := im.s.refreshSearch(ctx, im.tx, id); err != nil {
			return err
		}
	}
	return live.InvalidateCache(ctx, im.tx, "authz")
}

// Done applies cache invalidation locally after the import transaction committed.
func (im *Importer) Done() {
	im.s.Invalidate()
	im.s.authz.Invalidate()
}
