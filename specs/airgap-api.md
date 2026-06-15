# Air-gap API & panel hosting

<!-- last-verified: 2026-06-15 -->
<!-- 2026-06-15 (extensible-i18n closeout): error responses now coded via the internal/apierr envelope {error:{code,message,params}} — English-default message + panel-localized by error.<code>; no endpoint/flow change. -->

## Responsibility
Serve the five open (unauthenticated, wildcard-CORS) design endpoints — health/validate/compile/export/deploy-script — and optionally host the built panel SPA, on the operator/panel port of a two-mux HTTP server whose second (agent) mux stays empty until controller mode opts in.

## Files
- `internal/api/handler.go:1-580` — `Handler` (wraps `compiler.NewCompiler()`, :30-39); the five endpoint funcs; 4 MiB body cap + `readTopology` (:298-338); export ZIP / tar.gz / self-extracting-installer builders (:340-570); `writeJSON`/`writeError` (:572-580).
- `internal/api/server.go:1-189` — `Server` with two muxes `mux`+`agentMux` (:12-23); route table (:53-66); wildcard CORS middleware (:69-82); panic-recovery middleware with header tracking (:87-123); `EnableController` seam (:48-51); the two `ListenAndServe*` entrypoints with anti-Slowloris timeouts (:143-189).
- `internal/api/static.go:1-52` — `spaHandler` (SPA file-or-index serving + api-path 404 guard, :20-44); `EnableStatic` mounts it at `/` on the operator mux (:50-52).

## Inputs
- **HTTP POST bodies**: a `model.Topology` JSON document (see specs/model-validation.md for the wire shape), parsed by `readTopology` (`internal/api/handler.go:315-338`) under a 4 MiB `http.MaxBytesReader` cap (`internal/api/handler.go:301,317`). Primary caller is the panel designer's fetch client (see specs/panel-design.md).
- **Upstream library calls** (per-request, stateless):
  - `render.GenerateKeys(topo, render.AirGap) (map[string]compiler.KeyPair, error)` — `internal/render/render.go:70`; see specs/render-keys.md.
  - `(*compiler.Compiler).Compile(topo, keys) (*CompileResult, error)` — `internal/compiler/compiler.go:78`; see specs/compiler-allocation.md.
  - `validator.ValidateSchema` / `validator.ValidateSemantic` (`internal/validator/schema.go:67`, `internal/validator/semantic.go:15`) — called directly only by `/api/validate` (`internal/api/handler.go:106-108`); compile-path warnings come from inside `Compile` itself (`internal/compiler/compiler.go:80-95`).
  - `artifacts.Export(result, tmpDir)` (`internal/artifacts/export.go:40`) and `bundlesig.LoadConfigSignerFromEnv()` (`internal/bundlesig/bundlesig.go:132`) for `/api/export`; see specs/artifacts-signing.md.
  - `renderer.RenderAllBabelConfigs` (`internal/renderer/babel.go:177`) and `renderer.RenderDeployScripts` (`internal/renderer/deploy.go:38`) for `/api/deploy-script`.
- **Wiring from `cmd/server/main.go`**: `api.NewServer()` (:78), `EnableStatic` gated on `YAOG_WEB_DIR` (:83-84), air-gap default when controller env unset (:89), `EnableController` + agent port only in controller mode (:151,157).

## Outputs
- `GET /api/health` → `HealthResponse{status,timestamp}` (`internal/api/handler.go:76-86`).
- `POST /api/validate` → `ValidateResponse{valid, errors[], warnings[]}` merging schema + semantic passes (`internal/api/handler.go:89-119`).
- `POST /api/compile` → `CompileResponse` (`internal/api/handler.go:61-73`): the round-tripped topology (allocation state rides it back to the client), per-node WireGuard/Babel/sysctl/install/deploy config maps, non-fatal `warnings[]`, and the `compiler.CompileManifest`.
- `POST /api/export` → `application/zip` attachment of one self-extracting installer per node (tar.gz payload, embedded SHA-256, optional Ed25519 signature block) (`internal/api/handler.go:170-227,340-570`).
- `POST /api/deploy-script?format=ps1|<default bash>` → `deploy-all.ps1` / `deploy-all.sh` attachment (`internal/api/handler.go:231-294`).
- **Mux accessors for downstream wiring**: `Handler()`/`AgentHandler()` (`internal/api/server.go:127-135`); `EnableController(ch)` puts operator controller routes on `s.mux` and agent routes on `s.agentMux` (`internal/api/server.go:48-51`) — route content documented in specs/controller-operator-api.md and specs/controller-agent-api.md.

