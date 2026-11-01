#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# Tries the Compose stack (deploy/compose.yaml) out from start to finish, the way the installation guide sets it
# up: builds the image (or takes KOGGE_IMAGE), creates the secrets with scripts/init-secrets.sh, starts the stack as
# the Compose project kogge-smoke and waits until Kogge and Keycloak are ready. Then it signs in as the bootstrap
# administrator, creates a project and an issue, creates a repository and pushes to it over HTTP and over SSH,
# checks the connection to Keycloak, writes a backup with `kogge admin backup` and checks it, and checks which
# client addresses the audit log records: a request from the host (where the reverse proxy runs) with
# X-Forwarded-For shows the forwarded address, one from another container its own. At the end, also when a step
# failed, it removes the stack with its volumes.
#
# With --ha, it tries out Kogge on several nodes (compose.yaml with compose.ha.yaml) as the Compose
# project kogge-ha: two web nodes behind the load balancers, two workers. Besides the steps above, it checks that
# both web nodes answer requests through the load balancer, that the workers process the pushes, that SSH clients
# see one host key at both nodes, and that the installation keeps working while a web node is stopped.
#
#   deploy/scripts/smoke-test.sh          (make smoke-test)
#   deploy/scripts/smoke-test.sh --ha
#
# Environment:
#   KOGGE_IMAGE           the image to test, e.g. kogge:1.2.0 (default: build kogge-smoke:latest, removed afterwards)
#   KOGGE_SMOKE_HTTP_PORT, KOGGE_SMOKE_SSO_PORT, KOGGE_SMOKE_SSH_PORT
#                         the ports on 127.0.0.1 for Kogge, Keycloak and SSH (default 18135, 18136 and 12235)
#   KOGGE_SMOKE_SUBNET, KOGGE_SMOKE_GATEWAY
#                         the stack's network (default 172.31.254.0/24 and .1, with --ha 172.31.253.0/24 and .1, apart
#                         from a stack with the defaults); with --ha, KOGGE_SMOKE_LB_ADDRESS is the load balancer's
#                         address in it (default .10)
#   KOGGE_SMOKE_DIR       the directory for the deployment files, which Docker must be able to mount (default: a new
#                         one in $TMPDIR or /tmp; the Docker snap cannot mount /tmp: use TMPDIR=/var/tmp there)
#   KOGGE_SMOKE_PROJECT   the Compose project (default kogge-smoke, with --ha kogge-ha)
#   KOGGE_SMOKE_KEEP=1    leave the stack running (the script says how to remove it)
#
# Needs Docker with Compose v2.24 or later, curl, git, ssh, ssh-keygen and jq.
set -euo pipefail

ha=
case ${1:-} in
--ha) ha=1 ;;
"") ;;
*)
	echo "usage: $0 [--ha]" >&2
	exit 2
	;;
esac
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
project=kogge-smoke
net=172.31.254
if [ -n "$ha" ]; then
	project=kogge-ha
	net=172.31.253
fi
project=${KOGGE_SMOKE_PROJECT:-$project}
http_port=${KOGGE_SMOKE_HTTP_PORT:-18135}
sso_port=${KOGGE_SMOKE_SSO_PORT:-18136}
ssh_port=${KOGGE_SMOKE_SSH_PORT:-12235}
subnet=${KOGGE_SMOKE_SUBNET:-$net.0/24}
gateway=${KOGGE_SMOKE_GATEWAY:-$net.1}
lb_address=${KOGGE_SMOKE_LB_ADDRESS:-${subnet%.*}.10}
base=http://127.0.0.1:$http_port
public=http://localhost:$http_port
password=Smoke-Test-$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')

for tool in docker curl git ssh ssh-keygen jq; do
	command -v "$tool" >/dev/null || {
		echo "smoke test: $tool is missing" >&2
		exit 2
	}
done

step() { printf '\n== %s\n' "$*"; }
fail() {
	echo "smoke test FAILED: $*" >&2
	exit 1
}

image=${KOGGE_IMAGE:-}
built=
dir=${KOGGE_SMOKE_DIR:-}
made_dir=
if [ -z "$dir" ]; then
	dir=$(mktemp -d "${TMPDIR:-/tmp}/kogge-smoke.XXXXXX")
	made_dir=1
