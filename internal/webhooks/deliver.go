// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/metrics"
	"kogge.de/kogge/internal/platform/netpolicy"
	"kogge.de/kogge/internal/wiki"
)

const (
	maxAttempts    = 8 // with River's backoff, the last attempt is about two and a half hours after the first
	requestTimeout = 15 * time.Second
	excerptSize    = 2048
)

// Delivery is one attempt to deliver an event to a webhook (with its retries).
type Delivery struct {
	ID              int64           `json:"id"`
	EventID         string          `json:"eventId"`
	Event           string          `json:"event"`
	State           string          `json:"state" enum:"pending,succeeded,failed"`
	Attempts        int             `json:"attempts"`
	ResponseStatus  *int            `json:"responseStatus,omitempty"`
	ResponseExcerpt string          `json:"responseExcerpt,omitempty"`
	Error           string          `json:"error,omitempty"`
	DurationMs      *int            `json:"durationMs,omitempty"`
	Created         time.Time       `json:"created"`
	Delivered       *time.Time      `json:"delivered,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	PayloadHidden   bool            `json:"payloadHidden,omitempty" doc:"The payload is about something the reader may not see"`

	subject subject
}

// subject is what an event is about. Its payload shows it, so it goes only to webhooks whose owner may see it, and
// the delivery log shows it only to people who may see it.
type subject struct {
	kind string // none, issue, page or repository; empty for deliveries from before subjects were recorded
	id   int64
}

func subjectOf(a EventArgs) subject {
	switch {
	case a.IssueKey != "" && a.Event == "issue.deleted":
		return subject{kind: "none"} // the payload has only the id and key
	case a.IssueKey != "":
		return subject{kind: "issue", id: a.IssueID}
	case a.PageID != 0:
		return subject{kind: "page", id: a.PageID}
	case a.RepoID != 0:
		return subject{kind: "repository", id: a.RepoID} // runs and deployments are seen with their repository
	}
	return subject{kind: "none"}
}

func (s *Service) deliveries(ctx context.Context, hookID int64, limit int) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, event_id::text, event, state, attempts, response_status, response_excerpt, error, duration_ms,
		created_at, delivered_at, payload, coalesce(subject_type, ''), coalesce(subject_id, 0)
		FROM webhook_deliveries WHERE webhook_id = $1 ORDER BY id DESC LIMIT $2`, hookID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Delivery, error) {
		var d Delivery
		var payload []byte
		err := r.Scan(&d.ID, &d.EventID, &d.Event, &d.State, &d.Attempts, &d.ResponseStatus, &d.ResponseExcerpt, &d.Error, &d.DurationMs,
			&d.Created, &d.Delivered, &payload, &d.subject.kind, &d.subject.id)
		d.Payload = payload
		return d, err
	})
}

// Deliveries lists the latest deliveries of a webhook. Payloads are shown only to readers who may see what they are
// about — administering the webhook's scope is not enough — and showing them is audited.
func (s *Service) Deliveries(ctx context.Context, p *identity.Principal, id int64, limit int) ([]Delivery, error) {
	if _, err := s.hookForAdmin(ctx, p, id); err != nil {
		return nil, err
	}
	list, err := s.deliveries(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Delivery{}
	}
	seen := map[subject]bool{}
	shown := 0
	for i := range list {
		d := &list[i]
		ok, known := seen[d.subject]
		if !known {
			if ok, err = s.canSee(ctx, p.UserID(), d.subject); err != nil {
				return nil, err
			}
			seen[d.subject] = ok
		}
		switch {
		case !ok:
			d.Payload, d.PayloadHidden = nil, true
		case d.subject.kind != "none":
			shown++
		}
	}
	if shown > 0 {
		if err := audit.Record(ctx, s.pool, audit.Entry{ActorID: p.UserID(), Action: "webhook.deliveries.read", TargetType: "webhook",
			TargetID: strconv.FormatInt(id, 10), Details: map[string]any{"payloads": shown}}); err != nil {
			return nil, err
		}
	}
	// Targets may be internal services: what they answered is for Kogge's administrators, so that a webhook does
	// not become a way to read them.
	if admin, aerr := s.authz.IsAdmin(ctx, p); aerr != nil || !admin {
		for i := range list {
			list[i].ResponseExcerpt = ""
			if list[i].Error != "" && list[i].ResponseStatus == nil {
				list[i].Error = "The target could not be reached."
			}
		}
	}
	return list, nil
}

