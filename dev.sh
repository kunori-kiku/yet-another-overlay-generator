#!/usr/bin/env bash
# dev.sh — start/stop the overlay generator dev environment
# Usage: ./dev.sh [start|stop|restart|status]

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

start_frontend() {
  if [ -f "$PIDFILE_FRONTEND" ] && kill -0 "$(cat "$PIDFILE_FRONTEND")" 2>/dev/null; then
    echo "Frontend already running (pid $(cat "$PIDFILE_FRONTEND"))"
    return
  fi
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
