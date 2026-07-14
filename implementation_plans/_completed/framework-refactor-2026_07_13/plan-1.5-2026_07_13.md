# plan-1.5 — single-source the export bundle file-set (custody)

**Goal:** Make `internal/artifacts/export.go` derive the WRITTEN files, the LISTED manifest, the
CHECKSUMMED set, and the SIGNED coverage all from ONE source (`BundleFiles`), so a bundle member can
never be **written-but-unlisted** (shipped UNSIGNED/UNCHECKSUMMED — a tamper surface) nor
**listed-but-unwritten** (fails `sha256sum -c` on the node).

**Prerequisites:** plan-1. **Isolated from plan-1** (the review-added custody item) because it is a
distinct concern (bundle file-set robustness, not the heal/purity/port contract hardening) and it has a
rendered-output nuance (`manifest.json` file-order) that deserves its own reviewed byte-diff.

## Design (assessment recap)
`export.go` encodes the per-node bundle set THREE times: `BundleFiles` (the canonical `path→content`
map, shared with `localcompile.ArtifactsFromResult`); the Export write-loop, which INDEPENDENTLY
re-parses `result.WireGuardConfigs` etc. and `os.WriteFile`s each with a per-file mode (wg `0600`, babel
/sysctl/artifacts `0644`, install.sh `0755`); and `allFiles`, a THIRD hand-built list feeding
`manifest.json`'s `files` field. The **checksums** already single-source from `BundleFiles`
(`bundlesig.Canonicalize`); the write-loop + `allFiles` are the hand-parallel drift risk.

## Changes (`internal/artifacts/export.go`)
- Keep `BundleFiles` as the single source (unchanged signature — no blast radius to `localcompile`).
- Add `bundleFileMode(rel string) os.FileMode` — the ONE place a member's mode is derived from its slash
  path (`install.sh`→`0755`, `wireguard/*`→`0600`, else `0644`; reproduces every current mode).
- Rewrite the write-loop to iterate `BundleFiles(result, node.ID)`: for each member, `MkdirAll` its
  parent and `os.WriteFile` with `bundleFileMode` — so WRITTEN == the checksummed set.
- Derive `allFiles` = the sorted `BundleFiles` keys (+ `bundle.sig`/`signing-pubkey.pem` when signing) —
  so LISTED == the checksummed set. This makes `manifest.json`'s `files` **deterministically sorted**
  (today it is non-deterministic wg-map-order — a latent non-reproducibility this fixes).

## Verify + branch
Full Go suite (default + airgap, `-race`) + gofmt. **The CHECKSUMMED bundle is byte-identical** — the
localcompile + conformance goldens must be UNCHANGED (verified pre-analysis: `manifest.json` is excluded
from the checksum set AND the golden corpus, and no test pins its `files` order). Branch
`refactor/plan-1.5-export-single-source`.

## Tests produced
- A unit test asserting `written == BundleFiles keys == manifest files == checksummed set` for a
  representative bundle (peer + client) — **perpetual** (guards the custody single-source). Retirement:
  never.

## Invariants at risk
- **[5] signed-update custody:** the single `BundleFileSet` is the load-bearing fix — written == listed
  == signed == checksummed must hold structurally; do NOT reintroduce a second list.
- **[2] deployable configs:** the checksummed set stays byte-identical (goldens byte-verified); the only
  output change is `manifest.json`'s (non-checksummed, informational) `files` becoming sorted.

## Stop-loss
If regenerating the goldens shows ANY change to a CHECKSUMMED file (not just `manifest.json`), the
iteration order or mode derivation diverged — STOP; the checksummed set must be byte-identical.

## Out of scope
Any change to `BundleFiles`' `map[string]string` contract (keep it — a richer typed set is unnecessary;
the mode derives from the path); the trust-list / manifest signing model (unchanged).
