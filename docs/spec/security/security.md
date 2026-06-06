# Security Considerations

- **Key Management**: WireGuard private keys are generated fresh per compilation unless
  `fixed_private_key` is set. Non-fixed keys are NOT persisted to the topology JSON (cleared
  after compile).
- **Checksum Verification**: Install scripts verify `checksums.sha256` (SHA-256) before deploying
  configs.
- **File Permissions**: WireGuard configs are written with `0600` permissions.
- **Privilege Escalation**: Install scripts require root and verify with `id -u` check.
- **Transport**: The API server has no built-in TLS — should be reverse-proxied in production.