// canSee reports whether an active user may see the subject of an event. Deliveries from before subjects were
// recorded are seen by nobody.
func (s *Service) canSee(ctx context.Context, userID int64, subj subject) (bool, error) {
	switch subj.kind {
	case "none":
		return true, nil
	case "issue":
		i, err := s.issues.IssueInfo(ctx, subj.id)
		if err != nil {
			if apperr.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return s.issues.UserCanSeeIssue(ctx, userID, i.Key)
	case "page":
		return s.wiki.UserCanReadPage(ctx, userID, subj.id)
	case "repository":
		p, err := identity.LoadPrincipal(ctx, s.pool, userID, identity.MethodInternal)
		if err != nil {
			if inactive(err) {
				return false, nil
			}
			return false, err
		}
		level, err := s.code.AccessLevel(ctx, p, subj.id)
		if err != nil {
			if db.IsNoRows(err) {
				return false, nil
			}
			return false, err
		}
		return level >= code.LevelRead, nil
	}
	return false, nil
}

func inactive(err error) bool {
	return apperr.Is(err, "auth.user-inactive") || db.IsNoRows(err) || apperr.IsNotFound(err)
}

// mayDeliver returns why a webhook may not send an event now, or "" if it may: its owner must be active, still
// administer the webhook's scope and see what the event is about.
func (s *Service) mayDeliver(ctx context.Context, h *hookRow, subj subject) (string, error) {
	if h.ownerID == 0 {
		return "The webhook has no owner; an administrator of its scope must enter its address again.", nil
	}
	owner, err := identity.LoadPrincipal(ctx, s.pool, h.ownerID, identity.MethodInternal)
	if err != nil {
		if inactive(err) {
			return "The owner of the webhook is no longer active.", nil
		}
		return "", err
	}
	key, err := s.scopeKey(ctx, h.Scope, h.scopeID)
	if err != nil {
		return "The scope of the webhook no longer exists.", nil
	}
	if _, err := s.scopeOf(ctx, owner, h.Scope, key); err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return "The owner of the webhook no longer administers its scope.", nil
		}
		return "", err
	}
	ok, err := s.canSee(ctx, h.ownerID, subj)
	if err != nil {
		return "", err
	}
	if !ok {
		return "The owner of the webhook may not see what the event is about.", nil
	}
	return "", nil
}

// deliveryEvent asks the bus to start the delivery job in the transaction that created the delivery.
type deliveryEvent struct{ id int64 }

func (deliveryEvent) EventName() string { return "webhook.delivery" }

