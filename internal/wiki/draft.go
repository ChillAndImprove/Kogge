// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/docmodel/prosemirror"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Shared drafts: everyone editing a page edits one draft, live. Editors send their changes as
// ProseMirror steps that build on a version of the draft; the server applies them in order to its own copy of the
// document (with the editor's schema), so the draft is always the editors' document and never a browser's claim.
// Steps that build on an older version are refused: the editor catches up with the steps of others, rebases its own
// and sends them again. Every node keeps the documents of the drafts in use; the database holds a snapshot with the
// steps since, and notifications (LISTEN/NOTIFY) carry the steps to the editors connected to other nodes.

const (
	// DraftChannel is the notification channel of shared drafts.
	DraftChannel = "kogge_drafts"
	// draftSnapshotEvery is the number of steps between snapshots of a draft's document.
	draftSnapshotEvery = 50
	// draftStepsKept is the number of steps kept before the snapshot, for editors that catch up.
	draftStepsKept = 2000
	// maxDraftSize limits a draft's document (in positions, about characters).
	maxDraftSize = 1_000_000
)

var clientIDPattern = regexp.MustCompile(`^([0-9]{1,19}):[A-Za-z0-9_-]{1,40}$`)

// EditSession is what an editor needs to join the shared draft of a page.
type EditSession struct {
	Version      int                `json:"version" doc:"The steps applied to the draft; changes build on it"`
	Doc          *docmodel.Doc      `json:"doc" doc:"The draft's document as the editor holds it (with all attributes)"`
	Title        string             `json:"title"`
	BaseVersion  int                `json:"baseVersion" doc:"The page version the draft builds on"`
	Changed      bool               `json:"changed" doc:"The draft has changes that are not published"`
	Contributors []identity.UserRef `json:"contributors" doc:"Who changed the draft since it was last published"`
	Updated      time.Time          `json:"updated"`
	UpdatedBy    *identity.UserRef  `json:"updatedBy,omitempty"`
	EarlierDraft *Draft             `json:"earlierDraft,omitempty" doc:"The caller's personal draft from before drafts were shared"`
}

// StepsInput are changes of an editor to a shared draft.
type StepsInput struct {
	Version  int              `json:"version" minimum:"0" doc:"The draft version the steps build on"`
	ClientID string           `json:"clientId" maxLength:"64" doc:"The editor: the user's id, a colon and a random part"`
	Steps    []map[string]any `json:"steps" minItems:"1" maxItems:"500" doc:"Steps in ProseMirror's JSON form"`
}

// StepsResult is the version of the draft after accepted steps.
type StepsResult struct {
	Version int `json:"version"`
}

// DraftStep is a step of a shared draft.
type DraftStep struct {
	Version  int             `json:"version" doc:"The draft's version after the step"`
	Step     json.RawMessage `json:"step" doc:"The step in ProseMirror's JSON form"`
	ClientID string          `json:"clientId"`
}

// DraftSteps are the steps of a shared draft after a version, with what the editors need besides.
type DraftSteps struct {
	Version     int         `json:"version" doc:"The draft's current version"`
	Steps       []DraftStep `json:"steps"`
	Title       string      `json:"title"`
	BaseVersion int         `json:"baseVersion" doc:"The page version the draft builds on (it grows when the draft is published)"`
	Carets      []Caret     `json:"carets" doc:"Where the others are (editors connected recently)"`
}

// Caret is where an editor's selection is, at a version of the draft.
type Caret struct {
	ClientID string `json:"clientId"`
	UserID   int64  `json:"userId"`
	Name     string `json:"name"`
	Version  int    `json:"version"`
	Anchor   int    `json:"anchor"`
	Head     int    `json:"head"`
}

// DraftTitleInput changes the title of a shared draft.
type DraftTitleInput struct {
	Title    string `json:"title" maxLength:"255"`
	ClientID string `json:"clientId" maxLength:"64"`
}

// CaretInput tells the others where an editor's selection is.
type CaretInput struct {
	ClientID string `json:"clientId" maxLength:"64"`
	Version  int    `json:"version" minimum:"0"`
	Anchor   int    `json:"anchor" minimum:"0"`
	Head     int    `json:"head" minimum:"0"`
}

