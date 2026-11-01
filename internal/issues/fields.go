// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package issues

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/platform/apperr"
)

func fieldKey(id int64) string { return "cf_" + strconv.FormatInt(id, 10) }

func parseFieldKey(k string) (int64, bool) {
	rest, ok := strings.CutPrefix(k, "cf_")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil
}

// System field keys.
const (
	FieldSummary           = "summary"
	FieldDescription       = "description"
	FieldEnvironment       = "environment"
	FieldIssueType         = "issuetype"
	FieldStatus            = "status"
	FieldPriority          = "priority"
	FieldResolution        = "resolution"
	FieldAssignee          = "assignee"
	FieldReporter          = "reporter"
	FieldLabels            = "labels"
	FieldComponents        = "components"
	FieldFixVersions       = "fixVersions"
	FieldAffectsVersions   = "affectsVersions"
	FieldDueDate           = "duedate"
	FieldParent            = "parent"
	FieldOriginalEstimate  = "originalEstimate"
	FieldRemainingEstimate = "remainingEstimate"
	FieldSecurity          = "security"
)

// FieldInfo describes a field for clients (forms, columns, query builder).
type FieldInfo struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Custom       bool     `json:"custom"`
	Editable     bool     `json:"editable"`
	Orderable    bool     `json:"orderable"`
	Description  string   `json:"description,omitempty"`
	Options      []Option `json:"options,omitempty"`
	ProjectIDs   []int64  `json:"projectIds,omitempty"`
	IssueTypeIDs []int64  `json:"issueTypeIds,omitempty"`
}

var systemFields = []FieldInfo{
	{Key: FieldSummary, Name: "Summary", Type: "text", Editable: true, Orderable: true},
	{Key: FieldDescription, Name: "Description", Type: "textarea", Editable: true},
	{Key: FieldIssueType, Name: "Issue type", Type: "issuetype", Editable: true, Orderable: true},
	{Key: FieldStatus, Name: "Status", Type: "status", Orderable: true},
	{Key: FieldPriority, Name: "Priority", Type: "priority", Editable: true, Orderable: true},
	{Key: FieldResolution, Name: "Resolution", Type: "resolution", Editable: true, Orderable: true},
	{Key: FieldAssignee, Name: "Assignee", Type: "user", Editable: true, Orderable: true},
	{Key: FieldReporter, Name: "Reporter", Type: "user", Editable: true, Orderable: true},
	{Key: FieldLabels, Name: "Labels", Type: "labels", Editable: true},
	{Key: FieldComponents, Name: "Components", Type: "components", Editable: true},
	{Key: FieldFixVersions, Name: "Fix versions", Type: "multiversion", Editable: true},
	{Key: FieldAffectsVersions, Name: "Affected versions", Type: "multiversion", Editable: true},
	{Key: FieldDueDate, Name: "Due date", Type: "date", Editable: true, Orderable: true},
	{Key: FieldParent, Name: "Parent", Type: "issue", Editable: true},
	{Key: FieldOriginalEstimate, Name: "Original estimate", Type: "duration", Editable: true, Orderable: true},
	{Key: FieldRemainingEstimate, Name: "Remaining estimate", Type: "duration", Editable: true, Orderable: true},
	{Key: FieldSecurity, Name: "Security level", Type: "security", Editable: true},
	{Key: FieldEnvironment, Name: "Environment", Type: "text", Editable: true},
	{Key: FieldSprint, Name: "Sprint", Type: "sprint", Editable: true},
	{Key: "created", Name: "Created", Type: "datetime", Orderable: true},
	{Key: "updated", Name: "Updated", Type: "datetime", Orderable: true},
	{Key: "resolved", Name: "Resolved", Type: "datetime", Orderable: true},
	{Key: "timeSpent", Name: "Time spent", Type: "duration", Orderable: true},
	{Key: "votes", Name: "Votes", Type: "number", Orderable: true},
}

func systemField(key string) (FieldInfo, bool) {
	for _, f := range systemFields {
		if f.Key == key {
			return f, true
		}
	}
	return FieldInfo{}, false
}

func invalidField(key, msg string, v any) error {
	return apperr.Invalidf("issue.field-invalid", "The value of %s is not valid: %s", key, msg).WithFields(
		apperr.FieldError{Location: "body.fields." + key, Message: msg, Value: v})
}

