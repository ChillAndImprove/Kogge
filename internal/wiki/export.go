// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/docmodel/pdf"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/api"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/blob"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/httpx"
	"kogge.de/kogge/internal/platform/jobs"
)

// Exports. A page exports at once — as HTML, Markdown or PDF. A space, or a page with the pages below
// it, exports in the background: as a ZIP archive of an HTML site or of Markdown files, both with the pages'
// attachments, or as one PDF with a table of contents. Exports show what the person who asks for them may see
// (space.export is needed besides), and only that person downloads them, until they expire.

// Formats of exports.
const (
	ExportHTML     = "html"
	ExportMarkdown = "markdown"
	ExportPDF      = "pdf"
)

const (
	// exportKeep is how long a finished export can be downloaded.
	exportKeep = 72 * time.Hour
	// maxExportsRunning is how many exports one person may have queued or running at a time.
	maxExportsRunning = 3
	// maxPDFPages is the most pages one PDF holds; larger trees export as HTML or Markdown.
	maxPDFPages = 500
)

// Export is an export of a space or page tree.
type Export struct {
	ID         int64      `json:"id"`
	Space      SpaceRef   `json:"space"`
	Page       *PageRef   `json:"page,omitempty" doc:"The page whose tree is exported; none for the whole space"`
	Format     string     `json:"format" enum:"html,markdown,pdf"`
	Paper      string     `json:"paper" enum:"a4,letter"`
	State      string     `json:"state" enum:"queued,running,done,failed"`
	PagesDone  int        `json:"pagesDone"`
	PagesTotal int        `json:"pagesTotal" doc:"0 until the export knows its pages"`
	Error      string     `json:"error,omitempty" doc:"Why a failed export failed"`
	Filename   string     `json:"filename,omitempty"`
	Size       int64      `json:"size"`
	Created    time.Time  `json:"created"`
	Finished   *time.Time `json:"finished,omitempty"`
	Expires    time.Time  `json:"expires"`
	URL        string     `json:"url,omitempty" doc:"Where a finished export is downloaded"`
}

// NewExport asks for an export.
type NewExport struct {
	Format string `json:"format" enum:"html,markdown,pdf"`
	PageID int64  `json:"pageId,omitempty" doc:"The page whose tree is exported; without it, the whole space with its blog"`
	Paper  string `json:"paper,omitempty" enum:"a4,letter," doc:"Paper size of PDF exports (default A4)"`
}

type exportRow struct {
	id          int64
	spaceID     int64
	pageID      int64
	userID      int64
	adminScope  bool
	format      string
	paper       string
	state       string
	done, total int
	errorText   string
	filename    string
	contentType string
	sha         *string
	size        int64
	created     time.Time
	finished    *time.Time
	expires     time.Time
}

const exportColumns = `e.id, e.space_id, coalesce(e.page_id, 0), e.user_id, e.admin_scope, e.format, e.paper, e.state, e.pages_done,
	e.pages_total, e.error, e.filename, e.content_type, e.sha256, e.size, e.created_at, e.finished_at, e.expires_at`

func scanExport(row pgx.Row) (*exportRow, error) {
	var e exportRow
	err := row.Scan(&e.id, &e.spaceID, &e.pageID, &e.userID, &e.adminScope, &e.format, &e.paper, &e.state, &e.done, &e.total,
		&e.errorText, &e.filename, &e.contentType, &e.sha, &e.size, &e.created, &e.finished, &e.expires)
	return &e, err
}

func notFoundExport() error {
	return apperr.NotFound("export.not-found", "The export does not exist.")
}

