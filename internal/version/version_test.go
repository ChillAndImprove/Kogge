// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package version

import (
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func restore(t *testing.T) {
	v, c, d, m := Version, Commit, Date, modified
	t.Cleanup(func() { Version, Commit, Date, modified = v, c, d, m })
}

func vcs(revision, at, dirty string) *debug.BuildInfo {
	return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "-trimpath", Value: "true"},
		{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: revision}, {Key: "vcs.time", Value: at},
		{Key: "vcs.modified", Value: dirty}}}
}

// TestBuildInfo: a build without linker flags takes the commit and its date from Go's build information; the
// linker flags win where they are set.
func TestBuildInfo(t *testing.T) {
	restore(t)
	Version, Commit, Date, modified = "dev", "", "", false
	fromBuildInfo(vcs("0123456789abcdef0123456789abcdef01234567", "2026-10-20T08:15:00Z", "true"), true)
	if Commit != "0123456789abcdef0123456789abcdef01234567" || Date != "2026-10-20T08:15:00Z" || !modified {
		t.Errorf("from the build information: %q %q %v", Commit, Date, modified)
	}
	if got := BuildTime(); !got.Equal(time.Date(2026, 10, 20, 8, 15, 0, 0, time.UTC)) {
		t.Errorf("BuildTime = %v", got)
	}

	Version, Commit, Date, modified = "1.2.0", "fedcba9876543210fedcba9876543210fedcba98", "2026-10-01T00:00:00Z", false
	fromBuildInfo(vcs("0123456789abcdef0123456789abcdef01234567", "2026-10-20T08:15:00Z", "true"), true)
	if Commit != "fedcba9876543210fedcba9876543210fedcba98" || Date != "2026-10-01T00:00:00Z" || modified {
		t.Errorf("linker flags replaced: %q %q %v", Commit, Date, modified)
	}

	Version, Commit, Date = "dev", "", ""
	fromBuildInfo(nil, false)
	if Commit != "" || Date != "" || !BuildTime().IsZero() {
		t.Errorf("without build information: %q %q", Commit, Date)
	}
}

func TestDescribe(t *testing.T) {
	restore(t)
	Version, Commit, Date, modified = "1.2.0", "fedcba9876543210fedcba9876543210fedcba98", "2026-10-01T00:00:00Z", false
	want := "kogge 1.2.0\ncommit: fedcba9876543210fedcba9876543210fedcba98\nbuilt:  2026-10-01T00:00:00Z\n" +
		"go:     " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + "\n"
	if got := Describe("kogge"); got != want {
		t.Errorf("Describe = %q, want %q", got, want)
	}
	modified = true
	if got := Describe("kogge"); !strings.Contains(got, "(with changes that were not committed)") {
		t.Errorf("a build with changes: %q", got)
	}
	Commit, Date = "", ""
	if got := Describe("kogge-agent"); !strings.HasPrefix(got, "kogge-agent 1.2.0\ngo:") {
		t.Errorf("without commit and date: %q", got)
	}
}