fi
files=(-f "$dir/compose.yaml")
services=(db keycloak migrate kogge)
if [ -n "$ha" ]; then
	files+=(-f "$dir/compose.ha.yaml")
	services+=(kogge-2 worker lb ssh-lb)
fi
compose() { docker compose -p "$project" --project-directory "$dir" "${files[@]}" "$@"; }

cleanup() {
	local status=$?
	set +e
	if [ "$status" != 0 ] && [ -f "$dir/compose.yaml" ]; then
		echo "-- the stack at the failure:"
		compose ps -a
		for service in "${services[@]}"; do
			echo "-- the end of the log of $service:"
			compose logs --no-color --tail 60 "$service" 2>&1
		done
	fi
	if [ "${KOGGE_SMOKE_KEEP:-}" = 1 ]; then
		echo "The stack keeps running (KOGGE_SMOKE_KEEP=1): $dir; docker compose -p $project ${files[*]} down -v removes it."
		exit "$status"
	fi
	if [ -f "$dir/compose.yaml" ]; then
		compose down -v --remove-orphans >/dev/null 2>&1
	fi
	# The backups belong to the container's user.
	if [ -d "$dir/backups" ] && [ -n "$image" ]; then
		docker run --rm --user 0 --entrypoint find -v "$dir/backups:/backups" "$image" /backups -mindepth 1 -delete 2>/dev/null
	fi
	if [ -n "$made_dir" ]; then
		rm -rf "$dir"
	fi
	if [ -n "$built" ]; then
		docker rmi "$image" >/dev/null 2>&1
	fi
	if [ "$status" = 0 ]; then
		echo
		echo "smoke test passed"
	fi
	exit "$status"
}
trap cleanup EXIT

if [ -z "$image" ]; then
	step "Building the image"
	image=kogge-smoke:latest
	docker build -f "$root/deploy/Dockerfile" --build-arg VERSION=smoke \
		--build-arg COMMIT="$(git -C "$root" rev-parse HEAD 2>/dev/null || true)" -t "$image" "$root"
	built=1
fi

step "Deployment files in $dir"
mkdir -p "$dir/scripts"
cp "$root/deploy/compose.yaml" "$root/deploy/compose.ha.yaml" "$root/deploy/.env.example" "$dir/"
cp -R "$root/deploy/db" "$root/deploy/keycloak" "$root/deploy/ha" "$dir/"
cp "$root/deploy/scripts/init-secrets.sh" "$dir/scripts/"
# Compose's file secrets and configuration are bind mounts: Docker must see the directory.
if ! docker run --rm --entrypoint ls -v "$dir/db:/probe:ro" "$image" /probe/roles.sh >/dev/null 2>&1; then
	fail "Docker cannot mount $dir (the Docker snap cannot mount /tmp: set TMPDIR=/var/tmp or KOGGE_SMOKE_DIR)"
fi
# The Docker snap cannot start containers that may not gain privileges; the stack runs without that setting there.
nnp=true
if ! docker run --rm --security-opt no-new-privileges:true --entrypoint true "$image" 2>/dev/null; then
	nnp=false
	echo "This Docker cannot start containers with no-new-privileges (the Docker snap?): testing with KOGGE_NO_NEW_PRIVILEGES=false"
fi
"$dir/scripts/init-secrets.sh"
# What `sudo chown 10001:10001 backups` does for an operator.
docker run --rm --user 0 --entrypoint chown -v "$dir/backups:/backups" "$image" 10001:10001 /backups
{
	grep -v -E '^(KOGGE_PUBLIC_URL|KOGGE_SSO_URL|KOGGE_HTTP_PUBLISH|KOGGE_SSH_PUBLISH|KOGGE_SSO_PUBLISH|KOGGE_BOOTSTRAP_ADMIN_USERNAME|KOGGE_BOOTSTRAP_ADMIN_PASSWORD)=' \
		"$dir/.env.example"
	cat <<EOF
KOGGE_IMAGE=$image
KOGGE_PUBLIC_URL=$public
KOGGE_SSO_URL=http://localhost:$sso_port
KOGGE_HTTP_PUBLISH=127.0.0.1:$http_port
KOGGE_SSO_PUBLISH=127.0.0.1:$sso_port
KOGGE_SSH_PUBLISH=127.0.0.1:$ssh_port
KOGGE_SUBNET=$subnet
KOGGE_GATEWAY=$gateway
KOGGE_NO_NEW_PRIVILEGES=$nnp
KOGGE_BOOTSTRAP_ADMIN_USERNAME=admin
KOGGE_BOOTSTRAP_ADMIN_PASSWORD=$password
KOGGE_LB_ADDRESS=$lb_address
EOF
} >"$dir/.env"

