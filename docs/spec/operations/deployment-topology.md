# Deployment Topology

YAOG ships as **two distinct deployments built from one source tree**, plus a CLI and a tagged
local-design oracle. Which compute surface is reachable depends on the **build**, not on runtime
configuration. This split — introduced by plan-7 / milestone 1.7 — is the security boundary for the
anonymous-compute attack surface (see [Security delta](#security-delta-plan-7--milestone-17) below).

## The build-tag boundary (LOCKED owner decision, 2026-06-18)

The four anonymous air-gap compute routes —

| Route | Handler |
|---|---|
| `POST /api/validate` | `HandleValidate` |
| `POST /api/compile` | `HandleCompile` |
| `POST /api/export` | `HandleExport` |
| `POST /api/deploy-script` | `HandleDeployScript` |

— and their support code (the `compute` route chain, `gateAirgap`, the `Server.operatorAuth`
read/arming, the three handler-local ZIP helpers `createExportZip`/`tarGzDirectory`/
`makeSelfExtractingInstaller`) live behind a **`//go:build airgap`** build constraint
(`internal/api/airgap_routes.go`, `internal/api/handler_airgap.go`).

- **`go build ./...` (default — the controller binary)** neither **registers** nor **links** those
  routes. The un-tagged `registerRoutes` registers exactly one public route — `GET /api/health` —
  and `registerExtraRoutes` is a no-op stub (`internal/api/airgap_stubs.go`).
- **`go build -tags airgap ./...` (the local-design oracle)** **retains** all four routes unchanged.
  This is the boot target for plan-13's `--mode airgap` E2E and plan-21's `-tags airgap` DAST.

This is the **LOCKED owner decision**: the mechanism is a build tag, NOT a plain delete and NOT a
runtime env flag. The routes stay in the codebase as the local-design oracle; the build tag is what
removes them from the shipped controller.

## The deployments

### 1. Standalone static-local-design site (no backend)

A pure-frontend bundle where the panel runs entirely in the browser: the in-browser TypeScript
compiler (`frontend/src/compiler/`, plan-4) performs validate / compile / export, and the app is
**mode-locked to `local`** (the controller toggle and the controller-only nav are hidden). It POSTs
to **no** Go backend — there is no listener at all. Host it on any static file server / CDN.

- **Compute:** in-browser (TS compiler). No network compute path exists.
- **Anonymous attack surface:** none (no backend process).

**Build it:** `cd frontend && npm run build:local` (= `tsc -b && VITE_LOCAL_ONLY=1 vite build
--outDir dist-local`). Output is `frontend/dist-local/` — a self-contained directory of static
assets; copy it to any web root.

**The mode-lock mechanism (`VITE_LOCAL_ONLY`):** the flag is read in **one** place,
`frontend/src/lib/localOnly.ts` (`localOnly()`), typed in `frontend/src/vite-env.d.ts`. When set:

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

### 2. Controller server (the default binary)

`go build ./...` → `yaog-server`, run in controller mode
(`YAOG_CONTROLLER_STATE_DIR` + `YAOG_TENANT_ID` set). It serves:

- The panel SPA (when `YAOG_WEB_DIR` is set — the Docker image sets it), the all-in-one bundle.
- `GET /api/health` — the public, ungated liveness probe (CORS-wrapped, present in **both** builds).
- The operator routes under `/api/v1/operator/` and the agent routes under `/api/v1/agent/`
  (separate muxes/ports), gated by operator/agent auth (see `controller/controller-api.md`).

The controller compile path is the **operator-gated** `HandleCompilePreview` / `HandleStage`
(→ `controller.CompileSubgraph` / `controller.CompileAndStage`), **not** the anonymous air-gap
routes. The four anonymous compute routes are **absent** (return 404). A pinned, perpetual negative
test (`internal/api/airgap_routes_removed_test.go`, runs in the default `go test ./...`) asserts the
four routes 404 and `/api/health` stays 200.

**Default-build boot disposition:** if the controller env is **not** configured
(`YAOG_CONTROLLER_STATE_DIR` and/or `YAOG_TENANT_ID` unset), the default binary **fails loud**
(`cmd/server/boot_default.go`) — it is controller-only and links no air-gap compute surface to fall
back to, so it names the fix (set the controller env, or use the `-tags airgap` build / the
static-local-design site / `cmd/compiler`) instead of standing up a do-nothing listener.

**Docker image:** the `Dockerfile` builds the **default** (no `-tags airgap`) `cmd/server`, so the
four anonymous routes are absent at link time. It copies the all-in-one `frontend/dist` →
`/app/web`, sets `YAOG_WEB_DIR=/app/web`, and the `HEALTHCHECK` hits `/api/health`. **Do not add
`-tags airgap` to the controller image** — the local-design oracle is a separate artifact (§3).

**Release asset:** the platform bundles (`yaog-bundle-<os>-<arch>.{tar.gz,zip}`) carry the default
controller `yaog-server`, `yaog-compiler`, `yaog-agent`, and the all-in-one `frontend/`.

### 3. `-tags airgap` local-design oracle (dev / E2E / DAST only)

`go build -tags airgap ./...` → a server that **retains** the four anonymous compute routes. When
the controller env is unset it boots the air-gap server (`cmd/server/boot_airgap.go` →
`server.ListenAndServe`), serving the four compute routes + `/api/health` (+ the SPA when
`YAOG_WEB_DIR` is set). Its `ListenAndServe` startup banner advertises the four POST routes (the
default banner advertises only `GET /api/health`). This build is the **local-design oracle** and the
boot target for plan-13's `--mode airgap` E2E and plan-21's `-tags airgap` DAST. **It is not the
shipped controller artifact** — do not add `-tags airgap` to the controller Docker image.

**Release asset:** published per-arch as a standalone, opt-in `yaog-server-airgap-<os>-<arch>`
binary (NOT inside the platform bundle and NOT the controller image), clearly named so it cannot be
mistaken for the controller server.

### 4. `cmd/compiler` CLI (offline reference)

The offline CLI + reference implementation. It reads `topology.json`, runs the same
`render.All` → `artifacts.Export` pipeline, and writes the bundle. It never imports `internal/api`,
so it is unaffected by the build tag and produces byte-identical bundles in both build profiles. It
is the always-on offline compile path for anyone without a controller.

## How the pipeline stays exercised

Removing the anonymous HTTP handlers from the default build does not orphan the Go pipeline:

- `internal/conformance/` (plan-5) is a **required, green CI gate** that exercises the pipeline
  against the frozen `localcompile.Compile` I/O contract.
- `cmd/compiler` drives the same `render.All` → `artifacts.Export` path.
- The controller still links the pipeline via `CompileSubgraph` for operator routes.
- The `-tags airgap` test suite (`handler_airgap_test.go`, the retagged
  `airgap_auth_gate_test.go` / coded / warnings / deployscript / signing cases, and
  `airgap_routes_present_test.go`) guards the retained oracle.

## Security delta (plan-7 / milestone 1.7)

This plan removes **anonymous reachability** of the compile pipeline from the deployed
(default/controller) server and from the standalone static site. It does **not** fix the underlying
algorithms — that is **plan-8 / milestone 1.8**. The build tag is the security boundary.

The pre-rc.1 investigation flagged four findings on the anonymous air-gap compute surface. After
this plan, in the **default/controller** build (and absent entirely from the **static-local-design**
site):

| Finding | What it is | Status in default/controller build | Where it still lives |
|---|---|---|---|
| **S1** | Allocator compile-DoS: an oversized domain CIDR × many nodes drives unbounded CPU in `AllocateIPs` / `allocateFromCIDR`. | **Removed from the anonymous surface** — `/api/compile` is not registered or linked. | Reachable by an **authenticated operator** via `CompileSubgraph`/`CompileAndStage`, by anyone via **`cmd/compiler`**, and via the **`-tags airgap`** oracle. The FULL fix (cap + `context.Context`) is **plan-8**, an rc.1 blocker. |
| **S2** | Unbounded `Domains` / `reserved_ranges` counts inflate validation/allocation work. | **Removed from the anonymous surface.** | Same as S1. Schema-bound caps are **plan-8**. |
| **S3** | Quadratic peer-derivation gap-fill cursor (many same-pair `backup` edges). | **Removed from the anonymous surface.** | Same as S1. The cursor optimization is **plan-8** (post-rc.1). |
| **B4** | Export-ZIP buffered wholly in memory (`/api/export` / `makeSelfExtractingInstaller`). | **Closed by removal** — `/api/export` and the ZIP helpers are not linked into the default build. | Reachable via **`cmd/compiler` / `internal/artifacts`** and via the **`-tags airgap`** oracle. (Recorded by plan-21 as closed-by-removal for the deployed surface, not as a regression-matrix code fix.) |

### The caveats (read these — the class is downgraded, not eliminated)

1. **S1/S2/S3 remain reachable by an authenticated operator** via `CompileSubgraph`/`CompileAndStage`
   on the controller, and by **anyone** via `cmd/compiler`. The class is **downgraded from
   anonymous-remote to operator-or-CLI-trust** in the shipped controller — not eliminated. The
   algorithmic hardening that closes them for those trusted paths is **plan-8**.
2. **The `-tags airgap` build deliberately re-exposes the four routes** as the local-design oracle
   (and the plan-13 / plan-21 boot target). That build is **not** the shipped controller; it exists
   for dev / E2E / DAST. plan-21's S1 compile-DoS DAST case targets exactly this oracle boot to prove
   plan-8's cap fires there.
3. **The build tag is the security boundary.** The proof that no anonymous/unauthenticated route
   reaches the compile pipeline in the shipped controller is two-fold: (a) the un-tagged negative
   routing test (`airgap_routes_removed_test.go`) asserting the four routes 404 on
   `Server.Handler()`, and (b) the air-gap handlers being **excluded from the default binary at link
   time** by the build tag. (The controller binary still links the pipeline via `CompileSubgraph` for
   the operator-gated routes; only **anonymous** reachability is removed.)

This shrunken anonymous surface is a deliberate **input to Subject 4's re-audit** (plan-20/21/22):
the auditors see the controller with no anonymous compute oracle, S1/S2/S3/B4 re-scored
closed-by-removal for the deployed surface, and the `-tags airgap` oracle / `cmd/compiler` as the
surviving surface where plan-8's caps are the defense.

## See also

- `security/security.md` — overall security model and residuals.
- `controller/controller-api.md` — controller route map, plain-HTTP + token auth, env gating.
- `compiler/io-contract.md` — the frozen `localcompile.Compile` contract the oracle / CLI / conformance share.
- `api/http-api.md` — HTTP endpoint contracts.
