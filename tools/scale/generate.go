// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/authz"
	"kogge.de/kogge/internal/builds"
	"kogge.de/kogge/internal/code"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/docmodel"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/issues"
	"kogge.de/kogge/internal/platform/crypto"
	"kogge.de/kogge/internal/platform/db"
	"kogge.de/kogge/internal/platform/rank"
	"kogge.de/kogge/internal/wiki"
)

// Size is a reference size: how much data an installation holds.
type Size struct {
	Users, Projects, Issues, Spaces, Pages, CodeProjects, Repositories int
}

var sizes = map[string]Size{
	"XS": {Users: 50, Projects: 5, Issues: 2_000, Spaces: 3, Pages: 300, CodeProjects: 2, Repositories: 6},
	"S":  {Users: 500, Projects: 100, Issues: 200_000, Spaces: 20, Pages: 20_000, CodeProjects: 10, Repositories: 300},
	"M":  {Users: 2_000, Projects: 400, Issues: 1_000_000, Spaces: 80, Pages: 100_000, CodeProjects: 40, Repositories: 1_000},
}

// Manifest tells the benchmark what was generated.
type Manifest struct {
	Size       string   `json:"size"`
	Fraction   float64  `json:"fraction"`
	Token      string   `json:"token"`
	Projects   []string `json:"projects"` // keys; the first has the board with 300 cards
	Issues     int      `json:"issuesPerProject"`
	BoardID    int64    `json:"boardId"`
	Pages      []int64  `json:"pages"` // a sample
	Words      []string `json:"words"`
	Repository string   `json:"repository"` // KEY/slug with files and a pull request
	Files      []string `json:"files"`
	PullRequst int      `json:"pullRequest"`
	// The writer pushes commits that name issues and starts pipelines through the API; Git over HTTP takes the user
	// name with the token.
	Reader      string `json:"reader"`
	Writer      string `json:"writer"`
	WriterToken string `json:"writerToken"`
	// PushRepository (KEY/slug) starts empty and receives the benchmark's pushes; PipelineRepository has the
	// pipeline "handover" with one job for agents with the label scale-bench.
	PushRepository     string `json:"pushRepository"`
	PipelineRepository string `json:"pipelineRepository"`
	// AgentToken registers build agents that take jobs without an administrator's approval.
	AgentToken string `json:"agentToken"`
	// Authors are e-mail addresses of generated users, for the commits the benchmark pushes.
	Authors []string `json:"authors"`
}

// generator writes the data through the import interfaces of the modules, as a migration does.
type generator struct {
	a      *app.App
	rnd    *rand.Rand
	vocab  []string   // generated words, the most frequent first
	zipf   *rand.Zipf // word frequencies as in natural text
	size   Size
	users  []int64
	reader int64 // the user the benchmark signs in as
	writer int64 // the user the benchmark pushes and starts pipelines as
	group  int64 // the group of readers, granted everything
	log    *slog.Logger
	out    Manifest
}

const source = "scale-generator"