// toExport describes an export to its owner.
func (s *Service) toExport(ctx context.Context, q db.Querier, e *exportRow) (*Export, error) {
	sp, err := spaceByID(ctx, q, e.spaceID)
	if err != nil {
		return nil, err
	}
	out := &Export{ID: e.id, Space: SpaceRef{ID: sp.ID, Key: sp.Key, Name: sp.Name}, Format: e.format, Paper: e.paper,
		State: e.state, PagesDone: e.done, PagesTotal: e.total, Error: e.errorText, Filename: e.filename, Size: e.size,
		Created: e.created, Finished: e.finished, Expires: e.expires}
	if e.pageID != 0 {
		var title string
		if err := q.QueryRow(ctx, "SELECT title FROM pages WHERE id = $1", e.pageID).Scan(&title); err != nil && !db.IsNoRows(err) {
			return nil, err
		}
		out.Page = &PageRef{ID: e.pageID, Title: title, Kind: "page", SpaceKey: sp.Key, Status: StatusCurrent}
	}
	if e.state == "done" {
		out.URL = "/api/v1/wiki/exports/" + itoa(e.id) + "/download"
	}
	return out, nil
}

// exportRequested asks for the job of an export.
type exportRequested struct {
	id     int64
	userID int64
}

func (exportRequested) EventName() string { return "wiki.export-requested" }

// LiveMessage tells the owner's browser that the export changed.
func (e exportRequested) LiveMessage() events.LiveMessage {
	return events.LiveMessage{Type: "wiki-export", ID: itoa(e.id), User: e.userID}
}

// StartExport asks for an export of a space or of a page with the pages below it.
func (s *Service) StartExport(ctx context.Context, p *identity.Principal, spaceKey string, in NewExport) (*Export, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	sp, err := spaceByKey(ctx, s.pool, spaceKey)
	if err != nil {
		return nil, err
	}
	if !a.Can(sp.ID, PermView) {
		return nil, apperr.NotFound("space.not-found", "The space %s does not exist or you may not see it.", spaceKey)
	}
	if in.PageID != 0 {
		pg, err := s.readablePage(ctx, s.pool, a, in.PageID, false)
		if err != nil {
			return nil, err
		}
		if pg.SpaceID != sp.ID {
			return nil, notFoundPage()
		}
		if pg.Kind != "page" || pg.Status != StatusCurrent {
			return nil, apperr.Invalidf("export.page", "Only published pages export with the pages below them.")
		}
	}
	if !a.Can(sp.ID, PermExport) {
		return nil, apperr.Forbidden("space.export-denied", "You may not export pages of this space.")
	}
	switch in.Format {
	case ExportHTML, ExportMarkdown, ExportPDF:
	default:
		return nil, apperr.Invalidf("export.format", "Exports are HTML, Markdown or PDF.")
	}
	if in.Paper == "" {
		in.Paper = "a4"
	}
	if in.Paper != "a4" && in.Paper != "letter" {
		return nil, apperr.Invalidf("export.paper", "The paper of a PDF is A4 or Letter.")
	}
	var e *exportRow
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// One person has a few exports in progress at a time (their account row orders concurrent requests).
		if _, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE id = $1 FOR UPDATE", a.userID); err != nil {
			return err
		}
		var running int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM wiki_exports WHERE user_id = $1 AND state IN ('queued', 'running')",
			a.userID).Scan(&running); err != nil {
			return err
		}
		if running >= maxExportsRunning {
			return apperr.New(http.StatusConflict, "export.busy", "You have %d exports in progress; start another when one is done.", running)
		}
		e, err = scanExport(tx.QueryRow(ctx, `INSERT INTO wiki_exports AS e (space_id, page_id, user_id, admin_scope, format, paper, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, now() + $7::interval) RETURNING `+exportColumns, sp.ID, nullID(in.PageID), a.userID,
			p.AllowsScope(identity.ScopeAdmin), in.Format, in.Paper, fmt.Sprintf("%d seconds", int64(exportKeep.Seconds()))))
		if err != nil {
			return err
		}
		details := map[string]any{"format": in.Format}
		if in.PageID != 0 {
			details["page"] = in.PageID
		}
		if err := audit.Record(ctx, tx, audit.Entry{ActorID: a.userID, Action: "space.export", TargetType: "space", TargetID: sp.Key,
			Details: details}); err != nil {
			return err
		}
		return s.publish(ctx, tx, exportRequested{id: e.id, userID: a.userID})
	})
	if err != nil {
		return nil, err
	}
	return s.toExport(ctx, s.pool, e)
}

