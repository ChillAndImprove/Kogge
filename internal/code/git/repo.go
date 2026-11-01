// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNotFound is returned for refs, revisions and paths that do not exist.
var ErrNotFound = errors.New("git: not found")

// Repo is a bare repository.
type Repo struct {
	g    *Git
	Path string
}

// Open returns the repository at path (which must exist).
func (g *Git) Open(path string) *Repo { return &Repo{g: g, Path: path} }

func (r *Repo) run(ctx context.Context, o *Opts, args ...string) ([]byte, error) {
	return r.g.Run(ctx, r.Path, o, args...)
}

// Init creates a bare repository with the default branch.
func (g *Git) Init(ctx context.Context, path, defaultBranch string) (*Repo, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if _, err := g.Run(ctx, "", nil, "init", "--bare", "--quiet", "--initial-branch="+defaultBranch, path); err != nil {
		return nil, err
	}
	r := g.Open(path)
	// Settings of server repositories: no reflogs to grow forever, but keep enough history for safety.
	for _, kv := range [][2]string{{"core.logAllRefUpdates", "false"}, {"receive.denyNonFastForwards", "false"},
		{"gc.auto", "0"}, {"repack.writeBitmaps", "true"}, {"uploadpack.allowFilter", "true"}} {
		if _, err := r.run(ctx, nil, "config", kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// SetHead points HEAD (the default branch) to a branch.
func (r *Repo) SetHead(ctx context.Context, branch string) error {
	_, err := r.run(ctx, nil, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	return err
}

// ValidBranchName checks a branch name with git's rules.
func (g *Git) ValidBranchName(ctx context.Context, name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || len(name) > 250 {
		return false
	}
	_, err := g.Run(ctx, "", nil, "check-ref-format", "--branch", name)
	return err == nil && !strings.Contains(name, "..")
}

// ValidRefName checks a full ref name (refs/…).
func (g *Git) ValidRefName(ctx context.Context, name string) bool {
	if !strings.HasPrefix(name, "refs/") || len(name) > 255 {
		return false
	}
	_, err := g.Run(ctx, "", nil, "check-ref-format", name)
	return err == nil
}

// Ref is a branch or tag.
type Ref struct {
	Name          string    // full name, e.g. refs/heads/main
	SHA           string    // object the ref points to
	Type          string    // commit or tag
	Commit        string    // the commit (peeled for annotated tags)
	CommitterDate time.Time // of the commit
	Subject       string
	AuthorName    string
	AuthorEmail   string
	TagMessage    string // annotated tags
}

// ShortName returns the name without refs/heads/ or refs/tags/.
func (r Ref) ShortName() string {
	if s, ok := strings.CutPrefix(r.Name, "refs/heads/"); ok {
		return s
	}
	return strings.TrimPrefix(r.Name, "refs/tags/")
}

// Refs lists refs below the prefixes (e.g. refs/heads/), most recently committed first.
func (r *Repo) Refs(ctx context.Context, prefixes ...string) ([]Ref, error) {
	args := []string{"for-each-ref", "--sort=-committerdate", "--sort=-*committerdate",
		"--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(committerdate:unix)%00%(*committerdate:unix)%00" +
			"%(subject)%00%(*subject)%00%(authorname)%00%(*authorname)%00%(authoremail)%00%(*authoremail)%00%(contents:subject)%1e"}
	args = append(args, prefixes...)
	out, err := r.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	return parseRefs(out), nil
}

func parseRefs(out []byte) []Ref {
	var refs []Ref
	for _, rec := range bytes.Split(out, []byte{0x1e}) {
		rec = bytes.TrimLeft(rec, "\n")
		if len(rec) == 0 {
			continue
		}
		f := strings.Split(string(rec), "\x00")
		if len(f) < 13 {
			continue
		}
		ref := Ref{Name: f[0], SHA: f[1], Type: f[2], Commit: f[1], Subject: f[6], AuthorName: f[8], AuthorEmail: trimEmail(f[10])}
		date := f[4]
		if f[2] == "tag" {
			ref.Commit, date, ref.Subject, ref.AuthorName, ref.AuthorEmail = f[3], f[5], f[7], f[9], trimEmail(f[11])
			ref.TagMessage = f[12]
		}
		if sec, err := strconv.ParseInt(date, 10, 64); err == nil {
			ref.CommitterDate = time.Unix(sec, 0).UTC()
		}
		refs = append(refs, ref)
	}
	return refs
}

func trimEmail(s string) string { return strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">") }

// Resolve returns the commit a revision (branch, tag, sha, HEAD) names.
func (r *Repo) Resolve(ctx context.Context, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", ErrNotFound
	}
	out, err := r.run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		if ExitCode(err) == 1 {
			return "", ErrNotFound
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveBranch returns the tip of a branch.
func (r *Repo) ResolveBranch(ctx context.Context, branch string) (string, error) {
	return r.Resolve(ctx, "refs/heads/"+branch)
}

// Empty reports whether the repository has no branches.
func (r *Repo) Empty(ctx context.Context) (bool, error) {
	out, err := r.run(ctx, nil, "for-each-ref", "--count=1", "--format=%(refname)", "refs/heads/")
	return len(bytes.TrimSpace(out)) == 0, err
}

// TreeEntry is a file, directory or submodule in a tree.
type TreeEntry struct {
	Name string
	Path string
	Type string // blob, tree or commit (submodule)
	Mode string
	SHA  string
	Size int64
}

// Tree lists a directory at a commit; path "" is the root.
func (r *Repo) Tree(ctx context.Context, commit, path string) ([]TreeEntry, error) {
	path = strings.Trim(path, "/")
	spec := commit + "^{tree}"
	if path != "" {
		spec = commit + ":" + path
	}
	if t, err := r.ObjectType(ctx, spec); err != nil {
		return nil, err
	} else if t != "tree" {
		return nil, ErrNotFound
	}
	out, err := r.run(ctx, nil, "ls-tree", "-z", "-l", "--end-of-options", spec)
	if err != nil {
		return nil, err
	}
	entries := parseTree(out, path)
	return entries, nil
}

func parseTree(out []byte, dir string) []TreeEntry {
	var entries []TreeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			continue
		}
		f := strings.Fields(string(meta))
		if len(f) < 4 {
			continue
		}
		e := TreeEntry{Mode: f[0], Type: f[1], SHA: f[2], Name: string(name)}
		e.Size, _ = strconv.ParseInt(f[3], 10, 64)
		e.Path = e.Name
		if dir != "" {
			e.Path = dir + "/" + e.Name
		}
		entries = append(entries, e)
	}
	return entries
}

// ObjectType returns the type of an object (blob, tree, commit, tag).
func (r *Repo) ObjectType(ctx context.Context, spec string) (string, error) {
	out, err := r.run(ctx, nil, "cat-file", "-t", "--end-of-options", spec)
	if err != nil {
		if ExitCode(err) == 128 {
			return "", ErrNotFound
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BlobInfo describes a file at a commit.
type BlobInfo struct {
	SHA  string
	Size int64
	Mode string
}

// Stat returns the blob of a path at a commit.
func (r *Repo) Stat(ctx context.Context, commit, path string) (*BlobInfo, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil, ErrNotFound
	}
	out, err := r.run(ctx, nil, "ls-tree", "-z", "-l", "--end-of-options", commit, "--", path)
	if err != nil {
		return nil, err
	}
	entries := parseTree(out, "")
	if len(entries) != 1 || entries[0].Type != "blob" || entries[0].Name != path {
		return nil, ErrNotFound
	}
	return &BlobInfo{SHA: entries[0].SHA, Size: entries[0].Size, Mode: entries[0].Mode}, nil
}

// ReadBlob returns up to limit bytes of a blob.
func (r *Repo) ReadBlob(ctx context.Context, sha string, limit int64) ([]byte, error) {
	out, err := r.run(ctx, &Opts{Limit: limit}, "cat-file", "blob", sha)
	if errors.Is(err, ErrTooLarge) {
		return out, nil
	}
	return out, err
}

// StreamBlob writes a blob to w.
func (r *Repo) StreamBlob(ctx context.Context, sha string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := r.g.Command(ctx, r.Path, nil, "cat-file", "blob", sha)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{b: &stderr, n: 4096}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git cat-file: %w: %s", err, stderr.String())
	}
	return nil
}

// Archive writes an archive (zip or tar.gz) of a commit to w.
func (r *Repo) Archive(ctx context.Context, commit, format, prefix string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := r.g.Command(ctx, r.Path, nil, "archive", "--format="+format, "--prefix="+prefix+"/", "--end-of-options", commit)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{b: &stderr, n: 4096}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive: %w: %s", err, stderr.String())
	}
	return nil
}

// Signature is an author or committer.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// Commit is a commit with its message.
type Commit struct {
	SHA       string
	Parents   []string
	Author    Signature
	Committer Signature
	Message   string
}

// Subject returns the first line of the message.
func (c *Commit) Subject() string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return s
}

// LogFormat is the git log format ParseLog reads.
const LogFormat = "--format=%x1e%H%x00%P%x00%an%x00%ae%x00%at%x00%cn%x00%ce%x00%ct%x00%B"

// LogOptions select commits.
type LogOptions struct {
	Rev     string // revision or range (a..b)
	Path    string
	Skip    int
	Limit   int
	Exclude []string // revisions whose history is left out (e.g. other branches)
	Author  string
	Since   time.Time
	Until   time.Time
	Follow  bool // follow renames of a single file
	Reverse bool
	NoMerge bool
}

// Log lists commits, newest first.
func (r *Repo) Log(ctx context.Context, o LogOptions) ([]Commit, error) {
	args := []string{"log", LogFormat}
	if o.Limit > 0 {
		args = append(args, "--max-count="+strconv.Itoa(o.Limit))
	}
	if o.Skip > 0 {
		args = append(args, "--skip="+strconv.Itoa(o.Skip))
	}
	if o.Author != "" {
		args = append(args, "--author="+o.Author, "--regexp-ignore-case", "--fixed-strings")
	}
	if !o.Since.IsZero() {
		args = append(args, "--since="+strconv.FormatInt(o.Since.Unix(), 10))
	}
	if !o.Until.IsZero() {
		args = append(args, "--until="+strconv.FormatInt(o.Until.Unix(), 10))
	}
	if o.Follow && o.Path != "" {
		args = append(args, "--follow")
	}
	if o.Reverse {
		args = append(args, "--reverse")
	}
	if o.NoMerge {
		args = append(args, "--no-merges")
	}
	if o.Rev == "" || strings.HasPrefix(o.Rev, "-") {
		return nil, ErrNotFound
	}
	args = append(args, "--end-of-options", o.Rev)
	for _, x := range o.Exclude {
		if x != "" && !strings.HasPrefix(x, "-") {
			args = append(args, "^"+x)
		}
	}
	args = append(args, "--")
	if o.Path != "" {
		args = append(args, o.Path)
	}
	out, err := r.run(ctx, nil, args...)
	if err != nil {
		if ExitCode(err) == 128 {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return ParseLog(out), nil
}

// ParseLog parses the output of git log with LogFormat.
func ParseLog(out []byte) []Commit {
	var commits []Commit
	for _, rec := range bytes.Split(out, []byte{0x1e}) {
		if len(bytes.TrimSpace(rec)) == 0 {
			continue
		}
		f := strings.SplitN(string(rec), "\x00", 9)
		if len(f) < 9 {
			continue
		}
		c := Commit{SHA: strings.TrimSpace(f[0]), Author: Signature{Name: f[2], Email: f[3]}, Committer: Signature{Name: f[5], Email: f[6]},
			Message: strings.TrimRight(f[8], "\n")}
		if p := strings.TrimSpace(f[1]); p != "" {
			c.Parents = strings.Fields(p)
		}
		if sec, err := strconv.ParseInt(f[4], 10, 64); err == nil {
			c.Author.When = time.Unix(sec, 0).UTC()
		}
		if sec, err := strconv.ParseInt(f[7], 10, 64); err == nil {
			c.Committer.When = time.Unix(sec, 0).UTC()
		}
		commits = append(commits, c)
	}
	return commits
}

// Commit returns one commit.
func (r *Repo) Commit(ctx context.Context, rev string) (*Commit, error) {
	list, err := r.Log(ctx, LogOptions{Rev: rev, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// CountCommits counts the commits of a revision (range).
func (r *Repo) CountCommits(ctx context.Context, rev string) (int, error) {
	out, err := r.run(ctx, nil, "rev-list", "--count", "--end-of-options", rev, "--")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// AheadBehind counts the commits of a not in b (ahead) and of b not in a (behind).
func (r *Repo) AheadBehind(ctx context.Context, a, b string) (ahead, behind int, err error) {
	out, err := r.run(ctx, nil, "rev-list", "--left-right", "--count", "--end-of-options", a+"..."+b, "--")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("git rev-list: unexpected output %q", out)
	}
	ahead, _ = strconv.Atoi(f[0])
	behind, _ = strconv.Atoi(f[1])
	return ahead, behind, nil
}

// Distance is how far a ref is from a base commit: the commits only the ref has (ahead) and those only the base
// has (behind).
type Distance struct {
	Ahead, Behind int
}

// Distances counts the commits ahead of and behind base (a commit id) of refs (full names, with their commits), by
// name. Git 2.41 and later count all of them in one walk (the ahead-behind field of for-each-ref), which leaves out
// refs deleted meanwhile; older versions run rev-list for each ref's commit.
func (r *Repo) Distances(ctx context.Context, base string, refs []Ref) (map[string]Distance, error) {
	out := map[string]Distance{}
	if !objectID(base) {
		return nil, ErrNotFound
	}
	if len(refs) == 0 {
		return out, nil
	}
	if !atLeast(r.g.Version, 2, 41, 0) {
		for _, ref := range refs {
			ahead, behind, err := r.AheadBehind(ctx, ref.Commit, base)
			if err != nil {
				return nil, err
			}
			out[ref.Name] = Distance{Ahead: ahead, Behind: behind}
		}
		return out, nil
	}
	// The names are the patterns. Ref names cannot hold the characters of patterns (*, ? and [), and no ref lies
	// below the name of another, so every name matches its own ref only.
	var names []string
	for _, ref := range refs {
		if strings.HasPrefix(ref.Name, "refs/") {
			names = append(names, ref.Name)
		}
	}
	if len(names) == 0 {
		return out, nil
	}
	data, err := r.run(ctx, nil, append([]string{"for-each-ref", "--format=%(refname)%00%(ahead-behind:" + base + ")"}, names...)...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, counts, ok := strings.Cut(line, "\x00")
		f := strings.Fields(counts)
		if !ok || len(f) != 2 {
			continue
		}
		ahead, err1 := strconv.Atoi(f[0])
		behind, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("git for-each-ref: unexpected counts %q", counts)
		}
		out[name] = Distance{Ahead: ahead, Behind: behind}
	}
	return out, nil
}

// MergeBase returns the best common ancestor of two commits ("" if there is none).
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := r.run(ctx, nil, "merge-base", "--end-of-options", a, b)
	if err != nil {
		if ExitCode(err) == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r *Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := r.run(ctx, nil, "merge-base", "--is-ancestor", "--end-of-options", a, b)
	if err == nil {
		return true, nil
	}
	if ExitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// ChangedPaths lists the files that differ between two commits (from may be EmptyTree); a renamed file counts with
// its old and its new path. complete is false when there are more than limit files; the list is then empty.
func (r *Repo) ChangedPaths(ctx context.Context, from, to string, limit int) (paths []string, complete bool, err error) {
	out, err := r.run(ctx, &Opts{Limit: int64(limit) * 256}, "diff-tree", "-r", "-z", "--name-only", "--no-renames",
		"--end-of-options", from, to)
	if errors.Is(err, ErrTooLarge) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	paths = []string{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) > limit {
		return nil, false, nil
	}
	return paths, true, nil
}

// GrepOptions adjust a search of a commit's files.
type GrepOptions struct {
	Regex         bool   // a Perl-compatible regular expression instead of a text
	CaseSensitive bool   // otherwise letters match in either case
	Path          string // only files below this path
	MaxMatches    int    // matches in total; more are not read
	MaxPerFile    int    // matches per file
}

// maxGrepLine is the length in bytes up to which a matching line is returned.
const maxGrepLine = 400

// GrepMatch is a line of a file that matches (its text cut after maxGrepLine bytes).
type GrepMatch struct {
	Path string
	Line int
	Text string
}

// Grep searches the text files of a commit (an object id) and returns the matching lines, at most MaxMatches;
// complete is false when there were more. Expressions are Perl-compatible (as people know them from editors and
// programming languages) or, where git was built without that, POSIX extended. Text is UTF-8, so that ignoring
// case also matches Ä with ä.
func (r *Repo) Grep(ctx context.Context, commit, pattern string, o GrepOptions) (matches []GrepMatch, complete bool, err error) {
	if !objectID(commit) || pattern == "" {
		return nil, false, ErrNotFound
	}
	mode := "-F"
	if o.Regex {
		mode = "-P"
		if r.g.noPCRE.Load() {
			mode = "-E"
		}
	}
	args := []string{"grep", "-n", "-z", "-I", "--full-name", "--no-color", mode}
	if !o.CaseSensitive {
		args = append(args, "-i")
	}
	if o.MaxPerFile > 0 {
		args = append(args, "--max-count="+strconv.Itoa(o.MaxPerFile))
	}
	args = append(args, "-e", pattern, commit, "--")
	if o.Path != "" {
		args = append(args, o.Path)
	}
	out, err := r.run(ctx, &Opts{Limit: 8 << 20, Env: []string{"LC_ALL=C.UTF-8"}}, args...)
	if gitErr := (*Error)(nil); mode == "-P" && errors.As(err, &gitErr) && strings.Contains(gitErr.Stderr, "USE_LIBPCRE") {
		r.g.noPCRE.Store(true)
		return r.Grep(ctx, commit, pattern, o)
	}
	if err != nil && !errors.Is(err, ErrTooLarge) {
		if ExitCode(err) == 1 {
			return []GrepMatch{}, true, nil // nothing found
		}
		return nil, false, err
	}
	complete = err == nil
	matches = []GrepMatch{}
	prefix := commit + ":"
	for _, line := range strings.Split(string(out), "\n") {
		// commit:path NUL line NUL text
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		text := strings.TrimRight(parts[2], "\r")
		if len(text) > maxGrepLine {
			cut := maxGrepLine
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
			text = text[:cut]
		}
		matches = append(matches, GrepMatch{Path: strings.TrimPrefix(parts[0], prefix), Line: n, Text: text})
		if o.MaxMatches > 0 && len(matches) >= o.MaxMatches {
			return matches, false, nil
		}
	}
	return matches, complete, nil
}

// DiffOptions adjust a diff.
type DiffOptions struct {
	IgnoreWhitespace bool
	Context          int
	Paths            []string
	Limit            int64 // bytes of patch output
}

// Diff returns the patch between two commits (from may be EmptyTree).
func (r *Repo) Diff(ctx context.Context, from, to string, o DiffOptions) ([]byte, bool, error) {
	ctxLines := 3
	if o.Context > 0 {
		ctxLines = min(o.Context, 1000)
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--find-renames", "--full-index",
		"-U" + strconv.Itoa(ctxLines)}
	if o.IgnoreWhitespace {
		args = append(args, "--ignore-all-space")
	}
	args = append(args, "--end-of-options", from, to, "--")
	args = append(args, o.Paths...)
	limit := o.Limit
	if limit <= 0 {
		limit = 16 << 20
	}
	out, err := r.run(ctx, &Opts{Limit: limit}, args...)
	if errors.Is(err, ErrTooLarge) {
		return out, true, nil
	}
	return out, false, err
}

// Blame returns, for every line of a file at a commit, the commit that last changed it. The commit is an object id:
// git blame before Git 2.45 does not accept --end-of-options, so nothing else may stand where options could.
func (r *Repo) Blame(ctx context.Context, commit, path string) ([]BlameLine, map[string]*Commit, error) {
	if !objectID(commit) {
		return nil, nil, ErrNotFound
	}
	out, err := r.run(ctx, &Opts{Limit: 32 << 20}, "blame", "--porcelain", commit, "--", path)
	if err != nil {
		if ExitCode(err) == 128 {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	lines, commits := parseBlame(out)
	return lines, commits, nil
}

// objectID reports whether s is a full object id (SHA-1 or SHA-256) in lower-case hexadecimal.
func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// BlameLine is a line of a file with the commit that last changed it.
type BlameLine struct {
	SHA  string
	Line int
	Text string
}

func parseBlame(out []byte) ([]BlameLine, map[string]*Commit) {
	commits := map[string]*Commit{}
	var lines []BlameLine
	var cur *Commit
	var curLine int
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "\t") {
			if cur != nil {
				lines = append(lines, BlameLine{SHA: cur.SHA, Line: curLine, Text: l[1:]})
			}
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 3 && len(f[0]) == 40 || len(f) >= 3 && len(f[0]) == 64 {
			sha := f[0]
			if c, ok := commits[sha]; ok {
				cur = c
			} else {
				cur = &Commit{SHA: sha}
				commits[sha] = cur
			}
			curLine, _ = strconv.Atoi(f[2])
			continue
		}
		if cur == nil {
			continue
		}
		key, val, _ := strings.Cut(l, " ")
		switch key {
		case "author":
			cur.Author.Name = val
		case "author-mail":
			cur.Author.Email = trimEmail(val)
		case "author-time":
			if sec, err := strconv.ParseInt(val, 10, 64); err == nil {
				cur.Author.When = time.Unix(sec, 0).UTC()
			}
		case "committer":
			cur.Committer.Name = val
		case "committer-time":
			if sec, err := strconv.ParseInt(val, 10, 64); err == nil {
				cur.Committer.When = time.Unix(sec, 0).UTC()
			}
		case "summary":
			cur.Message = val
		}
	}
	return lines, commits
}

// MergeTree merges two commits without a working tree. It returns the resulting tree, or the conflicting paths.
func (r *Repo) MergeTree(ctx context.Context, ours, theirs string) (tree string, conflicts []string, err error) {
	out, err := r.run(ctx, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", "--end-of-options", ours, theirs)
	if err != nil && ExitCode(err) != 1 {
		return "", nil, err
	}
	parts := strings.Split(string(out), "\x00")
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("git merge-tree: no output")
	}
	tree = strings.TrimSpace(parts[0])
	if ExitCode(err) == 1 {
		for _, p := range parts[1:] {
			if p != "" {
				conflicts = append(conflicts, p)
			}
		}
		if len(conflicts) == 0 {
			conflicts = []string{"(unknown)"}
		}
	}
	return tree, conflicts, nil
}

// CommitTree writes a commit of a tree with parents and returns its id.
func (r *Repo) CommitTree(ctx context.Context, tree string, parents []string, message string, author, committer Signature) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email, "GIT_AUTHOR_DATE=" + gitDate(author.When),
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email, "GIT_COMMITTER_DATE=" + gitDate(committer.When),
	}
	out, err := r.run(ctx, &Opts{Stdin: strings.NewReader(message), Env: env}, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitDate(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return strconv.FormatInt(t.Unix(), 10) + " +0000"
}

// ErrStale is returned when a ref moved since it was read (compare-and-swap failed).
var ErrStale = errors.New("git: the ref changed")

// UpdateRef moves a ref to newSHA if it still points to oldSHA ("" = must not exist).
func (r *Repo) UpdateRef(ctx context.Context, ref, newSHA, oldSHA, reason string) error {
	old := oldSHA
	if old == "" {
		old = strings.Repeat("0", len(newSHA))
	}
	_, err := r.run(ctx, nil, "update-ref", "-m", reason, "--end-of-options", ref, newSHA, old)
	if err != nil && ExitCode(err) == 128 {
		return ErrStale
	}
	return err
}

// DeleteRef deletes a ref if it still points to oldSHA.
func (r *Repo) DeleteRef(ctx context.Context, ref, oldSHA string) error {
	_, err := r.run(ctx, nil, "update-ref", "-d", "--end-of-options", ref, oldSHA)
	if err != nil && ExitCode(err) == 128 {
		return ErrStale
	}
	return err
}

// Size returns the disk usage of the repository in bytes.
func (r *Repo) Size() int64 {
	var total int64
	_ = filepath.WalkDir(r.Path, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// HashObject stores a blob and returns its id.
func (r *Repo) HashObject(ctx context.Context, data []byte) (string, error) {
	out, err := r.run(ctx, &Opts{Stdin: bytes.NewReader(data)}, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TreeWith returns the tree of base (a tree id, or "" for none) with the file at path set to a blob, or removed
// when blob is "". It works on a temporary index, so it needs no working tree.
func (r *Repo) TreeWith(ctx context.Context, base, path, blob, mode string) (string, error) {
	f, err := os.CreateTemp("", "kogge-index-*")
	if err != nil {
		return "", err
	}
	index := f.Name()
	f.Close()
	os.Remove(index) // git creates it; an empty file is not a valid index
	defer os.Remove(index)
	o := &Opts{Env: []string{"GIT_INDEX_FILE=" + index}}
	if base != "" {
		if _, err := r.run(ctx, o, "read-tree", "--end-of-options", base); err != nil {
			return "", err
		}
	}
	if blob == "" {
		// Mode 0 and the null id remove an entry; unlike --remove, this needs no working tree.
		del := &Opts{Env: o.Env, Stdin: strings.NewReader("0 " + strings.Repeat("0", 40) + "\t" + path + "\n")}
		if _, err := r.run(ctx, del, "update-index", "--index-info"); err != nil {
			return "", err
		}
	} else if _, err := r.run(ctx, o, "update-index", "--add", "--cacheinfo", mode+","+blob+","+path); err != nil {
		return "", err
	}
	out, err := r.run(ctx, o, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TreeOf returns the root tree of a commit.
func (r *Repo) TreeOf(ctx context.Context, commit string) (string, error) {
	out, err := r.run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{tree}")
	if err != nil {
		if ExitCode(err) == 1 {
			return "", ErrNotFound
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
