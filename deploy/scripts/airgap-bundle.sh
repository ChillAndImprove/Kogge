#!/bin/sh
# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# Packages Kogge for installation without internet access: the images of the Compose stack
# (docker save), the binaries (server and agents), the Compose files and configuration templates, the README, the
# licences and the SBOMs of the images, with SHA-256 checksums of every file.
#
#   deploy/scripts/airgap-bundle.sh [VERSION]        writes dist/kogge-VERSION-linux-ARCH.tar and its .sha256
#
# Environment:
#   KOGGE_BUNDLE_DIR       output directory (default: dist/)
#   KOGGE_SKIP_BUILD=1     use the existing images kogge:VERSION and kogge-agent:VERSION instead of building them
#   KOGGE_BUNDLE_PLATFORM  the platform of the bundle, e.g. linux/arm64 (default: the Docker host's). For another
#                          platform than the host's, the images of the stack are pulled for it, which replaces
#                          their tags in the local Docker: do that on a build machine only.
#   KOGGE_BUNDLE_IMAGES=0  leave out the images and take the binaries, THIRD_PARTY_LICENSES.md and the SBOM from
#                          `make build agents sbom`
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
version=${1:-$(git -C "$root" describe --tags --always --dirty 2>/dev/null | sed 's/^v\([0-9]\)/\1/')}
version=${version:-dev}
out=${KOGGE_BUNDLE_DIR:-$root/dist}
host=$(docker version -f '{{.Server.Os}}/{{.Server.Arch}}' 2>/dev/null || echo "$(go env GOOS)/$(go env GOARCH)")
platform=${KOGGE_BUNDLE_PLATFORM:-$host}
if [ "${KOGGE_BUNDLE_IMAGES:-1}" = 0 ]; then
  platform=$(go env GOOS)/$(go env GOARCH)
fi
name=kogge-$version-$(printf '%s' "$platform" | tr / -)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/$name
mkdir -p "$dir/images" "$dir/bin" "$dir/deploy" "$dir/sbom"

if [ "${KOGGE_BUNDLE_IMAGES:-1}" = 0 ]; then
  for f in THIRD_PARTY_LICENSES.md bin/sbom.cdx.json; do
    [ -f "$root/$f" ] || { echo "$f is missing: run make build agents sbom" >&2; exit 1; }
  done
  cp "$root/bin/kogge" "$dir/bin/kogge-$(go env GOOS)-$(go env GOARCH)"
  cp "$root"/bin/agents/kogge-agent-* "$dir/bin/"
  cp "$root/THIRD_PARTY_LICENSES.md" "$dir/"
  cp "$root/bin/sbom.cdx.json" "$dir/sbom/kogge.cdx.json"
  rmdir "$dir/images"