// normaliseLabels trims labels, rejects whitespace and removes duplicates.
func normaliseLabels(key string, in []string) ([]string, error) {
	out := []string{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.IndexFunc(l, unicode.IsSpace) >= 0 || len(l) > 255 {
			return nil, invalidField(key, "labels must not contain spaces and have at most 255 characters", l)
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out, nil
}

func parseDate(key string, s string) (string, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return "", invalidField(key, "expected a date as YYYY-MM-DD", s)
	}
	return t.Format("2006-01-02"), nil
}

// customValue validates and normalises a custom field value. A JSON null clears the field (returns nil).
func customValue(f *CustomField, raw json.RawMessage, projectVersions map[int64]string) (any, error) {
	key := f.Key
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	optionOK := func(id int64, parent int64) bool {
		for _, o := range f.Options {
			if o.ID == id && !o.Disabled && o.ParentID == parent {
				return true
			}
		}
		return false
	}
	switch f.Type {
	case "text", "url":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, invalidField(key, "expected a string", string(raw))
		}
		s = strings.TrimSpace(s)
		if len(s) > 4000 {
			return nil, invalidField(key, "at most 4000 characters", nil)
		}
		if f.Type == "url" && s != "" {
			u, err := url.Parse(s)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, invalidField(key, "expected an http(s) URL", s)
			}
		}
		if s == "" {
			return nil, nil
		}
		return s, nil
	case "textarea":
		d, err := docmodel.Parse(raw, docmodel.Options{})
		if err != nil {
			return nil, invalidField(key, err.Error(), nil)
		}
		return d, nil
	case "number":
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, invalidField(key, "expected a number", string(raw))
		}
		return n, nil
	case "date":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, invalidField(key, "expected a date as YYYY-MM-DD", string(raw))
		}
		return parseDate(key, s)
	case "datetime":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, invalidField(key, "expected a date and time (RFC 3339)", string(raw))
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, invalidField(key, "expected a date and time (RFC 3339)", s)
		}
		return t.UTC().Format(time.RFC3339), nil
	case "select", "radio":
		var id int64
		if err := json.Unmarshal(raw, &id); err != nil || !optionOK(id, 0) {
			return nil, invalidField(key, "unknown option", string(raw))
		}
		return id, nil
	case "multiselect", "checkboxes":
		var ids []int64
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, invalidField(key, "expected a list of option ids", string(raw))
		}
		for _, id := range ids {
			if !optionOK(id, 0) {
				return nil, invalidField(key, "unknown option", id)
			}
		}
		if len(ids) == 0 {
			return nil, nil
		}
		slices.Sort(ids)
		return slices.Compact(ids), nil
	case "cascading":
		var v struct {
			Parent int64 `json:"parent"`
			Child  int64 `json:"child"`
		}
		if err := json.Unmarshal(raw, &v); err != nil || !optionOK(v.Parent, 0) || (v.Child != 0 && !optionOK(v.Child, v.Parent)) {
			return nil, invalidField(key, "unknown option", string(raw))
		}
		out := map[string]any{"parent": v.Parent}
		if v.Child != 0 {
			out["child"] = v.Child
		}
		return out, nil
	case "user", "group":
		var id int64
		if err := json.Unmarshal(raw, &id); err != nil || id <= 0 {
			return nil, invalidField(key, "expected an id", string(raw))
		}
		return id, nil
	case "multiuser":
		var ids []int64
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, invalidField(key, "expected a list of user ids", string(raw))
		}
		if len(ids) == 0 {
			return nil, nil
		}
		slices.Sort(ids)
		return slices.Compact(ids), nil
	case "labels":
		var ls []string
		if err := json.Unmarshal(raw, &ls); err != nil {
			return nil, invalidField(key, "expected a list of labels", string(raw))
		}
		ls, err := normaliseLabels(key, ls)
		if err != nil || len(ls) == 0 {
			return nil, err
		}
		return ls, nil
	case "version":
		var id int64
		if err := json.Unmarshal(raw, &id); err != nil {
			return nil, invalidField(key, "expected a version id", string(raw))
		}
		if _, ok := projectVersions[id]; !ok {
			return nil, invalidField(key, "the version does not belong to the project", id)
		}
		return id, nil
	case "multiversion":
		var ids []int64
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, invalidField(key, "expected a list of version ids", string(raw))
		}
		for _, id := range ids {
			if _, ok := projectVersions[id]; !ok {
				return nil, invalidField(key, "the version does not belong to the project", id)
			}
		}
		if len(ids) == 0 {
			return nil, nil
		}
		slices.Sort(ids)
		return slices.Compact(ids), nil
	}
	return nil, invalidField(key, "unsupported field type "+f.Type, nil)
}

