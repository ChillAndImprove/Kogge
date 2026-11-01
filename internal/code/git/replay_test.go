// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package git

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// history builds commits in a bare repository for the tests.
type history struct {
	t   *testing.T
	ctx context.Context
	r   *Repo
}

func newHistory(t *testing.T) *history {
	t.Helper()
	g, err := New(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	r, err := g.Init(ctx, filepath.Join(t.TempDir(), "r.git"), "main")
	if err != nil {
		t.Fatal(err)
	}
	return &history{t: t, ctx: ctx, r: r}
}

// commit writes a commit on parent ("" for a root commit) that sets files ("" removes one).
func (h *history) commit(parent, message string, author Signature, files map[string]string) string {
	h.t.Helper()
	tree := ""
	var err error
	if parent != "" {
		if tree, err = h.r.TreeOf(h.ctx, parent); err != nil {
			h.t.Fatal(err)
		}
	}
	for path, content := range files {
		blob := ""
		if content != "" {
			if blob, err = h.r.HashObject(h.ctx, []byte(content)); err != nil {
				h.t.Fatal(err)
			}
		}
		if tree, err = h.r.TreeWith(h.ctx, tree, path, blob, "100644"); err != nil {
			h.t.Fatal(err)
		}
	}
	if tree == "" {
		tree = EmptyTree
	}
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	sha, err := h.r.CommitTree(h.ctx, tree, parents, message, author, author)
	if err != nil {
		h.t.Fatal(err)
	}
	return sha
}

// raw writes a commit object as it is given (headers and message), past commit-tree.
func (h *history) raw(object string) string {
	h.t.Helper()
	out, err := h.r.run(h.ctx, &Opts{Stdin: strings.NewReader(object)}, "hash-object", "-t", "commit", "-w", "--stdin", "--literally")
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (h *history) file(commit, path string) string {
	h.t.Helper()
	info, err := h.r.Stat(h.ctx, commit, path)
	if errors.Is(err, ErrNotFound) {
		return ""
	}
	if err != nil {
		h.t.Fatal(err)
	}
	data, err := h.r.ReadBlob(h.ctx, info.SHA, 1<<20)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *history) rebase(base, tip string, committer Signature) (string, int, error) {
	h.t.Helper()
	shas, complete, err := h.r.RebaseCommits(h.ctx, base, tip, 100)
	if err != nil || !complete {
		h.t.Fatalf("rebase commits: %v %v", err, complete)
	}
	commits, err := h.r.ReadCommits(h.ctx, shas)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.r.Replay(h.ctx, base, commits, committer)
}

// TestReplay: a rebase without a working tree applies the commits one by one onto the new base, keeping authors
// (with their dates and zones), messages and encodings, with the merging user as committer; changes the base has
// already are left out, commits empty from the start stay, and a conflict names the commit and its files.
func TestReplay(t *testing.T) {
	h := newHistory(t)
	ctx := h.ctx
	anna := Signature{Name: "Anna Muster", Email: "anna@example.com", When: time.Unix(1_700_000_000, 0)}
	bob := Signature{Name: "Bob", Email: "bob@example.com", When: time.Unix(1_700_000_100, 0)}
	merger := Signature{Name: "Merle Merger", Email: "merle@example.com", When: time.Unix(1_800_000_000, 0)}

	start := h.commit("", "Start\n", anna, map[string]string{"a.txt": "one\ntwo\nthree\n", "b.txt": "b\n"})
	target := h.commit(start, "Target work\n", bob, map[string]string{"c.txt": "c\n", "b.txt": "b\nmore\n"})
	// The branch: a change of a.txt by Anna written in another zone, a commit whose change the target has already,
	// a commit empty from the start, and one by Bob.
	first := h.raw("tree " + func() string {
		tree, _ := h.r.TreeWith(ctx, mustTree(t, h, start), "a.txt", mustBlob(t, h, "ONE\ntwo\nthree\n"), "100644")
		return tree
	}() + "\nparent " + start + "\nauthor Anna Muster <anna@example.com> 1700000050 +0230\ncommitter Anna Muster <anna@example.com> 1700000050 +0230\n" +
		"\nUppercase one\n\nWith a body.\n  Indented, and trailing spaces  \n")
	same := h.commit(first, "Same as the target\n", bob, map[string]string{"c.txt": "c\n"})
	empty := h.raw("tree " + mustTree(t, h, same) + "\nparent " + same + "\nauthor Bob <bob@example.com> 1700000060 +0000\ncommitter Bob <bob@example.com> 1700000060 +0000\n\nNothing\n")
	last := h.commit(empty, "Three\n", bob, map[string]string{"a.txt": "ONE\ntwo\nTHREE\n"})

	shas, complete, err := h.r.RebaseCommits(ctx, target, last, 100)
	if err != nil || !complete || !slices.Equal(shas, []string{first, same, empty, last}) {
		t.Fatalf("rebase commits = %v %v %v", shas, complete, err)
	}
	if _, complete, _ := h.r.RebaseCommits(ctx, target, last, 3); complete {
		t.Error("four commits fit a limit of three")
	}
	tip, made, err := h.rebase(target, last, merger)
	if err != nil {
		t.Fatal(err)
	}
	if made != 3 {
		t.Errorf("%d commits made, want 3 (the one the target has is left out)", made)
	}
	log, err := h.r.Log(ctx, LogOptions{Rev: tip, Exclude: []string{target}, Reverse: true})
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, c := range log {
		subjects = append(subjects, c.Subject())
		if c.Committer.Name != merger.Name || c.Committer.Email != merger.Email || !c.Committer.When.Equal(merger.When) {
			t.Errorf("%s: committer %+v", c.Subject(), c.Committer)
		}
		if len(c.Parents) != 1 {
			t.Errorf("%s: parents %v", c.Subject(), c.Parents)
		}
	}
	if !slices.Equal(subjects, []string{"Uppercase one", "Nothing", "Three"}) {
		t.Errorf("rebased commits = %v", subjects)
	}
	if log[0].Parents[0] != target {
		t.Errorf("the first commit is on %s, want the target %s", log[0].Parents[0], target)
	}
	rebased, err := h.r.ReadCommits(ctx, []string{log[0].SHA, log[1].SHA})
	if err != nil {
		t.Fatal(err)
	}
	if rebased[0].Author != "Anna Muster <anna@example.com> 1700000050 +0230" {
		t.Errorf("author line = %q", rebased[0].Author)
	}
	if rebased[0].Message != "Uppercase one\n\nWith a body.\n  Indented, and trailing spaces  \n" {
		t.Errorf("message = %q", rebased[0].Message)
	}
	if rebased[1].Tree != mustTree(t, h, log[0].SHA) {
		t.Errorf("the empty commit changed the tree")
	}
	for path, want := range map[string]string{"a.txt": "ONE\ntwo\nTHREE\n", "b.txt": "b\nmore\n", "c.txt": "c\n"} {
		if got := h.file(tip, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}

	// A conflict: the target changed the line the branch changes.
	other := h.commit(target, "Lower\n", bob, map[string]string{"a.txt": "uno\ntwo\nthree\n"})
	_, _, err = h.rebase(other, last, merger)
	var conflict *ReplayConflict
	if !errors.As(err, &conflict) || conflict.Commit.SHA != first || !slices.Equal(conflict.Paths, []string{"a.txt"}) ||
		conflict.Commit.Subject() != "Uppercase one" {
		t.Fatalf("conflict = %v", err)
	}

	// Nothing to apply: every change is in the base already. (The commit empty from the start would stay.)
	if tip2, made, err := h.rebase(tip, same, merger); err != nil || made != 0 || tip2 != tip {
		t.Errorf("rebase onto its own result = %s %d %v", tip2, made, err)
	}
}

// TestReplayOddCommits: commits without parents (unrelated histories merged into the branch), merges (left out,
// their changes come with their commits), messages in another encoding, and authors without a name or address are
// applied as they are.
func TestReplayOddCommits(t *testing.T) {
	h := newHistory(t)
	ctx := h.ctx
	a := Signature{Name: "A", Email: "a@example.com", When: time.Unix(1_700_000_000, 0)}
	base := h.commit("", "Base\n", a, map[string]string{"base.txt": "base\n"})
	// Another history, merged into the branch.
	lone := h.commit("", "Lone root\n", a, map[string]string{"lone.txt": "lone\n"})
	work := h.commit(base, "Work\n", a, map[string]string{"work.txt": "work\n"})
	merged, err := h.r.CommitTree(ctx, func() string {
		tree, _ := h.r.TreeWith(ctx, mustTree(t, h, work), "lone.txt", mustBlob(t, h, "lone\n"), "100644")
		return tree
	}(), []string{work, lone}, "Merge lone\n", a, a)
	if err != nil {
		t.Fatal(err)
	}
	latin := h.raw("tree " + func() string {
		tree, _ := h.r.TreeWith(ctx, mustTree(t, h, merged), "l.txt", mustBlob(t, h, "l\n"), "100644")
		return tree
	}() + "\nparent " + merged + "\nauthor <> 1700000010 -0500\ncommitter C <c@example.com> 1700000010 -0500\nencoding ISO-8859-1\n\nGr\xfc\xdfe\n")

	shas, _, err := h.r.RebaseCommits(ctx, base, latin, 100)
	if err != nil || !slices.Equal(shas, []string{lone, work, latin}) && !slices.Equal(shas, []string{work, lone, latin}) {
		t.Fatalf("rebase commits = %v %v", shas, err)
	}
	newBase := h.commit(base, "Moved on\n", a, map[string]string{"new.txt": "new\n"})
	tip, made, err := h.rebase(newBase, latin, a)
	if err != nil || made != 3 {
		t.Fatalf("replay = %d %v", made, err)
	}
	for _, path := range []string{"base.txt", "lone.txt", "work.txt", "l.txt", "new.txt"} {
		if h.file(tip, path) == "" {
			t.Errorf("%s is missing", path)
		}
	}
	got, err := h.r.ReadCommits(ctx, []string{tip})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Encoding != "ISO-8859-1" || got[0].Message != "Gr\xfc\xdfe\n" || got[0].Author != "<> 1700000010 -0500" {
		t.Errorf("latin commit = %+v", got[0])
	}
}

func mustTree(t *testing.T, h *history, commit string) string {
	t.Helper()
	tree, err := h.r.TreeOf(h.ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func mustBlob(t *testing.T, h *history, content string) string {
	t.Helper()
	blob, err := h.r.HashObject(h.ctx, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return blob
}