func generateCommand(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	sizeName := fs.String("size", "XS", "reference size: XS (a try), S or M")
	seed := fs.Uint64("seed", 1, "seed of the random data")
	fraction := fs.Float64("fraction", 1, "generate this share of the size, e.g. 0.25 (for machines smaller than the reference)")
	manifest := fs.String("manifest", "scale.json", "file for the benchmark's manifest (holds an access token)")
	_ = fs.Parse(args)
	size, ok := sizes[strings.ToUpper(*sizeName)]
	if !ok {
		return fmt.Errorf("unknown size %q", *sizeName)
	}
	if *fraction <= 0 || *fraction > 1 {
		return fmt.Errorf("the fraction must be above 0 and at most 1")
	}
	part := func(n int) int { return max(1, int(float64(n)**fraction)) }
	size = Size{Users: part(size.Users), Projects: max(2, part(size.Projects)), Issues: part(size.Issues), Spaces: part(size.Spaces),
		Pages: part(size.Pages), CodeProjects: part(size.CodeProjects), Repositories: max(1, part(size.Repositories))}
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg.BootstrapAdmin = config.BootstrapAdmin{}
	ctx := context.Background()
	a, err := app.New(ctx, cfg, log, app.Options{})
	if err != nil {
		return err
	}
	defer a.Close()
	g := &generator{a: a, rnd: rand.New(rand.NewPCG(*seed, *seed^0x5ca1e)), size: size, log: log,
		out: Manifest{Size: strings.ToUpper(*sizeName), Fraction: *fraction}}
	g.vocabulary()
	start := time.Now()
	for _, step := range []struct {
		name string
		fn   func(context.Context) error
	}{{"users", g.people}, {"issues", g.issues}, {"pages", g.pages}, {"repositories", g.repositories}} {
		t := time.Now()
		if err := step.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		log.Info("generated", "what", step.name, "took", time.Since(t).Round(time.Second))
	}
	// Statistics for the planner, as autovacuum would gather them a little later.
	if _, err := a.Pool.Exec(ctx, "ANALYZE"); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(g.out, "", "  ")
	if err := os.WriteFile(*manifest, data, 0o600); err != nil {
		return err
	}
	log.Info("done", "took", time.Since(start).Round(time.Second), "manifest", *manifest)
	return nil
}

var words = strings.Fields(`order invoice customer payment shipping address warehouse product catalogue price discount voucher
	account login password session report export import schedule delivery return refund stock supplier contract tax currency
	language search filter checkout basket newsletter review rating image upload mobile browser server database cache queue
	timeout error release version branch build deploy monitor alert backup restore migration permission role audit privacy`)

// vocabulary makes 5,000 words from syllables; texts draw them with Zipf's law, so that a search for a word of
// medium frequency matches a realistic share of the issues and pages. The benchmark searches for such words.
func (g *generator) vocabulary() {
	syllables := strings.Fields("ka lo mi re tu sa ne vi po da fe gu ri mo la ten ber sch wal dor")
	seen := map[string]bool{}
	for len(g.vocab) < 5_000 {
		var b strings.Builder
		for range 2 + g.rnd.IntN(3) {
			b.WriteString(syllables[g.rnd.IntN(len(syllables))])
		}
		if w := b.String(); !seen[w] {
			seen[w] = true
			g.vocab = append(g.vocab, w)
		}
	}
	g.zipf = rand.NewZipf(g.rnd, 1.07, 2, uint64(len(g.vocab)-1))
	g.out.Words = g.vocab[300:340]
}

func (g *generator) sentence(min, max int) string {
	n := min + g.rnd.IntN(max-min+1)
	ws := make([]string, n)
	for i := range ws {
		if g.rnd.IntN(5) == 0 {
			ws[i] = words[g.rnd.IntN(len(words))]
		} else {
			ws[i] = g.vocab[g.zipf.Uint64()]
		}
	}
	s := strings.Join(ws, " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

func (g *generator) paragraphs(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteString("\n\n")
		}
		for range 3 + g.rnd.IntN(4) {
			b.WriteString(g.sentence(6, 14) + ". ")
		}
	}
	return b.String()
}

func (g *generator) user() int64 { return g.users[g.rnd.IntN(len(g.users))] }

func (g *generator) when() time.Time {
	return time.Now().Add(-time.Duration(g.rnd.Int64N(int64(2 * 365 * 24 * time.Hour))))
}

func (g *generator) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return db.InTx(ctx, g.a.Pool, fn)
}

