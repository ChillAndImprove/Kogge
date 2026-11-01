// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	nmail "net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/events"
	"kogge.de/kogge/internal/platform/jobs"
	"kogge.de/kogge/internal/platform/mail"
	"kogge.de/kogge/internal/platform/metrics"
)

// Filter subscriptions: the results of a saved filter by e-mail on a schedule, to the person who
// subscribes or to the members of a group. The filter runs for every recipient with their own permissions, so an
// e-mail lists only what its recipient may see, and who no longer sees the filter receives nothing.

// maxSubscriptionIssues is the number of issues an e-mail lists; a link opens all of them.
const maxSubscriptionIssues = 50

// maxSubscriptions is the number of subscriptions one person may set up.
const maxSubscriptions = 50

// Schedule is when a subscription sends: every hour (in some hours of the day), every day or every month, on some
// days of the week (hourly and daily) or a day of the month (monthly), in the time zone of the subscription.
type Schedule struct {
	Frequency string `json:"frequency" enum:"hourly,daily,monthly"`
	Minute    int    `json:"minute" minimum:"0" maximum:"1439" doc:"Hourly: the minute of the hour (0–59); daily and monthly: the minute of the day (510 is 08:30)"`
	Weekdays  []int  `json:"weekdays,omitempty" doc:"Hourly and daily: the days of the week, 1 (Monday) to 7 (Sunday)"`
	Hours     []int  `json:"hours,omitempty" doc:"Hourly: the hours of the day (0–23) to send in; every hour when empty"`
	MonthDay  int    `json:"monthDay,omitempty" minimum:"0" maximum:"31" doc:"Monthly: the day of the month (the last day of shorter months)"`
}

// normalized checks a schedule and returns it in canonical form (sorted days and hours, nothing that its frequency
// does not use).
func (sc Schedule) normalized() (Schedule, error) {
	// set sorts a set of numbers and reports whether they lie in [lo, hi].
	set := func(v []int, lo, hi int) ([]int, bool) {
		out := slices.Clone(v)
		slices.Sort(out)
		out = slices.Compact(out)
		if out == nil {
			out = []int{}
		}
		return out, len(out) == 0 || out[0] >= lo && out[len(out)-1] <= hi
	}
	out := Schedule{Frequency: sc.Frequency, Minute: sc.Minute, Weekdays: []int{}, Hours: []int{}}
	var ok bool
	switch sc.Frequency {
	case "hourly":
		if sc.Minute > 59 {
			return out, apperr.Invalid("subscription.minute", "An hourly subscription sends at a minute of the hour (0–59).")
		}
		if out.Hours, ok = set(sc.Hours, 0, 23); !ok {
			return out, apperr.Invalid("subscription.hours", "The hours of the day are 0 to 23.")
		}
	case "daily":
	case "monthly":
		if sc.MonthDay < 1 || sc.MonthDay > 31 {
			return out, apperr.Invalid("subscription.month-day", "Choose a day of the month (1–31).")
		}
		out.MonthDay = sc.MonthDay
		return out, nil
	default:
		return out, apperr.Invalid("subscription.frequency", "Choose hourly, daily or monthly.")
	}
	if out.Weekdays, ok = set(sc.Weekdays, 1, 7); !ok {
		return out, apperr.Invalid("subscription.weekdays", "The days of the week are 1 (Monday) to 7 (Sunday).")
	}
	if len(out.Weekdays) == 0 {
		return out, apperr.Invalid("subscription.weekdays", "Choose at least one day of the week.")
	}
	return out, nil
}

// isoWeekday is the ISO day of the week (1 = Monday, 7 = Sunday).
func isoWeekday(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

// Next returns when a (normalized) schedule sends next after a time. Local times that a change to daylight saving
// time skips move by the change.
func (sc Schedule) Next(after time.Time, loc *time.Location) time.Time {
	t := after.In(loc)
	onDay := func(d time.Time) bool { return slices.Contains(sc.Weekdays, isoWeekday(d)) }
	switch sc.Frequency {
	case "hourly":
		next := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), sc.Minute, 0, 0, loc)
		for range 8*24 + 2 {
			if next.After(after) && onDay(next) && (len(sc.Hours) == 0 || slices.Contains(sc.Hours, next.Hour())) {
				return next
			}
			next = next.Add(time.Hour)
		}
	case "daily":
		for d := range 8 {
			next := time.Date(t.Year(), t.Month(), t.Day()+d, sc.Minute/60, sc.Minute%60, 0, 0, loc)
			if next.After(after) && onDay(next) {
				return next
			}
		}
	case "monthly":
		for m := range 3 {
			first := time.Date(t.Year(), t.Month()+time.Month(m), 1, 0, 0, 0, 0, loc)
			last := first.AddDate(0, 1, -1).Day()
			next := time.Date(first.Year(), first.Month(), min(sc.MonthDay, last), sc.Minute/60, sc.Minute%60, 0, 0, loc)
			if next.After(after) {
				return next
			}
		}
	}
	// Not reached with a valid schedule.
	return after.Add(24 * time.Hour)
}

