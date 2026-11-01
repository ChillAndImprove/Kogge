// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package workflow models issue workflows: statuses, transitions with conditions, validators and
// post-functions. It validates definitions and lists the transitions available from a status; evaluating
// conditions against users and issues is up to the issues service.
package workflow

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Rule kinds.
const (
	// Conditions
	CondPermission   = "permission"
	CondRole         = "role"
	CondGroup        = "group"
	CondAssigneeOnly = "assignee-only"
	CondReporterOnly = "reporter-only"
	CondSubtasksDone = "subtasks-done"
	// Validators
	ValFieldRequired   = "field-required"
	ValCommentRequired = "comment-required"
	ValPermission      = "permission"
	// Post-functions
	PostSetResolution   = "set-resolution"
	PostClearResolution = "clear-resolution"
	PostAssignCurrent   = "assign-to-current-user"
	PostAssignReporter  = "assign-to-reporter"
	PostAssignLead      = "assign-to-lead"
	PostUnassign        = "unassign"
	PostSetField        = "set-field"
	PostAddComment      = "add-comment"
)

var conditionKinds = []string{CondPermission, CondRole, CondGroup, CondAssigneeOnly, CondReporterOnly, CondSubtasksDone}
var validatorKinds = []string{ValFieldRequired, ValCommentRequired, ValPermission}
var postKinds = []string{PostSetResolution, PostClearResolution, PostAssignCurrent, PostAssignReporter, PostAssignLead,
	PostUnassign, PostSetField, PostAddComment}

// Rule is a condition, validator or post-function with its parameters.
type Rule struct {
	Type       string `json:"type"`
	Permission string `json:"permission,omitempty"`
	Role       int64  `json:"role,omitempty"`
	Group      int64  `json:"group,omitempty"`
	Field      string `json:"field,omitempty"`
	Value      any    `json:"value,omitempty"`
	Resolution int64  `json:"resolution,omitempty"`
	Text       string `json:"text,omitempty"`
}

// Transition moves an issue to another status.
type Transition struct {
	ID            int      `json:"id"`
	Name          string   `json:"name"`
	From          []int64  `json:"-"`
	FromAny       bool     `json:"-"`
	To            int64    `json:"to"`
	Screen        []string `json:"screen,omitempty"`
	Conditions    []Rule   `json:"conditions,omitempty"`
	Validators    []Rule   `json:"validators,omitempty"`
	PostFunctions []Rule   `json:"postFunctions,omitempty"`
}

type transitionJSON struct {
	ID            int             `json:"id"`
	Name          string          `json:"name"`
	From          json.RawMessage `json:"from"`
	To            int64           `json:"to"`
	Screen        []string        `json:"screen,omitempty"`
	Conditions    []Rule          `json:"conditions,omitempty"`
	Validators    []Rule          `json:"validators,omitempty"`
	PostFunctions []Rule          `json:"postFunctions,omitempty"`
}

// MarshalJSON writes "from" as a list of status ids or "any".
func (t Transition) MarshalJSON() ([]byte, error) {
	from := json.RawMessage(`"any"`)
	if !t.FromAny {
		b, err := json.Marshal(nonNil(t.From))
		if err != nil {
			return nil, err
		}
		from = b
	}
	return json.Marshal(transitionJSON{ID: t.ID, Name: t.Name, From: from, To: t.To, Screen: t.Screen,
		Conditions: t.Conditions, Validators: t.Validators, PostFunctions: t.PostFunctions})
}

// UnmarshalJSON accepts "from" as a list of status ids or the string "any".
func (t *Transition) UnmarshalJSON(b []byte) error {
	var raw transitionJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*t = Transition{ID: raw.ID, Name: raw.Name, To: raw.To, Screen: raw.Screen, Conditions: raw.Conditions,
		Validators: raw.Validators, PostFunctions: raw.PostFunctions}
	from := strings.TrimSpace(string(raw.From))
	switch {
	case from == "" || from == "null" || from == `"any"`:
		t.FromAny = true
	default:
		if err := json.Unmarshal(raw.From, &t.From); err != nil {
			return fmt.Errorf("transition %q: from must be a list of status ids or \"any\"", raw.Name)
		}
	}
	return nil
}