# healthy SERVICE... tells whether all containers of the services are healthy.
healthy() {
	local service id
	for service in "$@"; do
		for id in $(compose ps -q "$service"); do
			[ "$(docker inspect -f '{{.State.Health.Status}}' "$id" 2>/dev/null)" = healthy ] || return 1
		done
		[ -n "$(compose ps -q "$service")" ] || return 1
	done
}

step "Starting the stack"
compose up -d
ready=
nodes=(keycloak)
[ -n "$ha" ] && nodes+=(kogge kogge-2 worker lb)
for _ in $(seq 1 150); do
	if curl -sf "$base/readyz" >/dev/null 2>&1 && healthy "${nodes[@]}"; then
		ready=1
		break
	fi
	sleep 2
done
[ -n "$ready" ] || fail "the stack did not get ready in 5 minutes"
compose ps
[ "$(docker inspect -f '{{.State.ExitCode}}' "$(compose ps -aq migrate)")" = 0 ] || fail "the migrations failed"

# api METHOD PATH [JSON] calls the API as the signed-in administrator and prints the response's body.
jar=$dir/cookies
csrf=
api() {
	local args=(-sS -o "$dir/response" -w '%{http_code}' -b "$jar" -c "$jar" -X "$1" -H "Origin: $public")
	[ -n "$csrf" ] && args+=(-H "X-CSRF-Token: $csrf")
	[ $# -ge 3 ] && args+=(-H 'Content-Type: application/json' --data "$3")
	local code
	code=$(curl "${args[@]}" "$base$2")
	if [ "${code:0:1}" != 2 ]; then
		echo "$1 $2: HTTP $code $(cat "$dir/response")" >&2
		return 1
	fi
	cat "$dir/response"
}

step "Signing in as the bootstrap administrator"
csrf=$(api POST /api/v1/auth/login "$(jq -n --arg p "$password" '{username: "admin", password: $p}')" | jq -r .csrfToken)
[ -n "$csrf" ] && [ "$csrf" != null ] || fail "no CSRF token after signing in"
version=$(api GET /api/v1/admin/system | jq -r .version)
echo "Kogge $version"

step "A project and an issue"
api POST /api/v1/projects '{"key": "SMOKE", "name": "Smoke test", "template": "kanban"}' >/dev/null
type_id=$(api GET /api/v1/projects/SMOKE/issue-types | jq -r '.[0].issueTypeId')
key=$(api POST /api/v1/issues "{\"project\": \"SMOKE\", \"typeId\": $type_id, \"summary\": \"Smoke test: Grüße\"}" | jq -r .key)
[ "$(api GET "/api/v1/issues/$key" | jq -r .summary)" = "Smoke test: Grüße" ] || fail "the issue $key does not read back"
echo "created $key"

step "A repository, pushed to over HTTP and over SSH"
api POST /api/v1/code/projects '{"key": "SMK", "name": "Smoke code"}' >/dev/null
api POST /api/v1/code/projects/SMK/repos '{"name": "app"}' >/dev/null
token=$(api POST /api/v1/me/tokens '{"name": "smoke test", "scopes": ["write"], "expiresInDays": 1}' | jq -r .token)
ssh-keygen -q -t ed25519 -N '' -C smoke-test -f "$dir/id_ed25519"
api POST /api/v1/me/ssh-keys "$(jq -n --arg k "$(cat "$dir/id_ed25519.pub")" '{name: "smoke test", key: $k}')" >/dev/null
work=$dir/clone
git init -q -b main "$work"
git -C "$work" -c user.name=Smoke -c user.email=smoke@example.com commit -q --allow-empty -m "$key Over HTTP"
git -C "$work" push -q "http://admin:$token@127.0.0.1:$http_port/scm/smk/app.git" main
git -C "$work" -c user.name=Smoke -c user.email=smoke@example.com commit -q --allow-empty -m "$key Over SSH"
GIT_SSH_COMMAND="ssh -i $dir/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR" \
	git -C "$work" push -q "ssh://git@127.0.0.1:$ssh_port/smk/app.git" main
subjects=$(api GET "/api/v1/code/repos/SMK/app/commits?ref=main" | jq -r '[.. | strings] | join("\n")')
grep -qx "$key Over HTTP" <<<"$subjects" || fail "the commit pushed over HTTP is missing"
grep -qx "$key Over SSH" <<<"$subjects" || fail "the commit pushed over SSH is missing"
echo "both pushes arrived"

if [ -n "$ha" ]; then
	step "Several nodes: both web nodes answer through the load balancer"
	# Kogge takes the request id of a trusted proxy: the logs of the nodes show which requests they answered.
	for i in $(seq 1 12); do
		curl -sf -o /dev/null -b "$jar" -H "X-Request-Id: smoke-ha-$i" "$base/api/v1/me" || fail "request $i through the load balancer failed"
	done
	for service in kogge kogge-2; do
		answered=$(compose logs --no-color "$service" 2>&1 | grep -c '"request_id":"smoke-ha-' || true)
		echo "$service answered $answered of the 12 requests"
		[ "$answered" -gt 0 ] || fail "the web node $service answered none of the requests through the load balancer"
	done

	step "Several nodes: the workers process the pushes"
	# The web nodes run no jobs: the development section of the issue comes from the workers' processing.
	development=
	for _ in $(seq 1 60); do
		development=$(api GET "/api/v1/issues/$key/development" | jq -r '[.commits[].subject] | join("\n")')
		grep -qx "$key Over SSH" <<<"$development" && grep -qx "$key Over HTTP" <<<"$development" && break
		sleep 1
	done
	grep -qx "$key Over HTTP" <<<"$development" && grep -qx "$key Over SSH" <<<"$development" ||
		fail "the workers did not process the pushes: the issue's development shows: $development"
	echo "the issue $key shows both commits"

	step "Several nodes: SSH clients see one host key at both nodes"
	# nginx sends the connections to the nodes in turn; the client learns the key at the first one and checks it
	# strictly at the others.
	for i in 1 2 3 4; do
		checking=yes
		[ "$i" = 1 ] && checking=accept-new
		GIT_SSH_COMMAND="ssh -i $dir/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=$checking -o UserKnownHostsFile=$dir/known_hosts -o LogLevel=ERROR" \
			git ls-remote -q "ssh://git@127.0.0.1:$ssh_port/smk/app.git" >/dev/null || fail "SSH connection $i: the host key differs"
	done
	echo "four SSH connections, one host key"

	step "Several nodes: a web node stops, the installation goes on"
	compose stop kogge-2
	for i in $(seq 1 10); do
		api GET "/api/v1/issues/$key" >/dev/null || fail "request $i while kogge-2 was stopped failed"
	done
	git -C "$work" -c user.name=Smoke -c user.email=smoke@example.com commit -q --allow-empty -m "$key With one web node"
	git -C "$work" push -q "http://admin:$token@127.0.0.1:$http_port/scm/smk/app.git" main
	GIT_SSH_COMMAND="ssh -i $dir/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$dir/known_hosts -o LogLevel=ERROR" \
		git -C "$work" ls-remote -q "ssh://git@127.0.0.1:$ssh_port/smk/app.git" >/dev/null || fail "SSH while kogge-2 was stopped failed"
	compose start kogge-2
	for _ in $(seq 1 60); do
		healthy kogge-2 && break
		sleep 2
	done
	healthy kogge-2 || fail "kogge-2 did not get ready again"
	echo "with kogge-2 stopped, reads, a push over HTTP and SSH went on; kogge-2 is back"
fi

step "Single sign-on: Kogge's connection to Keycloak"
checks=$(api POST /api/v1/admin/sso/check)
jq -r '.[] | "\(if .ok then "ok  " else "FAIL" end) \(.key): \(.detail)"' <<<"$checks"
[ "$(jq 'length > 0 and all(.ok)' <<<"$checks")" = true ] || fail "a check of single sign-on failed"

step "A backup with kogge admin backup"
compose exec -T kogge kogge admin backup -dir /backups -keep 7
file=$(compose exec -T kogge sh -c 'ls /backups/kogge-backup-*.tar' | tail -n 1 | tr -d '\r')
[ -n "$file" ] || fail "no backup in /backups"
listing=$(compose exec -T kogge tar -tf "$file")
[ "$(head -n 1 <<<"$listing")" = kogge-backup.json ] || fail "the backup does not start with its manifest"
[ "$(tail -n 1 <<<"$listing")" = kogge-backup-end.json ] || fail "the backup does not end with its summary"
grep -q '^repositories/.*\.git/' <<<"$listing" || fail "the backup has no repository"
summary=$(compose exec -T kogge tar -xOf "$file" kogge-backup-end.json)
manifest=$(compose exec -T kogge tar -xOf "$file" kogge-backup.json)
jq -e '.rows.issues >= 1 and .repositories == 1 and (.missing // [] | length) == 0' <<<"$summary" >/dev/null ||
	fail "the backup's summary: $summary"
jq -e --arg v "$version" '.kogge == $v and .schema.schema > 0' <<<"$manifest" >/dev/null || fail "the backup's manifest: $manifest"
echo "$file: $(jq -c '{rows: (.rows | add), repositories, blobs, logs}' <<<"$summary")"
compose exec -T kogge kogge admin fsck

step "Client addresses in the audit log"
# From the host, as a reverse proxy there connects: Kogge trusts the network's gateway and takes X-Forwarded-For.
curl -s -o /dev/null -X POST -H "Origin: $public" -H 'Content-Type: application/json' -H 'X-Forwarded-For: 203.0.113.77' \
	--data '{"username": "smoke-from-host", "password": "Wrong-Password-1"}' "$base/api/v1/auth/login"
# From another container in the network: not a trusted proxy, its header is ignored.
network=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(compose ps -q kogge)")
docker run --rm --network "$network" --entrypoint wget "$image" -q -O /dev/null --header 'X-Forwarded-For: 198.51.100.9' \
	--header 'Content-Type: application/json' --header "Origin: $public" \
	--post-data '{"username": "smoke-from-container", "password": "Wrong-Password-1"}' http://kogge:8080/api/v1/auth/login 2>/dev/null || true
if [ -n "$ha" ]; then
	# Through the load balancer, which does not trust the container either: Kogge sees the container's address.
	docker run --rm --network "$network" --entrypoint wget "$image" -q -O /dev/null --header 'X-Forwarded-For: 198.51.100.9' \
		--header 'Content-Type: application/json' --header "Origin: $public" \
		--post-data '{"username": "smoke-via-lb", "password": "Wrong-Password-1"}' http://lb:8080/api/v1/auth/login 2>/dev/null || true
fi
events=$(api GET "/api/v1/admin/audit?action=auth.login.failed&limit=50")
from_host=$(jq -r '[.[] | select(.details.username == "smoke-from-host")][0].actorIp' <<<"$events")
from_container=$(jq -r '[.[] | select(.details.username == "smoke-from-container")][0].actorIp' <<<"$events")
echo "from the host with X-Forwarded-For 203.0.113.77: $from_host"
echo "from a container with X-Forwarded-For 198.51.100.9: $from_container"
[ "$from_host" = 203.0.113.77 ] || fail "the audit log shows $from_host for the request from the host"
case $from_container in
198.51.100.9 | "$gateway" | null | "") fail "the audit log shows $from_container for the request from another container" ;;
esac
if [ -n "$ha" ]; then
	via_lb=$(jq -r '[.[] | select(.details.username == "smoke-via-lb")][0].actorIp' <<<"$events")
	echo "from a container through the load balancer with X-Forwarded-For 198.51.100.9: $via_lb"
	case $via_lb in
	198.51.100.9 | "$gateway" | "$lb_address" | null | "") fail "the audit log shows $via_lb for the request through the load balancer" ;;
	esac
fi
