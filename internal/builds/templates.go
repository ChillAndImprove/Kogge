// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package builds

import (
	"context"
	"embed"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/identity"
	"kogge.de/kogge/internal/platform/apperr"
	"kogge.de/kogge/internal/platform/audit"
	"kogge.de/kogge/internal/platform/db"
)

// Pipeline templates: pipeline files to start a repository's pipelines from — built-in ones for
// common kinds of projects, recognised by their files, and the organisation's own, which Helling's
// administrators maintain.

//go:embed templates/*.yml
var templateFiles embed.FS

// starter is a built-in template; the files of a commit's top that suggest it (globs).
type starter struct {
	id     string
	detect []string
}

var starters = []starter{
	{"go", []string{"go.mod"}},
	{"node", []string{"package.json"}},
	{"maven", []string{"pom.xml"}},
	{"gradle", []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"}},
	{"python", []string{"pyproject.toml", "requirements.txt", "setup.py"}},
	{"dotnet", []string{"*.sln", "*.csproj", "*.fsproj", "global.json"}},
	{"container", []string{"Dockerfile", "Containerfile"}},
	{"minimal", nil},
}

// Template is a pipeline template.
type PipelineTemplate struct {
	ID          string `json:"id" doc:"go, node, … for built-in templates; org-<id> for those of the organisation"`
	Builtin     bool   `json:"builtin"`
	Name        string `json:"name,omitempty" doc:"Name of a template of the organisation (built-in ones are named by the client)"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
	// Recommended says that the repository asked about has files of the kind of project the template is for.
	Recommended bool       `json:"recommended"`
	Updated     *time.Time `json:"updated,omitempty"`
}

// PipelineTemplateInput creates or changes a template of the organisation.
type PipelineTemplateInput struct {
	Name        string `json:"name" minLength:"1" maxLength:"100"`
	Description string `json:"description,omitempty" maxLength:"1000"`
	Content     string `json:"content" minLength:"1" maxLength:"262144" doc:"A valid pipeline file"`
}

// Templates lists the built-in templates and those of the organisation to signed-in users. With a repository the
// user may read, the templates for the kind of project at the top of its default branch are recommended.
func (s *Service) Templates(ctx context.Context, p *identity.Principal, key, slug string) ([]PipelineTemplate, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	var names []string
	if key != "" && slug != "" {
		repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
		if err != nil {
			return nil, err
		}
		if commit, err := s.repos.Resolve(ctx, repo.ID, repo.DefaultBranch); err == nil {
			names, _ = s.repos.RootNames(ctx, repo.ID, commit)
		}
	}
	out := []PipelineTemplate{}
	for _, st := range starters {
		content, err := templateFiles.ReadFile("templates/" + st.id + ".yml")
		if err != nil {
			return nil, err
		}
		t := PipelineTemplate{ID: st.id, Builtin: true, Content: string(content)}
		for _, n := range names {
			for _, pattern := range st.detect {
				if ok, _ := path.Match(pattern, n); ok {
					t.Recommended = true
				}
			}
		}
		out = append(out, t)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, content, updated_at FROM pipeline_templates
		ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	org, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (PipelineTemplate, error) {
		var t PipelineTemplate
		var id int64
		var updated time.Time
		err := r.Scan(&id, &t.Name, &t.Description, &t.Content, &updated)
		t.ID, t.Updated = "org-"+strconv.FormatInt(id, 10), &updated
		return t, err
	})
	if err != nil {
		return nil, err
	}
	// The organisation's templates come first: they are what it wants its repositories to use.
	return append(org, out...), nil
}

// orgTemplateID parses org-<id>.
func orgTemplateID(id string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "org-"), 10, 64)
	if !strings.HasPrefix(id, "org-") || err != nil || n <= 0 {
		return 0, apperr.NotFound("template.not-found", "The template does not exist.")
	}
	return n, nil
}

// SaveTemplate creates (id "") or changes a template of the organisation; its content must be a valid pipeline file.
func (s *Service) SaveTemplate(ctx context.Context, id string, in PipelineTemplateInput) (*PipelineTemplate, error) {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return nil, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, apperr.Invalid("template.name", "The template needs a name.")
	}
	if _, err := spec.Parse([]byte(in.Content)); err != nil {
		return nil, invalidPipelineFile(err)
	}
	var tid int64
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		action := "pipeline-template.create"
		if id == "" {
			err := tx.QueryRow(ctx, `INSERT INTO pipeline_templates (name, description, content, created_by, updated_by)
				VALUES ($1, $2, $3, $4, $4) RETURNING id`, in.Name, in.Description, in.Content, p.UserID()).Scan(&tid)
			if err != nil {
				return err
			}
		} else {
			if tid, err = orgTemplateID(id); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE pipeline_templates SET name = $2, description = $3, content = $4, updated_by = $5,
				updated_at = now() WHERE id = $1`, tid, in.Name, in.Description, in.Content, p.UserID())
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return apperr.NotFound("template.not-found", "The template does not exist.")
			}
			action = "pipeline-template.update"
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: action, TargetType: "pipeline-template",
			TargetID: itoa(tid), Details: map[string]any{"name": in.Name}})
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return nil, apperr.Conflict("template.name-taken", "A template named %s exists already.", in.Name)
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &PipelineTemplate{ID: "org-" + itoa(tid), Name: in.Name, Description: in.Description, Content: in.Content, Updated: &now}, nil
}

