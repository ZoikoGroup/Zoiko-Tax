# syntax=docker/dockerfile:1.10
#
# ZoikoTax fiscal core.
#
# Three properties this image has to hold, and why:
#
#   Reproducible   ADR-0001 §3.3 makes reproducible builds part of the C3 replay
#                  guarantee. -trimpath, a zeroed buildid and CGO_ENABLED=0 mean
#                  the same source and toolchain produce the same bytes.
#   Minimal        ADR-0001 §3.2 chose Go partly for single-binary deployment.
#                  The runtime stage has no shell, no package manager and no libc
#                  beyond what a static binary needs — nothing to exploit and
#                  nothing to drift.
#   Configless     ADR-0017 §2.1: configuration is environment variables only.
#                  There is no config file in this image, and no secret.
#
# Version identity does NOT come from a linker flag. The seven release-train
# versions arrive as environment variables and are read once at boot (ADR-0015
# §2.6), so a rebuild cannot silently disagree with the deployment record.

ARG GO_VERSION=1.25
ARG BUILDER_IMAGE=golang:${GO_VERSION}-bookworm
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

# -----------------------------------------------------------------------------
# build
# -----------------------------------------------------------------------------
FROM ${BUILDER_IMAGE} AS build

WORKDIR /src

# CGO off gives a static binary with no runtime linkage — the single-binary
# deployment ADR-0001 §3.3 relies on for a clean regional-cell footprint.
ENV CGO_ENABLED=0 \
    GOOS=linux

COPY . .

# If vendor/ is present (ADR-0001 control 6) the Go toolchain uses it
# automatically and this build touches no network. That is the intended steady
# state; the download path exists only until `make vendor` has been run.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    if [ ! -d vendor ]; then go mod download; fi; \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -buildid=" \
      -o /out/ \
      ./cmd/...

# -----------------------------------------------------------------------------
# ztax-core — the regional cell binary (ADR-0009 §2.1)
# -----------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE} AS ztax-core

COPY --from=build /out/ztax-core /usr/local/bin/ztax-core

# distroless nonroot is uid/gid 65532. Named rather than numeric so the intent
# survives a base-image change.
USER nonroot:nonroot

EXPOSE 8080

# No HEALTHCHECK: there is no shell and no curl in this image, by design.
# Liveness and readiness are HTTP probes against /healthz and /readyz, owned by
# the orchestrator (W1 lane B, Kubernetes baseline).

ENTRYPOINT ["/usr/local/bin/ztax-core"]

# -----------------------------------------------------------------------------
# ztax-migrate — the schema runner (ADR-0008 §2.8, ADR-0009 §2.2)
# -----------------------------------------------------------------------------
# A separate image because it runs under a DDL role the application never holds.
# The separation is the control: a compromise of ztax-core yields no ability to
# alter the schema, and a migration cannot run inside a request.
#
# It carries its migrations embedded (backend/migrations), so a container finds
# exactly the migrations it was built with rather than whatever a mounted volume
# happens to hold.
FROM ${RUNTIME_IMAGE} AS ztax-migrate

COPY --from=build /out/ztax-migrate /usr/local/bin/ztax-migrate

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/ztax-migrate"]
CMD ["up"]

# -----------------------------------------------------------------------------
# A stage for ztax-outbox-relay (ADR-0014 §2.2) is added when that command
# exists.
# -----------------------------------------------------------------------------
