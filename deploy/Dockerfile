# SPDX-FileCopyrightText: 2026 The Kogge authors
# SPDX-License-Identifier: EUPL-1.2
#
# Build from the repository root:
#   docker build -f deploy/Dockerfile -t kogge .                            the server (with agent downloads)
#   docker build -f deploy/Dockerfile --target agent -t kogge-agent .       the build agent
#
# Build arguments: VERSION, COMMIT and DATE describe the build (the Makefile and tools/release.sh set them from
# Git; DATE is the commit's date, so that building a commit again gives the same binaries); SOURCE_URL is the
# source repository the images' labels name. The web application and the programs are built on the building
# machine's platform and cross-compiled: images for other platforms (docker buildx build --platform
# linux/amd64,linux/arm64) need emulation only to install the operating system's packages. Go's build cache
# and npm's download cache are cache mounts, not layers of the images' build.

FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build
# The notices of the npm packages shipped in the web application.
COPY tools/npm-licenses.mjs /src/tools/
RUN node /src/tools/npm-licenses.mjs /src/web --append /src/npm-notices.md

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
# The third-party notices: the Go modules, then the npm packages.
COPY --from=web /src/npm-notices.md /tmp/npm-notices.md
RUN --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out && go run ./tools/golicenses -o /out/THIRD_PARTY_LICENSES.md \
    && cat /tmp/npm-notices.md >> /out/THIRD_PARTY_LICENSES.md
ARG VERSION=dev
ARG COMMIT=
ARG DATE=
# The agent for the platforms of build hosts; the server offers them for download.
ARG AGENT_PLATFORMS="linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64"
# The stages built on this one see the build's description too.
ENV KOGGE_VERSION=${VERSION} KOGGE_COMMIT=${COMMIT} KOGGE_DATE=${DATE} KOGGE_AGENT_PLATFORMS=${AGENT_PLATFORMS} \
    LDFLAGS="-s -w -X kogge.de/kogge/internal/version.Version=${VERSION} -X kogge.de/kogge/internal/version.Commit=${COMMIT} -X kogge.de/kogge/internal/version.Date=${DATE}"
RUN --mount=type=cache,target=/root/.cache/go-build \
    for p in ${AGENT_PLATFORMS}; do \
      os=${p%/*}; arch=${p#*/}; ext=; [ "$os" = windows ] && ext=.exe; \
      GOOS=$os GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o /out/agents/kogge-agent-$os-$arch$ext ./cmd/kogge-agent \
        || exit 1; \
    done

# The server for the image's platform.
FROM build AS server-build
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags webui -ldflags "$LDFLAGS" -o /out/kogge ./cmd/kogge

# The operating systems of the images: Alpine Linux with what Kogge and the agent run (Git; Git LFS and the Docker
# CLI for the agent's container executor).
FROM alpine:3.22 AS server-base
RUN apk add --no-cache git ca-certificates tzdata \
    && addgroup -S -g 10001 kogge && adduser -S -u 10001 -G kogge -h /var/lib/kogge kogge \
    && mkdir -p /var/lib/kogge && chown kogge:kogge /var/lib/kogge

FROM alpine:3.22 AS agent-base
RUN apk add --no-cache git git-lfs ca-certificates tzdata docker-cli openssh-client \
    && addgroup -S -g 10002 kogge-agent && adduser -S -u 10002 -G kogge-agent -h /var/lib/kogge-agent kogge-agent \
    && mkdir -p /var/lib/kogge-agent && chown kogge-agent:kogge-agent /var/lib/kogge-agent

# The SBOMs of the images (tools/sbom): the Go modules of their programs, the npm packages of the web application
# (server only) and the Alpine packages of the image.
FROM build AS server-sbom
ARG TARGETOS
ARG TARGETARCH
ARG SOURCE_URL=
COPY --from=server-base /lib/apk/db/installed /rootfs/lib/apk/db/installed
COPY --from=server-base /etc/apk/world /rootfs/etc/apk/world
COPY --from=server-base /usr/lib/os-release /rootfs/usr/lib/os-release
RUN --mount=type=cache,target=/root/.cache/go-build \
    go run ./tools/sbom -name kogge -version "$KOGGE_VERSION" -commit "$KOGGE_COMMIT" \
      -date "${KOGGE_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" -source "$SOURCE_URL" \
      -server "$TARGETOS/$TARGETARCH" -agent "$KOGGE_AGENT_PLATFORMS" -rootfs /rootfs -o /out/sbom.cdx.json

