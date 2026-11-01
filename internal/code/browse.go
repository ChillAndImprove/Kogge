// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/code/gitdiff"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
)

// Person is an author or committer, with the Kogge user of a verified e-mail address.
type Person struct {
	Name  string            `json:"name"`
	Email string            `json:"email"`
	User  *identity.UserRef `json:"user,omitempty"`
	When  time.Time         `json:"when"`
}

// CommitInfo is a commit.
type CommitInfo struct {
	SHA       string   `json:"sha"`
	Parents   []string `json:"parents"`
	Author    Person   `json:"author"`
	Committer Person   `json:"committer"`
	Subject   string   `json:"subject"`
	Message   string   `json:"message"`
}

func (s *Service) commitInfos(ctx context.Context, commits []git.Commit) ([]CommitInfo, error) {
	emails := []string{}
	for _, c := range commits {
		emails = append(emails, c.Author.Email, c.Committer.Email)
	}
	users, err := identity.UsersByEmail(ctx, s.pool, emails)
	if err != nil {
		return nil, err
	}
	person := func(sig git.Signature) Person {
		p := Person{Name: sig.Name, Email: sig.Email, When: sig.When}
		if u, ok := users[strings.ToLower(sig.Email)]; ok {
			p.User = &u
		}
		return p
	}
	out := make([]CommitInfo, 0, len(commits))
	for _, c := range commits {
		out = append(out, CommitInfo{SHA: c.SHA, Parents: nonNil(c.Parents), Author: person(c.Author), Committer: person(c.Committer),
			Subject: c.Subject(), Message: c.Message})
	}
	return out, nil
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// resolve turns a ref of the API (branch, tag or commit id; default: the default branch) into a commit.
func (s *Service) resolve(ctx context.Context, repo *git.Repo, r *repoRow, ref string) (string, error) {
	if ref == "" {
		ref = r.DefaultBranch
	}
	candidates := []string{"refs/heads/" + ref, "refs/tags/" + ref}
	if strings.HasPrefix(ref, "refs/") {
		candidates = []string{ref}
	}
	if shaPattern.MatchString(ref) {
		candidates = append(candidates, ref)
	}
	for _, c := range candidates {
		sha, err := repo.Resolve(ctx, c)
		if err == nil {
			return sha, nil
		}
		if !errors.Is(err, git.ErrNotFound) {
			return "", err
		}
	}
	return "", apperr.NotFound("ref.not-found", "The branch, tag or commit %s does not exist.", ref)
}

// readRepo opens a repository the principal may read.
func (s *Service) readRepo(ctx context.Context, p *identity.Principal, key, slug string) (*repoRow, *git.Repo, error) {
	a, err := s.Access(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	r, err := s.repo(ctx, a, key, slug, LevelRead)
	if err != nil {
		return nil, nil, err
	}
	return r, s.open(r.ID), nil
}

// Branch is a branch with its latest commit and its distance to the default branch.
type Branch struct {
	Name    string     `json:"name"`
	Commit  CommitInfo `json:"commit"`
	Default bool       `json:"default"`
	Ahead   int        `json:"ahead" doc:"Commits not on the default branch"`
	Behind  int        `json:"behind" doc:"Commits of the default branch not on this branch"`
}

// matchingRefs keeps the refs whose short names contain the filter (in any case) and puts first those named like
// it, so that a search for a name finds it on the first page however many others contain it.
func matchingRefs(refs []git.Ref, filter string) []git.Ref {
	filter = strings.ToLower(strings.TrimSpace(filter))
	var list []git.Ref
	for _, ref := range refs {
		if filter == "" || strings.Contains(strings.ToLower(ref.ShortName()), filter) {
			list = append(list, ref)
		}
	}
	if filter != "" {
		sort.SliceStable(list, func(i, j int) bool {
			return strings.ToLower(list[i].ShortName()) == filter && strings.ToLower(list[j].ShortName()) != filter
		})
	}
	return list
}

// pageOf returns the page of a list at offset; limits beyond 200 or below 1 become 50.
func pageOf[T any](list []T, limit, offset int) []T {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset = max(offset, 0)
	return list[min(offset, len(list)):min(offset+limit, len(list))]
}

// Branches lists a page of the branches: the default branch first, then a branch named like the filter, then the
// most recently changed. Their distances to the default branch are counted for the page only.
func (s *Service) Branches(ctx context.Context, p *identity.Principal, key, slug, filter string, limit, offset int) ([]Branch, int, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, 0, err
	}
	refs, err := repo.Refs(ctx, "refs/heads/")
	if err != nil {
		return nil, 0, err
	}
	list := matchingRefs(refs, filter)
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].ShortName() == r.DefaultBranch && list[j].ShortName() != r.DefaultBranch
	})
	total := len(list)
	list = pageOf(list, limit, offset)
	commits := make([]git.Commit, len(list))
	var others []git.Ref
	for i, ref := range list {
		commits[i] = git.Commit{SHA: ref.Commit, Author: git.Signature{Name: ref.AuthorName, Email: ref.AuthorEmail, When: ref.CommitterDate},
			Committer: git.Signature{When: ref.CommitterDate}, Message: ref.Subject}
		if ref.ShortName() != r.DefaultBranch {
			others = append(others, ref)
		}
	}
	infos, err := s.commitInfos(ctx, commits)
	if err != nil {
		return nil, 0, err
	}
	// The distances only decorate the list: without them, it is still worth showing.
	var distances map[string]git.Distance
	if defaultSHA, _ := repo.ResolveBranch(ctx, r.DefaultBranch); defaultSHA != "" && len(others) > 0 {
		if distances, err = repo.Distances(ctx, defaultSHA, others); err != nil {
			s.log.Warn("counting the commits of branches ahead of and behind the default branch", "repository", r.ID, "error", err)
		}
	}
	out := []Branch{}
	for i, ref := range list {
		b := Branch{Name: ref.ShortName(), Commit: infos[i], Default: ref.ShortName() == r.DefaultBranch}
		if d, ok := distances[ref.Name]; ok && !b.Default {
			b.Ahead, b.Behind = d.Ahead, d.Behind
		}
		out = append(out, b)
	}
	return out, total, nil
}

