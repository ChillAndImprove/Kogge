// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package code

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/code/git"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/db"
)

// Code owners: a CODEOWNERS file says who owns which files of a repository. The owners of the files that a pull
// request changes become its reviewers (people) or hear of it (members of small groups), and a merge check can ask
// that an owner of every changed file approves. The file counts as it is on the pull request's target branch: a
// pull request cannot choose its own owners.

// codeOwnersPaths are where the file may be; the first one found counts.
var codeOwnersPaths = []string{".kogge/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

const (
	maxCodeOwnersSize = 64 << 10
	maxOwnerRules     = 1000
	maxRuleOwners     = 50
	maxOwnerPattern   = 1024
	// ownerGroupNotify: the members of owner groups of up to this size hear of new pull requests (not whole
	// departments).
	ownerGroupNotify = 25
	// maxOwnedFiles: pull requests that change more files are not matched against the owners.
	maxOwnedFiles = 10000
	// listedOwnedFiles: the files of a pull request listed for each line of owners.
	listedOwnedFiles = 20
)

// ownerRule is a line of a CODEOWNERS file: a pattern and its owners.
type ownerRule struct {
	line    int
	pattern string
	re      *regexp.Regexp
	names   []string // the owners as written
	users   []identity.UserRef
	groups  []identity.GroupRef
	unknown []string // owners that name nobody
}

// codeOwners is a CODEOWNERS file with its owners looked up.
type codeOwners struct {
	path     string
	rules    []*ownerRule
	problems []CodeOwnersProblem
}

// CodeOwnersProblem is a line of a CODEOWNERS file that does not work as written. Clients word it from its code
// and values (name: the owner or pattern; max: a limit); the message words it in English.
type CodeOwnersProblem struct {
	Line    int               `json:"line" doc:"0: the whole file"`
	Code    string            `json:"code" enum:"too-large,too-many-lines,too-many-owners,not-an-owner,unknown-owner,unclosed-quote,long-pattern,exclusion,unsupported"`
	Values  map[string]string `json:"values,omitempty"`
	Message string            `json:"message"`
}

// ownersError is a problem of a line found while reading it.
type ownersError struct {
	code, name, message string
}

func (e *ownersError) Error() string { return e.message }

func (e *ownersError) problem(line int) CodeOwnersProblem {
	p := CodeOwnersProblem{Line: line, Code: e.code, Message: e.message}
	if e.name != "" {
		p.Values = map[string]string{"name": e.name}
	}
	return p
}

// parseCodeOwners reads the lines of a CODEOWNERS file: a pattern, then its owners; # starts a comment.
func parseCodeOwners(data []byte) ([]*ownerRule, []CodeOwnersProblem) {
	var rules []*ownerRule
	var problems []CodeOwnersProblem
	if len(data) > maxCodeOwnersSize {
		problems = append(problems, CodeOwnersProblem{Code: "too-large", Values: map[string]string{"max": "64 KiB"},
			Message: "The file is larger than 64 KiB: only its beginning counts."})
		data = data[:maxCodeOwnersSize]
	}
	for i, raw := range strings.Split(string(data), "\n") {
		n := i + 1
		fields, err := ownerFields(strings.TrimSpace(strings.TrimSuffix(raw, "\r")))
		if err != nil {
			problems = append(problems, err.problem(n))
			continue
		}
		if len(fields) == 0 {
			continue
		}
		re, perr := ownerPattern(fields[0])
		if perr != nil {
			problems = append(problems, perr.problem(n))
			continue
		}
		if len(rules) == maxOwnerRules {
			problems = append(problems, CodeOwnersProblem{Line: n, Code: "too-many-lines", Values: map[string]string{"max": strconv.Itoa(maxOwnerRules)},
				Message: fmt.Sprintf("Only the first %d lines with owners count.", maxOwnerRules)})
			break
		}
		r := &ownerRule{line: n, pattern: fields[0], re: re}
		for _, f := range fields[1:] {
			if len(r.names) == maxRuleOwners {
				problems = append(problems, CodeOwnersProblem{Line: n, Code: "too-many-owners", Values: map[string]string{"max": strconv.Itoa(maxRuleOwners)},
					Message: fmt.Sprintf("Only the first %d owners of a line count.", maxRuleOwners)})
				break
			}
			if !strings.HasPrefix(f, "@") && !strings.Contains(f, "@") {
				problems = append(problems, CodeOwnersProblem{Line: n, Code: "not-an-owner", Values: map[string]string{"name": f},
					Message: fmt.Sprintf(`%q is not an owner: write @name, @"name with spaces" or an e-mail address.`, f)})
				continue
			}
			r.names = append(r.names, f)
		}
		rules = append(rules, r)
	}
	return rules, problems
}

// ownerFields splits a line at spaces; double quotes keep spaces, and # outside a field starts a comment.
func ownerFields(line string) ([]string, *ownersError) {
	var out []string
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' {
			i++
			continue
		}
		if line[i] == '#' {
			break
		}
		var b strings.Builder
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			if line[i] == '"' {
				end := strings.IndexByte(line[i+1:], '"')
				if end < 0 {
					return nil, &ownersError{code: "unclosed-quote", message: "A quote is not closed."}
				}
				b.WriteString(line[i+1 : i+1+end])
				i += end + 2
				continue
			}
			b.WriteByte(line[i])
			i++
		}
		out = append(out, b.String())
	}
	return out, nil
}