// displayCustom renders a custom field value for the history.
func displayCustom(f *CustomField, v any, users func(int64) string, versions map[int64]string) string {
	if v == nil {
		return ""
	}
	option := func(id int64) string {
		for _, o := range f.Options {
			if o.ID == id {
				return o.Value
			}
		}
		return strconv.FormatInt(id, 10)
	}
	join := func(ids []int64, name func(int64) string) string {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = name(id)
		}
		return strings.Join(parts, ", ")
	}
	switch f.Type {
	case "select", "radio":
		if id, ok := toInt64(v); ok {
			return option(id)
		}
	case "multiselect", "checkboxes":
		return join(valueIDs(v), option)
	case "cascading":
		if m, ok := v.(map[string]any); ok {
			p, _ := toInt64(m["parent"])
			out := option(p)
			if c, ok := toInt64(m["child"]); ok && c != 0 {
				out += " - " + option(c)
			}
			return out
		}
	case "user":
		if id, ok := toInt64(v); ok {
			return users(id)
		}
	case "multiuser":
		return join(valueIDs(v), users)
	case "version":
		if id, ok := toInt64(v); ok {
			return versions[id]
		}
	case "multiversion":
		return join(valueIDs(v), func(id int64) string { return versions[id] })
	case "labels":
		if ls, ok := v.([]string); ok {
			return strings.Join(ls, " ")
		}
		if ls, ok := v.([]any); ok {
			parts := make([]string, len(ls))
			for i, l := range ls {
				parts[i] = fmt.Sprint(l)
			}
			return strings.Join(parts, " ")
		}
	case "textarea":
		if d, ok := v.(*docmodel.Doc); ok {
			return docmodel.Summary(d, 200)
		}
		return "…"
	case "number":
		if n, ok := v.(float64); ok {
			return strconv.FormatFloat(n, 'f', -1, 64)
		}
	}
	return fmt.Sprint(v)
}

// valueIDs returns the ids in a stored field value: one id or a list of ids.
func valueIDs(v any) []int64 {
	if n, ok := toInt64(v); ok {
		return []int64{n}
	}
	switch x := v.(type) {
	case []int64:
		return x
	case []any:
		var out []int64
		for _, e := range x {
			if n, ok := toInt64(e); ok {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

// fieldApplies reports whether a custom field is available for a project and issue type.
func fieldApplies(f *CustomField, projectID, typeID int64) bool {
	return (len(f.ProjectIDs) == 0 || slices.Contains(f.ProjectIDs, projectID)) &&
		(len(f.IssueTypeIDs) == 0 || slices.Contains(f.IssueTypeIDs, typeID))
}

// FormatDuration renders seconds as e.g. "1d 4h 30m" with the time tracking settings.
func FormatDuration(seconds int64, hoursPerDay, daysPerWeek int) string {
	if seconds <= 0 {
		return "0m"
	}
	minute, hour := int64(60), int64(3600)
	day := hour * int64(hoursPerDay)
	week := day * int64(daysPerWeek)
	var parts []string
	for _, u := range []struct {
		n    int64
		unit string
	}{{week, "w"}, {day, "d"}, {hour, "h"}, {minute, "m"}} {
		if seconds >= u.n {
			parts = append(parts, strconv.FormatInt(seconds/u.n, 10)+u.unit)
			seconds %= u.n
		}
	}
	return strings.Join(parts, " ")
}

// ParseDuration parses "1w 2d 3h 30m" (and plain minutes) with the time tracking settings.
func ParseDuration(s string, hoursPerDay, daysPerWeek int) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n * 60, nil
	}
	var total int64
	for _, part := range strings.Fields(s) {
		if len(part) < 2 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		unit := part[len(part)-1]
		n, err := strconv.ParseFloat(part[:len(part)-1], 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		var mult float64
		switch unit {
		case 'w':
			mult = float64(3600 * hoursPerDay * daysPerWeek)
		case 'd':
			mult = float64(3600 * hoursPerDay)
		case 'h':
			mult = 3600
		case 'm':
			mult = 60
		default:
			return 0, fmt.Errorf("invalid duration unit in %q", s)
		}
		total += int64(n * mult)
	}
	return total, nil
}