// Tag is a tag with its commit.
type Tag struct {
	Name      string     `json:"name"`
	Commit    CommitInfo `json:"commit"`
	Annotated bool       `json:"annotated"`
	Message   string     `json:"message,omitempty"`
}

// Tags lists a page of the tags: a tag named like the filter first, then the newest.
func (s *Service) Tags(ctx context.Context, p *identity.Principal, key, slug, filter string, limit, offset int) ([]Tag, int, error) {
	_, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, 0, err
	}
	refs, err := repo.Refs(ctx, "refs/tags/")
	if err != nil {
		return nil, 0, err
	}
	list := matchingRefs(refs, filter)
	total := len(list)
	list = pageOf(list, limit, offset)
	commits := make([]git.Commit, len(list))
	for i, ref := range list {
		commits[i] = git.Commit{SHA: ref.Commit, Author: git.Signature{Name: ref.AuthorName, Email: ref.AuthorEmail, When: ref.CommitterDate},
			Committer: git.Signature{When: ref.CommitterDate}, Message: ref.Subject}
	}
	infos, err := s.commitInfos(ctx, commits)
	if err != nil {
		return nil, 0, err
	}
	out := []Tag{}
	for i, ref := range list {
		out = append(out, Tag{Name: ref.ShortName(), Commit: infos[i], Annotated: ref.Type == "tag", Message: ref.TagMessage})
	}
	return out, total, nil
}

// TreeEntry is a file, directory or submodule.
type TreeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type" enum:"file,dir,submodule,symlink"`
	Size int64  `json:"size,omitempty"`
	Mode string `json:"mode"`
}

