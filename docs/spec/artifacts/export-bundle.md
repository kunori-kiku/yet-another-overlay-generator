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