// people creates the authors (placeholder users), the reader the benchmark uses and the writer it pushes as, with
// access tokens, and a registration token for the benchmark's build agent.
func (g *generator) people(ctx context.Context) error {
	err := g.inTx(ctx, func(tx pgx.Tx) error {
		for i := range g.size.Users {
			name := fmt.Sprintf("user%04d", i+1)
			id, _, err := identity.ImportUser(ctx, tx, name, fmt.Sprintf("User %04d", i+1), name+"@example.com")
			if err != nil {
				return err
			}
			g.users = append(g.users, id)
			if i < 20 {
				g.out.Authors = append(g.out.Authors, name+"@example.com")
			}
		}
		reader, err := g.a.Identity.CreateLocalUser(ctx, tx, "scale-reader", "Scale reader", "scale-reader@example.com", crypto.RandomString(24))
		if err != nil {
			return err
		}
		g.reader = reader.ID
		writer, err := g.a.Identity.CreateLocalUser(ctx, tx, "scale-writer", "Scale writer", "scale-writer@example.com", crypto.RandomString(24))
		if err != nil {
			return err
		}
		g.writer = writer.ID
		// The writer registers the benchmark's agent; managing agents gives no access to content.
		if err := authz.AddGrant(ctx, tx, authz.Grant{Permission: authz.AgentManage, PrincipalType: authz.PrincipalUser,
			PrincipalID: g.writer}); err != nil {
			return err
		}
		if g.group, _, err = identity.ImportGroup(ctx, tx, "scale-readers"); err != nil {
			return err
		}
		return identity.AddMember(ctx, tx, g.group, g.reader)
	})
	if err != nil {
		return err
	}
	p, err := identity.LoadPrincipal(ctx, g.a.Pool, g.reader, identity.MethodSession)
	if err != nil {
		return err
	}
	if g.out.Token, _, err = g.a.Identity.CreateAccessToken(ctx, p, "scale benchmark", []string{identity.ScopeRead}, nil); err != nil {
		return err
	}
	w, err := identity.LoadPrincipal(ctx, g.a.Pool, g.writer, identity.MethodSession)
	if err != nil {
		return err
	}
	if g.out.WriterToken, _, err = g.a.Identity.CreateAccessToken(ctx, w, "scale benchmark (pushes)",
		[]string{identity.ScopeRead, identity.ScopeWrite}, nil); err != nil {
		return err
	}
	never := 0
	t, err := g.a.Builds.CreateAgentToken(identity.WithPrincipal(ctx, w), builds.NewAgentToken{Description: "scale benchmark",
		ExpiresInDays: &never, MaxUses: &never, AutoApprove: true})
	if err != nil {
		return err
	}
	g.out.Reader, g.out.Writer, g.out.AgentToken = "scale-reader", "scale-writer", t.Token
	return nil
}

func letters(i, n int) string {
	b := make([]byte, n)
	for k := n - 1; k >= 0; k-- {
		b[k] = byte('A' + i%26)
		i /= 26
	}
	return string(b)
}

