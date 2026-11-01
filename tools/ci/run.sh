#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# The CI pipeline on this machine, for a repository without a CI server: the jobs of .github/workflows/ci.yml (Go,
# web application, browser tests) and the image build, for one commit, in a clean checkout of it, so that it tests
# exactly what was committed and leaves the working tree alone. The git hooks in .githooks (make hooks) start it in
# the background after every commit and run it before every push; tools/ci/status.sh shows the results, which are
# kept with their logs in .git/kogge-ci.
#
#   tools/ci/run.sh [-p commit|full] [REV]   run the pipeline for REV (default HEAD) and wait for its result
#   tools/ci/run.sh -w [-p commit|full]      the same for the working tree as it is, before committing it
#   tools/ci/run.sh -q [-p PROFILE] REV      hand REV to the background runner (what the post-commit hook does)
#
# Profiles: commit (after commits) runs the Go checks and tests, the image build, the web application's checks and
# build and the browser tests in Chrome. full (the default, and before pushes) runs the browser tests in Chrome,
# Firefox and WebKit, and also govulncheck, staticcheck and npm audit, whose findings are reported without failing
# the pipeline unless kogge.ci.strict is true.
#
# Settings, as environment variables or with git config (e.g. git config kogge.ci.skip image):
#   KOGGE_CI_SKIP      kogge.ci.skip      stages to leave out: go image web e2e scan
#   KOGGE_CI_BROWSERS  kogge.ci.browsers  browsers of the browser tests: chrome firefox webkit
#   KOGGE_CI_STRICT    kogge.ci.strict    true: findings of the scan stage fail the pipeline
#   KOGGE_CI_ENABLED   kogge.ci.enabled   false: the hooks do not run the pipeline
#   KOGGE_CI_KEEP      kogge.ci.keep      true: keep each run's checkout in ~/.cache/kogge-ci (to look into failures)
#   KOGGE_CI_SMOKE     kogge.ci.smoke     true: the image stage also runs the Compose stack's smoke test
#                                         (deploy/scripts/smoke-test.sh; slow, so off by default)
#   KOGGE_TEST_DATABASE_URL               PostgreSQL for the Go tests (default: pgtest's container kogge-test-pg)
#
# Needs git, Go, Node.js, curl and Docker (the image, and PostgreSQL for the browser tests), Google Chrome, and
# Playwright's Firefox and WebKit (cd e2e && npx playwright install firefox webkit).
set -euo pipefail

# Hooks run with variables that tie git to the repository being committed to; the git commands of the tests and of
# the server under test must find their own repositories.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
	GIT_COMMON_DIR GIT_QUARANTINE_PATH

# --- Stages. Each runs as a process of its own in the checkout (__stage NAME), so that a failing command ends it. ---

stage_go() {
	local unformatted
	unformatted=$(gofmt -l cmd internal tools)
	if [ -n "$unformatted" ]; then
		printf '%s\n' "$unformatted"
		echo "gofmt: these files are not formatted (make format)"
		return 1
	fi
	go vet ./...
	KOGGE_TEST_REQUIRE_DB=1 KOGGE_TEST_AGENT_DIR="$KOGGE_CI_AGENT_DIR" go test -timeout 15m ./...
	go run ./tools/golicenses
}

stage_web() {
	(cd web && npm ci --no-audit --no-fund --prefer-offline)
	node tools/npm-licenses.mjs web
	cd web
	npm run lint
	npm test
	npm run build
}

stage_image() {
	local version=ci-${KOGGE_CI_SHA:0:12}
	local args=(--build-arg "VERSION=$version" --build-arg "COMMIT=$KOGGE_CI_SHA" --build-arg "DATE=${KOGGE_CI_DATE:-}")
	docker build -f deploy/Dockerfile "${args[@]}" -t kogge-ci:latest .
	docker build -f deploy/Dockerfile "${args[@]}" --target agent -t kogge-ci-agent:latest .
	# The Compose stack from start to finish with the new image (deploy/scripts/smoke-test.sh), on free ports and in
	# a directory Docker can mount (the Docker snap cannot mount /tmp).
	if [ "${KOGGE_CI_SMOKE:-false}" = true ]; then
		KOGGE_IMAGE=kogge-ci:latest KOGGE_SMOKE_DIR="$KOGGE_CI_WORK/smoke" KOGGE_SMOKE_HTTP_PORT=$(free_port) \
			KOGGE_SMOKE_SSO_PORT=$(free_port) KOGGE_SMOKE_SSH_PORT=$(free_port) deploy/scripts/smoke-test.sh
	fi
}