func nonNil(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

// Options of a workflow.
type Options struct {
	// AutoResolution sets the resolution "Done" when an issue enters a done status without one, and clears the
	// resolution when it leaves the done category.
	AutoResolution bool `json:"autoResolution"`
}

// Definition is a workflow.
type Definition struct {
	Statuses    []int64      `json:"statuses"`
	Initial     int64        `json:"initial"`
	Transitions []Transition `json:"transitions"`
	Options     Options      `json:"options"`
}

// Problem is a validation finding.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Validate checks the definition; known reports whether a status id exists.
func (d *Definition) Validate(known func(statusID int64) bool) []Problem {
	var ps []Problem
	add := func(path, format string, args ...any) {
		ps = append(ps, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	if len(d.Statuses) == 0 {
		add("statuses", "a workflow needs at least one status")
	}
	seen := map[int64]bool{}
	for i, s := range d.Statuses {
		if seen[s] {
			add(fmt.Sprintf("statuses/%d", i), "status %d is listed twice", s)
		}
		seen[s] = true
		if known != nil && !known(s) {
			add(fmt.Sprintf("statuses/%d", i), "status %d does not exist", s)
		}
	}
	if !seen[d.Initial] {
		add("initial", "the initial status must be one of the workflow's statuses")
	}
	ids := map[int]bool{}
	for i, t := range d.Transitions {
		path := fmt.Sprintf("transitions/%d", i)
		if t.ID <= 0 || ids[t.ID] {
			add(path+"/id", "transition ids must be positive and unique")
		}
		ids[t.ID] = true
		if strings.TrimSpace(t.Name) == "" {
			add(path+"/name", "a transition needs a name")
		}
		if !seen[t.To] {
			add(path+"/to", "the target status is not part of the workflow")
		}
		for j, f := range t.From {
			if !seen[f] {
				add(fmt.Sprintf("%s/from/%d", path, j), "status %d is not part of the workflow", f)
			}
		}
		if !t.FromAny && len(t.From) == 0 {
			add(path+"/from", "a transition needs source statuses or \"any\"")
		}
		check := func(kind string, rules []Rule, allowed []string) {
			for j, r := range rules {
				if !slices.Contains(allowed, r.Type) {
					add(fmt.Sprintf("%s/%s/%d", path, kind, j), "unknown type %q", r.Type)
				}
				if (r.Type == CondPermission || r.Type == ValPermission) && r.Permission == "" {
					add(fmt.Sprintf("%s/%s/%d", path, kind, j), "a permission is required")
				}
				if (r.Type == ValFieldRequired || r.Type == PostSetField) && r.Field == "" {
					add(fmt.Sprintf("%s/%s/%d", path, kind, j), "a field is required")
				}
			}
		}
		check("conditions", t.Conditions, conditionKinds)
		check("validators", t.Validators, validatorKinds)
		check("postFunctions", t.PostFunctions, postKinds)
	}
	// Every status must be reachable from the initial status.
	reach := map[int64]bool{d.Initial: true}
	for changed := true; changed; {
		changed = false
		for _, t := range d.Transitions {
			if reach[t.To] {
				continue
			}
			ok := t.FromAny
			for _, f := range t.From {
				ok = ok || reach[f]
			}
			if ok {
				reach[t.To], changed = true, true
			}
		}
	}
	for _, s := range d.Statuses {
		if !reach[s] {
			add("statuses", "status %d cannot be reached from the initial status", s)
		}
	}
	return ps
}

// From lists the transitions available from a status (before conditions), excluding transitions to the same
// status for global transitions.
func (d *Definition) From(status int64) []Transition {
	var out []Transition
	for _, t := range d.Transitions {
		if t.FromAny && t.To == status {
			continue
		}
		if t.FromAny || slices.Contains(t.From, status) {
			out = append(out, t)
		}
	}
	return out
}

// Transition returns the transition with the id.
func (d *Definition) Transition(id int) (Transition, bool) {
	for _, t := range d.Transitions {
		if t.ID == id {
			return t, true
		}
	}
	return Transition{}, false
}

// HasStatus reports whether the status is part of the workflow.
func (d *Definition) HasStatus(id int64) bool { return slices.Contains(d.Statuses, id) }

// Parse decodes a definition.
func Parse(raw []byte) (*Definition, error) {
	var d Definition
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Simple builds a workflow where every status can be reached from every other status by a transition named
// after the target (good for Kanban and business projects).
func Simple(statuses []int64, names map[int64]string) *Definition {
	d := &Definition{Statuses: statuses, Initial: statuses[0], Options: Options{AutoResolution: true}}
	for i, s := range statuses {
		d.Transitions = append(d.Transitions, Transition{ID: (i + 1) * 10, Name: names[s], FromAny: true, To: s})
	}
	return d
}
