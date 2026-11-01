// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/httpx"
	"kogge.de/kogge/internal/platform/settings"
	"kogge.de/kogge/internal/platform/storage"
)

// Attachment is a file attached to a page (one version).
type Attachment struct {
	ID          int64             `json:"id"`
	PageID      int64             `json:"pageId"`
	Filename    string            `json:"filename"`
	Version     int               `json:"version"`
	ContentType string            `json:"contentType"`
	Size        int64             `json:"size"`
	Comment     string            `json:"comment"`
	Author      *identity.UserRef `json:"author,omitempty"`
	Created     time.Time         `json:"created"`
	URL         string            `json:"url"`
	CanDelete   bool              `json:"canDelete"`
}

func attachmentURL(id int64) string { return "/api/v1/wiki/attachments/" + itoa(id) }

// Attachments lists the attachments of a page: the latest version of each file, or all versions.
func (s *Service) Attachments(ctx context.Context, p *identity.Principal, pageID int64, all bool) ([]Attachment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pg, err := s.readablePage(ctx, s.pool, a, pageID, false)
	if err != nil {
		return nil, err
	}
	where := "a.page_id = $1"
	if !all {
		where += " AND a.version = (SELECT max(b.version) FROM page_attachments b WHERE b.page_id = a.page_id AND b.filename = a.filename)"
	}
	rows, err := s.pool.Query(ctx, `SELECT a.id, a.page_id, a.filename, a.version, a.content_type, a.size, a.comment, a.author_id, u.username,
		u.display_name, a.created_at FROM page_attachments a LEFT JOIN users u ON u.id = a.author_id WHERE `+where+
		` ORDER BY lower(a.filename), a.version DESC`, pageID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Attachment, error) {
		var at Attachment
		var aid *int64
		var un, dn *string
		err := row.Scan(&at.ID, &at.PageID, &at.Filename, &at.Version, &at.ContentType, &at.Size, &at.Comment, &aid, &un, &dn, &at.Created)
		if aid != nil {
			at.Author = &identity.UserRef{ID: *aid, Username: *un, DisplayName: *dn, Active: true}
		}
		at.URL = attachmentURL(at.ID)
		at.CanDelete = a.Can(pg.SpaceID, PermAttachDelete) || (aid != nil && *aid == a.userID && a.Can(pg.SpaceID, PermAttach))
		return at, err
	})
	return nonNil(out), err
}

// attachable returns the page if the principal may attach files to it.
func (s *Service) attachable(ctx context.Context, a *Access, pageID int64) (*pageRow, error) {
	pg, err := s.readablePage(ctx, s.pool, a, pageID, false)
	if err != nil {
		return nil, err
	}
	perms, err := s.pagePermissions(ctx, s.pool, a, pg)
	if err != nil {
		return nil, err
	}
	if !perms.Attach && !(pg.Status == StatusDraft && pg.CreatorID == a.userID && a.Can(pg.SpaceID, PermAttach)) {
		return nil, apperr.Forbidden("attachment.denied", "You may not attach files to this page.")
	}
	return pg, nil
}

// UploadLimit checks, before an upload is read, that the principal may attach files to the page and that the server
// has room for n more bytes (0: unknown); it returns the largest file.
func (s *Service) UploadLimit(ctx context.Context, p *identity.Principal, pageID, n int64) (int64, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return 0, err
	}
	if _, err := s.attachable(ctx, a, pageID); err != nil {
		return 0, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return 0, err
	}
	if err := storage.Check(st, s.blobs.Dir(), n); err != nil {
		return 0, err
	}
	return settings.MiB(st.MaxAttachmentMB), nil
}

func attachmentTooLarge(limit int64) error {
	return apperr.New(http.StatusRequestEntityTooLarge, "attachment.too-large", "The file is larger than %s.", storage.Format(limit))
}

