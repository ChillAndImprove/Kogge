// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package git

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAtLeast(t *testing.T) {
	for v, want := range map[string]bool{
		"2.39.0": false, "2.39.1": true, "2.39.5": true, "2.40.0": true, "2.43.0": true, "3.0.0": true,
		"2.38.4": false, "1.9.9": false, "2.39": false, "2.45.2.windows.1": true, "garbage": false,
	} {
		if got := atLeast(v, 2, 39, 1); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
}

func TestObjectID(t *testing.T) {
	for v, want := range map[string]bool{
		"03e941503a36ef87de411d004700bd7237efc87e":                         true,
		"03e941503a36ef87de411d004700bd7237efc87":                          false,
		"03E941503A36EF87DE411D004700BD7237EFC87E":                         false,
		"--output=/tmp/x":                                                  false,
		"8b1d1f3bb3bfd2f0b1e1b6a9bfb5c1a2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f708": true,
	} {
		if got := objectID(v); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
}

// TestChangedPaths: the files that differ between two commits, renames as two paths, and no list beyond the limit.
func TestChangedPaths(t *testing.T) {
	g, err := New(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	r, err := g.Init(ctx, filepath.Join(t.TempDir(), "r.git"), "main")
	if err != nil {
		t.Fatal(err)
	}
	commit := func(parent string, files map[string]string) string {
		t.Helper()
		tree := ""
		if parent != "" {
			if tree, err = r.TreeOf(ctx, parent); err != nil {
				t.Fatal(err)
			}
		}
		for path, content := range files {
			blob := ""
			if content != "" {
				if blob, err = r.HashObject(ctx, []byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			if tree, err = r.TreeWith(ctx, tree, path, blob, "100644"); err != nil {
				t.Fatal(err)
			}
		}
		var parents []string
		if parent != "" {
			parents = []string{parent}
		}
		sha, err := r.CommitTree(ctx, tree, parents, "change\n", Signature{Name: "A", Email: "a@example.com"}, Signature{Name: "A", Email: "a@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	first := commit("", map[string]string{"README.md": "r\n", "web/app.ts": "a\n", "docs/guide.md": "g\n"})
	second := commit(first, map[string]string{"web/app.ts": "b\n", "docs/guide.md": "", "docs/manual.md": "g\n", "a b.txt": "x\n"})
	for _, c := range []struct {
		from, to string
		limit    int
		want     []string
		complete bool
	}{
		{first, second, 10, []string{"a b.txt", "docs/guide.md", "docs/manual.md", "web/app.ts"}, true},
		{EmptyTree, first, 10, []string{"README.md", "docs/guide.md", "web/app.ts"}, true},
		{second, second, 10, []string{}, true},
		{first, second, 3, nil, false},
	} {
		got, complete, err := r.ChangedPaths(ctx, c.from, c.to, c.limit)
		if err != nil || complete != c.complete || !slices.Equal(got, c.want) || (c.want != nil && got == nil) {
			t.Errorf("%.7s..%.7s (limit %d) = %q %v %v, want %q %v", c.from, c.to, c.limit, got, complete, err, c.want, c.complete)
		}
	}
	if _, _, err := r.ChangedPaths(ctx, "--output=/tmp/x", second, 10); err == nil {
		t.Error("an option was accepted as a commit")
	}
}

// TestGrep: matching lines with their paths and numbers, text or expression, in either case, below a path, with
// limits; patterns stay patterns, also when they look like options.
func TestGrep(t *testing.T) {
	g, err := New(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	r, err := g.Init(ctx, filepath.Join(t.TempDir(), "r.git"), "main")
	if err != nil {
		t.Fatal(err)
	}
	tree := ""
	for path, content := range map[string]string{
		"README.md":         "# Payments\nThe payment service.\n",
		"src/pay.go":        "package src\n\n// Pay pays.\nfunc Pay() {}\n// PAY again\n",
		"src/refund.go":     "package src\n\nfunc Refund() { Pay() }\n",
		"docs/--version.md": "-e is an option\n",
		"bin/blob":          "pay\x00\x01\x02",
		"long.txt":          "Größe " + strings.Repeat("ä", 300) + "\n",
		"notes.txt":         "Kein ÄRGER\nid 42\n",
	} {
		blob, err := r.HashObject(ctx, []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		if tree, err = r.TreeWith(ctx, tree, path, blob, "100644"); err != nil {
			t.Fatal(err)
		}
	}
	commit, err := r.CommitTree(ctx, tree, nil, "files\n", Signature{Name: "A", Email: "a@example.com"}, Signature{Name: "A", Email: "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	find := func(pattern string, o GrepOptions) (string, bool) {
		t.Helper()
		ms, complete, err := r.Grep(ctx, commit, pattern, o)
		if err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
		var out []string
		for _, m := range ms {
			out = append(out, fmt.Sprintf("%s:%d", m.Path, m.Line))
		}
		slices.Sort(out)
		return strings.Join(out, " "), complete
	}
	if got, complete := find("pay", GrepOptions{}); got != "README.md:1 README.md:2 src/pay.go:3 src/pay.go:4 src/pay.go:5 src/refund.go:3" || !complete {
		t.Errorf("text = %s %v", got, complete)
	}
	if got, _ := find("Pay", GrepOptions{CaseSensitive: true}); got != "README.md:1 src/pay.go:3 src/pay.go:4 src/refund.go:3" {
		t.Errorf("case = %s", got)
	}
	if got, _ := find("func [A-Z][a-z]+\\(", GrepOptions{Regex: true, CaseSensitive: true}); got != "src/pay.go:4 src/refund.go:3" {
		t.Errorf("expression = %s", got)
	}
	if got, _ := find("pay", GrepOptions{Path: "src"}); got != "src/pay.go:3 src/pay.go:4 src/pay.go:5 src/refund.go:3" {
		t.Errorf("below src = %s", got)
	}
	if got, _ := find("pay", GrepOptions{MaxPerFile: 1}); got != "README.md:1 src/pay.go:3 src/refund.go:3" {
		t.Errorf("one per file = %s", got)
	}
	if got, complete := find("pay", GrepOptions{MaxMatches: 2}); strings.Count(got, ":") != 2 || complete {
		t.Errorf("limit = %s %v", got, complete)
	}
	if got, _ := find("-e", GrepOptions{}); got != "docs/--version.md:1" {
		t.Errorf("a pattern like an option = %s", got)
	}
	if got, complete := find("nowhere to be found", GrepOptions{}); got != "" || !complete {
		t.Errorf("nothing = %q %v", got, complete)
	}
	if _, _, err := r.Grep(ctx, "--output=/tmp/x", "pay", GrepOptions{}); err == nil {
		t.Error("an option was accepted as a commit")
	}
	// Case is ignored beyond ASCII, and expressions are Perl-compatible (\d is a digit, not a d).
	if got, _ := find("ärger", GrepOptions{}); got != "notes.txt:1" {
		t.Errorf("umlauts = %s", got)
	}
	if got, _ := find(`id \d+`, GrepOptions{Regex: true}); got != "notes.txt:2" {
		t.Errorf("\\d = %s", got)
	}
	// Where git was built without Perl-compatible expressions, it searches with POSIX extended ones.
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfor a; do [ \"$a\" = -P ] && { echo 'fatal: cannot use Perl-compatible regexes when not compiled with USE_LIBPCRE' >&2; exit 128; }; done\nexec " +
		g.Bin + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := &Git{Bin: wrapper, HooksDir: g.HooksDir, Timeout: g.Timeout, Version: g.Version}
	ms, _, err := plain.Open(r.Path).Grep(ctx, commit, `func [A-Z][a-z]+\(`, GrepOptions{Regex: true, CaseSensitive: true})
	if err != nil || len(ms) != 2 || !plain.noPCRE.Load() {
		t.Errorf("without PCRE = %v %v %v", ms, err, plain.noPCRE.Load())
	}
	// A long line is cut between characters, not inside one.
	ms, _, err = r.Grep(ctx, commit, "größe", GrepOptions{})
	if err != nil || len(ms) != 1 {
		t.Fatalf("long line = %v %v", ms, err)
	}
	if text := ms[0].Text; len(text) > maxGrepLine || len(text) < maxGrepLine-1 || !utf8.ValidString(text) {
		t.Errorf("long line cut to %d bytes, valid %v", len(text), utf8.ValidString(text))
	}
}

// TestDistances: the commits of branches ahead of and behind a base, in one for-each-ref with Git 2.41 and later
// and with rev-list for each branch before; both count the same.
func TestDistances(t *testing.T) {
	g, err := New(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	r, err := g.Init(ctx, filepath.Join(t.TempDir(), "r.git"), "main")
	if err != nil {
		t.Fatal(err)
	}
	sig := Signature{Name: "A", Email: "a@example.com"}
	commit := func(message string, parents ...string) string {
		t.Helper()
		sha, err := r.CommitTree(ctx, EmptyTree, parents, message+"\n", sig, sig)
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	c1 := commit("one")
	c2 := commit("two", c1)
	c3 := commit("three", c2)
	f1 := commit("feature one", c1)
	f2 := commit("feature two", f1)
	o1 := commit("unrelated")
	for name, sha := range map[string]string{"main": c3, "feature/x": f2, "merged": c2, "orphan": o1, "other": f1} {
		if err := r.UpdateRef(ctx, "refs/heads/"+name, sha, "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	refs := []Ref{{Name: "refs/heads/feature/x", Commit: f2}, {Name: "refs/heads/merged", Commit: c2}, {Name: "refs/heads/orphan", Commit: o1}}
	want := map[string]Distance{"refs/heads/feature/x": {2, 2}, "refs/heads/merged": {0, 1}, "refs/heads/orphan": {1, 3}}

	// A wrapper records the git commands, to count them.
	log := filepath.Join(t.TempDir(), "commands")
	wrapper := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\nexec "+g.Bin+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	commands := func() []string {
		t.Helper()
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(log)
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	for _, version := range []string{"2.41.0", "2.39.1"} {
		if version == "2.41.0" && !atLeast(g.Version, 2, 41, 0) {
			t.Logf("git %s counts with rev-list only", g.Version)
			continue
		}
		counted := &Git{Bin: wrapper, HooksDir: g.HooksDir, Timeout: g.Timeout, Version: version}
		got, err := counted.Open(r.Path).Distances(ctx, c3, refs)
		if err != nil || !maps.Equal(got, want) {
			t.Errorf("git %s: %v %v, want %v", version, got, err, want)
		}
		ran := commands()
		if version == "2.41.0" && (len(ran) != 1 || !strings.Contains(ran[0], " for-each-ref ")) {
			t.Errorf("git %s ran %q, want one for-each-ref", version, ran)
		}
		if version == "2.39.1" && (len(ran) != len(refs) || !strings.Contains(ran[0], " rev-list ")) {
			t.Errorf("git %s ran %q, want rev-list for each branch", version, ran)
		}
	}
	if atLeast(g.Version, 2, 41, 0) {
		// A branch deleted meanwhile is left out, and only the branches asked for are counted.
		got, err := r.Distances(ctx, c3, append(refs[:1:1], Ref{Name: "refs/heads/gone", Commit: f2}))
		if err != nil || len(got) != 1 || got["refs/heads/feature/x"] != (Distance{2, 2}) {
			t.Errorf("with a deleted branch = %v %v", got, err)
		}
	}
	if got, err := r.Distances(ctx, c3, nil); err != nil || len(got) != 0 {
		t.Errorf("no branches = %v %v", got, err)
	}
	// The base is a commit id, never a revision that could change the format.
	if _, err := r.Distances(ctx, "main)%(refname", refs); err == nil {
		t.Error("a revision was accepted as the base")
	}
}
