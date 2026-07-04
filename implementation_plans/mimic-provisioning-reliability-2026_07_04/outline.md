# mimic-provisioning-reliability — outline

> **Subject:** `mimic-provisioning-reliability-2026_07_04`
> **Ships as:** `v2.0.0-rc.2` (the first rc.2 subject; rc self-promotes to Latest — `make_latest`
> is true for non-`-beta.`/`-preview.` tags).
> **Trigger:** live-fleet smoke on `v2.0.0-rc.1` — enabling `transport: tcp` (mimic) on a link
> between two Debian-12 nodes hard-failed the deploy (`install.sh exit: exit status 100`), taking
> both nodes' tunnels down (`wireguard: NoInterfaces`).

## Mission

Make mimic (`transport: tcp`) provisioning **actually install** on the distros YAOG targets, and make
every failure mode **degrade instead of brick**. Today the mimic GitHub-`.deb` fallback fetches only
half of what upstream ships and aborts the whole node apply when the install fails; the fleet-wide
"Fall back to UDP" safety net cannot catch the abort; and `xdp_mode: native` is an unguarded second
failure surface with no way to know a NIC supports it.

## Root cause (owner-diagnosed + verified this session)

Upstream `hack3ric/mimic` 0.7.1 ships **two** Debian packages per `<codename>-<arch>`:
`<codename>_mimic_<ver>_<arch>.deb` (userspace) **and** `<codename>_mimic-dkms_<ver>_<arch>.deb`
(the DKMS eBPF kernel-module source, which **`Provides: mimic-modules`**). The `mimic` package
declares `Depends: mimic-modules (= <ver>)`.

YAOG's catalog models one pin per `<codename>-<arch>` key (`MimicDebs map[string]model.Artifact`),
so it can pin only the `mimic` package — the `-dkms` companion has **no slot** (the Discover UI even
warns "a duplicate would silently drop a pin", and `deriveKey` returns `''` for a dkms asset). The
install then runs `apt-get install ./mimic.deb`, cannot satisfy `Depends: mimic-modules`, and exits
100. Under `set -euo pipefail` that unguarded `apt-get` (`script.go:562` / `:1524`, and the TS mirror)
aborts the whole `install.sh` **before** the fallback-to-UDP logic a few lines later — so even a
`mimic_fallback: udp` link bricks, and no mimic breadcrumb is written (panel shows only the generic
`DegradedKeepingLastGood`).

Environmental amplifier (not a YAOG bug, but the reason the fallback must work): the DKMS build needs
`linux-headers-$(uname -r)`; a node on a stale point-release kernel (e.g. `6.1.0-13-cloud-amd64`)
whose exact headers have rolled out of the repo cannot build the module until it reboots into the
current kernel. So on some real nodes mimic legitimately **cannot** install — the correct outcome is a
clean UDP fallback (policy=udp) or a clean, mimic-specific error (policy=none), never a stuck node.

## Success criteria

1. A `<codename>-<arch>` catalog row pins **both** the `mimic` and `mimic-dkms` `.deb`s; the install
   fetches+verifies+installs both together (`apt-get install ./mimic.deb ./mimic-dkms.deb`) so the
   dependency resolves.
2. Under `mimic_fallback: udp`, **every** failure in the provisioning block (no apt/dpkg, no
   artifacts.json, no jq, no pin, download/checksum failure, apt-install failure, DKMS-build failure)
   degrades to `_MIMIC_SKIP=1` + a breadcrumb + a plain-UDP link — never a `set -e` abort. Under
   `none` (fail-closed) it writes the breadcrumb **then** exits (so the panel shows *why*).
3. `xdp_mode: native` never bricks: a failed native attach auto-downgrades to `skb`, and the
   **achieved** mode surfaces as a Node Condition. The agent additionally reports a **pre-deploy**
   native-XDP capability signal so the panel can warn before native is selected.
