// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package importer

import (
	"os"
	"slices"
	"strings"
	"testing"

	"kogge.de/kogge/internal/builds/spec"
)

// bambooWebSpecs is a plan as Bamboo exports it (YAML specs, with the plan's permissions as a second document).
const bambooWebSpecs = `---
version: 2
plan:
  project-key: SHOP
  key: WEB
  name: Web shop
stages:
- Build:
    manual: false
    final: false
    jobs:
    - Compile
    - Lint
- Package/Publish:
    manual: true
    jobs:
    - Package
Compile:
  key: COMP
  tasks:
  - checkout:
      force-clean-build: false
      description: Checkout Default Repository
  - script:
      interpreter: SHELL
      scripts:
      - |-
        echo "Building ${bamboo.buildNumber} on $bamboo_planRepository_branchName"
        ./gradlew build -Pversion=${bamboo.app.version} -Pagent=${bamboo.agentId}
      description: Build
  - maven:
      executable: Maven 3
      jdk: JDK 17
      goal: clean verify
      tests: true
      environment: MAVEN_OPTS="-Xmx1g"
  final-tasks:
  - test-parser:
      type: junit
      test-results: build/test-results/**/*.xml
  artifacts:
  - name: jar
    location: build/libs
    pattern: '*.jar'
    shared: true
    required: true
  requirements:
  - system.builder.mvn3.Maven 3
  - os: Linux
Lint:
  key: LINT
  docker:
    image: node:20
  tasks:
  - script: npm ci && npm run lint
  - any-task:
      plugin-key: com.example.sonar:sonar-scanner
      configuration:
        host: https://sonar.example.com
        password: BAMSCRT@0@0@abc
      description: Sonar
  requirements:
  - system.docker.executable
Package:
  key: PKG
  tasks:
  - script:
      interpreter: WINDOWS_POWER_SHELL
      scripts:
      - Write-Host "Version $env:bamboo_app_version"
  artifact-subscriptions:
  - artifact: jar
    destination: libs
  requirements:
  - system.builder.command.PowerShell
variables:
  app.version: 1.2.0
  deploy.password: BAMSCRT@0@0@xyz
triggers:
- polling:
    period: '180'
- cron: 0 30 2 ? * 2-6
branches:
  create: for-pull-request
  delete:
    after-deleted-days: 7
notifications:
- events:
  - plan-failed
  recipients:
  - committers
labels: []
other:
  concurrent-build-plugin: system-default
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

const bambooWebDeploySpecs = `---
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
  - clean
  - artifact-download:
      artifacts:
      - name: jar
  - script:
      interpreter: SHELL
      scripts:
      - ./deploy.sh ${bamboo.deploy.environment} ${bamboo.target.host}
  variables:
    target.host: staging.example.com
  requirements:
  - os: Linux
Production:
  tasks:
  - ssh:
      host: prod.example.com
      username: deploy
      password: BAMSCRT@0@0@zzz
      command: ./deploy.sh
  variables:
    target.host: www.example.com
---
version: 2
deployment:
  name: Web shop deployment
deployment-permissions:
- users:
  - jdoe
  permissions:
  - view