// ownExport loads an export of the principal; others' exports do not exist for them.
func (s *Service) ownExport(ctx context.Context, q db.Querier, p *identity.Principal, id int64) (*exportRow, error) {
	if !p.Authenticated() {
		return nil, notFoundExport()
	}
	e, err := scanExport(q.QueryRow(ctx, "SELECT "+exportColumns+" FROM wiki_exports e WHERE e.id = $1 AND e.user_id = $2 AND e.expires_at > now()",
		id, p.UserID()))
	if db.IsNoRows(err) {
		return nil, notFoundExport()
	}
	return e, err
}

// Exports lists the principal's exports (of a space), newest first.
func (s *Service) Exports(ctx context.Context, p *identity.Principal, spaceKey string) ([]Export, error) {
	var args db.Args
	where := "e.user_id = " + args.Add(p.UserID()) + " AND e.expires_at > now()"
	if spaceKey != "" {
		where += " AND e.space_id = (SELECT id FROM spaces WHERE upper(key) = upper(" + args.Add(spaceKey) + "))"
	}
	rows, err := s.pool.Query(ctx, "SELECT "+exportColumns+" FROM wiki_exports e WHERE "+where+" ORDER BY e.created_at DESC, e.id DESC LIMIT 50", args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*exportRow, error) { return scanExport(row) })
	if err != nil {
		return nil, err
	}
	out := []Export{}
	for _, e := range list {
		x, err := s.toExport(ctx, s.pool, e)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, nil
}

// GetExport returns an export of the principal.
func (s *Service) GetExport(ctx context.Context, p *identity.Principal, id int64) (*Export, error) {
	e, err := s.ownExport(ctx, s.pool, p, id)
	if err != nil {
		return nil, err
	}
	return s.toExport(ctx, s.pool, e)
}

// DeleteExport deletes an export of the principal (a running one stops at its next page).
func (s *Service) DeleteExport(ctx context.Context, p *identity.Principal, id int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.ownExport(ctx, tx, p, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM wiki_exports WHERE id = $1", id); err != nil {
			return err
		}
		if err := blob.RemoveRefs(ctx, tx, "wiki-export", id); err != nil {
			return err
		}
		return events.NotifyLive(ctx, tx, exportRequested{id: id, userID: p.UserID()}.LiveMessage())
	})
}

// exportFile returns a finished export for download. Who lost the permission to export the space, or to see the
// tree's page, no longer downloads it.
func (s *Service) exportFile(ctx context.Context, p *identity.Principal, id int64) (*exportRow, error) {
	e, err := s.ownExport(ctx, s.pool, p, id)
	if err != nil {
		return nil, err
	}
	if e.state != "done" || e.sha == nil {
		return nil, apperr.New(http.StatusConflict, "export.not-ready", "The export is not finished yet.")
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	if !a.Can(e.spaceID, PermView) || !a.Can(e.spaceID, PermExport) {
		return nil, notFoundExport()
	}
	if e.pageID != 0 {
		if _, err := s.readablePage(ctx, s.pool, a, e.pageID, false); err != nil {
			return nil, notFoundExport()
		}
	}
	return e, nil
}

// PurgeExports deletes expired exports; their files go with the blob collection. It returns how many it deleted.
func (s *Service) PurgeExports(ctx context.Context) (int, error) {
	n := 0
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "DELETE FROM wiki_exports WHERE expires_at <= now() RETURNING id")
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := blob.RemoveRefs(ctx, tx, "wiki-export", id); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// --- the job ---

// ExportArgs writes an export.
type ExportArgs struct {
	ExportID int64 `json:"exportId"`
}

// Kind implements river.JobArgs.
func (ExportArgs) Kind() string { return "wiki.export" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ExportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueExport, MaxAttempts: 3}
}

