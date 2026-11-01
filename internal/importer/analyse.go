// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/legacy"
)

// The analysis reads an extraction — and, when given, the database of the Kogge
// installation it will be loaded into — and reports what is there, how it will be translated, what cannot be
// translated exactly and what the administrator must decide. It uses the conversions of the load itself, so
// that what it reports is what the load will do; it never changes the target.

// Target is the Kogge installation an analysis matches users and keys against. It is only read: the analysis
// works in a read-only transaction.
type Target struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
	name string
}

// OpenTarget connects to the database of a Kogge installation for an analysis.
func OpenTarget(ctx context.Context, databaseURL string) (*Target, error) {
	pool, err := db.Open(ctx, databaseURL, "kogge-import-analysis", 2)
	if err != nil {
		return nil, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		pool.Close()
		return nil, err
	}
	name := "database"
	if u, err := url.Parse(databaseURL); err == nil && u.Host != "" {
		name = u.Host + u.Path
	}
	return &Target{pool: pool, tx: tx, name: name}, nil
}

// NewTarget uses a connection pool of the Kogge installation (tests and callers that have one).
func NewTarget(ctx context.Context, pool *pgxpool.Pool) (*Target, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	return &Target{tx: tx, name: "database"}, nil
}

// Close ends the read-only transaction (and the connection, if the target opened it).
func (t *Target) Close() {
	if t == nil {
		return
	}
	_ = t.tx.Rollback(context.Background())
	if t.pool != nil {
		t.pool.Close()
	}
}

// analysis is the state of one analysis.
type analysis struct {
	*Analysis
	ctx      context.Context
	l        lang
	work     Workdir
	target   *Target
	source   string                // the source as the load records it, e.g. jira:https://jira.example.com
	people   map[string]*UserMatch // by lower-case user name
	findings map[string]*Finding   // by kind and name
}

func newAnalysis(ctx context.Context, product, base string, w Workdir, language string, target *Target) (*analysis, error) {
	a := &analysis{Analysis: &Analysis{Product: product, Source: base, Workdir: w.Root, Language: language, Generated: time.Now(),
		Counts: []Figure{}, Sizes: []Figure{}, Users: []UserMatch{}, UserSummary: map[string]int{}, Translation: []Table{},
		Untranslatable: []Finding{}, Decisions: []Decision{}},
		ctx: ctx, l: lang(language), work: w, target: target, source: product + ":" + base, people: map[string]*UserMatch{},
		findings: map[string]*Finding{}}
	if target != nil {
		a.Target = target.name
	}
	st, err := readExtractionState(w, product)
	if err != nil {
		return nil, err
	}
	a.Extraction = st
	if a.Notes, err = readExtractionNotes(w, product); err != nil {
		return nil, err
	}
	if st != nil && st.Finished == nil {
		a.decide("extraction-unfinished", "high", a.l.t("The extraction did not finish. Run it again: it resumes where it stopped."))
	}
	// What the source could not deliver.
	unreadable := Decision{Code: "issues-unreadable", Severity: "high", Message: a.l.t("The source could not answer these issues (their fields or their rendering fail there): they are not in the extraction. Repair them in the source and extract again, or accept that they are left out.")}
	pages := Decision{Code: "pages-unreadable", Severity: "high", Message: a.l.t("The source could not answer these pages and blog posts (their content or history fail there): they are not in the extraction. Repair them in the source and extract again, or accept that they are left out.")}
	gone := Decision{Code: "attachments-gone", Severity: "medium", Message: a.l.t("The source lists these attachments but does not have their files any more: the load leaves them out.")}
	changed := Decision{Code: "source-changed", Severity: "medium", Message: a.l.t("The source changed while it was extracted: extract again (best while it is read-only, shortly before the cut-over).")}
	for _, n := range a.Notes {
		switch n.Kind {
		case "issue":
			unreadable.item(n.Name)
		case "page", "blogpost":
			pages.item(n.Name)
		case "attachment":
			gone.item(cmp.Or(n.Name, n.ID))
		case "listing":
			changed.item(n.ID + ": " + n.Note)
		}
	}
	for _, d := range []Decision{unreadable, pages, gone, changed} {
		a.decision(d)
	}
	return a, nil
}

func (a *analysis) count(key, label string, n int) {
	a.Counts = append(a.Counts, Figure{Key: key, Label: label, Value: int64(n)})
}

