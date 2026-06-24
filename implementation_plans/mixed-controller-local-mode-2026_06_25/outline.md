# Subject: mixed-controller-local-mode (2026_06_25)

## Mission

Let an operator mark **individual nodes** in a controller-managed topology as **manual** (no agent),
because they are not publicly reachable by the controller or the operator does not want an agent on
them — while the rest of the fleet stays controller-managed. Deliver the owner-chosen **Hybrid Kit
(Option C)**: the controller compiles AND signs each manual node's bundle exactly like a managed
node (one allocator, custody + keystone whole), the operator installs it by hand, and a one-shot
on-box **kit** handles keygen → identity registration → private-key splice, with an **optional
telemetry-only reporter** so manual nodes still show health in the fleet panel.

Success = a manual node participates correctly in the overlay (managed peers get its pubkey+endpoint
and vice-versa), the off-host signed membership manifest covers it, zero-knowledge custody is
preserved (its private key never reaches the controller), it appears in the panel as
"manual/unmonitored" without blocking fleet convergence, and the whole feature is reviewed per-PR
(4-lens + security HIGH — this touches enrollment, custody, and the keystone) and shipped as its own
beta after owner smokes.

## Principles (invariants — see repo-root `PRINCIPLES.md`)

- **Zero-knowledge custody is inviolable.** The controller never holds a node's WireGuard private
  key — manual nodes included. The manual operator generates + holds the private key off-controller
  and splices it by hand (the kit automates this on-box), exactly like the air-gap/`install.sh`
  placeholder splice (`internal/renderer/script.go:714-742`).
- **Off-host signed membership covers what runs.** A manual node rendered as a peer into managed
  bundles MUST be a member of the off-host-signed trust-list (`internal/controller/keystone.go:138-196`),
  else a managed node would trust a peer the signature never attested (owner decision D4 = include).
- **No shims / monkey-patches / no scope compromise** (`PRINCIPLES.md:67-84`). The `managed|manual`
  flag is a first-class orthogonal model field, not a special-case hack in one call site.
- **Per-PR independent review** (correctness / completeness / hygiene / structure + **security HIGH**),
  adversarially verified → fix → re-review → CI green → merge. Checkout-free reviews; isolated git
  worktree.
- **Local-vs-controller + Go↔TS parity** stays green (`internal/localcompile` golden contract).

## Owner-locked decisions (2026_06_25)

- **D1 = Hybrid Kit (Option C).** Controller compiles+signs the manual node's bundle; a one-shot
  on-box kit does keygen + descriptor + register + private-key splice; an optional telemetry-only
  reporter gives health visibility.
- **D2 (identity exchange)** follows from C: the kit generates the keypair on-box, prints/emits a
  **descriptor** (`{NodeID, pubkey, endpoint}`) the operator registers via a new **register-identity**
  call. (Operator-paste of a pubkey in the panel is also supported as the manual path.)
- **D3 = shown, excluded from convergence.** Manual nodes appear in the fleet list flagged
  "manual / unmonitored" and do NOT gate "fleet converged"; the optional reporter gives them health
  without making an unreachable box stall the rollup.
- **D4 = include manual nodes in the signed membership manifest.** Preserves "signature covers what
  runs."
- **D8 = separate release** after the theme+mimic fixes beta and after owner smokes (real
  two-node-with-one-manual deploy).

## Current state of the world

- Mode is a single GLOBAL switch (`controllerStore.ts:238,656`), but the model is custody-agnostic:
  `peers.go` renders any node that has a pubkey (in the `keys` map) + an endpoint. **`peers.go` needs
  ZERO change.**
- The single chokepoint that excludes a non-enrolled node (and drops every edge to it) is
  `enrolledSubgraph` (`internal/controller/compile.go:477-532`): admission `:480-485`, node drop
  `:513-522`, edge drop `:524-529`.
- Pre-known-identity hooks already exist but are stripped/barred in controller mode:
  `Node.FixedPrivateKey`/`WireGuardPublicKey` (`internal/model/topology.go:95-99`),
  `Node.PublicEndpoints` (`:101-121`), `Edge.EndpointHost/Port` (`:150-151`); UI-gated to local mode
  (`NodeForm.tsx:42,128`, `NodeEditor.tsx:18-22`), dropped on controller import (`custody.ts:45-67`),
  and the update-topology API refuses private keys (`handler_deploy.go:54-59`, `CodeCustodyPrivateKey`).