func (g *generator) issues(ctx context.Context) error {
	var types, statuses, priorities []int64
	var done, inProgress, todo, developers int64
	err := g.inTx(ctx, func(tx pgx.Tx) error {
		im := g.a.Issues.NewImporter(tx, source)
		for _, t := range []string{"Task", "Bug", "Story"} {
			id, err := im.IssueType("type-"+t, t, "", false)
			if err != nil {
				return err
			}
			types = append(types, id)
		}
		var err error
		if todo, err = im.Status("status-todo", "To Do", issues.CategoryTodo); err != nil {
			return err
		}
		if inProgress, err = im.Status("status-progress", "In Progress", "inprogress"); err != nil {
			return err
		}
		if done, err = im.Status("status-done", "Done", issues.CategoryDone); err != nil {
			return err
		}
		statuses = []int64{todo, inProgress, done}
		for _, p := range []string{"High", "Medium", "Low"} {
			id, err := im.Priority("priority-"+p, p)
			if err != nil {
				return err
			}
			priorities = append(priorities, id)
		}
		developers, err = im.ProjectRole("Developers", "")
		return err
	})
	if err != nil {
		return err
	}
	perProject := g.size.Issues / g.size.Projects
	g.out.Issues = perProject
	for p := range g.size.Projects {
		key := "G" + letters(p, 2)
		n := perProject
		if p == 0 {
			n = 300 // the board of the first project has 300 cards
		}
		var projectID int64
		err := g.inTx(ctx, func(tx pgx.Tx) error {
			im := g.a.Issues.NewImporter(tx, source)
			var err error
			if projectID, err = im.Project(issues.ImportProject{SourceID: key, Key: key, Name: "Generated " + key, Description: g.sentence(5, 10),
				LeadID: g.user(), IssueTypes: types, Statuses: statuses, Board: issues.BoardKanban}); err != nil {
				return err
			}
			if p == 0 {
				if g.out.BoardID, _, err = im.BoardOf(key); err != nil {
					return err
				}
			}
			return im.RoleMember(projectID, developers, "group", g.group)
		})
		if err != nil {
			return err
		}
		g.out.Projects = append(g.out.Projects, key)
		ranks := rank.Spread(n)
		for start := 0; start < n; start += 500 {
			err := g.inTx(ctx, func(tx pgx.Tx) error {
				im := g.a.Issues.NewImporter(tx, source)
				for i := start; i < min(start+500, n); i++ {
					created := g.when()
					status := done
					switch r := g.rnd.IntN(10); {
					case p == 0 || r < 2:
						status = []int64{todo, inProgress}[g.rnd.IntN(2)]
					case r < 3:
						status = inProgress
					}
					var resolved *time.Time
					if status == done {
						t := created.Add(time.Duration(g.rnd.IntN(30*24)) * time.Hour)
						resolved = &t
					}
					var labels []string
					for range g.rnd.IntN(3) {
						labels = append(labels, words[g.rnd.IntN(30)])
					}
					issueKey := key + "-" + strconv.Itoa(i+1)
					id, err := im.Issue(issues.ImportIssue{SourceID: issueKey, Key: issueKey, ProjectID: projectID, TypeID: types[g.rnd.IntN(len(types))],
						StatusID: status, PriorityID: priorities[g.rnd.IntN(len(priorities))], Summary: g.sentence(4, 10),
						Description: docmodel.FromMarkdown(g.paragraphs(1 + g.rnd.IntN(3))), ReporterID: g.user(), CreatorID: g.user(),
						AssigneeID: g.user(), Created: created, Updated: created.Add(time.Hour), Resolved: resolved, Labels: labels, Rank: ranks[i]})
					if err != nil {
						return err
					}
					for c := range g.rnd.IntN(3) {
						at := created.Add(time.Duration(c+1) * time.Hour)
						if err := im.Comment(id, issueKey+"-c"+strconv.Itoa(c), g.user(), at, at, docmodel.FromMarkdown(g.paragraphs(1)), nil); err != nil {
							return err
						}
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		err = g.inTx(ctx, func(tx pgx.Tx) error { return g.a.Issues.NewImporter(tx, source).Finish(ctx, projectID) })
		if err != nil {
			return err
		}
		g.log.Info("project", "key", key, "issues", n)
	}
	return nil
}

func (g *generator) pages(ctx context.Context) error {
	perSpace := g.size.Pages / g.size.Spaces
	for s := range g.size.Spaces {
		key := "W" + letters(s, 2)
		var spaceID, home int64
		var ids []int64
		err := g.inTx(ctx, func(tx pgx.Tx) error {
			im := g.a.Wiki.NewImporter(tx, source)
			var err error
			if spaceID, err = im.Space(wiki.ImportSpace{SourceID: key, Key: key, Name: "Generated " + key, Description: g.sentence(5, 10),
				CreatorID: g.user(), Created: g.when()}); err != nil {
				return err
			}
			return im.SpacePermissions(spaceID, []wiki.ImportGrant{{Permission: wiki.PermView, HolderType: "group", HolderID: g.group}})
		})
		if err != nil {
			return err
		}
		for start := 0; start < perSpace; start += 200 {
			err := g.inTx(ctx, func(tx pgx.Tx) error {
				im := g.a.Wiki.NewImporter(tx, source)
				for i := start; i < min(start+200, perSpace); i++ {
					parent := int64(0)
					if i > 0 {
						parent = ids[g.rnd.IntN(min(len(ids), 1+i/4))] // a tree: early pages get the children
					}
					at := g.when()
					id, _, err := im.Page(wiki.ImportPage{SourceID: key + "-" + strconv.Itoa(i), SpaceID: spaceID, ParentID: parent, Kind: "page",
						Status: "current", Title: g.sentence(2, 6) + " " + strconv.Itoa(i), CreatorID: g.user(), Created: at, UpdatedBy: g.user(), Updated: at})
					if err != nil {
						return err
					}
					body := "## " + g.sentence(2, 5) + "\n\n" + g.paragraphs(3+g.rnd.IntN(5)) + "\n\n- " + g.sentence(3, 6) + "\n- " + g.sentence(3, 6)
					if err := im.SetBody(id, docmodel.FromMarkdown(body), 1, g.user(), at, ""); err != nil {
						return err
					}
					ids = append(ids, id)
					if i == 0 {
						home = id
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if err := g.inTx(ctx, func(tx pgx.Tx) error { return g.a.Wiki.NewImporter(tx, source).FinishSpace(ctx, spaceID, home) }); err != nil {
			return err
		}
		for range 20 {
			g.out.Pages = append(g.out.Pages, ids[g.rnd.IntN(len(ids))])
		}
		g.log.Info("space", "key", key, "pages", perSpace)
	}
	return nil
}

// repositories creates empty repositories, one with history and a pull request of 2,000 changed lines, and two for
// the benchmark's pushes and pipelines, which the writer may change.
func (g *generator) repositories(ctx context.Context) error {
	perProject := g.size.Repositories / g.size.CodeProjects
	var target, pipelines int64
	for p := range g.size.CodeProjects {
		key := "C" + letters(p, 2)
		err := g.inTx(ctx, func(tx pgx.Tx) error {
			im := g.a.Code.NewImporter(tx, source)
			projectID, err := im.Project(code.ImportProject{SrcID: key, Key: key, Name: "Generated " + key})
			if err != nil {
				return err
			}
			if err := im.Grant(projectID, 0, code.PermRead, "group", g.group); err != nil {
				return err
			}
			for r := range perProject {
				slug := fmt.Sprintf("repo-%03d", r+1)
				id, _, err := im.Repository(projectID, code.ImportRepository{SrcID: key + "/" + slug, Slug: slug, Name: slug, DefaultBranch: "main"})
				if err != nil {
					return err
				}
				if p == 0 && r == 0 {
					target = id
					g.out.Repository = key + "/" + slug
				}
			}
			if p != 0 {
				return nil
			}
			if err := im.Grant(projectID, 0, code.PermWrite, "user", g.writer); err != nil {
				return err
			}
			if _, _, err := im.Repository(projectID, code.ImportRepository{SrcID: key + "/bench-push", Slug: "bench-push",
				Name: "bench-push", DefaultBranch: "main"}); err != nil {
				return err
			}
			if pipelines, _, err = im.Repository(projectID, code.ImportRepository{SrcID: key + "/bench-pipelines",
				Slug: "bench-pipelines", Name: "bench-pipelines", DefaultBranch: "main"}); err != nil {
				return err
			}
			g.out.PushRepository, g.out.PipelineRepository = key+"/bench-push", key+"/bench-pipelines"
			return nil
		})
		if err != nil {
			return err
		}
	}
	mirror, files, featureSHA, mainSHA, err := g.history(ctx)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(mirror))
	if err := g.a.Code.LoadMirror(ctx, target, mirror, "main"); err != nil {
		return err
	}
	g.out.Files = files
	pipelineMirror, err := g.pipelineHistory(ctx)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(pipelineMirror))
	if err := g.a.Code.LoadMirror(ctx, pipelines, pipelineMirror, "main"); err != nil {
		return err
	}
	return g.inTx(ctx, func(tx pgx.Tx) error {
		_, err := g.a.Code.NewImporter(tx, source).PullRequest(target, code.ImportPullRequest{SrcID: "pr-1", Number: 1,
			Title: "A change of 2,000 lines", AuthorID: g.user(), Source: "feature", Target: "main", SourceSHA: featureSHA, TargetSHA: mainSHA,
			State: code.StateOpen, Created: time.Now(), Updated: time.Now()})
		g.out.PullRequst = 1
		return err
	})
}

// benchPipelines is the pipeline file of the benchmark's pipeline repository: one job that does nothing, on agents
// with the label scale-bench, started through the API.
const benchPipelines = `version: 1
pipelines:
  handover:
    name: Hand-over
    on:
      api: true
    stages:
      - name: Run
        jobs:
          - name: noop
            runs-on: [shell, scale-bench]
            steps:
              - run: "true"
`

// pipelineHistory builds a repository with the benchmark's pipeline file.
func (g *generator) pipelineHistory(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp("", "kogge-scale-")
	if err != nil {
		return "", err
	}
	mirror := filepath.Join(dir, "repo.git")
	if out, err := exec.CommandContext(ctx, "git", "init", "-q", "--bare", mirror).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git init: %v: %s", err, out)
	}
	var stream bytes.Buffer
	fmt.Fprintf(&stream, "blob\nmark :1\ndata %d\n%s\n", len(benchPipelines), benchPipelines)
	fmt.Fprintf(&stream, "commit refs/heads/main\nmark :2\ncommitter Generator <gen@example.com> %d +0000\ndata 9\nPipeline\n"+
		"M 100644 :1 .kogge/pipelines.yml\n", time.Now().Unix())
	stream.WriteString("done\n")
	cmd := exec.CommandContext(ctx, "git", "--git-dir", mirror, "fast-import", "--quiet", "--done")
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git fast-import: %v: %s", err, out)
	}
	return mirror, nil
}

// history builds a repository with 200 files and a branch that changes 2,000 lines with git fast-import.
func (g *generator) history(ctx context.Context) (mirror string, files []string, feature, main string, err error) {
	dir, err := os.MkdirTemp("", "kogge-scale-")
	if err != nil {
		return "", nil, "", "", err
	}
	mirror = filepath.Join(dir, "repo.git")
	if out, err := exec.CommandContext(ctx, "git", "init", "-q", "--bare", mirror).CombinedOutput(); err != nil {
		return "", nil, "", "", fmt.Errorf("git init: %v: %s", err, out)
	}
	var stream bytes.Buffer
	blob := func(mark int, content string) {
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n%s\n", mark, len(content), content)
	}
	fileText := func(f, version int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "package gen\n\n// File %d, version %d.\n", f, version)
		for l := range 100 {
			fmt.Fprintf(&b, "var v%d_%d = %q // %s\n", f, l, words[(f+l+version)%len(words)], g.sentence(3, 6))
		}
		return b.String()
	}
	for f := range 200 {
		files = append(files, fmt.Sprintf("pkg/gen/file%03d.go", f))
		blob(f+1, fileText(f, 0))
	}
	when := time.Now().Add(-48 * time.Hour).Unix()
	fmt.Fprintf(&stream, "commit refs/heads/main\nmark :1000\ncommitter Generator <gen@example.com> %d +0000\ndata 14\nInitial import\n", when)
	for f := range files {
		fmt.Fprintf(&stream, "M 100644 :%d %s\n", f+1, files[f])
	}
	// The feature branch changes 20 files completely: 2,000 changed lines (added and removed).
	for f := range 20 {
		blob(2000+f, fileText(f, 1))
	}
	fmt.Fprintf(&stream, "commit refs/heads/feature\nmark :1001\ncommitter Generator <gen@example.com> %d +0000\ndata 13\nChange things\nfrom :1000\n", when+3600)
	for f := range 20 {
		fmt.Fprintf(&stream, "M 100644 :%d %s\n", 2000+f, files[f])
	}
	stream.WriteString("done\n")
	cmd := exec.CommandContext(ctx, "git", "--git-dir", mirror, "fast-import", "--quiet", "--done")
	cmd.Stdin = &stream
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, "", "", fmt.Errorf("git fast-import: %v: %s", err, out)
	}
	rev := func(ref string) string {
		out, _ := exec.CommandContext(ctx, "git", "--git-dir", mirror, "rev-parse", ref).Output()
		return strings.TrimSpace(string(out))
	}
	return mirror, files, rev("feature"), rev("main"), nil
}
