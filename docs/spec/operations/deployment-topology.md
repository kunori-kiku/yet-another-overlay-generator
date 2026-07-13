# Deployment Topology

YAOG ships as **two frontend deployment shapes built from one source tree**, plus a CLI. There is a
**single Go server build** — the controller. The two shapes are distinguished by a **frontend** build
flag (`VITE_LOCAL_ONLY`), not by a Go build tag; the local design workflow runs **in the browser** on
the Go pipeline compiled to WebAssembly (`web/yaog.wasm`).

This is the state after **framework-refactor plan-9**, which retired the former `//go:build airgap`
two-deployment split: with WASM proven as the in-browser local engine (plan-4/5), the anonymous
compute routes that build served were **deleted** (see [Security delta](#security-delta) below), so
there is no longer a build-tag boundary or a second server binary.

## No anonymous compute surface

The four anonymous compute routes that earlier plans gated behind a build tag —

| Route | Former handler |
|---|---|
| `POST /api/validate` | `HandleValidate` |
| `POST /api/compile` | `HandleCompile` |
| `POST /api/export` | `HandleExport` |
| `POST /api/deploy-script` | `HandleDeployScript` |

— **no longer exist in any build.** The handlers, their route registration, the operator-auth gate
(`gateAirgap`), the `Server.operatorAuth` field, and the three handler-local ZIP helpers
(`createExportZip` / `tarGzDirectory` / `makeSelfExtractingInstaller`) were removed in plan-9. The
server registers exactly one public route — `GET /api/health` — plus, in controller mode, the
operator/agent routes under `/api/v1/operator/` and `/api/v1/agent/`.

A pinned, perpetual guard (`internal/api/no_anonymous_compute_test.go`, in the default `go test ./...`)
asserts the four routes 404 on `Server.Handler()`, `/api/health` stays 200, **and** no api-package
production file re-declares one of the four handlers — so the deletion cannot silently creep back at
either the route or the link level.

## The deployments

### 1. Standalone static-local-design site (no backend)

A pure-frontend bundle where the panel runs entirely in the browser: the **in-browser Go/WASM
pipeline** (`web/yaog.wasm`, reached via `frontend/src/lib/localEngine.ts` → `frontend/src/wasm/`)
performs validate / compile / export / deploy-script, and the app is **mode-locked to `local`** (the
controller toggle and the controller-only nav are hidden). It POSTs to **no** Go backend — there is no
listener at all. Host it on any static file server / CDN.

- **Compute:** in-browser (WASM). No network compute path exists. A compile runs in the user's own
  tab on their own input — it is not a hosted service and not a remote attack surface.
- **Anonymous attack surface:** none (no backend process).

**Build it:** `cd frontend && npm run build:local` (= `tsc -b && VITE_LOCAL_ONLY=1 vite build
--outDir dist-local`). The build must include `web/yaog.wasm` + `web/wasm_exec.js` (`npm run build:wasm`
copies them into `frontend/public/` so `vite build` stamps them into the output). Output is
`frontend/dist-local/` — a self-contained directory of static assets; copy it to any web root.

**The mode-lock mechanism (`VITE_LOCAL_ONLY`):** the flag is read in **one** place,
`frontend/src/lib/localOnly.ts` (`localOnly()`), typed in `frontend/src/vite-env.d.ts`, and flows
through the shared deploy-mode descriptor (`frontend/src/lib/deployMode.ts`). When set:

- `controllerStore`'s initial `mode` is forced to `local`, and a persisted `controller` mode is
  coerced back to `local` on rehydrate (the `merge` hook) — a localStorage value written by the
  all-in-one build cannot resurface controller mode on the static site.
- `setMode('controller')` and `switchToController()` are **guarded no-ops** — the load-bearing lock
  (a deep link / programmatic call cannot escape local mode), pinned by
  `frontend/src/stores/controllerStore.local-only.test.ts`.
- The mode toggle (Settings) and the controller-only nav (Overview / Fleet) are **hidden** — no
  "connect to controller" affordance is offered (the cosmetic half of the lock).

**Release asset:** the Release workflow publishes this as a separate, cross-platform
`yaog-local-design-<version>.zip` (the `package-local-design` job, built from `build:local`),
distinct from the platform bundles (which carry the all-in-one `frontend/` for the controller).

### 2. Controller server (the Go binary)

`go build ./...` → `yaog-server`, run in controller mode
(`YAOG_CONTROLLER_STATE_DIR` + `YAOG_TENANT_ID` set). It serves:

- The panel SPA (when `YAOG_WEB_DIR` is set — the Docker image sets it), the all-in-one bundle.
- `GET /api/health` — the public, ungated liveness probe (CORS-wrapped).
- The operator routes under `/api/v1/operator/` and the agent routes under `/api/v1/agent/`
  (separate muxes/ports), gated by operator/agent auth (see `controller/controller-api.md`).

The controller compile path is the **operator-gated** `HandleCompilePreview` / `HandleStage`
(→ `controller.CompileSubgraph` / `controller.CompileAndStage`). There is **no** anonymous compute
route. In the all-in-one panel, LOCAL-mode design compute runs **in the browser** (the WASM engine),
exactly as on the standalone static site — the controller never serves an anonymous
validate/compile/export endpoint. Controller-mode Validate is browser-local verify (the panel runs
the in-browser validator and never calls a server validate route), keeping that surface off the wire.

**Boot disposition:** if the controller env is **not** configured
(`YAOG_CONTROLLER_STATE_DIR` and/or `YAOG_TENANT_ID` unset), the binary **fails loud**
(`cmd/server/main.go`) — it is controller-only and links no compute surface to fall back to, so it
names the fix (set the controller env, or use the standalone static-local-design site / `cmd/compiler`
for offline compilation) instead of standing up a do-nothing listener.

**Docker image:** the `Dockerfile` builds `cmd/server` (a plain `go build`, no build tag). It copies
the all-in-one `frontend/dist` → `/app/web`, sets `YAOG_WEB_DIR=/app/web`, and the `HEALTHCHECK` hits
`/api/health`.

**Release asset:** the platform bundles (`yaog-bundle-<os>-<arch>.{tar.gz,zip}`) carry the controller
`yaog-server`, `yaog-compiler`, `yaog-agent`, and the all-in-one `frontend/`.

### 3. `cmd/compiler` CLI (offline reference)

The offline CLI + reference implementation. It reads `topology.json`, runs the same
`render.All` → `artifacts.Export` pipeline, and writes the bundle. It never imports `internal/api`,
so it produces byte-identical bundles to the controller and the WASM engine. It is the always-on
offline compile path for anyone without a controller.

## How the pipeline stays exercised

Deleting the anonymous HTTP handlers does not orphan the Go pipeline:

- The **WASM conformance gate** (`scripts/wasm-conformance-gate.mjs`) is a required, green CI gate that
  executes `web/yaog.wasm` over the full success corpus and asserts it byte-equals the frozen Go
  golden — proving the in-browser engine == the Go controller pipeline.
- `internal/localcompile/` carries the frozen `Compile` I/O contract + the per-package coverage floor,
  exercised in the `go` job.
- `cmd/compiler` drives the same `render.All` → `artifacts.Export` path.
- The controller still links the pipeline via `CompileSubgraph` for the operator-gated routes.
- The `wasm-design` browser E2E (chromium perpetual guard, + the opt-in webkit/firefox soak) drives
  the whole local-mode flow (validate → compile → export → deploy-script) through the real WASM engine
  in a real browser, served from a controller `cmd/e2eserver` boot.

## Security delta

The anonymous reachability of the compile pipeline is **removed from the deployed server entirely** —
not gated behind a build tag, but deleted. The pre-rc.1 investigation flagged four findings on the
former anonymous air-gap compute surface. After plan-9:

| Finding | What it is | Status | Where it still lives |
|---|---|---|---|
| **S1** | Allocator compile-DoS: an oversized domain CIDR × many nodes drives unbounded CPU in `AllocateIPs` / `allocateFromCIDR`. | **No anonymous surface** — the routes are deleted, not gated. | Reachable by an **authenticated operator** via `CompileSubgraph`/`CompileAndStage` and by anyone via **`cmd/compiler`**. The WASM engine runs the same pipeline **in the user's own browser** (self-DoS only, not a hosted surface). The FULL fix (cap + `context.Context`) is **plan-8**, an rc.1 blocker. |
| **S2** | Unbounded `Domains` / `reserved_ranges` counts inflate validation/allocation work. | **No anonymous surface.** | Same as S1. Schema-bound caps are **plan-8**. |
| **S3** | Quadratic peer-derivation gap-fill cursor (many same-pair `backup` edges). | **No anonymous surface.** | Same as S1. The cursor optimization is **plan-8** (post-rc.1). |
| **B4** | Export-ZIP buffered wholly in memory (the former `/api/export` / `makeSelfExtractingInstaller`). | **Closed by deletion** — the ZIP helpers were removed with the handler. | Reachable via **`cmd/compiler` / `internal/artifacts`** and the in-browser WASM export (client-side). |

### The caveats (the class is downgraded, not eliminated)

1. **S1/S2/S3 remain reachable by an authenticated operator** via `CompileSubgraph`/`CompileAndStage`
   on the controller, and by **anyone** via `cmd/compiler`. The class is **downgraded from
   anonymous-remote to operator-or-CLI-trust** — the anonymous-remote surface is now fully gone (there
   is no longer even a build-tagged binary that re-exposes it). The algorithmic hardening that closes
   them for those trusted paths is **plan-8**.
2. **The in-browser WASM engine runs the same pipeline**, but in the user's own browser on their own
   input — a compile-DoS there costs the user their own tab, not a hosted service. It is not a remote
   attack surface.
3. **The proof that no anonymous route reaches the pipeline** is `no_anonymous_compute_test.go` (the
   four routes 404 on `Server.Handler()`; no handler re-declared) plus the controller still linking the
   pipeline only via `CompileSubgraph` for the operator-gated routes.

## See also

- `security/security.md` — overall security model and residuals.
- `controller/controller-api.md` — controller route map, plain-HTTP + token auth, env gating.
- `compiler/io-contract.md` — the frozen `localcompile.Compile` contract the CLI / WASM / conformance share.
- `api/http-api.md` — HTTP endpoint contracts.
