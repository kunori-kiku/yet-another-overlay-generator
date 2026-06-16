# Outline — controller-panel-rollout-ui

<!-- drafted: 2026-06-16 by draft-implementation-plan -->

## Mission

Build the operator-panel UI for the already-shipped, backend-complete signed **agent self-update +
canary-then-fleet** feature (closing the descoped **plan-9 step 8** "Canary UI" from
`signed-self-update-and-rc-hardening-2026_06_15`), plus a symmetric form for the **mimic GitHub-`.deb`
catalog**, and a **per-node update-status surface** on the Fleet view. The backend already accepts and
strictly validates every field via `POST /api/v1/operator/settings`; today these are API-only with no
panel surface, and configuration is observed only through the plan-4 version badge.

Success criteria:

- An operator can, from the Settings page, configure the agent rollout (target version, per-arch
  `agent_bins` pins, canary node IDs, promote-fleet-wide) and the mimic catalog (version, release base,
  per-`<codename>-<arch>` `.deb` pins), with an **"Assist from GitHub release"** affordance that
  pre-fills the per-asset SHA-256 pins (reliable for the agent; best-effort for mimic).
- The **expected GitHub proxy URL** (`githubProxy` / agent `--gh-proxy`) is surfaced in the config UI,
  and the assisted pin-fetch routes through it.
- The Fleet view shows a **per-node update-status chip** (off / not-targeted / pending / applying /
  applied / failed / stale), derived from the reported agent version vs the configured target, rollout
  membership, the agent health line, and last-seen staleness — refreshable live via an opt-in poll.
- The signed-artifact **custody chain is unchanged**: the panel never becomes a trust primitive; the
  assisted fetch is a convenience and the UI says so; trust stays with the keystone-signed
  `artifacts.json` + the agent verifying the downloaded bytes against the signed pin.
- Ships on a feature branch under per-PR review, released as **v2.0.0-beta.3** (tag user-gated).

## Principles (invariants the executor must respect)

Inherits `PRINCIPLES.md` verbatim (esp. `PRINCIPLES.md:26-38` signed-self-update custody, HIGH).
Subject-specific principles on top:

1. **Signed-artifact custody is unchanged — the UI is never a trust primitive.** `[STATED]` `[INFERRED]`
   **HIGH.** The assisted pin-fetch is a *convenience*: the fetched `.sha256` rides the same untrusted
   transport (`github.com` / the gh-proxy) as the binary. Trust comes only from the keystone signing
   `artifacts.json` and the agent verifying the downloaded bytes against the signed pin
   (`internal/agent/selfupdate.go` verify-before-exec). *Violations:* auto-saving a fetched hash as if
   authoritative; copy implying the sidecar is the trust anchor; any panel path that bypasses
   `validateAgentRollout` / `validateMimicCatalog` / the signing step; persisting a pin the operator
   never reviewed.

2. **The empty-target safety contract, made visible.** `[STATED]` **HIGH.** Empty `TargetAgentVersion`
   ⇒ no agent block ⇒ no self-update. The UI must keep this obvious and must never make a fleet-wide
   update easy to trigger by accident: `agent_rollout_fleet_wide` is OFF by default and flips only
   behind an explicit confirm. *Violations:* defaulting fleet-wide on; a single control that arms a
   target AND promotes fleet-wide without confirmation; hiding that an empty target disables updates.

3. **`POST /settings` is FULL-REPLACE — every save must round-trip ALL fields.** `[INFERRED FROM CODE]`
   **HIGH (data-loss).** The handler rebuilds `ControllerSettings` purely from the body
   (`handler_bootstrap.go:118-131`); any omitted field is persisted as its zero value. *Violations:*
   any save path (the existing bootstrap form included) that omits the rollout or mimic fields →
   silent wipe of an operator's config. The data-layer fix (plan-2) MUST land before any new form input.

4. **Controller-mode only.** `[STATED]` **MEDIUM.** Config cards live inside the Settings page's
   controller-only fragment; the status surface lives on the already-`RequireControllerMode`-guarded
   `/fleet` routes; the pin-fetch endpoint lives under operator routes (`cors(operatorAuth)`). *Never*
   touch air-gap/local mode. *Violations:* rendering a card in local mode; an unauthenticated endpoint.

