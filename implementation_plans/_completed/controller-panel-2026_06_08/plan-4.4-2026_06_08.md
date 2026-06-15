# Plan 4.4 — Phase 2d: controller panel as an entry in the existing app (PR-B)

Parent: [plan-4-2026_06_08.md](plan-4-2026_06_08.md) · Prereq: PR-A ([plan-4.5](plan-4.5-2026_06_08.md),
token auth + plain HTTP + two ports) merged. The operator's browser surface — integrated **into the
existing app**, not a separate route. Verifiable locally (`npm run lint && npm run build`; node/npm are
available).

## Adopted shape (owner-directed)

- **Entry, not a route.** The app is a single page with a Zustand-driven `viewMode` (`topology`|`audit`).
  Add a third `deploy` view: a **"🚀 Deploy" button in `TopBar`** (after the Audit toggle,
  `components/layout/TopBar.tsx`) sets `viewMode='deploy'`; `App.tsx` renders `<DeployPanel/>` for that
  mode (exactly how `<AuditView/>` is rendered for `audit`). The topology designer + `topologyStore` are
  **untouched** (single source of truth preserved).
- **Two modes inside the panel:**
  - **Mode A — Local / manual (works again):** reuse the existing `topologyStore` actions
    `compile()` / `exportArtifacts()` / `downloadDeployScript('sh'|'ps1')` (today in `RightPanel`) — the
    "download install bundle / deploy script" path, keys in the browser, no controller. Surface them in
    the panel so Mode A is first-class.
  - **Mode B — Controller / managed:** talk to a running controller over **plain HTTP** at a
    **configurable address (default `http://localhost:8080`, editable in the panel)** + an editable
    **secret path prefix** + an **operator token** field. Enroll nodes, **Deploy** (stage→promote), show
    per-node/per-edge readiness + audit.
- **Editable secret path** (owner): the controller mounts its routes under a runtime-configurable prefix;
  the panel edits it (and the operator token + base URL) and stores them in session/localStorage. The
  client sends `<baseURL><prefix>/api/v1/controller/...`. (Backend side of the editable prefix — stored
  + dispatch middleware + an operator get/set endpoint — is a small PR-A follow-up or lands with this PR;
  see "Backend deltas" below.)

## Frontend pieces

- `frontend/src/stores/controllerStore.ts` (Zustand, **separate** from topologyStore): `{baseURL,
  pathPrefix, operatorToken, nodes[], audit, currentTopology, deployState, lastError}` + actions
  `configure`, `refresh` (GET /nodes,/audit,/topology), `mintEnrollmentToken(nodeID,ttl)`, `deploy`
  (PUT current design via /update-topology → /stage → /promote), with poll/refresh. Persist
  baseURL/pathPrefix (NOT the operator token by default — session only) to localStorage.
- `frontend/src/api/controllerClient.ts`: typed fetch wrappers to `<baseURL><prefix>/api/v1/controller/*`
  with `Authorization: Bearer <operatorToken>`; central error handling (401/403 → "check token/URL").
- Components under `frontend/src/components/deploy/`:
  - `DeployPanel.tsx` — the `deploy` view shell + the A/B mode toggle.
  - `NodeRegistry.tsx` — per-node rows (enrolled/approved, applied-vs-desired generation drift,
    last-seen, health) + per-edge ready/pending derived from `/nodes` ∩ the current topology.
  - `EnrollmentFlow.tsx` — modal: pick node + TTL → mint → show the one-time token + the exact
    `agent enroll --controller <agent-url> --node-id <id> --token <tok>` command to copy.
  - `DeployBar.tsx` — the **Deploy** button (stage→promote) + result; a **step-up seam**
    (`requiresUserKey` no-op stub in v1) wrapping sensitive ops (membership changes), for Plan 5.
  - `AuditLog.tsx` — the hash-chained entries + a `verified` badge.
- `frontend/src/types/controller.ts` (mirrors the backend JSON; does NOT touch `types/topology.ts`).
- i18n EN/ZH in `frontend/src/i18n.ts` (the `txt(lang, zh, en)` pattern).

## Backend deltas this PR needs (small)

- **CORS on the operator port** for the browser panel: the operator routes must answer CORS preflight
  (`OPTIONS`, no auth) and send `Access-Control-Allow-*` so a cross-origin panel can call them (the
  air-gap server already has a `cors` middleware to reuse). The agent port needs no CORS.
- **Runtime-editable secret path** (if not already in PR-A): a stored prefix + a top-level dispatch that
  strips/validates it before the controller mux, + operator `GET/PUT /controller/config` for the prefix.
  Changing the prefix rotates the agent URL (document: re-distribute it).

## Definition of done

- [ ] `npm run lint && npm run build` clean; the `deploy` view toggles A/B; Mode A downloads bundles/
      scripts (reusing topologyStore); Mode B configures URL+prefix+token, enrolls (shows token+command),
      Deploys, and shows per-node/per-edge readiness + audit; designer/topologyStore untouched.
- [ ] Operator port serves CORS for the panel; secret path editable end-to-end.

## Out of scope (Plan 5 / plan-4.6)

OIDC operator login + RBAC (replaces the operator token); the real off-host hardware/Bitwarden step-up
signing behind the `requiresUserKey` seam; the "Roll keys" button + periodic rotation ([plan-4.6](plan-4.6-2026_06_08.md)).
