# Subject: theme-and-mimic-fixes (2026_06_25)

## Mission

Fix two bounded, independent defects the owner reported while running the live fleet, and ship
them together as one low-risk fixes beta (split-release decision D8):

1. **Theme stragglers** — three dark/light inconsistencies the beta.13 token sweep missed: node
   Conditions chips illegible in light mode, canvas (edge labels + grid) not theme-aware, and the
   Deploy button rendering grey in dark mode.
2. **Mimic "using local" bug** — `transport: tcp` links silently fall through to plain UDP because
   the mimic eBPF filter is pinned to a single egress IP derived from `ip route get 1.1.1.1`, which
   need not match the source IP WireGuard actually puts on the wire (multi-homing / secondary IPs /
   policy routing) or can literally resolve to `lo`/`127.0.0.1`.

Success = both defects fixed at their structural root with tests, reviewed per-PR (4 lenses +
security), CI green, shipped as a beta promoted to GitHub Latest, and an owner real-host mimic smoke
runbook delivered (the data-plane fix cannot be smoke-tested in this sandbox).

## Principles (invariants — see repo-root `PRINCIPLES.md`)

- **No shims / monkey-patches / ugly workarounds** (`PRINCIPLES.md:67-84`). Fix at the structural
  root. The mimic fix changes how `local=` is derived — that is the root, not a band-aid.
- **No scope compromise to close.** Both defects fully fixed (all reported symptoms + their
  siblings), not just the headline.
- **Per-PR independent review** (correctness / completeness / hygiene / structure + security),
  adversarially verified, fix, re-review until clean, then merge. Reviews are checkout-free
  (`git show <ref>:<path>` / `git diff main...<branch>`, Read only — never `git checkout` in the
  shared tree). Work in an isolated git worktree.
- **Local-vs-controller byte-parity for renderers.** The mimic install block is rendered by BOTH
  the Go renderer (`internal/renderer/script.go`) and the in-browser TS compiler
  (`frontend/src/compiler/renderers/script.ts`) byte-for-byte; any change MUST land in both and the
  `internal/localcompile` golden contract MUST stay green.
- **E2E locator hygiene.** Before any style change, grep `frontend/e2e` for color-class locators
  (`button.bg-*`) and prefer `data-testid` (lesson from the beta.13 theme sweep).
- **CI is the authoritative gate.** Verify locally (`go test`, `npm run build`, `vitest`) but the
  netns realtunnel + e2e + conformance + security-scan jobs are the merge gate.

## Locked decisions (owner, 2026_06_25)

- **D5 — mimic fix = comprehensive now.** Derive `local=` per-peer from the route to each peer's
  endpoint (not always `1.1.1.1`), reject a loopback/`lo` egress src, add a compile-time validation
  guard, plus the pure-Go golden test ladder. Owner runs the real-host smoke to confirm.
- **D6 — theme scope = the 3 reported bugs + the cheap `ROLE_HUE` dedup** (not a full canvas
  re-audit). Comprehensive for the reported issues + their clear cross-mode siblings.
- **D7 — Deploy-CTA = a new dedicated `--cta` token family** (vivid teal in both modes); smallest
  safe blast radius vs. brightening `--accent` (which recolors focus rings + 12 input borders +
  7 headings app-wide) or reusing `--info`/`--success` (collides with Roll-keys / overloads semantics).
- **D8 — split release.** This subject ships first as a fixes beta; mixed-mode ships separately
  (`implementation_plans/mixed-controller-local-mode-2026_06_25/`).

## Current state of the world

- `main` at the beta.13 tip; clean tree. Go at `$HOME/.local/go/bin/go`.
- Theme token source: `frontend/src/index.css` (light `:root` + dark `:root.dark`). Dark
  `--accent: #8b93a1` (graphite — intentional identity) is exactly why Deploy reads grey.
- Mimic renders identically in both modes; the bug is in the shared template, not a mode split.

## Must-read references