4. Discover pairs a `-dkms` asset to its `<codename>-<arch>` sibling row (one label, two assets);
   Assist fetches both sidecars and treats an empty/garbage SHA as a miss (never saves blank).
5. Go↔TS byte-exact conformance holds (both compilers/renderers change in the same PR; goldens +
   drift + i18n gates green), with a **new** fixture that actually emits an `artifacts.json` and
   exercises the `_MIMIC_SKIP` (policy=udp) branch.
6. Ships as `v2.0.0-rc.2`, verified (assets + sidecar + `version` stamp), self-promoted to Latest.

## Principles (invariants; risk class)

- **P1 — Shared pin type is off-limits (HIGH).** `model.Artifact` (`internal/model/artifact.go:11`)
  backs the mimic map, the agent-bins map, AND the release-pin Assist response. Do NOT add a dkms
  field to it — change only the **mimic map's value type**. Same trap in the frontend: `AgentPin`
  (`controllerClient.ts:753`) is shared by `agentBins` + `mimicDebs`; the companion needs a distinct
  mimic-deb type. Violation blast: agent self-update + Assist inherit a phantom field.
- **P2 — Go↔TS byte-exact (HIGH).** Every renderer/validator/model change lands in both
  `internal/…` and `frontend/src/…` in the SAME PR; the drift manifest + golden corpora + i18n
  sync gates go red on a split. Install-script changes require regenerating BOTH corpora + the drift
  manifest.
- **P3 — Fail-degradable, never brick (HIGH).** A mimic provisioning failure under policy=udp must
  leave the node applying (plain UDP); under policy=none it must fail with a mimic-specific
  breadcrumb, not a bare `apt` exit. No `set -e` abort inside the provisioning block.
- **P4 — Backward compatibility (MEDIUM).** Old catalogs (`{asset, sha256}` only) must still parse
  and behave (mimic-only → degrades under policy=udp). Flat-additive value type + a schema bump that
  is tolerant downward (old-schema files still load on the new binary).
- **P5 — Custody / trust unchanged (HIGH).** Trust remains the controller-signed `artifacts.json` +
  install-time `sha256sum -c`; Assist/Discover stay best-effort convenience that never auto-anchors
  trust. Both `.deb`s are SHA-pinned in the signed `artifacts.json`; neither is dpkg'd unverified.
- **P6 — No FE port/asset authority beyond the catalog (MEDIUM).** The panel writes catalog pins
  and `link_direction`/`xdp_mode`-class policy only; the backend remains the sole authority for
  ports/allocation.
- **P7 — Process:** no shims/monkey-patches; structure-aware code matching the file's idiom;
  per-PR independent workflow review → fix → re-review → CI green → merge; reviews checkout-free in
  isolated git worktrees (`git show <ref>:<path>`); no `--no-verify`/amends/force-push; branch off
  `main` (protected — six required checks by display name).

## Decisions log (locked)

- **D-vehicle (owner):** comprehensive fix, target **rc.2** (not a hotfix). The rc.1 soak surfaced
  this defect → rc.2 is its natural home. Fleet stays on the UDP/plain workaround meanwhile.
- **D-teardown (owner):** the Phase-0 teardown-then-rebuild is **expected** per-generation apply
  behavior; the owner relies on overlay failover (backup links / Babel) when a node drops mid-apply.
  So the destructive-on-failure behavior is **NOT in scope** and gets no follow-up subject. The
  mimic fix removes the trigger that leaves a node *stuck*.
- **D-native (owner):** include a **pre-deploy** native-XDP capability probe (plan-4) in addition to
  deploy-time auto-downgrade + achieved-mode reporting (plan-3).
- **D1 — value shape:** flat-additive `MimicDebPin{Asset, SHA256, DKMSAsset, DKMSSHA256}` (json
  `asset,sha256,dkms_asset,dkms_sha256`) as the **mimic map's** value type; `asset`/`sha256` keep
  today's meaning (the `mimic` pkg) so old catalogs bind unchanged; `dkms_*` are the companion.
  NOT nested (nested unbinds legacy `asset`/`sha256`). Do not touch `model.Artifact`.