type draftRow struct {
	pageID       int64
	baseVersion  int
	version      int
	title        string
	doc          []byte
	docVersion   int
	changed      bool
	contributors []int64
	updatedAt    time.Time
	updatedBy    *int64
}

// changedByOthers reports whether a draft has unpublished changes of others than a user (which a version written
// another way would discard).
func (d *draftRow) changedByOthers(uid int64) bool {
	if d == nil || !d.changed {
		return false
	}
	if len(d.contributors) == 0 {
		return true
	}
	for _, c := range d.contributors {
		if c != uid {
			return true
		}
	}
	return false
}

const draftColumns = "page_id, base_version, version, title, doc, doc_version, changed, contributors, updated_at, updated_by"

func scanDraft(row pgx.Row) (*draftRow, error) {
	var d draftRow
	err := row.Scan(&d.pageID, &d.baseVersion, &d.version, &d.title, &d.doc, &d.docVersion, &d.changed, &d.contributors,
		&d.updatedAt, &d.updatedBy)
	return &d, err
}

func loadDraft(ctx context.Context, q db.Querier, pageID int64, forUpdate bool) (*draftRow, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	d, err := scanDraft(q.QueryRow(ctx, "SELECT "+draftColumns+" FROM page_shared_drafts WHERE page_id = $1"+lock, pageID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return d, err
}

// draftDocs keeps the documents of the drafts in use on this node, at their latest version.
type draftDocs struct {
	mu   sync.Mutex
	docs map[int64]*draftDoc
}

type draftDoc struct {
	version int
	doc     *prosemirror.Node
	used    time.Time
}

const draftDocsKept = 200

func (c *draftDocs) get(pageID int64, version int) *prosemirror.Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.docs[pageID]; d != nil && d.version == version {
		d.used = time.Now()
		return d.doc
	}
	return nil
}

// latest returns the newest document known for a page at or below a version.
func (c *draftDocs) latest(pageID int64, atMost int) (*prosemirror.Node, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.docs[pageID]; d != nil && d.version <= atMost {
		return d.doc, d.version
	}
	return nil, 0
}

func (c *draftDocs) put(pageID int64, version int, doc *prosemirror.Node) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.docs == nil {
		c.docs = map[int64]*draftDoc{}
	}
	if d := c.docs[pageID]; d != nil && d.version > version {
		return
	}
	c.docs[pageID] = &draftDoc{version: version, doc: doc, used: time.Now()}
	if len(c.docs) > draftDocsKept {
		var oldest int64
		var at time.Time
		for id, d := range c.docs {
			if oldest == 0 || d.used.Before(at) {
				oldest, at = id, d.used
			}
		}
		delete(c.docs, oldest)
	}
}

func (c *draftDocs) drop(pageID int64) {
	c.mu.Lock()
	delete(c.docs, pageID)
	c.mu.Unlock()
}

// parseDraftDoc reads a stored document with the editor's schema; an empty document gets the editor's empty
// paragraph, as editors show it.
func parseDraftDoc(raw []byte) (*prosemirror.Node, error) {
	doc, err := prosemirror.Editor.ParseNode(raw)
	if err != nil {
		return nil, fmt.Errorf("the document does not fit the editor: %w", err)
	}
	if doc.ChildCount() == 0 {
		doc = prosemirror.Editor.EmptyDoc()
	}
	return doc, nil
}