func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// enqueue stores a delivery and starts it.
func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, hookID int64, eventID, event string, payload []byte, subj subject) (int64, error) {
	var id int64
	var subjectID any
	if subj.id != 0 {
		subjectID = subj.id
	}
	if err := tx.QueryRow(ctx, `INSERT INTO webhook_deliveries (webhook_id, event_id, event, payload, subject_type, subject_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, hookID, eventID, event, payload, subj.kind, subjectID).Scan(&id); err != nil {
		return 0, err
	}
	return id, s.bus.Publish(ctx, tx, deliveryEvent{id: id})
}

// Ping sends a test event to a webhook.
func (s *Service) Ping(ctx context.Context, p *identity.Principal, id int64) (int64, error) {
	h, err := s.hookForAdmin(ctx, p, id)
	if err != nil {
		return 0, err
	}
	eventID := newEventID()
	payload, _ := json.Marshal(map[string]any{"id": eventID, "event": "ping", "timestamp": time.Now().UTC(),
		"actor": userRef(ctx, s, p.UserID()), "webhook": map[string]any{"id": h.ID, "name": h.Name, "scope": h.Scope, "scopeKey": h.ScopeKey}})
	var deliveryID int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		deliveryID, err = s.enqueue(ctx, tx, h.ID, eventID, "ping", payload, subject{kind: "none"})
		return err
	})
	return deliveryID, err
}

// Redeliver sends the payload of an earlier delivery again (as a new delivery with the same event id).
func (s *Service) Redeliver(ctx context.Context, p *identity.Principal, deliveryID int64) (int64, error) {
	var hookID int64
	var eventID, event string
	var payload []byte
	var subj subject
	err := s.pool.QueryRow(ctx, `SELECT webhook_id, event_id::text, event, payload, coalesce(subject_type, ''), coalesce(subject_id, 0)
		FROM webhook_deliveries WHERE id = $1`, deliveryID).Scan(&hookID, &eventID, &event, &payload, &subj.kind, &subj.id)
	if db.IsNoRows(err) {
		return 0, apperr.NotFound("webhook-delivery.not-found", "The delivery does not exist.")
	}
	if err != nil {
		return 0, err
	}
	h, err := s.hookForAdmin(ctx, p, hookID)
	if apperr.IsNotFound(err) {
		// The deliveries of webhooks one cannot manage do not exist for one.
		return 0, apperr.NotFound("webhook-delivery.not-found", "The delivery does not exist.")
	}
	if err != nil {
		return 0, err
	}
	// The payload goes to the webhook's current address: its current owner must be allowed to receive it.
	if reason, err := s.mayDeliver(ctx, h, subj); err != nil {
		return 0, err
	} else if reason != "" {
		return 0, apperr.Conflict("webhook.cannot-deliver", "%s", reason)
	}
	var id int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if id, err = s.enqueue(ctx, tx, hookID, eventID, event, payload, subj); err != nil {
			return err
		}
		// A delivery that is still being retried stops: the new one replaces it.
		_, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET state = 'failed', error = 'Replaced by delivery ' || $2::text || '.'
			WHERE id = $1 AND state = 'pending'`, deliveryID, strconv.FormatInt(id, 10))
		return err
	})
	return id, err
}

// EventArgs carries an event of a module to the webhooks that subscribe to it.
type EventArgs struct {
	Event     string           `json:"event"`
	EventID   string           `json:"eventId"`
	At        time.Time        `json:"at"`
	ActorID   int64            `json:"actorId,omitempty"`
	IssueID   int64            `json:"issueId,omitempty"`
	IssueKey  string           `json:"issueKey,omitempty"`
	ProjectID int64            `json:"projectId,omitempty"`
	Fields    []string         `json:"fields,omitempty"`
	From      int64            `json:"from,omitempty"`
	To        int64            `json:"to,omitempty"`
	PageID    int64            `json:"pageId,omitempty"`
	SpaceID   int64            `json:"spaceId,omitempty"`
	RepoID    int64            `json:"repoId,omitempty"`
	PRID      int64            `json:"prId,omitempty"`
	Updates   []code.RefUpdate `json:"updates,omitempty"`
	RunID     int64            `json:"runId,omitempty"`
	DeployID  int64            `json:"deployId,omitempty"`
	Env       string           `json:"env,omitempty"`
	Commit    string           `json:"commit,omitempty"`
	// FromKey and FromProjectID are the key and project of a moved issue before the move.
	FromKey       string `json:"fromKey,omitempty"`
	FromProjectID int64  `json:"fromProjectId,omitempty"`
}

// Kind implements river.JobArgs.
func (EventArgs) Kind() string { return "webhooks.event" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (EventArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueEvents} }

// DeliverArgs delivers one delivery.
type DeliverArgs struct {
	DeliveryID int64 `json:"deliveryId"`
}

// Kind implements river.JobArgs.
func (DeliverArgs) Kind() string { return "webhooks.deliver" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (DeliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueEvents, MaxAttempts: maxAttempts}
}

// CleanupArgs deletes deliveries older than 30 days.
type CleanupArgs struct{}

// Kind implements river.JobArgs.
func (CleanupArgs) Kind() string { return "webhooks.cleanup" }

type eventWorker struct {
	river.WorkerDefaults[EventArgs]
	s *Service
}

func (w *eventWorker) Work(ctx context.Context, job *river.Job[EventArgs]) error {
	return w.s.fanOut(ctx, job.Args)
}

type deliverWorker struct {
	river.WorkerDefaults[DeliverArgs]
	s *Service
}

func (w *deliverWorker) Timeout(*river.Job[DeliverArgs]) time.Duration { return time.Minute }

func (w *deliverWorker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	return w.s.deliver(ctx, job.Args.DeliveryID, job.Attempt >= job.MaxAttempts)
}

type cleanupWorker struct {
	river.WorkerDefaults[CleanupArgs]
	s *Service
}