type exportWorker struct {
	river.WorkerDefaults[ExportArgs]
	s *Service
}

func (w *exportWorker) Timeout(*river.Job[ExportArgs]) time.Duration { return 2 * time.Hour }

func (w *exportWorker) Work(ctx context.Context, job *river.Job[ExportArgs]) error {
	err := w.s.runExport(ctx, job.Args.ExportID)
	var final *exportFailure
	if err == nil {
		return nil
	}
	if errors.As(err, &final) || job.Attempt >= job.MaxAttempts {
		// The export fails for good: its owner sees why, and may start another.
		w.s.log.Warn("export failed", "export", job.Args.ExportID, "error", err)
		return w.s.failExport(context.WithoutCancel(ctx), job.Args.ExportID, err)
	}
	return err
}

// ExportCleanupArgs deletes expired exports.
type ExportCleanupArgs struct{}

// Kind implements river.JobArgs.
func (ExportCleanupArgs) Kind() string { return "wiki.export-cleanup" }

type exportCleanupWorker struct {
	river.WorkerDefaults[ExportCleanupArgs]
	s *Service
}

func (w *exportCleanupWorker) Work(ctx context.Context, _ *river.Job[ExportCleanupArgs]) error {
	n, err := w.s.PurgeExports(ctx)
	if n > 0 {
		w.s.log.Info("deleted expired exports", "exports", n)
	}
	return err
}

// Register adds the wiki's jobs: exports, and the cleanup of expired ones.
func (s *Service) Register(reg *jobs.Registry) {
	river.AddWorker(reg.Workers, &exportWorker{s: s})
	river.AddWorker(reg.Workers, &exportCleanupWorker{s: s})
	reg.Every(time.Hour, ExportCleanupArgs{}, nil)
	s.bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if e, ok := ev.(exportRequested); ok {
			return ExportArgs{ExportID: e.id}, nil
		}
		return nil, nil
	}, "wiki.export-requested")
}

// exportFailure is a reason an export cannot succeed, however often it is tried.
type exportFailure struct{ err *apperr.Error }

func (e *exportFailure) Error() string { return e.err.Error() }

func failure(err *apperr.Error) error { return &exportFailure{err: err} }