func (a *analysis) size(key, label string, n int64, bytes bool) {
	a.Sizes = append(a.Sizes, Figure{Key: key, Label: label, Value: n, Bytes: bytes})
}

// person records a user of the source. Later sightings complete what earlier ones lacked.
func (a *analysis) person(name, display, email string, active *bool) {
	if name == "" {
		return
	}
	k := strings.ToLower(name)
	u, ok := a.people[k]
	if !ok {
		u = &UserMatch{Name: name, Active: true}
		a.people[k] = u
	}
	u.DisplayName = cmp.Or(u.DisplayName, display)
	u.Email = cmp.Or(u.Email, email)
	if active != nil && !*active {
		u.Active = false
	}
}

// matchUsers decides how each user of the source will be matched, against the target if there is one.
func (a *analysis) matchUsers() error {
	l := a.l
	names := make([]string, 0, len(a.people))
	for k := range a.people {
		names = append(names, k)
	}
	slices.Sort(names)
	// Placeholder names that several users of the source would get (Kogge's user names are stricter).
	byPlaceholder := map[string][]string{}
	for _, k := range names {
		p := identity.ImportUsername(a.people[k].Name)
		byPlaceholder[p] = append(byPlaceholder[p], a.people[k].Name)
	}
	byHand := Decision{Code: "users-by-hand", Severity: "medium", Message: l.t("Check these users: their user name matches a Kogge user whose e-mail address differs, or several users of the source would get the same name in Kogge. Map them by hand if they are other people.")}
	for _, k := range names {
		u := a.people[k]
		if same := byPlaceholder[identity.ImportUsername(u.Name)]; len(same) > 1 {
			u.Note = l.t("Kogge user names cannot tell apart: %s", strings.Join(same, ", "))
			byHand.item(u.Name + ": " + u.Note)
		}
		if a.target == nil {
			u.Match = "unknown"
		} else {
			m, found, err := identity.FindImportUser(a.ctx, a.target.tx, u.Name, u.Email)
			if err != nil {
				return err
			}
			switch {
			case !found:
				u.Match, u.Kogge = "new", identity.ImportUsername(u.Name)
			case m.Placeholder:
				u.Match, u.Kogge = "placeholder", m.Username
			default:
				u.Match, u.Kogge = m.By, m.Username
				if m.By == "username" && u.Email != "" && m.Email != "" && !strings.EqualFold(u.Email, m.Email) {
					u.Note = l.t("the Kogge user has the e-mail address %s", m.Email)
					byHand.item(u.Name + ": " + u.Note)
				}
			}
		}
		if !u.Active && u.Note == "" {
			u.Note = l.t("inactive in the source: keeps authorship, joins no groups")
		}
		a.UserSummary[u.Match]++
		a.Users = append(a.Users, *u)
	}
	if len(byHand.Items) > 0 {
		a.Decisions = append(a.Decisions, byHand)
	}
	if n := a.UserSummary["new"]; n > 0 {
		a.decide("placeholders", "low", l.t("%d users have no Kogge account yet and become placeholders. Connect the company directory before the load, or create the accounts, so that they are matched.", n))
	}
	if n := a.UserSummary["unknown"]; n > 0 {
		a.decide("users-unmatched", "low", l.t("The %d users were not matched against a Kogge installation (no database was given): run the analysis with --database-url to see which become placeholders.", n))
	}
	return nil
}

// decide records a decision.
func (a *analysis) decide(code, severity, message string, items ...string) {
	d := Decision{Code: code, Severity: severity, Message: message}
	for _, it := range items {
		d.item(it)
	}
	a.Decisions = append(a.Decisions, d)
}

// decision records a decision with the items collected for it, if there are any.
func (a *analysis) decision(d Decision) {
	if len(d.Items) > 0 {
		a.Decisions = append(a.Decisions, d)
	}
}

// find records an occurrence of something that cannot be translated exactly.
func (a *analysis) find(kind, name, example, note string) {
	k := kind + "\x00" + name
	f, ok := a.findings[k]
	if !ok {
		f = &Finding{Kind: kind, Name: name, Note: note}
		a.findings[k] = f
	}
	f.Count++
	if example != "" && len(f.Examples) < 5 && !slices.Contains(f.Examples, example) {
		f.Examples = append(f.Examples, example)
	}
}

