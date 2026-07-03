# STATUS
<!-- regenerated: 2026-07-03 -->
<!-- by: hand — link-directionality subject DELIVERED + RELEASED as v2.0.0-beta.18 (GitHub Latest) -->

## Active work

- **SUBJECT `link-directionality-2026_07_03` DELIVERED — RELEASED as `v2.0.0-beta.18` (GitHub
  *Latest*, 2026-07-03; tag on `1c38dfa`; beta.17 demoted).** All 4 plans merged (PRs #221–#224),
  each independently workflow-reviewed → adversarially verified → fixed at root → re-reviewed
  clean → CI-green before merge. The owner root-caused the live "NAT override goes direct"
  residue: edges are unconditionally bidirectional, so the auto-reverse peer dials the from-node's
  plain public IP and, when it handshakes first, WireGuard endpoint roaming permanently bypasses
  the relay/accelerator path. Shipped fix = per-edge **`link_direction`** (`""`≡`both` default /
  `forward` — **no stored `reverse`, D11**: single-linking the other way is an explicit editor
  FLIP that swaps from/to + mirrors pins, allocation-stable): a `forward` edge's reverse peer
  keeps its full `[Peer]` (AllowedIPs/Babel/return traffic) but carries NO dial `Endpoint`.
  - plan-1 (#221) core: both compilers + **4** loud validation codes + panel-load sanitize +
    conformance (zero churn across all 20 pre-existing success goldens; allocation provably
    direction-blind) **+ D12 discovered fix**: the TS validator never mirrored
    `validation_edge_mimic_fallback_invalid` (bad `mimic_fallback` passed in-browser Validate,
    failed Go compile) — mirrored + corpus-exercised.
  - plan-2 (#222) panel UX: node-name-labeled select (`A ⇄ B`/`A → B`/`B → A`-flip), both-mode
    **reverse-dial readout** (compiler-exact last-wins semantics), single-linked `→` chip, and the
    label pill wired to true selection equivalence (review caught a real MAJOR: the pill's
    store-only selection desynced React Flow's internal selection → default-Backspace deleted the
    WRONG edge; fixed with `addSelectedEdges` + the `elementsSelectable` gate).
  - plan-3 (#223) proof + docs: realtunnel **`c4` PASSED on the real kernel in CI** (suppressed
    side renders no `Endpoint`, tunnel forms from the dialer's inbound handshake alone, routes
    both ways) + `edge.md` §Link direction + `peer-derivation.md` rule 0 + bilingual wiki (review
    caught a BLOCKER: the rule-0 insertion had deleted normative rule 1 — restored).
  - plan-4 (#224) release: CHANGELOG (reviewed, 1 wording fix), tag, 29 assets, sidecar +
    `version` stamp verified on the published binary, promoted to Latest.
  - **OWED: owner fleet smoke of beta.18** (single-link the accelerator edge, both boot orders —
    script in Next actions), alongside the still-owed beta.17 hardening smoke; both gate rc.1.

- **SUBJECT `pre-rc1-hardening-2026_07_02` COMPLETE — RELEASED as `v2.0.0-beta.17` (GitHub *Latest*,
  2026-07-03; beta.16 demoted).** All **9 code/hardening plans merged** (PRs #208–#217), each
  independently workflow-reviewed → adversarially verified → fixed at root → re-verified → CI-green
  before merge; CHANGELOG PR #218 merged (main `907c0a5`); annotated tag `v2.0.0-beta.17` pushed →
  Release workflow green (29 assets incl. per-arch `yaog-agent-*` + `.sha256` pins) → promoted to
  Latest. Plans:
  - **Security:** plan-2 CRITICAL self-update keystone bypass on the deferred-retry swap (#208) ·
    plan-4 WG public-key validation at every ingress (#209) · plan-5 agent-route DoS hardening —
    per-node rate limit + `/report`+`/telemetry` bounds + no per-beat fsync + trusted-proxy IP (#211) ·
    plan-6 bootstrap binary SHA-256 pin (#212) · plan-7 node-ID charset validation (#213).
  - **Added:** plan-8 `agent kit verify` (#214) · plan-10 host resource telemetry via the Sampler
    framework (#216).
  - **Fixed:** plan-9 distinguishable/reasoned/persistent failed-update state (#215) · **plan-1**
    (reclassified) — the NAT "goes direct" is **WireGuard endpoint roaming** over the owner's asymmetric
    DNAT+SNAT (the `.conf` was correct), *not* a config bug; residual shipped = a port-only-override
    validator (`validation_edge_endpoint_port_without_host`, require-explicit-host) + frontend field
    coupling + roaming docs (#217).
  - **OPEN owner decision (deferred → rc.2):** a *pinned-endpoint* feature (timer re-asserts the
    configured endpoint to fight roaming) — not built; roaming is correct WG behavior.
  - **OWED: owner fleet smoke of beta.17**, then **plan-11** (refresh `docs/spec/rc1/RC1-GATE.md` + cut
    `v2.0.0-rc.1`). Owner chose "beta.17 now → smoke → rc.1".

- **TWO new subjects DRAFTED (2026-06-25), from three owner-reported items while running the live
  fleet; both foldered under `implementation_plans/` with full per-plan detail. Latest shipped is
  `v2.0.0-beta.13` (GitHub Latest).**
  1. **`theme-and-mimic-fixes-2026_06_25/`** (ships first as a fixes beta — split-release D8):
     **plan-1** theme stragglers (node-condition chips illegible in light mode → tokens; canvas grid +
     edge labels not theme-aware → neutral-map + `ROLE_HUE` dedup; Deploy button grey in dark →
     new `--cta` token family). **plan-2** the mimic "using local, did not work" bug — root cause:
     the eBPF filter `local=${MIMIC_EGRESS_IP}:<port>` is pinned to `ip route get 1.1.1.1`'s src
     (`internal/renderer/script.go:788,811`), matched by exact hash with no fallback → diverges from
     WG's real on-the-wire source (multi-homing / secondary IPs / policy routing) or resolves to
     `lo`/`127.0.0.1` → silent drop to plain UDP. Fix = route-independent per-peer `remote=` filter +
     reject loopback egress + Go test ladder (the compile-time guard was deferred — it can't see the
     runtime egress IP; see the plan-2 outline decision; data-plane confirmation is an owner real-host
     smoke, not feasible in-sandbox). **plan-3** release.
  2. **`mixed-controller-local-mode-2026_06_25/`** (ships separately after smokes — the larger
     feature, owner chose **Hybrid Kit / Option C**): per-node `deployment_mode: manual` lets a node
     be deployed by hand (no agent) inside a controller topology. Single chokepoint is
     `enrolledSubgraph` (`internal/controller/compile.go:477-532`); `peers.go` needs ZERO change.
     7 plans: model+compiler admission → registration+custody+keystone membership → signed manual
     bundle+download → on-box kit (keygen→descriptor→register→splice) → optional telemetry-only
     reporter → frontend → release. Zero-knowledge custody inviolable; manual nodes are signed
     membership members (D4); shown "manual/unmonitored", excluded from convergence (D3).
  - **NEXT = execute `theme-and-mimic-fixes` plan-1 + plan-2 (file-disjoint, parallelizable), each
    per-PR workflow-reviewed → fixed → re-reviewed → merged, then plan-3 release.**

- **`v2.0.0-beta.11` — published to GitHub Latest (2026-06-23, PR #183; beta.10 demoted).** A fast
  follow-up fixing two findings the owner hit smoking beta.10 on the live fleet (both reproduced
  against the real `hack3ric/mimic` upstream + a real `gh-proxy.com`): (1) **mimic "Discover from
  release" failed** because discovery routed the GitHub REST API through the gh-proxy, whose shared
  API token is globally rate-limited (403) — fixed by hitting `api.github.com` **directly** (egress
  guard + host-pin retained; `.deb` downloads still proxied), a forgiving+normalizing release-base
  parser, and dropping the version field from discovery; (2) **a stalled self-update rollout was
  invisible** — a deferred update (target bumped but pins still resolve to the old binary → the
  self-test correctly refuses, no brick) now surfaces as a `selfupdate: Blocked` condition (live via
  `/telemetry`), observability-only + self-clearing. Reviewed (4-lens, security-weighted) → caught a
  real **major** (the Blocked-record path could wipe custody floors on a corrupt state.json → fixed
  to bail, with a regression test) + nits → re-reviewed PASS → CI green. **Owed:** owner re-smoke of
  Discover (now direct) + the self-update re-arm (re-fetch beta.11 pins → redeploy → nodes advance).

- **SUBJECT beta9-smoke-hardening — DELIVERED (2026-06-23); `v2.0.0-beta.10` published to GitHub
  Latest.** All 5 plans merged (PRs #176 spine, #177–#181). Fixed the defects + UX gaps surfaced while
  smoking `v2.0.0-beta.9` on a live ~9-node fleet, foldered in
  [`implementation_plans/beta9-smoke-hardening-2026_06_23/`](implementation_plans/beta9-smoke-hardening-2026_06_23/outline.md).
  **Headline fix:** the beta.9 Node Conditions channel sampled conditions ONLY at apply time (false
  `wireguard: LinkDown` pre-handshake, stuck `selfupdate: HealthConfirmedProbationary`) and froze that
  worst-case snapshot while idle — made honest by a **dedicated, extensible `POST /telemetry`
  heartbeat** (agent `Sampler` framework; default 30s; carries conditions + a metrics map but NO
  applied_generation/checksum — observability split from deploy custody; plan-1 #177). Plus:
  controller-mode Validate runs the in-browser validator — browser-local verify, the controller never
  serves nor calls `/api/validate` (plan-2 #178); off-host signing-handle auto-recovery — the
  controller serves the non-secret public descriptor so a cleared browser re-prompts the authenticator
  instead of a fleet-stranding re-pin (plan-3 #179); mimic catalog discover-and-pick (`/release-assets`,
  SSRF-guarded; pick-from checklist, empty-SHA rows; plan-4 #180); CHANGELOG + release (plan-5 #181).
  Each PR independently 4-lens workflow-reviewed (security-weighted for plan-3/4) → fixed at root →
  re-reviewed → CI green → merged; full local verify (both build profiles, `-race`, airgap, FE) green
  before the tag. `release.yml` + `docker.yml` green; release promoted to **GitHub Latest**
  (`releases/latest` → beta.10; beta.9 demoted). **Process note:** a review workflow's agents ran
  `git checkout` in the shared tree and discarded uncommitted plan-3 edits → recovered via isolated
  **git worktrees** per branch + made all review workflows **checkout-free** (`git show <ref>:<path>`).
  **Owed (gating rc.1, not merge):** owner browser smoke — the live telemetry heartbeat un-freezing
  conditions on a node; controller-mode Validate; signing-handle recovery on a cleared/fresh browser
  (enroll A → clear → fresh B → Deploy prompts a tap, no re-pin); mimic Discover against the real
  upstream. **Follow-up (non-blocking):** visual-corpus baseline regen for the new mimic Discover UI.

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

**`link-directionality` is DONE and RELEASED (beta.18 = Latest). The remaining steps are
owner-only:**
1. **Owner fleet smoke of beta.17 + beta.18** — the hardening set, plus the beta.18 script:
   update the panel/agents to beta.18 → open the accelerator edge → set Link direction to
   `<NAT-peer> → <hub>` (or use the flip choice if drawn the other way; the editor prefills the
   accelerator host) → Deploy → on the hub, `wg show <iface>`: the peer for the NAT-side node must
   show NO configured endpoint until its handshake arrives, and the runtime endpoint must be the
   ACCELERATOR's egress, never the peer's direct IP → restart the two nodes in BOTH orders
   (peer-first, hub-first) and confirm the path never goes direct. Pass-or-accept-risk.
2. **pre-rc1-hardening plan-11 — cut `v2.0.0-rc.1`:** refresh `docs/spec/rc1/RC1-GATE.md`, roll the
   CHANGELOG, tag + publish. (rc.1 is `make_latest=true` in `release.yml`, so it self-promotes;
   betas need the manual `gh release edit --latest`.) Archiving
   `link-directionality-2026_07_03/` to `_completed/` rides that session. Say the word when the
   smokes are clean.

Separate from the release: the owner's live WireGuard-endpoint symptom is a fleet **NAT/roaming**
matter — the deterministic in-product fix is the `link-directionality` subject above (single-link
the edge so the reverse peer can never race the relay); operational alternatives remain (a)
advertise the accelerator as the node's `public_endpoints`
so BOTH link directions ride the relay (an L7 connection-terminating relay needs a local UDP-over-its-
transport wrapper + WG endpoint = `127.0.0.1:<port>`), and (b) confirm the agent is actually applying
deploys (`applied` vs `desired` generation; `install.sh` DOES full down→up WG then restart babeld, so a
stale interface means the apply didn't run, not that deploy skips the restart). A `journalctl -u
yaog-agent` slice would pin which. Any resulting apply-path fix is a post-beta.17 change.

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

- `beta9-smoke-hardening-2026_06_23` (2026-06-23) — **5 plans, `v2.0.0-beta.10` → GitHub Latest (PRs
  #176–#181).** Live-fleet smoke fixes: a dedicated `/telemetry` heartbeat + `Sampler` framework that
  makes Node Conditions honest (no more frozen apply-time snapshot); controller-mode Validate →
  in-browser (kills the `/api/validate` 404); off-host signing-handle auto-recovery (serve the
  non-secret descriptor → no fleet-stranding re-pin); mimic catalog discover-and-pick (SSRF-guarded
  `/release-assets`). Each PR 4-lens-reviewed → fixed → merged; review workflows made checkout-free
  after a shared-tree clobber; isolated git worktrees per branch.
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
