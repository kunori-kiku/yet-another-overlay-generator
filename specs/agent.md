# Node agent

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own node-side enrollment, controller polling, WireGuard key custody, fail-closed bundle and membership
verification, crash-safe apply/uninstall, signed self-update, and last-known-good state
(`cmd/agent/main.go:87-128,924-1115`, `internal/agent/agent.go:90-349`).

## Files

- `cmd/agent/main.go:87-425,924-1115` — exposes managed commands and manual `kit verify/apply` paths.
- `internal/agent/controller_client.go:51-531` — implements node enrollment and authenticated controller calls.
- `internal/agent/cycle.go:58-248` — owns one poll, rekey, fetch, apply, and report cycle.
- `internal/agent/agent.go:90-349,531-674` — verifies, stages, executes, and records one apply/uninstall.
- `internal/agent/verify.go:126-420` — verifies bundle integrity and off-host membership.
- `internal/agent/state.go:47-216` and `internal/agent/apply_intent.go:16-248` — persist last-good floors
  and unresolved root-mutation intent.
- `internal/agent/state_lock.go:13-111` and `internal/agent/installer_command_unix.go:13-58` — keep the
  state-directory lease across installer lifetime.
- `internal/agent/selfupdate.go:1-693` — verifies, swaps, probes, finalizes, or rolls back agent binaries.

## Inputs

Managed mode receives a node ID, controller base URL, one-time enrollment token or per-node bearer,
local key/state paths, and optional tier-1 and keystone public anchors
(`cmd/agent/main.go:924-1056`). Manual mode receives a downloaded directory/ZIP and the separately
provisioned trust anchors through `kit verify/apply` (`cmd/agent/main.go:229-425`). `artifacts-signing`
and `keystone-trustlist` define the two verification layers; `agent-telemetry` owns sampling/upload.

## Outputs

The agent keeps its private WireGuard key in a protected local file and registers only the public key
(`internal/agent/keygen.go:27-148`). A successful run materializes a fresh verified bundle, writes a
durable `PendingApply`, executes the copied installer, then atomically advances last-good checksum,
membership epoch, active telemetry policy, and conditions before reporting
(`internal/agent/agent.go:287-349,531-624`).

## Decision points (if any)

- A controller cycle may time out, rotate and re-register the local key, idle an already-applied frozen
  bundle, or apply a new bundle. Rekey skips the stale pre-rotation bundle, and both rekey and idle
  advance the appropriate wake watermark (`internal/agent/cycle.go:58-196`).
- A present signature is always verified, a configured signing pin rejects unsigned input, and a
  configured/sticky keystone requires exact signed membership and an epoch at least the durable floor
  (`internal/agent/verify.go:149-249,315-420`, `internal/agent/agent.go:145-239`).
- A forced signed self-update happens before an incompatible apply; an ordinary update waits until a
  successful apply, while bounded probation and rollback protect restart loops
  (`internal/agent/agent.go:250-349`, `internal/agent/selfupdate.go:411-693`).

## Invariants

- No verification, staging, self-update, or installer failure intentionally tears down the previous
  working overlay or regresses its durable floors (`internal/agent/agent.go:90-349,631-674`).
- Every root apply/uninstall holds one state-directory lease; on Unix the guardian inherits the exact
  lock descriptor so killing the Go parent cannot admit an overlapping installer
  (`internal/agent/agent.go:115-134,531-568`, `internal/agent/installer_command_unix.go:13-58`).
- Before root mutation, `PendingApply` binds the verified bundle, action, membership floor, and trust
  anchors; exact retry or a strictly newer same-action candidate may converge it, then success clears
  it atomically (`internal/agent/apply_intent.go:16-248`).

## Gotchas (optional)

- Manual AgentHeld bundles must use `kit apply`; executing their downloaded `install.sh` directly
  bypasses the off-host membership and durable-state gates (`cmd/agent/main.go:174-210,347-425`).
- Linux root execution is supported, but Windows deliberately keeps portable verify/key operations
  while refusing `install.sh` execution (`internal/agent/installer_command_windows.go:11-18`).
- `manifest.json` compile time is only an honest-source rollback hint because it is outside the signed
  checksum set; the signed keystone epoch and bundle digest are the attacker-resistant floors
  (`internal/agent/agent.go:230-249`, `internal/agent/verify.go:366-420`).
