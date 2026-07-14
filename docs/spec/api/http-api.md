# HTTP API

Base URL (controller): `http://localhost:8080`

The deployed server exposes a **minimal** public HTTP surface. The only ungated route is:

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/health` | Health check → `{ "status": "ok", "timestamp": "..." }` |

The operator and agent surfaces (`/api/v1/operator/…`, `/api/v1/agent/…`) live on separate
muxes/ports and are token-gated; they are documented in
[../controller/controller-api.md](../controller/controller-api.md) and
`specs/controller-agent-api.md`, not here. CORS is enabled for `/api/health`
(`Access-Control-Allow-Origin: *`).

## Removed: the anonymous compute routes

Earlier releases exposed four **anonymous** topology-compute routes on the operator port —
`POST /api/validate`, `POST /api/compile`, `POST /api/export`, and
`POST /api/deploy-script?format=sh|ps1`. They were **removed** in framework-refactor plan-9 (there
is no `-tags airgap` build): no anonymous path reaches the compile pipeline. See
[../operations/deployment-topology.md](../operations/deployment-topology.md) ("No anonymous compute
surface") for the full story and the `internal/api/no_anonymous_compute_test.go` guard that pins
their 404.

Design compute now runs on two paths instead:

- **LOCAL design** compiles **in the browser** via the Go/WASM engine (`web/yaog.wasm`).
- **Controller-mode** design compute is the **operator-gated** `HandleCompilePreview` / `HandleStage`
  (→ `controller.CompileSubgraph` / `controller.CompileAndStage`).

The wire shape of a topology body — field parity with the Go model and round-trip rules — is
specified in [wire-contract.md](wire-contract.md).

## Compile contract (WASM engine + operator-gated controller path)

The compile contract that the removed `/api/compile` route once carried still holds on the
in-browser WASM engine and the operator-gated controller path:

- Compile MUST run semantic validation, not only schema validation. Warnings that are generated but
  discarded mean an operator ships a green compile over a provably dead overlay (audit blocker UX-1).
- The compile result MUST carry a `warnings[]` array alongside the rendered configs. Non-fatal
  findings — double-NAT links, endpoint-less edges, isolated nodes — MUST appear there so the UI can
  surface them after a successful compile.
- Hard validation errors (semantic errors, not warnings) MUST fail compilation. A topology that
  cannot produce a deployable overlay MUST NOT render as if it succeeded.
- The compiled `topology` MUST satisfy the round-trip rules in [wire-contract.md](wire-contract.md):
  every transported field round-trips, and non-fixed WireGuard keys are blanked by design.

## Deploy-script contract

A deploy script MUST be rendered from a **compiled** topology — one that has been run through the
compiler so a real `PeerMap` (per-node, per-interface peer info) exists. Rendering from raw input
with a nil peer map produces uninstall sections with no per-interface teardown, so generated
uninstall scripts cannot tear down the per-peer WireGuard interfaces they brought up (D36). The
compile step MUST therefore run before the deploy renderer, passing the resulting `PeerMap` and
Babel configs to `RenderDeployScripts`.

## Robustness requirements (controller HTTP server)

These apply to the controller's HTTP server and every POST handler it still exposes (the operator
and agent routes):

- **Body size cap.** Each POST handler MUST cap the request body. A body beyond the limit MUST be
  rejected with **413**, not buffered. An unbounded `io.ReadAll` on every POST is an OOM DoS vector
  (D34).
- **Server timeouts.** The server MUST set read, write, and idle timeouts. A bare
  `http.ListenAndServe` with no timeouts is open to Slowloris / slow-body DoS (D33).
- **Panic recovery.** Handlers MUST recover from panics and return a **500** JSON error body rather
  than aborting the connection (D60).

Error responses MUST be a JSON body of the form `{ "error": "<message>" }` so the frontend store can
surface the message rather than rendering a blank panel.