// failExport records why an export failed, in the words of its owner's language.
func (s *Service) failExport(ctx context.Context, id int64, cause error) error {
	reason := apperr.Invalidf("export.failed", "The export failed.")
	var final *exportFailure
	if errors.As(cause, &final) {
		reason = final.err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var uid int64
		var locale string
		err := tx.QueryRow(ctx, `SELECT e.user_id, coalesce(u.locale, '') FROM wiki_exports e JOIN users u ON u.id = e.user_id
			WHERE e.id = $1 AND e.state <> 'done' FOR UPDATE OF e`, id).Scan(&uid, &locale)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if locale != "de" && locale != "en" {
			if st, err := s.settings.Get(ctx); err == nil {
				locale = st.DefaultLocale
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE wiki_exports SET state = 'failed', error = $2, finished_at = now(), expires_at = now() + $3::interval
			WHERE id = $1`, id, reason.Localize(locale).Detail, fmt.Sprintf("%d seconds", int64(exportKeep.Seconds()))); err != nil {
			return err
		}
		return events.NotifyLive(ctx, tx, exportRequested{id: id, userID: uid}.LiveMessage())
	})
}

// progress records how far an export is, at most every second, and tells its owner's browser.
type progress struct {
	s      *Service
	id     int64
	userID int64
	mu     sync.Mutex
	last   time.Time
}

func (pr *progress) report(ctx context.Context, done, total int, force bool) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if !force && time.Since(pr.last) < time.Second {
		return nil
	}
	pr.last = time.Now()
	return db.InTx(ctx, pr.s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE wiki_exports SET pages_done = $2, pages_total = $3 WHERE id = $1", pr.id, done, total)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errExportGone
		}
		return events.NotifyLive(ctx, tx, exportRequested{id: pr.id, userID: pr.userID}.LiveMessage())
	})
}

// errExportGone ends an export whose owner deleted it.
var errExportGone = errors.New("the export was deleted")

// runExport writes an export: it collects the pages its owner may see, renders them into a file and stores it.
func (s *Service) runExport(ctx context.Context, id int64) error {
	e, err := scanExport(s.pool.QueryRow(ctx, "SELECT "+exportColumns+" FROM wiki_exports e WHERE e.id = $1", id))
	if db.IsNoRows(err) {
		return nil // deleted
	}
	if err != nil {
		return err
	}
	if e.state == "done" || e.state == "failed" {
		return nil
	}
	if _, err := s.pool.Exec(ctx, "UPDATE wiki_exports SET state = 'running', pages_done = 0 WHERE id = $1", id); err != nil {
		return err
	}
	p, err := identity.LoadPrincipal(ctx, s.pool, e.userID, identity.MethodInternal)
	if err != nil {
		if apperr.Is(err, "auth.user-inactive") {
			return failure(apperr.Invalidf("export.inactive", "The account that asked for the export is not active."))
		}
		return err
	}
	if !e.adminScope {
		// Asked for with an access token without the admin scope: the export sees what that token sees.
		p.Method, p.Scopes = identity.MethodToken, []string{identity.ScopeRead}
	}
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if !a.Can(e.spaceID, PermView) || !a.Can(e.spaceID, PermExport) {
		return failure(apperr.Invalidf("export.denied", "You may no longer export pages of this space."))
	}
	sp, err := spaceByID(ctx, s.pool, e.spaceID)
	if err != nil {
		return err
	}
	x, err := s.newExporter(ctx, p, a, sp, e.format)
	if err != nil {
		return err
	}
	tree, err := x.collect(e.pageID)
	if err != nil {
		return err
	}
	if e.format == ExportPDF && len(tree) > maxPDFPages {
		return failure(apperr.Invalidf("export.too-large", "A PDF holds at most %d pages; export the %d pages as HTML or Markdown, or a part of the tree.",
			maxPDFPages, len(tree)))
	}
	pr := &progress{s: s, id: id, userID: e.userID}
	x.progress = func(done int) error { return pr.report(ctx, done, len(tree), false) }
	if err := pr.report(ctx, 0, len(tree), true); err != nil {
		if errors.Is(err, errExportGone) {
			return nil
		}
		return err
	}
	name, contentType := x.fileName(tree, e.pageID)
	ref, err := s.storeExport(ctx, func(w io.Writer) error {
		if e.format == ExportPDF {
			return x.writePDF(w, tree, e.paper, e.pageID)
		}
		return x.writeZip(w, tree, e.pageID)
	}, contentType)
	if errors.Is(err, errExportGone) {
		return nil
	}
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE wiki_exports SET state = 'done', pages_done = pages_total, filename = $2, content_type = $3,
			sha256 = $4, size = $5, finished_at = now(), expires_at = now() + $6::interval WHERE id = $1`,
			id, name, contentType, ref.SHA256, ref.Size, fmt.Sprintf("%d seconds", int64(exportKeep.Seconds())))
		if err != nil || tag.RowsAffected() == 0 {
			return err // deleted in the meantime: the blob stays unreferenced and is collected
		}
		if err := blob.AddRef(ctx, tx, ref.SHA256, "wiki-export", id); err != nil {
			return err
		}
		return events.NotifyLive(ctx, tx, exportRequested{id: id, userID: e.userID}.LiveMessage())
	})
}

// storeExport writes a file with write and stores it as a blob, without a copy on the disk in between.
func (s *Service) storeExport(ctx context.Context, write func(io.Writer) error, contentType string) (blob.Ref, error) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := write(pw)
		pw.CloseWithError(err)
		done <- err
	}()
	ref, err := s.blobs.Put(ctx, pr, 0, contentType)
	// Storing may fail before the writer is done; the writer then stops at its next write.
	pr.CloseWithError(errNotStored)
	if werr := <-done; werr != nil && !errors.Is(werr, errNotStored) {
		return blob.Ref{}, werr
	}
	return ref, err
}