func (w *cleanupWorker) Work(ctx context.Context, _ *river.Job[CleanupArgs]) error {
	_, err := w.s.pool.Exec(ctx, "DELETE FROM webhook_deliveries WHERE created_at < now() - interval '30 days'")
	return err
}

// Register adds the jobs and subscribes to the events of the modules.
func (s *Service) Register(reg *jobs.Registry, bus *events.Bus) {
	river.AddWorker(reg.Workers, &eventWorker{s: s})
	river.AddWorker(reg.Workers, &deliverWorker{s: s})
	river.AddWorker(reg.Workers, &cleanupWorker{s: s})
	reg.Every(24*time.Hour, CleanupArgs{}, nil)
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		if d, ok := ev.(deliveryEvent); ok {
			return DeliverArgs{DeliveryID: d.id}, nil
		}
		return nil, nil
	}, "webhook.delivery")
	names := make([]string, 0, len(Catalogue))
	for _, e := range Catalogue {
		names = append(names, e.Name)
	}
	bus.Subscribe(func(ev events.Event) (river.JobArgs, *river.InsertOpts) {
		a := EventArgs{Event: ev.EventName(), EventID: newEventID(), At: time.Now().UTC()}
		switch e := ev.(type) {
		case issues.IssueEvent:
			a.ActorID, a.IssueID, a.IssueKey, a.ProjectID, a.Fields, a.From, a.To = e.ActorID, e.IssueID, e.Key, e.ProjectID, e.Fields, e.From, e.To
			a.FromKey, a.FromProjectID = e.FromKey, e.FromProjectID
		case wiki.PageEvent:
			a.ActorID, a.PageID, a.SpaceID = e.ActorID, e.PageID, e.SpaceID
		case code.RepoEvent:
			a.ActorID, a.RepoID, a.PRID, a.Updates = e.ActorID, e.RepoID, e.PRID, e.Updates
		case builds.RunEvent:
			a.RunID, a.RepoID = e.RunID, e.RepoID
		case builds.DeploymentEvent:
			a.RunID, a.RepoID, a.DeployID, a.Env, a.Commit = e.RunID, e.RepoID, e.DeploymentID, e.Environment, e.Commit
		default:
			return nil, nil
		}
		return a, nil
	}, names...)
}

// fanOut finds the webhooks that subscribe to an event and starts a delivery for each.
func (s *Service) fanOut(ctx context.Context, a EventArgs) error {
	var codeProject int64
	if a.RepoID != 0 {
		r, err := s.code.RepoBasicsByID(ctx, a.RepoID)
		if err != nil {
			return nil // the repository is gone
		}
		codeProject = r.ProjectID
	}
	// A moved issue concerns the webhooks of the project it left too (if their owners may see it in its new one).
	rows, err := s.pool.Query(ctx, `SELECT `+hookColumns+` FROM webhooks WHERE active AND (events = '{}' OR $1 = ANY(events)) AND (
		(scope = 'instance') OR (scope = 'project' AND scope_id IN ($2, $6)) OR (scope = 'space' AND scope_id = $3)
		OR (scope = 'code-project' AND scope_id = $4) OR (scope = 'repository' AND scope_id = $5))`,
		a.Event, a.ProjectID, a.SpaceID, codeProject, a.RepoID, a.FromProjectID)
	if err != nil {
		return err
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*hookRow, error) { return scanHook(r) })
	if err != nil || len(found) == 0 {
		return err
	}
	// A webhook sends only what its owner may see (and only while they administer its scope).
	subj := subjectOf(a)
	var hooks []int64
	for _, h := range found {
		reason, err := s.mayDeliver(ctx, h, subj)
		if err != nil {
			return err
		}
		if reason != "" {
			s.log.Debug("webhook skipped", "webhook", h.ID, "event", a.Event, "reason", reason)
			continue
		}
		hooks = append(hooks, h.ID)
	}
	if len(hooks) == 0 {
		return nil
	}
	payload, err := s.payload(ctx, a)
	if err != nil {
		if apperr.IsNotFound(err) || db.IsNoRows(err) {
			return nil // deleted in the meantime
		}
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, h := range hooks {
			if _, err := s.enqueue(ctx, tx, h, a.EventID, a.Event, payload, subj); err != nil {
				return err
			}
		}
		return nil
	})
}

