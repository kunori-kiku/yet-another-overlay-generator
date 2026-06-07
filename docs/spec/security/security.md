# Security Considerations

- **Key Management**: WireGuard keys are **persistent by public-key presence**. A node whose
  `wireguard_public_key` is non-empty is key-fixed: the compiler reuses its existing key pair and
  MUST NOT generate a fresh one. A node with no public key is new: the compiler generates a fresh
  key pair and persists the resulting **public** key back onto the node so the next compile reuses
  it. The **private** key is not persisted to the topology JSON unless the operator deliberately
  supplies it via `fixed_private_key` + `wireguard_private_key` (the paste path used for migration
  and for live-key capture). Re-randomization MUST occur only for genuinely new nodes or an
  explicit operator-initiated rotation (clearing the key fields), never as a side effect of an
  unrelated edit — this is invariant I5 in
  [../compiler/allocation-stability.md](../compiler/allocation-stability.md) and the key-persistence
  semantics in [../data-model/node.md](../data-model/node.md). Any pasted private key is a live
  credential and MUST be handled accordingly (least exposure, never echoed).
  > **Compliance:** `generateKeys` currently rotates the key of every non-fixed node on every
  > compile and blanks the node's stored key (`internal/api/handler.go:308-314`). Closed by the
  > sticky-pin allocation work.
- **Checksum Verification**: Install scripts verify `checksums.sha256` (SHA-256) before deploying
  configs.
- **File Permissions**: WireGuard configs are written with `0600` permissions.
- **Privilege Escalation**: Install scripts require root and verify with `id -u` check.
- **Transport**: The API server has no built-in TLS — should be reverse-proxied in production.