FROM build AS agent-sbom
ARG TARGETOS
ARG TARGETARCH
ARG SOURCE_URL=
COPY --from=agent-base /lib/apk/db/installed /rootfs/lib/apk/db/installed
COPY --from=agent-base /etc/apk/world /rootfs/etc/apk/world
COPY --from=agent-base /usr/lib/os-release /rootfs/usr/lib/os-release
RUN --mount=type=cache,target=/root/.cache/go-build \
    go run ./tools/sbom -name kogge-agent -version "$KOGGE_VERSION" -commit "$KOGGE_COMMIT" \
      -date "${KOGGE_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" -source "$SOURCE_URL" \
      -server "" -web "" -agent "$TARGETOS/$TARGETARCH" -rootfs /rootfs -o /out/sbom.cdx.json

# The files of a release besides the images (tools/release.sh): the server for the target platform, the agents,
# the third-party notices and the SBOM of the binaries.
FROM build AS release-sbom
ARG SOURCE_URL=
RUN --mount=type=cache,target=/root/.cache/go-build \
    go run ./tools/sbom -name kogge -version "$KOGGE_VERSION" -commit "$KOGGE_COMMIT" \
      -date "${KOGGE_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" -source "$SOURCE_URL" -agent "$KOGGE_AGENT_PLATFORMS" \
      -o /out/release.cdx.json

FROM scratch AS release
COPY --from=server-build /out/kogge /kogge
COPY --from=build /out/agents /agents
COPY --from=build /out/THIRD_PARTY_LICENSES.md /THIRD_PARTY_LICENSES.md
COPY --from=release-sbom /out/release.cdx.json /sbom.cdx.json

# The build agent. Mount the work directory at the same path as on the host when the agent uses the host's Docker.
FROM agent-base AS agent
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG DATE=
ARG SOURCE_URL=
LABEL org.opencontainers.image.title="Kogge build agent" \
      org.opencontainers.image.description="Runs the pipeline jobs of Kogge on a build host" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.licenses="EUPL-1.2" \
      org.opencontainers.image.vendor="The Kogge authors" \
      org.opencontainers.image.base.name="docker.io/library/alpine:3.22"
COPY --from=build /out/agents/kogge-agent-${TARGETOS}-${TARGETARCH} /usr/local/bin/kogge-agent
COPY LICENSE NOTICE /usr/share/doc/kogge/
COPY --from=build /out/THIRD_PARTY_LICENSES.md /usr/share/doc/kogge/
COPY --from=agent-sbom /out/sbom.cdx.json /usr/share/doc/kogge/
USER kogge-agent
ENV KOGGE_AGENT_CONFIG=/var/lib/kogge-agent/config.json
VOLUME /var/lib/kogge-agent
ENTRYPOINT ["kogge-agent"]
CMD ["run"]

FROM server-base AS server
ARG VERSION=dev
ARG COMMIT=
ARG DATE=
ARG SOURCE_URL=
LABEL org.opencontainers.image.title="Kogge" \
      org.opencontainers.image.description="Issues and boards, wiki, Git hosting and pipelines on your own servers" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}" \
      org.opencontainers.image.source="${SOURCE_URL}" \
      org.opencontainers.image.licenses="EUPL-1.2" \
      org.opencontainers.image.vendor="The Kogge authors" \
      org.opencontainers.image.base.name="docker.io/library/alpine:3.22"
COPY --from=server-build /out/kogge /usr/local/bin/kogge
COPY --from=build /out/agents /usr/share/kogge/agents
COPY LICENSE NOTICE /usr/share/doc/kogge/
COPY --from=build /out/THIRD_PARTY_LICENSES.md /usr/share/doc/kogge/
COPY --from=server-sbom /out/sbom.cdx.json /usr/share/doc/kogge/
USER kogge
ENV KOGGE_DATA_DIR=/var/lib/kogge KOGGE_HTTP_ADDR=:8080 KOGGE_SSH_ADDR=:2222
VOLUME /var/lib/kogge
EXPOSE 8080 2222
ENTRYPOINT ["kogge"]
CMD ["serve"]