5. **EN/ZH i18n bijection + coded-error localization.** `[INFERRED FROM CODE]` **MEDIUM.** `zh.ts` is
   `Record<keyof typeof en, string>` — a key in one catalog only fails `npm run build` (and CI +
   release + Docker). Every string add is a lockstep two-file edit. User-facing errors route through
   `tError`/`localizeError`; no raw `<status> <body>` reaches the UI. New backend error strings stay
   **English** (`internal/api/i18n_gate_test.go` scans `internal/api`). *Violations:* a one-catalog key;
   an English-only user string; a raw error surfaced.

6. **SSRF safety on the pin-fetch endpoint.** `[INFERRED FROM DOMAIN]` **MEDIUM.** The endpoint fetches
   an operator-influenced URL server-side. Enforce: http(s)-only on the *resolved* URL, a bounded
   timeout, a tiny response cap (a sidecar is one hex line, ~80 bytes), refuse redirects to non-http(s),
   and validate asset names against the existing safe patterns *before* fetching. *Violations:* fetching
   an arbitrary operator URL with no guards; following a redirect to `file://`/an internal address.

7. **Status-chip derivation honesty.** `[INFERRED FROM CODE]` **LOW.** "Failed" has no positive backend
   field — it is inferred from the `lastHealth` `abandoned:` prefix (a failed node rolls back to its
   OLD version, so a version-only compare mislabels failed as pending). Parse `lastHealth` on stable
   PREFIXES, never equality. A node mid-self-update legitimately goes quiet (re-execs) — do not flag a
   healthy in-progress update as failed. *Violations:* equality-matching the free-form health string;
   labelling a quiet mid-update node failed on staleness alone.

## Current state of the world

