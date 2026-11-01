# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# Developer entry points. Run `make help` for an overview.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# The build's description (internal/version): the version from the newest tag (v1.2.0 gives 1.2.0), the commit, and
# the commit's date as the build date, so that building a commit again gives the same binaries.
VERSION ?= $(or $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v\([0-9]\)/\1/'),dev)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE ?= $(shell TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null)
LDFLAGS := -s -w -X kogge.de/kogge/internal/version.Version=$(VERSION) -X kogge.de/kogge/internal/version.Commit=$(COMMIT) \
	-X kogge.de/kogge/internal/version.Date=$(DATE)
GO_BUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

# Platforms of the agent binaries the server offers for download (`make agents`, deploy/Dockerfile).
AGENT_PLATFORMS ?= linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64

# Local development database (Docker) and server settings.
DEV_DB_URL ?= postgres://kogge:kogge@127.0.0.1:55432/kogge?sslmode=disable
DEV_ENV := KOGGE_DATABASE_URL="$(DEV_DB_URL)" KOGGE_DATA_DIR=$(CURDIR)/data KOGGE_LOG_FORMAT=text KOGGE_AGENT_DIR=$(CURDIR)/bin/agents \
	KOGGE_PUBLIC_URL=http://localhost:5173 KOGGE_AUTO_MIGRATE=true \
	KOGGE_BOOTSTRAP_ADMIN_USERNAME=admin KOGGE_BOOTSTRAP_ADMIN_PASSWORD=Kogge-Dev-Secret-2026

.PHONY: e2e e2e-browsers showcase showcase-record showcase-check help build build-server agents bundle sbom release-local \
	smoke-test smoke-test-ha run dev-db dev-db-stop migrate test test-go test-web lint format api-types web-install web-dev \
	web-build licenses ci clean

help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

build: web-build ## Build release binaries with the embedded web application into bin/
	$(GO_BUILD) -tags webui -o bin/kogge ./cmd/kogge
	$(GO_BUILD) -o bin/kogge-agent ./cmd/kogge-agent
	go run ./tools/golicenses -o THIRD_PARTY_LICENSES.md
	node tools/npm-licenses.mjs web --append THIRD_PARTY_LICENSES.md

build-server: ## Build the server and the build agent without the web application (fast)
	$(GO_BUILD) -o bin/kogge ./cmd/kogge
	$(GO_BUILD) -o bin/kogge-agent ./cmd/kogge-agent

agents: ## Build kogge-agent for all platforms into bin/agents (offered for download by the server)
	@mkdir -p bin/agents
	@for p in $(AGENT_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "kogge-agent $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GO_BUILD) -o bin/agents/kogge-agent-$$os-$$arch$$ext ./cmd/kogge-agent || exit 1; \
	done

bundle: ## Build the images and package them for installations without internet access (dist/)
	deploy/scripts/airgap-bundle.sh $(VERSION)

sbom: ## Write the SBOM (CycloneDX 1.6) of the binaries and the web application to bin/sbom.cdx.json
	go run ./tools/sbom -version $(VERSION) -commit "$(COMMIT)" -date "$(DATE)" -agent "$(AGENT_PLATFORMS)" -o bin/sbom.cdx.json

release-local: ## Build release VERSION=x.y.z into dist/x.y.z like the release workflow, without publishing or signing
	tools/release.sh $(VERSION)

smoke-test: ## Run the Compose stack from a fresh image and try it out end to end (deploy/scripts/smoke-test.sh)
	deploy/scripts/smoke-test.sh

smoke-test-ha: ## The same with several nodes behind load balancers (deploy/compose.ha.yaml)
	deploy/scripts/smoke-test.sh --ha

run: ## Run the server against the development database (web application via `make web-dev`)
	$(DEV_ENV) go run ./cmd/kogge serve

dev-db: ## Start the development PostgreSQL in Docker (port 55432)
	@docker start kogge-dev-db >/dev/null 2>&1 || docker run -d --name kogge-dev-db -e POSTGRES_USER=kogge \
		-e POSTGRES_PASSWORD=kogge -e POSTGRES_DB=kogge -p 127.0.0.1:55432:5432 postgres:18-alpine >/dev/null
	@until docker exec kogge-dev-db pg_isready -U kogge -q; do sleep 1; done; echo "PostgreSQL is ready on 55432"

dev-db-stop: ## Stop the development PostgreSQL
	docker stop kogge-dev-db

migrate: ## Apply migrations to the development database
	$(DEV_ENV) go run ./cmd/kogge migrate

test: test-go test-web ## Run all tests

test-go: ## Run the Go tests (PostgreSQL via KOGGE_TEST_DATABASE_URL or Docker)
	KOGGE_TEST_AGENT_DIR=$${KOGGE_TEST_AGENT_DIR:-$(CURDIR)/data/test-agents} go test -timeout 15m ./...

test-web: ## Run the web application's tests
	cd web && npm test --silent

lint: ## Check formatting and run static checks
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; echo "run make format"; exit 1)
	go vet ./...
	cd web && npm run --silent lint

format: ## Format Go and web sources
	gofmt -w cmd internal
	cd web && npm run --silent format

api-types: ## Regenerate the web application's API types from the OpenAPI document
	go run ./cmd/kogge openapi > web/src/api/openapi.json
	cd web && npx openapi-typescript src/api/openapi.json -o src/api/schema.d.ts

web-install: ## Install the web application's packages
	cd web && npm ci --no-audit --no-fund

web-dev: ## Run the web application with hot reload on http://localhost:5173
	cd web && npm run dev

web-build: ## Build the web application into internal/webui/dist
	cd web && npm run build

licenses: ## Check the licences of all Go and npm dependencies
	go run ./tools/golicenses
	node tools/npm-licenses.mjs web

ci: lint test licenses ## Everything the CI pipeline runs

# promtool from the Prometheus image, so that the check needs nothing but Docker.
PROMTOOL ?= docker run --rm -v "$(CURDIR)/deploy/monitoring:/m:ro" -w /m --entrypoint promtool prom/prometheus:v3.15.0

.PHONY: monitoring-check

monitoring-check: ## Check the alerting rules in deploy/monitoring and run their tests (promtool)
	$(PROMTOOL) check rules alerts.yml
	$(PROMTOOL) test rules alerts.test.yml

.PHONY: ci-local ci-worktree ci-status hooks

ci-local: ## Run the whole CI pipeline (also image and browser tests) for HEAD in a clean checkout (tools/ci/run.sh)
	tools/ci/run.sh HEAD

ci-worktree: ## Run the whole CI pipeline for the working tree as it is, before committing
	tools/ci/run.sh -w

ci-status: ## Show the latest results of the local CI pipeline
	tools/ci/status.sh

hooks: ## Use the repository's git hooks: checks before a commit, the CI pipeline after it and before a push
	git config core.hooksPath .githooks
	@echo "The pipeline now runs after every commit (tools/ci/status.sh); git config kogge.ci.enabled false pauses it."

clean: ## Remove build output
	rm -rf bin internal/webui/dist

e2e: ## Run the browser tests against a running server (KOGGE_E2E_URL, default http://localhost:18080; KOGGE_E2E_BROWSER)
	cd e2e && npx playwright test

e2e-browsers: ## Run the browser tests in Chrome, Firefox and WebKit, one after another
	cd e2e && for b in chrome firefox webkit; do KOGGE_E2E_BROWSER=$$b npx playwright test || exit 1; done

.PHONY: scale-generate scale-bench

# Scale tests (tools/scale): data of a reference size in an installation of its own, then the
# measurements against a server running on that data. The manifest holds access tokens and stays in data/.
SCALE_SIZE ?= XS
SCALE_MANIFEST ?= data/scale.json
SCALE_URL ?= http://localhost:8080

scale-generate: ## Generate scale data of SCALE_SIZE (XS, S, M) with the server's configuration (KOGGE_DATABASE_URL, …)
	@mkdir -p $(dir $(SCALE_MANIFEST))
	go run ./tools/scale generate -size $(SCALE_SIZE) -manifest $(SCALE_MANIFEST)

scale-bench: build-server ## Measure the operations with performance targets and Git over HTTP against SCALE_URL (needs git)
	go run ./tools/scale bench -url $(SCALE_URL) -manifest $(SCALE_MANIFEST) -agent bin/kogge-agent -strict

showcase: ## Build the preview for GitHub Pages from the recorded sample content into web/dist-showcase
	cd web && npm run build:showcase

showcase-record: ## Fill an empty Kogge (KOGGE_SHOWCASE_URL) with the sample content and record the preview from it
	cd e2e && npx playwright test -c showcase.config.ts showcase/seed.spec.ts && npx playwright test -c showcase.config.ts showcase/record.spec.ts

showcase-check: ## Check the built preview (make showcase) in Chrome: pages and what is new render from the recording
	cd e2e && npx playwright test -c showcase.config.ts showcase/preview.spec.ts