// Tree is a directory at a commit.
type Tree struct {
	Ref     string      `json:"ref"`
	Commit  string      `json:"commit"`
	Path    string      `json:"path"`
	Kind    string      `json:"kind" enum:"dir,file" doc:"file when the path is a file (then there are no entries)"`
	Entries []TreeEntry `json:"entries"`
	Readme  string      `json:"readme,omitempty" doc:"Path of the directory's README, if any"`
	Latest  *CommitInfo `json:"latest,omitempty" doc:"The latest commit that changed the directory"`
}

var readmeNames = []string{"readme.md", "readme.markdown", "readme", "readme.txt", "readme.rst"}

// Tree lists a directory.
func (s *Service) Tree(ctx context.Context, p *identity.Principal, key, slug, ref, dir string) (*Tree, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return nil, err
	}
	dir = strings.Trim(dir, "/")
	entries, err := repo.Tree(ctx, commit, dir)
	if errors.Is(err, git.ErrNotFound) {
		if _, err := repo.Stat(ctx, commit, dir); err == nil {
			return &Tree{Ref: ref, Commit: commit, Path: dir, Kind: "file", Entries: []TreeEntry{}}, nil
		}
		return nil, apperr.NotFound("path.not-found", "The path %s does not exist at %s.", dir, ref)
	}
	if err != nil {
		return nil, err
	}
	t := &Tree{Ref: ref, Commit: commit, Path: dir, Kind: "dir", Entries: []TreeEntry{}}
	for _, e := range entries {
		te := TreeEntry{Name: e.Name, Path: e.Path, Size: e.Size, Mode: e.Mode}
		switch {
		case e.Type == "tree":
			te.Type = "dir"
		case e.Type == "commit":
			te.Type = "submodule"
		case e.Mode == "120000":
			te.Type = "symlink"
		default:
			te.Type = "file"
		}
		t.Entries = append(t.Entries, te)
		if te.Type == "file" && t.Readme == "" && slices.Contains(readmeNames, strings.ToLower(e.Name)) {
			t.Readme = e.Path
		}
	}
	sort.SliceStable(t.Entries, func(i, j int) bool {
		di, dj := t.Entries[i].Type == "dir", t.Entries[j].Type == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(t.Entries[i].Name) < strings.ToLower(t.Entries[j].Name)
	})
	if latest, err := repo.Log(ctx, git.LogOptions{Rev: commit, Path: dir, Limit: 1}); err == nil && len(latest) == 1 {
		infos, err := s.commitInfos(ctx, latest)
		if err != nil {
			return nil, err
		}
		t.Latest = &infos[0]
	}
	return t, nil
}

// LFSPointer describes a file stored with Git LFS.
type LFSPointer struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// File is the content of a file at a commit.
type File struct {
	Ref       string        `json:"ref"`
	Commit    string        `json:"commit"`
	Path      string        `json:"path"`
	Size      int64         `json:"size"`
	Mode      string        `json:"mode"`
	Binary    bool          `json:"binary"`
	Image     bool          `json:"image" doc:"An image the browser can show (from the raw address)"`
	Truncated bool          `json:"truncated" doc:"Only the beginning is shown"`
	Language  string        `json:"language,omitempty"`
	Lines     []string      `json:"lines" doc:"Highlighted lines (escaped HTML with token classes)"`
	Markdown  *docmodel.Doc `json:"markdown,omitempty" doc:"Markdown files rendered as a document"`
	LFS       *LFSPointer   `json:"lfs,omitempty"`
}

// maxFileView is the size of files shown in the browser.
const maxFileView = 1 << 20

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".svg": "image/svg+xml", ".ico": "image/x-icon", ".bmp": "image/bmp", ".avif": "image/avif"}

