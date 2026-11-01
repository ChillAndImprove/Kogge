// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Rebasing without a working tree (pull requests merged with the strategies rebase and rebase-merge): the commits
// of a branch are applied one after another to another commit, each as a three-way merge of the commit with what was
// built so far, whose base is the commit's parent — what git cherry-pick does. git merge-tree --write-tree (Git
// 2.38) computes such merges but chooses the merge base itself; naming it (--merge-base) needs Git 2.40, and Kogge
// runs with 2.39.1 (Debian 12). So each step merges with a helper commit: the tree built so far with the commit's
// parent as its only parent, whose merge base with the commit is that parent. Like the trees of the merge checks,
// helper commits and the commits of a rebase that does not go through are referenced by nothing; garbage collection
// removes them.

// RawCommit is a commit as it is stored.
type RawCommit struct {
	SHA      string
	Tree     string
	Parents  []string
	Author   string // the author line: name <email> seconds zone
	Encoding string // the encoding of the message where it is not UTF-8
	Message  string // the message as it is, with its line endings

	object []byte // the commit object as read
}

// Subject returns the first line of the message.
func (c *RawCommit) Subject() string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return strings.TrimSpace(s)
}

// helper signs the helper commits of a rebase, which nobody sees.
var helper = Signature{Name: "Kogge", Email: "kogge@localhost"}

// ReadCommits reads commit objects, all in one git process.
func (r *Repo) ReadCommits(ctx context.Context, shas []string) ([]RawCommit, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	for _, s := range shas {
		if !objectID(s) {
			return nil, ErrNotFound
		}
	}
	out, err := r.run(ctx, &Opts{Stdin: strings.NewReader(strings.Join(shas, "\n") + "\n")}, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	rd := bufio.NewReader(bytes.NewReader(out))
	commits := make([]RawCommit, 0, len(shas))
	for range shas {
		header, err := rd.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("git cat-file --batch: %w", err)
		}
		f := strings.Fields(header)
		if len(f) != 3 || f[1] != "commit" {
			return nil, fmt.Errorf("git cat-file --batch: %w: %s", ErrNotFound, strings.TrimSpace(header))
		}
		size, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, fmt.Errorf("git cat-file --batch: %q", header)
		}
		body := make([]byte, size+1) // the object and the newline after it
		if _, err := io.ReadFull(rd, body); err != nil {
			return nil, fmt.Errorf("git cat-file --batch: %w", err)
		}
		c := parseCommit(body[:size])
		c.SHA, c.object = f[0], body[:size]
		commits = append(commits, c)
	}
	return commits, nil
}

// parseCommit reads the headers and the message of a commit object.
func parseCommit(data []byte) RawCommit {
	var c RawCommit
	headers, message, _ := bytes.Cut(data, []byte("\n\n"))
	c.Message = string(message)
	for _, line := range strings.Split(string(headers), "\n") {
		if strings.HasPrefix(line, " ") {
			continue // the continuation of a header of several lines (signatures)
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "tree":
			c.Tree = value
		case "parent":
			c.Parents = append(c.Parents, value)
		case "author":
			c.Author = value
		case "encoding":
			c.Encoding = value
		}
	}
	return c
}

// RebaseCommits lists the commits a rebase of tip onto base applies, oldest first: those of tip that base does not
// have, without merge commits (as git rebase: their changes come with the commits they merged). It lists at most
// limit; complete is false if there are more.
func (r *Repo) RebaseCommits(ctx context.Context, base, tip string, limit int) (shas []string, complete bool, err error) {
	if !objectID(base) || !objectID(tip) {
		return nil, false, ErrNotFound
	}
	// --reverse would apply after --max-count and so keep the newest commits: the order is turned here.
	out, err := r.run(ctx, nil, "rev-list", "--topo-order", "--no-merges", "--max-count="+strconv.Itoa(limit+1), tip, "^"+base, "--")
	if err != nil {
		return nil, false, err
	}
	shas = strings.Fields(string(out))
	if len(shas) > limit {
		return nil, false, nil
	}
	slices.Reverse(shas)
	return shas, true, nil
}

// ReplayConflict is the commit of a rebase that does not apply, with the files it conflicts in.
type ReplayConflict struct {
	Commit RawCommit
	Paths  []string
}

func (e *ReplayConflict) Error() string {
	return fmt.Sprintf("git: commit %s conflicts in %s", e.Commit.SHA, strings.Join(e.Paths, ", "))
}