// FilterRef names a filter.
type FilterRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GroupRef names a group.
type GroupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Subscription is a subscription of a filter, as the caller sees it.
type Subscription struct {
	ID     int64             `json:"id"`
	Filter FilterRef         `json:"filter"`
	User   *identity.UserRef `json:"user,omitempty" doc:"The recipient of a personal subscription"`
	Group  *GroupRef         `json:"group,omitempty" doc:"The group whose members receive the e-mails"`
	Schedule
	SendEmpty bool             `json:"sendEmpty" doc:"Also sent when the filter finds no issues"`
	TimeZone  string           `json:"timeZone" doc:"The time zone of the schedule (of who set it up)"`
	Next      time.Time        `json:"next"`
	Last      *time.Time       `json:"last,omitempty"`
	LastError string           `json:"lastError,omitempty" doc:"Why the last sending failed"`
	CreatedBy identity.UserRef `json:"createdBy"`
	CanManage bool             `json:"canManage" doc:"The caller may change, send and delete it"`
	CanMute   bool             `json:"canMute" doc:"The caller belongs to the group and may stop its e-mails for themselves"`
	Muted     bool             `json:"muted" doc:"The caller receives no e-mails of this group subscription"`
}

// FilterSubscriptions are subscriptions and whether this installation sends e-mail.
type FilterSubscriptions struct {
	Mail          bool           `json:"mail" doc:"E-mail is configured; without it, subscriptions send nothing"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// SubscriptionChange changes when a subscription sends.
type SubscriptionChange struct {
	Schedule
	SendEmpty bool `json:"sendEmpty,omitempty" doc:"Also send when the filter finds no issues"`
}

// SubscriptionInput subscribes to a filter.
type SubscriptionInput struct {
	SubscriptionChange
	GroupID int64 `json:"groupId,omitempty" doc:"Subscribe the members of this group (the filter's owner, for a group they belong to that the filter is shared with); without it, the caller"`
}

const subscriptionSelect = `SELECT s.id, s.filter_id, f.name, f.owner_id, s.user_id, u.username, u.display_name, u.status = 'active',
	s.group_id, g.name, s.frequency, s.minute, s.weekdays, s.hours, coalesce(s.month_day, 0), s.send_empty, s.time_zone,
	s.next_run_at, s.last_run_at, s.last_error, c.id, c.username, c.display_name, c.status = 'active'
	FROM filter_subscriptions s JOIN saved_filters f ON f.id = s.filter_id LEFT JOIN users u ON u.id = s.user_id
	LEFT JOIN groups g ON g.id = s.group_id JOIN users c ON c.id = s.created_by`

type subscriptionRow struct {
	Subscription
	owner int64 // of the filter
}

func scanSubscriptions(rows pgx.Rows) ([]subscriptionRow, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (subscriptionRow, error) {
		var s subscriptionRow
		var uid, gid *int64
		var un, dn, gn *string
		var active *bool
		err := row.Scan(&s.ID, &s.Filter.ID, &s.Filter.Name, &s.owner, &uid, &un, &dn, &active, &gid, &gn, &s.Frequency, &s.Minute,
			&s.Weekdays, &s.Hours, &s.MonthDay, &s.SendEmpty, &s.TimeZone, &s.Next, &s.Last, &s.LastError, &s.CreatedBy.ID,
			&s.CreatedBy.Username, &s.CreatedBy.DisplayName, &s.CreatedBy.Active)
		if uid != nil {
			s.User = &identity.UserRef{ID: *uid, Username: *un, DisplayName: *dn, Active: active != nil && *active}
		}
		if gid != nil {
			s.Group = &GroupRef{ID: *gid, Name: *gn}
		}
		return s, err
	})
}

// manageableBy reports whether a principal may change, send and delete a subscription: who set it up, who receives
// it alone, for a group subscription the filter's owner, and administrators.
func (sub *subscriptionRow) manageableBy(p *identity.Principal, admin bool) bool {
	me := p.UserID()
	return admin || sub.CreatedBy.ID == me || (sub.User != nil && sub.User.ID == me) || (sub.Group != nil && sub.owner == me)
}

// visibleSubscriptions returns the subscriptions that match a condition (on s and f) and that the caller may see:
// of filters they see, those they may manage and those of groups they belong to.
func (s *Service) visibleSubscriptions(ctx context.Context, p *identity.Principal, cond string, args ...any) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, subscriptionSelect+" WHERE "+cond+" ORDER BY lower(f.name), s.id", args...)
	if err != nil {
		return nil, err
	}
	list, err := scanSubscriptions(rows)
	if err != nil || len(list) == 0 {
		return []Subscription{}, err
	}
	var filterIDs, ids []int64
	for _, sub := range list {
		filterIDs, ids = append(filterIDs, sub.Filter.ID), append(ids, sub.ID)
	}
	filters, err := s.visibleFilters(ctx, p, "f.id = ANY($1)", filterIDs)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	for _, f := range filters {
		seen[f.ID] = true
	}
	rows, err = s.pool.Query(ctx, "SELECT subscription_id FROM filter_subscription_mutes WHERE user_id = $1 AND subscription_id = ANY($2)",
		p.UserID(), ids)
	if err != nil {
		return nil, err
	}
	mutedIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	admin, err := s.authz.IsAdmin(ctx, p)
	if err != nil {
		return nil, err
	}
	out := []Subscription{}
	for _, sub := range list {
		manage := sub.manageableBy(p, admin)
		member := sub.Group != nil && p.InGroup(sub.Group.ID)
		if !seen[sub.Filter.ID] || !manage && !member {
			continue
		}
		sub.CanManage, sub.CanMute, sub.Muted = manage, member, member && slices.Contains(mutedIDs, sub.ID)
		out = append(out, sub.Subscription)
	}
	return out, nil
}

// FilterSubscriptions lists the subscriptions of a filter that the caller may see.
func (s *Service) FilterSubscriptions(ctx context.Context, p *identity.Principal, filterID int64) (*FilterSubscriptions, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	if _, err := s.GetFilter(ctx, p, filterID); err != nil {
		return nil, err
	}
	list, err := s.visibleSubscriptions(ctx, p, "s.filter_id = $1", filterID)
	if err != nil {
		return nil, err
	}
	return &FilterSubscriptions{Mail: s.mailer != nil, Subscriptions: list}, nil
}

// MySubscriptions lists the subscriptions the caller receives (alone or with a group), set up, or, as the owner of
// the filter, may manage.
func (s *Service) MySubscriptions(ctx context.Context, p *identity.Principal) (*FilterSubscriptions, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	list, err := s.visibleSubscriptions(ctx, p, `(s.user_id = $1 OR s.created_by = $1 OR s.group_id = ANY($2)
		OR (s.group_id IS NOT NULL AND f.owner_id = $1))`, p.UserID(), nonNil(p.Groups))
	if err != nil {
		return nil, err
	}
	return &FilterSubscriptions{Mail: s.mailer != nil, Subscriptions: list}, nil
}

// subscriptionOf returns a subscription the caller may see; who may not learns nothing about it.
func (s *Service) subscriptionOf(ctx context.Context, p *identity.Principal, id int64) (*Subscription, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	list, err := s.visibleSubscriptions(ctx, p, "s.id = $1", id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, apperr.NotFound("subscription.not-found", "The subscription does not exist.")
	}
	return &list[0], nil
}

// manageable returns a subscription the caller may change.
func (s *Service) manageable(ctx context.Context, p *identity.Principal, id int64) (*Subscription, error) {
	sub, err := s.subscriptionOf(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if !sub.CanManage {
		return nil, apperr.Forbidden("subscription.manage-denied",
			"Only who set up the subscription, the owner of the filter and administrators can change it.")
	}
	return sub, nil
}

// zoneOf returns the time zone of a user (the installation's when they chose none).
func (s *Service) zoneOf(ctx context.Context, p *identity.Principal) string {
	tz := p.User.TimeZone
	if tz == "" {
		if st, err := s.settings.Get(ctx); err == nil {
			tz = st.DefaultTimeZone
		}
	}
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		return "UTC"
	}
	return tz
}

func zoneLocation(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// sharedWithGroup reports whether all members of a group may see a filter by its shares.
func sharedWithGroup(f *Filter, groupID int64) bool {
	for _, sh := range f.Shares {
		if sh.Type == "anyone" || sh.Type == "logged-in" || sh.Type == "group" && sh.ID == groupID {
			return true
		}
	}
	return false
}

// Subscribe subscribes the caller (or, for the filter's owner, a group they belong to) to a filter they see. The
// schedule runs in the caller's time zone.
func (s *Service) Subscribe(ctx context.Context, p *identity.Principal, filterID int64, in SubscriptionInput) (*Subscription, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	f, err := s.GetFilter(ctx, p, filterID)
	if err != nil {
		return nil, err
	}
	var userID, groupID any = p.UserID(), nil
	if in.GroupID != 0 {
		admin, err := s.authz.IsAdmin(ctx, p)
		if err != nil {
			return nil, err
		}
		switch {
		case f.Owner.ID != p.UserID() && !admin:
			return nil, apperr.Forbidden("subscription.group-denied", "Only the owner of the filter can subscribe a group.")
		case !p.InGroup(in.GroupID) && !admin:
			return nil, apperr.Forbidden("subscription.group-member", "You can only subscribe groups you belong to.")
		case !sharedWithGroup(f, in.GroupID):
			return nil, apperr.Invalid("subscription.not-shared", "Share the filter with the group before you subscribe it.")
		}
		userID, groupID = nil, in.GroupID
	}
	if s.mailer == nil {
		return nil, apperr.Conflict("subscription.no-mail", "This installation sends no e-mail; ask your administrator to set it up.")
	}
	sc, err := in.Schedule.normalized()
	if err != nil {
		return nil, err
	}
	var count int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM filter_subscriptions WHERE created_by = $1", p.UserID()).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxSubscriptions {
		return nil, apperr.Conflict("subscription.limit", "You have set up %d subscriptions, the most one person can have; delete one first.",
			maxSubscriptions)
	}
	tz := s.zoneOf(ctx, p)
	var monthDay any
	if sc.MonthDay != 0 {
		monthDay = sc.MonthDay
	}
	var id int64
	err = s.pool.QueryRow(ctx, `INSERT INTO filter_subscriptions (filter_id, user_id, group_id, created_by, frequency, minute, weekdays,
		hours, month_day, send_empty, time_zone, next_run_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		filterID, userID, groupID, p.UserID(), sc.Frequency, sc.Minute, sc.Weekdays, sc.Hours, monthDay, in.SendEmpty, tz,
		sc.Next(now(), zoneLocation(tz))).Scan(&id)
	if db.IsForeignKeyViolation(err) {
		return nil, apperr.Invalid("subscription.group", "The group does not exist.")
	}
	if err != nil {
		return nil, err
	}
	return s.subscriptionOf(ctx, p, id)
}