// File returns a file for display.
func (s *Service) File(ctx context.Context, p *identity.Principal, key, slug, ref, filePath string) (*File, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return nil, err
	}
	filePath = strings.Trim(filePath, "/")
	info, err := repo.Stat(ctx, commit, filePath)
	if errors.Is(err, git.ErrNotFound) {
		return nil, apperr.NotFound("path.not-found", "The file %s does not exist at %s.", filePath, ref)
	}
	if err != nil {
		return nil, err
	}
	f := &File{Ref: ref, Commit: commit, Path: filePath, Size: info.Size, Mode: info.Mode, Lines: []string{}}
	_, f.Image = imageTypes[strings.ToLower(path.Ext(filePath))]
	if info.Mode == "160000" {
		return f, nil
	}
	data, err := repo.ReadBlob(ctx, info.SHA, maxFileView)
	if err != nil {
		return nil, err
	}
	f.Truncated = int64(len(data)) < info.Size
	if lfs := parseLFSPointer(data); lfs != nil {
		f.LFS = lfs
		f.Size = lfs.Size
		return f, nil
	}
	if f.Image {
		return f, nil
	}
	if isBinary(data) {
		f.Binary = true
		return f, nil
	}
	text := string(data)
	if f.Truncated {
		// Cut at the last complete line.
		if i := strings.LastIndexByte(text, '\n'); i > 0 {
			text = text[:i]
		}
	}
	f.Lines, f.Language = highlight(filePath, text)
	if ext := strings.ToLower(path.Ext(filePath)); (ext == ".md" || ext == ".markdown") && !f.Truncated {
		f.Markdown = docmodel.FromMarkdownWith(text, s.markdownOptions(ctx, r, ref, commit, filePath))
	}
	return f, nil
}

// markdownOptions resolve the images and links of a Markdown file in a repository. Paths are the repository's —
// relative to the file, or from the repository's top with a leading slash: images come from the file's commit,
// links open in Hafen at the ref. Addresses of the instance become paths; images from other servers show only if
// the instance allows them.
func (s *Service) markdownOptions(ctx context.Context, r *repoRow, ref, commit, filePath string) docmodel.MarkdownOptions {
	external := false
	if st, err := s.settings.Get(ctx); err == nil {
		external = st.AllowExternalImages
	}
	dir := path.Dir(filePath)
	// inRepo resolves a path of the repository and keeps the fragment; ".." stops at the repository's top.
	inRepo := func(target string) (p, fragment string) {
		target, fragment, _ = strings.Cut(target, "#")
		target, _, _ = strings.Cut(target, "?")
		if unescaped, err := url.PathUnescape(target); err == nil {
			target = unescaped
		}
		if !strings.HasPrefix(target, "/") {
			target = "/" + dir + "/" + target
		}
		return strings.TrimPrefix(path.Clean(target), "/"), fragment
	}
	ownAddress := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil || !u.IsAbs() || s.opt.PublicURL == nil || !strings.EqualFold(u.Host, s.opt.PublicURL.Host) {
			return "", false
		}
		return u.RequestURI(), true
	}
	return docmodel.MarkdownOptions{
		Image: func(src string) (string, bool) {
			if own, ok := ownAddress(src); ok {
				return own, true
			}
			if u, err := url.Parse(src); err != nil || u.IsAbs() || strings.HasPrefix(src, "//") {
				return src, external && err == nil && (u.Scheme == "https" || u.Scheme == "http")
			}
			p, _ := inRepo(src)
			if p == "" {
				return "", false
			}
			return "/api/v1/code/repos/" + url.PathEscape(r.ProjectKey) + "/" + url.PathEscape(r.Slug) + "/raw?ref=" + url.QueryEscape(commit) +
				"&path=" + url.QueryEscape(p), true
		},
		Link: func(href string) string {
			p, fragment := inRepo(href)
			out := "/code/" + strings.ToLower(r.ProjectKey) + "/" + r.Slug
			if p != "" {
				out += "/src/" + escapeRepoPath(p)
			}
			if ref != "" && ref != r.DefaultBranch {
				out += "?at=" + url.QueryEscape(ref)
			}
			if fragment != "" {
				out += "#" + fragment
			}
			return out
		},
	}
}

