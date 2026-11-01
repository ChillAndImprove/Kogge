// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"html/template"
	"slices"
	"strings"
	"time"
)

// The analysis before a load and the validation after it write their reports into
// the work directory twice: as JSON for scripts, and as a self-contained HTML page for the administrators of the
// customer, which can be printed or sent on.

// Analysis is the report of the analysis of an extraction: what is there, how it will be translated, what cannot
// be translated exactly, and the decisions the administrator must take before the load.
type Analysis struct {
	Product        string           `json:"product"`
	Source         string           `json:"source"`
	Workdir        string           `json:"workdir"`
	Language       string           `json:"language"`
	Generated      time.Time        `json:"generated"`
	Extraction     *ExtractionState `json:"extraction,omitempty"`
	Target         string           `json:"target,omitempty" doc:"The Kogge installation the users and keys were matched against"`
	Counts         []Figure         `json:"counts"`
	Sizes          []Figure         `json:"sizes"`
	Users          []UserMatch      `json:"users"`
	UserSummary    map[string]int   `json:"userSummary" doc:"Users by how they will be matched"`
	Translation    []Table          `json:"translation" doc:"Configuration and how it will be translated"`
	Untranslatable []Finding        `json:"untranslatable" doc:"What cannot be translated exactly"`
	Decisions      []Decision       `json:"decisions" doc:"What the administrator must decide before the load"`
	Estimate       *Estimate        `json:"estimate,omitempty"`
	Notes          []ExtractionNote `json:"extractionNotes,omitempty" doc:"What the extraction could not read"`
}

// Figure is a number of the source (entities, bytes).
type Figure struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value int64  `json:"value"`
	Bytes bool   `json:"bytes,omitempty"`
}

// UserMatch says how a user of the source becomes a Kogge user.
type UserMatch struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Active      bool   `json:"active"`
	// Match is username or email (an existing Kogge user), placeholder (a placeholder of an earlier migration), new
	// (a new placeholder) or unknown (no target installation was given).
	Match string `json:"match"`
	Kogge string `json:"kogge,omitempty" doc:"The Kogge user name"`
	Note  string `json:"note,omitempty"`
}

// Table is a table of a report.
type Table struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Intro   string     `json:"intro,omitempty"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Classes marks rows (pass, warn, fail, muted), by index.
	Classes []string `json:"classes,omitempty"`
	More    int      `json:"more,omitempty" doc:"Rows left out"`
	// MoreText says so on the page.
	MoreText string `json:"-"`
}

// add appends a row, or counts it as left out beyond max rows.
func (t *Table) add(class string, max int, cells ...string) {
	if max > 0 && len(t.Rows) >= max {
		t.More++
		return
	}
	t.Rows = append(t.Rows, cells)
	t.Classes = append(t.Classes, class)
}

// Finding is something that cannot be translated exactly, with how often it occurs and examples.
type Finding struct {
	Kind     string   `json:"kind" doc:"macro, field, permission, workflow-function, …"`
	Name     string   `json:"name"`
	Count    int      `json:"count"`
	Examples []string `json:"examples,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// Decision is something the administrator must decide or do.
type Decision struct {
	Code     string   `json:"code" doc:"Stable identifier, e.g. key-collision"`
	Severity string   `json:"severity" doc:"high (the load fails or overwrites data), medium (data or rights are lost or changed), low"`
	Message  string   `json:"message"`
	Items    []string `json:"items,omitempty"`
	More     int      `json:"more,omitempty"`
}

// Estimate is the expected duration of the load, from the measured throughput of earlier loads.
type Estimate struct {
	Seconds float64 `json:"seconds"`
	Basis   string  `json:"basis"`
}

// Validation is the report of the validation after a load: checks with their outcome.
type Validation struct {
	Product   string    `json:"product"`
	Source    string    `json:"source"`
	Workdir   string    `json:"workdir"`
	Language  string    `json:"language"`
	Generated time.Time `json:"generated"`
	Passed    int       `json:"passed"`
	Warnings  int       `json:"warnings"`
	Failed    int       `json:"failed"`
	Checks    []Check   `json:"checks"`
}

