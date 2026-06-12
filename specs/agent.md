# Agent (node-side pull/verify/apply daemon)

<!-- last-verified: 2026-06-12 -->

## Responsibility
Pull a per-node install bundle from a configured source or the networked controller, verify it fail-closed (signature, per-file SHA-256, keystone trust-list, anti-rollback), then execute the bundle's own `install.sh` as root — never tearing down the running overlay on failure.

## Files
- `cmd/agent/main.go:1-455` — CLI: `keygen`/`enroll`/`run` dispatch (main.go:46-60), flag parsing, controller-mode single-shot and `--daemon` loops.
- `internal/agent/agent.go:1-321` — `Run(cfg *Config) (*RunResult, error)` (agent.go:67-169): pull → verify → membership → anti-rollback → stage → apply → report.
- `internal/agent/cycle.go:1-197` — `RunControllerCycle(client, CycleConfig) (resumeGen int64, applied bool, err error)` (cycle.go:84-182): one poll → fetch → (rekey | apply) cycle, the testable unit the daemon loops.
- `internal/agent/controller_client.go:1-399` — bearer-token HTTP client for `/api/v1/controller/{enroll,config,poll,report,rekey}` (controller_client.go:51); implements `Source` (Fetch, :271-312) and `Reporter` (Report, :353-368).
- `internal/agent/keygen.go:1-95` — `EnsureKey(keyPath) (pubKey string, created bool, err error)` (keygen.go:27-51, idempotent) and `RegenerateKey` (keygen.go:64-69, forced rotation); atomic 0600 writes (keygen.go:75-94).
- `internal/agent/verify.go:1-447` — `VerifyBundle(files, pinnedPubPEM)` tier-1 integrity (verify.go:112-184) and `VerifyMembership(files, MembershipConfig, prevEpoch)` keystone gate (verify.go:247-361).
- `internal/agent/state.go:1-148` — persisted `State` (state.go:29-53), `LoadState`/`SaveState` (state.go:62-98), `CheckRollback` on manifest `compiled_at` (state.go:129-147).
- `internal/agent/source.go:1-300` — `Source`/`Reporter` interfaces (source.go:57-68), `DirSource` (source.go:76-130), `HTTPSource` (source.go:138-258), `NewSourceFromSpec` for `dir:PATH | http(s)://` (source.go:285-299).

## Inputs
- **Bundles** as `map[string][]byte` path→content from a `Source.Fetch(nodeID)` (source.go:57-60): a directory tree, an HTTP file server, or the controller's `GET /config` (base64-decoded, controller_client.go:303-311) — server side documented in specs/controller-agent-api.md; bundle layout produced per specs/artifacts-signing.md.
- **CLI flags**: `--node-id`, `--source`, `--pubkey` (pinned signing key PEM), `--operator-cred`/`--operator-cred-alg`/`--operator-rpid`/`--operator-origin` (keystone pin, main.go:173-176), `--state-dir`, `--staging-dir`, `--controller`, `--token`, `--after` (resume cursor), `--daemon` (main.go:169-183).
- **Enrollment**: single-use token + node id, exchanged via `Enroll(enrollmentToken, nodeID, wgPub) (*EnrollResult, error)` (controller_client.go:205-234) for the per-node bearer token (token minting: specs/controller-agent-api.md; ceremony: docs/spec/controller/enrollment.md).
- **On-node trust anchors** (provisioned out of band, typically by the bootstrap script — see specs/controller-agent-api.md): pinned signing pubkey PEM and the off-host operator credential at `/etc/wireguard/operator-cred.pem` (internal/api/handler_bootstrap.go:286-288).
- **Generation signals**: long-poll `Poll(after) (gen, changed, err)` (controller_client.go:319-346) and the `rekey_requested` flag in the `/config` envelope (controller_client.go:70-77) — set by promote/rekey flows, see specs/controller-stage-promote.md and specs/panel-deploy-fleet.md.

## Outputs
- **Applied overlay**: the verified bundle staged to a 0700 dir (agent.go:176-209) and executed via `bash install.sh` (agent.go:229-250); install.sh — not the agent — splices the private key from `/etc/wireguard/agent.key` (agent.go:229-232; script contents: docs/spec/artifacts/install-script.md).
- **Persisted state**: `State` JSON at `/var/lib/yaog-agent/state.json` (state.go:21-25) recording `last_compiled_at`, `last_checksum`, `last_result`, `membership_epoch`, `health` (state.go:29-53).
- **Status reports**: best-effort `Reporter.Report(nodeID, payload)` — `POST /report` with `{applied_generation, checksum, health}` (controller_client.go:90-94, 353-368), feeding the registry in specs/controller-store.md.
- **WireGuard public keys**: printed by `keygen` (main.go:93), registered at enroll (main.go:124-137), and re-registered via `Rekey(wgPub)` after a rotation (controller_client.go:242-265) — consumed by the compiler per specs/render-keys.md.
- **Secrets on disk**: WG private key at `/etc/wireguard/agent.key` 0600 (keygen.go:15) and bearer token at `/etc/wireguard/agent-controller.token` 0600 under a 0700 dir (main.go:73, 156-164).