// escapeRepoPath escapes the parts of a path for an address, keeping its slashes.
func escapeRepoPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	// Invalid UTF-8 in the sample (allowing a character cut at the end).
	for len(sample) > 0 {
		r, size := utf8.DecodeRune(sample)
		if r == utf8.RuneError && size == 1 && len(sample) > 3 {
			return true
		}
		sample = sample[size:]
	}
	return false
}

func parseLFSPointer(data []byte) *LFSPointer {
	if len(data) > 1024 || !bytes.HasPrefix(data, []byte("version https://git-lfs.github.com/spec/v1")) {
		return nil
	}
	p := &LFSPointer{}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "oid sha256:"); ok {
			p.OID = v
		}
		if v, ok := strings.CutPrefix(line, "size "); ok {
			p.Size, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if len(p.OID) != 64 {
		return nil
	}
	return p
}

// RawView says how raw content is requested: to download, embedded as an image, or opened as a page.
type RawView int

// Raw views.
const (
	RawDownload RawView = iota
	RawImage
	RawPage
)

// Raw writes a file's content (for downloads and images); the caller sets nothing else.
func (s *Service) Raw(ctx context.Context, p *identity.Principal, key, slug, ref, filePath string, w http.ResponseWriter, view RawView) error {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return err
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return err
	}
	filePath = strings.Trim(filePath, "/")
	info, err := repo.Stat(ctx, commit, filePath)
	if errors.Is(err, git.ErrNotFound) {
		return apperr.NotFound("path.not-found", "The file %s does not exist at %s.", filePath, ref)
	}
	if err != nil {
		return err
	}
	head, err := repo.ReadBlob(ctx, info.SHA, 1024)
	if err != nil {
		return err
	}
	if lfs := parseLFSPointer(head); lfs != nil && s.lfsObjectOf(ctx, r.ID, lfs.OID) {
		if rc, err := s.blobs.Open(lfs.OID); err == nil {
			defer rc.Close()
			setRawHeaders(w, filePath, lfs.Size, head, view)
			w.Header().Set("Content-Type", contentTypeOf(filePath, nil))
			_, err = io.Copy(w, rc)
			return err
		}
	}
	setRawHeaders(w, filePath, info.Size, head, view)
	return repo.StreamBlob(ctx, info.SHA, w)
}

// setRawHeaders makes content of repositories safe to serve from Kogge's origin: text is plain text, only images
// show inline, and a sandbox policy keeps SVG scripts from running. SVG shows inline only as an embedded image:
// opened as a page it could pass off text and links as Kogge's.
func setRawHeaders(w http.ResponseWriter, filePath string, size int64, head []byte, view RawView) {
	h := w.Header()
	ct := contentTypeOf(filePath, head)
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, max-age=60")
	name := path.Base(filePath)
	disposition := "inline"
	if view == RawDownload || (!strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "text/plain")) ||
		(ct == "image/svg+xml" && view != RawImage) {
		disposition = "attachment"
	}
	h.Set("Content-Disposition", disposition+"; filename*=UTF-8''"+url.PathEscape(name))
}

func contentTypeOf(filePath string, head []byte) string {
	if ct, ok := imageTypes[strings.ToLower(path.Ext(filePath))]; ok {
		return ct
	}
	if head != nil && !isBinary(head) {
		return "text/plain; charset=utf-8"
	}
	switch strings.ToLower(path.Ext(filePath)) {
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	}
	return "application/octet-stream"
}