// Check is one check of a validation.
type Check struct {
	ID      string   `json:"id" doc:"Stable identifier, e.g. counts.issues"`
	Group   string   `json:"group" doc:"counts, attachments, links, renderings, permissions, history, repositories, proposals"`
	Status  string   `json:"status" enum:"pass,warn,fail"`
	Title   string   `json:"title"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
	More    int      `json:"more,omitempty" doc:"Details left out"`
}

// Outcomes of checks.
const (
	Pass = "pass"
	Warn = "warn"
	Fail = "fail"
)

// maxDetails bounds the details of a check and the items of a decision.
const maxDetails = 50

// detail appends a detail unless there are enough already (then it is counted).
func (c *Check) detail(s string) {
	if len(c.Details) >= maxDetails {
		c.More++
		return
	}
	c.Details = append(c.Details, s)
}

// item appends an item unless there are enough already (then it is counted).
func (d *Decision) item(s string) {
	if len(d.Items) >= maxDetails {
		d.More++
		return
	}
	d.Items = append(d.Items, s)
}

// add records a check.
func (v *Validation) add(c Check) {
	switch c.Status {
	case Pass:
		v.Passed++
	case Warn:
		v.Warnings++
	default:
		c.Status = Fail
		v.Failed++
	}
	v.Checks = append(v.Checks, c)
}

// --- writing ---

//go:embed report.html
var reportTemplate string

var reportPage = template.Must(template.New("report").Funcs(template.FuncMap{
	"rowClass": func(classes []string, i int) string {
		if i < len(classes) {
			return classes[i]
		}
		return ""
	},
}).Parse(reportTemplate))

// reportView is what the HTML page shows.
type reportView struct {
	Lang     string
	Title    string
	Subtitle string
	Facts    [][2]string
	Tiles    []tile
	Sections []section
	Footer   string
}

type tile struct {
	Label, Value, Class string
}

type section struct {
	ID     string
	Title  string
	Intro  []string
	Items  []item
	Tables []Table
}

type item struct {
	Class   string
	Badge   string
	Title   string
	Text    string
	Details []string
	More    string
}

// writeReport stores a report as name.json and name.html in the work directory.
func writeReport(w Workdir, name string, data any, view reportView) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := w.Write(b, name+".json"); err != nil {
		return err
	}
	l := lang(view.Lang)
	for i := range view.Sections {
		for j := range view.Sections[i].Tables {
			if t := &view.Sections[i].Tables[j]; t.More > 0 {
				t.MoreText = l.t("and %d more", t.More)
			}
		}
	}
	var page bytes.Buffer
	if err := reportPage.Execute(&page, view); err != nil {
		return err
	}
	return writeFile(w.Path(name+".html"), page.Bytes())
}

// productName is the name of a migrated product.
func productName(p string) string {
	return map[string]string{"jira": "Jira", "confluence": "Confluence", "bitbucket": "Bitbucket", "bamboo": "Bamboo"}[p]
}

// view builds the HTML page of an analysis.
func (a *Analysis) view() reportView {
	l := lang(a.Language)
	v := reportView{Lang: a.Language, Title: l.t("Analysis of the %s migration", productName(a.Product)), Subtitle: a.Source,
		Footer: l.t("Written by Kogge on %s from the work directory %s.", l.time(a.Generated), a.Workdir)}
	v.Facts = append(v.Facts, [2]string{l.t("Source"), a.Source}, [2]string{l.t("Work directory"), a.Workdir})
	if a.Extraction != nil {
		state := l.t("complete")
		if a.Extraction.Finished == nil {
			state = l.t("not finished: resume the extraction before the load")
		}
		v.Facts = append(v.Facts, [2]string{l.t("Extraction"), l.t("started %s, %s", l.time(a.Extraction.Started), state)})
		if files, ok := a.Extraction.Options["attachments"].(bool); ok && !files {
			v.Facts = append(v.Facts, [2]string{l.t("Attachments"), l.t("not extracted (trial run)")})
		}
	}
	target := l.t("none: users and keys were not matched against a Kogge installation")
	if a.Target != "" {
		target = a.Target
	}
	v.Facts = append(v.Facts, [2]string{l.t("Target"), target})

	high, medium := 0, 0
	for _, d := range a.Decisions {
		switch d.Severity {
		case "high":
			high++
		case "medium":
			medium++
		}
	}
	// The first figures of a product are its main ones.
	for _, f := range a.Counts[:min(4, len(a.Counts))] {
		v.Tiles = append(v.Tiles, tile{Label: f.Label, Value: l.n(f.Value)})
	}
	if len(a.Sizes) > 0 && a.Sizes[0].Bytes {
		v.Tiles = append(v.Tiles, tile{Label: a.Sizes[0].Label, Value: l.bytes(a.Sizes[0].Value)})
	}
	class := Pass
	if high > 0 {
		class = Fail
	} else if medium > 0 {
		class = Warn
	}
	v.Tiles = append(v.Tiles, tile{Label: l.t("Decisions"), Value: l.n(int64(len(a.Decisions))), Class: class})
	if a.Estimate != nil {
		v.Tiles = append(v.Tiles, tile{Label: l.t("Estimated load"), Value: l.duration(time.Duration(a.Estimate.Seconds * float64(time.Second)))})
	}

	// Decisions first: they are what the administrator has to act on.
	dec := section{ID: "decisions", Title: l.t("Decisions to take before the load")}
	if len(a.Decisions) == 0 {
		dec.Intro = append(dec.Intro, l.t("Nothing needs a decision."))
	}
	for _, d := range a.Decisions {
		it := item{Class: map[string]string{"high": Fail, "medium": Warn}[d.Severity], Text: d.Message, Details: d.Items,
			Badge: map[string]string{"high": l.t("high"), "medium": l.t("medium"), "low": l.t("low")}[d.Severity]}
		if it.Class == "" {
			it.Class = "muted"
		}
		if d.More > 0 {
			it.More = l.t("and %d more", d.More)
		}
		dec.Items = append(dec.Items, it)
	}
	v.Sections = append(v.Sections, dec)

	counts := section{ID: "counts", Title: l.t("What the extraction holds")}
	t := Table{ID: "counts", Columns: []string{l.t("Entities"), l.t("Number")}}
	for _, f := range a.Counts {
		t.add("", 0, f.Label, l.n(f.Value))
	}
	counts.Tables = append(counts.Tables, t)
	if len(a.Sizes) > 0 {
		s := Table{ID: "sizes", Columns: []string{l.t("Data"), l.t("Size")}}
		for _, f := range a.Sizes {
			value := l.n(f.Value)
			if f.Bytes {
				value = l.bytes(f.Value)
			}
			s.add("", 0, f.Label, value)
		}
		counts.Tables = append(counts.Tables, s)
	}
	if a.Estimate != nil {
		counts.Intro = append(counts.Intro, l.t("The load is expected to take about %s (%s).",
			l.duration(time.Duration(a.Estimate.Seconds*float64(time.Second))), a.Estimate.Basis))
	} else {
		counts.Intro = append(counts.Intro, l.t("No estimate of the load's duration: no load has run from this work directory yet. A trial load into a test installation measures the throughput; the next analysis estimates from it."))
	}
	v.Sections = append(v.Sections, counts)

	users := section{ID: "users", Title: l.t("Users and how they will be matched")}
	users.Intro = append(users.Intro, l.t("Users are matched to Kogge users by user name, then by verified e-mail address; the others become inactive placeholder users that keep authorship and assignments and become the person's account at the first sign-in with the same user name."))
	var parts []string
	for _, k := range []string{"username", "email", "placeholder", "new", "unknown"} {
		if n := a.UserSummary[k]; n > 0 {
			parts = append(parts, matchLabel(l, k)+": "+l.n(int64(n)))
		}
	}
	if len(parts) > 0 {
		users.Intro = append(users.Intro, strings.Join(parts, " · "))
	}
	ut := Table{ID: "users", Columns: []string{l.t("User name"), l.t("Name"), l.t("E-mail address"), l.t("Match"), l.t("Kogge user"), l.t("Note")}}
	for _, u := range a.Users {
		class := ""
		switch {
		case u.Note != "":
			class = Warn
		case u.Match == "new" || u.Match == "placeholder":
			class = "muted"
		}
		name := u.Name
		if !u.Active {
			name += " " + l.t("(inactive)")
		}
		ut.add(class, 2000, name, u.DisplayName, u.Email, matchLabel(l, u.Match), u.Kogge, u.Note)
	}
	users.Tables = append(users.Tables, ut)
	v.Sections = append(v.Sections, users)

	conf := section{ID: "translation", Title: l.t("Configuration and how it will be translated"), Tables: a.Translation}
	v.Sections = append(v.Sections, conf)

	un := section{ID: "untranslatable", Title: l.t("What cannot be translated exactly")}
	if len(a.Untranslatable) == 0 {
		un.Intro = append(un.Intro, l.t("Everything found can be translated."))
	}
	ft := Table{ID: "untranslatable", Columns: []string{l.t("Kind"), l.t("Name"), l.t("Occurrences"), l.t("Examples"), l.t("What happens")}}
	for _, f := range a.Untranslatable {
		ft.add("", 0, f.Kind, f.Name, l.n(int64(f.Count)), strings.Join(f.Examples, "; "), f.Note)
	}
	if len(ft.Rows) > 0 {
		un.Tables = append(un.Tables, ft)
	}
	v.Sections = append(v.Sections, un)

	if len(a.Notes) > 0 {
		notes := section{ID: "extraction", Title: l.t("What the extraction could not read")}
		nt := Table{ID: "notes", Columns: []string{l.t("Kind"), l.t("Id"), l.t("Name"), l.t("Note")}}
		for _, n := range a.Notes {
			nt.add(Warn, 500, n.Kind, n.ID, n.Name, n.Note)
		}
		notes.Tables = append(notes.Tables, nt)
		v.Sections = append(v.Sections, notes)
	}
	return v
}

func matchLabel(l lang, m string) string {
	switch m {
	case "username":
		return l.t("by user name")
	case "email":
		return l.t("by e-mail address")
	case "placeholder":
		return l.t("placeholder of an earlier migration")
	case "new":
		return l.t("new placeholder")
	}
	return l.t("not matched (no target)")
}

// view builds the HTML page of a validation.
func (v *Validation) view() reportView {
	l := lang(v.Language)
	out := reportView{Lang: v.Language, Title: l.t("Validation of the %s migration", productName(v.Product)), Subtitle: v.Source,
		Footer: l.t("Written by Kogge on %s from the work directory %s.", l.time(v.Generated), v.Workdir)}
	out.Facts = append(out.Facts, [2]string{l.t("Source"), v.Source}, [2]string{l.t("Work directory"), v.Workdir})
	result := l.t("passed")
	if v.Failed > 0 {
		result = l.t("failed")
	} else if v.Warnings > 0 {
		result = l.t("passed with warnings")
	}
	out.Facts = append(out.Facts, [2]string{l.t("Result"), result})
	out.Tiles = []tile{{Label: l.t("Passed"), Value: l.n(int64(v.Passed)), Class: Pass}, {Label: l.t("Warnings"), Value: l.n(int64(v.Warnings)), Class: Warn},
		{Label: l.t("Failed"), Value: l.n(int64(v.Failed)), Class: Fail}}
	var groups []string
	for _, c := range v.Checks {
		if !slices.Contains(groups, c.Group) {
			groups = append(groups, c.Group)
		}
	}
	for _, g := range groups {
		s := section{ID: g, Title: groupTitle(l, g)}
		for _, c := range v.Checks {
			if c.Group != g {
				continue
			}
			it := item{Class: c.Status, Badge: map[string]string{Pass: l.t("pass"), Warn: l.t("warning"), Fail: l.t("fail")}[c.Status],
				Title: c.Title, Text: c.Message, Details: c.Details}
			if c.More > 0 {
				it.More = l.t("and %d more", c.More)
			}
			s.Items = append(s.Items, it)
		}
		out.Sections = append(out.Sections, s)
	}
	return out
}

func groupTitle(l lang, g string) string {
	switch g {
	case "counts":
		return l.t("Counts")
	case "attachments":
		return l.t("Attachment checksums")
	case "links":
		return l.t("Link integrity")
	case "renderings":
		return l.t("Sample renderings")
	case "permissions":
		return l.t("Permission spot checks")
	case "history":
		return l.t("History and authors")
	case "repositories":
		return l.t("Repositories")
	case "proposals":
		return l.t("Pipeline proposals")
	}
	return g
}