- Branch base: `main` @ `c6eadb6` (PR #119 merged: post-audit doc honesty + the descope records).
- Shipped (last subject, `v2.0.0-beta.2`, GitHub latest): the signed agent self-update + canary-then-fleet
  **engine** (backend + agent), fully unit-tested + deep-reviewed. Config is **API-only**; observation is
  the plan-4 version badge.
- This subject is the descoped **plan-9 step 8** ("Canary UI"), recorded as owed in
  `implementation_plans/_completed/signed-self-update-and-rc-hardening-2026_06_15/CLOSURE.md` (Descoped
  deliverables) and `STATUS.md`.
- No frontend test runner exists (deferred); the i18n bijection `tsc` gate + `npm run build`/`lint` are
  the FE gates. Go: `go build/vet/test ./...` locally + CI authoritative.

## Must-read references

### Memory
- `agent-self-update-signed-verification` (subject SHIPPED; the custody trust argument).
- `controller-panel-2-trimmed-plan5-shipped`, `panel-appshell-redesign-shipped` (panel auth/shell/IA).
- `review-each-pr-before-merge` (per-PR independent review discipline).

### specs/ (top-level — partial-load per `specs/README.md`)
`panel-deploy-fleet`, `panel-shell`, `panel-auth`, `controller-operator-api`, `controller-agent-api`,
`controller-stage-promote`, `keystone-trustlist`, `agent`, `artifacts-signing`.

### docs/spec (prose)
- `docs/spec/controller/agent-selfupdate.md` (esp. §"Panel scope (observability)" :158-166 — authorizes
  this as the documented follow-up; the badge-flip + canary D2 semantics).
- `docs/spec/api/wire-contract.md` (FE↔BE parity — does not yet document the rollout settings fields).
- `docs/spec/controller/signing.md`, `docs/spec/artifacts/mimic.md` (the signed-pin trust chain).
- `docs/spec/controller/operator-auth.md` (cookie/CSRF/CORS the endpoint + forms inherit).
- `PRINCIPLES.md:26-38` (custody HIGH), `:56-63` (key custody; local-toolchain: no Go locally is fine,
  run `npm run lint && npm run build` before pushing FE).

### Production code (with line anchors)
- `internal/api/handler_bootstrap.go:29-170` (settingsJSON wire, HandleSettings GET/POST, mapping,
  validations), `:217-321` (validateAbsoluteHTTPURL, regexes, validateMimicCatalog, validateAgentRollout).
- `internal/api/handler_controller.go:203-252` (RegisterOperatorRoutes, `op := cors(operatorAuth)`),
  `:293-324` (cors), `:399-420` (nodeJSON, AgentVersion at :411-413), `:982-1016` (HandleNodes),
  `:1648-1659` (decodeJSON / DisallowUnknownFields).
- `internal/api/auth_controller.go:150-202` (operatorAuth + CSRF), `internal/api/cookie_session.go:26-118`.
- `internal/controller/store.go:282-368` (ControllerSettings + the agent/mimic fields + Clone),
  `internal/controller/settings.go:11-40` (DefaultSettings/WithDefaults),
  `internal/controller/compile.go:159-195,248-257` (BuildFetchSettings, AgentRolloutNodeIDs, CompileAndStage).
- `internal/renderer/fetch.go:11-14` (Artifact {asset, sha256}), `internal/render/artifacts_json.go:56-84`
  (per-node agent block gate), `internal/agent/selfupdate.go:171` (certified arches), `:321/348/384`
  (lastHealth prefixes: self-updated / probationary / abandoned).
- `.github/workflows/release.yml:218-231` (per-arch agent `.sha256` sidecar publishing).
- `internal/apierr/apierr.go` (CodeReqFieldInvalid etc.; add new codes here).
- `frontend/src/api/controllerClient.ts:615-664` (ControllerSettings TS, SettingsJSON, mapSettings,
  postSettings — the drop-on-save), `frontend/src/types/controller.ts` (ControllerNode).
- `frontend/src/components/deploy/BootstrapSettings.tsx:43-51,63-142` (the controlled-form pattern +
  drop-prone literals), `frontend/src/components/pages/SettingsPage.tsx:99-156` (render site + confirm modal model).
- `frontend/src/components/deploy/NodeRegistry.tsx:73-126` (badge + derived-cell patterns),
  `frontend/src/components/pages/FleetNodeDetailPage.tsx:45-61`, `FleetPage.tsx`,
  `frontend/src/stores/controllerStore.ts` (refresh/nodes/settings), `frontend/src/lib/localizeError.ts`.
- `frontend/src/i18n/messages/{en,zh}.ts` (catalogs + the bijection type).

### Test gates
- `internal/api/i18n_gate_test.go` (English-only wire surfaces — must stay green).
- Backend: `internal/api/*_test.go`, `internal/controller/*_test.go` (settings/rollout).
- FE: `npm run lint`, `npm run build` (= `tsc` bijection + vite). `.github/workflows/ci.yml`.

## Standing rules

See memory `review-each-pr-before-merge`. Per PR: structure-aware implementation → local gate
(`go build/vet/test ./...` + `gofmt`; `npm run lint && npm run build`) → push → **independent review
workflow** → fix → re-review → merge (`gh api --method PUT …/merge -f merge_method=merge -f sha=…`) →
sync `main` → delete branch. Git author `kunori-kiku <rokuyanlin@gmail.com>` on every commit; no
`--no-verify`/`--amend`/force-push; branch-first. **No Claude/Anthropic attribution** in commits/PRs.

## Decisions log

**Preflight (2026-06-16):**
- Scope = **config + per-node status** (the full plan-9 step 8), not config-only or a rich dashboard.
- Pin entry = **assisted from GitHub release** (panel fetches sidecars + pre-fills; convenience only).
- Placement = **config on Settings, status on Fleet/Node-detail**.
- Delivery = **feature branch → per-PR review → v2.0.0-beta.3 on main**.
- Assumed HIGH principles confirmed: custody unchanged; empty-target safety contract; fleet-affecting
  actions need confirm; i18n bijection + tError + cookie/CSRF preserved; controller-mode only.
- User addition: **surface the expected GitHub proxy URL** in the config UI; assist routes through it.

**Post-flight (2026-06-16):**
- Milestone shape = **accept 4-plan core + the server-computed `in_rollout` flag on nodeJSON** (avoids a
  Go/TS membership-drift class).
- Rollout UX = **persist settings + clear copy** (a Compile→Stage→Promote is still required via the
  existing deploy flow); no combined save+stage+promote chain; promote-fleet-wide is a confirmed,
  reversible toggle.
- Live status = **opt-in auto-poll** (15–30s, paused on tab-hidden, cleared on logout/mode-switch).
- Scope = **also build a mimic-catalog form** (symmetry) → adds plan-4; data layer + pin-fetch endpoint
  cover mimic too. (Note: per the full-replace contract, the agent form must round-trip mimic fields
  regardless of whether a mimic form exists.)

**Executor-improvised defaults (LOW principle-risk; documented here, no need to re-ask):**
- `min_agent_version` is surfaced as an **optional advanced field** with copy explaining its
  forced-before-apply semantics.
- The agent pin-fetch / form offers **only the self-update-certified arches** (`linux-amd64`,
  `linux-arm64`, `selfupdate.go:171`); 386/armv7 are bootstrap-install-only and not offered for self-update.
- The pin-fetch endpoint resolves a specific `?version` by **rewriting the default
  `releases/latest/download` base to `releases/download/<version>`** (the release.yml asset names are a
  fixed contract), so a target version is never checked against a moving "latest".
- The mimic assist is **best-effort** (the mimic `.deb` release base is external and may not publish
  `.sha256` sidecars); manual entry is the guaranteed path and the always-available default.
- Update-status enum (the i18n vocabulary): `off` (no target) / `not-targeted` (target set, node not in
  rollout) / `pending` (in rollout, version < target, no failure marker) / `applying` (probationary
  health marker) / `applied` (version == target) / `failed` (lastHealth `abandoned:` prefix) / `stale`
  (no recent check-in AND not legitimately mid-update). `lastHealth`-prefix→state mapping per
  `selfupdate.go:321/348/384`.

## Milestones

### plan-1 — Backend: assisted release-pin-fetch endpoint + `in_rollout` flag
**Goal.** Add an operator-only endpoint that fetches per-asset `.sha256` sidecars through the persisted
gh-proxy and returns `renderer.Artifact`-shaped pins for both the agent (per-arch) and mimic
(per-`<codename>-<arch>`) cases, plus a server-computed `in_rollout` (and target echo) on `nodeJSON`.
**Proposed solution.** New handler registered beside `settings` under `op := cors(operatorAuth)`; an
injected bounded-timeout, http(s)-only, redirect-restricted, response-capped `http.Client`; reuse
`AgentRolloutNodeIDs` for `in_rollout`. New `apierr` codes + EN/ZH `error.<code>` keys. Go tests.
**Hazards.** SSRF; version/base coupling; custody-perception; English-only Go strings.
**Verification.** `go build/vet/test ./...`, `gofmt -l` empty; `i18n_gate_test` green; bijection green.
**Stop-loss.** If the sidecar fetch needs the GitHub releases **API** (auth/rate-limit) or redirects make
http(s)-only impractical → STOP, draft plan-1.5.

### plan-2 — Frontend data layer: full rollout+mimic contract + drop-on-save fix
**Goal.** Carry the five agent-rollout fields AND the mimic fields through the TS contract and STOP the
full-replace wipe; add the `fetchPins` client wrapper. No UI.
**Proposed solution.** Extend `ControllerSettings`/`SettingsJSON`/`mapSettings`; convert `postSettings`
and `BootstrapSettings`' onSave/empty-default literals to **spread round-trips** so every field is sent.
**Hazards.** Full-replace data loss (highest); `DisallowUnknownFields` exactness; read-only
`agent_path_prefix`.
**Verification.** `tsc`+`vite build`; manual round-trip smoke (edit a bootstrap field → save → rollout +
mimic config survive).
**Stop-loss.** If the round-trip exposes a deeper store/persistence bug → STOP, draft plan-2.5.

### plan-3 — Agent rollout config card (Settings)
**Goal.** `AgentUpdateSettings.tsx` on Settings: target/min version, per-arch bins editor + "Assist from
GitHub release", canary multiselect (from store nodes), promote-fleet-wide behind a confirm modal,
read-only proxy-URL echo, custody-perception copy, EN/ZH keys, client-side mirror of `validateAgentRollout`.
**Hazards.** Custody-perception copy (HIGH); confirm is load-bearing; target-requires-bins; bijection;
single ErrorBoundary (defensive null/empty handling).
**Verification.** `tsc`+build; manual smoke (assist pre-fill, save, confirm).
**Stop-loss.** If the form pattern needs a shared field-component extraction shared with plan-4 → STOP,
draft plan-3.5.

### plan-4 — Mimic catalog config card (Settings)
**Goal.** `MimicCatalogSettings.tsx` symmetric to plan-3: mimic version, release base, per-`<codename>-<arch>`
`.deb` pins editor + **best-effort** assist (manual fallback), EN/ZH keys, client-side mirror of
`validateMimicCatalog` (debKey/debAsset/sha256 patterns; debs-require-release-base).
**Hazards.** Mimic assist may have no sidecars (must degrade gracefully to manual); same full-replace +
bijection constraints; the `<codename>-<arch>` key UX.
**Verification.** `tsc`+build; manual smoke (manual entry saves; assist degrades cleanly).
**Stop-loss.** If mimic sidecars are unavailable AND the assist endpoint can't degrade → ship manual-only,
note it; no insertion needed.

### plan-5 — Per-node update-status surface (Fleet + Node detail) + opt-in poll
**Goal.** A derived per-node status chip on `NodeRegistry` + a Field on `FleetNodeDetailPage`, via a pure
`deriveUpdateState(node, settings)` (using `in_rollout` from plan-1, version compare, `lastHealth`
prefixes, lastSeen staleness); plus an opt-in 15–30s poll (paused on tab-hidden, cleared on
logout/mode-switch).
**Hazards.** "Failed" has no positive field (prefix-parse only); membership must match `AgentRolloutNodeIDs`
(use `in_rollout`); mid-update quiet node ≠ failed; poll cleanup/session hygiene; stale cache on reload.
**Verification.** `tsc`+build; manual smoke (chip states; poll clears on logout).
**Stop-loss.** If reliable status needs a new agent-reported field → STOP, draft plan-5.5 (don't add an
agent wire field unilaterally).

### plan-6 — Closure & v2.0.0-beta.3 release
**Goal.** Update specs (`agent-selfupdate.md` §Panel scope → built; `wire-contract.md` parity;
`specs/panel-deploy-fleet.md`, `specs/panel-shell.md`); flip the CLOSURE.md descope record to delivered;
CHANGELOG + STATUS; archive this subject; cut `v2.0.0-beta.3` (tag user-gated).
**Hazards.** Spec drift; release notes honesty (cover the prior owed item now delivered).
**Verification.** Release workflow green; `gh release ... --latest`.
**Stop-loss.** Release-only; no insertion expected.

## Insertion-point markers

- **plan-1.5** — pin-fetch needs the GitHub releases API (auth/rate-limit) or redirects defeat
  http(s)-only; or SSRF posture needs an allowlist.
- **plan-2.5** — the drop-on-save fix uncovers a deeper store/round-trip or generation-poll bug.
- **plan-3.5** — the agent + mimic forms warrant a shared field/editor component before duplicating.
- **plan-5.5** — reliable per-node status genuinely needs a new agent-reported update-state field
  (an agent wire change — must be a deliberate, separately-reviewed decision).

## Closure criteria

- [ ] Agent + mimic config cards work end-to-end (manual smoke); assist pre-fills agent reliably,
      mimic best-effort with manual fallback.
- [ ] GitHub proxy URL surfaced; assist routes through it.
- [ ] Per-node status chip renders all enum states; opt-in poll updates live and clears on logout.
- [ ] Full-replace drop-on-save fixed (round-trip smoke proves rollout + mimic config survive a
      bootstrap-field save).
- [ ] EN/ZH bijection green; `npm run lint && npm run build` green; `go build/vet/test ./...` +
      `i18n_gate_test` green; CI green on every PR.
- [ ] Custody-perception copy present; no path persists an unreviewed/auto-trusted pin.
- [ ] Specs updated; `agent-selfupdate.md` §Panel scope flipped to built; CLOSURE.md descope marked
      delivered.
- [ ] `v2.0.0-beta.3` released (user-gated tag), notes cover the now-delivered Canary UI.

## Plan status table

| Plan | Milestone | Status | Commit / note |
|---|---|---|---|
| plan-1 | backend pin-fetch + in_rollout | pending | |
| plan-2 | frontend data layer + drop-on-save fix | pending | |
| plan-3 | agent rollout config card | pending | |
| plan-4 | mimic catalog config card | pending | |
| plan-5 | per-node status surface + poll | pending | |
| plan-6 | closure & v2.0.0-beta.3 | pending | |