`

func TestConvertBambooPlan(t *testing.T) {
	p, err := convertBambooPlan(bambooPlan{Key: "SHOP-WEB", Name: "Web shop", Enabled: true, Specs: bambooWebSpecs, DefaultBranch: "main",
		Known: map[string]bool{"app_version": true, "target_host": true},
		Deployments: []bambooDeployment{{Name: "Web shop deployment", Specs: bambooWebDeploySpecs,
			Variables: map[string][]bambooVariable{"Production": {{Name: "api.token", Value: "********"}, {Name: "region", Value: "eu"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := pipelineFile("Proposed by the migration.", []*convertedPlan{p})
	if os.Getenv("KOGGE_SHOW_PIPELINE") != "" {
		t.Logf("\n%s", data)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	f, err := spec.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	pl := f.Pipelines[0]
	if pl.ID != "shop-web" || pl.Name != "Web shop" || pl.On.Push == nil || !slices.Equal(pl.On.Push.Branches, []string{"main"}) ||
		pl.On.PullRequest == nil || pl.On.Manual == nil || len(pl.On.Schedule) != 1 || pl.On.Schedule[0].Expr != "30 2 * * 1-5" {
		t.Errorf("pipeline %s %q, triggers %+v %+v", pl.ID, pl.Name, pl.On.Push, pl.On.Schedule)
	}
	if pl.Variables["app_version"] != "1.2.0" || len(pl.Variables) != 1 {
		t.Errorf("variables: %v", pl.Variables)
	}
	var stages []string
	for _, s := range pl.Stages {
		stages = append(stages, s.Name)
	}
	if strings.Join(stages, ",") != "Build,Package-Publish,Deploy Staging,Deploy Production" {
		t.Fatalf("stages: %v", stages)
	}
	compile, lint, pack := pl.Stages[0].Jobs[0], pl.Stages[0].Jobs[1], pl.Stages[1].Jobs[0]
	if compile.Image != "" || !slices.Equal(compile.RunsOn, []string{"maven-3", "os-linux", "jdk-17"}) || len(compile.Steps) != 2 ||
		!slices.Equal(compile.Reports.JUnit, []string{"**/target/surefire-reports/*.xml", "build/test-results/**/*.xml"}) ||
		len(compile.Artifacts) != 1 || !slices.Equal(compile.Artifacts[0].Paths, []string{"build/libs/*.jar"}) {
		t.Errorf("compile: %+v", compile)
	}
	if run := compile.Steps[0].Run; compile.Steps[0].Name != "Build" ||
		!strings.Contains(run, `echo "Building ${KOGGE_RUN_NUMBER} on ${KOGGE_BRANCH}"`) || !strings.Contains(run, "-Pversion=${app_version}") {
		t.Errorf("script: %q", run)
	}
	if run := compile.Steps[1].Run; run != "export MAVEN_OPTS=\"-Xmx1g\"\nmvn clean verify\n" {
		t.Errorf("maven: %q", run)
	}
	if lint.Image != "node:20" || len(lint.RunsOn) != 0 || len(lint.Steps) != 2 || lint.Steps[0].Run != "npm ci && npm run lint\n" ||
		!strings.Contains(lint.Steps[1].Name, "Sonar") || !strings.Contains(lint.Steps[1].Run, "exit 1") {
		t.Errorf("lint: %+v", lint)
	}
	if !pack.Manual || pack.Steps[0].Shell != "pwsh" || pack.Steps[0].Run != "Write-Host \"Version $env:app_version\"\n" ||
		!slices.Equal(pack.Dependencies, []string{"Build/Compile"}) {
		t.Errorf("package: %+v", pack)
	}
	staging, production := pl.Stages[2], pl.Stages[3]
	if sj := staging.Jobs[0]; sj.Environment != "staging" || sj.Manual || staging.Only == nil || !slices.Equal(staging.Only.Branches, []string{"main"}) ||
		len(sj.Steps) != 1 || sj.Steps[0].Run != "./deploy.sh ${KOGGE_ENVIRONMENT} ${target_host}\n" {
		t.Errorf("staging: %+v %+v", staging, sj)
	}
	if pj := production.Jobs[0]; pj.Environment != "production" || !pj.Manual || !strings.Contains(pj.Steps[0].Run, "exit 1") {
		t.Errorf("production: %+v", pj)
	}
	if p.Placeholders != 2 || !slices.Equal(p.Labels, []string{"jdk-17", "maven-3", "os-linux", "powershell"}) {
		t.Errorf("placeholders %d, labels %v", p.Placeholders, p.Labels)
	}
	if strings.Join(p.Secrets, "; ") != "deploy_password (plan SHOP-WEB); api_token (environment Production of Web shop deployment)" {
		t.Errorf("secrets: %v", p.Secrets)
	}
	if len(p.Environments) != 2 || p.Environments[0].ID != "staging" || p.Environments[1].ID != "production" ||
		!slices.Equal(p.Environments[1].Variables, []bambooVariable{{Name: "region", Value: "eu"}, {Name: "target_host", Value: "www.example.com"}}) {
		t.Errorf("environments: %+v", p.Environments)
	}
	notes := strings.Join(p.Notes, "\n")
	for _, n := range []string{"agentId has no counterpart", `"Sonar" has no counterpart`, "Notifications", "named releases"} {
		if !strings.Contains(notes, n) {
			t.Errorf("no note with %q:\n%s", n, notes)
		}
	}
	text := string(data)
	for _, s := range []string{"# Converted from the Bamboo plan SHOP-WEB (Web shop).", "# Bamboo requirements: system.builder.mvn3.Maven 3, os Linux",
		"# Configuration of the Bamboo task:", "password: (secret)"} {
		if !strings.Contains(text, s) {
			t.Errorf("the file lacks %q", s)
		}
	}
	if strings.Contains(text, "BAMSCRT") || strings.Contains(text, "deploy.password") {
		t.Errorf("the file shows a secret:\n%s", text)
	}

	// A disabled plan runs only when started by hand.
	p, err = convertBambooPlan(bambooPlan{Key: "SHOP-WEB", Name: "Web shop", Specs: bambooWebSpecs})
	if err != nil {
		t.Fatal(err)
	}
	if on := p.Pipeline.Content[3]; len(on.Content) != 2 || on.Content[0].Value != "manual" {
		t.Errorf("triggers of a disabled plan: %v", on.Content)
	}
}

func TestQuartzToCron(t *testing.T) {
	for q, want := range map[string]string{
		"0 0 2 * * ?":        "0 2 * * *",
		"0 30 2 ? * MON-FRI": "30 2 * * MON-FRI",
		"0 0/15 * * * ?":     "0/15 * * * *",
		"0 0 12 ? * 1,7 *":   "0 12 * * 0,6",
		"0 0 0 1 1 ? 2030":   "",
		"0 0 0 L * ?":        "",
		"*/10 * * * * ?":     "",
		"0 0 0 ? * 2#1":      "",
		"not a cron":         "",
		"0 15 10 ? * 6/2":    "15 10 * * 5/2",
	} {
		got, ok := quartzToCron(q)
		if got != want || ok != (want != "") {
			t.Errorf("quartzToCron(%q) = %q, %v; want %q", q, got, ok, want)
		}
	}
}

func TestBambooLabel(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"system.builder.mvn3.Maven 3", "", "maven-3"},
		{"system.jdk.JDK 17", "", "jdk-17"},
		{"system.docker.executable", "", "docker"},
		{"system.git.executable", "", "git"},
		{"Maven 3", "", "maven-3"},
		{"os", `Ubuntu \d+`, "os-ubuntu"},
		{"system.builder.node.Node.js 18", "", "node-js-18"},
		{"custom.capability", "x86_64|amd64", "custom-capability-x86-64-amd64"},
	} {
		if got := label(c.key, c.value); got != c.want {
			t.Errorf("label(%q, %q) = %q, want %q", c.key, c.value, got, c.want)
		}
	}
}
