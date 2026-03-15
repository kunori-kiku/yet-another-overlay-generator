#!/usr/bin/env bash
set -euo pipefail

# One-click local deployment for YAOG release assets.
# Downloads a prebuilt platform bundle into a subdirectory under current directory,
# then generates start/stop scripts for backend + frontend.

OWNER="kunori-kiku"
REPO="yet-another-overlay-generator"
TAG=""
DEPLOY_DIR=""
SKIP_FRONTEND="false"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/deploy.sh [options]

Options:
  --tag <tag>             Release tag to deploy, e.g. v0.3.0 (default: latest release)
  --owner <owner>         GitHub owner (default: kunori-kiku)
  --repo <repo>           GitHub repo (default: yet-another-overlay-generator)
  --dir <dir>             Deploy subdirectory (default: ./yaog-<tag>)
  --skip-frontend         Remove frontend files after extraction
  -h, --help              Show this help
EOF
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: command not found: $cmd" >&2
    exit 1
  fi
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --tag)
        TAG="$2"
        shift 2
        ;;
      --owner)
        OWNER="$2"
        shift 2
        ;;
      --repo)
        REPO="$2"
        shift 2
        ;;
      --dir)
        DEPLOY_DIR="$2"
        shift 2
        ;;
      --skip-frontend)
        SKIP_FRONTEND="true"
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "ERROR: unknown argument: $1" >&2
        usage
        exit 1
        ;;
    esac
  done
}

detect_arch() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64)
      echo "amd64"
      ;;
    aarch64|arm64)
      echo "arm64"
      ;;
    i386|i486|i586|i686)
      echo "386"
      ;;
    armv7l|armv7)
      echo "armv7"
      ;;
    *)
      echo "ERROR: unsupported architecture: $machine" >&2
      exit 1
      ;;
  esac
}

detect_platform_id() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(detect_arch)"

  case "$os" in
    linux)
      echo "linux-${arch}"
      ;;
    *)
      echo "ERROR: unsupported OS for deploy.sh: $os" >&2
      exit 1
      ;;
  esac
}

download_asset() {
  local asset="$1"
  local out="$2"
  local url="https://github.com/${OWNER}/${REPO}/releases/download/${TAG}/${asset}"
  echo "Downloading ${asset}..."
  curl -fL "$url" -o "$out"
}

resolve_latest_tag() {
  local api_url="https://api.github.com/repos/${OWNER}/${REPO}/releases/latest"
  echo "Resolving latest release tag from GitHub..."
  local latest
  latest="$(curl -fsSL "$api_url" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  if [[ -z "$latest" ]]; then
    echo "ERROR: failed to resolve latest release tag from $api_url" >&2
    exit 1
  fi
  TAG="$latest"
}

write_runtime_files() {
  local dir="$1"

  cat > "$dir/run_frontend.py" <<'PYEOF'
#!/usr/bin/env python3
import os
import posixpath
import urllib.error
import urllib.request
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

FRONTEND_DIR = os.environ.get("YAOG_FRONTEND_DIR", ".")
BACKEND_BASE = os.environ.get("YAOG_BACKEND_BASE", "http://127.0.0.1:8080")
FRONTEND_HOST = os.environ.get("YAOG_FRONTEND_HOST", "127.0.0.1")
FRONTEND_PORT = int(os.environ.get("YAOG_FRONTEND_PORT", "5173"))

class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=FRONTEND_DIR, **kwargs)

    def do_OPTIONS(self):
        if self.path.startswith("/api/"):
            self.send_response(204)
            self.send_header("Access-Control-Allow-Origin", "*")
            self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
            self.send_header("Access-Control-Allow-Headers", "Content-Type")
            self.end_headers()
            return
        super().do_OPTIONS()

    def _proxy_api(self):
        req_url = BACKEND_BASE.rstrip("/") + self.path
        body = None
        content_length = self.headers.get("Content-Length")
        if content_length:
            body = self.rfile.read(int(content_length))

        req = urllib.request.Request(req_url, data=body, method=self.command)
        for h in ["Content-Type", "Accept"]:
            val = self.headers.get(h)
            if val:
                req.add_header(h, val)

        try:
            with urllib.request.urlopen(req) as resp:
                self.send_response(resp.status)
                for k, v in resp.getheaders():
                    if k.lower() in ("connection", "transfer-encoding"):
                        continue
                    self.send_header(k, v)
                self.end_headers()
                self.wfile.write(resp.read())
        except urllib.error.HTTPError as e:
            self.send_response(e.code)
            self.end_headers()
            self.wfile.write(e.read())
        except Exception as e:
            self.send_response(502)
            self.end_headers()
            self.wfile.write(f"Proxy error: {e}".encode("utf-8"))

    def do_GET(self):
        if self.path.startswith("/api/"):
            self._proxy_api()
            return

        full_path = self.translate_path(self.path)
        if os.path.isdir(full_path):
            index_path = os.path.join(full_path, "index.html")
            if os.path.exists(index_path):
                self.path = posixpath.join(self.path.rstrip("/"), "index.html")
                return super().do_GET()

        if os.path.exists(full_path):
            return super().do_GET()

        self.path = "/index.html"
        return super().do_GET()

    def do_POST(self):
        if self.path.startswith("/api/"):
            self._proxy_api()
            return
        self.send_response(404)
        self.end_headers()