// legacyEntity returns the Kogge entity the load recorded for a source entity (in the target).
func (a *analysis) legacyEntity(typ, srcID string) (int64, bool, error) {
	if a.target == nil {
		return 0, false, nil
	}
	return legacy.Lookup(a.ctx, a.target.tx, a.source, typ, srcID)
}

// group says what a group of the source becomes in the target; renamed reports a group of another name.
func (a *analysis) group(name string) (how string, renamed bool, err error) {
	if a.target == nil {
		return a.l.t("created, or the group of an earlier migration"), false, nil
	}
	id, kname, err := identity.FindImportGroup(a.ctx, a.target.tx, name)
	if err != nil {
		return "", false, err
	}
	switch {
	case !strings.EqualFold(kname, name):
		return a.l.t("into %q: Kogge has a group %q of its own", kname, name), true, nil
	case id != 0:
		return a.l.t("into the group of an earlier migration"), false, nil
	}
	return a.l.t("created"), false, nil
}

// identityMatch returns the id of the Kogge user a user of the source matches by name, if any.
func identityMatch(a *analysis, name string) (int64, bool, error) {
	if a.target == nil {
		return 0, false, nil
	}
	m, found, err := identity.FindImportUser(a.ctx, a.target.tx, name, "")
	return m.ID, found, err
}

// declare lists something that cannot be translated exactly, also if nothing uses it.
func (a *analysis) declare(kind, name, note string) {
	if _, ok := a.findings[kind+"\x00"+name]; !ok {
		a.findings[kind+"\x00"+name] = &Finding{Kind: kind, Name: name, Note: note}
	}
}

// phase is a part of a load whose duration the estimate extrapolates.
type phase struct {
	name  string // as in Report.Timing
	items int
	bytes int64
}

// estimate extrapolates the duration of the load from the phases of earlier loads from this work directory.
func (a *analysis) estimate(report string, phases ...phase) error {
	var rep Report
	ok, err := a.work.Read(&rep, report)
	if err != nil || !ok || len(rep.Timing) == 0 {
		return err
	}
	var seconds float64
	var measured, missing []string
	for _, p := range phases {
		t, ok := rep.Timing[p.name]
		switch {
		case p.items == 0 && p.bytes == 0:
			continue
		case !ok || t.Seconds <= 0 || (p.items > 0 && t.Items == 0) || (p.bytes > 0 && p.items == 0 && t.Bytes == 0):
			missing = append(missing, p.name)
			continue
		}
		if p.items > 0 && t.Items > 0 {
			seconds += float64(p.items) * t.Seconds / float64(t.Items)
			measured = append(measured, a.l.t("%s: %d in %s", p.name, t.Items, a.l.duration(time.Duration(t.Seconds*float64(time.Second)))))
		} else {
			seconds += float64(p.bytes) * t.Seconds / float64(t.Bytes)
			measured = append(measured, a.l.t("%s: %s in %s", p.name, a.l.bytes(t.Bytes), a.l.duration(time.Duration(t.Seconds*float64(time.Second)))))
		}
	}
	if len(measured) == 0 {
		return nil
	}
	basis := a.l.t("extrapolated from the load of %s (%s)", a.l.time(rep.Started), strings.Join(measured, ", "))
	if len(missing) > 0 {
		basis += "; " + a.l.t("not included, as that load had none: %s", strings.Join(missing, ", "))
	}
	a.Estimate = &Estimate{Seconds: seconds, Basis: basis}
	return nil
}

// finish completes the report and writes it into the work directory.
func (a *analysis) finish() error {
	keys := make([]string, 0, len(a.findings))
	for k := range a.findings {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(x, y string) int {
		fx, fy := a.findings[x], a.findings[y]
		return cmp.Or(cmp.Compare(fx.Kind, fy.Kind), cmp.Compare(fy.Count, fx.Count), cmp.Compare(fx.Name, fy.Name))
	})
	for _, k := range keys {
		a.Untranslatable = append(a.Untranslatable, *a.findings[k])
	}
	order := map[string]int{"high": 0, "medium": 1, "low": 2}
	slices.SortStableFunc(a.Decisions, func(x, y Decision) int { return order[x.Severity] - order[y.Severity] })
	return writeReport(a.work, a.Product+"-analysis", a.Analysis, a.view())
}

// yesNo words a flag.
func (l lang) yesNo(b bool) string {
	if b {
		return l.t("yes")
	}
	return l.t("no")
}

// keyed formats an entity of the source with its key or id.
func keyed(name, key string) string {
	if key == "" || key == name {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, key)
}
