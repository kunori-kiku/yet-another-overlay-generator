# STATUS
<!-- regenerated: 2026-06-19 -->
<!-- by: pre-rc1 program — Subject 2 closed -->

## Active work

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

- **Owed manual smokes (owner-accepted risk), gate rc.1 — not code-merge:**
  1. Two-node controller WebAuthn login → hydrated canvas + login-survives-refresh + no token in
     localStorage (beta.1).
  2. NAT sticky-pin Compile → edit port/transit IP → deploy → no drift (beta.1).
  3. mimic GitHub-`.deb` install on a kernel-≥6.1 Debian host (beta.1).
  4. **Self-update field smoke (beta.2):** canary agent version → download/verify/swap/re-exec →
     badge flips → promote to fleet; tampered hash refused keep-last-good; crashing binary rolls back
     within the attempt cap. Mechanism unit-tested + deep-reviewed; the live end-to-end run is owed.
  5. **Panel rollout-UI smoke (beta.3):** in controller mode, the agent + mimic config cards render,
     "Assist from GitHub release" pre-fills pins, a bootstrap-field save round-trips the rollout/mimic
     config (drop-on-save), fleet-wide gates on the confirm, the per-node chip shows
     pending→applying→applied as a canary advances, and the Live poll stops on logout. No FE test
     runner exists, so this is owner-verified in a browser.
  6. **Keystone rotation + reprovision smoke (beta.5):** on two real systemd hosts — rotate the
     operator credential (acked), confirm the fleet refuses the served bundle (fail-closed) and the
     panel shows `redeploy_required`, then `yaog-agent reprovision-keystone` on a node re-pins the new
     key and `systemctl restart`s so it trusts the fresh signed deploy; plus the WebAuthn-passkey
     rotation path. The headless path is covered (in-process + real-binary ed25519 bash repro + the
     `internal/regression` suite); the real-host restart + passkey legs are owed.
  7. **Fleet-operability panel smoke (beta.6):** in controller mode — a stuck "Roll keys" straggler
     is released by the per-node "Cancel rekey" button (node stays approved, keeps polling); Deploy
     stays enabled while nodes rekey and routes through the advisory confirm; flipping an edge
     primary↔backup then re-compiling shows NO "pin occupied by two different links"; an existing
     topology with a duplicate-pinned backup auto-heals on load; the registry reflects server truth on
     login/reload without a manual re-login and "Live" refreshes immediately. No FE test runner, so
     owner-verified in a browser.
  8. **Pin-collision + Export/Import smoke (beta.7):** on the real fleet whose stored topology had the
     collision — log in (canvas heals, local Validate now passes), then Deploy and confirm the staged
     bundles compile clean (deploy self-heal) and the previously-colliding links come up with fresh,
     non-overlapping transit IPs/ports. Separately: controller-mode Export downloads the design;
     Import of that file writes a new server version, re-hydrates the canvas, and does NOT deploy or
     leave fleet data in localStorage. No FE test runner, so owner-verified in a browser.
  9. **Phone-UX smoke (Subject 2):** on a real ~360–414px viewport — the Topbar hamburger opens the
     off-canvas nav Drawer (focus-trap, Esc, backdrop-click, route-change auto-close), the Drawer never
     leaks onto the login/splash branch and does not reopen on refresh; operator pages reflow to mobile
     cards; the `/design` route shows the read-only gate below `lg` and editing stays disabled (no node
     drag / edge draw / store mutation) in the read-only preview; no desktop ≥1024px regression. These
     are owed-by-design (no in-env browser) and are slated to be **covered by Subject 3's device-emulation
     E2E harness** (plan-13/plan-17), shrinking the owed-manual list rather than persisting it.
- **rc.1 is a later owner call** once the owed smokes pass and the beta soak is clean.
- **Deferred to rc.2/GA** (documented, not built): the bootstrap-TOFU hole (the agent's first binary
  is fetched without a pre-shared pin); the FileStore SPOF (global mutex + 200ms generation poll) fix;
  a reliable *persistent* per-node `failed` update-state (would need a positive agent-reported field —
  the chip's `failed` is best-effort/transient today); the full wiki rewrite; a frontend test runner.
- No code blockers. `main` is green.

## Next actions

1. Execute **Subject 3** (full-stack E2E simulation / pitfall-hunt, plans 13–19): plan-13 harness FIRST
   (Playwright + virtual-WebAuthn-via-CDP + device emulation + network-fault injection against the real
   built FE + a live Go controller + real/mock agent), then 14–17, then the MANDATORY real-tunnel
   integration plan-18 (containers/netns bring up the GENERATED WireGuard+Babel and assert tunnels form
   + routes converge), then plan-19 last. Same per-plan rhythm: build → independent multi-lens review →
   fix at root → re-review GO → CI green → merge.
2. Then Subject 4 (security re-audit, plans 20–21) → plan-22 cuts `rc.1` (gated on real-tunnel +
   conformance harness green; plan-22 is the sole tag authority and adds `-race`, `govulncheck`,
   frontend-e2e + realtunnel as required checks).
3. Owner: run the owed manual smokes on real hardware/fleet (see Open questions / blockers) — the
   Subject-2 phone smokes and several earlier ones are slated to be absorbed by the Subject-3 harness.

## Recently closed subjects (last 3)

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
