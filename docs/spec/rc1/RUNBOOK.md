# RC1 RUNBOOK — irreducible real-hardware/firmware/kernel manual smokes

> **What this is.** The *smallest defensible set* of manual smokes the owner must run before tagging
> `v2.0.0-rc.1` — the legs that **no** in-process test, browser E2E, or single-host netns tier can
> synthesize honestly. Produced by plan-19 (3.7). The **live pass/fail STATE** of these legs lives in
> [`RC1-GATE.md`](../../../RC1-GATE.md) (criterion C1, the three-state ledger); **this file owns the
> *reasoning* and the *procedures*.** Do not duplicate the live STATE here — one ledger, no drift.

## How the nine owed smokes collapse to three (+ one open dependency)

`STATUS.md` carried **eight** "owner-accepted risk, gate rc.1" smokes accumulated across beta.1–beta.7,
plus a **ninth** added at Subject-2 closure (the phone-UX smoke, `STATUS.md:132`) — **nine** total.
Three mechanizing tiers — all now **landed** — subtract everything a machine can prove, leaving three
genuinely-irreducible sections **plus one open dependency** (smoke #5, below — bucket-B-eligible but its
covering spec was never written, so it is flagged, not silently retired, per the plan's Risk R1):

| Tier | What it mechanizes | Status |
|------|--------------------|--------|
| **A — Go↔TS conformance + unit** (`internal/conformance/`, `normalize/pins_test.go`, `allocation_stability_test.go`, the TS `normalizeEdges.ts` pin) | pin/heal byte-equality, no-drift, babeld.conf C1 golden, self-update logic | landed (plan-5 / Subject 1, PR #142) |
| **B — browser E2E** (`frontend/e2e/`, Playwright + virtual WebAuthn) | login/refresh/no-token-in-localStorage, deploy/rekey/revoke UX, export/import round-trip, responsive matrix, adversarial/edge (**not** the rollout UI — see open dependency #5) | landed (plans 13–17, PRs #149–#154) |
| **3.6-netns — real-tunnel data plane** (`test/realtunnel/`, systemd-nspawn) | per-iface WG handshake, babel route convergence, overlay ping 0% loss, SNAT transit→overlay rewrite, the C3 reverse-endpoint footprint | landed (plan-18, PR #155) |

Because **3.6 is delivered, mandatory, and required-in-CI**, it brings up the generated
WireGuard+Babel+SNAT data plane on a real Linux kernel and asserts handshake / route / ping / SNAT /
C3 — so the old "real multi-host NAT" smoke collapses to a single narrow property. The residue is
**three** sections (the labels C1/C2/C3 below are *runbook sections*, NOT the investigation-report
finding numbers):

- **Section C1 — real WebAuthn authenticator** (firmware/UX): the login ceremony + keystone-sign +
  passkey rotation. A synthetic assertion is exactly what a real authenticator may not emit.
- **Section C2 (shrunk) — real-NAT-box endpoint-rewrite survival**: the *single* property a one-host
  netns cannot synthesize — a stateful NAT box rewriting the source port + a rebinding/idle cycle.
- **Section C3 — mimic eBPF/DKMS/XDP on a real ≥6.1 kernel**: the one data-plane leg 3.6 explicitly
  excludes (`transport:tcp`); DKMS compile + module load + XDP attach + on-wire TCP shaping.

### Analytical triage (the canonical "why" — live STATE is in `RC1-GATE.md`)

| # | `STATUS.md` owed smoke | Buckets (per leg) | Subtracting tier → residue |
|---|---|---|---|
| 1 | WebAuthn login + refresh-survival + no-token-in-localStorage | **C** (login ceremony) + **B** (refresh/token) | refresh → `frontend/e2e/session.spec.ts`; no-token → `frontend/e2e/fixtures/leakOracle.ts` (`assertNoFleetSecrets`); login wiring → `frontend/e2e/login-webauthn.spec.ts` (B); login *ceremony* → real authenticator (**§C1**). |
| 2 | NAT sticky-pin Compile→edit→deploy→no-drift | **A** (no-drift) + **3.6-netns** (deploy/handshake/route/SNAT/C3) + **C** (NAT-box rewrite only) | no-drift → conformance (A); deploy/handshake/route/ping/SNAT/dummy0 → 3.6 REQUIRED canary (`TestSimpleMeshCanary`); the C3 reverse-endpoint footprint → 3.6 ADDITIVE `TestC3OneDirectional` (currently `continue-on-error`, covered-but-not-yet-required-gated); residue = **only** real-NAT-box source-port rewrite (**§C2-shrunk**). |
| 3 | mimic GitHub-`.deb` install on kernel ≥6.1 | **C** (eBPF/DKMS/XDP) | `.deb` pin/verify bash unit-tested (`script_mimic_test.go:100`); 3.6 proves the non-mimic data plane but **excludes** mimic; DKMS+load+XDP need a real ≥6.1 kernel (**§C3**). |
| 4 | Self-update field smoke | **A** | mechanics tested + deep-reviewed; optional real-host confirmation, NOT a gate. |
| 5 | Panel rollout-UI smoke | **B-eligible, NOT YET COVERED** | bucket-B-eligible (pure browser UX) but **no rollout Playwright spec exists** (the `UpdateStatusChip`/`AgentUpdateSettings`/`MimicCatalogSettings` cards + the pending→applying→applied chip + Live-poll-stop have neither an E2E nor a vitest test). **OPEN DEPENDENCY** (Risk R1): stays owner-owed until a rollout spec lands or the owner accepts the risk. |
| 6 | Keystone rotation + reprovision (2 hosts) + passkey rotation | **A** (headless) + **C** (passkey) + **3.6-netns** (restart/reconverge) | headless covered; passkey rotation → **§C1**; systemd restart→re-pin→reconverge is netns-confirmable (`requireRouteConvergence`); a thin owner systemd-lifecycle confirmation remains, folded into §C1's evidence. |
| 7 | Fleet-operability panel smoke | **A** (heal/pin) + **B** (UX) | heal/pin in unit + conformance; cancel-rekey/advisory/server-truth → `frontend/e2e/fleet-rekey.spec.ts`. **No irreducible residue.** |
| 8 | Pin-collision + Export/Import smoke | **A** (heal) + **B** (round-trip) | heal (A); export/import round-trip + no-localStorage-leak → `frontend/e2e/export-import.spec.ts` (`assertNoFleetSecrets`). **No irreducible residue.** |
| 9 | Phone-UX smoke (Subject 2, `STATUS.md:132`) | **B** | responsive behavior matrix → `frontend/e2e/responsive/` (plan-17, device-emulation smokes). **No irreducible residue.** |

**Disposition:** smokes 4,7,8,9 fully retired to CI; **5 is an OPEN DEPENDENCY** (bucket-B-eligible, no spec yet — not retired); 1,2,3,6 reduce to the three irreducible sections below.

### Open owner decision (raise before `RC1-GATE.md` freezes — plan-22)

If the owner escalates 3.6 to a **required Tier-3** (real multi-host VMs with real NAT), **§C2 retires
almost entirely** (rebinding/CGNAT folds into that tier) and only **§C1 + §C3** remain irreducible.
**Working assumption (conservative default):** 3.6 stays Tier-1 (single-host netns, as delivered), so
§C2-shrunk survives as the single NAT line the gate ledger carries or accepts-risk. Confirm at plan-22.

### Adding a NEW owed smoke (a future beta)

Any new owed smoke MUST be triaged through the same rubric — **A** (conformance/unit) / **B**
(browser E2E) / **3.6-netns** (generated-config data plane on a real kernel) / **C** (irreducible
firmware/kernel/NAT-box) — and its live STATE recorded in `RC1-GATE.md`, never as a parallel
`STATUS.md` table.

---

## Section C1 — real WebAuthn authenticator device

**Why irreducible.** A synthetic assertion (the Playwright CDP virtual authenticator) proves the
*protocol wiring* but is exactly what a real authenticator may legitimately differ from: synced
passkeys emit a signature counter of 0 (intentionally NOT checked — `webauthn.go:18-21,163-164`), and
the platform-attested `clientDataJSON.origin` is only meaningful from real firmware. The keystone /
controller surface is explicitly out of 3.6's scope, so the netns tier cannot reach this leg.

**Prerequisites.**
- A TLS-fronted panel origin (`YAOG_PANEL_ORIGIN` set + `YAOG_SECURE_COOKIE=true`).
- ≥1 platform authenticator (Touch ID / Windows Hello / Android); ideally one roaming key (YubiKey).
- An operator credential pinned: `create-operator` + the passkey-pin flow (pin handlers
  `internal/api/handler_passkey.go`; the verifier is `internal/trustlist/webauthn.go:111`
  `verifyAssertion`).

**Steps (copy-pasteable on the live panel).**
1. Log in with the authenticator → the canvas hydrates.
2. Hard-refresh the page → the session survives (httpOnly cookie; no re-auth prompt).
3. DevTools → Application → Local Storage: confirm **no** operator token/bearer is present (also a
   Playwright target, bucket B — this is a belt-and-suspenders confirmation, not the irreducible core).
4. On a **keystone-ON** tenant, click Deploy → the keystone-sign ceremony prompts the authenticator;
   the browser produces an ES256 assertion; the signed trust-list is **accepted** by the Go verifier.
   This exercises the **fixed F1 route** `getTrustlist` →
   `request(cfg, 'trustlist', …)` (`frontend/src/api/controllerClient.ts:993-1005`, `credentials:'include'`+CSRF).
5. Passkey rotation (owed #6): rotate the pinned credential, then re-login with the new authenticator.
6. (Thin systemd-lifecycle confirmation, owed #6) On the two real hosts, after a keystone rotation +
   `yaog-agent reprovision-keystone` + `systemctl restart yaog-agent`, confirm the fleet reconverges
   (the *route* leg itself is netns-covered by `requireRouteConvergence`; here you only confirm the
   real systemd restart re-pins + restarts cleanly).

**Pass predicate (binary).** login succeeds **AND** refresh survives **AND** no token in localStorage
**AND** the keystone-sign assertion is accepted on Deploy **AND** the rotated credential can re-login.

**Fail signatures.**
- A **401 on Deploy keystone-sign** ⇒ F1 regressed (the trustlist fetch stopped sending credentials;
  see the F1 comment at `controllerClient.ts` around `getTrustlist`).
- (Keystone-sign leg, step 4 ONLY) An assertion **accepted with a mismatched browser origin** ⇒ the
  *keystone* operator credential is pinned with an empty `pin.Origin` **by design** (the keystone-
  credential pin path is `internal/api/handler_keystone.go` `pinFromCredential`; an empty RPID/Origin is
  explicitly valid for a keystone binding — `handler_bootstrap.go:306`), so the advisory origin check
  (`webauthn.go:170-171`, `if pin.Origin != "" && cd.Origin != pin.Origin`) is intentionally disabled
  there. **Record the pin's Origin as evidence** — on the keystone path this is a config-gated (B3)
  property to observe, not auto-fail.
- (Login leg, steps 1–2) A **login** credential is a *sibling* key on a different path
  (`internal/api/handler_passkey.go`) and MUST carry a non-empty Origin (B3, `handler_passkey.go:236-237`,
  enforced at `:243-246`) so the same advisory gate IS authoritative for login — so a mismatched-origin
  acceptance on the **login** leg is a **real failure**, not an observe-only property.
- A real authenticator **rejected** where the virtual one passed ⇒ a firmware/UV-policy gap.

**Evidence to capture.** the login + Deploy network traces (status codes), a screenshot of the empty
localStorage, the pinned credential's `Origin` value, and the rotation re-login confirmation.

**Cannot be harnessed or netns'd because.** the firmware path has no test double (the synced-passkey
counter=0 and platform-attested origin are real-device properties — `webauthn.go:163-171`), and 3.6 is
explicitly out of the keystone/controller surface (3.6 Scope-Out).

---

## Section C2 (shrunk) — real-NAT-box endpoint-rewrite survival

**Why irreducible (and why only this).** 3.6's single-host netns supplies real kernel interfaces but
**not a stateful NAT box**. Everything else about a NAT'd deploy is already netns-covered:
bidirectional handshake, babel route convergence, overlay ping, SNAT transit→overlay rewrite, dummy0
routing, and the `has_public_ip=false` one-directional reverse-endpoint footprint
(`test/realtunnel/`: `TestSimpleMeshCanary`, `TestRelayTopology`, `TestNatHub`, `TestC3OneDirectional`).
**Do NOT re-test any of those here — that is a double-count.** This section tests the **one** property a
one-host netns cannot synthesize: survival across a real source-port rewrite + a NAT rebinding cycle.

**Prerequisites.** ≥2 hosts where one sits behind a **real stateful NAT box** that rewrites the source
port (one public VPS + one home-NAT box; a CGNAT / port-restricted cone if available). Sticky-pin
model: transit IPs `10.10.0.0/24`, a per-edge NAT port + transit IP (PRs #98–#106).

**Steps (NAT-box-specific ONLY).**
1. Compile a two-node topology with one NAT'd node; set its forward `Endpoint host:port` to the NAT
   box's **public** address/port (the forward-endpoint derivation is `internal/compiler/peers.go:717-731`,
   `formatEndpoint` at `peers.go:1124`).
2. Deploy to both hosts; bring the tunnel up.
3. Idle past the NAT's UDP mapping timeout, then send overlay traffic → confirm the handshake
   **re-forms** (or that `PersistentKeepalive` held the mapping the whole time).
4. (Best-effort, CGNAT / port-restricted cone) confirm the generated `Endpoint host:port` still reaches
   the peer after the source-port rewrite.

**Delegated to 3.6 (do NOT re-run here):** bidirectional handshake, babel route convergence, overlay
ping, SNAT transit→overlay rewrite, dummy0 routing, and the C3 `has_public_ip=false` one-directional
footprint — all asserted by `test/realtunnel/`.

**Pass predicate (binary).** the tunnel **survives** a real NAT-box source-port rewrite and a
rebinding/idle-timeout cycle (the handshake re-forms, or keepalive holds the mapping the whole time).

**Fail signatures.** the tunnel forms once then **dies after the NAT mapping ages out and never
re-forms** (a keepalive/endpoint-rebind gap) — distinct from the C3 one-directional footprint, which is
3.6's caught regression, not this.

**Evidence to capture.** `wg show` latest-handshakes before/after the idle cycle on both ends; the NAT
box's observed public source port vs the configured `Endpoint`.

**Cannot be harnessed or netns'd because.** a single-host netns has no stateful NAT box; only a Tier-3
multi-host-VM-with-real-NAT tier would exercise port rewrite / rebinding / CGNAT cones (out of CI
scope). **If the owner escalates 3.6 to a required Tier-3, this section retires** (see the open owner
decision above).

---

## Section C3 — mimic eBPF on a real ≥6.1 kernel

**Why irreducible.** 3.6's netns MVV proves the **udp+babel+SNAT data plane on a real kernel** but
**explicitly excludes mimic / `transport:tcp`**. DKMS compilation against the running kernel, module
load, XDP attach, and on-wire TCP shaping need a real ≥6.1 kernel + NIC. The `.deb` pin/verify bash is
already unit-asserted (`script_mimic_test.go:100`, `:218`); this run confirms it on real silicon — it
does not re-test the bash.

**Prerequisites.** Debian 12 / Ubuntu 24.04 on a ≥6.1 kernel with `apt`/`dpkg`; DKMS + kernel headers
+ a toolchain installable (`script.go:478-482`); a configured mimic catalog in `artifacts.json`
(`.mimic.release_url` + `.mimic.debs["<codename>-<arch>"].{asset,sha256}`). **First refresh/confirm the
catalog pins against the current GitHub release** before treating a `sha256sum -c` mismatch as a
regression.

**Steps.**
1. Deploy a two-node topology with one `transport="tcp"` edge.
2. Run the install script → the GitHub `.deb` fallback fires (`internal/renderer/script.go:438-482`,
   after the distro-package attempt at `:446`): `curl` the pinned asset, then
   `sha256sum -c -` (`script.go:477`) **MUST pass BEFORE** `apt-get install "$_mimic_deb"`
   (`script.go:482`) — the ordering is unit-asserted at `script_mimic_test.go:100`; confirm it on the
   real box.
3. DKMS builds the eBPF module (headers at `script.go:478-480`), it loads, and
   `systemctl enable --now mimic@<egress>` succeeds (`set -euo pipefail` makes a no-eBPF kernel a hard
   abort).
4. `wg-quick up` runs **after** mimic (ordering per `docs/spec/artifacts/mimic.md`).
5. `tcpdump` on the egress NIC shows **TCP-shaped** packets where WireGuard would be UDP.
6. Confirm `xdp_mode = skb` is the default (`script.go:43-45` `MimicXDPMode`, emitted at `:716`;
   unit-asserted at `script_mimic_test.go:218`).

**Pass predicate (binary).** the pinned `.deb` verifies **AND** the DKMS module builds+loads **AND**
`mimic@<egress>` is active **AND** a WG handshake forms through it **AND** `tcpdump` confirms TCP
shaping.

**Fail signatures.**
- `sha256sum -c` **mismatch** ⇒ pin/asset drift (first refresh the catalog, then re-judge).
- DKMS **build failure** ⇒ missing kernel headers / toolchain.
- A clean **install-abort on a <6.1 / no-eBPF kernel** ⇒ the expected hard failure (record as the
  negative-path confirmation, not a defect).

**Evidence to capture.** the `sha256sum -c -` output, `dkms status`, `systemctl status mimic@<egress>`,
and the `tcpdump` capture showing TCP-shaped frames.

**Cannot be harnessed or netns'd because.** DKMS compiles against the *running* kernel and the module
attaches via XDP to a real NIC; a netns shares the host kernel and has no eBPF/DKMS build surface, and
3.6's MVV explicitly excludes mimic.

---

## Cross-reference

- The **live three-state STATE** (automated / owner-run-passed / owner-accepted-risk) for every leg
  above lives in [`RC1-GATE.md`](../../../RC1-GATE.md) (criterion C1). This file is the *procedure + reasoning*;
  that file is the *state-of-record*. They link bidirectionally.
- rc.1 gates on the surviving irreducible runs via `RC1-GATE.md` criterion C1 (hard-vs-advisory per the
  owner; recorded there).
- **Retirement:** when all three sections become mechanizable (3.6 escalates to a required Tier-3
  absorbing §C2, **and** a CI virtual-authenticator + a CI ≥6.1-kernel-with-eBPF runner absorb §C1/§C3),
  move this runbook to `tests/legacy/rc1/residual-manual-smokes/` with a tombstone pointing at the
  absorbing tier.
