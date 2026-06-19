#!/usr/bin/env bash
# dev.sh — start/stop the overlay generator dev environment
# Usage: ./dev.sh [start|stop|restart|status]
#
# Backend: the DEFAULT (controller-only) server (`go run ./cmd/server/`, no -tags airgap) on
# :8080 — the four anonymous air-gap compute routes (/api/validate|compile|export|deploy-script)
# are gated off this build behind //go:build airgap (plan-7 / 1.7). LOCAL design now compiles
# entirely IN-BROWSER (the plan-4 TS compiler, default-ON), so the Vite dev server only ever
# talks to the backend for /api/health + the controller (operator/agent) routes. To exercise the
# air-gap routes locally, run the oracle instead: `go run -tags airgap ./cmd/server/` (or
# `cmd/compiler` for the CLI). See docs/spec/operations/deployment-topology.md.

set -euo pipefail
cd "$(dirname "$0")"

PIDFILE_BACKEND=".dev-backend.pid"
PIDFILE_FRONTEND=".dev-frontend.pid"
LOG_BACKEND=".dev-backend.log"
LOG_FRONTEND=".dev-frontend.log"

start_backend() {
  if [ -f "$PIDFILE_BACKEND" ] && kill -0 "$(cat "$PIDFILE_BACKEND")" 2>/dev/null; then
    echo "Backend already running (pid $(cat "$PIDFILE_BACKEND"))"
    return
  fi
  echo "Starting Go backend on :8080..."
  nohup go run ./cmd/server/ > "$LOG_BACKEND" 2>&1 &
  echo $! > "$PIDFILE_BACKEND"
  disown
  echo "Backend started (pid $!) — log: $LOG_BACKEND"
}

# Install frontend deps only when needed: first run (no node_modules) or
# package-lock.json changed since the last successful install (e.g. after a
# pull that added a dependency). The stamp lives inside node_modules so a
# wiped node_modules naturally forces a reinstall. Without this, `npx vite`
# on a fresh clone silently fetches vite into the npx cache and then dies
# resolving the config's imports against an empty node_modules.
ensure_frontend_deps() {
  local lock="frontend/package-lock.json"
  local stamp="frontend/node_modules/.dev-deps-hash"
  local want
  want=$(sha256sum "$lock" | cut -d' ' -f1)
  if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$want" ]; then
    return
  fi
  echo "Installing frontend dependencies (first run or package-lock.json changed)..."
  (cd frontend && npm install --legacy-peer-deps)
  echo "$want" > "$stamp"
}

start_frontend() {
  if [ -f "$PIDFILE_FRONTEND" ] && kill -0 "$(cat "$PIDFILE_FRONTEND")" 2>/dev/null; then
    echo "Frontend already running (pid $(cat "$PIDFILE_FRONTEND"))"
    return
  fi
  ensure_frontend_deps
  echo "Starting Vite frontend on :5173..."
  nohup bash -c 'cd frontend && npx vite --host' > "$LOG_FRONTEND" 2>&1 &
  echo $! > "$PIDFILE_FRONTEND"
  disown
  echo "Frontend started (pid $!) — log: $LOG_FRONTEND"
}

stop_all() {
  for pidfile in "$PIDFILE_BACKEND" "$PIDFILE_FRONTEND"; do
    if [ -f "$pidfile" ]; then
      pid=$(cat "$pidfile")
      if kill -0 "$pid" 2>/dev/null; then
        echo "Stopping pid $pid..."
        kill "$pid" 2>/dev/null || true
        pkill -P "$pid" 2>/dev/null || true
      fi
      rm -f "$pidfile"
    fi
  done
  # Belt-and-suspenders: kill any lingering processes on dev ports
  lsof -ti:8080 2>/dev/null | xargs -r kill 2>/dev/null || true
  lsof -ti:5173 2>/dev/null | xargs -r kill 2>/dev/null || true
  echo "Stopped."
}

show_status() {
  for label_pidfile in "Backend:$PIDFILE_BACKEND" "Frontend:$PIDFILE_FRONTEND"; do
    label="${label_pidfile%%:*}"
    pidfile="${label_pidfile#*:}"
    if [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
      echo "$label: running (pid $(cat "$pidfile"))"
    else
      echo "$label: stopped"
      rm -f "$pidfile"
    fi
  done
}

case "${1:-start}" in
  start)
    start_backend
    sleep 2
    start_frontend
    sleep 2
    echo ""
    show_status
    echo ""
    echo "Ready! Open http://localhost:5173/"
    echo "Logs:  tail -f $LOG_BACKEND $LOG_FRONTEND"
    ;;
  stop)
    stop_all
    ;;
  restart)
    stop_all
    sleep 1
    start_backend
    sleep 2
    start_frontend
    sleep 2
    echo ""
    show_status
    echo ""
    echo "Ready! Open http://localhost:5173/"
    ;;
  status)
    show_status
    ;;
  logs)
    tail -f "$LOG_BACKEND" "$LOG_FRONTEND"
    ;;
  *)
    echo "Usage: $0 [start|stop|restart|status|logs]"
    exit 1
    ;;
esac