Endpoint/status-code contract is specified in `docs/spec/api/http-api.md`; request-body field parity in `docs/spec/api/wire-contract.md`.

## Decision points
- **Method gate**: every endpoint rejects the wrong verb with 405 (e.g. `internal/api/handler.go:77-80,90-93`).
- **Body-size triage**: `readTopology` errors map to 413 if `errBodyTooLarge` (sentinel, `internal/api/handler.go:305-310`), else 400 (`internal/api/handler.go:96-103`).
- **Compile failure class**: `Compile` errors → 422; key-generation or render errors → 500 (`internal/api/handler.go:138-155`).
- **Deploy-script format**: `?format=ps1` selects PowerShell, anything else bash (`internal/api/handler.go:278-288`).
- **Signing on/off**: `LoadConfigSignerFromEnv` returns nil when `YAOG_BUNDLE_SIGNING_KEY` is unset; nil signer means the installer wrapper is byte-identical to unsigned output (`internal/api/handler.go:344-351,471-517`).
- **CORS preflight**: OPTIONS short-circuits with 204 before the handler runs (`internal/api/server.go:75-78`).
- **SPA routing** (`internal/api/static.go:28-42`): any path with prefix `/api/` or containing `/api/v1/operator/` or `/api/v1/agent/` → hard 404 (never index.html, even under the optional secret path prefix); else serve the real file if one exists (via traversal-safe `http.Dir`); else fall back to `index.html` for client-side routes.
- **Panic recovery**: writes a 500 JSON body only if no response header was written yet, tracked by `headerTrackingResponseWriter` (`internal/api/server.go:87-123`).

## Invariants
- **Stateless per-request pipeline** — no server-side persistence; all allocation state returns inside the response topology (PRINCIPLES.md "Stateless compiler"; export's temp dir is created and removed per request, `internal/api/handler.go:204-209`).
- **Air-gap surface is untouched by controller mode** — `EnableController` is the single opt-in seam; without it `agentMux` serves nothing and `s.mux` carries exactly the five `/api/*` routes (`internal/api/server.go:39-51,131-135`; gate in `cmd/server/main.go:89`). Controller routes live under `/api/v1/operator/` and `/api/v1/agent/` and cannot collide (`internal/api/server.go:47`).
- **Integrity anchors are Go-emitted constants** — the installer's expected SHA-256 and optional signature/pubkey are computed server-side and embedded as literals, never derived from files the payload carries (PRINCIPLES.md "Generated scripts run as root on fleets"; `internal/api/handler.go:457-517`).

## Gotchas
- `/api/deploy-script` must run the FULL compile pipeline (keys → compile → `RenderAllBabelConfigs` → `RenderDeployScripts`) because per-peer interface names exist only in the post-compile `PeerMap`, and the script's `HasBabel` check looks nodes up in `BabelConfigs` (`internal/api/handler.go:247-272`). It is not a cheap variant of compile.
- Export ZIP entry names use `naming.SafeInstallerFileName(nodeName)` (`internal/api/handler.go:379`, `internal/naming/naming.go:47`), NOT the raw node directory name — the deploy script looks installers up by the same normalization; diverging rules silently skip nodes with uppercase/space/special characters.
- These endpoints answer with `Access-Control-Allow-Origin: *` and no auth (`internal/api/server.go:69-82`) — intentionally open for the air-gap design loop. The credentialed-CORS + cookie/CSRF machinery for operator routes is a separate middleware chain in `handler_controller.go` (see specs/controller-operator-api.md and specs/panel-auth.md); do not conflate the two CORS policies.
- `docs/spec/api/http-api.md`'s "Compliance" block describes a pre-Plan-3 state (no compile warnings field); live code DOES carry `warnings` in `CompileResponse` (`internal/api/handler.go:71,164`). Trust the cited code lines over that doc paragraph.
