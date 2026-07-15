# Artifacts and signing

<!-- last-verified: 2026-07-15 -->

## Responsibility

Define one canonical per-node bundle member set, materialize it on disk for CLI/controller callers,
produce deterministic checksum bytes, and optionally attach an Ed25519 signature over those exact
bytes. The exporter is a presentation sink; compilation and rendering happen earlier through
`internal/localcompile`.

## Files

- `internal/artifacts/export.go:23-60` defines `BundleFiles`, the single member-set constructor
  shared by disk export and the in-memory localcompile contract.
- `internal/artifacts/export.go:63-77` maps member paths to file modes.
- `internal/artifacts/export.go` renders node directories, checksum/signature metadata, manifests,
  README files, and project-level deploy scripts into a fresh tree, then publishes that exact tree.
- `internal/bundlesig/bundlesig.go:28-205` owns signing environment names, PKCS#8/PKIX parsing,
  the `ConfigSigner` seam, canonicalization, and Ed25519 sign/verify.
- `internal/localcompile/compile.go:109-173` reshapes rendered results into the in-memory artifact
  contract using the same `BundleFiles` and checksum canonicalizer.
- `internal/agent/verify.go:126-231` is the node-side fail-closed bundle verification gate.

## Canonical bundle members

`BundleFiles(result,nodeID)` returns only deployable, checksummed members:

- every `wireguard/<interface>.conf` belonging to the node;
- `babel/babeld.conf` when the node has Babel output;
- `sysctl/99-overlay.conf`;
- `install.sh`;
- `artifacts.json` only when release/mimic configuration produced non-empty content; and
- `README.txt` with custody-aware operator instructions.

That map is the source for all four views of membership: files written, paths listed in
`manifest.json`, paths hashed into `checksums.sha256`, and bytes authenticated by `bundle.sig`.
This prevents an executable/config member from being written outside the integrity set or listed
without being shipped (`internal/artifacts/export.go:141-193`). Paths are sorted before writing and
listing.

Member modes are derived centrally: WireGuard configs are `0600`, `install.sh` is `0755`, and the
remaining members are `0644`. Canonical bundle paths are validated before publication: they must be
relative, clean slash-separated paths without aliases, control characters, or case-fold collisions.
Export directories use portable, case-fold-unique node IDs only. The complete export is built in a
fresh sibling tree and replaces only a real directory destination; symlink destinations are rejected,
late failures leave the prior tree unchanged, and a successful re-export removes every stale node,
member, and signing sidecar.

## Checksums and signatures

`bundlesig.Canonicalize` sorts member paths and emits one LF-terminated sha256sum line per member:

```text
<lowercase SHA-256><two spaces><relative path>\n
```

The returned bytes are written verbatim as `checksums.sha256`. Signing, when enabled, is Ed25519
over those exact bytes—not the compiler manifest checksum and not a map serialization
(`internal/bundlesig/bundlesig.go:160-205`).

`YAOG_BUNDLE_SIGNING_KEY` points to an Ed25519 PKCS#8 PEM private key. Unset means a backward-
compatible hash-only bundle. A malformed configured key fails before export begins. A signed node
directory adds:

- `bundle.sig` — standard-base64 raw Ed25519 signature plus a trailing newline;
- `signing-pubkey.pem` — the corresponding PKIX public-key PEM.

Those two files are listed in `manifest.json` but are not checksum members: they authenticate the
checksum set and cannot self-reference. `ConfigSigner` is the injection seam for another signing
backend; the current in-process implementation and `bundlesig` package remain standard-library
only (`internal/bundlesig/bundlesig.go:95-158`).

## Directory metadata

Each node directory also contains two integrity metadata files:

- `checksums.sha256` — canonical member hashes;
- `manifest.json` — node/project metadata, architecture, compiler checksum, compile time, and the
  member path list, plus signature/public-key paths when signed;

