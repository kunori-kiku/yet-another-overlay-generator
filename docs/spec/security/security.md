# Security Considerations

- **Key Management**: WireGuard keys are **persistent across recompiles** (invariant I5 in
  [../compiler/allocation-stability.md](../compiler/allocation-stability.md)). Under the stateless
  compiler this REQUIRES the **private** key to round-trip through the topology JSON: a node that
  persisted only its public key could not re-render its own `Interface PrivateKey` on the next
  compile. `generateKeys` therefore branches on the state of the node's two key fields:
  - **private key present** (regardless of `fixed_private_key`): the compiler parses it, derives the
    public key, reuses the pair, and writes the derived public key back (healing a stale/missing
    public key);
  - **public key present but private key absent**: a **hard error** — the operator must paste the
    live private key (from the host's `/etc/wireguard`) or clear both key fields to rotate;
  - **both absent** (new node): a fresh pair is generated and **both** keys are written back so they
    persist and the next compile reuses them.

  **Rotation is explicit only** — clearing both key fields (regenerate) or pasting a different
  private key — never a side effect of an unrelated edit. See
  [../data-model/node.md](../data-model/node.md).

  **Secret material — explicit.** Because the private key round-trips, the **topology JSON and the
  browser's localStorage now carry WireGuard private keys** (this generalizes the trust surface the
  `fixed_private_key` paste path already accepted). Both MUST be treated as **secret material**:
  least exposure, never echoed to logs or chat, transmitted only over the encrypted channel that
  carries the topology, and stored only on trusted operator machines. Exporting or sharing a
  topology file shares live node credentials.
  > **Compliance:** `generateKeys` previously rotated the key of every non-fixed node on every
  > compile and blanked the node's stored key (`internal/api/handler.go:308-314`). Closed by the
  > sticky-pin allocation work: keys now round-trip and are reused.

  **Parallel links share node keys.** Parallel tunnels between the same host pair
  ([../data-model/edge.md](../data-model/edge.md) §Parallel links) reuse the two nodes' existing
  keypairs on every link. This is sound: each link is a separate WireGuard device with its own
  UDP socket and listen port on both ends, so sessions cannot cross-talk; the known shared-key
  hazards apply to duplicate keys *within one device's peer table*, not across devices
  (per-interface keys are upstream best practice, not a requirement). **Per-edge keypairs are a
  documented escape hatch, not implemented** — if parallel-link handshakes ever misbehave in the
  field, introducing optional per-edge keys is the designed fallback.
- **mimic transport is shaping, not security**: a `transport: "tcp"` edge wraps the link with mimic
  (eBPF UDP→fake-TCP) to traverse UDP-hostile networks. mimic is **keyless** — it provides no
  encryption, authentication, or confidentiality, and adds **no secret material** to the topology;
  WireGuard remains the sole source of crypto and the only secret (its keys). mimic is **not** a
  censorship/DPI-circumvention mechanism. See [../artifacts/mimic.md](../artifacts/mimic.md).
- **Integrity & Authenticity (signed bundles)**: Install scripts verify `checksums.sha256`
  (SHA-256) before deploying configs — this is **integrity / tamper-detection**: any changed file no
  longer matches its recorded digest. Phase 0 adds optional **authenticity** on top.
  - `checksums.sha256` is now a **canonical, sorted, deterministic** serialization
    (`internal/bundlesig.Canonicalize`): one `sha256sum`-format line per file, sorted by path,
    LF-only, trailing newline. It covers every shipped file including `install.sh`.
  - **Signing is opt-in** via the `YAOG_BUNDLE_SIGNING_KEY` environment variable (path to an
    Ed25519 PKCS#8 PEM private key) at export time. When set, each per-node bundle is shipped with a
    detached **Ed25519 signature** (`bundle.sig`, base64) over the canonical `checksums.sha256` plus
    the verifying public key (`signing-pubkey.pem`, also embedded in `install.sh`). The signed object
    is the canonical checksum list, **never** the manifest's truncated `computeChecksum`. When the
    key is unset, bundles are hash-only and byte-identical to before (back-compat / air-gap path
    unchanged).
  - At install, when `bundle.sig` is present, `install.sh` **verifies the Ed25519 signature before**
    running `sha256sum -c`, and **fails loudly** (nonzero exit) if `openssl` is missing or lacks
    Ed25519 — it never silently downgrades a signed bundle to hash-only.
  - **Out-of-band-pin caveat (honest limitation):** Phase 0 authenticity is only as strong as the
    operator's trust in the verifying key, and Phase 0 ships that key *inside the bundle*. Against a
    bundle from an **untrusted source**, an attacker who rewrites the bundle can swap in their own
    pubkey and re-sign — so the signature then proves only internal consistency, not provenance. It
    is a genuine authenticity anchor only when the verifying key is **pinned out of band** (e.g. an
    operator-built air-gapped bundle whose key the operator configured themselves, or — later — a
    trust anchor delivered with the agent at install time in Phase 1b/3). See
    [../controller/signing.md](../controller/signing.md).
- **File Permissions**: WireGuard configs are written with `0600` permissions.
- **Privilege Escalation**: Install scripts require root and verify with `id -u` check.
- **Transport**: The API server has no built-in TLS — should be reverse-proxied in production.
