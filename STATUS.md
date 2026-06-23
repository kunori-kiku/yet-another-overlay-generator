# STATUS
<!-- regenerated: 2026-06-23 -->
<!-- by: draft-implementation-plan — beta9-smoke-hardening subject drafted (5 plans), executing plan-1 -->

## Active work

- **NEW SUBJECT EXECUTING: beta9-smoke-hardening (2026-06-23).** Fixes the defects + UX gaps surfaced
  while smoking `v2.0.0-beta.9` on a live ~9-node fleet, shipping as `v2.0.0-beta.10` (→ Latest). 5
  plans, foldered in
  [`implementation_plans/beta9-smoke-hardening-2026_06_23/`](implementation_plans/beta9-smoke-hardening-2026_06_23/outline.md)
  (owner-approved). **Headline:** the Node Conditions channel is apply-time-only → the panel freezes a
  worst-case post-apply snapshot (false `wireguard: LinkDown` sampled pre-handshake, stuck
  `selfupdate: HealthConfirmedProbationary`) though the overlay is healthy — fixed by a **dedicated,
  extensible `/telemetry` monitoring heartbeat** (plan-1). Plus: controller-mode Validate runs the
  in-browser validator (kill the `/api/validate` 404, plan-2); off-host signing-handle auto-recovery
  (serve the public descriptor — no fleet-stranding rotation, plan-3); mimic catalog discover-and-pick
  (plan-4); release beta.10 (plan-5). Each PR independently workflow-reviewed → fixed → re-reviewed →
  merged. **plan-1 EXECUTING.**

