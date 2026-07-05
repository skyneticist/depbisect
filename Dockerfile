# syntax=docker/dockerfile:1

# Cross-compile the static binary for the requested target platform. Running the
# build stage on the native BUILDPLATFORM (and letting Go cross-compile via
# TARGETOS/TARGETARCH) keeps multi-arch builds fast — no emulation for the build.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=docker
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/depbisect ./cmd/depbisect

# ---------------------------------------------------------------------------
# Runtime variants: one image per ecosystem, so each pull stays as small as
# its toolchain allows. Every variant carries git (worktrees) with
# safe.directory '*' (mounted repos are owned by the host user). Debian keeps
# glibc for native modules and prebuilt toolchains.
#
# The js variant is intentionally LAST: a bare `docker build .` (Makefile,
# docs, CI docker-smoke) builds the final stage, so the default image stays
# the JavaScript one that :latest has always pointed at. docker.yml selects
# the other variants with --target.
# ---------------------------------------------------------------------------

# uv ships as a distroless binary image. Pulling it through a FROM stage
# (rather than COPY --from=<ref>) keeps the pin visible to dependabot.
FROM ghcr.io/astral-sh/uv:0.11 AS uv-dist

# --- go: bisect go.mod projects ---------------------------------------------
# golang:bookworm is buildpack-deps based, so git, ca-certificates, and a C
# toolchain (for cgo test builds) are already present.
FROM golang:1.25-bookworm AS go
RUN git config --system --add safe.directory '*'
COPY --from=build /out/depbisect /usr/local/bin/depbisect
WORKDIR /work
ENTRYPOINT ["depbisect"]
CMD ["help"]

# --- rust: bisect Cargo.toml projects ---------------------------------------
# rust:slim includes cargo, rustc, and the gcc linker, but not git.
FROM rust:1-slim-bookworm AS rust
RUN apt-get update \
 && apt-get upgrade -y \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && git config --system --add safe.directory '*'
COPY --from=build /out/depbisect /usr/local/bin/depbisect
WORKDIR /work
ENTRYPOINT ["depbisect"]
CMD ["help"]

# --- python: bisect pyproject.toml projects with uv --------------------------
# Matches the interpreter CI tests against (setup-python "3.12"). uv is told
# to use it rather than downloading a managed interpreter at run time.
FROM python:3.12-slim AS python
ENV UV_PYTHON_PREFERENCE=only-system
RUN apt-get update \
 && apt-get upgrade -y \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && git config --system --add safe.directory '*'
COPY --from=uv-dist /uv /uvx /usr/local/bin/
COPY --from=build /out/depbisect /usr/local/bin/depbisect
WORKDIR /work
ENTRYPOINT ["depbisect"]
CMD ["help"]

# --- js (default): bisect package.json projects with npm, pnpm, or yarn ------
# Node 22 is the active LTS (Node 20 reached end-of-life in April 2026).
FROM node:22-slim AS js
# Pin pnpm and classic yarn and bake them into the image: without a pin,
# corepack fetches the *latest* release on first use — a network download at
# run time and a moving target that has broken this image before (a pnpm too
# new for the image's Node crashed on first use). Baked-in known-good versions
# work offline and fail loudly in CI's docker-smoke if an upgrade misbehaves.
# Repos that declare a Berry yarn via package.json's packageManager field are
# still honored: corepack intercepts and fetches that exact version at run
# time (which needs network, as Berry installs generally do anyway).
ENV COREPACK_DEFAULT_TO_LATEST=0
RUN apt-get update \
 && apt-get upgrade -y \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && corepack enable \
 && corepack prepare pnpm@9.15.4 --activate \
 && corepack prepare yarn@1.22.22 --activate \
 && git config --system --add safe.directory '*'
COPY --from=build /out/depbisect /usr/local/bin/depbisect
WORKDIR /work
ENTRYPOINT ["depbisect"]
CMD ["help"]