func userRef(ctx context.Context, s *Service, id int64) *identity.UserRef {
	if id == 0 {
		return nil
	}
	users, err := identity.GetUserRefs(ctx, s.pool, []int64{id})
	if err != nil {
		return nil
	}
	if u, ok := users[id]; ok {
		return &u
	}
	return nil
}

func (s *Service) link(path string) string {
	return strings.TrimRight(s.opt.PublicURL.String(), "/") + path
}

// payload builds the JSON body of an event.
func (s *Service) payload(ctx context.Context, a EventArgs) ([]byte, error) {
	body := map[string]any{"id": a.EventID, "event": a.Event, "timestamp": a.At, "actor": userRef(ctx, s, a.ActorID)}
	switch {
	case a.IssueKey != "":
		if a.Event == "issue.deleted" {
			body["issue"] = map[string]any{"id": a.IssueID, "key": a.IssueKey}
			break
		}
		i, err := s.issues.IssueInfo(ctx, a.IssueID)
		if err != nil {
			return nil, err
		}
		body["issue"], body["url"] = i, s.link(i.Path)
		if len(a.Fields) > 0 {
			body["changes"] = map[string]any{"fields": a.Fields}
		}
		if a.From != 0 || a.To != 0 {
			names, err := s.issues.StatusNames(ctx, []int64{a.From, a.To})
			if err != nil {
				return nil, err
			}
			body["transition"] = map[string]string{"from": names[a.From], "to": names[a.To]}
		}
		if a.FromKey != "" {
			from, err := s.issues.ProjectKeyOf(ctx, a.FromProjectID)
			if err != nil && !db.IsNoRows(err) {
				return nil, err
			}
			body["move"] = map[string]string{"fromKey": a.FromKey, "toKey": a.IssueKey, "fromProject": from, "toProject": i.ProjectKey}
		}
	case a.PageID != 0:
		pg, err := s.wiki.PageInfo(ctx, a.PageID)
		if err != nil {
			return nil, err
		}
		body["page"], body["url"] = pg, s.link(pg.Path)
	case a.RunID != 0:
		run, err := s.builds.RunInfo(ctx, a.RunID)
		if err != nil {
			return nil, err
		}
		body["run"] = run
		if a.DeployID != 0 {
			body["deployment"] = map[string]any{"id": a.DeployID, "environment": a.Env, "commit": a.Commit}
		}
		if run.Repository != nil {
			body["url"] = s.link(builds.RunPath(run.Repository.ProjectKey, run.Repository.Slug, run.Number))
		}
	case a.RepoID != 0:
		r, err := s.code.RepoBasicsByID(ctx, a.RepoID)
		if err != nil {
			return nil, err
		}
		repoPath := "/code/" + strings.ToLower(r.ProjectKey) + "/" + r.Slug
		body["repository"] = map[string]any{"id": r.ID, "projectKey": r.ProjectKey, "slug": r.Slug, "name": r.Name,
			"defaultBranch": r.DefaultBranch, "cloneUrl": r.CloneURL, "url": s.link(repoPath)}
		body["url"] = s.link(repoPath)
		if a.PRID != 0 {
			pr, err := s.code.PullRequestByID(ctx, a.PRID)
			if err != nil {
				return nil, err
			}
			prPath := repoPath + "/pull-requests/" + strconv.Itoa(pr.Number)
			body["pullRequest"] = map[string]any{"id": pr.ID, "number": pr.Number, "title": pr.Title, "state": pr.State, "draft": pr.Draft,
				"source": pr.Source, "target": pr.Target, "sourceCommit": pr.SourceSHA, "author": userRef(ctx, s, pr.AuthorID),
				"url": s.link(prPath)}
			body["url"] = s.link(prPath)
		}
		if a.Event == "repository.pushed" {
			updates := []map[string]string{}
			for _, u := range a.Updates {
				updates = append(updates, map[string]string{"ref": u.Ref, "before": u.Old, "after": u.New})
			}
			body["push"] = map[string]any{"updates": updates}
		}
	}
	return json.Marshal(body)
}

