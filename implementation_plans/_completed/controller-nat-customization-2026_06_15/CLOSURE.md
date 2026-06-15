# Closure — controller-nat-customization

**Status: DELIVERED. Closed 2026-06-15. All seven PRs (#98–#104) + a locator follow-up
(#105) merged to `main`, in order, each reviewed all-Opus.**

Made YAOG controller mode **server-authoritative and operator-customizable at the NAT
boundary**: the controller computes the authoritative per-edge allocation (placeholder keys,
transit IPs, link-locals, ports) and the operator can override exactly the NAT-relevant
values per edge — external `endpoint_host:endpoint_port` and the internal listen port /
transit IP — with those choices surviving verbatim and **sticky-reused** through
Compile → adjust → Save → Deploy (no drift, no clobber). The meaningless per-node
`listen_port` is removed outright (sole internal auto-allocation base is the constant 51820),
which also fixes the always-firing co-hosted port-overlap rule that would have blocked every
multi-node-per-VPS deploy. Zero-knowledge custody is preserved throughout (controller paths
use `render.AgentHeld` placeholder keys, never `render.AirGap`). Local/air-gap mode unaffected.

## Shipped (PRs)

| PR | # | Merge | What |
|----|---|-------|------|
| PR1 | [#98](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/98) | `63ad33b` | `deploy()` post-promote re-GET + merge `compiled_port`+six `pinned_*` by edge ID into the canvas (full-hydrate fallback only if the node/edge set diverged); non-clobbering force-Save. |
| PR2 | [#99](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/99) | `e4afe99` | EdgeEditor directional NAT readout (to-node @ `pinned_to_port`, mirroring the compiler) + role-change re-alloc warning. |
| PR3 | [#100](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/100) | `2ffe0e2` | Remove `Node.ListenPort` (backend); base 51820 in `lowestFreePort` + client wg0; delete the listen-port validators **and the co-hosted effective-range overlap rule** (always-fired under a uniform base); keep overflow-only. |
| PR4 | [#101](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/101) | `baca9a7` | Remove `listen_port` from the frontend (types / NodeForm / NodeEditor / AuditView / `NODE_OMITEMPTY` / i18n). |
| PR5 | [#102](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/102) | `6cded0b` | Extract `controller.CompileSubgraph` from `CompileAndStage` (no behavior change). |
| PR6 | [#103](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/103) | `9e41716` | `POST /api/v1/operator/compile-preview` → `CompileSubgraph` (AgentHeld, **no** persist/stage/export/audit side effects) + Compile button + merge-to-canvas; DeployPage shows the preview. |
| PR7 | [#104](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/104) | `899ff54` | Operator-settable `pinned_*_port` AND `pinned_*_transit_ip` per edge (sticky) + relaxed pin floor to `minPinnedPort` (1024) for port-restricted NAT VPSes; client pair-completeness / transit-CIDR validation. |
| follow-up | [#105](https://github.com/kunori-kiku/yet-another-overlay-generator/pull/105) | — | Repoint the effective-range-overflow error off the removed `listen_port` field (`nodes[%d].listen_port` → `nodes[%d]`); regression guards assert the error code. |

## Process

Per-PR: structure-aware implement → `cd frontend && npm run lint && npm run build` + Go
`build`/`vet`/`test ./internal/...` + gofmt (local toolchain + CI). Two all-Opus review
**workflows** at the end (the first review attempts were wiped out — Explore agents defaulted
to Haiku, then transient `529 Overloaded` — and re-run with `model: 'opus'` forced on every
agent + a 3-skeptic 2-of-3 adversarial majority, gated behind a canary-probe backoff loop):

- **Comprehensive review** (`wpxkwp77i`, 49 Opus agents over the integrated `main...pr7` diff):
  0 hard blockers, 5 should-fix + 3 nits confirmed. Each fix was applied on its **origin PR
  branch** and merged up the stack (PR1+PR2→PR6; PR3+PR6→PR7) so every PR stayed
  self-consistent. Fixes: a stale-local-compile zero-knowledge UI leak (clear `compileResult`
  on controller entry), the NAT readout `dialsFromNode` inversion, `saveDesign` mutating the
  canvas before the conflict gate, a drift-prone duplicated pin-field constant (unified), the
  stale `validation.md` spec, and three stale comments.
- **Re-review of the fixes** (`wlpo7gsfk`, per-fix + regression sweep + merge-coherence):
  returned **GO** — all 8 fixes resolved and regression-free, integration coherent.

Merged PR1→PR7 in order (merge commits, the repo convention). PR7 (#104) hit a stacked-merge
conflict on GitHub once PR1–PR6 landed; resolved by merging the new `main` back into the PR7
branch locally (clean via the ort strategy), re-gating, and merging.

## Verification at closure

**Automated (green on merged `main`: `go build`/`vet`/`test ./internal/...`, frontend
`lint`+`build`):**
- Co-hosted regression guard — `internal/validator/field_safety_test.go`
  `TestValidateSemantic_CoHostedNodesValidateClean` (perpetual): two same-hostname nodes
  validate clean under the uniform 51820 base (the deleted overlap rule must stay dead).
- Effective-range overflow asserted by error **code** (locator-independent after #105).
- NAT-range pin accepted — `internal/validator/allocation_pins_test.go` (a `30050` pin passes
  under the relaxed `minPinnedPort` floor).
- Compile-preview custody + no-side-effects — `internal/api/controller_http_test.go`
  `TestControllerHTTP_CompilePreview`: configs carry only `PRIVATEKEY_PLACEHOLDER` (regex-checked),
  stored topology byte-identical, promote→409 (nothing staged).
- i18n en/zh bijection enforced by `tsc` in the frontend build.

## What was parked / owed (not defects)

- **Live NAT round-trip on the real deployment** (the subject's manual closure criterion):
  Compile → edit the internal port + transit IP on a port-restricted NAT VPS → Save → deploy →
  confirm the agent listens on exactly the chosen port/IP and the configured forward survives a
  re-deploy with no drift. Needs a browser + a real NAT VPS; code paths are unit/build-verified,
  the end-to-end pass remains the owner's to run.
- **Out-of-subject nits the re-review explicitly classified pre-existing** (left untouched on
  purpose): a 1-key en/zh catalog count difference (intentional — zh is a permitted subset with
  per-key English fallback, `tsc` passes), and ~13 repo-wide `gofmt`-flagged files that predate
  this subject (the subject's own Go file is gofmt-clean).

## Pointers

- Outline + decisions log: `outline.md` (this folder).
- Spec refresh: `docs/spec/compiler/validation.md` (listen_port validators removed).
- Memory: [[controller-nat-port-ip-customization-plan]] (marked SHIPPED) — builds on
  [[controller-mode-redesign-decisions]] and [[i18n-error-envelope-shipped]].
- Perpetual guard (never retire): `internal/validator/field_safety_test.go`
  `TestValidateSemantic_CoHostedNodesValidateClean`.
