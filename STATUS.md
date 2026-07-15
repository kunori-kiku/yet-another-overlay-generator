# STATUS
<!-- regenerated: 2026-07-16 -->
<!-- by: hand — v2.0.0-rc.6 remains GitHub Latest; rc.7 is immutably tagged at c3c5c25 and its exact GHCR version image exists at sha256:7e71e286 after every release gate, exact-22 check, and native-Windows check passed, but a parent-index runtime-verifier defect stopped the transaction before any GitHub release object or Latest promotion; child-digest verification + an exact-state recovery workflow are underway -->

## Active work

- **✅ SUBJECT `post-refactor-debt-paydown-2026_07_14` — COMPLETE 2026-07-15 (14/14 merged; archived to
  `_completed/`). Shipped as `v2.0.0-rc.6`; the independently reviewed, gate-green rc.7 implementation
  is immutably tagged at `c3c5c25` and awaiting completion of its failed publication transaction, and
  carries enrollment-scoped WebAuthn UV proof instead of plan-6's blanket runtime/node gate.** The
  successor to `framework-refactor`, from
  a fresh **30-agent repo-wide debt sweep + a 7-agent security-correctness gap-pass** (both briefed to
  NOT re-report shipped work) → **14 plans in 4 tiers**:
  correctness/security fixes → structural paydown → machine-gate/FE → doc/state hygiene. Executed per-PR
  with the full review regime (build → independent workflow review → fix → re-review → CI-green → merge),
  gated by an **18-agent pre-execution plan review** (GO-WITH-FIXES; caught 3 real blockers before any
  code — plan-3 fixed 1/4 SNAT sites + invented a divergent teardown, plan-5's lockTenantOps unreachable,
  plan-9's field_safety infeasible) and a **10-agent adversarial review-at-last** (GO-WITH-FIXES; both
  findings fixed — #291 stray wasm, #292 client-mimic). Folder:
  [`implementation_plans/_completed/post-refactor-debt-paydown-2026_07_14/`](implementation_plans/_completed/post-refactor-debt-paydown-2026_07_14/outline.md)
  (+ `ASSESSMENT.md` evidence base, `REVIEW-CORRECTIONS.md`). **Headline defects, all fixed at root +
  regression-pinned:** (1) the WASM engine (default+only in-browser local engine) was NEVER built in the
  release/Docker pipelines → every shipped panel 404s on `/yaog.wasm`, CI-invisible → **now built in both
  + red-build asserted + fault-tolerant load** (#278); (2) standalone `install.sh` root-installed an
  attacker `.deb` — an unlisted `artifacts.json` passed `bundle.sig`+`sha256sum -c` (the agent path is
  guarded at `verify.go:225`, the shell mirror was not) → **coverage guard mirrored into the shell,
  fail-closed** (#277); (3) `deploy.go --uninstall` orphaned mimic (root eBPF) + drifted on the SNAT
  delete → **mimic teardown + CIDR-agnostic SNAT in both shells** (#279, +#292 client-mimic); (4) the
  self-update **exact-vs-semver** comparator wedged the whole channel on a `v`-less target → **routed
  through `compareVersions` + a 256 MiB cap** (#280); (5) the trust-list-sign vs re-stage custody race →
  **one `lockTenantOps` over read+guard+verify+write + a durable-only `GetNodeRecord`** (#281). Plus the
  Tier-2/3/4 paydown (agent `ControllerLoop` #283, byte-identical `derivePeersWithDomains` split #284,
  `handler_bootstrap` split + agent-mux adapter #285, the non-vacuous wire-DTO drift gate #286, finished
  `Field` adoption #287, six-subject archive + reconcile #288, airgap/TS-compiler prose purge #289,
  pipeline/Docker hygiene #290). **Negative evidence: NO trust-root bypass, NO key leak, NO shipped CVE —
  the controller/agent-managed paths are sound; the defects lived in the mirrors/edges.** Owner scope
  decisions taken provisionally during execution now stand (comprehensive/all-4-tiers ·
  fix-ship-breaker-first-no-out-of-band-release · ran-the-security-pass · name). **✅ plan-6 (WebAuthn UV,
  #282) MERGED 2026-07-15, THEN SUPERSEDED BEFORE RELEASE:** a blanket runtime requirement would have
  changed the acceptance contract underneath existing users, potentially locking out credentials they
  had already enrolled and making upgraded nodes reject the fleet's currently served manifest. The rc.7
  implementation verifies UV only while a new browser credential is enrolled: login and keystone
  enrollment each use an authenticated, purpose+actor-scoped, one-use server challenge and a second
  assertion by the exact candidate credential before persistence. Normal login/signing/membership
  remains signature + binding + User-Presence verified; the first-party browser prefers UV for later
  assertions without requiring it, and both enrollment surfaces warn that a later non-UV assertion is
  possession-only. WebAuthn backup/sync state is separate from UV. **The initial rc.7 tag run failed
  before publication because checkout flattened the local annotated tag; after the centralized fix and
  documented pre-boundary retag, Release run `29437046676` passed every gate, the exact-22 asset check,
  and native Windows execution, then published immutable version image `sha256:7e71e286…`. Its new
  read-back verifier incorrectly reused the multi-arch parent digest across sequential amd64/arm64 runs
  on Docker's classic image store, so no GitHub release object was created and Latest remains rc.6. The
  image children, labels, and runtime versions are exact; the tag/image may not move, and an exact-state
  automated recovery is underway. No fleet re-sign is required and existing manifests remain valid.**
  The merged implementation also reconciles the bilingual operator wiki and
  controller-agent lifecycle documentation. Deferred, non-blocking: **plan-3.5**
  (go:embed/`ShellToken` PowerShell deploy templating —
  no PS1 `ShellToken` constructor yet). Memory: `post-refactor-debt-paydown-shipped.md`.

- **📋 SUBJECT `mixed-controller-local-mode-2026_06_25` — PARTIALLY SHIPPED; still ACTIVE (plans 5 + 7
  pending).** The owner-chosen **Hybrid Kit (Option C)**: mark individual nodes `deployment_mode: manual`
  (no agent) inside a controller-managed topology — the controller compiles + signs their bundle exactly
  like any managed node, the operator installs it by hand, and a one-shot on-box `yaog-agent kit` does
  keygen → descriptor → register → private-key splice. Zero-knowledge custody stays inviolable; manual
  nodes are signed-membership members (D4) and appear "manual/unmonitored", excluded from convergence
  (D3). **Six of eight plans MERGED** (plan-1/2/3/4/6/8; PRs #196–#202 — the self-update reliability
  rider plan-8 = #201), all shipped in **`v2.0.0-beta.15`** (CHANGELOG roll #203). **Remaining before
  this subject closes: plan-5 (optional telemetry-only reporter for manual nodes) + plan-7 (release +
  owner two-node-with-one-manual smoke).** Folder:
  [`implementation_plans/mixed-controller-local-mode-2026_06_25/`](implementation_plans/mixed-controller-local-mode-2026_06_25/outline.md).

- **✅ SUBJECT `telemetry-history-and-delta-deploy-2026_07_13` — DELIVERED as `v2.0.0-rc.5` (GitHub
  *Latest*, 2026-07-13; annotated tag on `ac3d660`; rc.4 demoted; self-promoted; 29 assets);
  CLOSED + archived to `_completed/`.** All 8 plans merged (PRs #249–#257), each independently
  workflow-reviewed → fixed → re-reviewed → CI-green → merged. **(A) Resource history + charts** — agent
  `cpu_pct` (stateful `/proc/stat` jiffies delta; first-beat gap, never a fabricated 0); controller
  keeps a bounded per-node history (in-memory O(1) append — the heartbeat NEVER fsyncs — + a 5-min
  off-heartbeat flusher → append-only per-node JSONL with amortized compaction; configurable cap
  `TelemetryHistoryCap` nil⇒20160≈7d/0⇒off, a persisted 0 surviving restart via `capLoader`); operator
  `node-history` query aggregates server-side (avg/min/max, gaps omitted, step floor 1s/widen ≤1000);
  panel charts via lazy Recharts behind a reusable series-generic `TimeSeriesChart`. **(B) Delta deploy**
  — per-node skip on `hex(sha256(checksums.sha256))` (excludes volatile `compiled_at`) → unchanged node
  keeps its generation → its agent never re-fetches (mixed-gen fleet); FAIL-OPEN; keystone-aware disable
  on first-pin/rotation; zero-changed short-circuit PURGES lingering staged bundles; `WithForceAll`/
  `WithForceNodes` (drift/rescue) + `DeployPreview` (canvas dry-run, best-effort "Deploy anyway").
  **(plan-1.5) Telemetry-freshness FRAMEWORK fix** for the recurring "a new metric only fires at deploy
  time, then freezes" class: metrics ride the `Sampler` heartbeat as the SOLE producer (`/report` =
  conditions only) + a coalescing post-apply kick; conditions stay dual-write. Review caught 3 real
  defects pre-merge (plan-5 zero-changed custody blocker; plan-6 preview-vs-canvas mismatch + hard-block;
  plan-7 doc "single producer of conditions" overstatement). Memory:
  `telemetry-history-and-delta-deploy-shipped.md`. **Owed (owner): update the controller to rc.5 +
  browser-smoke the charts (granularities + cap incl. 0=off) and delta deploy (unchanged topology →
  "0 updated, N unchanged", NO node refresh; change one → only it refreshes; Force redeploys an unchanged
  node). A defect during the rc.5 soak → rc.6 under the same gate rules.**

- **✅ SUBJECT `mimic-fleet-robustness-2026_07_07` — DELIVERED as `v2.0.0-rc.4` (GitHub *Latest*,
  2026-07-07; tag on `cbe0735`; rc.3 demoted; self-promoted); CLOSED + archived to `_completed/`.**
  The rc.3-soak fleet findings, each fixed per-PR (independent workflow review → fix → re-review → CI
  green → merge): (1) mimic build deps — **`bubblewrap`** + **`dwarves`**, which mimic-dkms's DKMS
  build needs but declares neither, now `_pm_install`ed in the provisioning step AND the
  `_mimic_module_ready` retry (#241; the retry locus was the review catch — a binary-present but
  module-unbuilt node short-circuits provision on `command -v mimic`); (2) unconditional Phase-0
  teardown so tcp→udp de-provisions the stale `mimic@` (#241); (3) live mimic condition — re-probes
  `systemctl is-active mimic@<egress>` each heartbeat → a `Stopped` warn (#242); (4) relay-path warning
  `validation_edge_mimic_relay_path` — mimic needs a direct L3/L4 path, no L7 relay (#243); (5) docs
  (#244); rc.4 roll (#245). **Deferred (out of scope):** auto-coordinated fallback (telemetry→compile;
  the clean fix for a genuinely-unbuildable node is `transport: udp` both ends). **Owed (owner):**
  update the controller to rc.4 + redeploy the fleet (fixes are in the rendered `install.sh`, not the
  agent binary; `apt-get install -y bubblewrap dwarves` is now automatic on redeploy); set L7-relay
  edges to `transport: udp`. Memory: `mimic-fleet-robustness-shipped.md`.

- **✅ SUBJECT `mimic-runtime-reliability-2026_07_06` — DELIVERED as `v2.0.0-rc.3` (GitHub *Latest*,
  2026-07-06; tag on `2ad18f2`; rc.2 demoted; self-promoted; 29 assets); CLOSED + archived to
  `_completed/`.** The rc.2 live-fleet smoke (node hkg14) found `transport:
  tcp` failing at RUNTIME after the rc.2 install fix: mimic exit-22 (`is the Mimic kernel module
  loaded?`) → `dkms status: mimic/0.7.1 **added**` (never built) → `linux-headers-6.1.0-13-cloud-amd64`
  **pruned from the repo** (node on a stale kernel since Dec-2024, never rebooted). ROOT DEFECT:
  `_mimic_provision` (`script.go:593`) declares success on `command -v mimic` (BINARY only), never
  verifying the DKMS **module** built/loads — so it proceeds to a broken start; and on a
  `mimic_fallback: udp` link the false-success **skips the UDP fallback** (silent no-degrade).
  Secondary: the rc.2 `restart` change can orphan `/run/mimic/*.lock` → exit-17 wedge. **5 plans:**
  (1) module build/load verification + honor-policy (`module_unavailable` outcome/condition) + lock
  cleanup + `modprobe` on start [FLEET-CRITICAL] · (2) per-node egress-interface override (owner ask)
  · (3) pre-deploy "can this node run mimic" capability probe + panel warning · (4) docs + proof · (5)
  release rc.3. Posture: detect + honor policy + clear "reboot into the current kernel" guidance —
  install.sh NEVER auto-swaps a kernel. Plan folder:
  [`implementation_plans/mimic-runtime-reliability-2026_07_06/`](implementation_plans/mimic-runtime-reliability-2026_07_06/outline.md).
  plans 1 (#235, module gate + honor-policy + lock cleanup) + 3 (#236, native-XDP always-visible +
  the mimic-capability probe; review caught + fixed a mimic-warning over-fire) + 2 (#237, per-node
  egress override + a signing guard for the owner's "sign the new surface" ask) + 4 (#238, docs) ALL
  MERGED, + plan-5 (#239, CHANGELOG) + the `v2.0.0-rc.3` tag CUT (owner: "cut now"). Release verified:
  29 assets, GitHub Latest (rc.2 demoted; self-promoted). **Owed: owner updates the controller to rc.3
  + re-deploys the affected fleet nodes** — the fix is in the rendered `install.sh`, delivered by the
  controller, NOT the agent binary, so cutting rc.3 makes it available but the operator applies it by
  updating the controller + redeploying. A defect during soak → rc.4 under the same gate rules. Owner
  unblock still valid (udp fallback / reboot into the current kernel).

- **🎯 `v2.0.0-rc.1` RELEASED — GitHub *Latest* (2026-07-03; tag on `f4c4389`; beta.18 demoted;
  self-promoted via the `make_latest` belt exactly as gated).** The rc promotes the soaked
  beta.18 line with ZERO code changes since the last beta. Gate
  ([`docs/spec/rc1/RC1-GATE.md`](docs/spec/rc1/RC1-GATE.md)) closed **GO with zero accepted-risk
  exceptions** on the owner's 2026-07-03 clean live-fleet smokes (beta.17 hardening set +
  beta.18 single-linked accelerator edge): criteria A–E all satisfied, the owed-smoke residue
  discharged by sustained live-fleet operation (beta.9→18), release verified (29 assets, sidecar
  hash, published `version` = `v2.0.0-rc.1`), and **branch protection is now LIVE on `main`**
  (all six CI jobs required — by their check-run DISPLAY names; the gate doc's old short job-ID
  contexts would never have been satisfied and were corrected at set time; force-pushes +
  deletions disallowed). Gate PR #226 (reviewed, 4 residuals fixed), closeout this PR.
  **Both driving subjects are CLOSED + archived to `_completed/`:**
  - `pre-rc1-hardening-2026_07_02` — 9 hardening plans (beta.17, PRs #208–#218) + plan-11 (the
    rc.1 cut, #226).
  - `link-directionality-2026_07_03` — per-edge `link_direction` killing the reverse-peer
    roaming race (beta.18, PRs #220–#225; D11 one-spelling design; kernel-proven via realtunnel
    `c4`; owner-smoked clean).
  **🎯 `v2.0.0-rc.2` RELEASED — GitHub *Latest* (2026-07-04; tag on `cb2ecdd`; rc.1 demoted;
  self-promoted). Verified: 29 assets, sidecar hash + published `version` = `v2.0.0-rc.2`.**

- **✅ SUBJECT `mimic-provisioning-reliability-2026_07_04` — DELIVERED as `v2.0.0-rc.2`; CLOSED +
  archived to `_completed/`. Owner-requested security review CLEAN (3 adversarial lenses —
  install-integrity / trust-anchor / silent-escape — zero findings).** Owner live-fleet smoke of
  rc.1 found `transport: tcp` (mimic) deploys **hard-failing on Debian-12 nodes** (`install.sh exit:
  exit status 100`, taking tunnels down → `wireguard: NoInterfaces`). ROOT CAUSE: upstream
  `hack3ric/mimic` ships **two** debs per `<codename>-<arch>` — `mimic` (userspace) + `mimic-dkms`
  (the DKMS eBPF module, which **Provides** the `mimic-modules` the `mimic` pkg **Depends** on). YAOG's
  one-pin-per-`<codename>-<arch>` catalog (`MimicDebs map[string]model.Artifact`) can pin only `mimic`,
  so `apt install ./mimic.deb` can't resolve the dep → exit 100; and the unguarded `apt-get` under
  `set -euo pipefail` (`script.go:562`/`:1524` + TS mirror) aborts the whole apply **before** the
  fallback-to-UDP logic, so even a `mimic_fallback: udp` link bricks (and no mimic breadcrumb is
  written). **6 plans:** (1) two-package catalog model (`MimicDebPin{asset,sha256,dkms_asset,
  dkms_sha256}`, NOT extending the shared `model.Artifact`) + install BOTH debs + robust
  policy-aware fallback + a NEW conformance fixture (no golden emits an `artifacts.json` today) · (2)
  panel two-package UX (Discover pairs the `-dkms` asset to its sibling label) + Assist both-sidecars +
  empty-SHA-is-a-miss · (3) native-XDP deploy-time auto-downgrade→skb + achieved-mode Node Condition ·
  (4) native-XDP pre-deploy capability probe (agent heuristic) + panel warning · (5) docs + proof · (6)
  release rc.2. **Owner decisions:** comprehensive/rc.2; the Phase-0 teardown is EXPECTED (failover by
  design — NOT in scope, no follow-up subject); include the pre-deploy native probe (D-native). Plan
  folder: [`implementation_plans/mimic-provisioning-reliability-2026_07_04/`](implementation_plans/mimic-provisioning-reliability-2026_07_04/outline.md).
  **All 6 plans MERGED (#228–#233) + `v2.0.0-rc.2` CUT (owner: "cut now").** plan-1 two-package model
  + install BOTH debs + robust policy-aware fallback; plan-2 panel two-package UX + Assist; plan-3
  native auto-downgrade (review caught a redeploy+reboot de-cloak → fixed via `restart` + a client-tcp
  golden); plan-4 pre-deploy native probe; plan-5 docs; plan-6 CHANGELOG + tag. **Security review
  CLEAN** — 3 adversarial lenses, zero findings: no unverified `.deb` installs (every download
  SHA-256-verified against the signed `artifacts.json`); a UDP de-cloak requires the operator's
  explicit `mimic_fallback=udp` (shipped default fail-closed) and surfaces as a `warn` condition; the
  keystone / bundle-signing / off-host passkey trust anchor is untouched (mimic pins + install.sh ride
  inside the signed bundle, verified before install); the new native-XDP telemetry is
  observability-only. **Owed: an owner fleet-smoke of the mimic fix** (deploy a `transport: tcp` link
  with the two-package catalog; confirm both debs install / clean UDP fallback on a stale-kernel node
  / achieved XDP mode in the panel). A defect during soak → rc.3 under the same gate rules.

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

### Prior release history

- **Released:** **`v2.0.0-beta.8`** (GitHub *latest*) — pre-rc.1 blocker hotfix (PR #136). Fast-tracked six
  investigation-confirmed blockers: fleet-mux panic recovery (B1), keystone-sign-on-refresh 401 (F1),
  babeld.conf byte-stability under edge reorder (C1), and enrollment-lifecycle hardening (S4 revoked-
  resurrection guard, S5 enrollment-token purge-on-revoke, S6 TTL cap). Independent review GO (0 findings)
  → CI + Release + Docker green.
- **Drafted (awaiting execution + owner sign-off on 3 pending decisions):** the **pre-rc.1 program** —
  `implementation_plans/_completed/pre-rc1-2026_06_18/` (outline + 22 plan files across 4 subjects: refactor+security →
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
- `main` contains the reviewed rc.7 implementation and annotated-tag validator repair; exact-commit
  main CI runs `29434577226` (`01ab037`) and `29436552114` (`c3c5c25`) passed all seven required jobs.
  Its local gate mirror is also green across Go
  format/vet/test/race/coverage, frontend lint/build/Vitest/Playwright, WASM conformance, govulncheck,
  DAST, release-asset synthesis, workflow lint, cross-builds, and the real-tunnel canary. The first
  first tag-triggered Release run (`29434982352`) failed pre-boundary because `actions/checkout` rewrote
  the local annotated tag ref to the peeled commit. After PR #300 fixed that and the tag was lawfully
  recreated, run `29437046676` passed its validator, seven gates, seven platform builds, exact-22 seal,
  and native Windows amd64/386 execution. It pushed exact GHCR digest `sha256:7e71e286…`, then its new
  runtime verifier failed by reusing one parent index digest for sequential amd64/arm64 Docker runs.
  Both exact child digests verify successfully; GitHub has no rc.7 release object and both Latest
  pointers remain rc.6. Because the image boundary was crossed, neither tag nor image will move. The
  child-digest fix and a fail-closed recovery transaction over the original artifacts are active.
  Hardware-backed browser enrollment remains an explicit owner smoke.

## Next actions

**Two current tracks:**

**Track 1 — `post-refactor-debt-paydown-2026_07_14` COMPLETE + archived to `_completed/`.** All 14 plans
merged to `main` (#277–#290, +#291/#292 review follow-ups), each workflow-reviewed → fixed → re-reviewed →
CI-green; shipped as `v2.0.0-rc.6`. **plan-6 (WebAuthn UV, #282) merged 2026-07-15; the merged rc.7
implementation supersedes its blanket runtime/node gate before rc.7 is published.** The replacement
verifies a purpose+actor-scoped UV proof from the exact candidate only when a login or keystone browser
credential is enrolled, with an explicit possession/copy warning in both panel surfaces. There is no
node-side UV migration and no mandatory trust-list re-sign. Full detail is in the ✅ closed entry above.
**Residual (non-blocking, its own future unit):** the go:embed/`ShellToken` PowerShell deploy templating
(plan-3.5). The bilingual wiki and controller-agent lifecycle prose are reconciled in the merged rc.7
implementation.

**Track 2 — the rc line to GA.**

**`v2.0.0-rc.6` is GitHub Latest (2026-07-14; annotated tag on `91fcb71`; rc.5 demoted; self-promoted;
22 assets — the 7 `yaog-server-airgap-*` binaries are intentionally gone, one server build post
framework-refactor). It ships the `post-refactor-debt-paydown` delta (PRs #277–#292). The road to GA
(current rc.7 release recovery is active; hardware-only checks remain owner-paced):**
1. **Complete the immutable `v2.0.0-rc.7` recovery transaction.** Tag `v2.0.0-rc.7` is fixed at
   `c3c5c25`; versioned GHCR digest `sha256:7e71e286…` is fixed and must not be overwritten. The source
   run's validator, seven gates, exact 22 assets, and native Windows checks are green, but its
   parent-index runtime verifier prevented the draft/finalizer jobs from running. Merge the child-digest
   verifier and reviewed `Recover Release` workflow, wait for exact-main CI, then dispatch it with the
   immutable tag, source run, revision, and image digest. It must adopt—not rebuild—the version image;
   re-download and re-seal the original artifact IDs; create one private draft; then converge
   GHCR/GitHub Latest and verify both. Only post-publication verification may change this file to shipped.
2. **Carry the rc.6 real-host/browser smokes as explicitly owed risk where hardware is unavailable.**
   Owner owes: update the controller to rc.6+ and browser-smoke
   the fixes — (a) **local in-browser design now actually loads** (the shipped panel finally contains
   `yaog.wasm`: design → Validate → compile → export with no backend — the headline fix); (b) deploy a
   `transport: tcp` node then run the deploy-script `--uninstall` → the `mimic@` unit is stopped/disabled
   and the overlay SNAT rules are gone (incl. a non-default transit CIDR); (c) `install.sh` still installs
   a legit bundle cleanly (the new signed-set guard only rejects a *tampered* unlisted `artifacts.json`);
   (d) self-update across the rc.5→rc.6 boundary reconciles (no channel wedge). The install.sh +
   deploy-script fixes ride the rendered scripts, so **update the controller and redeploy** to apply.
   Release cut hit a real `release.yml` E2E-gate gap (it wasn't building the wasm before the E2E panel
   build — fixed #295, tag moved to the fixed commit); the published rc.6 is correct. Any confirmed
   defect before the rc.7 cut must be fixed first; a defect found after publication advances to rc.8 (a
   red required gate or a new blocker never tags over).
3. **rc.5 surfaces to also smoke** (carried, not yet owner-confirmed): the node-detail CPU/RAM/load
   charts (granularities + retention cap incl. `0`=off) and delta deploy (unchanged topology →
   "0 updated, N unchanged", no node refresh; change one → only it re-stages; Force redeploys an
   unchanged node). **STILL owed from rc.4:** set L7-relay edges to `transport: udp`. The single
   controller-update + redeploy covers rc.4/rc.5/rc.6 at once.
4. **rc.x backlog (deliberate deferrals, unchanged):** FileStore host-loss SPOF (backup/restore/HA — see
   the persisted encrypted-object-storage plan), bootstrap-TOFU first-fetch pinning + operator-cred OOB
   delivery, the pinned-endpoint anti-roaming re-assert option (owner decision open), the `EDGE_OMITEMPTY`
   `mimic_fallback` canonicalization gap, the auto-coordinated mimic fallback (deferred), and the Dockerfile-vs-go.mod toolchain
   alignment note.
5. **GA when the rc line has soaked clean** — per `RELEASING.md`'s ramp.

Operational note (unchanged): a CI job display-name change silently orphans its required
branch-protection context — update protection in the same PR as any `name:` edit in `ci.yml`.

Historical (rc.1 shipped 2026-07-03, GitHub Latest at the time): the pre-rc.1 program (Subjects 1–4,
PRs #137–#159), the rc.1 go/no-go gate ([`docs/spec/rc1/RC1-GATE.md`](docs/spec/rc1/RC1-GATE.md), closed
GO with zero exceptions), and the `link-directionality` NAT/roaming fix (single-link the accelerator
edge so the reverse peer can't race the relay; folded into rc.1) are all delivered + archived. The
release line has since advanced preview → beta → rc through **rc.6**.

## Recently closed subjects (most recent first)

- `post-refactor-debt-paydown-2026_07_14` (2026-07-14 → complete 2026-07-15) — **14 plans, shipped as
  `v2.0.0-rc.6` (PRs #277–#292).** The residual + security debt after framework-refactor, from a 30-agent
  sweep + 7-agent security gap-pass → 18-agent pre-execution review (caught 3 blockers) → execution →
  10-agent review-at-last (2 findings fixed: #291 stray wasm, #292 client-mimic). Tier-1: the standalone
  install.sh signed-set bypass, the **WASM ship-breaker** (never built in release/Docker → shipped panels
  404), deploy.go mimic-teardown + CIDR-agnostic SNAT, the self-update semver wedge, the trust-list-sign
  custody race. Tier-2/3/4: agent `ControllerLoop`, byte-identical `derivePeersWithDomains` split,
  `handler_bootstrap` split + agent-mux adapter, the non-vacuous wire-DTO drift gate, finished `Field`
  adoption, doc/state hygiene. **plan-6 (WebAuthn UV, #282) merged 2026-07-15; its blanket runtime/node
  gate is superseded in the merged, still-unpublished rc.7 implementation by server-verified,
  enrollment-scoped UV proof for both browser credential types (no fleet re-sign).** NO trust-root bypass
  / key leak / CVE. Memory:
  `post-refactor-debt-paydown-shipped.md`, `release-e2e-gate-mirrors-ci.md`.
- `framework-refactor-2026_07_13` (2026-07-13) — **14 phases (plans 0/1/1.5/1.6/2/3/4/5/5b/6/7/8/9/10;
  PRs #260–#275); the "WASM-Unified Core + Machine-Gated Paydown" program.** WASM is now the DEFAULT
  in-browser local engine (multi-browser Playwright e2e: chromium + webkit + firefox); the ~10.6K-LOC
  hand-mirrored TS compiler + `internal/conformance` + `@noble`/`jszip` DELETED (−12.3K LOC); the Store
  keystone collapsed onto ONE core over a KV port (−941 LOC); the airgap anonymous-compute surface
  DELETED (one server build, no `-tags airgap`); convention → machine-gates (arch-ratchet / auth-adapter
  / `ShellToken` / Wire-DTO+omitempty drift gate — 7 required checks). Each PR workflow-reviewed → fixed →
  re-reviewed → green → merged. Memory: `framework-refactor-shipped.md`. Owed: owner real-host custody
  smoke of the plan-8 Store core (keystone-rotation + restart + passkey) before live-trust — a release
  gate, not a merge gate.
- `telemetry-history-and-delta-deploy-2026_07_13` (2026-07-13) — **8 plans, `v2.0.0-rc.5` (GitHub
  Latest, PRs #249–#257; annotated tag on `ac3d660`; rc.4 demoted; 29 assets).** (A) per-node resource
  history + node-detail CPU/RAM/load charts (`cpu_pct` /proc/stat delta; bounded never-fsync in-mem →
  JSONL store; configurable cap; server-side aggregated `node-history` query; reusable `TimeSeriesChart`)
  + (B) delta deploy (per-node `sha256(checksums.sha256)` skip → kept-generation/mixed-gen fleet;
  fail-open; keystone-aware disable on first-pin/rotation; zero-changed PURGE; Force + best-effort canvas
  preview) + (plan-1.5) the telemetry-freshness FRAMEWORK fix (metrics = sole heartbeat producer +
  post-apply kick; conditions stay dual-write). Each PR workflow-reviewed → fixed → re-reviewed → green;
  review caught 3 real defects pre-merge (plan-5 zero-changed custody blocker, plan-6 preview-vs-canvas +
  hard-block, plan-7 doc "single producer" overstatement). Owed: owner controller-update + browser smoke.
- `mimic-fleet-robustness-2026_07_07` (2026-07-07) — **5 plans, `v2.0.0-rc.4` (GitHub Latest at the time,
  PRs #241–#245; tag on `cbe0735`).** Fixed the rc.3-soak mimic fleet findings: build deps
  (`bubblewrap`+`dwarves`, which mimic-dkms's DKMS build needs but declares neither) in the provisioning
  step AND the module-build retry; unconditional Phase-0 teardown so `tcp→udp` de-provisions the stale
  `mimic@`; a live `mimic` condition (re-probes `systemctl is-active` each heartbeat); the
  `validation_edge_mimic_relay_path` warning (mimic needs a direct L3/L4 path, no L7 relay). Owed: owner
  controller-update + fleet redeploy + set L7-relay edges to `transport: udp`.
- `mimic-runtime-reliability-2026_07_06` (2026-07-06) — **5 plans, `v2.0.0-rc.3` (GitHub Latest, PRs
  #235–#239; tag on `2ad18f2`).** Fixed the rc.2-soak mimic RUNTIME defect (a stale-kernel node looped
  on mimic exit-22 because the DKMS module was never built): module build/load VERIFICATION (not just
  `command -v mimic`) + honor-policy, orphaned-lock cleanup, `ModuleUnavailable` condition, a pre-deploy
  mimic-capability probe, always-visible native-XDP (owner-flagged), a per-node egress-interface
  override (rides the signed install.sh). Each PR reviewed → verified → fixed → green; reviews caught
  the mimic-warning over-fire + the missing `NODE_OMITEMPTY` entry. Owner: "cut now"; owed: owner
  updates the controller to rc.3 + re-deploys.
- `mimic-provisioning-reliability-2026_07_04` (2026-07-04) — **6 plans, `v2.0.0-rc.2` (GitHub Latest,
  PRs #228–#233; tag on `cb2ecdd`).** Fixed the rc.1-soak mimic install defect: two-package
  `mimic`+`mimic-dkms` install + robust policy-aware fallback, panel two-package UX + Assist
  reliability, native-XDP deploy-time auto-downgrade + a pre-deploy capability probe, docs. Each PR
  independently reviewed → adversarially verified → fixed → CI-green; the review caught a real
  redeploy+reboot de-cloak (fixed via the `restart` lifecycle + a client-tcp golden). Owner-requested
  security review CLEAN. Owed: owner fleet-smoke of the fix.
- `link-directionality-2026_07_03` (2026-07-03) — **4 plans, `v2.0.0-beta.18` (PRs #220–#225);
  per-edge `link_direction` (D11 one-spelling; editor flip) killing the reverse-peer roaming race;
  kernel-proven (realtunnel `c4`); owner-smoked clean; folded into `v2.0.0-rc.1`.**
- `pre-rc1-hardening-2026_07_02` (2026-07-02/03) — **11 plans, `v2.0.0-beta.17` (PRs #208–#218) +
  the `v2.0.0-rc.1` cut (#226); the CRITICAL self-update keystone bypass + the audited security
  scopes + the rc.1 gate closed GO with zero exceptions; branch protection set.**
- `beta16-smoke-hardening-2026_06_27` (2026-06-27) — **3 fixes, `v2.0.0-beta.16` (PRs #204–#206).** A
  smoke-hardening of beta.15: a node could report stale status (sticky `selfupdate: Blocked` + frozen
  `Last Seen`) though it had successfully self-updated. fix-A (panel): the node-detail page refreshes
  (was a frozen cache snapshot; #204). fix-B (agent): clear sticky `selfupdate: Blocked` on
  `FinalizeSelfUpdate` + bound the `wg show` timeout + a top-level heartbeat `recover()` (#205); CHANGELOG
  #206. Each PR reviewed → fixed → re-reviewed → green.
- `theme-and-mimic-fixes-2026_06_25` (2026-06-25) — **3 plans, `v2.0.0-beta.14` (GitHub Latest at the
  time; PRs #193–#195).** Two owner-reported live-fleet defects fixed at root: the beta.13 theme
  stragglers (node-condition chips illegible in light mode → tokens; canvas grid + edge labels →
  neutral-map + `ROLE_HUE` dedup; Deploy button grey in dark → a new `--cta` token family) and the mimic
  "using local" bug (the `local=` filter pinned to `ip route get 1.1.1.1`'s src diverged from WG's real
  on-the-wire source → an additive route-independent `remote=` filter + a loopback-egress guard + a Go
  test ladder). Each PR 4-lens + security reviewed → fixed → re-reviewed → green. Owed: owner real-host
  mimic smoke.
- `beta9-smoke-hardening-2026_06_23` (2026-06-23) — **5 plans, `v2.0.0-beta.10` → GitHub Latest (PRs
  #176–#181).** Live-fleet smoke fixes: a dedicated `/telemetry` heartbeat + `Sampler` framework that
  makes Node Conditions honest (no more frozen apply-time snapshot); controller-mode Validate →
  in-browser (kills the `/api/validate` 404); off-host signing-handle auto-recovery (serve the
  non-secret descriptor → no fleet-stranding re-pin); mimic catalog discover-and-pick (SSRF-guarded
  `/release-assets`). Each PR 4-lens-reviewed → fixed → merged; review workflows made checkout-free
  after a shared-tree clobber; isolated git worktrees per branch.
- `agent-feedback-and-version-aware-rollout-2026_06_22` (2026-06-23) — **10 plans, `v2.0.0-beta.9` (PRs
  #162–#175).** The reusable structured agent→panel Node Conditions channel, mimic→UDP per-link fallback,
  and version-aware rollout (panel knows/displays its own version; "Update all" → panel version; refuse a
  target newer than the panel) + default release URLs / working "Assist from release". Each PR
  4-lens-reviewed → fixed → re-reviewed → green; #173 fixed a real release.yml gate bug (gate-e2e now
  mirrors ci.yml's required job).
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
