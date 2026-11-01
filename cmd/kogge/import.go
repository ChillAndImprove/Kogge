// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"kogge.de/kogge/internal/app"
	"kogge.de/kogge/internal/config"
	"kogge.de/kogge/internal/importer"
)

const importUsage = `Usage:
  kogge import jira|confluence|bitbucket|bamboo STEP [flags]

The steps of a migration, for one product at a time:
  extract   read the source through its REST API into the work directory (no database needed); an interrupted
            extraction resumes where it stopped (--restart begins afresh)
  analyse   report what the extraction holds, how it will be translated, what cannot be translated exactly and
            what needs a decision: PRODUCT-analysis.html and .json in the work directory; with --database-url,
            also how users and keys match the installation the load will go into
  load      write the work directory into Kogge (repeatable: what earlier loads wrote is updated); an entity that
            cannot be loaded is reported and the others are loaded (--stop-on-error stops at the first one)
  validate  compare the load with the extraction (counts, attachment checksums, links, sample renderings,
            permissions, history): PRODUCT-validation.html and .json; exits with status 1 if a check fails
  all       extract, analyse, load and validate

load, validate and all read the configuration of the server (KOGGE_DATABASE_URL, KOGGE_DATA_DIR, ...).
load exits with status 1 when entities could not be loaded; the report lists them.

Flags:
`

var importSteps = []string{"extract", "analyse", "analyze", "load", "validate", "all"}

