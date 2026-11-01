#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# Builds a release of Kogge from the committed state of the working tree into dist/VERSION/:
#
#   kogge-VERSION-linux-amd64, kogge-VERSION-linux-arm64          the server with the web application
#   kogge-agent-VERSION-OS-ARCH[.exe]                             the build agent for every platform of AGENT_PLATFORMS
#   kogge-VERSION.cdx.json                                        the SBOM of the binaries (tools/sbom)
#   THIRD_PARTY_LICENSES.md, LICENSE, NOTICE, release-notes.md    the licences and the notes (tools/relnotes)
#   kogge-VERSION-linux-ARCH.tar                                  the offline bundle for this machine's architecture
#   SHA256SUMS                                                    the checksums of all of them
#
# and the images kogge:VERSION and kogge-agent:VERSION for this machine's architecture. The binaries come out of
# the same build as the images (deploy/Dockerfile, target release), cross-compiled without emulation. The release
# workflow (.github/workflows/release.yml) runs it for the files (KOGGE_RELEASE_IMAGES=0), builds the images for
# both architectures itself and signs and publishes everything; `make release-local VERSION=x.y.z` runs all of
# it here and publishes and signs nothing.
#
#   tools/release.sh VERSION
#
# Environment:
#   KOGGE_RELEASE_DIR          output directory (default dist/VERSION; must not exist)
#   KOGGE_RELEASE_IMAGES=0     leave out the images and the offline bundle
#   KOGGE_RELEASE_ALLOW_DIRTY=1  build although the working tree has changes that are not committed
#   KOGGE_SOURCE_URL           the source repository for the images' labels and the SBOM (default: git remote origin)
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

version=${1:-}
version=${version#v}
if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "usage: tools/release.sh VERSION (a semantic version such as 1.2.0 or 1.2.0-rc.1)" >&2
	exit 2
fi
if [ -n "$(git status --porcelain --untracked-files=no)" ] && [ "${KOGGE_RELEASE_ALLOW_DIRTY:-}" != 1 ]; then
	echo "the working tree has changes that are not committed; a release is built from a commit" \
		"(KOGGE_RELEASE_ALLOW_DIRTY=1 builds it anyway)" >&2
	exit 1
fi
commit=$(git rev-parse HEAD)
# The commit's date is the build's date: building the commit again gives the same binaries.
date=$(TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)
epoch=$(git log -1 --format=%ct)
source_url=${KOGGE_SOURCE_URL:-$(git config --get remote.origin.url || true)}
agent_platforms=$(sed -n 's/^AGENT_PLATFORMS ?= *//p' Makefile)
out=${KOGGE_RELEASE_DIR:-dist/$version}
if [ -e "$out" ]; then
	echo "$out exists: remove it or choose another KOGGE_RELEASE_DIR" >&2
	exit 1
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$out"
build_args=(--build-arg "VERSION=$version" --build-arg "COMMIT=$commit" --build-arg "DATE=$date"
	--build-arg "SOURCE_URL=$source_url" --build-arg "AGENT_PLATFORMS=$agent_platforms"
	--build-arg "SOURCE_DATE_EPOCH=$epoch")

echo "== Kogge $version from $commit ($date)"
for platform in linux/amd64 linux/arm64; do
	echo "== the files for $platform"
	docker build -f deploy/Dockerfile --platform "$platform" --target release "${build_args[@]}" \
		--output "type=local,dest=$work/${platform/\//-}" .
	cp "$work/${platform/\//-}/kogge" "$out/kogge-$version-${platform/\//-}"
done
# The agents, the notices and the SBOM do not depend on the server's platform: both builds have them, and they
# must agree.
for f in agents THIRD_PARTY_LICENSES.md sbom.cdx.json; do
	if ! diff -r "$work/linux-amd64/$f" "$work/linux-arm64/$f" >/dev/null; then
		echo "the builds for linux/amd64 and linux/arm64 made different $f" >&2
		exit 1
	fi
done
for agent in "$work"/linux-amd64/agents/kogge-agent-*; do
	name=$(basename "$agent")
	cp "$agent" "$out/kogge-agent-$version-${name#kogge-agent-}"
done
cp "$work/linux-amd64/THIRD_PARTY_LICENSES.md" LICENSE NOTICE "$out/"
cp "$work/linux-amd64/sbom.cdx.json" "$out/kogge-$version.cdx.json"
go run ./tools/relnotes -version "$version" HEAD >"$out/release-notes.md"

if [ "${KOGGE_RELEASE_IMAGES:-1}" != 0 ]; then
	echo "== the images for this machine's architecture"
	docker build -f deploy/Dockerfile "${build_args[@]}" -t "kogge:$version" .
	docker build -f deploy/Dockerfile --target agent "${build_args[@]}" -t "kogge-agent:$version" .
	echo "== the offline bundle"
	KOGGE_SKIP_BUILD=1 KOGGE_BUNDLE_DIR="$out" deploy/scripts/airgap-bundle.sh "$version"
fi

(cd "$out" && find . -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort | xargs sha256sum) >"$work/SHA256SUMS"
mv "$work/SHA256SUMS" "$out/SHA256SUMS"
echo "== release $version in $out:"
ls -l "$out"