var errNotStored = errors.New("the export was not stored")

// --- HTTP ---

func (h *Handler) registerExports(a huma.API) {
	s := h.S
	start := api.Op("start-space-export", http.MethodPost, "/api/v1/wiki/spaces/{key}/exports", "Export a space or a page tree", tagSpaces)
	start.DefaultStatus = http.StatusAccepted
	start.Description = "Starts an export in the background: an HTML site or Markdown files (ZIP, with attachments), or one PDF. " +
		"It contains the pages the caller may see; get-export tells how far it is, its url downloads it."
	huma.Register(a, start, func(ctx context.Context, in *struct {
		Key  string `path:"key"`
		Body NewExport
	}) (*body[*Export], error) {
		p, err := signedIn(ctx)
		if err != nil {
			return nil, err
		}
		return out(s.StartExport(ctx, p, in.Key, in.Body))
	})
	huma.Register(a, api.Op("list-exports", http.MethodGet, "/api/v1/wiki/exports", "My exports", tagSpaces),
		func(ctx context.Context, in *struct {
			Space string `query:"space" doc:"Only the exports of a space"`
		}) (*body[[]Export], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.Exports(ctx, p, in.Space))
		})
	huma.Register(a, api.Op("get-export", http.MethodGet, "/api/v1/wiki/exports/{id}", "An export of mine", tagSpaces),
		func(ctx context.Context, in *idPath) (*body[*Export], error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return out(s.GetExport(ctx, p, in.ID))
		})
	huma.Register(a, api.Op("delete-export", http.MethodDelete, "/api/v1/wiki/exports/{id}", "Delete an export of mine", tagSpaces),
		func(ctx context.Context, in *idPath) (*struct{}, error) {
			p, err := signedIn(ctx)
			if err != nil {
				return nil, err
			}
			return nil, s.DeleteExport(ctx, p, in.ID)
		})
}

func (h *Handler) downloadExport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, r, notFoundExport())
		return
	}
	e, err := h.S.exportFile(r.Context(), identity.From(r.Context()), id)
	if err != nil {
		httpx.WriteProblem(w, r, err)
		return
	}
	f, err := h.S.blobs.Open(*e.sha)
	if err != nil {
		httpx.WriteProblem(w, r, apperr.Internal(err))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", e.contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": e.filename}))
	w.Header().Set("Content-Security-Policy", httpx.SandboxCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	finished := e.created
	if e.finished != nil {
		finished = *e.finished
	}
	http.ServeContent(w, r, "", finished, f)
}

// export writes one page as HTML (one file, with its images), Markdown or PDF.
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, r, notFoundPage())
		return
	}
	format := r.URL.Query().Get("format")
	switch format {
	case "md", ExportMarkdown:
		format = ExportMarkdown
	case ExportPDF:
	default:
		format = ExportHTML
	}
	data, name, contentType, err := h.S.ExportPage(r.Context(), identity.From(r.Context()), id, format, r.URL.Query().Get("paper"))
	if err != nil {
		if apperr.Is(err, "export.pdf-busy") {
			w.Header().Set("Retry-After", strconv.Itoa(int(pdfRetryAfter.Seconds())))
		}
		httpx.WriteProblem(w, r, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Security-Policy", httpx.SandboxCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(data)
}

// pdfSlots lets a few pages at a time become PDFs, while further requests wait for their turn: rendering takes
// processor time, and memory for the images. (Exports of spaces and trees are jobs, with workers of their own.)
var pdfSlots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)/2))

// pdfWait is how long a request waits for its turn. Then it is answered that the server is busy, so that requests
// do not pile up while many people, or a script, ask for PDFs; pdfRetryAfter is the pause the answer asks for.
var pdfWait = 30 * time.Second

const pdfRetryAfter = 30 * time.Second