// importCommand runs a migration from Jira, Confluence, Bitbucket or Bamboo Data Center.
func importCommand(args []string) error {
	if len(args) < 2 || !slices.Contains([]string{"jira", "confluence", "bitbucket", "bamboo"}, args[0]) ||
		!slices.Contains(importSteps, args[1]) {
		fmt.Fprint(os.Stderr, importUsage)
		return errors.New("import: expected jira|confluence|bitbucket|bamboo and extract|analyse|load|validate|all")
	}
	product, step := args[0], args[1]
	if step == "analyze" {
		step = "analyse"
	}
	fs := flag.NewFlagSet("import "+product, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, importUsage)
		fs.PrintDefaults()
	}
	sourceURL := fs.String("url", "", "base URL of the source, e.g. https://jira.example.com")
	tokenFile := fs.String("token-file", "", "file with a personal access token of an administrator (or KOGGE_IMPORT_TOKEN)")
	workdir := fs.String("workdir", "kogge-import", "work directory for the extracted data and the reports")
	keys := fs.String("keys", "", "comma-separated project or space keys (default: all)")
	noFiles := fs.Bool("skip-attachments", false, "leave attachment files (bitbucket: LFS objects) out of the extraction (trial runs)")
	history := fs.Bool("history", false, "confluence: also migrate earlier versions of pages")
	jiraURL := fs.String("jira-url", "", "confluence: base URL of the migrated Jira; links to its issues become issue references")
	gitUser := fs.String("git-user", "", "bitbucket: user name for git, with the token as password (default: the token as bearer token)")
	noGit := fs.Bool("skip-git", false, "bitbucket: leave the repositories' history out of the extraction (trial runs)")
	concurrency := fs.Int("concurrency", 4, "extract: requests for details and files at a time; validate: files hashed at a time")
	restart := fs.Bool("restart", false, "extract: begin afresh instead of resuming an interrupted extraction")
	stopOnError := fs.Bool("stop-on-error", false, "load: stop at the first entity that cannot be loaded")
	language := fs.String("lang", "en", "analyse, validate: language of the reports (en or de)")
	databaseURL := fs.String("database-url", "", "analyse: database of the Kogge installation to match users and keys against (read only)")
	sampleSize := fs.Int("sample", 25, "validate: entities per spot check (renderings, permissions, history)")
	repos := map[string]string{}
	fs.Func("repo", "bamboo: PLAN-KEY=KEY/slug proposes the plan's pipeline in that repository (repeatable)", func(v string) error {
		plan, repo, ok := strings.Cut(v, "=")
		if !ok || !strings.Contains(repo, "/") {
			return errors.New("expected PLAN-KEY=KEY/slug")
		}
		repos[strings.TrimSpace(plan)] = strings.TrimSpace(repo)
		return nil
	})
	_ = fs.Parse(args[2:])
	lang, err := importer.ParseLanguage(*language)
	if err != nil {
		return err
	}

	token := strings.TrimSpace(os.Getenv("KOGGE_IMPORT_TOKEN"))
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(b))
	}
	if (step == "extract" || step == "all") && (*sourceURL == "" || token == "") {
		return errors.New("import: --url and a token (--token-file or KOGGE_IMPORT_TOKEN) are required for the extraction")
	}
	var selected []string
	for _, k := range strings.Split(*keys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			selected = append(selected, k)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	type migration interface {
		Extract(context.Context) error
		Analyse(context.Context, *importer.Target, string) (*importer.Analysis, error)
		Load(context.Context, importer.Loader) (*importer.Report, error)
		Validate(context.Context, importer.Loader, importer.ValidateOptions) (*importer.Validation, error)
	}
	var m migration
	switch product {
	case "jira":
		m = importer.NewJira(importer.JiraOptions{URL: *sourceURL, Token: token, Projects: selected, Workdir: *workdir,
			SkipAttachments: *noFiles, Concurrency: *concurrency, Restart: *restart}, logger)
	case "confluence":
		m = importer.NewConfluence(importer.ConfluenceOptions{URL: *sourceURL, Token: token, Spaces: selected, Workdir: *workdir,
			JiraURL: *jiraURL, History: *history, NoFiles: *noFiles, Concurrency: *concurrency, Restart: *restart}, logger)
	case "bitbucket":
		m = importer.NewBitbucket(importer.BitbucketOptions{URL: *sourceURL, Token: token, Projects: selected, Workdir: *workdir,
			GitUser: *gitUser, SkipGit: *noGit, SkipLFS: *noFiles, Concurrency: *concurrency, Restart: *restart}, logger)
	default:
		m = importer.NewBamboo(importer.BambooOptions{URL: *sourceURL, Token: token, Projects: selected, Workdir: *workdir,
			Repositories: repos, Restart: *restart}, logger)
	}
	if step == "extract" || step == "all" {
		started := time.Now()
		if err := m.Extract(ctx); err != nil {
			return fmt.Errorf("extraction: %w (run the extraction again: it resumes where it stopped)", err)
		}
		logger.Info("extraction finished", "workdir", *workdir, "duration", time.Since(started).Round(time.Second))
	}
	if step == "extract" {
		return nil
	}
	if step == "analyse" {
		var target *importer.Target
		if *databaseURL != "" {
			if target, err = importer.OpenTarget(ctx, *databaseURL); err != nil {
				return fmt.Errorf("the target database: %w", err)
			}
			defer target.Close()
		}
		return analyse(ctx, m.Analyse, target, lang, *workdir, product)
	}

	cfg, appLogger, err := load()
	if err != nil {
		return err
	}
	quiet := *cfg
	quiet.BootstrapAdmin = config.BootstrapAdmin{}
	a, err := app.New(ctx, &quiet, appLogger, app.Options{})
	if err != nil {
		return err
	}
	defer a.Close()
	loader := importer.Loader{Pool: a.Pool, Issues: a.Issues, Wiki: a.Wiki, Code: a.Code, Builds: a.Builds, Log: logger,
		StopOnError: *stopOnError}
	if step == "all" {
		target, err := importer.NewTarget(ctx, a.Pool)
		if err != nil {
			return err
		}
		err = analyse(ctx, m.Analyse, target, lang, *workdir, product)
		target.Close()
		if err != nil {
			return err
		}
	}
	failures := 0
	if step == "load" || step == "all" {
		rep, err := m.Load(ctx, loader)
		if rep != nil {
			printReport(rep)
			fmt.Printf("\nThe full report is in %s.\n", filepath.Join(*workdir, product+"-report.json"))
		}
		if err != nil {
			return fmt.Errorf("load: %w (the load can be run again: it continues where it is)", err)
		}
		failures = rep.Counts["failures"]
	}
	if step == "validate" || step == "all" {
		v, err := m.Validate(ctx, loader, importer.ValidateOptions{Language: lang, Sample: *sampleSize, DataDir: cfg.DataDir,
			Concurrency: *concurrency})
		if err != nil {
			return fmt.Errorf("validation: %w", err)
		}
		printValidation(v, filepath.Join(*workdir, product+"-validation.html"))
		if v.Failed > 0 {
			return fmt.Errorf("validation: %d checks failed", v.Failed)
		}
	}
	if failures > 0 {
		return fmt.Errorf("load: %d entities could not be loaded; the report lists them", failures)
	}
	return nil
}