- `docs/spec/artifacts/mimic.md` — mimic runtime contract (egress NIC attach, `local=` filter,
  MTU−12, fallback policy, ordering, verification §164-167).
- The grounding brief (this session's investigation) — full file:line map of both defects.

## Milestones

| Plan | Title | Track | Depends on | Parallel? |
|------|-------|-------|-----------|-----------|
| plan-1 | Theme stragglers (nodeConditions tokens, canvas neutral-map + ROLE_HUE, `--cta` family) | frontend | — | yes (disjoint from plan-2) |
| plan-2 | Mimic `local=` correctness + reject-loopback guard + validation + Go test ladder (Go + TS mirror) | backend + compiler | — | yes (disjoint from plan-1) |
| plan-3 | Release the fixes beta + owner real-host mimic smoke runbook | release | plan-1, plan-2 | no |

plan-1 and plan-2 are file-disjoint and can be built concurrently. plan-3 gates on both merged +
green.

## Decisions log

- D5/D6/D7/D8 above (owner, 2026_06_25 AskUserQuestion).
- Mimic data-plane faithful smoke is NOT feasible in-sandbox (no mimic `.deb`, needs 2 real hosts +
  public UDP path). plan-2 lands pure-Go golden tests + the harmless guards; the live confirmation
  is the owner runbook in plan-3. (This is not a scope compromise — the IP-derivation fix is a real
  correctness fix gated by tests; only the kernel data-plane assertion is owner-run.)
- Canvas categorical hues (node role colors, edge-type colors) are KEPT (meaning-bearing); only
  neutral chrome (grid, label pill bg/text, shadows) maps to tokens, and they are made both-mode
  legible. Extract a single `ROLE_HUE` map so node border + handle + MiniMap stop drifting.
- **Upstream mimic filter semantics (plan-2 Step 0, confirmed via the Debian `mimic.1` manpage):** the
  filter form is strictly `{local|remote}={ip}:{port}` — **no** port-only or wildcard-IP form, IPv6
  bracketed, multiple filters are a whitelist (OR). So the fix is the **additive `remote=<peer_ep>`
  filter** (route-independent, the multi-homing fix) + a `local=` loopback guard, not a port-only match.
- **plan-2 Step 3 (compile-time validation guard) — deliberately NOT added** (documented, not a silent
  shrink). The root fix is the install-time `remote=`/loopback change; a compile-time guard cannot see
  the *runtime* egress IP (the actual failure variable), and a new cross-language validator *code*
  would either be dead (every mimic interface always gets a listen port, so a `local=` line always
  exists) or fire on configs already problematic for plain dialing reasons (no endpoint ⇒ undialable),
  which the existing `validateMimicTransport` + `validateEdgeEndpointConsistency` already cover. Added
  surface < value; folded into the spec's documented egress limitation instead. The independent review
  is the arbiter — if it judges the guard a real gap, it becomes a follow-up.

## Closure criteria

- Both plans merged via reviewed PRs (4-lens + security, re-reviewed after fixes), CI green.
- `npm run build` (tsc -b) + `vitest` + `go test -race ./...` + `go test -tags airgap ./...` green
  locally; `internal/localcompile` golden contract green (mimic TS/Go parity).
- Beta tagged, `release.yml` + `docker.yml` green, promoted to GitHub Latest, assets verified.
- Owner mimic smoke runbook written into the release notes / `docs/spec/artifacts/mimic.md`.
- STATUS.md + memory updated; subject archived to `_completed/` after the owner's live smoke confirms.

## Plan status

| Plan | Status |
|------|--------|
| plan-1 | merged (#193) — theme stragglers (node-condition chips, canvas neutral-map + `ROLE_HUE`, `--cta` family) |
| plan-2 | merged (#194) — mimic `local=`/additive `remote=` correctness + loopback-egress guard + Go test ladder |
| plan-3 | merged (#195) — released `v2.0.0-beta.14` (GitHub Latest) + owner real-host mimic smoke runbook |

All three plans merged and SHIPPED as `v2.0.0-beta.14`; subject delivered — archived to `_completed/`.
