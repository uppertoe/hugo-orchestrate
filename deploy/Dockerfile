# syntax=docker/dockerfile:1

# Stage 1: static orchestrator binary (cross-compiled on the build platform).
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/orchestrator ./cmd/orchestrator

# Stage 2: download + sha256-verify the Hugo versions in the manifest.
# Runs on the build platform but fetches TARGETARCH tarballs, so multi-arch
# builds stay fast under emulation-free cross builds.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS hugo-installer
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY deploy/install-hugo/ deploy/install-hugo/
COPY deploy/hugo-manifest.txt /etc/orchestrator/hugo-manifest.txt
RUN go run ./deploy/install-hugo \
    --manifest /etc/orchestrator/hugo-manifest.txt \
    --dest /opt/hugo \
    --arch "$TARGETARCH"

# Stage 3: runtime. Not distroless/static: we shell out to the real git
# binary (Hugo themes are commonly git submodules), and hugo_extended is
# dynamically linked against libstdc++.
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 65532 orchestrator \
    && useradd -u 65532 -g 65532 -M -s /usr/sbin/nologin orchestrator \
    # Pre-create the static root so named volumes inherit non-root ownership;
    # host bind mounts still need a manual chown 65532:65532 (see README).
    && install -d -o 65532 -g 65532 /srv/static

COPY --from=build /out/orchestrator /usr/local/bin/orchestrator
COPY --from=hugo-installer /opt/hugo /opt/hugo
COPY deploy/hugo-manifest.txt /etc/orchestrator/hugo-manifest.txt

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/orchestrator"]