func docOfNode(n *prosemirror.Node) (*docmodel.Doc, error) {
	raw, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	var d docmodel.Doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func nodeOfDoc(d *docmodel.Doc) (*prosemirror.Node, error) {
	if d == nil {
		d = docmodel.Empty()
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return parseDraftDoc(raw)
}

// draftDocAt returns the document of a draft at its current version: from this node's memory, else from the
// snapshot and the steps since.
func (s *Service) draftDocAt(ctx context.Context, q db.Querier, d *draftRow) (*prosemirror.Node, error) {
	if doc := s.drafts.get(d.pageID, d.version); doc != nil {
		return doc, nil
	}
	doc, from := s.drafts.latest(d.pageID, d.version)
	if doc == nil || from < d.docVersion {
		var err error
		if doc, err = parseDraftDoc(d.doc); err != nil {
			return nil, err
		}
		from = d.docVersion
	}
	if from < d.version {
		rows, err := q.Query(ctx, "SELECT version, step FROM page_draft_steps WHERE page_id = $1 AND version > $2 AND version <= $3 ORDER BY version",
			d.pageID, from, d.version)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		next := from + 1
		for rows.Next() {
			var v int
			var raw []byte
			if err := rows.Scan(&v, &raw); err != nil {
				return nil, err
			}
			if v != next {
				return nil, fmt.Errorf("draft of page %d: step %d is missing", d.pageID, next)
			}
			step, err := prosemirror.Editor.ParseStep(raw)
			if err == nil {
				doc, err = prosemirror.Apply(doc, step)
			}
			if err != nil {
				return nil, fmt.Errorf("draft of page %d: step %d: %w", d.pageID, v, err)
			}
			next++
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if next != d.version+1 {
			return nil, fmt.Errorf("draft of page %d: steps up to %d are missing", d.pageID, d.version)
		}
	}
	s.drafts.put(d.pageID, d.version, doc)
	return doc, nil
}

// editablePage loads a page the principal may edit (for unpublished pages: their creator).
func (s *Service) editablePage(ctx context.Context, q db.Querier, a *Access, pageID int64, forUpdate bool) (*pageRow, error) {
	pg, err := s.readablePage(ctx, q, a, pageID, forUpdate)
	if err != nil {
		return nil, err
	}
	if ok, err := s.mayEdit(ctx, a, pg); err != nil {
		return nil, err
	} else if !ok {
		return nil, apperr.Forbidden("page.edit-denied", "You may not edit this page.")
	}
	return pg, nil
}

// ensureDraft returns the shared draft of a page, locked, and starts it from the current version if there is none.
func (s *Service) ensureDraft(ctx context.Context, tx pgx.Tx, pg *pageRow, uid int64) (*draftRow, error) {
	d, err := loadDraft(ctx, tx, pg.ID, true)
	if err != nil || d != nil {
		return d, err
	}
	doc, err := nodeOfDoc(pg.Body)
	if err != nil {
		return nil, apperr.Invalidf("draft.content", "The page cannot be edited: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO page_shared_drafts (page_id, base_version, title, doc, updated_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (page_id) DO NOTHING`, pg.ID, pg.Version, pg.Title, raw, nullID(uid)); err != nil {
		return nil, err
	}
	return loadDraft(ctx, tx, pg.ID, true)
}

// JoinDraft returns the shared draft of a page to edit it (starting it if needed).
func (s *Service) JoinDraft(ctx context.Context, p *identity.Principal, pageID int64) (*EditSession, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	var out *EditSession
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.editablePage(ctx, tx, a, pageID, false)
		if err != nil {
			return err
		}
		d, err := s.ensureDraft(ctx, tx, pg, a.userID)
		if err != nil {
			return err
		}
		doc, err := s.draftDocAt(ctx, tx, d)
		if err != nil {
			return err
		}
		out, err = s.editSession(ctx, tx, a, d, doc)
		return err
	})
	return out, err
}

func (s *Service) editSession(ctx context.Context, q db.Querier, a *Access, d *draftRow, doc *prosemirror.Node) (*EditSession, error) {
	body, err := docOfNode(doc)
	if err != nil {
		return nil, err
	}
	out := &EditSession{Version: d.version, Doc: body, Title: d.title, BaseVersion: d.baseVersion, Changed: d.changed,
		Contributors: []identity.UserRef{}, Updated: d.updatedAt}
	ids := append([]int64{}, d.contributors...)
	if d.updatedBy != nil {
		ids = append(ids, *d.updatedBy)
	}
	refs, err := identity.GetUserRefs(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range d.contributors {
		if r, ok := refs[id]; ok {
			out.Contributors = append(out.Contributors, r)
		}
	}
	if d.updatedBy != nil {
		if r, ok := refs[*d.updatedBy]; ok {
			out.UpdatedBy = &r
		}
	}
	// A personal draft from before drafts were shared, which its author may still want.
	var earlier Draft
	var raw []byte
	var updated time.Time
	err = q.QueryRow(ctx, "SELECT title, body, base_version, updated_at FROM page_drafts WHERE page_id = $1 AND user_id = $2",
		d.pageID, a.userID).Scan(&earlier.Title, &raw, &earlier.BaseVersion, &updated)
	if err == nil {
		earlier.Saved, earlier.Updated, earlier.Body = true, &updated, &docmodel.Doc{}
		if json.Unmarshal(raw, earlier.Body) == nil {
			out.EarlierDraft = &earlier
		}
	} else if !db.IsNoRows(err) {
		return nil, err
	}
	return out, nil
}

// checkClient checks that an editor's id belongs to the caller: editors confirm their own steps by it.
func checkClient(clientID string, uid int64) error {
	m := clientIDPattern.FindStringSubmatch(clientID)
	if m == nil || m[1] != strconv.FormatInt(uid, 10) {
		return apperr.Invalid("draft.client", "The editor's id must start with your user id.")
	}
	return nil
}

func behind(version int) error {
	return apperr.Conflict("draft.behind", "The draft changed meanwhile (now version %d); catch up and send again.", version)
}

// SubmitSteps applies an editor's steps to the shared draft, if they build on its current version.
func (s *Service) SubmitSteps(ctx context.Context, p *identity.Principal, pageID int64, in StepsInput) (*StepsResult, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	// Who may not edit the page learns nothing about it from the steps.
	if _, err := s.editablePage(ctx, s.pool, a, pageID, false); err != nil {
		return nil, err
	}
	if err := checkClient(in.ClientID, a.userID); err != nil {
		return nil, err
	}
	steps := make([]prosemirror.Step, len(in.Steps))
	for i, raw := range in.Steps {
		if steps[i], err = prosemirror.Editor.StepFromJSON(map[string]any(raw)); err != nil {
			return nil, apperr.Invalidf("draft.step", "A change cannot be applied: %v", err)
		}
	}
	opts, err := s.docOptions(ctx)
	if err != nil {
		return nil, err
	}
	var version int
	var doc *prosemirror.Node
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.editablePage(ctx, tx, a, pageID, false)
		if err != nil {
			return err
		}
		d, err := loadDraft(ctx, tx, pg.ID, true)
		if err != nil {
			return err
		}
		if d == nil {
			return apperr.Conflict("draft.gone", "The draft was published or discarded meanwhile; load it again.")
		}
		if in.Version != d.version {
			return behind(d.version)
		}
		if doc, err = s.draftDocAt(ctx, tx, d); err != nil {
			return err
		}
		for _, step := range steps {
			next, err := prosemirror.Apply(doc, step)
			if err != nil {
				return apperr.Invalidf("draft.step", "A change cannot be applied: %v", err)
			}
			if err := checkStep(step, next, opts); err != nil {
				return err
			}
			doc = next
		}
		if doc.Content().Size() > maxDraftSize {
			return apperr.Invalid("draft.too-large", "The page is too large.")
		}
		if doc.Height() > docmodel.MaxDepth {
			return apperr.Invalid("draft.too-deep", "The page is nested too deeply.")
		}
		version, err = s.appendSteps(ctx, tx, d, steps, doc, in.ClientID, a.userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.drafts.put(pageID, version, doc)
	return &StepsResult{Version: version}, nil
}

// checkStep checks what a step brings into a draft by the rules of documents (links, images, issue keys, …).
func checkStep(step prosemirror.Step, after *prosemirror.Node, opts docmodel.Options) error {
	var nodes []*prosemirror.Node
	var mark *prosemirror.Mark
	switch st := step.(type) {
	case *prosemirror.ReplaceStep:
		nodes = st.Slice.Content.Nodes()
	case *prosemirror.ReplaceAroundStep:
		nodes = st.Slice.Content.Nodes()
	case *prosemirror.AddMarkStep:
		mark = st.Mark
	case *prosemirror.AddNodeMarkStep:
		mark = st.Mark
	case *prosemirror.AttrStep:
		if n := after.NodeAt(st.Pos); n != nil {
			nodes = []*prosemirror.Node{n}
		}
	}
	invalid := func(err error) error {
		return apperr.Invalidf("draft.content", "The change is not allowed: %v", err)
	}
	for _, n := range nodes {
		raw, err := json.Marshal(n)
		if err != nil {
			return err
		}
		var node docmodel.Node
		if err := json.Unmarshal(raw, &node); err != nil {
			return err
		}
		if err := docmodel.ValidateNode(&node, 1, opts); err != nil {
			return invalid(err)
		}
	}
	if mark != nil {
		raw, err := json.Marshal(mark.JSON())
		if err != nil {
			return err
		}
		var m docmodel.Mark
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		if err := docmodel.ValidateMark(&m, opts); err != nil {
			return invalid(err)
		}
	}
	return nil
}

// appendSteps stores accepted steps of a locked draft and tells the editors; it returns the new version.
func (s *Service) appendSteps(ctx context.Context, tx pgx.Tx, d *draftRow, steps []prosemirror.Step, doc *prosemirror.Node,
	clientID string, uid int64) (int, error) {
	version := d.version
	encoded := make([]json.RawMessage, len(steps))
	for i, step := range steps {
		raw, err := json.Marshal(step.JSON())
		if err != nil {
			return 0, err
		}
		encoded[i] = raw
		version++
		if _, err := tx.Exec(ctx, "INSERT INTO page_draft_steps (page_id, version, step, client_id, user_id) VALUES ($1, $2, $3, $4, $5)",
			d.pageID, version, raw, clientID, nullID(uid)); err != nil {
			return 0, err
		}
	}
	snapshot, snapshotVersion := []byte(nil), d.docVersion
	if version-d.docVersion >= draftSnapshotEvery {
		raw, err := json.Marshal(doc)
		if err != nil {
			return 0, err
		}
		snapshot, snapshotVersion = raw, version
	}
	if _, err := tx.Exec(ctx, `UPDATE page_shared_drafts SET version = $2, changed = true, updated_at = now(), updated_by = $3,
		contributors = CASE WHEN $3::bigint IS NULL OR $3 = ANY(contributors) THEN contributors ELSE contributors || $3::bigint END,
		doc = coalesce($4, doc), doc_version = $5 WHERE page_id = $1`, d.pageID, version, nullID(uid), snapshot, snapshotVersion); err != nil {
		return 0, err
	}
	if snapshot != nil {
		if _, err := tx.Exec(ctx, "DELETE FROM page_draft_steps WHERE page_id = $1 AND version <= $2", d.pageID,
			snapshotVersion-draftStepsKept); err != nil {
			return 0, err
		}
	}
	return version, s.notifyDraft(ctx, tx, d.pageID, draftEvent{Type: "steps", From: d.version, To: version,
		Steps: stepsOut(encoded, clientID)})
}

func stepsOut(encoded []json.RawMessage, clientID string) []draftStepOut {
	out := make([]draftStepOut, len(encoded))
	for i, raw := range encoded {
		out[i] = draftStepOut{Step: raw, Client: clientID}
	}
	return out
}

// DraftStepsSince returns the steps of a shared draft after a version (for editors that catch up or cannot keep
// a connection open), with the title and where the others are.
func (s *Service) DraftStepsSince(ctx context.Context, p *identity.Principal, pageID int64, since int) (*DraftSteps, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, err
	}
	pg, err := s.editablePage(ctx, s.pool, a, pageID, false)
	if err != nil {
		return nil, err
	}
	d, err := loadDraft(ctx, s.pool, pg.ID, false)
	if err != nil {
		return nil, err
	}
	if d == nil && since == 0 {
		// Nobody has edited the page yet.
		return &DraftSteps{Steps: []DraftStep{}, Title: pg.Title, BaseVersion: pg.Version, Carets: []Caret{}}, nil
	}
	if d == nil {
		return nil, apperr.Conflict("draft.gone", "The draft was published or discarded meanwhile; load it again.")
	}
	if since > d.version {
		return nil, apperr.Conflict("draft.ahead", "The draft has no version %d; load it again.", since)
	}
	steps, err := s.stepsBetween(ctx, s.pool, pageID, since, d.version)
	if err != nil {
		return nil, err
	}
	return &DraftSteps{Version: d.version, Steps: steps, Title: d.title, BaseVersion: d.baseVersion, Carets: s.hub.carets(pageID)}, nil
}

var errStepsGone = apperr.Conflict("draft.reset", "The draft changed too much since; load it again.")

// stepsBetween reads the steps of a draft after one version up to another; errStepsGone if some are no longer kept.
func (s *Service) stepsBetween(ctx context.Context, q db.Querier, pageID int64, after, upTo int) ([]DraftStep, error) {
	out := []DraftStep{}
	if after >= upTo {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT version, step, client_id FROM page_draft_steps WHERE page_id = $1 AND version > $2 AND version <= $3 ORDER BY version",
		pageID, after, upTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st DraftStep
		if err := rows.Scan(&st.Version, &st.Step, &st.ClientID); err != nil {
			return nil, err
		}
		if st.Version != after+len(out)+1 {
			return nil, errStepsGone
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != upTo-after {
		return nil, errStepsGone
	}
	return out, nil
}

// SetDraftTitle changes the title of a shared draft (the last change wins).
func (s *Service) SetDraftTitle(ctx context.Context, p *identity.Principal, pageID int64, in DraftTitleInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if _, err := s.editablePage(ctx, s.pool, a, pageID, false); err != nil {
		return err
	}
	if err := checkClient(in.ClientID, a.userID); err != nil {
		return err
	}
	title := strings.TrimSpace(in.Title)
	if err := validTitle(title); err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		pg, err := s.editablePage(ctx, tx, a, pageID, false)
		if err != nil {
			return err
		}
		d, err := s.ensureDraft(ctx, tx, pg, a.userID)
		if err != nil {
			return err
		}
		if d.title == title {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE page_shared_drafts SET title = $2, changed = true, updated_at = now(), updated_by = $3,
			contributors = CASE WHEN $3 = ANY(contributors) THEN contributors ELSE contributors || $3::bigint END
			WHERE page_id = $1`, pageID, title, a.userID); err != nil {
			return err
		}
		return s.notifyDraft(ctx, tx, pageID, draftEvent{Type: "title", Title: title, Client: in.ClientID,
			User: &draftUser{ID: a.userID, Name: p.User.DisplayName}})
	})
}

// DraftCaret tells the other editors of a draft where an editor's selection is.
func (s *Service) DraftCaret(ctx context.Context, p *identity.Principal, pageID int64, in CaretInput) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if _, err := s.editablePage(ctx, s.pool, a, pageID, false); err != nil {
		return err
	}
	if err := checkClient(in.ClientID, a.userID); err != nil {
		return err
	}
	return s.sendCaret(ctx, p, pageID, in)
}

func (s *Service) sendCaret(ctx context.Context, p *identity.Principal, pageID int64, in CaretInput) error {
	return s.notifyDraft(ctx, s.pool, pageID, draftEvent{Type: "caret", Client: in.ClientID,
		User: &draftUser{ID: p.UserID(), Name: p.User.DisplayName}, Version: in.Version, Anchor: in.Anchor, Head: in.Head})
}

// DropEarlierDraft deletes the caller's personal draft from before drafts were shared.
func (s *Service) DropEarlierDraft(ctx context.Context, p *identity.Principal, pageID int64) error {
	a, err := s.Access(ctx, p)
	if err != nil {
		return err
	}
	if _, err := s.readablePage(ctx, s.pool, a, pageID, false); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, "DELETE FROM page_drafts WHERE page_id = $1 AND user_id = $2", pageID, a.userID)
	return err
}

// replaceDraft replaces the content of a locked draft by a document, as one step of uid, and its title.
func (s *Service) replaceDraft(ctx context.Context, tx pgx.Tx, d *draftRow, body *docmodel.Doc, title string, uid int64) error {
	doc, err := s.draftDocAt(ctx, tx, d)
	if err != nil {
		return err
	}
	with, err := nodeOfDoc(body)
	if err != nil {
		return apperr.Invalidf("draft.content", "The content cannot be edited: %v", err)
	}
	step := prosemirror.ReplaceAll(doc, with)
	next, err := prosemirror.Apply(doc, step)
	if err != nil {
		return apperr.Invalidf("draft.content", "The content cannot be edited: %v", err)
	}
	// This node's memory learns the new version from the database: the transaction may still fail.
	if _, err := s.appendSteps(ctx, tx, d, []prosemirror.Step{step}, next, serverClient, uid); err != nil {
		return err
	}
	if title != d.title {
		if _, err := tx.Exec(ctx, "UPDATE page_shared_drafts SET title = $2 WHERE page_id = $1", d.pageID, title); err != nil {
			return err
		}
		return s.notifyDraft(ctx, tx, d.pageID, draftEvent{Type: "title", Title: title, Client: serverClient})
	}
	return nil
}

// serverClient is the editor id of changes the server makes (publishing through the API, discarding).
const serverClient = "0:server"