// Replay applies commits (oldest first, as RebaseCommits lists them) to base and returns the last new commit (base,
// if none was made). The new commits keep their authors and messages; committer is their committer. A commit whose
// changes base has already is left out, as git rebase does (it would change nothing); one that changed nothing in
// the first place stays. A commit that conflicts ends the rebase with a *ReplayConflict, and no ref changes.
func (r *Repo) Replay(ctx context.Context, base string, commits []RawCommit, committer Signature) (tip string, made int, err error) {
	trees := map[string]string{}
	for _, c := range commits {
		trees[c.SHA] = c.Tree
	}
	// The trees of parents outside the list: the commit the branch started from, merge commits left out.
	var others []string
	for _, c := range commits {
		for _, p := range c.Parents {
			if _, ok := trees[p]; !ok && !slices.Contains(others, p) {
				others = append(others, p)
			}
		}
	}
	more, err := r.ReadCommits(ctx, others)
	if err != nil {
		return "", 0, err
	}
	for _, c := range more {
		trees[c.SHA] = c.Tree
	}
	tip = base
	tipTree, err := r.TreeOf(ctx, base)
	if err != nil {
		return "", 0, err
	}
	help := helper
	help.When = committer.When
	for _, c := range commits {
		if len(c.Parents) > 1 {
			return "", 0, fmt.Errorf("git: replay of the merge commit %s", c.SHA)
		}
		parentTree := EmptyTree
		if len(c.Parents) == 1 {
			parentTree = trees[c.Parents[0]]
		}
		tree := tipTree
		if c.Tree != parentTree {
			ours, theirs := "", c.SHA
			if len(c.Parents) == 1 {
				// The tree so far on the commit's parent: their merge base is the parent.
				if ours, err = r.CommitTree(ctx, tipTree, c.Parents, "rebase\n", help, help); err != nil {
					return "", 0, err
				}
			} else {
				// A commit without parents: both sides on a commit of the empty tree.
				root, err := r.CommitTree(ctx, EmptyTree, nil, "rebase\n", help, help)
				if err != nil {
					return "", 0, err
				}
				if ours, err = r.CommitTree(ctx, tipTree, []string{root}, "rebase\n", help, help); err != nil {
					return "", 0, err
				}
				if theirs, err = r.CommitTree(ctx, c.Tree, []string{root}, "rebase\n", help, help); err != nil {
					return "", 0, err
				}
			}
			merged, conflicts, err := r.MergeTree(ctx, ours, theirs)
			if err != nil {
				return "", 0, err
			}
			if len(conflicts) > 0 {
				return "", 0, &ReplayConflict{Commit: c, Paths: conflicts}
			}
			if merged == tipTree {
				continue // its changes are there already
			}
			tree = merged
		}
		if tip, err = r.commitAs(ctx, tree, tip, c, committer); err != nil {
			return "", 0, err
		}
		tipTree = tree
		made++
	}
	return tip, made, nil
}

// commitAs writes the commit of a tree on parent with the author line, the encoding and the message of c, as they
// are: git commit-tree would write the author anew (refusing those of old histories without a name, for one).
func (r *Repo) commitAs(ctx context.Context, tree, parent string, c RawCommit, committer Signature) (string, error) {
	var b strings.Builder
	b.WriteString("tree " + tree + "\nparent " + parent + "\nauthor " + c.Author + "\ncommitter " + ident(committer) + "\n")
	if c.Encoding != "" {
		b.WriteString("encoding " + c.Encoding + "\n")
	}
	b.WriteString("\n" + c.Message)
	object := b.String()
	out, err := r.run(ctx, &Opts{Stdin: strings.NewReader(object)}, "hash-object", "-t", "commit", "-w", "--stdin")
	if fsckFailed(err) && len(c.object) > 0 {
		// Defects that old tools wrote and that Kogge accepts in pushes (an author without a name, a date out of
		// range): if the commit itself has them, it is in the repository as it is, and so goes its copy.
		if _, check := r.run(ctx, &Opts{Stdin: bytes.NewReader(c.object)}, "hash-object", "-t", "commit", "--stdin"); fsckFailed(check) {
			out, err = r.run(ctx, &Opts{Stdin: strings.NewReader(object)}, "hash-object", "-t", "commit", "-w", "--stdin", "--literally")
		}
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// fsckFailed reports whether git refused to write an object because it is malformed.
func fsckFailed(err error) bool {
	var gitErr *Error
	return errors.As(err, &gitErr) && strings.Contains(gitErr.Stderr, "fails fsck")
}

// ident is a committer line: the name and address without the characters that would end them, and the time in UTC.
func ident(s Signature) string {
	clean := func(v string) string {
		return strings.TrimSpace(strings.Map(func(r rune) rune {
			if r == '<' || r == '>' || r == '\n' || r == '\r' || r == 0 {
				return -1
			}
			return r
		}, v))
	}
	name := clean(s.Name)
	if name == "" {
		name = "Kogge"
	}
	return name + " <" + clean(s.Email) + "> " + gitDate(s.When)
}