// UpdateSubscription changes when a subscription sends; its time zone and recipients stay.
func (s *Service) UpdateSubscription(ctx context.Context, p *identity.Principal, id int64, in SubscriptionChange) (*Subscription, error) {
	sub, err := s.manageable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	sc, err := in.Schedule.normalized()
	if err != nil {
		return nil, err
	}
	var monthDay any
	if sc.MonthDay != 0 {
		monthDay = sc.MonthDay
	}
	if _, err := s.pool.Exec(ctx, `UPDATE filter_subscriptions SET frequency = $2, minute = $3, weekdays = $4, hours = $5, month_day = $6,
		send_empty = $7, next_run_at = $8 WHERE id = $1`, id, sc.Frequency, sc.Minute, sc.Weekdays, sc.Hours, monthDay, in.SendEmpty,
		sc.Next(now(), zoneLocation(sub.TimeZone))); err != nil {
		return nil, err
	}
	return s.subscriptionOf(ctx, p, id)
}

// Unsubscribe deletes a subscription.
func (s *Service) Unsubscribe(ctx context.Context, p *identity.Principal, id int64) error {
	if _, err := s.manageable(ctx, p, id); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, "DELETE FROM filter_subscriptions WHERE id = $1", id)
	return err
}

// subscriptionDue asks the job to send the subscriptions that are due (at commit).
type subscriptionDue struct{}