- **SUBJECT agent-feedback-and-version-aware-rollout — DELIVERED (2026-06-23); `v2.0.0-beta.9`
  published.** All ten plans done. The reusable structured agent→panel **Node Conditions** channel
  (plan-1/2/3), **mimic→UDP per-link fallback** (plan-4/5/6), **version-aware rollout** — panel knows +
  displays its own version, "Update all" → panel version, refuse a target newer than the panel
  (plan-7/8), and **default release URLs / working "Assist from release"** (plan-9). Each PR
  independently workflow-reviewed (4 lenses) → fixed at root → re-reviewed clean → CI green → merged
  (PRs #162–#173). plan-10 rolled the CHANGELOG (#171; the review caught that the beta.9 delta is the
  whole pre-rc.1 program PRs #137–#171, so the notes carry a full `Security` section + the air-gap
  boundary change) and **published `v2.0.0-beta.9`**, then promoted it to **GitHub Latest** at the
  owner's request (easier deploy — the `releases/latest/download` alias now resolves to beta.9;
  promoting a release to Latest clears its prerelease flag, so beta.9 is a non-prerelease Latest and
  beta.8 is demoted). The first tag push exposed a real **release.yml gate bug** (gate-e2e ran the
  non-blocking visual corpus AND built the panel without `VITE_E2E=1`, so the required ErrorBoundary
  spec deterministically failed) — fixed in **#173** (gate-e2e now mirrors ci.yml's required job), tag
  re-cut from the green tip, `release.yml` + `docker.yml` green, all 29 assets present (7 bundles, 7
  airgap servers, agent linux/windows binaries **+ `.sha256` sidecars**, local-design zip). Smokes:
  published `yaog-server`/`yaog-agent` `version` → `v2.0.0-beta.9`; agent `.sha256` verifies the
  binary; `DefaultMimicReleaseBase` (hack3ric/mimic) reachable. **Owed:** owner browser+two-node smoke
  of the new panel features (the agent-feedback subject's UI) — beta.9 was cut so the owner can smoke.
  **Follow-up (non-blocking):** regenerate the visual-corpus baselines for the new settings UI via a
  reviewed `--update-snapshots` run (the corpus is `continue-on-error` until that determinism pass).

- **PRE-RC.1 PROGRAM COMPLETE (authorable scope) — all 22 plans across Subjects 1–4 merged (PRs
  #137–#159, 2026-06-19/21).** Every CI-gated rc.1 criterion is GREEN. The remaining steps to cut
  `v2.0.0-rc.1` are **owner-only** and tracked in [`docs/spec/rc1/RC1-GATE.md`](docs/spec/rc1/RC1-GATE.md)
  (the single-source-of-truth go/no-go) — see **Next actions**.

- **SUBJECT 1 (refactor + security) COMPLETE — all 9 plans merged to `main` (2026-06-19).** The
  local-mode→browser migration shipped: plan-1 CJK→English hygiene (#137), plan-2 god-file splits
  (#139), plan-9 FE↔Go drift (#138), plan-8 residual security + compiler-correctness (#140), plan-3
  `internal/localcompile` façade + frozen I/O contract + golden corpus (#141), plan-5 Go↔TS
  conformance harness + required CI gate (#142 — caught + fixed a real F3 heal drift on its first
  run), plan-4 the full **TypeScript compiler** byte-exact vs the Go oracle (#143), plan-6 store rewire
  to the in-browser compiler (#144), plan-7 backend shrink — air-gap compute behind `//go:build airgap`,
  controller-only default, local mode **default-ON** (#145). Each: independent multi-lens workflow review
  → fix at root (no shims) → re-review GO → CI green → merge. Both Go build profiles (default +
  `-tags airgap`) are CI-gated; the conformance harness pins TS==Go byte-for-byte.
- **SUBJECT 2 (phone UX) COMPLETE — all 3 plans merged to `main` (2026-06-19, PR #147).** One combined
  branch `feat/phone-ux-subject2`: plan-11 reusable off-canvas `Drawer` primitive + `useMediaQuery`
  (Contingency B — owns the primitive AND the sidebar consumer), plan-10 descriptor-spine responsive
  operator surfaces (desktop table / mobile cards), plan-12 small-screen read-only design-canvas gate
  (editing hard-disabled below `lg`; the store cannot be mutated from the gated canvas). Frontend-only;
  no backend/contract change. Independent 3-lens review (correctness/no-desktop-regression ·
  completeness/Contingency-B-scope · hygiene/adversarial) → GO/0-blockers → 3 non-blocking findings
  fixed at root (gate-scrim-before-drawer; aria-label key rename) → CI green → merged.
- **IN PROGRESS: SUBJECT 3 (full-stack E2E simulation / pitfall-hunt, plans 13–19).** Delivered to
  main: plan-13 (harness, PR #149), plan-14 (operator flow, #150), plan-15 (adversarial/edge, #152),
  plan-16 (edge-case & adversarial hunt — Go fuzz/DoS corpus + browser fault-injection, #153). plan-17
  (phone-UX device-emulation — the **responsive verification layer**: `frontend/e2e/responsive/` device
  matrix + 8 behavior smokes + a visual-regression corpus; verifies Subject 2) IN PROGRESS. Remaining:
  plan-18 (3.6 real-tunnel netns/containers — MANDATORY before rc.1; likely needs a privileged host),
  plan-19 (3.7 closure). Then Subject 4 (security re-audit, plans 20–21 + plan-22 cuts rc.1). rc.1 is
  NOT cut until all four subjects are done.
- Decision (2026-06-19, in the outline decisions log): local-engine **default-ON** folded into plan-7
  (the real-world soak gate is waived — replaced by the green conformance harness); the
  `VITE_YAOG_LOCAL_ENGINE=backend` escape hatch is retained (works against a `-tags airgap` server).
- **SUBJECT 3 COMPLETE — plans 13–19 merged (2026-06-19/20, PRs #149–#156).** plan-18 (3.6 real-tunnel
  netns gate, PR #155) green-and-required on CI (`ubuntu-latest` boots nested systemd-nspawn); plan-19
  (3.7 closure, PR #156) authored `docs/spec/rc1/RUNBOOK.md` (9 owed smokes → 3 irreducible hardware
  legs) + the criterion-C1 owed-smoke ledger in `RC1-GATE.md`.
- **IN PROGRESS: SUBJECT 4 (security re-audit, the LAST subject before rc.1).** plan-20 (4.1) authored
  the post-refactor **re-audit charter** at `docs/spec/rc1/plans/4.1-reaudit-charter.md` (11-surface
  inventory: O1–O7 re-verify + N1–N4 new; the two-lens workflow; the 14+B1–B4 baseline disposition map;
  the exit bar — amended to include `realtunnel` + conformance green-and-required; the owner sign-off
  path). plan-21 (4.2) EXECUTES it (→ `docs/spec/rc1/4.2-verdict.md`); plan-22 (4.3) cuts rc.1.

### Prior release history

- **Released:** **`v2.0.0-beta.8`** (GitHub *latest*) — pre-rc.1 blocker hotfix (PR #136). Fast-tracked six
  investigation-confirmed blockers: fleet-mux panic recovery (B1), keystone-sign-on-refresh 401 (F1),
  babeld.conf byte-stability under edge reorder (C1), and enrollment-lifecycle hardening (S4 revoked-
  resurrection guard, S5 enrollment-token purge-on-revoke, S6 TTL cap). Independent review GO (0 findings)
  → CI + Release + Docker green.
- **Drafted (awaiting execution + owner sign-off on 3 pending decisions):** the **pre-rc.1 program** —
  `implementation_plans/pre-rc1-2026_06_18/` (outline + 22 plan files across 4 subjects: refactor+security →
  phone UX → full-stack simulation → security audit again → rc.1). Built via the `draft-implementation-plan`
  skill from a 55-agent investigation → adversarial critique → coherence reconciliation. Pending owner
  decisions: air-gap removal mechanism (build-tag vs delete), transit-CIDR const home, rc.1 `--prerelease`.
- **Released:** **`v2.0.0-beta.7`** (superseded by beta.8) — edge-pin-collision root-cause fix (PR #135).
  Fixed the **"pin occupied by two different links"** corruption the operator hit on a live fleet
  (validate showed 10 errors while incremental deploys looked fine). Root cause: incremental
  enrollment compiles only the enrolled subgraph (dropping not-yet-enrolled edges), so the allocator's
  gap-fill restarted each pool from the bottom without seeing the dropped edges' pins, handing two
  edges that were never compiled together the same transit IP / port / link-local. **Prevent:**
  subgraph compiles now **reserve out-of-subgraph edge pins** (into both endpoint domains' pools) so a
  new node's links never re-use a live link's resource — full compiles byte-for-byte unchanged.
  **Clean:** `internal/normalize.HealCollidingPins` (inverse of the validator's cross-link dedup)
  strips the colliding edge so it re-allocates fresh, wired at the `update-topology` write path, at
  `CompileAndStage` start (**deploy self-heals** an already-corrupt fleet), and on every panel canvas
  load (TS mirror). Verified against the real topology: **10 collisions → 0.** Also: **controller-mode
  design Export/Import** (server-authoritative — strip keys → update-topology → re-hydrate; no
  localStorage fleet-data leak; never auto-deploys) and an edge-inspector port-label clarification
  (names the node, display-only). Reviewed by an independent 4-dimension workflow (GO, 0 blockers) →
  findings applied (multi-CIDR superset reservation, heal-on-stage, TS↔Go heal parity) → full suite +
  `tsc -b` + eslint green; CI green.
- **Prior releases:** **`v2.0.0-beta.6`** — fleet/keystone operability (PR #134, atop
  #133), bundling the bugs surfaced during live fleet operation. A stuck "Roll keys" rotation can now
  be released without evicting the node (`POST {operator}/clear-rekey`, idempotent + audited +
  strictly weaker than revoke; per-node **"Cancel rekey"** button); the panel's **Deploy gate is
  advisory** (a `window.confirm`), not a hard block, so a single offline straggler no longer wedges
  every deploy; an **edge role flip no longer corrupts allocation pins** (the editor now clears all
  six `pinned_*` + `compiled_port`, with a pure/idempotent **load-time auto-heal**
  `healDuplicatePinnedBackups` that strips a backup's pins iff its transit IPs collide with a same-pair
  primary); the **fleet view reflects server truth without a re-login** (refresh-on-auth on
  Fleet/Deploy + immediate refresh when "Live" is enabled); and **bootstrap re-pins the operator
  credential by default + `systemctl restart`s the agent** (#133) so a re-bootstrap's new
  token/credential actually takes effect. Reviewed by an independent multi-dimension workflow (GO, 0
  blockers) → nits applied → full suite + `tsc -b` + eslint green; CI green.
- **`v2.0.0-beta.5`** — keystone-rotation safety (PRs #129/#130/#131
  + #132). Reproduced and fixed the root cause where rotating the off-host operator credential
  silently stranded the whole fleet: a changed credential now requires an acknowledged rotation, the
  controller exposes a server-truth `redeploy_required` signal, and the agent gains
  `reprovision-keystone`. A non-release adversarial regression suite (`internal/regression`) then
  surfaced three adjacent trust-list-serving bugs — all fixed: the **served-vs-staged trust-list
  split** (a mid-deploy re-stage no longer bricks `/config`), a **monotonic anti-rollback floor**
  across a keystone-OFF apply, and an **atomic `GetServedConfig`** snapshot (no torn bundle/manifest
  pair); plus `keystone_no_signed_manifest` reclassified 500→409. **`v2.0.0-beta.4`** — a security hardening fix (PR #128): the
  controller persists the bundle-signing **public** key per tenant (`SigningAnchor`) and reconciles
  it at stage time, so a redeploy that drops or swaps `YAOG_BUNDLE_SIGNING_KEY` now FAILS LOUD
  (`signing_key_missing` 412 / `signing_key_mismatch` 409) instead of silently shipping unsigned
  bundles. Trust-on-first-use; rotation via `YAOG_BUNDLE_SIGNING_KEY_ROTATE`; private key stays
  off-host; pin/rotate audited; air-gap export unchanged. **`v2.0.0-beta.3`** — the operator-panel UI for agent self-update +
  canary-then-fleet (the descoped plan-9 "Canary UI"): agent + mimic config cards, assisted
  release-pin fetch (`POST /release-pins`, SSRF-guarded), per-node update-status chip + opt-in live
  poll, the full-replace drop-on-save fix (PRs #121–#126). Atop **`v2.0.0-beta.2`** /
  **`v2.0.0-beta.1`** (`signed-self-update-and-rc-hardening`, PRs #109–#118).

## Open questions / blockers

- **Owed manual smokes (owner-accepted risk), gate rc.1 — not code-merge:** the nine beta.1–Subject-2
  owed smokes are triaged (9 → 3 irreducible owner-run legs + 1 open dependency) in
  [`docs/spec/rc1/RUNBOOK.md`](docs/spec/rc1/RUNBOOK.md), with their live A/B/C state in the criterion-C1
  ledger of [`docs/spec/rc1/RC1-GATE.md`](docs/spec/rc1/RC1-GATE.md). Not re-listed here (single source of
  truth — no third drift surface).
- **rc.1 gates on [`docs/spec/rc1/RC1-GATE.md`](docs/spec/rc1/RC1-GATE.md); the owner signs the go/no-go
  there.** That document is the single source of truth for the criteria (A–E), the owed-smoke ledger (it
  references `docs/spec/rc1/RUNBOOK.md`), the required-checks set, and the release runbook.
- **Deferred to rc.2/GA** (documented, not built): the bootstrap-TOFU hole (the agent's first binary
  is fetched without a pre-shared pin); the FileStore SPOF (global mutex + 200ms generation poll) fix;
  a reliable *persistent* per-node `failed` update-state (would need a positive agent-reported field —
  the chip's `failed` is best-effort/transient today); the full wiki rewrite; a frontend test runner.
- No code blockers. `main` is green.

## Next actions

**Subjects 1–4 are all delivered + merged (PRs #137–#158).** The rc.1 gate is authored and every
*automatable* criterion is GREEN in CI (`go` incl. `-race`, `frontend`, `conformance`, `frontend-e2e`
incl. the `@security` specs, `realtunnel`, `security-scan` incl. govulncheck). The remaining steps are
**owner-only**, tracked in [`docs/spec/rc1/RC1-GATE.md`](docs/spec/rc1/RC1-GATE.md):

0. ✅ DONE — `realtunnel-bakein` 20/20 + negative proof green on CI (run 27881474085, 2026-06-21).
1. Run the three irreducible hardware smokes (`docs/spec/rc1/RUNBOOK.md` §C1 authenticator / §C2
   real-NAT-box / §C3 mimic eBPF ≥6.1) + owed-smoke #5 (rollout UI) — pass-or-accept-risk.
2. Set branch protection to require `go`, `frontend`, `conformance`, `frontend-e2e`, `realtunnel`.
3. Sign the go/no-go in RC1-GATE.md, then execute the release runbook (CHANGELOG roll → annotated tag →
   `--latest` publish → verify). rc.1 ships as GitHub **Latest** (beta.8 demoted — the 2026-06-18 owner
   override).

## Recently closed subjects (last 3)

- `pre-rc1-2026_06_18` (2026-06-19/21) — **the full pre-rc.1 program: 22 plans across 4 subjects (PRs
  #137–#159).** Subject 1 refactor+security (TS browser compiler, controller-only backend, plan-8
  fixes), Subject 2 phone UX, Subject 3 full-stack E2E sim + the MANDATORY real-tunnel netns gate,
  Subject 4 the final security re-audit (GO verdict, `internal/dast` live-wire, `security-scan` CI incl.
  govulncheck which caught + fixed go1.25.0 stdlib CVEs via the go1.26.4 toolchain bump). Each plan:
  build → independent multi-lens workflow review → fix → re-review clean → CI green → merge. The rc.1
  go/no-go gate is authored at `docs/spec/rc1/RC1-GATE.md`; the **terminal `v2.0.0-rc.1` tag cut is
  owner-only** (hardware smokes + 20/20 CI bake-in + branch protection + owner signature).
- `keystone-rotation-safety` (2026-06-17, **released `v2.0.0-beta.5`, GitHub latest**) — reproduced +
  fixed the keystone-rotation fleet-stranding root cause (acked rotation, server-truth
  `redeploy_required`, `yaog-agent reprovision-keystone`; PRs #129/#130/#131); built the non-release
  `internal/regression` suite, which surfaced three adjacent fixes — served-vs-staged trust-list split
  (re-stage no longer bricks `/config`), monotonic anti-rollback floor, atomic `GetServedConfig` — plus
  `keystone_no_signed_manifest` 500→409 (PR #132). Reviewed → fixed → re-reviewed.
- `controller-panel-rollout-ui-2026_06_16` (2026-06-16, **released `v2.0.0-beta.3`**) —
  the operator-panel UI for signed agent self-update + canary-then-fleet (the descoped plan-9 Canary
  UI): agent + mimic config cards, assisted release-pin fetch (`POST /release-pins`, SSRF-guarded),
  per-node update-status chip + opt-in live poll, and the full-replace drop-on-save fix. PRs #121–#126.
- `signed-self-update-and-rc-hardening-2026_06_15` (2026-06-16, **delivered**) — beta.1 (mimic from
  GitHub with SHA-256-pinned `.deb` + signed `artifacts.json`, agent version reporting + build-version
  injection, full input validation + backend robustness, controller-mode UX/resilience, RC paperwork)
  and beta.2 (signed agent self-update + canary-then-fleet, verified-before-exec, brick-bounded).
  PRs #109–#118; released `v2.0.0-beta.1` then `v2.0.0-beta.2`.
- `controller-nat-customization-2026_06_15` (2026-06-15, delivered) — controller mode made
  server-authoritative + operator-customizable at the NAT boundary; sticky per-edge NAT port + transit
  IP, zero-knowledge compile-preview, per-node `listen_port` removed. PRs #98–#106; `v2.0.0-preview.10`.
