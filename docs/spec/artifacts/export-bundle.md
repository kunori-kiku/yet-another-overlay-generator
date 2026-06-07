# Export Bundle

## Export Directory Structure

```
<output>/
├── deploy-all.sh
├── deploy-all.ps1
├── <node-name>/
│   ├── wireguard/
│   │   ├── wg-peer1.conf
│   │   ├── wg-peer2.conf
│   │   └── ...
│   ├── babel/
│   │   └── babeld.conf
│   ├── sysctl/
│   │   └── 99-overlay.conf
│   ├── install.sh
│   ├── checksums.sha256
│   ├── bundle.sig          # only when signing is enabled
│   ├── signing-pubkey.pem  # only when signing is enabled
│   ├── manifest.json
│   └── README.txt
└── ...
```

## Checksum

SHA-256 of the string representation of the compiled topology, truncated to 16 hex characters.
Written to manifest and verified by install scripts.

Per-node `checksums.sha256` covers the rendered wireguard/babel/sysctl config files **and
`install.sh` itself** (D24, Plan 5 / PR #7) — the bytes checksummed are identical for client and
non-client bundles; `manifest.json` (including `compiled_at`) is written separately and is not
part of the checksum set.

### Canonical `checksums.sha256` (Phase 0)

`checksums.sha256` is now **canonical, sorted, and deterministic**. Its content is produced by
`internal/bundlesig.Canonicalize(bundleFiles)`: one `sha256sum`-format line per file
(`<64-hex-lowercase-sha256><two spaces><path>`), **sorted by path in byte order**, LF-only, with a
**trailing newline**. The same file set always yields the same bytes regardless of map-iteration
order. The format is unchanged from `sha256sum`'s perspective, so `sha256sum -c checksums.sha256`
still consumes it; it is simply now stable instead of nondeterministically ordered. This canonical
byte string is the exact payload that gets signed (see below).

## Signed bundles (opt-in)

Signing is **opt-in** and off by default: with no signing key configured, bundles are hash-only and
byte-identical to pre-Phase-0 output (apart from `checksums.sha256` now being sorted + trailing-LF).
Signing is enabled by setting the **`YAOG_BUNDLE_SIGNING_KEY`** environment variable to the path of
an Ed25519 PKCS#8 PEM private key at export time. When enabled, each per-node bundle additionally
gets:

- **`bundle.sig`** — `base64` of the raw 64-byte Ed25519 detached signature over the canonical
  `checksums.sha256` bytes.
- **`signing-pubkey.pem`** — the PKIX/SubjectPublicKeyInfo PEM public key, for `openssl`-based
  verification (the same key is also embedded into `install.sh` as a Go-emitted constant).

The signed object is the canonical `checksums.sha256`, **never** the compiler's manifest `checksum`
(`computeChecksum`, a truncated non-canonical `%v` hash). The full contract — canonical
serialization, Ed25519 primitives, opt-in env var, the out-of-band-pin limitation, and the
install-time verify ordering — lives in
[../controller/signing.md](../controller/signing.md).