// EventName implements events.Event.
func (subscriptionDue) EventName() string { return "issues.filter-subscription-due" }

// SendSubscription sends a subscription in a moment (its schedule stays), at most once a minute.
func (s *Service) SendSubscription(ctx context.Context, p *identity.Principal, id int64) error {
	sub, err := s.manageable(ctx, p, id)
	if err != nil {
		return err
	}
	if s.mailer == nil {
		return apperr.Conflict("subscription.no-mail", "This installation sends no e-mail; ask your administrator to set it up.")
	}
	if sub.Last != nil && now().Sub(*sub.Last) < time.Minute {
		return apperr.TooManyRequests("subscription.too-soon", "The subscription was sent less than a minute ago; try again shortly.")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE filter_subscriptions SET next_run_at = now() WHERE id = $1", id); err != nil {
			return err
		}
		return s.bus.Publish(ctx, tx, subscriptionDue{})
	})
}

// MuteSubscription stops (or resumes) the e-mails of a group subscription for the caller, a member of the group.
func (s *Service) MuteSubscription(ctx context.Context, p *identity.Principal, id int64, muted bool) error {
	sub, err := s.subscriptionOf(ctx, p, id)
	if err != nil {
		return err
	}
	if !sub.CanMute {
		return apperr.Invalid("subscription.not-member", "Only members of the group of a subscription can stop its e-mails for themselves.")
	}
	if muted {
		_, err = s.pool.Exec(ctx, "INSERT INTO filter_subscription_mutes (subscription_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			id, p.UserID())
	} else {
		_, err = s.pool.Exec(ctx, "DELETE FROM filter_subscription_mutes WHERE subscription_id = $1 AND user_id = $2", id, p.UserID())
	}
	return err
}

// --- delivery ---

// subscriptionMailer sends the results of filters.
type subscriptionMailer struct {
	sender *mail.Sender
	public *url.URL
}

// SetMail lets subscriptions send e-mail (with links to the installation's public address) and registers their job.
func (s *Service) SetMail(sender *mail.Sender, public *url.URL, reg *jobs.Registry) {
	s.mailer = &subscriptionMailer{sender: sender, public: public}
	river.AddWorker(reg.Workers, &subscriptionWorker{s: s})
	reg.Every(time.Minute, SubscriptionArgs{}, nil)
	s.bus.Subscribe(func(events.Event) (river.JobArgs, *river.InsertOpts) { return SubscriptionArgs{}, nil },
		subscriptionDue{}.EventName())
}

// SubscriptionArgs sends the subscriptions that are due.
type SubscriptionArgs struct{}

// Kind implements river.JobArgs.
func (SubscriptionArgs) Kind() string { return "issues.filter-subscriptions" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (SubscriptionArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: jobs.QueueMail} }

type subscriptionWorker struct {
	river.WorkerDefaults[SubscriptionArgs]
	s *Service
}

func (w *subscriptionWorker) Timeout(*river.Job[SubscriptionArgs]) time.Duration {
	return 30 * time.Minute
}

func (w *subscriptionWorker) Work(ctx context.Context, _ *river.Job[SubscriptionArgs]) error {
	return w.s.DeliverSubscriptions(ctx)
}

// DeliverSubscriptions sends the subscriptions whose time has come. Each moves on to its next time before it is
// sent, so that several nodes never send it twice and a failing one does not repeat every minute.
func (s *Service) DeliverSubscriptions(ctx context.Context) error {
	if s.mailer == nil {
		return nil
	}
	type claimed struct {
		id int64
		sc Schedule
		tz string
	}
	for {
		var due []int64
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id, frequency, minute, weekdays, hours, coalesce(month_day, 0), time_zone
				FROM filter_subscriptions WHERE next_run_at <= now() ORDER BY next_run_at LIMIT 20 FOR UPDATE SKIP LOCKED`)
			if err != nil {
				return err
			}
			list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (claimed, error) {
				var c claimed
				err := row.Scan(&c.id, &c.sc.Frequency, &c.sc.Minute, &c.sc.Weekdays, &c.sc.Hours, &c.sc.MonthDay, &c.tz)
				return c, err
			})
			if err != nil {
				return err
			}
			for _, c := range list {
				if _, err := tx.Exec(ctx, "UPDATE filter_subscriptions SET next_run_at = $2 WHERE id = $1", c.id,
					c.sc.Next(now(), zoneLocation(c.tz))); err != nil {
					return err
				}
				due = append(due, c.id)
			}
			return nil
		})
		if err != nil || len(due) == 0 {
			return err
		}
		for _, id := range due {
			if err := s.sendSubscription(ctx, id); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s.log.Warn("filter subscription not sent", "subscription", id, "error", err)
			}
		}
	}
}

// sendSubscription runs the filter for every recipient (with their permissions), mails the results and records how
// it went.
func (s *Service) sendSubscription(ctx context.Context, id int64) error {
	rows, err := s.pool.Query(ctx, subscriptionSelect+" WHERE s.id = $1", id)
	if err != nil {
		return err
	}
	list, err := scanSubscriptions(rows)
	if err != nil || len(list) == 0 {
		return err
	}
	sub := list[0]
	var recipients []int64
	if sub.User != nil {
		recipients = []int64{sub.User.ID}
	} else {
		rows, err := s.pool.Query(ctx, `SELECT m.user_id FROM group_members m WHERE m.group_id = $1 AND NOT EXISTS (
			SELECT 1 FROM filter_subscription_mutes x WHERE x.subscription_id = $2 AND x.user_id = m.user_id) ORDER BY m.user_id`,
			sub.Group.ID, sub.ID)
		if err != nil {
			return err
		}
		if recipients, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
	}
	var query string
	if err := s.pool.QueryRow(ctx, "SELECT query FROM saved_filters WHERE id = $1", sub.Filter.ID).Scan(&query); err != nil {
		return err
	}
	var failed []error
	for _, uid := range recipients {
		if err := s.sendSubscriptionTo(ctx, sub, query, uid); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed = append(failed, err)
		}
	}
	problem := ""
	if len(failed) > 0 {
		// The first problem: those of the query are the same for all recipients.
		problem = failed[0].Error()
		var ae *apperr.Error
		if errors.As(failed[0], &ae) {
			problem = ae.Detail
		}
		problem = truncateText(problem, 1000)
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE filter_subscriptions SET last_run_at = now(), last_error = $2 WHERE id = $1", sub.ID,
			problem); err != nil {
			return err
		}
		// Lists of subscriptions that are open show when it was sent.
		users := []int64{sub.CreatedBy.ID}
		if sub.User != nil && sub.User.ID != sub.CreatedBy.ID {
			users = append(users, sub.User.ID)
		}
		for _, uid := range users {
			if err := events.NotifyLive(ctx, tx, events.LiveMessage{Type: "filter-subscriptions", ID: strconv.FormatInt(sub.ID, 10),
				User: uid}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return errors.Join(failed...)
}

type subscriptionRecipient struct {
	Email, Name, Locale string
	Verified, Active    bool
}

// sendSubscriptionTo mails the results of the filter to one recipient, if they may see the filter and have a
// verified address; who has nothing to see gets no e-mail unless the subscription wants it.
func (s *Service) sendSubscriptionTo(ctx context.Context, sub subscriptionRow, query string, uid int64) error {
	var r subscriptionRecipient
	err := s.pool.QueryRow(ctx, `SELECT coalesce(email, ''), display_name, coalesce(locale, ''), email_verified, status = 'active'
		FROM users WHERE id = $1`, uid).Scan(&r.Email, &r.Name, &r.Locale, &r.Verified, &r.Active)
	if db.IsNoRows(err) || err == nil && (!r.Active || !r.Verified || r.Email == "") {
		return nil
	}
	if err != nil {
		return err
	}
	p, err := identity.LoadPrincipal(ctx, s.pool, uid, identity.MethodInternal)
	if err != nil {
		return err
	}
	if _, err := s.GetFilter(ctx, p, sub.Filter.ID); err != nil {
		if apperr.IsNotFound(err) {
			return nil
		}
		return err
	}
	res, err := s.Search(ctx, p, query, maxSubscriptionIssues, 0)
	if err != nil {
		return err
	}
	if res.Total == 0 && !sub.SendEmpty {
		return nil
	}
	msg, err := s.composeSubscription(ctx, sub, r, query, res)
	if err != nil {
		return err
	}
	err = s.mailer.sender.Send(ctx, msg)
	metrics.Mail(err)
	if err != nil {
		_, _ = s.pool.Exec(ctx, "INSERT INTO mail_log (user_id, address, subject, state, error) VALUES ($1, $2, $3, 'failed', $4)",
			uid, r.Email, msg.Subject, truncateText(err.Error(), 1000))
		return fmt.Errorf("e-mail to %s: %w", r.Email, err)
	}
	_, err = s.pool.Exec(ctx, "INSERT INTO mail_log (user_id, address, subject, state) VALUES ($1, $2, $3, 'sent')", uid, r.Email, msg.Subject)
	return err
}

// truncateText shortens a text to at most n bytes without splitting a character.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

type subscriptionMailData struct {
	Instance, Heading, Greeting, Intro, More, Open, Footer, Manage, ManageLabel string
	Columns                                                                     []string
	Rows                                                                        [][]string
	Links                                                                       []string
}

// composeSubscription renders the e-mail of a subscription for a recipient, in their language.
func (s *Service) composeSubscription(ctx context.Context, sub subscriptionRow, r subscriptionRecipient, query string,
	res *SearchResult) (mail.Message, error) {
	st, err := s.settings.Get(ctx)
	if err != nil {
		return mail.Message{}, err
	}
	lang := r.Locale
	if lang != "de" && lang != "en" {
		lang = st.DefaultLocale
	}
	tr := subscriptionTexts["en"]
	if lang == "de" {
		tr = subscriptionTexts["de"]
	}
	base := strings.TrimRight(s.mailer.public.String(), "/")
	data := subscriptionMailData{Instance: st.Name, Greeting: fmt.Sprintf(tr["greeting"], r.Name),
		Open:   base + "/issues/search?filter=" + strconv.FormatInt(sub.Filter.ID, 10) + "&q=" + url.QueryEscape(query),
		Manage: base + "/issues/filters?tab=subscriptions", ManageLabel: tr["manage"], Columns: strings.Split(tr["columns"], "|")}
	switch {
	case res.Total == 0:
		data.Heading, data.Intro = fmt.Sprintf(tr["heading.none"], sub.Filter.Name), tr["none"]
	case res.Total == 1:
		data.Heading, data.Intro, data.More = fmt.Sprintf(tr["heading.one"], sub.Filter.Name), tr["found.one"], tr["open"]
	case res.Total > len(res.Issues):
		data.Heading = fmt.Sprintf(tr["heading.other"], sub.Filter.Name, res.Total)
		data.Intro, data.More = fmt.Sprintf(tr["some"], res.Total, len(res.Issues)), tr["all"]
	default:
		data.Heading = fmt.Sprintf(tr["heading.other"], sub.Filter.Name, res.Total)
		data.Intro, data.More = fmt.Sprintf(tr["found.other"], res.Total), tr["open"]
	}
	if sub.Group != nil {
		data.Footer = fmt.Sprintf(tr["footer.group"], sub.Group.Name, sub.CreatedBy.DisplayName, sub.Filter.Name)
	} else {
		data.Footer = fmt.Sprintf(tr["footer.user"], sub.Filter.Name)
	}
	for _, i := range res.Issues {
		status, assignee, priority := "", tr["unassigned"], ""
		if i.Status != nil {
			status = i.Status.Name
		}
		if i.Assignee != nil {
			assignee = i.Assignee.DisplayName
		}
		if i.Priority != nil {
			priority = i.Priority.Name
		}
		data.Rows = append(data.Rows, []string{i.Key, i.Summary, status, assignee, priority})
		data.Links = append(data.Links, base+"/browse/"+i.Key)
	}
	msg := mail.Message{To: nmail.Address{Name: r.Name, Address: r.Email}, Subject: fmt.Sprintf("[%s] %s", st.Name, data.Heading),
		Headers: map[string]string{"X-Kogge-Category": "filter-subscription"}}
	var text, html bytes.Buffer
	if err := subscriptionText.Execute(&text, data); err != nil {
		return msg, err
	}
	if err := subscriptionHTML.Execute(&html, data); err != nil {
		return msg, err
	}
	msg.Text, msg.HTML = text.String(), html.String()
	return msg, nil
}

var subscriptionTexts = map[string]map[string]string{
	"en": {
		"greeting":      "Hello %s,",
		"heading.none":  "%s: no issues",
		"heading.one":   "%s: 1 issue",
		"heading.other": "%s: %d issues",
		"none":          "The filter finds no issues.",
		"found.one":     "The filter finds 1 issue.",
		"found.other":   "The filter finds %d issues.",
		"some":          "The filter finds %d issues; here are the first %d.",
		"all":           "Show all in Kontor",
		"open":          "Open in Kontor",
		"columns":       "Key|Summary|Status|Assignee|Priority",
		"unassigned":    "Unassigned",
		"footer.user":   "You receive this e-mail because you subscribed to the filter %s.",
		"footer.group":  "You receive this e-mail as a member of the group %s, which %s subscribed to the filter %s.",
		"manage":        "Manage your subscriptions",
	},
	"de": {
		"greeting":      "Hallo %s,",
		"heading.none":  "%s: keine Vorgänge",
		"heading.one":   "%s: 1 Vorgang",
		"heading.other": "%s: %d Vorgänge",
		"none":          "Der Filter findet keine Vorgänge.",
		"found.one":     "Der Filter findet 1 Vorgang.",
		"found.other":   "Der Filter findet %d Vorgänge.",
		"some":          "Der Filter findet %d Vorgänge; hier sind die ersten %d.",
		"all":           "Alle in Kontor anzeigen",
		"open":          "In Kontor öffnen",
		"columns":       "Schlüssel|Zusammenfassung|Status|Bearbeitende Person|Priorität",
		"unassigned":    "Nicht zugewiesen",
		"footer.user":   "Sie erhalten diese E-Mail, weil Sie den Filter %s abonniert haben.",
		"footer.group":  "Sie erhalten diese E-Mail als Mitglied der Gruppe %s, die %s für den Filter %s abonniert hat.",
		"manage":        "Ihre Abonnements verwalten",
	},
}

var subscriptionText = template.Must(template.New("text").Parse(`{{.Greeting}}

{{.Intro}}
{{range $i, $r := .Rows}}
{{index $r 0}}  {{index $r 1}}
    {{index $r 2}} · {{index $r 3}}{{if index $r 4}} · {{index $r 4}}{{end}}
    {{index $.Links $i}}
{{end}}
{{if .More}}{{.More}}: {{.Open}}
{{end}}
--
{{.Footer}}
{{.ManageLabel}}: {{.Manage}}
`))

// No external resources, no tracking: inline styles only.
var subscriptionHTML = htmltemplate.Must(htmltemplate.New("html").Parse(`<!DOCTYPE html>
<html><body style="margin:0;padding:24px;background:#f4f6f8;font-family:Segoe UI,Helvetica,Arial,sans-serif;color:#1f2933">
<div style="max-width:760px;margin:0 auto;background:#ffffff;border:1px solid #dde3e8;border-radius:8px;padding:24px">
<div style="font-size:12px;color:#6b7785;margin-bottom:8px">{{.Instance}}</div>
<div style="font-size:18px;font-weight:600;margin-bottom:16px">{{.Heading}}</div>
<p style="margin:0 0 12px 0">{{.Greeting}}</p>
<p style="margin:0 0 12px 0">{{.Intro}}</p>
{{if .Rows}}<table style="width:100%;border-collapse:collapse;font-size:13px">
<tr>{{range .Columns}}<th style="text-align:left;padding:6px 8px;border-bottom:2px solid #dde3e8;color:#4a5562">{{.}}</th>{{end}}</tr>
{{range $i, $r := .Rows}}<tr>
<td style="padding:6px 8px;border-bottom:1px solid #eef1f4;white-space:nowrap"><a href="{{index $.Links $i}}" style="color:#0b6e75;text-decoration:none">{{index $r 0}}</a></td>
<td style="padding:6px 8px;border-bottom:1px solid #eef1f4">{{index $r 1}}</td>
<td style="padding:6px 8px;border-bottom:1px solid #eef1f4;white-space:nowrap">{{index $r 2}}</td>
<td style="padding:6px 8px;border-bottom:1px solid #eef1f4;white-space:nowrap">{{index $r 3}}</td>
<td style="padding:6px 8px;border-bottom:1px solid #eef1f4;white-space:nowrap">{{index $r 4}}</td>
</tr>{{end}}</table>{{end}}
{{if .More}}<p style="margin:16px 0 0 0"><a href="{{.Open}}" style="color:#0b6e75">{{.More}}</a></p>{{end}}
<div style="border-top:1px solid #e6eaee;padding-top:12px;margin-top:16px;font-size:12px;color:#6b7785">{{.Footer}}
<a href="{{.Manage}}" style="color:#6b7785">{{.ManageLabel}}</a></div>
</div></body></html>
`))