// AddAttachment stores a file on a page; a file with the same name becomes a new version.
func (s *Service) AddAttachment(ctx context.Context, p *identity.Principal, pageID int64, filename, contentType, comment string, r io.Reader) (*Attachment, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pg, err := s.attachable(ctx, a, pageID)
	if err != nil {
		return nil, err
	}
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if filename == "." || filename == "/" || filename == "" {
		filename = "file"
	}
	if contentType == "" || contentType == "application/octet-stream" {
		if ct := mime.TypeByExtension(filepath.Ext(filename)); ct != "" {
			contentType = ct
		} else {
			contentType = "application/octet-stream"
		}
	}
	ref, err := s.blobs.Put(ctx, r, settings.MiB(st.MaxAttachmentMB), contentType)
	if errors.Is(err, blob.ErrTooLarge) {
		return nil, attachmentTooLarge(settings.MiB(st.MaxAttachmentMB))
	}
	if err != nil {
		return nil, err
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM pages WHERE id = $1 FOR UPDATE", pageID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO page_attachments (page_id, filename, version, content_type, size, sha256, comment, author_id)
			VALUES ($1, $2, coalesce((SELECT max(version) + 1 FROM page_attachments WHERE page_id = $1 AND filename = $2), 1), $3, $4, $5, $6, $7)
			RETURNING id`, pageID, filename, contentType, ref.Size, ref.SHA256, comment, a.userID).Scan(&id); err != nil {
			return err
		}
		if err := blob.AddRef(ctx, tx, ref.SHA256, "page-attachment", id); err != nil {
			return err
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.attachment", PageID: pageID, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
	if err != nil {
		return nil, err
	}
	list, err := s.Attachments(ctx, p, pageID, true)
	if err != nil {
		return nil, err
	}
	for _, at := range list {
		if at.ID == id {
			return &at, nil
		}
	}
	return nil, apperr.Internal(errors.New("attachment vanished"))
}

func (s *Service) attachmentRow(ctx context.Context, q db.Querier, a *Access, id int64) (*Attachment, string, *pageRow, error) {
	var at Attachment
	var sha string
	var author *int64
	err := q.QueryRow(ctx, `SELECT id, page_id, filename, version, content_type, size, comment, author_id, created_at, sha256
		FROM page_attachments WHERE id = $1`, id).Scan(&at.ID, &at.PageID, &at.Filename, &at.Version, &at.ContentType, &at.Size, &at.Comment,
		&author, &at.Created, &sha)
	if db.IsNoRows(err) {
		return nil, "", nil, apperr.NotFound("attachment.not-found", "The attachment does not exist.")
	}
	if err != nil {
		return nil, "", nil, err
	}
	if author != nil {
		at.Author = &identity.UserRef{ID: *author}
	}
	pg, err := s.readablePage(ctx, q, a, at.PageID, false)
	if err != nil {
		return nil, "", nil, apperr.NotFound("attachment.not-found", "The attachment does not exist.")
	}
	return &at, sha, pg, nil
}

// DeleteAttachment removes all versions of a file from a page.
func (s *Service) DeleteAttachment(ctx context.Context, p *identity.Principal, id int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		at, _, pg, err := s.attachmentRow(ctx, tx, a, id)
		if err != nil {
			return err
		}
		own := at.Author != nil && at.Author.ID == a.userID
		all := a.Can(pg.SpaceID, PermAttachDelete)
		passes, err := s.passesEditRestrictions(ctx, tx, a, pg)
		if err != nil {
			return err
		}
		open := pg.Status != StatusTrashed && pg.Status != StatusArchived
		if !passes || !open || !all && !(own && a.Can(pg.SpaceID, PermAttach)) {
			return apperr.Forbidden("attachment.delete-denied", "You may not delete this attachment.")
		}
		// Without attachment.delete, people delete the versions they uploaded, not those of others.
		var author any
		if !all {
			author = a.userID
		}
		rows, err := tx.Query(ctx, `DELETE FROM page_attachments WHERE page_id = $1 AND filename = $2 AND ($3::bigint IS NULL OR author_id = $3)
			RETURNING id`, at.PageID, at.Filename, author)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, aid := range ids {
			if err := blob.RemoveRefs(ctx, tx, "page-attachment", aid); err != nil {
				return err
			}
		}
		return s.publish(ctx, tx, PageEvent{Name: "page.attachment", PageID: at.PageID, SpaceID: pg.SpaceID, ActorID: a.userID})
	})
}

// --- HTTP: uploads, downloads and exports ---

// Handler serves the wiki API.
type Handler struct {
	S *Service
}

// Routes registers the file endpoints on the authenticated router.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/v1/wiki/pages/{id}/attachments", h.upload)
	r.Get("/api/v1/wiki/attachments/{id}", h.download)
	r.Get("/api/v1/wiki/pages/{id}/export", h.export)
	r.Get("/api/v1/wiki/exports/{id}/download", h.downloadExport)
	r.Get("/api/v1/wiki/pages/{id}/edit/socket", h.draftSocket)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	p := identity.From(r.Context())
	if !p.Authenticated() {
		httpx.WriteProblem(w, r, apperr.Unauthorized("auth.required", "Please sign in."))
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, r, notFoundPage())
		return
	}
	// The request is limited before it is read (who may attach, the free space, the size); a file that is refused
	// ends it, the files before it are kept.
	maxFile, err := h.S.UploadLimit(r.Context(), p, pageID, r.ContentLength)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	if !httpx.LimitUpload(w, r, httpx.UploadLimit(maxFile), attachmentTooLarge(maxFile)) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteProblem(w, r, apperr.BadRequest("attachment.form", "Send the files as multipart/form-data."))
		return
	}
	comment := ""
	var out []*Attachment
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if httpx.TooLarge(err) {
			httpx.WriteProblem(w, r, attachmentTooLarge(maxFile))
			return
		}
		if err != nil {
			httpx.WriteProblem(w, r, apperr.BadRequest("attachment.form", "The upload was interrupted."))
			return
		}
		if part.FormName() == "comment" && part.FileName() == "" {
			b, _ := io.ReadAll(io.LimitReader(part, 2000))
			comment = string(b)
			_ = part.Close()
			continue
		}
		if part.FileName() == "" {
			_ = part.Close()
			continue
		}
		if len(out) == httpx.MaxFiles {
			_ = part.Close()
			httpx.WriteProblem(w, r, httpx.TooManyFiles())
			return
		}
		at, err := h.S.AddAttachment(r.Context(), p, pageID, part.FileName(), part.Header.Get("Content-Type"), comment, part)
		_ = part.Close()
		if httpx.TooLarge(err) {
			err = attachmentTooLarge(maxFile)
		}
		if err != nil {
			httpx.WriteProblem(w, r, err)
			return
		}
		out = append(out, at)
	}
	if len(out) == 0 {
		httpx.WriteProblem(w, r, apperr.BadRequest("attachment.empty", "No file was sent."))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.NotFound("attachment.not-found", "The attachment does not exist."))
		return
	}
	a, err := h.S.Access(r.Context(), identity.From(r.Context()))
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	at, sha, _, err := h.S.attachmentRow(r.Context(), h.S.pool, a, id)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	f, err := h.S.blobs.Open(sha)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.Internal(err))
		return
	}
	defer f.Close()
	httpx.FileHeaders(w, at.ContentType, at.Filename, r.URL.Query().Get("download") != "")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", at.Created, f)
}
