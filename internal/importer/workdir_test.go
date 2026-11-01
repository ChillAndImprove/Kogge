// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkdirStaysInside: parts of work directory paths come from the source system; none of them leaves the
// directory, and distinct parts stay distinct (audit M16: an attachment id "../../x" wrote above it).
func TestWorkdirStaysInside(t *testing.T) {
	w := Workdir{Root: "/var/lib/kogge-import/work"}
	for _, parts := range [][]string{
		{"jira", "attachments", "../../../escaped.txt"},
		{"confluence", "..", "x"},
		{"..\\..\\windows"},
		{"a/../../b"},
		{".", "..", "%2E%2E"},
	} {
		p := w.Path(parts...)
		if rel, err := filepath.Rel(w.Root, p); err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			t.Errorf("Path(%q) = %s, outside of the work directory", parts, p)
		}
	}
	if a, b := w.Path("a/b"), w.Path("a%2Fb"); a == b {
		t.Errorf("distinct parts share a path: %s", a)
	}
	if a, b := w.Path("x", ".."), w.Path("x", "%2E%2E"); a == b {
		t.Errorf("distinct parts share a path: %s", a)
	}
	if got := w.Path("jira", "issues", "SHOP-1.json"); got != "/var/lib/kogge-import/work/jira/issues/SHOP-1.json" {
		t.Errorf("ordinary parts changed: %s", got)
	}
}
