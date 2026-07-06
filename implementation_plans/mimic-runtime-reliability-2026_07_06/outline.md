# mimic-runtime-reliability — the rc.2-soak module-build defect + robustness (ships as v2.0.0-rc.3)

> Subject opened 2026-07-06 from the owner's rc.2 live-fleet smoke. Owner decisions (preflight):
> **bundle everything into rc.3**; subject name **mimic-runtime-reliability**; remediation posture
> **detect + honor policy + clear "reboot" guidance** (install.sh must NEVER silently swap a node's
> kernel). Execution is per-PR: independent workflow review → fix → re-review → CI green → merge.

## Context — the defect the rc.2 soak surfaced (owner fleet, node hkg14, 2026-07-06)

rc.2 fixed the mimic **install** (two-package `mimic`+`mimic-dkms`; no more `exit status 100`). But a
`transport: tcp` link on `hkg14` (Debian 12) then failed at **runtime** in a retry loop. Peeling the
onion with the owner's live diagnostics:

1. First symptom — `mimic@eth0` exit **17**: `failed to lock on eth0 at /run/mimic/f0000000_2.lock:
   File exists` / `no version found in lock file`. A **stale/half-written lock** from a prior instance
   wedged every restart (systemd then rate-limits: "start request repeated too quickly"). My rc.2
   plan-3 change from a no-op `enable --now` to `systemctl restart` introduced the stop→orphan→restart
   cycle. **Secondary bug.**
2. After clearing the lock — exit **22**: `libbpf: extern (func ksym) 'mimic_change_csum_offset':
   not found in kernel or module BTFs` → `failed to load BPF program: Invalid argument` → `hint: is
   the Mimic kernel module loaded?`. The mimic **kernel module is not loaded**.
3. Root — `dkms status → mimic/0.7.1: **added**` (DKMS states: added→built→installed). The module was
   **never built** for `uname -r = 6.1.0-13-cloud-amd64`; `modprobe mimic → Module mimic not found`.
4. Why — `apt-get install linux-headers-6.1.0-13-cloud-amd64` → **`E: Unable to locate package`**.
   hkg14 has run `6.1.0-13-cloud-amd64` since Dec 2024 and never rebooted; Debian bookworm's 6.1.0
   point release advanced, so the headers for that *specific old* kernel were **pruned from the repo**.
   No matching headers ⇒ DKMS can't compile ⇒ stuck at `added` ⇒ no `mimic.ko` ⇒ exit 22.

**The code defect (`internal/renderer/script.go`, `_mimic_provision`):**
- `L571` installs `linux-headers-$(uname -r)` **best-effort** (`|| true`) — a missing-headers failure
  is swallowed.
- `L593` the *entire* provisioning success gate is **`command -v mimic`** — the userspace BINARY. The
  `.deb` installs the binary fine, so provisioning returns SUCCESS even though the DKMS **kernel
  module** silently stayed at `added`. install.sh then writes the config and starts mimic → exit-22
  loop, never checking `dkms status`/`modprobe`.
- **The `udp`-policy hole:** because provision falsely "succeeds," `if ! _mimic_provision` is false →
  `_MIMIC_SKIP` is never set → a `mimic_fallback: udp` link **does NOT degrade to plain UDP** on a
  stale-kernel node; it proceeds as if mimic works. So this bug also silently defeats the fallback.
- The docs (mimic.md) DESCRIBE the "reboot into the current kernel" case but install.sh never
  **detects** it — the fix closes doc-vs-code drift.

This is exactly the scenario `docs/spec/artifacts/mimic.md` documents ("a node behind its repo's point
release can't build the module until it reboots into the current kernel") — undetected until now.

## Immediate operator unblock (handed to owner, not code)

- No reboot: set the edge's `mimic_fallback: udp` + redeploy → the link comes up as plain UDP.
- Restore cloaking: `apt-get update && apt-get install -y linux-image-cloud-amd64
  linux-headers-cloud-amd64 && reboot` → DKMS builds mimic on the current kernel → redeploy.

## Decisions log (locked)

1. **Bundle into rc.3** (owner) — module fix + lock cleanup + egress override + capability probe + docs.
2. Subject **mimic-runtime-reliability** (owner).
3. **Remediation posture: detect + honor policy + clear guidance** (owner) — install.sh verifies the
   module is genuinely usable and, if not, honors `mimic_fallback` with a clear
   "reboot into the current kernel / use mimic_fallback=udp" message. It does NOT auto-install a
   kernel or reboot (invasive; a deploy can't reboot). It DOES install the matching
   `linux-headers-$(uname -r)` and explicitly trigger + verify the DKMS build (the buildable case).
4. New closed `MimicOutcome` **`module_unavailable`** (distinct from `install_failed` = deb failed,
   and `kernel_too_old` = no eBPF/bpffs) → a `ModuleUnavailable` mimic Node Condition.
5. Egress override is a **per-node** field (`Node.mimic_egress_interface`, default "" ≡ auto-detect,
   byte-identical) — networking is per-node; sits beside `xdp_mode`.
6. Lock cleanup + `modprobe mimic` land in the same start-block PR as the module gate (one coherent
   "robust mimic start" change).

## Plan status

| # | Plan | Status | PR |
|---|------|--------|-----|
| 1 | Module build/load verification + honor-policy + lock cleanup + modprobe (fleet-critical core) | ✅ merged | #235 |
| 2 | Per-node egress-interface override | pending | — |
| 3 | Native-XDP always-visible fix + pre-deploy "can this node run mimic" capability probe + panel warning | in review | (this PR) |
| 4 | Docs (mimic.md + bilingual wiki) + behavioral proof | pending | — |
| 5 | Release v2.0.0-rc.3 | pending | — |

## Cross-cutting invariants (review lenses check these)

- **Go↔TS byte-exact (HIGH):** every install.sh change lands in `internal/renderer/script.go` AND
  `frontend/src/compiler/renderers/script.ts` in the SAME PR (drift manifest + golden gates).
  Regenerate BOTH golden corpora (`internal/localcompile` contract + `internal/conformance`) + drift.
- **No `eq` in the TS template engine:** conditionals are precomputed bool fields (e.g. an existing
  `MimicNative`/`MimicFallbackUDP`), never `{{ if eq }}`. New template branches follow that.
- **Closed enums stay closed:** a new `MimicOutcome` needs the Go const + TS mirror + `classifyMimic`
  case + `mimicCond.*` i18n key (en+zh) + the mimic-condition test; regen drift if it tracks the enum.
- **Deployable / honor-policy (HIGH):** an unusable mimic module degrades per `mimic_fallback`
  (udp → plain UDP via `_MIMIC_SKIP`; none → fail closed) with a CLEAR breadcrumb — never a cryptic
  exit-22 loop, never a false-success that defeats the fallback.
- **Additive / byte-stable (HIGH):** the egress override empty ≡ auto-detect compiles byte-identical
  to today (assert zero churn on existing goldens); a catalog/no-mimic deploy is unchanged.
- **`gofmt -l ./cmd ./internal` clean** before every push (build/vet/test don't catch fmt drift).
- Process: no shims; structure-aware; per-PR independent workflow review → fix → re-review;
  reviews checkout-free; `.e2e-bin/e2eserver` + `frontend/dist` rebuilt before local e2e.

## Out of scope (rc.4+ / deferred)

Auto-installing a kernel or rebooting from install.sh; threading the per-node egress override into the
agent's native-XDP NIC probe (the module-capability probe is node-global, egress-independent, so
unaffected); an in-panel "reboot this node" action; upstream mimic patches (report the stale-lock +
"reclaim a versionless lock" upstream separately).