- **D2 — schema bump 1→2** (`artifactsFileSchema`, `artifacts_json.go:10`): honest shape-version
  signal so an OLD air-gap binary reading a NEW catalog fails-closed at the `>` guard
  (`fetchsettings_env.go:53-55`) instead of silently installing without the companion; a schema-1
  catalog still loads on the schema-2 binary (`1 > 2` is false). install.sh reads via jq and is
  schema-agnostic (no skew inside a bundle — install.sh + artifacts.json ship together).
- **D3 — install both debs** in one `apt-get install -y ./mimic.deb ./mimic-dkms.deb` so apt
  resolves `mimic → mimic-modules` from the local companion (which Provides it).
- **D4 — robust block = a shell function returning non-zero** on any failure, called under a
  policy-aware `if ! _mimic_provision …; then <udp: skip+breadcrumb+continue | none: breadcrumb+exit>`.
  Replaces the block's five bare `exit 1`s + the two unguarded `apt-get install`s.
- **D5 — native auto-downgrade:** when `xdp_mode=native` and the `mimic@<egress>` attach fails,
  rewrite the config to `skb` and retry once before declaring failure; record the achieved mode via a
  new breadcrumb outcome (`native_downgraded_skb`), classified into the mimic Node Condition.
- **D6 — Assist slot discriminator:** the release-pin request/response gains a per-asset `slot`
  (`mimic`|`dkms`) so a row's two sidecars don't collide on the `<codename>-<arch>` key. Empty/garbage
  SHA → treated as a miss (row note), never saved.
- **D7 — pre-deploy probe = driver+kernel heuristic (primary), real attach (escalation).** plan-4's
  agent reports native-XDP capability as `supported|unsupported|conditional|unknown` from
  `ethtool -i`/driver + kernel version (zero new deps, no live-NIC touch); virtio_net → `conditional`.
  The DEFINITIVE answer stays plan-3's deploy-time achieved-mode condition. A real brief drv-mode
  attach probe is a **plan-4.5** escalation if the heuristic proves too coarse (weigh minimal-deps +
  live-NIC-safety with the owner first).
- **D8 — new conformance fixture** (`26-mimic-catalog-dkms-fallback` or similar): a topology with a
  configured two-package `MimicDebs` catalog + a `mimic_fallback: udp` tcp link, so a golden emits an
  `artifacts.json` (proving the two-deb `.mimic.debs[k]` shape) AND renders the `_MIMIC_SKIP` branch.
  The existing `10-mimic-tcp` covers only the block text + the fail-closed branch.

## Must-read references (file:line)

- `internal/model/artifact.go:11-52` — `Artifact` (shared!), `InstallFetch`, `MimicOutcome*` +
  `MimicBreadcrumbPath` (the closed breadcrumb enum — extending it is a coordinated script+agent
  change).
- `internal/renderer/script.go:80-143` (config struct + `resolveMimicFallbackUDP`),
  `:441-581` (node deps/mimic install block), `:795-875` (node Phase-3 mimic provisioning + native
  attach), `:1038-1060` (`resolveMimicXDPMode`), `:1489-1542` (client install block), `:1639-1700`
  (client Phase-3).
