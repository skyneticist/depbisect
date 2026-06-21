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

# Runtime image carries everything depbisect drives: git for worktrees, plus
# node, npm, and pnpm (via corepack) for installing candidate dependencies and
# running verification commands. Debian slim keeps glibc for native npm modules.
FROM node:20-slim
# Without a pin, corepack fetches the *latest* pnpm on first use; current pnpm
# requires Node >=22 and crashes on this image's Node 20 (No such built-in
# module: node:sqlite). Pin a pnpm that supports Node 20 and bake it into the
# image so the package manager works offline, with no download at run time.
ENV COREPACK_DEFAULT_TO_LATEST=0
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && corepack enable \
 && corepack prepare pnpm@9.15.4 --activate \
 && git config --system --add safe.directory '*'
COPY --from=build /out/depbisect /usr/local/bin/depbisect
WORKDIR /work
ENTRYPOINT ["depbisect"]
CMD ["help"]
