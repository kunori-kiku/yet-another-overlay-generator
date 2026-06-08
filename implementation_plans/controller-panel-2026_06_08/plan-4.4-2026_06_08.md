# Plan 4.4 — Phase 2d: operator browser-auth + controller panel (frontend)

Parent: [plan-4-2026_06_08.md](plan-4-2026_06_08.md) · Prereq: 4.1/4.2/4.3a/4.3b/4.3c merged (the
single-tenant controller is complete end-to-end server+agent). Final Phase 2 sub-plan: the operator's
**browser** surface — the Deploy button + per-node status + enrollment UX from the original vision.

Split into two stacked PRs (backend before frontend):

- **4.4a — operator browser-auth + read/token endpoints (backend).** CI-testable (httptest).
- **4.4b — the controller panel (frontend).** Self-verifiable locally (`npm run lint && npm run build`).

## Adopted decision — operator browser-auth (resolved 2026-06-08, no user prompt needed)

The controller's operator routes are mTLS-only (4.3b: operator-cert CN), which a browser cannot cleanly
present. Browser-friendly operator auth is OIDC, which is Plan 5. For v1 the chosen, non-throwaway
bridge: an **env-configured operator bearer token** (`YAOG_CONTROLLER_OPERATOR_TOKEN`), accepted on
operator routes **alongside** the operator mTLS cert, unified at the **one** auth chokepoint
(`requireOperator`). Properties:

- One chokepoint, **two principal sources** today (operator mTLS cert OR operator bearer token), a
  **third** in Plan 5 (OIDC session) — same `tenant`+`node=operator` context downstream. This is an
  extension of the chokepoint, not a second auth path bolted on elsewhere.
- Bearer token: optional env (unset ⇒ operator routes stay mTLS-only, the 4.3b behaviour). Compared
  **constant-time** (`crypto/subtle`). Only meaningful over the existing TLS 1.3 transport.
- Honest scope: a static shared operator token is a v1 single-operator bridge, NOT multi-operator
  RBAC / per-operator audit identity — that is OIDC + RBAC (Plan 5). Documented as such; not overclaimed.

Dev plumbing: the Vite dev proxy (`:5173` → controller `:8080`) terminates/forwards TLS to the
controller (configured to trust the dev CA in dev); the browser talks plain HTTP to Vite. The panel
holds the operator token in session and sends `Authorization: Bearer`.

## 4.4a — backend (operator browser-auth + read/token endpoints)

1. `internal/api/auth_controller.go`: extend `requireOperator` to accept a valid
   `Authorization: Bearer <YAOG_CONTROLLER_OPERATOR_TOKEN>` (constant-time) as the operator principal,
   falling back to the existing operator-mTLS-cert path. `ControllerHandler` gains `operatorToken
   string` (from env; empty ⇒ bearer disabled). Agent routes (`requireNode`) are UNCHANGED (mTLS only).
2. `internal/api/handler_controller.go`: operator read + token endpoints (all `requireOperator`):
   - `GET  /api/v1/controller/nodes` → the registry projected for the panel: per node `{node_id,
     status, has_wg_public_key, mtls_cert_fp, desired_generation, applied_generation, last_checksum,
     last_seen, enrolled_at}` (never any key material — public-keys-only model holds).
   - `GET  /api/v1/controller/audit` → the hash-chained audit entries + a `verified` bool
     (`VerifyAuditChain`).
   - `GET  /api/v1/controller/topology` → the current stored topology JSON (the panel computes
     per-edge readiness against `/nodes`).
   - `POST /api/v1/controller/enrollment-token` `{node_id, ttl_seconds}` → mint a single-use token via
     `NewEnrollmentToken` + `CreateEnrollmentToken`; return the **plaintext** once (operator hands it
     to the node out-of-band). This is the missing mint route the panel's enrollment UX needs.
3. `cmd/server/main.go`: read `YAOG_CONTROLLER_OPERATOR_TOKEN` into the handler.
4. Tests `internal/api/controller_http_test.go` (extend): bearer-authed operator calls succeed; a wrong
   /absent bearer on operator routes → 401/403; bearer never grants node routes; `/nodes` reflects an
   enrolled+reported node; `/enrollment-token` mints a token a node can then enroll with; the mTLS
   operator path still works (no regression).
5. Spec `docs/spec/controller/controller-api.md`: the bearer principal source (env, constant-time, v1
   scope vs OIDC), the read/token routes.

## 4.4b — frontend (controller panel)

- New `/controller` route + `ControllerPanel` view, SEPARATE from the topology designer; the existing
  designer + `topologyStore` are **untouched** (single-source-of-truth preserved).
- `frontend/src/stores/controllerStore.ts` (Zustand): operator token, node registry + per-node status,
  current topology + derived per-edge readiness, audit, deploy state; actions `refresh`, `createToken`,
  `updateTopology`, `deploy` (= stage → promote), with poll/refresh.
- `frontend/src/api/controllerClient.ts`: typed calls to `/api/v1/controller/*` with the bearer header.
- Components: `NodeRegistry` (per-node enrolled/approved, applied-vs-desired generation drift,
  last-seen; per-edge ready/pending), `EnrollmentFlow` (modal: pick node + TTL → mint → show/copy the
  one-time token + the agent `enroll` command), `DeployBar` (Deploy button = stage→promote + result),
  `AuditLog` (chain + verified badge).
- `frontend/src/types/controller.ts` (mirrors the backend JSON; does NOT pollute `types/topology.ts`).
- i18n EN/ZH (`frontend/src/i18n.ts`).
- Verify locally: `npm run lint && npm run build` (tsc) before push.

## Definition of done

- [ ] 4.4a: CI green; operator bearer auth works + never escalates to node routes; read/token endpoints
      operator-gated; mTLS operator path unregressed; no new go.mod dep.
- [ ] 4.4b: `npm run lint && npm run build` clean; the panel deploys + shows per-node/per-edge readiness
      + enrollment flow against a running controller; designer/topologyStore untouched.

## Out of scope (Plan 5)

OIDC operator login + RBAC + per-operator audit identity (replaces the v1 bearer token); multi-tenant
principal-derived tenant; KMS; hardware-signed membership; stage→promote step-up. The real-host
two-node mTLS smoke remains the owed manual gate.
