#!/usr/bin/env pwsh
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

param(
    [string]$Tag,
    [string]$Owner = "kunori-kiku",
    [string]$Repo = "yet-another-overlay-generator",
    [string]$Dir = "",
    [switch]$SkipFrontend
)

function Require-Command {
    param([Parameter(Mandatory = $true)][string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command not found: $Name"
    }
}

function Get-Arch {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString().ToLowerInvariant()
    switch ($arch) {
        "x64" { return "amd64" }
        "arm64" { return "arm64" }
        "x86" { return "386" }
        "arm" { return "armv7" }
        default { throw "Unsupported architecture: $arch" }
    }
}

function Get-PlatformId {
    $arch = Get-Arch
    if ($IsWindows) {
        return "windows-$arch"
    }
    if ($IsLinux) {
        return "linux-$arch"
    }
    throw "Unsupported OS: this deploy script currently supports Linux and Windows only."
}

function Get-ArchiveExtension {
    param([Parameter(Mandatory = $true)][string]$PlatformId)
    if ($PlatformId.StartsWith("windows-")) {
        return "zip"
    }
    return "tar.gz"
}

function Download-Asset {
    param(
        [Parameter(Mandatory = $true)][string]$Asset,
        [Parameter(Mandatory = $true)][string]$OutFile
    )

    $url = "https://github.com/$Owner/$Repo/releases/download/$Tag/$Asset"
    Write-Host "Downloading $Asset ..."
    Invoke-WebRequest -Uri $url -OutFile $OutFile
}

function Resolve-LatestTag {
    $api = "https://api.github.com/repos/$Owner/$Repo/releases/latest"
    Write-Host "Resolving latest release tag from GitHub..."
    $resp = Invoke-RestMethod -Uri $api
    if (-not $resp.tag_name) {
        throw "Failed to resolve latest release tag from $api"
    }
    return [string]$resp.tag_name
}

function Write-RuntimeFiles {
    param([Parameter(Mandatory = $true)][string]$DeployDir)

    $pythonProxy = @'
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
'@

    Set-Content -Path (Join-Path $DeployDir "run_frontend.py") -Value $pythonProxy -NoNewline

    $startSh = @'
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
fi

echo "Frontend: http://${FRONTEND_HOST}:${FRONTEND_PORT}"
echo "Backend:  http://${BACKEND_HOST}:${BACKEND_PORT}"
'@
    Set-Content -Path (Join-Path $DeployDir "start.sh") -Value $startSh -NoNewline

    $stopSh = @'
#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUN_DIR="$SCRIPT_DIR/run"

for name in frontend backend; do
  pidfile="$RUN_DIR/$name.pid"
  if [ -f "$pidfile" ]; then
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    rm -f "$pidfile"
  fi
done
'@
    Set-Content -Path (Join-Path $DeployDir "stop.sh") -Value $stopSh -NoNewline

    $startPs1 = @'
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

$backendPidFile = Join-Path $RunDir "backend.pid"
$frontendPidFile = Join-Path $RunDir "frontend.pid"

$backendBin = Join-Path $ScriptDir "bin/yaog-server"
if (Test-Path "$backendBin.exe") {
    $backendBin = "$backendBin.exe"
}

if (-not (Test-AlivePid $backendPidFile)) {
    $backendProc = Start-Process -FilePath $backendBin -ArgumentList @("-addr", ":$BackendPort") -RedirectStandardOutput (Join-Path $LogDir "backend.log") -RedirectStandardError (Join-Path $LogDir "backend.log") -PassThru
    Set-Content -Path $backendPidFile -Value $backendProc.Id
}

if (-not (Test-AlivePid $frontendPidFile)) {
    $env:YAOG_FRONTEND_DIR = (Join-Path $ScriptDir "frontend")
    $env:YAOG_BACKEND_BASE = "http://$BackendHost`:$BackendPort"
    $env:YAOG_FRONTEND_HOST = $FrontendHost
    $env:YAOG_FRONTEND_PORT = $FrontendPort

    $pythonCmd = if (Get-Command python3 -ErrorAction SilentlyContinue) {
        "python3"
    } elseif (Get-Command python -ErrorAction SilentlyContinue) {
        "python"
    } elseif (Get-Command py -ErrorAction SilentlyContinue) {
        "py"
    } else {
        throw "Python runtime not found (need python3/python/py)."
    }

    $pythonArgs = if ($pythonCmd -eq "py") {
        @("-3", (Join-Path $ScriptDir "run_frontend.py"))
    } else {
        @((Join-Path $ScriptDir "run_frontend.py"))
    }

    $frontendProc = Start-Process -FilePath $pythonCmd -ArgumentList $pythonArgs -RedirectStandardOutput (Join-Path $LogDir "frontend.log") -RedirectStandardError (Join-Path $LogDir "frontend.log") -PassThru
    Set-Content -Path $frontendPidFile -Value $frontendProc.Id
}

Write-Host "Frontend: http://$FrontendHost`:$FrontendPort"
Write-Host "Backend:  http://$BackendHost`:$BackendPort"
'@
    Set-Content -Path (Join-Path $DeployDir "start.ps1") -Value $startPs1 -NoNewline

    if (-not $IsWindows) {
        & chmod +x (Join-Path $DeployDir "run_frontend.py")
        & chmod +x (Join-Path $DeployDir "start.sh")
        & chmod +x (Join-Path $DeployDir "stop.sh")
    }
}

if ($IsLinux) {
    Require-Command -Name "curl"
    Require-Command -Name "tar"
    Require-Command -Name "python3"
}

if ($IsWindows) {
    Require-Command -Name "curl"
}

$platformId = Get-PlatformId
$archiveExt = Get-ArchiveExtension -PlatformId $platformId
$workdir = New-Item -ItemType Directory -Path (Join-Path ([System.IO.Path]::GetTempPath()) ("yaog-deploy-" + [Guid]::NewGuid().ToString("N")))
$effectiveTag = if ([string]::IsNullOrWhiteSpace($Tag)) { Resolve-LatestTag } else { $Tag }
$deployDir = if ([string]::IsNullOrWhiteSpace($Dir)) { "./yaog-$effectiveTag" } else { $Dir }

try {
    $Tag = $effectiveTag
    $bundleAsset = "yaog-bundle-$platformId.$archiveExt"
    $bundlePath = Join-Path $workdir.FullName $bundleAsset

    New-Item -ItemType Directory -Force -Path $deployDir | Out-Null

    Download-Asset -Asset $bundleAsset -OutFile $bundlePath

    Get-ChildItem -Path $deployDir -Force | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue

    if ($archiveExt -eq "zip") {
        Expand-Archive -Path $bundlePath -DestinationPath $deployDir -Force
    } else {
        & tar -xzf $bundlePath -C $deployDir
    }

    if ($SkipFrontend) {
        Remove-Item -Path (Join-Path $deployDir "frontend") -Recurse -Force -ErrorAction SilentlyContinue
        New-Item -ItemType Directory -Force -Path (Join-Path $deployDir "frontend") | Out-Null
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $deployDir "logs") | Out-Null
    New-Item -ItemType Directory -Force -Path (Join-Path $deployDir "run") | Out-Null

    Write-RuntimeFiles -DeployDir $deployDir

    Write-Host ""
    Write-Host "Deployment complete in: $deployDir"
    Write-Host "Linux/macOS start: cd '$deployDir' && ./start.sh"
    Write-Host "PowerShell start:  cd '$deployDir' && pwsh ./start.ps1"
}
finally {
    Remove-Item -Path $workdir.FullName -Recurse -Force -ErrorAction SilentlyContinue
}