if __name__ == "__main__":
    server = ThreadingHTTPServer((FRONTEND_HOST, FRONTEND_PORT), Handler)
    print(f"Frontend serving on http://{FRONTEND_HOST}:{FRONTEND_PORT}")
    print(f"Proxying /api/* to {BACKEND_BASE}")
    server.serve_forever()
PYEOF

  cat > "$dir/start.sh" <<'SHEOF'
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_DIR="$SCRIPT_DIR/run"
LOG_DIR="$SCRIPT_DIR/logs"
mkdir -p "$RUN_DIR" "$LOG_DIR"

BACKEND_PORT="${BACKEND_PORT:-8080}"
FRONTEND_PORT="${FRONTEND_PORT:-5173}"
BACKEND_HOST="${BACKEND_HOST:-127.0.0.1}"
FRONTEND_HOST="${FRONTEND_HOST:-127.0.0.1}"

if [ -f "$RUN_DIR/backend.pid" ] && kill -0 "$(cat "$RUN_DIR/backend.pid")" 2>/dev/null; then
  echo "Backend already running (PID: $(cat "$RUN_DIR/backend.pid"))"
else
  nohup "$SCRIPT_DIR/bin/yaog-server" -addr ":$BACKEND_PORT" > "$LOG_DIR/backend.log" 2>&1 &
  echo $! > "$RUN_DIR/backend.pid"
  echo "Started backend PID $(cat "$RUN_DIR/backend.pid")"
fi

if [ -f "$RUN_DIR/frontend.pid" ] && kill -0 "$(cat "$RUN_DIR/frontend.pid")" 2>/dev/null; then
  echo "Frontend already running (PID: $(cat "$RUN_DIR/frontend.pid"))"
else
  export YAOG_FRONTEND_DIR="$SCRIPT_DIR/frontend"
  export YAOG_BACKEND_BASE="http://${BACKEND_HOST}:${BACKEND_PORT}"
  export YAOG_FRONTEND_HOST="$FRONTEND_HOST"
  export YAOG_FRONTEND_PORT="$FRONTEND_PORT"
  nohup python3 "$SCRIPT_DIR/run_frontend.py" > "$LOG_DIR/frontend.log" 2>&1 &
  echo $! > "$RUN_DIR/frontend.pid"
  echo "Started frontend PID $(cat "$RUN_DIR/frontend.pid")"
fi

echo ""
echo "Frontend: http://${FRONTEND_HOST}:${FRONTEND_PORT}"
echo "Backend:  http://${BACKEND_HOST}:${BACKEND_PORT}"
echo "Logs:     $LOG_DIR"
SHEOF

  cat > "$dir/stop.sh" <<'SHEOF'
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_DIR="$SCRIPT_DIR/run"

stop_pidfile() {
  local name="$1"
  local pidfile="$RUN_DIR/$name.pid"
  if [ -f "$pidfile" ]; then
    local pid
    pid="$(cat "$pidfile")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" || true
      echo "Stopped $name (PID $pid)"
    fi
    rm -f "$pidfile"
  fi
}

stop_pidfile "frontend"
stop_pidfile "backend"
SHEOF

  cat > "$dir/start.ps1" <<'PSEOF'
#!/usr/bin/env pwsh
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RunDir = Join-Path $ScriptDir "run"
$LogDir = Join-Path $ScriptDir "logs"
New-Item -ItemType Directory -Force -Path $RunDir | Out-Null
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