- Enrollment is the only writer of `NodeApproved`+`WGPublicKey` (`enrollment.go:215-224`); dedupe via
  `CheckWGKeyUnique` (`:256-281`); `NodeStatus` is `pending|approved|revoked` only (`store.go:67-91`).

## Must-read references

- `docs/spec/operations/deployment-topology.md` — the two-deployment story + build-tag boundary.
- `docs/spec/roles/roles.md` — role semantics (deployment_mode is orthogonal to role).
- The grounding brief (this session) — full file:line map of the mode boundary, the chokepoint, the
  custody/keystone invariants, and the Option-C design.

## Milestones

| Plan | Title | Track | Depends on |
|------|-------|-------|-----------|
| plan-1 | Model `deployment_mode` + compiler admission (admit manual from topology identity; keep edges; keygen branch; validator rule; skipped-reporting) | Go | — |
| plan-2 | Manual-identity registration + custody gate (allow public/bar private) + dedupe across manual+enrolled + keystone membership scope (include) | Go | plan-1 |
| plan-3 | Manual-node signed bundle production + per-node download endpoint | Go (+FE-light) | plan-1, plan-2 |
| plan-4 | On-box kit: `yaog-agent kit` (keygen → descriptor → register → private-key splice) | Go/agent | plan-2, plan-3 |
| plan-5 | Optional telemetry-only reporter for manual nodes (health without `/config`, excluded from convergence) | Go/agent | plan-2 |
| plan-6 | Frontend: `deployment_mode` editor + custody relax (public allowed) + manual-node panel UX (chip, convergence exclusion, per-node bundle download) | TS | plan-1 (parallel to 2-5) |
| plan-7 | Release the mixed-mode beta + owner smoke runbook | release | all |

Spine = plan-1 → {plan-2, plan-3 (after 2)} → {plan-4 (after 2,3), plan-5 (after 2)}; plan-6 runs in
parallel off plan-1; plan-7 gates on all.

## Decisions log

- D1–D4, D8 above (owner, 2026_06_25 AskUserQuestion + skill clarification).
- The `managed|manual` flag lives as `Node.DeploymentMode string \`json:"deployment_mode,omitempty"\``
  (`""`==managed for back-compat, mirroring `Edge.Role`), orthogonal to `role`. TS mirror
  `deployment_mode?: 'managed'|'manual'`.
- A manual node's pubkey is **operator/kit-asserted** (registered without the enroll-token ceremony),
  not enroll-proved. The `CheckWGKeyUnique` dedupe invariant MUST extend across manual + enrolled
  pubkeys. This is a trust-source change, not a custody violation (the keystone root is already the
  operator who authors the design + signs the manifest).
- Manual nodes are excluded from `SkippedUnenrolled` and from convergence (D3) but INCLUDED in the
  signed membership manifest (D4).
- **plan-1 scope refinements (during execution):** (a) `GenerateKeysWith` (render.go) needed **NO
  change** — for a manual node the AgentHeld branch already trusts the pubkey `enrolledSubgraph` stamps
  onto it from the topology + sets the private placeholder. Verified, not modified. (b) The "**a manual
  node MUST carry a pubkey**" SEMANTIC error was **moved from plan-1 to plan-2** (controller
  registration): the shared semantic validator runs PRE-keygen and would false-fire on a local-mode
  manual node whose key is generated at compile; the controller registration path is the correct home.
  plan-1 still ships the `deployment_mode` schema enum check (Go + TS) + the `enrolledSubgraph`
  admission + tests; the not-ready-manual node is excluded (not skipped) defensively.

## Closure criteria

- All 7 plans merged via reviewed PRs (4-lens + security HIGH, re-reviewed after fixes), CI green.
- A manual node compiles correctly: managed peers carry it as a `[Peer]` (pubkey + transit IP +
  allowed-IP), its own bundle carries the managed peers, and the membership manifest includes it.
- Zero-knowledge preserved (regression test: a manual node's bundle/registry never carries a private
  key; the update-topology API still refuses private keys; the kit's private key stays on-box).
- Panel shows the manual node as "manual/unmonitored", excluded from convergence; optional reporter
  surfaces its conditions.
- `internal/regression` + `internal/localcompile` golden contract green.
- Beta tagged + released after owner smoke; STATUS + memory updated; subject archived to `_completed/`.

## Plan status

| Plan | Status |
|------|--------|
| plan-1 | drafted |
| plan-2 | drafted |
| plan-3 | drafted |
| plan-4 | drafted |
| plan-5 | drafted |
| plan-6 | drafted |
| plan-7 | drafted |