// pdfTurn waits for a turn to render a PDF, at most pdfWait; release ends the turn.
func pdfTurn(ctx context.Context) (release func(), err error) {
	wait := time.NewTimer(pdfWait)
	defer wait.Stop()
	select {
	case pdfSlots <- struct{}{}:
		return func() { <-pdfSlots }, nil
	case <-wait.C:
		return nil, apperr.New(http.StatusServiceUnavailable, "export.pdf-busy",
			"Many pages are becoming PDFs at the moment; try again in half a minute.")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ExportPage renders a page the principal may read and export, in a format; it returns the file with its name and
// type.
func (s *Service) ExportPage(ctx context.Context, p *identity.Principal, id int64, format, paper string) ([]byte, string, string, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, "", "", err
	}
	pg, err := s.readablePage(ctx, s.pool, a, id, false)
	if err != nil {
		return nil, "", "", err
	}
	if !a.Can(pg.SpaceID, PermExport) {
		return nil, "", "", apperr.Forbidden("space.export-denied", "You may not export pages of this space.")
	}
	sp, err := spaceByID(ctx, s.pool, pg.SpaceID)
	if err != nil {
		return nil, "", "", err
	}
	x, err := s.newExporter(ctx, p, a, sp, format)
	if err != nil {
		return nil, "", "", err
	}
	x.single = true
	x.pages[pg.ID] = &exportPage{id: pg.ID, title: pg.Title}
	pg, d, err := x.page(pg.ID)
	if err != nil {
		return nil, "", "", err
	}
	name := slug(pg.Title)
	if name == "" {
		name = "page-" + itoa(pg.ID)
	}
	var out []byte
	var contentType string
	switch format {
	case ExportMarkdown:
		md, err := x.markdownPage(pg, d, false)
		if err != nil {
			return nil, "", "", err
		}
		out, contentType, name = []byte(md), "text/markdown; charset=utf-8", name+".md"
	case ExportPDF:
		release, err := pdfTurn(ctx)
		if err != nil {
			return nil, "", "", err
		}
		defer release()
		var buf bytes.Buffer
		o := x.pdfOptions(paper, pg.Title, sp.Name, false, nil)
		if err := pdf.Write(&buf, []pdf.Section{{ID: sectionID(pg.ID), Title: pg.Title, Meta: x.meta(pg), Doc: d}},
			pdfResources{x: x}, o); err != nil {
			return nil, "", "", err
		}
		out, contentType, name = buf.Bytes(), "application/pdf", name+".pdf"
	default:
		out, contentType, name = []byte(x.htmlPage(pg, d, "", "")), "text/html; charset=utf-8", name+".html"
	}
	if a.userID != 0 {
		if err := audit.Record(ctx, s.pool, audit.Entry{ActorID: a.userID, Action: "page.export", TargetType: "page", TargetID: itoa(pg.ID),
			Details: map[string]any{"format": format, "space": sp.Key}}); err != nil {
			return nil, "", "", err
		}
	}
	return out, name, contentType, nil
}

// exportLanguage returns the language and time zone of a person, for the words and dates exports contain.
func (s *Service) exportLanguage(ctx context.Context, p *identity.Principal) (string, *time.Location) {
	st, _ := s.settings.Get(ctx)
	lang, zone := "", ""
	if p.User != nil {
		lang, zone = p.User.Locale, p.User.TimeZone
	}
	if lang != "de" && lang != "en" {
		lang = st.DefaultLocale
	}
	if lang != "de" {
		lang = "en"
	}
	if zone == "" {
		zone = st.DefaultTimeZone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return lang, loc
}

// publicBase is the instance's address without a trailing slash, for links of exports to it.
func (s *Service) publicBase() string {
	if s.public == nil {
		return ""
	}
	return strings.TrimRight(s.public.String(), "/")
}

// SetPublicURL tells the service the instance's address (links of exports lead there).
func (s *Service) SetPublicURL(u *url.URL) { s.public = u }