stage_e2e() {
	if [ ! -f internal/webui/dist/index.html ]; then
		(cd web && npm ci --no-audit --no-fund --prefer-offline && npm run build)
	fi
	# Version, commit and date as a release has them (the checkout has no .git for Go to read them from): the
	# system page shows them.
	local ldflags="-X kogge.de/kogge/internal/version.Version=ci-${KOGGE_CI_SHA:0:12}"
	ldflags+=" -X kogge.de/kogge/internal/version.Commit=$KOGGE_CI_SHA -X kogge.de/kogge/internal/version.Date=${KOGGE_CI_DATE:-}"
	go build -tags webui -ldflags "$ldflags" -o bin/kogge ./cmd/kogge
	go build -ldflags "$ldflags" -o bin/kogge-agent ./cmd/kogge-agent
	(cd e2e && npm ci --no-audit --no-fund --prefer-offline)
	local browser playwright=()
	for browser in $KOGGE_CI_BROWSERS; do
		case $browser in
		chrome) ;; # the installed Google Chrome
		firefox | webkit) playwright+=("$browser") ;;
		*)
			echo "unknown browser $browser (chrome, firefox or webkit)"
			return 1
			;;
		esac
	done
	if [ ${#playwright[@]} -gt 0 ]; then
		(cd e2e && npx playwright install "${playwright[@]}") || echo "could not install ${playwright[*]}; using what is there"
	fi
	# One PostgreSQL for the run; every browser gets a database and a server of its own.
	KOGGE_CI_PG=kogge-ci-pg-$$
	export KOGGE_CI_PG
	trap 'docker rm -f "$KOGGE_CI_PG" >/dev/null 2>&1 || true' EXIT
	docker run -d --rm --name "$KOGGE_CI_PG" -e POSTGRES_USER=kogge -e POSTGRES_PASSWORD=kogge -e POSTGRES_DB=postgres \
		-p 127.0.0.1::5432 postgres:18-alpine >/dev/null
	local i
	for i in $(seq 1 60); do
		# Over TCP: the image's initialisation runs a server on the socket only, which goes away again.
		docker exec "$KOGGE_CI_PG" pg_isready -h 127.0.0.1 -U kogge -q 2>/dev/null && break
		sleep 1
	done
	KOGGE_CI_PGPORT=$(docker port "$KOGGE_CI_PG" 5432/tcp | sed -n '1s/.*://p')
	export KOGGE_CI_PGPORT
	local failed=()
	for browser in $KOGGE_CI_BROWSERS; do
		echo "-- browser tests in $browser"
		if ! (cd e2e && node -e "$launch_probe" "$browser"); then
			echo "$browser does not start on this machine: install what it needs (cd e2e && sudo npx playwright" \
				"install-deps $browser), or leave it out (git config kogge.ci.browsers \"chrome firefox\")"
			failed+=("$browser")
		elif ! "$BASH" "$KOGGE_CI_SELF" __stage browser "$browser"; then
			failed+=("$browser")
		fi
	done
	if [ ${#failed[@]} -gt 0 ]; then
		echo "the browser tests failed in ${failed[*]}"
		return 1
	fi
}

# stage_browser runs the browser tests in one browser against a new Kogge with an empty database.
stage_browser() {
	local browser=$1 db=e2e_$1 data http ssh i ready=0 result=0
	data=$KOGGE_CI_WORK/e2e-$browser
	mkdir -p "$data"
	docker exec "$KOGGE_CI_PG" createdb -U kogge "$db"
	http=$(free_port)
	ssh=$(free_port)
	KOGGE_DATABASE_URL="postgres://kogge:kogge@127.0.0.1:$KOGGE_CI_PGPORT/$db?sslmode=disable" \
		KOGGE_DATA_DIR="$data/data" KOGGE_HTTP_ADDR="127.0.0.1:$http" KOGGE_SSH_ADDR="127.0.0.1:$ssh" \
		KOGGE_PUBLIC_URL="http://localhost:$http" KOGGE_AUTO_MIGRATE=true KOGGE_LOG_FORMAT=text \
		KOGGE_BOOTSTRAP_ADMIN_USERNAME=admin KOGGE_BOOTSTRAP_ADMIN_PASSWORD=Kogge-Dev-Secret-2026 \
		bin/kogge serve >"$data/kogge.log" 2>&1 &
	server=$! # global: the trap runs after this function has returned
	trap 'kill "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true' EXIT
	for i in $(seq 1 90); do
		if curl -sf "http://127.0.0.1:$http/readyz" >/dev/null 2>&1; then
			ready=1
			break
		fi
		kill -0 "$server" 2>/dev/null || break
		sleep 1
	done
	if [ "$ready" != 1 ]; then
		echo "Kogge did not get ready:"
		cat "$data/kogge.log"
		return 1
	fi
	rm -rf e2e/test-results
	(cd e2e && KOGGE_E2E_URL="http://localhost:$http" KOGGE_E2E_BROWSER="$browser" npx playwright test) || result=$?
	if [ "$result" != 0 ]; then
		echo "-- the end of Kogge's log ($browser)"
		tail -n 100 "$data/kogge.log"
		mkdir -p "$KOGGE_CI_ARTIFACTS/e2e-$browser"
		cp -R e2e/test-results/. "$KOGGE_CI_ARTIFACTS/e2e-$browser/" 2>/dev/null || true
		cp "$data/kogge.log" "$KOGGE_CI_ARTIFACTS/e2e-$browser/"
		echo "traces and the server log: $KOGGE_CI_ARTIFACTS/e2e-$browser"
	fi
	return "$result"
}

# stage_scan looks for known vulnerabilities and suspicious code; it needs the internet (Go and npm registries).
stage_scan() {
	local result=0 toolchain
	# The tools must be built with Kogge's toolchain: built with the older one their modules ask for, they cannot read
	# Kogge's code.
	toolchain=$(go env GOVERSION)
	echo "-- govulncheck"
	GOTOOLCHAIN=$toolchain go run golang.org/x/vuln/cmd/govulncheck@latest ./... || result=1
	echo "-- staticcheck"
	GOTOOLCHAIN=$toolchain go run honnef.co/go/tools/cmd/staticcheck@latest ./... || result=1
	echo "-- npm audit of the packages the web application ships"
	(cd web && npm audit --omit=dev --audit-level=high) || result=1
	return "$result"
}

# launch_probe starts and closes a browser (chrome: the installed Google Chrome) and prints why it cannot start.
launch_probe='
const pw = require("@playwright/test");
const name = process.argv[1];
const type = name === "chrome" ? pw.chromium : pw[name];
type.launch(name === "chrome" ? { channel: "chrome" } : {}).then(
  (b) => b.close(),
  (e) => { console.error(e.message.split("\n").slice(0, 12).join("\n")); process.exit(1); },
);'

free_port() {
	node -e 'const s = require("net").createServer().listen(0, "127.0.0.1", () => { console.log(s.address().port); s.close(); });'
}

# --- The runner. ---

# setting NAME DEFAULT prints KOGGE_CI_<NAME>, else git config kogge.ci.<name>, else DEFAULT.
setting() {
	local var value
	var=KOGGE_CI_$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')
	value=${!var:-}
	if [ -z "$value" ]; then
		value=$(git -C "$repo" config --get "kogge.ci.$1" 2>/dev/null || true)
	fi
	printf '%s' "${value:-$2}"
}

# One pipeline at a time on this machine: the lock is a directory with the runner's process id.
take_lock() {
	if mkdir "$state/lock" 2>/dev/null; then
		echo $$ >"$state/lock/pid"
		return 0
	fi
	local pid
	pid=$(cat "$state/lock/pid" 2>/dev/null || true)
	if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
		rm -rf "$state/lock" # left by a runner that died
		if mkdir "$state/lock" 2>/dev/null; then
			echo $$ >"$state/lock/pid"
			return 0
		fi
	fi
	return 1
}

release_lock() {
	if [ "$(cat "$state/lock/pid" 2>/dev/null || true)" = $$ ]; then
		rm -rf "$state/lock"
	fi
}

enqueue() {
	printf '%s %s\n' "$1" "$2" >"$state/pending.$$"
	mv -f "$state/pending.$$" "$state/pending"
}

# drain runs the waiting commit until none waits. Commits made during a run replace each other, so the next run is
# for the newest; a runner busy with a run takes the waiting commit when it is done.
drain() {
	local sha profile
	while [ -s "$state/pending" ]; do
		take_lock || return 0
		while mv "$state/pending" "$state/taken" 2>/dev/null; do
			sha='' profile=''
			read -r sha profile <"$state/taken" || true
			rm -f "$state/taken"
			if [ -n "$sha" ]; then
				pipeline "$sha" "${profile:-commit}" || true
			fi
		done
		release_lock
	done
}

duration() { printf '%dm%02ds' $(($1 / 60)) $(($1 % 60)); }

# pipeline SHA PROFILE runs the stages of the profile for the commit and records the result in results.tsv.
pipeline() {
	local sha=$1 profile=$2 started at log work stage t0 skip browsers stages strict smoke commit_date
	local failed=() advisory=()
	started=$(date +%s)
	at=$(date '+%Y-%m-%dT%H:%M:%S%z')
	log=$state/logs/$sha.log
	work=${XDG_CACHE_HOME:-$HOME/.cache}/kogge-ci/run-$$
	rm -rf "$work" "$state/logs/$sha"
	mkdir -p "$work/src" "$state/logs"
	echo "$sha $profile $at" >"$state/running"
	{
		echo "Kogge CI ($profile) for $(git -C "$repo" log -1 --format='%h %s' "$sha")"
		echo "Started $(date) in $work"
	} >"$log"
	if ! git -C "$repo" archive "$sha" | tar -x -C "$work/src" 2>>"$log"; then
		echo "== checkout: FAILED" >>"$log"
		failed+=(checkout)
	fi
	cp "$self" "$work/ci.sh"
	skip=$(setting skip "")
	strict=$(setting strict false)
	smoke=$(setting smoke false)
	# The commit's date is the build's date (internal/version); the checkout has no Git to ask.
	commit_date=$(TZ=UTC0 git -C "$repo" log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ "$sha")
	if [ "$profile" = full ]; then
		browsers=$(setting browsers "chrome firefox webkit")
		stages="go image web e2e scan"
	else
		browsers=$(setting browsers chrome)
		stages="go image web e2e"
	fi
	[ ${#failed[@]} -eq 0 ] || stages=""
	for stage in $stages; do
		case " $skip " in
		*" $stage "*)
			echo "== $stage: left out (kogge.ci.skip)" >>"$log"
			continue
			;;
		esac
		t0=$(date +%s)
		echo "== $stage ($(date +%H:%M:%S))" >>"$log"
		if (cd "$work/src" && KOGGE_CI_SELF="$work/ci.sh" KOGGE_CI_WORK="$work" KOGGE_CI_SHA="$sha" KOGGE_CI_DATE="$commit_date" \
			KOGGE_CI_BROWSERS="$browsers" KOGGE_CI_SMOKE="$smoke" KOGGE_CI_AGENT_DIR="$repo/data/test-agents" \
			KOGGE_CI_ARTIFACTS="$state/logs/$sha" nice -n 10 "$BASH" "$work/ci.sh" __stage "$stage") \
			>>"$log" 2>&1 </dev/null; then
			echo "== $stage: passed in $(duration $(($(date +%s) - t0)))" >>"$log"
		elif [ "$stage" = scan ] && [ "$strict" != true ]; then
			advisory+=("$stage")
			echo "== $stage: findings after $(duration $(($(date +%s) - t0))) (reported only; kogge.ci.strict)" >>"$log"
		else
			failed+=("$stage")
			echo "== $stage: FAILED after $(duration $(($(date +%s) - t0)))" >>"$log"
		fi
	done
	last_status=pass
	[ ${#failed[@]} -eq 0 ] || last_status=fail
	{
		echo
		echo "Result: $last_status in $(duration $(($(date +%s) - started)))${failed[*]:+ (failed: ${failed[*]})}${advisory[*]:+ (findings: ${advisory[*]})}"
	} >>"$log"
	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$sha" "$profile" "$last_status" "$at" "$(($(date +%s) - started))" \
		"${failed[*]:-}" >>"$state/results.tsv"
	rm -f "$state/running"
	if [ "$(setting keep false)" != true ]; then
		rm -rf "$work"
	fi
	# Keep the newest 50 logs.
	find "$state/logs" -maxdepth 1 -name '*.log' -printf '%T@ %p\n' 2>/dev/null | sort -rn | sed -n '51,$s/^[^ ]* //p' |
		while IFS= read -r old; do rm -rf "$old" "${old%.log}"; done
	if [ "$last_status" = fail ] && [ "$background" = 1 ] && command -v notify-send >/dev/null; then
		notify-send "Kogge CI failed" "$(git -C "$repo" log -1 --format='%h %s' "$sha"): ${failed[*]}" 2>/dev/null || true
	fi
	[ "$last_status" = pass ]
}

# snapshot prints a commit of the working tree as it is (tracked and new files, without ignored ones), made through
# an index of its own: neither the index nor any branch changes.
snapshot() {
	local index tree
	index=$(mktemp)
	cp "$(git rev-parse --git-path index)" "$index" 2>/dev/null || true
	GIT_INDEX_FILE=$index git add -A
	tree=$(GIT_INDEX_FILE=$index git write-tree)
	rm -f "$index"
	git commit-tree "$tree" -p HEAD -m "working tree on $(git rev-parse --short HEAD), $(date '+%Y-%m-%d %H:%M')"
}

main() {
	local profile=full queue=0 drain_only=0 worktree=0 opt sha
	while getopts 'p:qdwh' opt; do
		case $opt in
		p) profile=$OPTARG ;;
		q) queue=1 ;;
		d) drain_only=1 ;;
		w) worktree=1 ;;
		*)
			sed -n '11,13p' "$self" | sed 's/^# *//'
			exit 2
			;;
		esac
	done
	shift $((OPTIND - 1))
	case $profile in
	commit | full) ;;
	*)
		echo "kogge-ci: unknown profile $profile (commit or full)" >&2
		exit 2
		;;
	esac
	repo=$(git rev-parse --show-toplevel)
	state=$(cd "$(git rev-parse --git-common-dir)" && pwd)/kogge-ci
	mkdir -p "$state/logs"
	background=0
	if [ "$drain_only" = 1 ]; then
		background=1
		drain
		exit 0
	fi
	if [ "$worktree" = 1 ]; then
		sha=$(snapshot)
	elif ! sha=$(git rev-parse --verify --quiet "${1:-HEAD}^{commit}"); then
		echo "kogge-ci: ${1:-HEAD} is not a commit" >&2
		exit 2
	fi
	if [ "$queue" = 1 ]; then
		background=1
		enqueue "$sha" "$profile"
		drain
		exit 0
	fi
	if ! take_lock; then
		echo "kogge-ci: waiting for the pipeline that runs (tools/ci/status.sh)" >&2
		until take_lock; do sleep 5; done
	fi
	trap release_lock EXIT
	echo "kogge-ci: $profile pipeline for $(git log -1 --format='%h %s' "$sha"); log: $state/logs/$sha.log" >&2
	local result=0
	pipeline "$sha" "$profile" || result=1
	release_lock
	trap - EXIT
	# Commits that were handed over meanwhile run in the background.
	if [ -s "$state/pending" ]; then
		(setsid "$self" -d </dev/null >/dev/null 2>&1 &) 2>/dev/null || ("$self" -d </dev/null >/dev/null 2>&1 &)
	fi
	sed -n '$p' "$state/logs/$sha.log" >&2
	exit "$result"
}

self=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")
if [ "${1:-}" = __stage ]; then
	shift
	stage=$1
	shift
	"stage_$stage" "$@"
	exit 0
fi
main "$@"
