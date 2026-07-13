#!/usr/bin/env bash
# build-wasm.sh — build the in-browser Go/WASM local engine (framework-refactor plan-3).
#
# Compiles the pure Go compile pipeline (cmd/wasm) to GOOS=js GOARCH=wasm as web/yaog.wasm,
# refreshes the version-pinned web/wasm_exec.js from the ACTIVE Go toolchain, and copies both
# into frontend/public/ so Vite serves them at /yaog.wasm + /wasm_exec.js for wasmEngine.ts.
#
# web/yaog.wasm and the frontend/public/ copies are gitignored build artifacts (rebuilt here +
# in CI); web/wasm_exec.js is the ONE committed, toolchain-pinned runtime. The wasm-conformance
# gate (scripts/wasm-conformance-gate.mjs) asserts the committed web/wasm_exec.js matches the
# building toolchain — so if a toolchain bump changes it, re-run this script and review the diff.
#
# Toolchain: this repo builds with the go.mod `toolchain` (go1.26.5 as of plan-3); wasm_exec.js
# is stable across patch releases. Run from the repo root:  ./scripts/build-wasm.sh
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

goroot="$(go env GOROOT)"
wasm_exec_src="$goroot/lib/wasm/wasm_exec.js"
if [[ ! -f "$wasm_exec_src" ]]; then
  echo "error: toolchain wasm_exec.js not found at $wasm_exec_src" >&2
  exit 1
fi

echo "building web/yaog.wasm ($(go env GOVERSION), GOOS=js GOARCH=wasm)..."
mkdir -p web
GOOS=js GOARCH=wasm go build -o web/yaog.wasm ./cmd/wasm

echo "refreshing web/wasm_exec.js from the toolchain..."
cp "$wasm_exec_src" web/wasm_exec.js

echo "copying both into frontend/public/ (Vite static assets)..."
mkdir -p frontend/public
cp web/yaog.wasm web/wasm_exec.js frontend/public/

echo "done. web/yaog.wasm + web/wasm_exec.js built; frontend/public/ populated."