// DeleteTemplate deletes a template of the organisation.
func (s *Service) DeleteTemplate(ctx context.Context, id string) error {
	p, err := s.requireAgents(ctx)
	if err != nil {
		return err
	}
	tid, err := orgTemplateID(id)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		if err := tx.QueryRow(ctx, "DELETE FROM pipeline_templates WHERE id = $1 RETURNING name", tid).Scan(&name); err != nil {
			if db.IsNoRows(err) {
				return apperr.NotFound("template.not-found", "The template does not exist.")
			}
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID(), Action: "pipeline-template.delete",
			TargetType: "pipeline-template", TargetID: itoa(tid), Details: map[string]any{"name": name}})
	})
}

// Problem is a mistake in a pipeline file, where the line and column are known.
type Problem struct {
	File    string `json:"file,omitempty" doc:"The included file with the mistake; empty: the pipeline file"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

// CheckedPipeline is a pipeline a valid file defines.
type CheckedPipeline struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	File     string   `json:"file,omitempty" doc:"The included file that defines it"`
	Triggers []string `json:"triggers" doc:"push, pull-request, schedule, manual, api, pipeline"`
	Stages   int      `json:"stages"`
	Jobs     int      `json:"jobs"`
}

// Check is the result of checking a pipeline file.
type Check struct {
	Valid     bool              `json:"valid"`
	Problems  []Problem         `json:"problems"`
	Warnings  []Problem         `json:"warnings" doc:"Doubtful parts that do not keep the file from running"`
	Pipelines []CheckedPipeline `json:"pipelines"`
	Includes  []spec.Included   `json:"includes" doc:"The files the file includes, as read for the check"`
	// Unresolved: the file includes others, which were not read (no repository was given).
	Unresolved bool `json:"unresolved,omitempty" doc:"The file includes others, which were not read: no repository was given"`
}

// CheckInput is a pipeline file to check, with the repository and ref it is for: their included files are read,
// with the caller's permissions.
type CheckInput struct {
	Content    string `json:"content" maxLength:"262144"`
	Repository string `json:"repository,omitempty" maxLength:"200" doc:"KEY/slug of the repository the file is for"`
	Ref        string `json:"ref,omitempty" maxLength:"255" doc:"Its branch, tag or commit; default: the default branch"`
}

// CheckFile validates a pipeline file and tells what it defines. With a repository, the files it includes are read
// with the caller's permissions, as for a run the caller starts.
func (s *Service) CheckFile(ctx context.Context, p *identity.Principal, in CheckInput) (*Check, error) {
	if !p.Authenticated() {
		return nil, apperr.Unauthorized("auth.required", "Please sign in.")
	}
	out := &Check{Problems: []Problem{}, Warnings: []Problem{}, Pipelines: []CheckedPipeline{}, Includes: []spec.Included{}}
	var reader spec.Reader
	if in.Repository != "" {
		key, slug, _ := strings.Cut(in.Repository, "/")
		repo, _, err := s.repoFor(ctx, p, key, slug, LevelRead)
		if err != nil {
			return nil, err
		}
		ref := in.Ref
		if ref == "" {
			ref = "refs/heads/" + repo.DefaultBranch
		}
		commit, err := s.repos.Resolve(ctx, repo.ID, ref)
		if err != nil {
			return nil, apperr.Invalidf("run.ref", "The ref %s does not exist.", in.Ref)
		}
		reader = s.readerFor(p, repo, commit)
	}
	f, err := spec.Load(ctx, []byte(in.Content), reader)
	var mistakes spec.Errors
	if err != nil && !errors.As(err, &mistakes) {
		return nil, err
	}
	if err != nil {
		out.Problems = problems(err)
		return out, nil
	}
	out.Valid, out.Unresolved, out.Includes, out.Warnings = true, f.Unresolved, nonNil(f.Includes), nonNil(problemsOf(f.Warnings))
	for _, pl := range f.Pipelines {
		c := CheckedPipeline{ID: pl.ID, Name: pl.Name, File: pl.File, Stages: len(pl.Stages), Triggers: []string{}}
		if pl.On.Push != nil {
			c.Triggers = append(c.Triggers, "push")
		}
		if pl.On.PullRequest != nil {
			c.Triggers = append(c.Triggers, "pull-request")
		}
		if len(pl.On.Schedule) > 0 {
			c.Triggers = append(c.Triggers, "schedule")
		}
		if pl.On.Manual != nil {
			c.Triggers = append(c.Triggers, "manual")
		}
		if pl.On.API {
			c.Triggers = append(c.Triggers, "api")
		}
		if len(pl.On.Pipeline) > 0 {
			c.Triggers = append(c.Triggers, "pipeline")
		}
		for _, st := range pl.Stages {
			c.Jobs += len(st.Jobs)
		}
		out.Pipelines = append(out.Pipelines, c)
	}
	return out, nil
}

func problemsOf(list spec.Errors) []Problem {
	out := make([]Problem, len(list))
	for i, e := range list {
		out[i] = Problem{File: e.File, Line: e.Line, Column: e.Column, Message: e.Message}
	}
	return out
}

func problems(err error) []Problem {
	var list spec.Errors
	if errors.As(err, &list) {
		return problemsOf(list)
	}
	var one spec.Error
	if errors.As(err, &one) {
		return problemsOf(spec.Errors{one})
	}
	return []Problem{{Message: err.Error()}}
}

// invalidPipelineFile reports the mistakes of a pipeline file as field errors of its content.
func invalidPipelineFile(err error) error {
	list := problems(err)
	errs := make([]apperr.FieldError, len(list))
	for i, p := range list {
		errs[i] = apperr.FieldError{Location: "body.content", Message: spec.Error{File: p.File, Line: p.Line, Column: p.Column, Message: p.Message}.Error()}
	}
	return apperr.Invalidf("template.invalid", "The template is no valid pipeline file: %s", list[0].Message).WithFields(errs...)
}