// deliver sends a delivery. Failures are retried by the job queue; final tells that this is the last attempt.
func (s *Service) deliver(ctx context.Context, id int64, final bool) error {
	var sealed, event, eventID, state string
	var hookID int64
	var payload []byte
	var subj subject
	err := s.pool.QueryRow(ctx, `SELECT d.webhook_id, w.secret_sealed, d.event, d.event_id::text, d.payload, d.state,
		coalesce(d.subject_type, ''), coalesce(d.subject_id, 0)
		FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id WHERE d.id = $1`, id).
		Scan(&hookID, &sealed, &event, &eventID, &payload, &state, &subj.kind, &subj.id)
	if db.IsNoRows(err) {
		return nil // the webhook was deleted
	}
	if err != nil {
		return err
	}
	if state != "pending" {
		return nil
	}
	h, err := scanHook(s.pool.QueryRow(ctx, "SELECT "+hookColumns+" FROM webhooks WHERE id = $1", hookID))
	if err != nil {
		return err
	}
	if !h.Active {
		_, err := s.pool.Exec(ctx, "UPDATE webhook_deliveries SET state = 'failed', error = 'The webhook is inactive.' WHERE id = $1", id)
		return err
	}
	// Permissions may have changed since the event: check again before anything leaves.
	if reason, err := s.mayDeliver(ctx, h, subj); err != nil {
		return err
	} else if reason != "" {
		_, err := s.pool.Exec(ctx, "UPDATE webhook_deliveries SET state = 'failed', error = $2 WHERE id = $1", id, reason)
		return err
	}
	hookURL := h.URL
	secret, err := s.opt.Keyring.Open(sealed, sealAAD(hookID))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Kogge-Webhook/"+s.opt.Version)
	req.Header.Set("X-Kogge-Event", event)
	req.Header.Set("X-Kogge-Event-Id", eventID)
	req.Header.Set("X-Kogge-Delivery", strconv.FormatInt(id, 10))
	req.Header.Set("X-Kogge-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))

	started := time.Now()
	status, excerpt, sendErr := s.send(ctx, req)
	duration := int(time.Since(started).Milliseconds())
	metrics.Webhook(sendErr == nil && status >= 200 && status < 300)
	newState, errText := "pending", ""
	switch {
	case sendErr == nil && status >= 200 && status < 300:
		newState = "succeeded"
	case sendErr != nil:
		errText = sendErr.Error()
	default:
		errText = fmt.Sprintf("The target answered %d.", status)
	}
	if newState == "pending" && (final || errors.Is(sendErr, errForbiddenTarget)) {
		newState = "failed" // the target policy does not change between attempts
	}
	var statusArg any
	if status != 0 {
		statusArg = status
	}
	if _, err := s.pool.Exec(ctx, `UPDATE webhook_deliveries SET state = $2, attempts = attempts + 1, response_status = $3, response_excerpt = $4,
		error = $5, duration_ms = $6, delivered_at = CASE WHEN $2 = 'succeeded' THEN now() ELSE delivered_at END WHERE id = $1`,
		id, newState, statusArg, excerpt, errText, duration); err != nil {
		return err
	}
	if newState == "pending" {
		return errors.New("webhook delivery failed: " + errText) // retried with backoff
	}
	return nil
}

// send posts a request with the outbound policy and returns the status and the beginning of the response.
func (s *Service) send(ctx context.Context, req *http.Request) (int, string, error) {
	policy, err := s.policy(ctx)
	if err != nil {
		return 0, "", err
	}
	if !policy.HostAllowed(req.URL.Hostname()) {
		return 0, "", errForbiddenTarget
	}
	dialer := policy.Dialer(req.URL.Hostname(), 5*time.Second)
	client := &http.Client{
		Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second,
			ResponseHeaderTimeout: requestTimeout, DisableKeepAlives: true},
		Timeout: requestTimeout,
		// Redirects could lead to forbidden targets; the response is recorded instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		if netpolicy.Forbidden(err) {
			return 0, "", errForbiddenTarget
		}
		return 0, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, excerptSize))
	return res.StatusCode, strings.ToValidUTF8(string(b), "�"), nil
}

var errForbiddenTarget = netpolicy.ErrForbidden

// policy is the outbound policy of webhooks.
func (s *Service) policy(ctx context.Context) (*netpolicy.Policy, error) {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	return netpolicy.New(st.WebhookTargets, s.opt.AllowLoopback), nil
}
