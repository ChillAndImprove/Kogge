// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

const software = `{
  "statuses": [1, 2, 3, 4], "initial": 1,
  "transitions": [
    {"id": 11, "name": "Start progress", "from": [1], "to": 2},
    {"id": 21, "name": "Request review", "from": [2], "to": 3, "screen": ["assignee"]},
    {"id": 31, "name": "Done", "from": "any", "to": 4,
     "conditions": [{"type": "permission", "permission": "issue.resolve"}],
     "postFunctions": [{"type": "set-resolution", "resolution": 1}]},
    {"id": 41, "name": "Reopen", "from": [4], "to": 1, "postFunctions": [{"type": "clear-resolution"}]}
  ],
  "options": {"autoResolution": true}
}`

func TestParseAndAvailable(t *testing.T) {
	d, err := Parse([]byte(software))
	if err != nil {
		t.Fatal(err)
	}
	if ps := d.Validate(func(int64) bool { return true }); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	names := func(ts []Transition) string {
		var n []string
		for _, t := range ts {
			n = append(n, t.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(d.From(1)); got != "Start progress,Done" {
		t.Errorf("from 1: %s", got)
	}
	if got := names(d.From(4)); got != "Reopen" {
		t.Errorf("from done (global transition to the same status hidden): %s", got)
	}
	// Round trip keeps "any".
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"from":"any"`) || !strings.Contains(string(b), `"from":[1]`) {
		t.Errorf("marshal: %s", b)
	}
}

func TestValidate(t *testing.T) {
	d := &Definition{Statuses: []int64{1, 2, 3, 2}, Initial: 9, Transitions: []Transition{
		{ID: 1, Name: "", From: []int64{1}, To: 5},
		{ID: 1, Name: "x", To: 2},
		{ID: 2, Name: "y", FromAny: true, To: 2, Conditions: []Rule{{Type: "magic"}, {Type: CondPermission}}},
	}}
	ps := d.Validate(func(id int64) bool { return id != 3 })
	text := ""
	for _, p := range ps {
		text += p.Path + ": " + p.Message + "\n"
	}
	for _, want := range []string{"listed twice", "status 3 does not exist", "initial status", "unique",
		"needs a name", "target status", "source statuses", "unknown type \"magic\"", "permission is required",
		"cannot be reached"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing problem %q in:\n%s", want, text)
		}
	}
}

func TestSimple(t *testing.T) {
	d := Simple([]int64{1, 2, 3}, map[int64]string{1: "To Do", 2: "In Progress", 3: "Done"})
	if ps := d.Validate(nil); len(ps) != 0 {
		t.Fatalf("problems: %v", ps)
	}
	if len(d.From(2)) != 2 {
		t.Errorf("from 2: %v", d.From(2))
	}
}
