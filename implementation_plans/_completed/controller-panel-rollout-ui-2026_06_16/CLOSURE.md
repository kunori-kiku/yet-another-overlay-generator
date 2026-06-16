# CLOSURE — controller-panel-rollout-ui-2026_06_16

<!-- closed: 2026-06-16 -->

**Outcome: DELIVERED to `main`.** The operator-panel UI for the signed agent self-update +
canary-then-fleet engine (which shipped headless in `v2.0.0-beta.2`) — i.e. the descoped **plan-9
step 8 "Canary UI"** from `signed-self-update-and-rc-hardening-2026_06_15`, now built. Plus a
symmetric mimic-catalog config form and a per-node update-status surface. Targets **v2.0.0-beta.3**
(the annotated tag push is owner-gated — see below).

## Shipped

| Plan | What | PR | Commit |
|---|---|---|---|
| plan-1 | Backend: assisted `release-pins` endpoint (gh-proxy-applied `.sha256` fetch, egress-guarded) + nodeJSON `in_rollout` + 3 apierr codes | #121 | `38b2d3b` + `cc60bc7` |
| plan-2 | Frontend data layer: rollout+mimic TS contract + **full-replace drop-on-save fix** + `fetchPins` | #122 | `7256a6a` |
| plan-3 | `AgentUpdateSettings` card: target/min version, per-arch bins + assist, canary multiselect, fleet-wide confirm, version_applied→persist-base | #123 | `b712537` |
| plan-4 | `MimicCatalogSettings` card: dynamic per-`<codename>-<arch>` rows, best-effort per-row assist | #124 | `986ca0b` |
| plan-5 | Per-node update-status chip (`deriveUpdateState`, pure + SemVer comparator) on registry/detail + opt-in Live poll | #125 | `475d56c` |
| plan-6 | Closure: spec flips, CHANGELOG/STATUS, descope→delivered, archive | (this PR) | — |

Each build PR went through an independent multi-dimension review workflow (correctness, security/
SSRF, code hygiene, principles/custody, i18n bijection, test/UX completeness), adversarial
verification, fix, and re-review until clean — per the standing per-PR review discipline.

## The custody argument (why the assist is safe)

The "Assist from GitHub release" affordance fetches the `.sha256` sidecars server-side (the panel
cannot — no CORS, and the gh-proxy must apply server-side) and pre-fills the per-asset pins for the
operator to **review**. It is **convenience only and never a trust primitive**: the fetched sidecar
rides the same untrusted transport (`github.com` / the gh-proxy) as the binary, so the panel never
auto-saves or auto-trusts a fetched pin. Trust stays exactly where it was — the controller-signed,
keystone-bound `artifacts.json` that the agent verifies a downloaded binary against before exec
(and the install-time `sha256sum -c` for mimic `.deb`s). The egress path is SSRF-guarded
(http(s)-only, redirect/response caps, a dial-time private-IP reject that also defeats DNS-rebind,
covering 6to4/NAT64). The custody chain (PRINCIPLES.md "Signed-artifact self-update custody") is
unchanged by this subject.

## Deferred / owed

- **Owner-owed manual browser smoke (beta.3)**, recorded in `STATUS.md`: the cards render in
  controller mode, assist pre-fills, a bootstrap-field save round-trips the rollout/mimic config,
  fleet-wide gates on the confirm, the chip transitions pending→applying→applied as a canary
  advances, and the Live poll stops on logout. No FE test runner exists (deferred), so this is
  owner-verified.
- **A reliable, *persistent* per-node `failed` state** (plan-5.5 marker, NOT built): the chip's
  `failed` is best-effort because the agent's `lastHealth` `abandoned:` line is transient
  (overwritten by the next routine apply report). A dependable failed state would need a positive
  agent-reported update-outcome field — a deliberate, separately-reviewed agent wire change.
- **`v2.0.0-beta.3` tag** is owner-gated (publishing a GitHub release is outward-facing): push the
  annotated tag → `release.yml` builds + creates the release + attaches assets → `gh release edit
  v2.0.0-beta.3 --notes-file <notes> --latest`.

## Notes on scope decisions

- `wire-contract.md` was deliberately **not** edited: it is the *topology* FE↔BE parity contract
  (`model.Topology`), and the new fields are *controller-API* surfaces (operator settings / nodeJSON
  / the release-pins endpoint). Those were recorded where they belong —
  `specs/controller-operator-api.md` + `specs/panel-deploy-fleet.md` + the `agent-selfupdate.md`
  §Panel scope flip.
- The mimic assist is **per-row** (one fetch per fillable row) rather than one atomic call, so a
  missing sidecar leaves only that row for manual entry — the faithful implementation of the plan's
  best-effort intent (manual entry is the guaranteed default).
- The `version_applied` ⇒ persist-tagged-base contract (surfaced by the plan-1 re-review): an assist
  that pins a tag also persists the tagged release base on save, so the agent (which fetches the
  verbatim saved base with no latest→tag rewrite) does not download an unpinned binary.
