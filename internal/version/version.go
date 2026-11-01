// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Package version describes the build: its version, the commit it was built from and its date. Release builds
// set them with -ldflags "-X kogge.de/kogge/internal/version.Version=… -X ….Commit=… -X ….Date=…" (Makefile,
// deploy/Dockerfile); a plain `go build` in a Git checkout takes the commit and its date from the build
// information Go records.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"time"
)

var (
	// Version of this build: 1.2.0 for a release, 1.2.0-3-g1a2b3c4 for a commit after one, dev without either.
	Version = "dev"
	// Commit is the Git commit the build was made from (40 hex digits), empty if unknown.
	Commit = ""
	// Date is the build's date (RFC 3339, UTC). Release builds take the date of the commit, so that building the
	// same commit again gives the same binary.
	Date = ""
)

// modified is set for builds from a working tree with changes that were not committed.
var modified bool

func init() {
	fromBuildInfo(debug.ReadBuildInfo())
}

// fromBuildInfo fills in the commit and the date from Go's version control information where the linker flags
// did not set them.
func fromBuildInfo(info *debug.BuildInfo, ok bool) {
	if !ok || Commit != "" {
		return
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			Commit = s.Value
		case "vcs.time":
			if Date == "" {
				Date = s.Value
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
}

// BuildTime returns Date as a time, or the zero time if the build has no date.
func BuildTime() time.Time {
	t, err := time.Parse(time.RFC3339, Date)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// Describe is the output of `kogge version` and `kogge-agent version`: the program and its version on the first
// line, then the commit, the date and the Go toolchain.
func Describe(program string) string {
	s := fmt.Sprintf("%s %s\n", program, Version)
	if Commit != "" {
		s += "commit: " + Commit
		if modified {
			s += " (with changes that were not committed)"
		}
		s += "\n"
	}
	if Date != "" {
		s += "built:  " + Date + "\n"
	}
	return s + fmt.Sprintf("go:     %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