$BackendPort = if ($env:BACKEND_PORT) { $env:BACKEND_PORT } else { "8080" }
$FrontendPort = if ($env:FRONTEND_PORT) { $env:FRONTEND_PORT } else { "5173" }
$BackendHost = if ($env:BACKEND_HOST) { $env:BACKEND_HOST } else { "127.0.0.1" }
$FrontendHost = if ($env:FRONTEND_HOST) { $env:FRONTEND_HOST } else { "127.0.0.1" }

$backendPidFile = Join-Path $RunDir "backend.pid"
$frontendPidFile = Join-Path $RunDir "frontend.pid"

function Test-AlivePid([string]$PidFile) {
    if (-not (Test-Path $PidFile)) { return $false }
    $pid = Get-Content $PidFile -Raw
    if ([string]::IsNullOrWhiteSpace($pid)) { return $false }
    try {
        $null = Get-Process -Id ([int]$pid) -ErrorAction Stop
        return $true
    } catch {
        return $false
    }
}

if (Test-AlivePid $backendPidFile) {
    Write-Host "Backend already running (PID: $(Get-Content $backendPidFile -Raw))"
} else {
    $backendProc = Start-Process -FilePath (Join-Path $ScriptDir "bin/yaog-server") -ArgumentList @("-addr", ":$BackendPort") -RedirectStandardOutput (Join-Path $LogDir "backend.log") -RedirectStandardError (Join-Path $LogDir "backend.log") -PassThru
    Set-Content -Path $backendPidFile -Value $backendProc.Id
    Write-Host "Started backend PID $($backendProc.Id)"
}

if (Test-AlivePid $frontendPidFile) {
    Write-Host "Frontend already running (PID: $(Get-Content $frontendPidFile -Raw))"
} else {
    $env:YAOG_FRONTEND_DIR = (Join-Path $ScriptDir "frontend")
    $env:YAOG_BACKEND_BASE = "http://$BackendHost`:$BackendPort"
    $env:YAOG_FRONTEND_HOST = $FrontendHost
    $env:YAOG_FRONTEND_PORT = $FrontendPort
    $frontendProc = Start-Process -FilePath "python3" -ArgumentList @((Join-Path $ScriptDir "run_frontend.py")) -RedirectStandardOutput (Join-Path $LogDir "frontend.log") -RedirectStandardError (Join-Path $LogDir "frontend.log") -PassThru
    Set-Content -Path $frontendPidFile -Value $frontendProc.Id
    Write-Host "Started frontend PID $($frontendProc.Id)"
}

Write-Host ""
Write-Host "Frontend: http://$FrontendHost`:$FrontendPort"
Write-Host "Backend:  http://$BackendHost`:$BackendPort"
Write-Host "Logs:     $LogDir"
PSEOF

  chmod +x "$dir/run_frontend.py" "$dir/start.sh" "$dir/stop.sh"
}

main() {
  parse_args "$@"

  if [[ -z "$TAG" ]]; then
    resolve_latest_tag
  fi

  if [[ -z "$DEPLOY_DIR" ]]; then
    DEPLOY_DIR="./yaog-${TAG}"
  fi

  require_cmd curl
  require_cmd tar
  require_cmd python3

  local platform_id
  platform_id="$(detect_platform_id)"

  local workdir
  workdir="$(mktemp -d)"
  trap 'rm -rf "$workdir"' EXIT

  local bundle_asset="yaog-bundle-${platform_id}.tar.gz"
  local bundle_file="$workdir/$bundle_asset"

  mkdir -p "$DEPLOY_DIR/bin" "$DEPLOY_DIR/frontend" "$DEPLOY_DIR/logs" "$DEPLOY_DIR/run"

  download_asset "$bundle_asset" "$bundle_file"

  rm -rf "$DEPLOY_DIR/bin" "$DEPLOY_DIR/frontend"
  mkdir -p "$DEPLOY_DIR/bin" "$DEPLOY_DIR/frontend"
  tar -xzf "$bundle_file" -C "$DEPLOY_DIR"

  if [[ "$SKIP_FRONTEND" == "true" ]]; then
    rm -rf "$DEPLOY_DIR/frontend"
    mkdir -p "$DEPLOY_DIR/frontend"
  fi

  write_runtime_files "$DEPLOY_DIR"

  echo ""
  echo "Deployment complete in: $DEPLOY_DIR"
  echo "Run: cd '$DEPLOY_DIR' && ./start.sh"
  echo "Stop: cd '$DEPLOY_DIR' && ./stop.sh"
}

main "$@"