`checksums.sha256` and `manifest.json` are not members of the checksum set and do not appear in the
manifest's `files` array. The checksum file cannot hash itself, and `manifest.json` carries volatile
`compiled_at`. `README.txt`, by contrast, is a canonical member: its instructions are listed in the
manifest, hashed in `checksums.sha256`, and authenticated by `bundle.sig` when signing is enabled.
Consumers must not equate “all files in the directory” with “manifest/checksum members.”

Project-level `deploy-all.sh`/`deploy-all.ps1` outputs are written at the export root, outside every
node bundle and its integrity set.

## Callers

The offline compiler runs the canonical localcompile path in `AirGap` custody, then calls
`artifacts.Export` to write its output (`cmd/compiler/main.go:70-119`). Controller deploy preview
and stage call the same exporter in a temporary directory so the store receives exactly the disk
bundle shape (`internal/controller/compile_preview.go:81-108` and
`internal/controller/compile_stage.go:187-255`).

The browser/WASM path does not write a filesystem tree. `localcompile.Compile` calls
`ArtifactsFromResult`, which uses the same `BundleFiles` and `Canonicalize` functions to expose
per-node files, checksums, and optional signatures in memory
(`internal/localcompile/compile.go:15-34,109-173`). The localcompile golden corpus pins that shape.

There is no anonymous air-gap HTTP export API and no self-extracting installer endpoint. Local
browser export is Go/WASM; the controller surface stages authenticated per-node bundles; the
standalone CLI writes directories.

## Verification and trust tiers

Before running `install.sh`, the agent requires `checksums.sha256`, verifies a present signature,
then verifies every listed file hash. A configured pinned bundle public key overrides the key
carried in the bundle and makes an unsigned bundle a hard failure. With neither pin nor signature,
hash-only bundles remain accepted for compatibility (`internal/agent/verify.go:126-215`).
`install.sh` and, when present, `artifacts.json` receive explicit coverage checks so an attacker
cannot add or omit them outside the listed set (`internal/agent/verify.go:207-229`).

The controller adds two related but distinct controls:

- A persisted bundle-signing anchor detects a later missing or unexpectedly changed
  `YAOG_BUNDLE_SIGNING_KEY`; `YAOG_BUNDLE_SIGNING_KEY_ROTATE` is the explicit one-deploy re-pin
  escape hatch (`internal/controller/keystone.go:67-123`).
- Keystone's off-host-signed trust list binds `hex(sha256(checksums.sha256))` to a node identity and
  WireGuard public key. `trustlist.json` and `trustlist.sig` therefore cannot be exported inside the
  checksum set they bind; the controller appends them when serving config.

Tier-1 bundle signing proves the checksum set was signed by the configured bundle key. Keystone is
the separate off-host membership/provenance authority. Neither replaces per-file checksum
verification.

## Integration invariant

The install-script renderer embeds a signing public key only when its `localcompile.CompileRequest`
receives a `ConfigSigner`; export must use that same signer when it writes `bundle.sig`. The live
disk-writing paths enforce this rather than relying on two environment reads:

- controller stage and deploy preview resolve one signer and pass it through
  `CompileSubgraphWithSigner` and `artifacts.ExportWithSigner`; stage also checks the persisted
  signing anchor with that exact object;
- the standalone compiler resolves one signer and passes it to both `localcompile.CompileResult`
  and `artifacts.ExportWithSigner`;
- `artifacts.Export` remains an environment-loading convenience for callers that do not render
  with a pre-resolved signer.

This keeps direct/manual `install.sh` verification aligned with the signature that managed agents
pre-verify. Controller empty-stage cleanup is projected before signer resolution, so a malformed
configured key cannot prevent stale staged bundles from being purged. Integration tests pin both
the signer alignment and that cleanup ordering.

Other invariants:

- Hashes describe the exact in-memory strings written as members; `BundleFiles` keeps the two views
  single-sourced.
- Signed and unsigned exports never mix within one run because the signer is resolved before the
  node loop.
- Trust-list files remain outside bundle export and outside `checksums.sha256` by construction.
