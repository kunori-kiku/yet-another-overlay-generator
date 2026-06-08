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
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/yaog-server ./cmd/server

# --- final image ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 65532 yaog \
    && mkdir -p /data \
    && chown -R yaog:yaog /data
COPY --from=backend /out/yaog-server /usr/local/bin/yaog-server
COPY --from=frontend --chown=yaog:yaog /app/frontend/dist /app/web

# The server serves the panel from here, persists state here, and listens on both ports.
ENV YAOG_WEB_DIR=/app/web \
    YAOG_CONTROLLER_STATE_DIR=/data
EXPOSE 8080 9090
# /data is owned by uid 65532 above, so a fresh named volume inherits that ownership.
VOLUME ["/data"]
USER yaog
# ENTRYPOINT is the bare binary; CMD holds the serve flags. This way
# `docker compose run --rm controller create-operator ...` REPLACES the CMD, so argv is
# `yaog-server create-operator ...` and the subcommand dispatch in main.go fires. (An
# entrypoint with baked-in serve flags would instead APPEND the subcommand after them
# and silently keep serving — the documented first-operator bootstrap would not run.)
ENTRYPOINT ["/usr/local/bin/yaog-server"]
CMD ["--addr", ":8080", "--agent-addr", ":9090"]
