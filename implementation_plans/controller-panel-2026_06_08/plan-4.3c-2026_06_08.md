# Plan 4.3c — Phase 2c-c: agent mTLS client + end-to-end loop

Parent: [plan-4.3-2026_06_08.md](plan-4.3-2026_06_08.md) · Prereq: 4.1/4.2/4.3a/4.3b merged. Third and
final 4.3 PR — connects the existing node agent (Plan 3) to the networked controller (4.3b), closing
the single-tenant control loop end to end.

## Goal

Give `cmd/agent` a **controller mode**: enroll over TLS, then pull/verify/apply/report over **per-node
mTLS** — reusing the agent's existing verify (`VerifyBundle`) + apply (run `install.sh`) from Plan 3.
Prove the whole chain in an in-process test: enroll → operator stage/promote → agent poll → config →
verify → apply (mocked) → report.

## Bootstrap-trust decision (adopted)

The agent's **first** `/enroll` call must trust the controller's TLS server cert before it has anything
from the controller. Resolution: the operator provides the controller's **CA cert PEM out-of-band**
(alongside the single-use enrollment token) — the agent is configured with `--controller-ca <pem>` and
trusts it for the enroll TLS *and* the subsequent mTLS. (The DevCA is ephemeral, so on a controller
restart the operator re-distributes the CA cert + re-issues tokens — same "re-enroll on restart" model
as 4.2; honest about the operational cost.) The enroll response's `ca_cert_pem` MUST equal the pinned
CA; a mismatch is a hard refusal.

## Implementation

1. `internal/agent/controller_client.go` (NEW): a `ControllerClient{baseURL, caPEM, clientCert}` over
   `net/http` + `crypto/tls`:
   - `Enroll(token, nodeID, csrDER, wgPub) (EnrollResult, error)` — POST `/api/v1/controller/enroll`
     over TLS trusting `caPEM` (no client cert); verify the response `ca_cert_pem == caPEM`.
   - `Poll(after) (gen int64, changed bool, err error)` — GET `/poll?after=` over mTLS; 204 → no change.
   - `Config() (files map[string][]byte, gen int64, err error)` — GET `/config` over mTLS; base64-decode.
   - `Report(appliedGen, checksum, health) error` — POST `/report` over mTLS.
   - Implements the existing `agent.Source` (`Fetch`) where natural, or is used directly by the run loop.
2. `cmd/agent` subcommands:
   - `enroll` — generate the **mTLS** keypair (Ed25519) + CSR (CN `<tenant>:<nodeID>`), call
     `ControllerClient.Enroll`, write the client cert + key to `/etc/wireguard/agent-mtls.{crt,key}`
     (0600); also ensure the **WG** key (`agent keygen`, Plan 1b) exists and register its public key in
     the enroll request. Print the result.
   - `run --controller <url> --controller-ca <pem> [--pubkey <pinned signing pem>]` — the controller
     control loop: load the mTLS cert; `Poll` (long-poll); on a new generation, `Config` → write the
     bundle to a staging dir → reuse `VerifyBundle` (Go-side, fail-closed) → run `install.sh` (verify +
     splice + apply) → `Report`. Keep-last-good / fail-closed exactly as Plan 1b; anti-rollback uses the
     bundle generation (now bound in signed content) instead of the unsigned manifest.
3. The agent does NOT splice or render — it reuses Plan 1b's apply path verbatim.
4. Tests `internal/agent/controller_client_test.go` (in-process, reuse the httptest+TLS+dev-CA harness
   pattern from 4.3b): stand up the real `ControllerHandler` over httptest TLS; the agent `ControllerClient`
   enrolls (certless), an operator stages+promotes, the agent polls (gets the gen), fetches config,
   `VerifyBundle` passes, the apply step is **mocked** (assert the staged bundle is what would be applied),
   and `Report` updates the registry. Plus: a CA-mismatch enroll is refused; a poll/config without the
   mTLS cert fails.
5. Spec: extend `docs/spec/controller/agent.md` with the controller mode (enroll/run, bootstrap-trust
   CA pin, mTLS client) — the Plan 1b static-source mode stays documented for air-gap.

## Definition of done

- [ ] CI green; in-process e2e covers enroll→stage/promote→poll→config→verify→(mock apply)→report;
      CA-mismatch + no-cert rejections; no new go.mod dep; agent reuses VerifyBundle + install.sh.
- [ ] **Real-host two-node smoke** recorded in the PR is OWED (manual; cannot be CI'd) — actual mTLS
      handshake + a real `install.sh` apply on two Linux hosts.

## Out of scope (4.4 / Plan 5)

The frontend Deploy panel (4.4); OIDC/RBAC, multi-tenant, KMS, stage→promote step-up, hardware-signed
membership (Plan 5).
