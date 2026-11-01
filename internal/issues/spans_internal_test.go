// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func ref(v int64) *int64 { return &v }

func describeSpans(spans []span) string {
	var parts []string
	for _, s := range spans {
		v := "∅"
		if s.value != nil {
			v = fmt.Sprint(*s.value)
		}
		end := "…"
		if s.end != nil {
			end = s.end.Format("02")
		}
		by := "-"
		if s.setter != nil {
			by = fmt.Sprint(*s.setter)
		}
		parts = append(parts, fmt.Sprintf("%s %s-%s by %s", v, s.start.Format("02"), end, by))
	}
	return strings.Join(parts, ", ")
}

func day(d int) time.Time { return time.Date(2026, 10, d, 9, 0, 0, 0, time.UTC) }

// TestFieldSpans: a field's values follow its changes, from the value it had when the issue was created.
func TestFieldSpans(t *testing.T) {
	history := []historyEntry{
		{at: day(3), actor: ref(7), items: []ChangeItem{{Field: FieldAssignee, From: nil, To: json.Number("8")}}},
		{at: day(5), actor: ref(8), items: []ChangeItem{{Field: FieldStatus, From: json.Number("1"), To: json.Number("2")},
			{Field: FieldAssignee, From: json.Number("8"), To: json.Number("9")}}},
		{at: day(9), actor: ref(9), items: []ChangeItem{{Field: FieldAssignee, From: json.Number("9"), To: nil}}},
	}
	if got := describeSpans(fieldSpans(FieldAssignee, day(1), ref(7), nil, history)); got != "∅ 01-03 by 7, 8 03-05 by 7, 9 05-09 by 8, ∅ 09-… by 9" {
		t.Errorf("assignee: %s", got)
	}
	if got := describeSpans(fieldSpans(FieldStatus, day(1), ref(7), ref(2), history)); got != "1 01-05 by 7, 2 05-… by 8" {
		t.Errorf("status: %s", got)
	}
	// Without changes, the current value since the creation.
	if got := describeSpans(fieldSpans(FieldPriority, day(1), ref(7), ref(3), history)); got != "3 01-… by 7" {
		t.Errorf("priority: %s", got)
	}
}

// TestVersionSpans: fix versions are sets; Kogge records them whole (ids, or names in older entries), imported
// history adds or removes one version at a time. Both give a span per version and stay.
func TestVersionSpans(t *testing.T) {
	byName := map[string]int64{"1.0": 10, "1.1": 11, "2.0": 20}
	// Kogge: none → {1.0} (by names, as older entries) → {1.0, 2.0} (ids) → {2.0}.
	kogge := []historyEntry{
		{at: day(2), actor: ref(7), items: []ChangeItem{{Field: FieldFixVersions, FromString: "", ToString: "1.0"}}},
		{at: day(4), actor: ref(8), items: []ChangeItem{{Field: FieldFixVersions, From: []any{json.Number("10")},
			To: []any{json.Number("10"), json.Number("20")}, FromString: "1.0", ToString: "1.0, 2.0"}}},
		{at: day(6), actor: ref(9), items: []ChangeItem{{Field: FieldFixVersions, From: []any{json.Number("10"), json.Number("20")},
			To: []any{json.Number("20")}, FromString: "1.0, 2.0", ToString: "2.0"}}},
	}
	if got := describeSpans(versionSpans(day(1), ref(7), []int64{20}, kogge, byName)); got != "10 02-06 by 7, 20 04-… by 8" {
		t.Errorf("Kogge's history: %s", got)
	}
	// Imported: {1.1} at creation, then +2.0, then −1.1 (the current set is {2.0}).
	imported := []historyEntry{
		{at: day(3), actor: ref(7), items: []ChangeItem{{Field: FieldFixVersions, To: json.Number("20"), ToString: "2.0"}}},
		{at: day(8), actor: ref(8), items: []ChangeItem{{Field: FieldFixVersions, From: json.Number("11"), FromString: "1.1"}}},
	}
	if got := describeSpans(versionSpans(day(1), ref(7), []int64{20}, imported, byName)); got != "11 01-08 by 7, 20 03-… by 7" {
		t.Errorf("imported history: %s", got)
	}
	// No history: the current versions since the creation.
	if got := describeSpans(versionSpans(day(1), ref(7), []int64{11, 10}, nil, byName)); got != "10 01-… by 7, 11 01-… by 7" {
		t.Errorf("no history: %s", got)
	}
}