// ownerPattern compiles a pattern of a CODEOWNERS file. Patterns match paths from the top of the repository:
// with a slash at the start or inside they match from the top, otherwise at any depth; * and ? stay within a
// directory, ** crosses directories, and a pattern that matches a directory owns everything in it.
func ownerPattern(p string) (*regexp.Regexp, *ownersError) {
	pattern := p
	switch {
	case len(p) > maxOwnerPattern:
		return nil, &ownersError{code: "long-pattern", message: fmt.Sprintf("The pattern is longer than %d characters.", maxOwnerPattern)}
	case strings.HasPrefix(p, "!"):
		return nil, &ownersError{code: "exclusion", name: p,
			message: p + ": patterns cannot leave files out; give those files their owners in a later line."}
	case strings.ContainsAny(p, `[]\`):
		return nil, &ownersError{code: "unsupported", name: p, message: p + ": character classes and escapes are not supported."}
	}
	anchored := strings.HasPrefix(p, "/") || strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.TrimPrefix(p, "/")
	dir := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return regexp.MustCompile(`^.*$`), nil // the top of the repository
	}
	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(p); {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i += 2
		case p[i] == '*':
			b.WriteString("[^/]*")
			i++
		case p[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			r, size := utf8.DecodeRuneInString(p[i:])
			b.WriteString(regexp.QuoteMeta(string(r)))
			i += size
		}
	}
	if dir {
		b.WriteString("/.*$")
	} else {
		b.WriteString("(?:/.*)?$")
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, &ownersError{code: "unsupported", name: pattern, message: pattern + ": " + err.Error()}
	}
	return re, nil
}

// ruleOf returns the last line whose pattern matches a path (later lines win), or nil.
func (c *codeOwners) ruleOf(path string) *ownerRule {
	for i := len(c.rules) - 1; i >= 0; i-- {
		if c.rules[i].re.MatchString(path) {
			return c.rules[i]
		}
	}
	return nil
}

// resolve looks the owners up: @name is a user or else a group, an e-mail address a user who confirmed it.
func (c *codeOwners) resolve(ctx context.Context, q db.Querier) error {
	var names, emails []string
	for _, r := range c.rules {
		for _, n := range r.names {
			if name, ok := strings.CutPrefix(n, "@"); ok {
				names = append(names, name)
			} else {
				emails = append(emails, n)
			}
		}
	}
	users, err := identity.UsersByUsername(ctx, q, names)
	if err != nil {
		return err
	}
	groups, err := identity.GroupsByName(ctx, q, names)
	if err != nil {
		return err
	}
	byEmail, err := identity.UsersByEmail(ctx, q, emails)
	if err != nil {
		return err
	}
	for _, r := range c.rules {
		for _, n := range r.names {
			name, isName := strings.CutPrefix(n, "@")
			key := strings.ToLower(name)
			switch u, g, e := users[key], groups[key], byEmail[key]; {
			case isName && u.ID != 0:
				if !slices.ContainsFunc(r.users, func(x identity.UserRef) bool { return x.ID == u.ID }) {
					r.users = append(r.users, u)
				}
			case isName && g.ID != 0:
				if !slices.ContainsFunc(r.groups, func(x identity.GroupRef) bool { return x.ID == g.ID }) {
					r.groups = append(r.groups, g)
				}
			case !isName && e.ID != 0 && e.Active:
				if !slices.ContainsFunc(r.users, func(x identity.UserRef) bool { return x.ID == e.ID }) {
					r.users = append(r.users, e)
				}
			default:
				r.unknown = append(r.unknown, n)
				c.problems = append(c.problems, CodeOwnersProblem{Line: r.line, Code: "unknown-owner", Values: map[string]string{"name": n},
					Message: fmt.Sprintf("%s is neither a person nor a group.", n)})
			}
		}
	}
	slices.SortFunc(c.problems, func(a, b CodeOwnersProblem) int { return a.Line - b.Line })
	return nil
}

// loadCodeOwners reads the CODEOWNERS file of a commit; nil if it has none.
func loadCodeOwners(ctx context.Context, q db.Querier, repo *git.Repo, commit string) (*codeOwners, error) {
	for _, p := range codeOwnersPaths {
		info, err := repo.Stat(ctx, commit, p)
		if errors.Is(err, git.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		data, err := repo.ReadBlob(ctx, info.SHA, maxCodeOwnersSize+1)
		if err != nil {
			return nil, err
		}
		c := &codeOwners{path: p}
		c.rules, c.problems = parseCodeOwners(data)
		if err := c.resolve(ctx, q); err != nil {
			return nil, err
		}
		return c, nil
	}
	return nil, nil
}

// OwnedFiles are changed files of a pull request that the same line of CODEOWNERS gives owners.
type OwnedFiles struct {
	Line       int                 `json:"line" doc:"The line of the CODEOWNERS file"`
	Pattern    string              `json:"pattern"`
	Users      []identity.UserRef  `json:"users"`
	Groups     []identity.GroupRef `json:"groups"`
	Unknown    []string            `json:"unknown" doc:"Owners as written that name nobody"`
	Files      []string            `json:"files" doc:"The first 20 files"`
	FileCount  int                 `json:"fileCount"`
	ApprovedBy []identity.UserRef  `json:"approvedBy" doc:"Owners who approved: the people named, or members of the groups"`
	owners     map[int64]bool      // people named
	ownerGroup map[int64]bool      // groups named
}

// prOwners are the code owners of a pull request's changes.
type prOwners struct {
	path  string
	known bool // the changed files are known (not too many)
	owned []*OwnedFiles
}

// ownedFiles matches the files that a pull request changes (from its merge base to its source) against the
// CODEOWNERS file of its target; nil if the target has none.
func (s *Service) ownedFiles(ctx context.Context, repoID int64, target, base, source string) (*prOwners, error) {
	repo := s.open(repoID)
	c, err := loadCodeOwners(ctx, s.pool, repo, target)
	if err != nil || c == nil {
		return nil, err
	}
	files, known, err := repo.ChangedPaths(ctx, base, source, maxOwnedFiles)
	if err != nil {
		return nil, err
	}
	out := &prOwners{path: c.path, known: known}
	byRule := map[*ownerRule]*OwnedFiles{}
	for _, f := range files {
		r := c.ruleOf(f)
		if r == nil || len(r.names) == 0 {
			continue // nobody owns it
		}
		o := byRule[r]
		if o == nil {
			o = &OwnedFiles{Line: r.line, Pattern: r.pattern, Users: nonNil(r.users), Groups: nonNil(r.groups), Unknown: nonNil(r.unknown),
				Files: []string{}, ApprovedBy: []identity.UserRef{}, owners: map[int64]bool{}, ownerGroup: map[int64]bool{}}
			for _, u := range r.users {
				o.owners[u.ID] = true
			}
			for _, g := range r.groups {
				o.ownerGroup[g.ID] = true
			}
			byRule[r] = o
			out.owned = append(out.owned, o)
		}
		if len(o.Files) < listedOwnedFiles {
			o.Files = append(o.Files, f)
		}
		o.FileCount++
	}
	slices.SortFunc(out.owned, func(a, b *OwnedFiles) int { return a.Line - b.Line })
	return out, nil
}

// owns reports whether a person owns the files, named or as a member of a group named.
func (o *OwnedFiles) owns(userID int64, groups []int64) bool {
	return o.owners[userID] || slices.ContainsFunc(groups, func(g int64) bool { return o.ownerGroup[g] })
}

// review marks the approvals of owners and the reviewers who own files of the pull request.
func (p *prOwners) review(ctx context.Context, q db.Querier, pr *prRow) error {
	ids := make([]int64, 0, len(pr.Reviewers))
	for _, rv := range pr.Reviewers {
		ids = append(ids, rv.User.ID)
	}
	groupsOf, err := identity.GroupsOfUsers(ctx, q, ids)
	if err != nil {
		return err
	}
	for i, rv := range pr.Reviewers {
		for _, o := range p.owned {
			if !o.owns(rv.User.ID, groupsOf[rv.User.ID]) {
				continue
			}
			pr.Reviewers[i].CodeOwner = true
			if rv.State == ReviewApproved && rv.User.ID != pr.authorID {
				o.ApprovedBy = append(o.ApprovedBy, rv.User)
			}
		}
	}
	return nil
}

// codeOwnersCheck is the merge check: an owner approved every changed file that has owners.
func codeOwnersCheck(p *prOwners) MergeCheck {
	c := MergeCheck{Key: "code-owners"}
	if p != nil && !p.known {
		c.Reason = "unknown"
		return c.say(apperr.Msg("The pull request changes too many files to find their code owners."))
	}
	waiting := 0
	if p != nil {
		for _, o := range p.owned {
			if len(o.ApprovedBy) == 0 {
				waiting += o.FileCount
			}
		}
	}
	switch {
	case p == nil || len(p.owned) == 0:
		c.OK, c.Reason = true, "none"
		return c.say(apperr.Msg("No changed file has code owners."))
	case waiting == 0:
		c.OK, c.Reason = true, "approved"
		return c.say(apperr.Msg("Code owners approved all the files they own."))
	default:
		c.Reason, c.Values = "waiting", map[string]int{"count": waiting}
		return c.say(plural(waiting, apperr.Msg("1 file waits for the approval of a code owner."),
			apperr.Msg("%d files wait for the approval of a code owner.", waiting)))
	}
}

// addCodeOwners makes the people who own changed files reviewers (if they can read the repository; never the
// author) and returns the members of small owner groups, to tell them.
func (s *Service) addCodeOwners(ctx context.Context, tx pgx.Tx, r *repoRow, prID, authorID int64, p *prOwners) ([]int64, error) {
	if p == nil {
		return nil, nil
	}
	var people, groups []int64
	for _, o := range p.owned {
		for _, u := range o.Users {
			if u.ID != authorID && !slices.Contains(people, u.ID) {
				people = append(people, u.ID)
			}
		}
		for _, g := range o.Groups {
			if !slices.Contains(groups, g.ID) {
				groups = append(groups, g.ID)
			}
		}
	}
	for _, uid := range people {
		rp, err := s.identity.PrincipalOf(ctx, uid, identity.MethodInternal)
		if err != nil {
			continue
		}
		ra, err := s.Access(ctx, rp)
		if err != nil {
			return nil, err
		}
		if ra.Level(r.ProjectID, r.ID) < LevelRead {
			continue // the file may name people who left the team
		}
		if err := s.addReviewers(ctx, tx, ra, r, prID, authorID, []int64{uid}); err != nil {
			return nil, err
		}
	}
	members, err := identity.ActiveMembers(ctx, tx, groups, ownerGroupNotify)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(append(people, members...), func(id int64) bool { return id == authorID }), nil
}

// CodeOwnersFile is a repository's CODEOWNERS file as Kogge reads it.
type CodeOwnersFile struct {
	Path     string              `json:"path,omitempty" doc:"Where the file is; empty if the ref has none"`
	Ref      string              `json:"ref"`
	Rules    []CodeOwnersRule    `json:"rules"`
	Problems []CodeOwnersProblem `json:"problems"`
	Owners   *CodeOwnersRule     `json:"owners,omitempty" doc:"With a path: the line that gives it owners"`
}

// CodeOwnersRule is a line of a CODEOWNERS file with its owners.
type CodeOwnersRule struct {
	Line    int                 `json:"line"`
	Pattern string              `json:"pattern"`
	Users   []identity.UserRef  `json:"users"`
	Groups  []identity.GroupRef `json:"groups"`
	Unknown []string            `json:"unknown" doc:"Owners as written that name nobody"`
}

func (r *ownerRule) info() CodeOwnersRule {
	return CodeOwnersRule{Line: r.line, Pattern: r.pattern, Users: nonNil(r.users), Groups: nonNil(r.groups), Unknown: nonNil(r.unknown)}
}

// CodeOwners returns the CODEOWNERS file of a ref (default: the default branch) and, with a path, its owners.
func (s *Service) CodeOwners(ctx context.Context, p *identity.Principal, key, slug, ref, path string) (*CodeOwnersFile, error) {
	r, repo, err := s.readRepo(ctx, p, key, slug)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if path, err = cleanRepoPath(path); err != nil {
			return nil, err
		}
	}
	if ref == "" {
		ref = r.DefaultBranch
	}
	commit, err := s.resolve(ctx, repo, r, ref)
	if err != nil {
		return nil, err
	}
	c, err := loadCodeOwners(ctx, s.pool, repo, commit)
	if err != nil {
		return nil, err
	}
	out := &CodeOwnersFile{Ref: ref, Rules: []CodeOwnersRule{}, Problems: []CodeOwnersProblem{}}
	if c == nil {
		return out, nil
	}
	out.Path, out.Problems = c.path, nonNil(c.problems)
	for _, rule := range c.rules {
		out.Rules = append(out.Rules, rule.info())
	}
	if path != "" {
		if rule := c.ruleOf(path); rule != nil && len(rule.names) > 0 {
			info := rule.info()
			out.Owners = &info
		}
	}
	return out, nil
}
