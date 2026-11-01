#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# The results of the local CI pipeline (tools/ci/run.sh):
#
#   tools/ci/status.sh                       the latest runs, the run in progress and the commit waiting for one
#   tools/ci/status.sh REV                   the log of the run for REV
#   tools/ci/status.sh --passed PROFILE REV  succeeds if REV passed the pipeline of PROFILE (a full run counts for both)
set -eu

state=$(cd "$(git rev-parse --git-common-dir)" && pwd)/kogge-ci
results=$state/results.tsv

case ${1:-} in
--passed)
	[ $# -eq 3 ] || {
		echo "usage: tools/ci/status.sh --passed commit|full REV" >&2
		exit 2
	}
	sha=$(git rev-parse --verify --quiet "$3^{commit}") || exit 1
	[ -f "$results" ] || exit 1
	awk -F '\t' -v sha="$sha" -v profile="$2" \
		'$1 == sha && $3 == "pass" && ($2 == profile || $2 == "full") { ok = 1 } END { exit !ok }' "$results"
	exit
	;;
-h | --help)
	sed -n '7,9p' "$0" | sed 's/^# *//'
	exit 0
	;;
'') ;;
*)
	sha=$(git rev-parse --verify "$1^{commit}")
	if [ ! -f "$state/logs/$sha.log" ]; then
		echo "No log for $(git rev-parse --short "$sha") (the newest 50 are kept)." >&2
		exit 1
	fi
	cat "$state/logs/$sha.log"
	exit
	;;
esac

pid=$(cat "$state/lock/pid" 2>/dev/null || true)
if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && [ -f "$state/running" ]; then
	read -r sha profile since <"$state/running"
	echo "Running:  $(git log -1 --format='%h %s' "$sha" 2>/dev/null | cut -c1-70) ($profile, since $since)"
	echo "          tail -f $state/logs/$sha.log"
fi
if [ -s "$state/pending" ]; then
	read -r sha profile <"$state/pending"
	echo "Waiting:  $(git log -1 --format='%h %s' "$sha" 2>/dev/null | cut -c1-70) ($profile)"
fi
if [ ! -s "$results" ]; then
	echo "No runs yet. Run tools/ci/run.sh, or make hooks to run the pipeline after every commit."
	exit 0
fi
echo "Latest runs (newest last):"
tail -n "${KOGGE_CI_STATUS_LINES:-10}" "$results" | while IFS=$'\t' read -r sha profile status at seconds failed; do
	subject=$(git log -1 --format=%s "$sha" 2>/dev/null | cut -c1-55)
	printf '  %s  %-6s %-4s %3dm  %s %s%s\n' "${at%:*}" "$profile" "$status" $((seconds / 60)) "${sha:0:10}" "$subject" \
		"${failed:+  [failed: $failed]}"
done
echo "Logs: tools/ci/status.sh REV, or $state/logs"