func analyse(ctx context.Context, fn func(context.Context, *importer.Target, string) (*importer.Analysis, error), target *importer.Target,
	lang, workdir, product string) error {
	a, err := fn(ctx, target, lang)
	if err != nil {
		return fmt.Errorf("analysis: %w", err)
	}
	fmt.Printf("\nAnalysis of %s\n", a.Source)
	for _, f := range a.Counts {
		fmt.Printf("  %-40s %d\n", f.Label, f.Value)
	}
	severities := map[string]int{}
	for _, d := range a.Decisions {
		severities[d.Severity]++
	}
	fmt.Printf("  decisions: %d high, %d medium, %d low; not translatable exactly: %d kinds\n", severities["high"], severities["medium"],
		severities["low"], len(a.Untranslatable))
	if a.Estimate != nil {
		fmt.Printf("  estimated load: %s\n", time.Duration(a.Estimate.Seconds*float64(time.Second)).Round(time.Minute))
	}
	fmt.Printf("\nThe report is in %s (and .json).\n", filepath.Join(workdir, product+"-analysis.html"))
	return nil
}

func printValidation(v *importer.Validation, page string) {
	fmt.Printf("\nValidation of %s: %d passed, %d warnings, %d failed\n", v.Source, v.Passed, v.Warnings, v.Failed)
	for _, c := range v.Checks {
		if c.Status != importer.Pass {
			fmt.Printf("  %-5s %s: %s\n", c.Status, c.Title, c.Message)
		}
	}
	fmt.Printf("\nThe report is in %s (and .json).\n", page)
}

func printReport(r *importer.Report) {
	fmt.Printf("\nMigration from %s (%s)\n", r.Source, r.Finished.Sub(r.Started).Round(time.Second))
	names := make([]string, 0, len(r.Counts))
	for k := range r.Counts {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		fmt.Printf("  %-20s %d\n", k, r.Counts[k])
	}
	fmt.Printf("  users matched: %d, placeholders: %d\n", len(r.UsersMatched), len(r.Placeholders))
	if len(r.Unsupported) > 0 {
		fmt.Println("\nNot converted (kept as placeholders or left out):")
		keys := make([]string, 0, len(r.Unsupported))
		for k := range r.Unsupported {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return r.Unsupported[b] - r.Unsupported[a] })
		for i, k := range keys {
			if i == 20 {
				fmt.Printf("  … and %d more\n", len(keys)-20)
				break
			}
			fmt.Printf("  %5d  %s\n", r.Unsupported[k], k)
		}
	}
	if len(r.Failures) > 0 {
		fmt.Printf("\n%d entities could not be loaded, the first ones:\n", r.Counts["failures"])
		for i, f := range r.Failures {
			if i == 10 {
				break
			}
			fmt.Printf("  %s %s: %s\n", f.Entity, cmp.Or(f.Name, f.SourceID), f.Error)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Printf("\n%d warnings, the first ones:\n", len(r.Warnings))
		for i, w := range r.Warnings {
			if i == 10 {
				break
			}
			fmt.Println("  " + w)
		}
	}
}