else
  if [ "${KOGGE_SKIP_BUILD:-}" != 1 ]; then
    commit=$(git -C "$root" rev-parse HEAD 2>/dev/null || true)
    date=$(TZ=UTC0 git -C "$root" log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null || true)
    for target in server agent; do
      tag=kogge:$version
      [ "$target" = agent ] && tag=kogge-agent:$version
      docker build -f "$root/deploy/Dockerfile" --platform "$platform" --target "$target" --build-arg VERSION="$version" \
        --build-arg COMMIT="$commit" --build-arg DATE="$date" -t "$tag" "$root"
    done
  fi
  # Kogge's images and those of the Compose stack (with the load balancers of several nodes), except the
  # development-only mail catcher.
  images="kogge:$version kogge-agent:$version"
  stack=$(sed -n 's/^[[:space:]]*image:[[:space:]]*\([^$[:space:]][^[:space:]]*\)[[:space:]]*$/\1/p' \
    "$root/deploy/compose.yaml" "$root/deploy/compose.ha.yaml" | sort -u)
  for image in $stack; do
    case $image in *mailpit*) continue ;; esac
    if [ "$platform" != "$host" ]; then
      docker pull --platform "$platform" "$image"
    else
      docker image inspect "$image" >/dev/null 2>&1 || docker pull "$image"
    fi
    images="$images $image"
  done
  # Docker's containerd image store keeps the images of several platforms under one name: save the bundle's.
  only=
  if docker save --help 2>&1 | grep -q -- '--platform'; then
    only="--platform $platform"
  fi
  for image in $images; do
    # An image for another platform would not start on the target.
    have=$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$image")
    if [ "$have" != "$platform" ]; then
      echo "$image is for $have, not for $platform" >&2
      exit 1
    fi
    echo "Saving $image"
    # shellcheck disable=SC2086 # $only is empty or two words
    docker save $only -o "$dir/images/$(printf '%s' "$image" | tr '/:' '__').tar" "$image"
  done
  # The binaries, the third-party notices and the SBOMs the build generated, taken from the images.
  arch=${platform#*/}
  container=$(docker create "kogge:$version")
  docker cp "$container:/usr/local/bin/kogge" "$dir/bin/kogge-linux-$arch"
  docker cp "$container:/usr/share/kogge/agents/." "$dir/bin/"
  docker cp "$container:/usr/share/doc/kogge/THIRD_PARTY_LICENSES.md" "$dir/"
  docker cp "$container:/usr/share/doc/kogge/sbom.cdx.json" "$dir/sbom/kogge.cdx.json"
  docker rm -v "$container" >/dev/null
  container=$(docker create "kogge-agent:$version")
  docker cp "$container:/usr/share/doc/kogge/sbom.cdx.json" "$dir/sbom/kogge-agent.cdx.json"
  docker rm -v "$container" >/dev/null
fi

# Deployment files, the README and the licences.
cp "$root/deploy/compose.yaml" "$root/deploy/compose.ha.yaml" "$root/deploy/.env.example" "$root/deploy/.gitignore" "$dir/deploy/"
cp -R "$root/deploy/db" "$root/deploy/keycloak" "$root/deploy/ha" "$root/deploy/monitoring" "$root/deploy/proxy" "$root/deploy/systemd" "$dir/deploy/"
mkdir -p "$dir/deploy/scripts"
cp "$root/deploy/scripts/init-secrets.sh" "$dir/deploy/scripts/"
printf '\n# The images of this bundle (loaded by install.sh).\nKOGGE_IMAGE=kogge:%s\n' "$version" >>"$dir/deploy/.env.example"
cp "$root/README.md" "$root/README.de.md" "$root/LICENSE" "$root/NOTICE" "$dir/"
printf '%s\n' "$platform" >"$dir/PLATFORM"

cat >"$dir/install.sh" <<'EOF'
#!/bin/sh
# Checks the bundle and loads its images into Docker. Then: cd deploy && scripts/init-secrets.sh, copy
# .env.example to .env and adjust it, and `docker compose up -d` (see README.md). sbom/ holds the SBOMs
# (CycloneDX) of the images, SHA256SUMS the checksums of every file.
set -eu
cd "$(dirname "$0")"
sha256sum -c --quiet SHA256SUMS
platform=$(cat PLATFORM)
if [ -d images ]; then
  host=$(docker version -f '{{.Server.Os}}/{{.Server.Arch}}')
  if [ "$host" != "$platform" ]; then
    echo "This bundle is for $platform, but Docker runs on $host: use the bundle for $host." >&2
    exit 1
  fi
  for image in images/*.tar; do
    [ -e "$image" ] || continue
    docker load -i "$image"
  done
  echo "The bundle is complete and its images are loaded."
else
  echo "The bundle is complete."
fi
EOF
chmod +x "$dir/install.sh"

(cd "$dir" && find . -type f -exec sha256sum {} + | sed 's|  \./|  |' | LC_ALL=C sort -k 2) >"$work/SHA256SUMS"
mv "$work/SHA256SUMS" "$dir/SHA256SUMS"
mkdir -p "$out"
tar -C "$work" -cf "$out/$name.tar" "$name"
(cd "$out" && sha256sum "$name.tar" >"$name.tar.sha256")
echo "Bundle written to $out/$name.tar"