## Decision points
- **Mode select**: `--controller` set → controller mode, else `--source` is required (main.go:191-211); `--daemon` → loop forever, else one cycle (main.go:351-383).
- **Signature policy** (verify.go:129-157): signature present → always verified (pinned key wins over the bundle's `signing-pubkey.pem`); key pinned but bundle unsigned → fail closed; neither → hash-only verification permitted.
- **Keystone on/off**: empty `OperatorCredPEM` → `VerifyMembership` is a no-op (verify.go:249-251); set → requires `trustlist.json`/`trustlist.sig`/`checksums.sha256`, canonical-bytes equality, off-host signature, bundle-digest binding, self-membership, peer-key membership, and epoch ≥ floor (verify.go:256-360) — primitives in specs/keystone-trustlist.md. Algorithm dispatch is on the PINNED alg, never the artifact's (verify.go:367-390).
- **Rekey vs apply** (cycle.go:113-148): after a poll wake, Fetch first; if `LastRekeyRequested()` → `RegenerateKey` + `Rekey`, SKIP apply, resume from `max(polledGen, LastFetchedGeneration())` (cycle.go:143-147); else `agent.Run` and resume from `LastFetchedGeneration()` (cycle.go:179-181, closing the poll→fetch race).
- **Anti-rollback**: candidate `compiled_at` strictly older than last applied → refuse; equal → idempotent re-apply allowed; corrupt baseline → allow forward (state.go:129-147). Trust-list epoch strictly older → refuse (verify.go:356-358).
- **Identity guard**: manifest `node_id` non-empty and ≠ configured node id → refuse (agent.go:106-109).
- **Report truthfulness**: `Report` sends the fetched generation only when `LastResult == "ok"`, else the unchanged prior watermark set via `SetPriorGeneration` (controller_client.go:138-140, 359-367; cycle.go:154).

## Invariants
- **Zero-knowledge key custody** (PRINCIPLES.md "Key custody"): the WG private key is the only secret the agent generates, written exclusively to the key path at 0600 via atomic temp+rename, never logged or returned (keygen.go:24-26, 75-94); the agent never splices it — install.sh does (agent.go:229-232; docs/spec/controller/key-custody.md).
- **Keep-last-good**: every failure path (fetch, verify, membership, rollback, stage, apply, rekey) leaves the running overlay untouched and preserves the prior baseline — `recordFailure` never advances `LastCompiledAt`/`LastChecksum`/`MembershipEpoch` (agent.go:275-295), and `RunControllerCycle` returns the unchanged watermark on any error (cycle.go:80-83, 114, 137-140, 169).
- **Fail-closed before root**: `install.sh` runs only after `VerifyBundle` → `VerifyMembership` → `CheckRollback` all pass and the bundle is staged (agent.go:114-160); `install.sh` itself must exist and be checksummed (verify.go:176-181) — PRINCIPLES.md "Generated scripts run as root on fleets".

## Gotchas
- **The daemon success path has NO sleep**: pacing comes entirely from the server-side long-poll (client `pollHTTPTimeout` 90s deliberately exceeds the server's ~55s deadline so a 204 timeout is a response, not a transport error — controller_client.go:43-47); only the error path backs off, a fixed 5s (main.go:373-383). A timed-out poll returns `(after, false, nil)` and the loop immediately re-polls (cycle.go:105-107).
- **`compiled_at` anti-rollback is honest-source-only**: `manifest.json` is deliberately outside the signed/checksummed set, so an active attacker can forge `compiled_at` (in-code note at agent.go:138-142, pointing to docs/spec/controller/agent.md). The attacker-resistant rollback floor is the keystone `membership_epoch` — only enforced when keystone is ON.
- **The `--after` cursor is a flag, not persisted state**: `State` keys rollback on the `compiled_at` string, not the controller's int64 generation (main.go:270-274, 320-324), so a single-shot re-run with the default `--after 0` re-fetches (and idempotently re-applies) the current generation; only the in-process daemon advances the cursor (main.go:375-383). The rekey-wake watermark advance (cycle.go:122-147) exists because a rekey-all bumps the tenant generation WITHOUT changing the bundle — resuming from the fetched (smaller) generation would tight-loop or re-apply the stale pre-rekey bundle.
