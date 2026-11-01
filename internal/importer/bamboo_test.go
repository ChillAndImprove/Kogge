// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/spec"
	"kogge.de/kogge/internal/importer"
	"kogge.de/kogge/internal/testutil/apitest"
)

const bambooToken = "bamboo-test-token"

const bambooPlanSpecs = `---
version: 2
plan:
  project-key: SHOP
  key: WEB
  name: Web shop
stages:
- Build:
    jobs:
    - Compile
Compile:
  key: COMP
  tasks:
  - checkout
  - script:
      interpreter: SHELL
      scripts:
      - make build VERSION=${bamboo.buildNumber} TEAM=$bamboo_team
  - any-task:
      plugin-key: com.example:upload
      description: Upload
  final-tasks:
  - test-parser: junit
  artifacts:
  - name: dist
    location: dist
    pattern: '**'
  docker:
    image: golang:1.27
repositories:
- web-repo:
    scope: global
triggers:
- bitbucket-server-trigger
variables:
  deploy.password: BAMSCRT@0@0@xyz
  go.flags: -mod=readonly
---
version: 2
plan:
  key: SHOP-WEB
plan-permissions:
- users:
  - jdoe
  permissions:
  - view
`

const bambooDeploySpecs = `---
version: 2
deployment:
  name: Web shop deployment
  source-plan: SHOP-WEB
release-naming: release-${bamboo.buildNumber}
environments:
- Staging
- Production
Staging:
  triggers:
  - build-success
  tasks:
  - artifact-download:
      artifacts:
      - name: dist
  - script: ./deploy.sh ${bamboo.target.host}
Production:
  tasks:
  - script: ./deploy.sh ${bamboo.target.host}
`

const bambooInfraSpecs = `---
version: 2
plan:
  project-key: OPS
  key: INFRA
  name: Infrastructure
stages:
- Plan:
    jobs:
    - Terraform
Terraform:
  tasks:
  - script: terraform plan
repositories:
- infra:
    type: git
    url: https://git.example.com/infra.git
`

