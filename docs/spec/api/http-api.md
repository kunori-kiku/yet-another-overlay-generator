# HTTP API

Base URL: `http://localhost:8080`

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/health` | Health check → `{ "status": "ok", "timestamp": "..." }` |
| `POST` | `/api/validate` | Validate topology → `{ "valid": bool, "errors": [...], "warnings": [...] }` |
| `POST` | `/api/compile` | Compile topology → full `CompileResponse` with all configs |
| `POST` | `/api/export` | Export artifact ZIP (binary download) |
| `POST` | `/api/deploy-script?format=sh\|ps1` | Download deploy script |

All POST endpoints accept `Content-Type: application/json` with a `Topology` object as body. The
wire shape of that body — field parity with the Go model and round-trip rules — is specified in
[wire-contract.md](wire-contract.md).

> The endpoints above are the open topology-design surface (operator/panel port `:8080`). The
> separate controller **agent** surface (`/enroll`, `/config`, `/poll`, `/report`, `/rekey`,
> `/bootstrap` on the agent port `:9090`) and its wire — including the `agent_version` field on
> `POST /report` — are documented in `specs/controller-agent-api.md`, not here.

CORS is enabled for all origins (`Access-Control-Allow-Origin: *`).

## Status-code contract

| Code | Meaning | When |
|---|---|---|
| `200` | OK | Successful health/validate/compile/export/deploy-script |
| `400` | Bad Request | Unreadable body, empty body, or malformed JSON |
| `405` | Method Not Allowed | Wrong HTTP method for the endpoint |
| `413` | Payload Too Large | Request body exceeds the configured size cap |
| `422` | Unprocessable Entity | Topology is structurally valid JSON but fails compilation (hard validation errors) |
| `500` | Internal Server Error | Key generation, render failure, or a recovered handler panic |

Error responses MUST be a JSON body of the form `{ "error": "<message>" }` so the frontend store can
surface the message rather than rendering a blank panel.

## Compile contract

`POST /api/compile` is the primary endpoint. Its contract:

- Compile MUST run semantic validation (the same checks as `/api/validate`) as part of every
  compile, not only schema validation. Warnings that are generated but discarded mean an operator
  ships a green compile over a provably dead overlay (audit blocker UX-1).
- The `CompileResponse` MUST carry a `warnings[]` array alongside the rendered configs. Non-fatal
  findings — double-NAT links, endpoint-less edges, isolated nodes — MUST appear there so the UI can
  surface them after a successful compile.
- Hard validation errors (semantic errors, not warnings) MUST fail the request with **422** and a
  JSON error body. A topology that cannot produce a deployable overlay MUST NOT return `200`.
- The returned `topology` MUST satisfy the round-trip rules in [wire-contract.md](wire-contract.md):
  every transported field round-trips, and non-fixed WireGuard keys are blanked by design.

> **Compliance:** `HandleCompile` calls `generateKeys` then `Compile` then `renderAll`
> (`handler.go:122-139`); it never calls `validator.ValidateSemantic`, and `CompileResponse`
> (`handler.go:55-65`) has no `warnings` field, so the NAT/endpoint-less-edge warnings produced by
> `validateNATReachability` (`nat.go:25-38`) are discarded. `HandleValidate` does run both passes
> (`handler.go:94-96`). Compile failures already map to 422 via
> `writeError(w, http.StatusUnprocessableEntity, …)` (`handler.go:131`). Closed by Plan 3.

## Robustness requirements

These apply to the HTTP server and every POST handler.

- **Body size cap.** Each POST handler MUST cap the request body. A body beyond the limit MUST be
  rejected with **413**, not buffered. An unbounded `io.ReadAll` on every POST is an OOM DoS vector
  (D34).
- **Server timeouts.** The server MUST set read, write, and idle timeouts. A bare
  `http.ListenAndServe` with no timeouts is open to Slowloris / slow-body DoS (D33).
- **Panic recovery.** Handlers MUST recover from panics and return a **500** JSON error body rather
  than aborting the connection. A panic in the allocator (e.g. an IPv6 CIDR reaching the IPv4-only
  allocator — see [../compiler/ip-allocation.md](../compiler/ip-allocation.md)) MUST become a clean
  5xx, not a dropped connection (D60). The streaming export handler MAY be exempted from the
  recovery wrapper if it conflicts with response streaming, provided that exemption is documented.

> **Compliance:** the server uses bare `http.ListenAndServe(addr, s.mux)` with no `http.Server`
> timeouts (`server.go:63`); `readTopology` does an uncapped `io.ReadAll(r.Body)`
> (`handler.go:249`); there is no recover middleware on the mux (`server.go:24-31`, `:34-47`).
> Closed by Plan 3.

## Deploy-script endpoint contract

`POST /api/deploy-script?format=sh|ps1` returns a downloadable deploy script.

- The deploy script MUST be rendered from a **compiled** topology — one that has been run through the
  compiler so a real `PeerMap` (per-node, per-interface peer info) exists. Rendering from raw input
  with a nil peer map produces uninstall sections with no per-interface teardown, so generated
  uninstall scripts cannot tear down the per-peer WireGuard interfaces they brought up (D36).
- The endpoint therefore MUST run `generateKeys` + `Compile` (mirroring `/api/compile`) before
  invoking the deploy renderer, and pass the resulting `PeerMap` and Babel configs to
  `RenderDeployScripts`.

> **Compliance:** `HandleDeployScript` calls `renderer.RenderDeployScripts(topo, nil, nil)`
> (`handler.go:222`) — it passes a nil peer map and nil Babel configs and never compiles, so the
> rendered uninstall path lacks per-interface teardown and the Babel-mode decision falls back to a
> role-only heuristic. Closed by Plan 4.
