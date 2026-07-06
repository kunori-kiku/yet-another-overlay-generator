# mimic-fleet-robustness — the rc.3-soak fleet findings (ships as v2.0.0-rc.4)

> Subject opened 2026-07-07 from the owner's rc.3 live-fleet debugging. Five YAOG-fixable findings;
> the L7-relay incompatibility itself is `transport: udp` (topology), not code. Owner: "plan and fix
> what you can fix." Execution per-PR: independent workflow review → fix → re-review → CI green → merge.

## Context — what the rc.3 soak surfaced (owner fleet, 2026-07-07)

rc.3 made stale-kernel nodes degrade cleanly instead of the exit-22 loop (working as designed). But the
owner's deeper fleet debugging surfaced five distinct issues, root-caused with live diagnostics:

1. **Missing mimic build deps.** On a current Debian kernel (`6.1.0-49-amd64`, headers present) the DKMS
   build failed: `make: bwrap: No such file or directory` (Error 127), then after installing
   bubblewrap, `pahole: not found`. mimic-dkms's build needs **`bubblewrap`** (build sandbox) +
   **`dwarves`** (`pahole`, BTF gen) but declares neither, and YAOG's installer only pulls
   `dkms`/`gcc`/`linux-headers`. Installing both by hand → the module built + loaded + mimic ran. So
   this is a missing-dependency bug, NOT a kernel incompatibility. **CONFIRMED fix.**
2. **Stale mimic condition in the panel.** The `mimic` Node Condition is a DEPLOY-TIME breadcrumb the
   agent re-reads verbatim; it is never re-derived from the live unit state. `systemctl stop mimic@eth0`
   → the panel still shows "active." Unlike the wireguard condition (live `wg show` since beta.10), the
   mimic condition is frozen at apply time. **CONFIRMED bug.**
3. **tcp→udp doesn't stop mimic.** The mimic teardown (`systemctl disable --now mimic@…`) lives ONLY in
   the `--uninstall` path, gated on `{{ if .HasMimic }}`; the install-path Phase 0 has NO mimic
   teardown. So flipping a node's last tcp link to udp (`HasMimic` true→false) leaves the old `mimic@`
   running → it keeps shaping traffic WG now sends as plain UDP → the link stays broken. **CONFIRMED
   bug (verified in code: teardown at script.go ~197 is inside `if UNINSTALL`, Phase 0 at ~295 has no
   mimic teardown).**
4. **`mimic_fallback: udp` can split a link (unilateral fallback).** A link needs BOTH ends on mimic or
   BOTH on UDP; a per-node runtime fallback where one end degrades and the other still shapes → the
   shaping end sends fake-TCP the UDP end can't decode → link dead. The clean operator fix is
   `transport: udp` on that edge (both ends). rc.4 surfaces this rather than auto-coordinating (see
   Out of scope).
5. **mimic over an L7 relay can't work.** The owner's tcpdump showed a UDP-accelerator/L7 relay
   (DNAT+SNAT, terminates+re-originates) RST-ing the reverse fake-TCP leg (`154.3.37.50:51820 →
   209.248.1.98:47883 [S]` → relay `[R.]`, while the forward leg used `:45606`). mimic needs L3/L4
   transparency; an L7 relay breaks the end-to-end fake-TCP. The edge should be `transport: udp`. The
   topology already tags an edge's `type` as `direct`/`public-endpoint`/`relay-path`/`candidate`
   (model topology.go:194, TS topology.ts:89), so YAOG can WARN at design time.

## Decisions log (locked)

1. **Scope = the 5 clear fixes** (owner: "fix what you can fix"). Auto-coordinated fallback deferred
   (Out of scope) with rationale.
2. Items 1 (deps) + 3 (teardown) are both install.sh → one plan/PR (one goldens regen).
3. The relay/L7 finding is a **warning** (not a hard error) — a `transport: tcp` edge whose `type` is
   `relay-path` gets a validator warning advising `udp`; deploy is not blocked.
4. Live mimic condition (item 2) mirrors the existing `wgShowFn`/`wgShowTimeout` pattern
   (`conditions_wireguard.go`) — a timeout-guarded `systemctl is-active mimic@<egress>` probe,
   indirected for tests. The egress comes from the breadcrumb's `egress` field.

## Plan status

| # | Plan | Status | PR |
|---|------|--------|-----|
| 1 | install.sh: mimic build deps (bubblewrap+dwarves) + unconditional Phase-0 teardown | ✅ merged | #241 |
| 2 | Live mimic condition (re-probe the mimic@ unit each heartbeat) | ✅ merged | #242 |
| 3 | Relay-path warning (`transport: tcp` on a `relay-path` edge) | ✅ merged | #243 |
| 4 | Docs (mimic.md + bilingual wiki) | ✅ merged | #244 |
| 5 | Release v2.0.0-rc.4 | ✅ released (tag cbe0735) | #245 |

## Cross-cutting invariants (review lenses check these)

- **Go↔TS byte-exact:** every install.sh change lands in `script.go` (node+client) AND `script.ts` in
  the SAME PR; regen BOTH golden corpora + drift. No `{{ if eq }}` (precompute bools). Assert
  non-affected fixtures are byte-stable.
- **Deps installed BEFORE the two-package `.deb`:** the mimic-dkms postinst builds at install time, so
  `bubblewrap`+`dwarves` must be present first (beside `dkms`/`gcc`, script.go ~578) + in the
  `_mimic_module_ready` retry.
- **Teardown must not break a HasMimic=true node:** Phase 0 stops any stale `mimic@`, Phase 3
  re-provisions — assert a tcp node still ends with mimic active; a tcp→udp node ends with mimic gone.
- **Live probe is timeout-guarded + best-effort:** a wedged systemctl NEVER blocks the heartbeat
  (mirror wgShowTimeout); a stopped unit while the breadcrumb says active → a `warn` condition.
- **New validator code = drift regen + `error.<code>` i18n (en+zh).**
- `gofmt -l` clean; `.e2e-bin/e2eserver`+`dist` rebuilt before local e2e; FE tests under `src/api/`
  named `*.conformance.test.ts` (vitest glob); the Go install template is a backtick raw string (no
  backticks inside).

## Out of scope (deferred / not code)

- **Auto-coordinated fallback (telemetry→compile).** After item 1 most nodes build mimic; the residual
  genuinely-unbuildable node's clean fix is `transport: udp` on its edges (now surfaced by items 2+3).
  Forcing both ends to UDP from the controller's stored `mimic_capability` telemetry is a complex
  feedback loop with staleness/timing concerns — deferred to a future rc with owner buy-in.
- **The L7-relay link itself** — `transport: udp` for relayed edges; mimic is for direct paths. rc.4
  only WARNS about it (item 3).
- Upstream mimic packaging (it should Depend on bubblewrap+dwarves) — report upstream; YAOG installs
  them defensively.