func fakeBamboo(t *testing.T) *httptest.Server {
	specsJSON := func(code string) string {
		b, _ := json.Marshal(map[string]any{"spec": map[string]string{"code": code}})
		return string(b)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+bambooToken {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		switch p := r.URL.Path; p {
		case "/rest/api/latest/info":
			fmt.Fprint(w, `{"version":"10.2.1","edition":"","buildDate":"2026-01-01T00:00:00.000Z","buildNumber":"100201","state":"RUNNING"}`)
		case "/rest/api/latest/repository":
			fmt.Fprint(w, `{"size":1,"startIndex":0,"maxResult":25,"searchResults":[{"id":1,"name":"web-repo","url":"https://bitbucket.example.com/scm/shop/web.git"},
				{"id":2,"name":"tools-repo","url":"ssh://git@bitbucket.example.com:7999/ops/tools.git"}]}`)
		case "/rest/admin/latest/globalVariables":
			fmt.Fprint(w, `[{"id":1,"name":"artifactory.url","value":"https://repo.example.com"},{"id":2,"name":"artifactory.password","value":"********","encrypted":true}]`)
		case "/rest/api/latest/project":
			// One project per page, whatever the client asks for.
			projects := []string{`{"key":"SHOP","name":"Shop","description":""}`, `{"key":"OPS","name":"Operations","description":""}`}
			start, _ := strconv.Atoi(q.Get("start-index"))
			start = min(start, len(projects))
			end := min(start+1, len(projects))
			fmt.Fprintf(w, `{"expand":"projects","projects":{"size":%d,"start-index":%d,"max-result":1,"project":[%s]}}`, len(projects), start,
				strings.Join(projects[start:end], ","))
		case "/rest/api/latest/project/SHOP/variables":
			fmt.Fprint(w, `[{"name":"team","value":"shop"}]`)
		case "/rest/api/latest/project/OPS/variables":
			http.Error(w, "not found", http.StatusNotFound)
		case "/rest/api/latest/project/SHOP":
			fmt.Fprint(w, `{"key":"SHOP","name":"Shop","plans":{"size":2,"start-index":0,"max-result":500,"plan":[
				{"key":"SHOP-WEB","name":"Shop - Web shop","shortName":"Web shop","shortKey":"WEB","enabled":true},
				{"key":"SHOP-OLD","name":"Shop - Old","shortName":"Old","shortKey":"OLD","enabled":false}]}}`)
		case "/rest/api/latest/project/OPS":
			fmt.Fprint(w, `{"key":"OPS","name":"Operations","plans":{"size":2,"start-index":0,"max-result":500,"plan":[
				{"key":"OPS-TOOLS","name":"Operations - Tools","enabled":true},{"key":"OPS-INFRA","name":"Operations - Infrastructure","enabled":true}]}}`)
		case "/rest/api/latest/plan/SHOP-WEB/specs":
			if q.Get("format") != "YAML" {
				http.Error(w, "java", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, specsJSON(bambooPlanSpecs))
		case "/rest/api/latest/plan/SHOP-OLD/specs":
			http.Error(w, `{"message":"The plan uses a task that cannot be exported"}`, http.StatusBadRequest)
		case "/rest/api/latest/plan/OPS-TOOLS/specs":
			fmt.Fprint(w, specsJSON(strings.NewReplacer("INFRA", "TOOLS", "Infrastructure", "Tools", "- infra:\n    type: git\n    url: https://git.example.com/infra.git",
				"- tools-repo").Replace(bambooInfraSpecs)))
		case "/rest/api/latest/plan/OPS-INFRA/specs":
			fmt.Fprint(w, specsJSON(bambooInfraSpecs))
		case "/rest/api/latest/result/SHOP-WEB":
			fmt.Fprint(w, `{"results":{"size":120,"start-index":0,"max-result":3,"result":[
				{"key":"SHOP-WEB-120","state":"Successful","buildState":"Successful","number":120,"buildCompletedTime":"2026-09-30T10:00:00.000+02:00","buildDurationInSeconds":300},
				{"key":"SHOP-WEB-119","state":"Failed","buildState":"Failed","number":119,"buildCompletedTime":"2026-09-29T10:00:00.000+02:00","buildDurationInSeconds":100},
				{"key":"SHOP-WEB-118","state":"Successful","buildState":"Successful","number":118,"buildCompletedTime":"2026-09-28T10:00:00.000+02:00","buildDurationInSeconds":200}]}}`)
		case "/rest/api/latest/deploy/project/all":
			fmt.Fprint(w, `[{"id":11,"name":"Web shop deployment","planKey":{"key":"SHOP-WEB"},"environments":[{"id":21,"name":"Staging"},{"id":22,"name":"Production"}]},
				{"id":12,"name":"Elsewhere","planKey":{"key":"OTHER-X"},"environments":[]}]`)
		case "/rest/api/latest/deploy/project/11/specs":
			fmt.Fprint(w, specsJSON(bambooDeploySpecs))
		case "/rest/api/latest/deploy/environment/21/variables":
			fmt.Fprint(w, `[{"name":"target.host","value":"staging.example.com"}]`)
		case "/rest/api/latest/deploy/environment/22/variables":
			fmt.Fprint(w, `[{"name":"target.host","value":"www.example.com"},{"name":"api.token","value":"********"}]`)
		default:
			if strings.HasPrefix(p, "/rest/api/latest/result/") {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			t.Errorf("unexpected request %s", r.URL)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBambooImport(t *testing.T) {
	env := apitest.New(t)
	admin := env.Admin()
	admin.MustDo(http.MethodPost, "/api/v1/code/projects", map[string]any{"key": "SHOP", "name": "Shop"}, nil)
	admin.MustDo(http.MethodPost, "/api/v1/code/projects/SHOP/repos", map[string]any{"name": "web", "readme": true}, nil)
	src := fakeBamboo(t)
	work := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()

	if err := importer.NewBamboo(importer.BambooOptions{URL: src.URL, Token: "wrong", Workdir: t.TempDir()}, log).Extract(ctx); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Fatalf("extract with a wrong token: %v", err)
	}
	bm := importer.NewBamboo(importer.BambooOptions{URL: src.URL, Token: bambooToken, Workdir: work,
		Repositories: map[string]string{"OPS-INFRA": "SHOP/web"}}, log)
	if err := bm.Extract(ctx); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bamboo/projects/SHOP/plans/SHOP-WEB/specs.json", "bamboo/projects/SHOP/plans/SHOP-OLD/specs-error.json",
		"bamboo/projects/OPS/plans/OPS-INFRA/specs.json", "bamboo/deployments/11/specs.json", "bamboo/deployments/11/variables-22.json",
		"bamboo/global-variables.json"} {
		if _, err := os.Stat(filepath.Join(work, f)); err != nil {
			t.Errorf("not extracted: %s", f)
		}
	}

	loader := importer.Loader{Pool: env.App.Pool, Issues: env.App.Issues, Code: env.App.Code, Builds: env.App.Builds, Log: log}
	rep, err := bm.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"plans": 4, "pipelines": 2, "pull-requests": 1, "environments": 2, "variables": 4, "secrets": 3, "placeholders": 1}
	for k, n := range want {
		if rep.Counts[k] != n {
			t.Errorf("count %s = %d, want %d (%v)", k, rep.Counts[k], n, rep.Counts)
		}
	}
	warnings := strings.Join(rep.Warnings, "\n")
	for _, w := range []string{"SHOP-OLD: Bamboo could not export", "OPS-TOOLS: the repository OPS/tools is not in Kogge", "OPS-TOOLS: the converted pipeline is in",
		"the secret deploy_password", "the secret api_token", "the secret artifactory_password"} {
		if !strings.Contains(warnings, w) {
			t.Errorf("no warning with %q in\n%s", w, warnings)
		}
	}
	if strings.Contains(warnings, "BAMSCRT") {
		t.Errorf("a secret in the report:\n%s", warnings)
	}
	if _, err := os.Stat(filepath.Join(work, "bamboo/proposals/OPS-TOOLS.yml")); err != nil {
		t.Errorf("no proposal file for the unmapped plan: %v", err)
	}
	var web *importer.PlanSummary
	for i, p := range rep.Plans {
		if p.Key == "SHOP-WEB" {
			web = &rep.Plans[i]
		}
	}
	if web == nil || web.Repository != "SHOP/web" || web.Pipeline != "shop-web" || web.PullRequest == 0 || web.Builds != 120 ||
		web.SuccessRate < 0.66 || web.SuccessRate > 0.67 || web.AverageSeconds != 200 || web.LastSuccess == nil ||
		web.LastSuccess.UTC().Format("2006-01-02T15") != "2026-09-30T08" || web.Placeholders != 1 {
		t.Errorf("plan summary: %+v", web)
	}

	// The proposal: a pull request from a branch with the pipeline file.
	var pr struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Source string `json:"source"`
		Target string `json:"target"`
		State  string `json:"state"`
	}
	admin.Get("/api/v1/code/repos/SHOP/web/pull-requests/"+strconv.Itoa(web.PullRequest), &pr)
	if pr.Title != "Pipelines converted from Bamboo" || pr.Source != "migration/bamboo-pipelines" || pr.Target != "main" || pr.State != "open" {
		t.Errorf("pull request: %+v", pr)
	}
	res := admin.Do(http.MethodGet, "/api/v1/code/repos/SHOP/web/raw?ref=migration/bamboo-pipelines&path=.kogge/pipelines.yml", nil)
	f, err := spec.Parse(res.Body)
	if err != nil {
		t.Fatalf("the proposed file: %v\n%s", err, res.Body)
	}
	pipelines := map[string]*spec.Pipeline{}
	for _, p := range f.Pipelines {
		pipelines[p.ID] = p
	}
	shop := pipelines["shop-web"]
	if len(f.Pipelines) != 2 || shop == nil || pipelines["ops-infra"] == nil || len(shop.Stages) != 3 ||
		shop.Stages[0].Jobs[0].Image != "golang:1.27" || shop.Variables["go_flags"] != "-mod=readonly" {
		t.Fatalf("the proposed pipelines:\n%s", res.Body)
	}
	if run := shop.Stages[0].Jobs[0].Steps[0].Run; run != "make build VERSION=${KOGGE_RUN_NUMBER} TEAM=${team}\n" {
		t.Errorf("the converted script: %q", run)
	}
	var timeline []struct {
		Kind string `json:"kind"`
	}
	admin.Get("/api/v1/code/repos/SHOP/web/pull-requests/"+strconv.Itoa(web.PullRequest)+"/timeline", &timeline)
	if len(timeline) != 1 || timeline[0].Kind != "opened" {
		t.Errorf("timeline: %+v", timeline)
	}

	// Environments and variables; the secrets are left to people.
	var envs []struct {
		Name string `json:"name"`
	}
	admin.Get("/api/v1/code/repos/SHOP/web/environments", &envs)
	if len(envs) != 2 {
		t.Errorf("environments: %+v", envs)
	}
	variables := func() string {
		rows, err := env.App.Pool.Query(ctx, `SELECT v.scope || ' ' || coalesce(e.name, '') || ' ' || v.name || '=' || v.value FROM build_variables v
			LEFT JOIN environments e ON v.scope = 'environment' AND e.id = v.scope_id ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var list []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			list = append(list, s)
		}
		return strings.Join(list, "; ")
	}
	if got := variables(); got != "environment production target_host=www.example.com; environment staging target_host=staging.example.com; "+
		"instance  artifactory_url=https://repo.example.com; repository  team=shop" {
		t.Errorf("variables: %s", got)
	}

	// The old addresses of plans, builds, jobs, deployment projects and environments lead to the pipelines of the
	// repository the plans went to; Bamboo projects to the code project of their repositories.
	for path, target := range map[string]string{
		"/browse/SHOP-WEB":                         "/builds/shop/web",
		"/browse/SHOP-WEB-120":                     "/builds/shop/web",
		"/browse/shop-web-120/log":                 "/builds/shop/web",
		"/browse/SHOP-WEB-COMP-120/test":           "/builds/shop/web",
		"/browse/SHOP-WEB-COMP":                    "/builds/shop/web",
		"/browse/SHOP-WEB12":                       "/builds/shop/web", // a plan branch
		"/browse/SHOP-WEB/latest":                  "/builds/shop/web",
		"/browse/OPS-INFRA-7":                      "/builds/shop/web",
		"/browse/SHOP":                             "/builds/shop",
		"/browse/OPS":                              "/builds/shop",
		"/chain/viewChain.action?planKey=SHOP-WEB": "/builds/shop/web",
		"/chain/result/viewChainResult.action?planKey=SHOP-WEB&buildNumber=118": "/builds/shop/web",
		"/build/viewBuildResults.action?buildKey=SHOP-WEB-COMP&buildNumber=118": "/builds/shop/web",
		"/chain/admin/config/editChainDetails.action?buildKey=SHOP-WEB":         "/builds/shop/web/settings",
		"/deploy/viewDeploymentProjectEnvironments.action?id=11":                "/builds/shop/web/environments",
		"/deploy/viewEnvironment.action?id=22":                                  "/builds/shop/web/environments",
	} {
		res := admin.Do(http.MethodGet, path, nil)
		if res.Status != http.StatusFound || res.Header.Get("Location") != target {
			t.Errorf("GET %s: %d %s, want %s", path, res.Status, res.Header.Get("Location"), target)
		}
	}
	// What was not migrated, or what the reader may not see, is not redirected: the reader gets the same answer as
	// for an address that never existed.
	outsider := env.CreateUser("outsider")
	missing := admin.Do(http.MethodGet, "/browse/NOPE-NOPE-1", nil)
	if missing.Status == http.StatusFound {
		t.Fatalf("an address that never existed is redirected to %s", missing.Header.Get("Location"))
	}
	for _, path := range []string{"/browse/SHOP-OLD", "/browse/OPS-TOOLS-3", "/browse/SHOP-NOPE", "/browse/NOPE",
		"/deploy/viewDeploymentProjectEnvironments.action?id=12", "/deploy/viewEnvironment.action?id=99", "/deploy/viewEnvironment.action?id=x"} {
		if res := admin.Do(http.MethodGet, path, nil); res.Status != missing.Status {
			t.Errorf("GET %s: %d %s, want %d as for an address that never existed", path, res.Status, res.Header.Get("Location"), missing.Status)
		}
	}
	for _, path := range []string{"/browse/SHOP-WEB-120", "/browse/SHOP", "/deploy/viewEnvironment.action?id=22",
		"/chain/admin/config/editChainDetails.action?buildKey=SHOP-WEB"} {
		if res := outsider.Do(http.MethodGet, path, nil); res.Status != missing.Status {
			t.Errorf("GET %s by a reader without access: %d %s, want %d as for an address that never existed", path, res.Status,
				res.Header.Get("Location"), missing.Status)
		}
	}

	// A second load changes nothing; variables people changed in Kogge stay.
	admin.MustDo(http.MethodPut, "/api/v1/builds/variables/team?scope=repository&project=SHOP&repository=web", map[string]any{"value": "changed"}, nil)
	again, err := bm.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	if again.Counts["pull-requests"] != 1 || !strings.Contains(variables(), "team=changed") {
		t.Errorf("second load: %v, variables %s", again.Counts, variables())
	}
	var prs, commits int
	if err := env.App.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM pull_requests), (SELECT count(*) FROM pushes WHERE protocol = 'import')`).
		Scan(&prs, &commits); err != nil || prs != 1 || commits != 1 {
		t.Errorf("after the second load: %d pull requests, %d import pushes (%v)", prs, commits, err)
	}

	// A declined proposal is not made again.
	admin.MustDo(http.MethodPost, "/api/v1/code/repos/SHOP/web/pull-requests/"+strconv.Itoa(web.PullRequest)+"/decline", map[string]any{}, nil)
	third, err := bm.Load(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	if third.Counts["pull-requests"] != 0 || !strings.Contains(strings.Join(third.Warnings, "\n"), "not made again") {
		t.Errorf("after declining: %v", third.Counts)
	}
}
