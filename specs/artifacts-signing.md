# Artifacts and signing

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the canonical per-node bundle member set, its deterministic checksum serialization, optional
tier-1 Ed25519 signature, safe disk publication, and the equivalent in-memory artifact view
(`internal/artifacts/export.go:23-95`, `internal/localcompile/compile.go:109-173`).

## Files

- `internal/artifacts/export.go:23-95` — defines the single canonical bundle member constructor.
- `internal/artifacts/export.go:169-431` — validates, materializes, signs, and atomically publishes
  complete export trees.
- `internal/bundlesig/bundlesig.go:28-205` — loads/injects signing keys and canonicalizes, signs, or
  verifies checksum bytes.
- `internal/localcompile/compile.go:109-173` — reshapes the same member set for CLI/WASM/controller
  in-memory consumers.

## Inputs

`render-keys` supplies a fully rendered `CompileResult`, custody mode, and an already resolved
`ConfigSigner`; `telemetry-policy` may add exactly one AgentHeld policy member
(`internal/artifacts/export.go:48-95`). Disk callers supply a dedicated output directory, while the
in-memory path receives the same result and signer directly
(`internal/artifacts/export.go:186-229`, `internal/localcompile/compile.go:109-130`).

## Outputs

Each node receives the sorted canonical members plus `checksums.sha256`, `manifest.json`, and, when
configured, `bundle.sig` and `signing-pubkey.pem`. Project deploy helpers remain outside node bundles
(`internal/artifacts/export.go:285-418`). `ArtifactsFromResult` returns the same files, checksum bytes,
signatures, public key, and project helpers without filesystem, clock, or environment reads
(`internal/localcompile/compile.go:131-173`).

## Decision points (if any)

- AgentHeld output may contain `telemetry.json` or `telemetry-policy.json`, never both; AirGap output
  omits executable telemetry policy (`internal/artifacts/export.go:72-92`).
- A nil signer preserves hash-only compatibility. A configured but unreadable/malformed key fails
  before publication, while `ExportWithSigner` keeps the install-script pin and emitted signature on
  one immutable signer snapshot (`internal/bundlesig/bundlesig.go:63-108,147-158`,
  `internal/artifacts/export.go:186-229`).
- An existing export is replaced only after a complete sibling staging tree succeeds; unsafe roots,
  symlinks, special files, traversal aliases, and case-fold collisions are rejected
  (`internal/artifacts/export.go:208-284,420-508`).

## Invariants

- `BundleFiles` is the one source for written, manifest-listed, checksummed, and signed members, so
  executable/config bytes cannot fall outside the integrity set
  (`internal/artifacts/export.go:36-48,285-373`).
- `Canonicalize` sorts paths and emits lowercase SHA-256 `sha256sum` lines; `bundle.sig` signs those
  exact bytes, not compiler metadata or a map serialization
  (`internal/bundlesig/bundlesig.go:160-205`).
- `trustlist.json` and `trustlist.sig` are not bundle members: `keystone-trustlist` binds the digest
  of `checksums.sha256` and the controller appends that served authority later
  (`internal/artifacts/export.go:180-185`, `internal/api/handler_agent.go:111-132`).

## Gotchas (optional)

- `manifest.json`, `checksums.sha256`, and signing sidecars are metadata around the canonical member
  set, not self-referential checksum members; `README.txt` is a protected member
  (`internal/artifacts/export.go:36-48,341-401`).
- WireGuard configs are written `0600`, `install.sh` is `0755`, and other members are `0644`; mode
  derivation remains centralized beside membership (`internal/artifacts/export.go:113-128`).
- Agent verification requires `install.sh` and every present artifact/telemetry policy to be covered,
  and a configured signing pin turns an unsigned bundle into a hard failure
  (`internal/agent/verify.go:151-249`).