- `frontend/src/compiler/renderers/script.ts` — TS mirror: node block `:472-549`, client
  `:1473-1540`, helpers `:1436-1522`, builders `:1587/:1631`, render exports `:1682-1712`.
  **Use `grep -a`** (long lines trip grep's binary heuristic).
- `internal/render/artifacts_json.go:10,21-34,65-92` — schema const, `artifactsMimic.Debs`, builder.
- `internal/render/render.go:236-270` (`FetchSettings`, `MimicDebs`),
  `internal/render/fetchsettings_env.go:40-69` (loader + schema guard).
- `internal/controller/store.go:385-452` (`ControllerSettings.MimicDebs` + `Clone`),
  `internal/controller/compile.go:152-164` (`BuildFetchSettings`),
  `internal/controller/settings.go:52-71` (`WithDefaults` / assist pre-fill).
- `internal/api/handler_bootstrap.go:32-303` (settings wire + `validateMimicCatalog`),
  `internal/api/release_pins.go:193-419` (Assist), `internal/api/release_assets.go:123-253`
  (Discover), `internal/api/routes_controller.go:307-317`.
- `frontend/src/components/deploy/MimicCatalogSettings.tsx` (whole),
  `frontend/src/lib/mimicDiscover.ts` (whole — `deriveKey`/`collidingKeys`),
  `frontend/src/api/controllerClient.ts:753-880` (shared `AgentPin` + mimic mapping).
- Conformance: `internal/localcompile/testdata/contract/topologies/10-mimic-tcp.json` (+ golden
  `…/golden/mimic-tcp.golden`, `internal/conformance/testdata/golden/mimic-tcp.json`),
  `internal/localcompile/contract_golden_test.go:23-54`, `internal/conformance/drift_test.go:42-45`,
  `internal/conformance/coverage_floor_test.go:36`.
- Agent conditions (native/mimic surfacing): `internal/agent/conditions*.go` (classifyMimic),
  `frontend/src/components/deploy/MimicConditionChip.tsx`, `internal/agent/telemetry.go` (heartbeat).
- Docs: `docs/spec/artifacts/mimic.md`, `docs/wiki.md` + `docs/wiki-zh.md`.

## Milestones

### plan-1 — Two-package catalog + install both debs + robust fallback (CORE)
- **Goal:** each `<codename>-<arch>` pins `{mimic, mimic-dkms}`; install fetches+verifies+installs
  both; every failure degrades per policy (no `set -e` abort). Go + TS mirror + conformance.
- **Hazards:** the shared-type trap (P1); the schema bump must not orphan legacy catalogs (P4); the
  two install blocks (node + client) × two languages (Go + TS) must stay byte-identical; the
  breadcrumb enum is closed (coordinate model+agent if a new outcome is added — install-failed reused
  here, native outcomes deferred to plan-3).
- **Gate:** `go build/vet ./... && go build -tags airgap ./... && go test -race ./... && go test
  -tags airgap ./...`; `cd frontend && npm run lint && npm run build && npx vitest run`; new fixture
  golden + drift regen clean; `git diff` on the two golden dirs shows ONLY the intended new/updated
  bytes (schema=2 + the two-deb `.mimic.debs[k]` + the `_MIMIC_SKIP` branch).
- **Stop-loss:** if the coverage floor or an unrelated golden churns unexpectedly → STOP, diff, and
  reconcile before regenerating blindly.

### plan-2 — Panel catalog UX (two packages) + Assist robustness
- **Goal:** two-asset rows (mimic + dkms) with a distinct mimic-deb wire type; Discover pairs the
  `-dkms` asset to its sibling label; Assist fetches both sidecars via a `slot` discriminator and
  treats empty/garbage SHA as a miss; `validateMimicCatalog` (Go + TS mirror) validates the companion.
- **Hazards:** the shared `AgentPin` FE trap (P1); the Assist key-collision (D6); e2e must locate by
  `data-testid`, never color/text; the existing `collidingKeys` semantics (a paired dkms is NOT a
  collision).
- **Gate:** vitest (mimicDiscover + store), Playwright e2e (add a two-package row, Assist both,
  save round-trip), `npm run build`; Go `validateMimicCatalog` table test + `release_pins` slot test.

### plan-3 — Native-XDP deploy-time auto-downgrade + achieved-mode condition
- **Goal:** native attach failure → rewrite config to skb + retry once; record achieved mode via new
  breadcrumb outcome(s); agent classifies it into the mimic Node Condition; chip surfaces it. Both
  install blocks + TS mirror + goldens.
- **Hazards:** closed breadcrumb enum (model + agent + any test that pins the set); goldens re-emit;
  don't regress the fail-closed native path (policy=none native-fail still errors clearly).
- **Gate:** Go renderer/agent tests; conformance regen; vitest chip test.

### plan-4 — Native-XDP pre-deploy capability probe + panel warning
- **Goal:** the agent probes native-XDP capability (D7 heuristic) and reports it on the heartbeat;
  controller stores it; the panel shows a per-node indicator and warns when `xdp_mode: native` is
  selected on an `unsupported` node.
- **Hazards:** minimal-deps (no cilium/ebpf); no unsafe live-NIC mutation in the primary path;
  telemetry schema is additive (don't break the `/telemetry` contract); virtio → `conditional`, not a
  false negative.
- **Gate:** agent probe unit test (mock ethtool/driver), telemetry round-trip test, panel warning
  vitest/e2e.
- **Insertion point:** plan-4.5 if the heuristic is too coarse and a real drv-mode attach probe is
  wanted (owner decision on deps + NIC-safety).

### plan-5 — Docs + behavioral proof
- **Goal:** `docs/spec/artifacts/mimic.md` (two-package install, DKMS/headers caveat, stale-kernel
  reboot note, native auto-downgrade + capability signal), bilingual wiki (en+zh, lockstep). Proof:
  a focused Go test asserting the two-deb install command shape + each policy-aware failure path's
  breadcrumb; the new golden IS the artifacts.json-shape proof. (Realtunnel DKMS-in-netns is
  impractical — headers/build — so no new realtunnel scenario unless a cheap skb-mode path exists;
  decide in-plan.)
- **Gate:** `go test ./...`, docs render check, wiki en/zh parity.

### plan-6 — Release v2.0.0-rc.2
- **Goal:** CHANGELOG roll (`rc.1..HEAD` delta), full local verify, reviewed CHANGELOG PR → merge →
  green tip → annotated tag `v2.0.0-rc.2` → release.yml green (self-promotes: non-beta/preview) →
  verify (29 assets + `.sha256` sidecars + published `version` stamp) → STATUS + memory closeout →
  `git mv` subject to `_completed/`.
- **Gate:** release + docker workflows green; `gh release list` shows rc.2 as Latest.

## Insertion-point markers

- **plan-1.5** — if regenerating goldens churns an unrelated corpus or the coverage floor moves
  unexpectedly (the two-blocks×two-languages symmetry is subtle): STOP, diff, reconcile, document.
- **plan-2.5** — if the Assist `slot` change ripples into the agent-bins Assist path (shared handler)
  more than expected: isolate the mimic path, don't fork the agent path.
- **plan-4.5** — real drv-mode attach probe (owner decision on deps + live-NIC safety) if the D7
  heuristic is too coarse.

## Closure criteria

- All six success criteria met; every plan's gate green; each PR independently workflow-reviewed →
  fixed → re-reviewed → merged.
- `v2.0.0-rc.2` released as GitHub Latest, verified.
- STATUS.md + memory refreshed; subject `git mv`'d to `_completed/`.
- Owner handed the smoke script (set a fleet accelerator edge to `transport: tcp`, deploy, confirm
  both debs install / clean UDP fallback on a stale-kernel node / achieved XDP mode in the panel).

## Plan status

| Plan | Title | Status | PR |
|------|-------|--------|----|
| 1 | Two-package catalog + install both debs + robust fallback | pending | — |
| 2 | Panel catalog UX (two packages) + Assist robustness | pending | — |
| 3 | Native-XDP deploy-time auto-downgrade + condition | pending | — |
| 4 | Native-XDP pre-deploy capability probe + panel warning | pending | — |
| 5 | Docs + behavioral proof | pending | — |
| 6 | Release v2.0.0-rc.2 | pending | — |