// Archive writes a zip or tar.gz archive of a ref.
func (s *Service) Archive(ctx context.Context, p *identity.Principal, key, slug, ref, format string, w http.ResponseWriter) error {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return err
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return err
	}
	if format != "zip" && format != "tar.gz" {
		return apperr.Invalid("archive.format", "The format must be zip or tar.gz.")
	}
	name := archiveName(r.Slug, cmpOr(ref, r.DefaultBranch))
	ct := "application/zip"
	if format == "tar.gz" {
		ct = "application/gzip"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name+"."+format))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	return repo.Archive(ctx, commit, format, name, w)
}

// archiveName names an archive and the folder of its entries after the repository and the ref. The ref may be any
// revision ("main^{/fix}"), so all but letters, digits, ".", "_" and "-" becomes "-": nothing in the name
// separates paths, also not "\" on Windows.
func archiveName(slug, ref string) string {
	return slug + "-" + strings.Map(func(c rune) rune {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '.' || c == '_' || c == '-' {
			return c
		}
		return '-'
	}, ref)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// CommitList is a page of commits.
type CommitList struct {
	Commits []CommitInfo `json:"commits"`
	More    bool         `json:"more" doc:"More commits follow"`
}

// Commits lists the history of a ref (and path).
func (s *Service) Commits(ctx context.Context, p *identity.Principal, key, slug, ref, filePath, exclude string, limit, offset int) (*CommitList, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return nil, err
	}
	o := git.LogOptions{Rev: commit, Path: strings.Trim(filePath, "/"), Skip: offset, Limit: min(max(limit, 1), 200) + 1}
	if exclude != "" {
		ex, err := s.resolve(ctx, repo, r, exclude)
		if err != nil {
			return nil, err
		}
		o.Exclude = []string{ex}
	}
	if o.Path != "" {
		o.Follow = true
	}
	list, err := repo.Log(ctx, o)
	if err != nil {
		return nil, err
	}
	more := len(list) >= o.Limit
	if more {
		list = list[:o.Limit-1]
	}
	infos, err := s.commitInfos(ctx, list)
	if err != nil {
		return nil, err
	}
	return &CommitList{Commits: nonNil(infos), More: more}, nil
}

// DiffOptions select how diffs are shown.
type DiffOptions struct {
	IgnoreWhitespace bool
	Context          int
	Path             string
}

// CommitDetail is a commit with its changes.
type CommitDetail struct {
	CommitInfo
	Diff *gitdiff.Diff `json:"diff"`
}

// Commit returns a commit with its diff against the first parent.
func (s *Service) Commit(ctx context.Context, p *identity.Principal, key, slug, sha string, o DiffOptions) (*CommitDetail, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolve(ctx, repo, r, sha)
	if err != nil {
		return nil, err
	}
	c, err := repo.Commit(ctx, commit)
	if err != nil {
		return nil, err
	}
	infos, err := s.commitInfos(ctx, []git.Commit{*c})
	if err != nil {
		return nil, err
	}
	from := git.EmptyTree
	if len(c.Parents) > 0 {
		from = c.Parents[0]
	}
	d, err := s.diff(ctx, repo, from, c.SHA, o)
	if err != nil {
		return nil, err
	}
	return &CommitDetail{CommitInfo: infos[0], Diff: d}, nil
}

func (s *Service) diff(ctx context.Context, repo *git.Repo, from, to string, o DiffOptions) (*gitdiff.Diff, error) {
	var paths []string
	if o.Path != "" {
		paths = []string{strings.Trim(o.Path, "/")}
	}
	patch, truncated, err := repo.Diff(ctx, from, to, git.DiffOptions{IgnoreWhitespace: o.IgnoreWhitespace, Context: o.Context, Paths: paths})
	if err != nil {
		return nil, err
	}
	d := gitdiff.Parse(patch, gitdiff.Limits{})
	if truncated {
		d.Truncated = true
		if n := len(d.Files); n > 0 {
			d.Files[n-1].Truncated = true
		}
	}
	highlightDiff(d)
	return d, nil
}

