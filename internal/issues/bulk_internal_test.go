// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/platform/apperr"
)

// TestApplyValues: adding keeps the current values first, removing keeps the order of the rest, replacing takes the
// new values once each; the result is never nil (an empty list is stored as []).
func TestApplyValues(t *testing.T) {
	for _, c := range []struct {
		mode            string
		current, values []string
		want            []string
	}{
		{"add", []string{"b", "a"}, []string{"a", "c", "c"}, []string{"b", "a", "c"}},
		{"add", nil, nil, []string{}},
		{"remove", []string{"a", "b", "c"}, []string{"b", "x"}, []string{"a", "c"}},
		{"remove", []string{"a"}, []string{"a"}, []string{}},
		{"replace", []string{"a", "b"}, []string{"c", "c", "a"}, []string{"c", "a"}},
		{"replace", []string{"a"}, nil, []string{}},
	} {
		got := applyValues(c.mode, c.current, c.values)
		if got == nil || !slices.Equal(got, c.want) {
			t.Errorf("%s %v with %v: %#v, want %v", c.mode, c.current, c.values, got, c.want)
		}
	}
	if got := applyValues("add", []int64{3, 1}, []int64{1, 2}); !slices.Equal(got, []int64{3, 1, 2}) {
		t.Errorf("ids: %v", got)
	}
}

// TestBulkOutcome: what the person may not do or what does not apply to an issue is skipped; invalid values and
// everything unexpected failed.
func TestBulkOutcome(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{apperr.Forbidden("issue.edit-denied", "You may not edit this issue."), "skipped"},
		{apperr.NotFound("issue.not-found", "The issue does not exist."), "skipped"},
		{apperr.Conflict("bulk.unchanged", "%s needed no change.", "SHOP-1"), "skipped"},
		{fmt.Errorf("moving: %w", apperr.Forbidden("issue.create-denied", "You may not create issues in this project.")), "skipped"},
		{apperr.Invalidf("issue.field-not-on-screen", "The field %s is not on the screen.", "Severity"), "skipped"},
		{apperr.Invalidf("issue.move-subtask", "Sub-tasks move with their parents."), "skipped"},
		{apperr.Invalidf("issue.field-invalid", "The value of %s is invalid.", "Due date"), "failed"},
		{apperr.Internal(errors.New("connection reset")), "failed"},
		{errors.New("connection reset"), "failed"},
	} {
		if got := bulkOutcome(c.err); got != c.want {
			t.Errorf("%v: %s, want %s", c.err, got, c.want)
		}
	}
}

// TestFieldPermission: a bulk edit of a field needs the permission that editing it in the issue view needs.
func TestFieldPermission(t *testing.T) {
	for key, want := range map[string]string{FieldAssignee: PermAssign, FieldReporter: PermModifyReporter,
		FieldFixVersions: PermResolve, FieldDueDate: PermSchedule, FieldSprint: PermSchedule, FieldSecurity: PermSetSecurity,
		FieldLabels: PermEdit, FieldPriority: PermEdit, FieldComponents: PermEdit, "cf_7": PermEdit} {
		if got := fieldPermission(key); got != want {
			t.Errorf("%s: %s, want %s", key, got, want)
		}
	}
	for _, key := range bulkFieldKeys {
		if !slices.Contains(ProjectPermissions, fieldPermission(key)) {
			t.Errorf("%s needs %s, which is no project permission", key, fieldPermission(key))
		}
	}
}

// TestMappingOf: the answers of a move plan are looked up by the issue type, and statuses by type and status.
func TestMappingOf(t *testing.T) {
	m := mappingOf(MoveInput{Types: []TypeMapping{{From: 1, To: 11}, {From: 2, To: 12}},
		Statuses: []StatusMapping{{Type: 11, From: 5, To: 15}, {Type: 12, From: 5, To: 25}}})
	if m.types[1] != 11 || m.types[2] != 12 || m.types[3] != 0 {
		t.Errorf("types: %v", m.types)
	}
	if m.statuses[[2]int64{11, 5}] != 15 || m.statuses[[2]int64{12, 5}] != 25 || m.statuses[[2]int64{11, 6}] != 0 {
		t.Errorf("statuses: %v", m.statuses)
	}
}

// TestKQLCatalogueNames: the catalogue lists every alias the compiler takes under the field it stands for, so that
// the query box knows the fields however they are written; functions are found whatever their case.
func TestKQLCatalogueNames(t *testing.T) {
	names := map[string]bool{}
	for _, f := range kqlFields {
		if names[canonicalField(f.Name)] {
			t.Errorf("%s is listed twice", f.Name)
		}
		names[canonicalField(f.Name)] = true
		for _, alias := range f.Aliases {
			if canonicalField(alias) != canonicalField(f.Name) {
				t.Errorf("the alias %s of %s means %s to the compiler", alias, f.Name, canonicalField(alias))
			}
		}
	}
	for alias, field := range fieldAliases {
		if alias == field {
			continue
		}
		if !slices.ContainsFunc(kqlFields, func(f KQLField) bool {
			return canonicalField(f.Name) == field && slices.ContainsFunc(f.Aliases, func(a string) bool { return strings.EqualFold(a, alias) })
		}) {
			t.Errorf("the alias %s of %s is missing from the catalogue", alias, field)
		}
	}
	if f := kqlFunction(" MEMBERSOF "); f == nil || f.Name != "membersOf" || f.Arg != "group" {
		t.Errorf("membersOf: %+v", f)
	}
	if kqlFunction("fields") != nil {
		t.Error("an unknown function is found")
	}
}
