// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

// Command relnotes writes the release notes of a version from the Conventional Commits since the version before
// it: breaking changes, security fixes, features, fixes and performance improvements, and the database migrations
// an upgrade applies. The release workflow (.github/workflows/release.yml) and tools/release.sh use it.
//
//	go run ./tools/relnotes v1.2.0                 the notes of the tag v1.2.0, since the tag before it
//	go run ./tools/relnotes -version 1.2.0 HEAD    the notes of a release that is not tagged yet
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// commit is a commit as git log prints it with logFormat.
type commit struct {
	hash, subject, body string
}

// change is a commit read as a Conventional Commit.
type change struct {
	kind, scope, summary, hash string
	breaking                   bool
	// note is the text of a BREAKING CHANGE footer.
	note string
}

const logFormat = "%H%x00%s%x00%b%x1e"

var conventional = regexp.MustCompile(`^([a-z]+)(?:\(([^)]*)\))?(!)?: (.+)$`)

func main() {
	from := flag.String("from", "", "the previous version's tag (default: the tag before REV)")
	version := flag.String("version", "", "the version to name (default: REV without its leading v)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: relnotes [-from TAG] [-version VERSION] REV")
		os.Exit(2)
	}
	rev := flag.Arg(0)
	if *version == "" {
		*version = strings.TrimPrefix(rev, "v")
	}
	if *from == "" {
		// The newest version tag before REV; none for the first release.
		out, err := git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", rev+"^")
		if err == nil {
			*from = strings.TrimSpace(out)
		}
	}
	span := rev
	if *from != "" {
		span = *from + ".." + rev
	}
	log, err := git("log", "--no-merges", "--format="+logFormat, span)
	if err != nil {
		fail(err)
	}
	// The migrations an upgrade from the previous version applies (a first release has no upgrades).
	var migrations []string
	if *from != "" {
		out, err := git("log", "--diff-filter=A", "--name-only", "--format=", span, "--", "internal/migrations/*.sql")
		if err != nil {
			fail(err)
		}
		migrations = strings.Fields(out)
		slices.Sort(migrations)
	}
	fmt.Print(notes(*version, strings.TrimPrefix(*from, "v"), parseLog(log), migrations))
}

func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "relnotes:", err)
	os.Exit(1)
}

func parseLog(log string) []commit {
	var commits []commit
	for _, record := range strings.Split(log, "\x1e") {
		fields := strings.SplitN(strings.TrimLeft(record, "\n"), "\x00", 3)
		if len(fields) != 3 {
			continue
		}
		commits = append(commits, commit{hash: fields[0], subject: fields[1], body: fields[2]})
	}
	return commits
}

// parse reads a commit as a Conventional Commit; ok is false for other commits.
func parse(c commit) (change, bool) {
	m := conventional.FindStringSubmatch(c.subject)
	if m == nil {
		return change{}, false
	}
	ch := change{kind: m[1], scope: m[2], breaking: m[3] == "!", summary: m[4], hash: c.hash}
	// A footer BREAKING CHANGE: text (or BREAKING-CHANGE:) runs to the next footer or the end of the paragraph.
	lines := strings.Split(c.body, "\n")
	for i, line := range lines {
		text, ok := strings.CutPrefix(line, "BREAKING CHANGE:")
		if !ok {
			text, ok = strings.CutPrefix(line, "BREAKING-CHANGE:")
		}
		if !ok {
			continue
		}
		ch.breaking = true
		note := []string{strings.TrimSpace(text)}
		for _, more := range lines[i+1:] {
			if strings.TrimSpace(more) == "" || footer.MatchString(more) {
				break
			}
			note = append(note, strings.TrimSpace(more))
		}
		ch.note = strings.TrimSpace(strings.Join(note, " "))
		break
	}
	return ch, true
}

// footer is the start of a trailer such as Co-Authored-By: or Refs #12.
var footer = regexp.MustCompile(`^[A-Za-z][A-Za-z-]*(: | #)`)

// notes writes the release notes in Markdown.
func notes(version, previous string, commits []commit, migrations []string) string {
	sections := []struct {
		title string
		match func(change) bool
	}{
		{"Breaking changes", func(c change) bool { return c.breaking }},
		{"Security", func(c change) bool { return c.kind == "security" || c.scope == "security" }},
		{"Features", func(c change) bool { return c.kind == "feat" }},
		{"Fixes", func(c change) bool { return c.kind == "fix" }},
		{"Performance", func(c change) bool { return c.kind == "perf" }},
	}
	listed := make([][]change, len(sections))
	other := 0
	var unconventional []commit
	for _, c := range commits {
		ch, ok := parse(c)
		if !ok {
			unconventional = append(unconventional, c)
			continue
		}
		// A breaking change is listed among the breaking changes and in the first other section that fits.
		if ch.breaking {
			listed[0] = append(listed[0], ch)
		}
		placed := false
		for i := 1; i < len(sections) && !placed; i++ {
			if sections[i].match(ch) {
				listed[i] = append(listed[i], ch)
				placed = true
			}
		}
		if !placed && !ch.breaking {
			other++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Kogge %s\n\n", version)
	if previous != "" {
		fmt.Fprintf(&b, "The changes since %s.\n", previous)
	} else {
		b.WriteString("The first release.\n")
	}
	if len(migrations) > 0 {
		b.WriteString("\n### Upgrading\n\nWrite a backup before upgrading (`kogge admin backup`). The upgrade applies these " +
			"database migrations at its first start (the `migrate` service of the Compose stack, or `kogge migrate`); " +
			"they only go forward:\n\n")
		for _, m := range migrations {
			fmt.Fprintf(&b, "- `%s`\n", strings.TrimPrefix(m, "internal/migrations/"))
		}
	}
	for i, s := range sections {
		if len(listed[i]) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", s.title)
		for _, c := range listed[i] {
			b.WriteString("- ")
			if c.scope != "" {
				fmt.Fprintf(&b, "**%s:** ", c.scope)
			}
			fmt.Fprintf(&b, "%s (%s)\n", c.summary, c.hash[:min(len(c.hash), 12)])
			if i == 0 && c.note != "" {
				fmt.Fprintf(&b, "  %s\n", c.note)
			}
		}
	}
	if len(unconventional) > 0 {
		b.WriteString("\n### Other changes\n\n")
		for _, c := range unconventional {
			fmt.Fprintf(&b, "- %s (%s)\n", c.subject, c.hash[:min(len(c.hash), 12)])
		}
	}
	if other > 0 {
		fmt.Fprintf(&b, "\n%d more commits change documentation, tests, the build or the code's structure only.\n", other)
	}
	return b.String()
}