// Comparison is the difference between two refs: the commits of "to" not in "from", and the changes since their
// merge base.
type Comparison struct {
	From      string        `json:"from"`
	To        string        `json:"to"`
	MergeBase string        `json:"mergeBase"`
	Commits   []CommitInfo  `json:"commits"`
	More      bool          `json:"more"`
	Diff      *gitdiff.Diff `json:"diff"`
}

// Compare compares two refs (three-dot: changes of "to" since the merge base).
func (s *Service) Compare(ctx context.Context, p *identity.Principal, key, slug, from, to string, o DiffOptions) (*Comparison, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	fromSHA, err := s.resolve(ctx, repo, r, from)
	if err != nil {
		return nil, err
	}
	toSHA, err := s.resolve(ctx, repo, r, to)
	if err != nil {
		return nil, err
	}
	base, err := repo.MergeBase(ctx, fromSHA, toSHA)
	if err != nil {
		return nil, err
	}
	c := &Comparison{From: fromSHA, To: toSHA, MergeBase: base, Commits: []CommitInfo{}}
	list, err := repo.Log(ctx, git.LogOptions{Rev: toSHA, Exclude: []string{fromSHA}, Limit: 251})
	if err != nil {
		return nil, err
	}
	if len(list) > 250 {
		list, c.More = list[:250], true
	}
	if c.Commits, err = s.commitInfos(ctx, list); err != nil {
		return nil, err
	}
	c.Commits = nonNil(c.Commits)
	diffBase := base
	if diffBase == "" {
		diffBase = git.EmptyTree
	}
	if c.Diff, err = s.diff(ctx, repo, diffBase, toSHA, o); err != nil {
		return nil, err
	}
	return c, nil
}

// BlameRange is a run of lines last changed by one commit.
type BlameRange struct {
	Commit CommitInfo `json:"commit"`
	From   int        `json:"from"`
	To     int        `json:"to"`
}

// Blame is a file with the commits that last changed its lines.
type Blame struct {
	Path     string       `json:"path"`
	Commit   string       `json:"commit"`
	Language string       `json:"language,omitempty"`
	Lines    []string     `json:"lines"`
	Ranges   []BlameRange `json:"ranges"`
}

// Blame annotates a file.
func (s *Service) Blame(ctx context.Context, p *identity.Principal, key, slug, ref, filePath string) (*Blame, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return nil, err
	}
	filePath = strings.Trim(filePath, "/")
	info, err := repo.Stat(ctx, commit, filePath)
	if errors.Is(err, git.ErrNotFound) {
		return nil, apperr.NotFound("path.not-found", "The file %s does not exist at %s.", filePath, ref)
	}
	if err != nil {
		return nil, err
	}
	if info.Size > maxFileView {
		return nil, apperr.Invalid("blame.too-large", "The file is too large to annotate.")
	}
	lines, commits, err := repo.Blame(ctx, commit, filePath)
	if err != nil {
		return nil, err
	}
	var list []git.Commit
	for _, c := range commits {
		list = append(list, *c)
	}
	infos, err := s.commitInfos(ctx, list)
	if err != nil {
		return nil, err
	}
	bySHA := map[string]CommitInfo{}
	for _, ci := range infos {
		bySHA[ci.SHA] = ci
	}
	b := &Blame{Path: filePath, Commit: commit, Ranges: []BlameRange{}}
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.Text
		if n := len(b.Ranges); n > 0 && b.Ranges[n-1].Commit.SHA == l.SHA && b.Ranges[n-1].To == i {
			b.Ranges[n-1].To = i + 1
			continue
		}
		b.Ranges = append(b.Ranges, BlameRange{Commit: bySHA[l.SHA], From: i + 1, To: i + 1})
	}
	b.Lines, b.Language = highlight(filePath, strings.Join(texts, "\n"))
	if len(texts) == 0 {
		b.Lines = []string{}
	}
	return b, nil
}
