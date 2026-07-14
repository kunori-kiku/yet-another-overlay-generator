# syntax=docker/dockerfile:1
#
# Self-contained YAOG controller image: one container serves the panel (SPA) + the
# operator/panel API (:8080) + the agent API (:9090). State persists under /data
# (mount a volume). The AGENT is NOT containerized — it installs on the host via the
# one-shot bootstrap; this image is the CONTROLLER only.

# --- build the frontend (panel) ---
FROM node:20-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --legacy-peer-deps
COPY frontend/ ./
RUN npm run build

# --- build the server (static, CGO-free) ---
FROM golang:1.25-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
# BUILD_VERSION stamps the binary's `version` subcommand; pass --build-arg BUILD_VERSION=<tag> from
# the image build (the docker workflow forwards the release tag). EXTENDS the existing -s -w flags.
ARG BUILD_VERSION=dev
# Controller server build (framework-refactor plan-9 collapsed the server to a single build).
# There is no anonymous compute surface — the four /api/{validate,compile,export,deploy-script}
# routes were deleted — so no unauthenticated path reaches the compile pipeline. See
# docs/spec/operations/deployment-topology.md.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.BuildVersion=${BUILD_VERSION}" -o /out/yaog-server ./cmd/server

# Build the in-browser Go/WASM local engine (framework-refactor plan-4 made Go/WASM the default+only
# local engine). The frontend stage (node:alpine) has no Go, so the wasm is built HERE and COPYed into
# the served web root in the final stage. GOOS=js GOARCH=wasm resolves the go.mod toolchain (go1.26.5,
# the same toolchain that produced the committed web/wasm_exec.js) via GOTOOLCHAIN=auto — do NOT pin
# GOTOOLCHAIN=local. web/wasm_exec.js is the committed, toolchain-pinned runtime (COPY . . above).
RUN GOOS=js GOARCH=wasm go build -trimpath -o /out/web/yaog.wasm ./cmd/wasm \
    && cp web/wasm_exec.js /out/web/wasm_exec.js

# --- final image ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 65532 yaog \
    && mkdir -p /data \
    && chown -R yaog:yaog /data
COPY --from=backend /out/yaog-server /usr/local/bin/yaog-server
COPY --from=frontend --chown=yaog:yaog /app/frontend/dist /app/web
# The WASM local engine (plan-2): the panel fetches /yaog.wasm + /wasm_exec.js from the web root. The
# node:alpine frontend stage cannot build the wasm, so it comes from the Go backend stage — COPYed AFTER
# the dist so it lands in the served /app/web root. Without it, in-browser local design 404s.
COPY --from=backend --chown=yaog:yaog /out/web/yaog.wasm /out/web/wasm_exec.js /app/web/

# The server serves the panel from here, persists state here, and listens on both ports.
ENV YAOG_WEB_DIR=/app/web \
    YAOG_CONTROLLER_STATE_DIR=/data
EXPOSE 8080 9090
# Liveness probe: hit the public, unauthenticated /api/health on the operator/panel port
# (the one ungated route in the server build).
# busybox wget (in the alpine base) exits
# non-zero on connection failure or an HTTP error, which marks the container unhealthy so
# an orchestrator can restart/replace it. start-period covers the brief startup window.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/api/health || exit 1
# /data is owned by uid 65532 above. A fresh NAMED volume inherits that ownership; a
# BIND mount (the shipped docker-compose.yml uses ./data) does NOT — the host dir must
# be chowned to 65532 (documented in docker-compose.yml / docs/spec/controller/docker.md).
VOLUME ["/data"]
USER yaog
# ENTRYPOINT is the bare binary; CMD holds the serve flags. This way
# `docker compose run --rm controller create-operator ...` REPLACES the CMD, so argv is
# `yaog-server create-operator ...` and the subcommand dispatch in main.go fires. (An
# entrypoint with baked-in serve flags would instead APPEND the subcommand after them
# and silently keep serving — the documented first-operator bootstrap would not run.)
ENTRYPOINT ["/usr/local/bin/yaog-server"]
CMD ["--addr", ":8080", "--agent-addr", ":9090"]
