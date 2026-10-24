# syntax=docker/dockerfile:1.7
#
# Deed container images. Adapted from Porch's Dockerfile (Apache-2.0).
#
#   web    : builds the SPA into internal/webui/dist from the locked pnpm dependency tree (typecheck + vite build +
#            scripts/check-dist.mjs, which fails on dev-only code in the bundle).
#   build  : static Go binaries (CGO_ENABLED=0, -trimpath), cross-compiled on the build platform.
#   deed   : distroless static (Debian 13), non-root UID/GID 65532, no shell, no package manager.  (default target)
#   agent  : deed-agent on the same base.                                                   (--target agent)
#
# Base images are pinned by digest. Update with: docker buildx imagetools inspect <image>:<tag>
#
# Runtime layout:
#   /usr/local/bin/deed      binary (root-owned, read-only)
#   /etc/deed/deed.yaml      configuration (deploy/deed.container.yaml baked in; mount your own read-only over it)
#   /var/lib/deed            data volume (SQLite, pre-migration snapshots), 0700 65532:65532
#   /run/secrets/deed_kek    KEK (compose secret); never inside the data volume
#
# Build: docker build --build-arg VERSION=v0.1.0 --build-arg COMMIT=$(git rev-parse HEAD) \
#          --build-arg BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) -t deed:local .

ARG NODE_IMAGE=docker.io/library/node:24.22.0-trixie-slim@sha256:a6155384446670e9719bdc4fd0ebe4d4b29fb32a8d6aa523da3547a6cfbcf22c
ARG GO_IMAGE=docker.io/library/golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

# ---------------------------------------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
ENV CI=true \
    COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    PNPM_HOME=/pnpm \
    PATH=/pnpm:$PATH
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && corepack install
RUN --mount=type=cache,id=deed-pnpm-store,target=/pnpm/store \
    pnpm config set store-dir /pnpm/store && \
    pnpm install --frozen-lockfile
COPY web/ ./
COPY scripts/ /src/scripts/
COPY api/openapi.json /src/api/openapi.json
RUN mkdir -p /src/internal/webui/dist && pnpm build

# ---------------------------------------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/
COPY --from=web /src/internal/webui/dist/ internal/webui/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    for b in deed deed-agent; do \
      GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
        -ldflags "-s -w -buildid= \
          -X github.com/aurimasniekis/go-deed/internal/buildinfo.Version=${VERSION} \
          -X github.com/aurimasniekis/go-deed/internal/buildinfo.Commit=${COMMIT} \
          -X github.com/aurimasniekis/go-deed/internal/buildinfo.Date=${BUILD_DATE}" \
        -o /out/$b ./cmd/$b || exit 1; \
    done
RUN install -d -m 0700 -o 65532 -g 65532 /rootfs/var/lib/deed /rootfs/var/lib/deed-kek && \
    install -d -m 0755 -o 0 -g 0 /rootfs/etc/deed
COPY --chmod=0444 deploy/deed.container.yaml /rootfs/etc/deed/deed.yaml

# ---------------------------------------------------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE} AS agent
ARG VERSION=dev
ARG COMMIT=
LABEL org.opencontainers.image.title="deed-agent" \
      org.opencontainers.image.description="Deed reference collector (Docker) for push sources" \
      org.opencontainers.image.source="https://github.com/aurimasniekis/go-deed" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --chown=0:0 --chmod=0444 LICENSE NOTICE /usr/share/doc/deed/
COPY --from=build --chown=0:0 --chmod=0555 /out/deed-agent /usr/local/bin/deed-agent
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/deed-agent"]

# ---------------------------------------------------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE} AS deed
ARG VERSION=dev
ARG COMMIT=
LABEL org.opencontainers.image.title="deed" \
      org.opencontainers.image.description="Deed: source-aware graph of infrastructure, software, identities and operational relationships" \
      org.opencontainers.image.source="https://github.com/aurimasniekis/go-deed" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian13:nonroot" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --chown=0:0 --chmod=0444 LICENSE NOTICE /usr/share/doc/deed/
COPY --from=build --chown=0:0 --chmod=0555 /out/deed /usr/local/bin/deed
COPY --from=build /rootfs/ /
ENV DEED_CONFIG=/etc/deed/deed.yaml
USER 65532:65532
WORKDIR /var/lib/deed
VOLUME ["/var/lib/deed"]
# Public listener. The management listener (metrics, /healthz, /readyz) is 0.0.0.0:9464 inside the container so a
# Prometheus on the compose network can scrape it; never publish that port.
EXPOSE 8080/tcp
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/deed", "healthcheck", "-url", "http://127.0.0.1:9464/readyz"]
ENTRYPOINT ["/usr/local/bin/deed"]
CMD ["serve"]
