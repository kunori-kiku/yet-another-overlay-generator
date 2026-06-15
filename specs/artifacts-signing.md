# Artifacts & Signing

<!-- last-verified: 2026-06-15 -->
<!-- 2026-06-15 (extensible-i18n closeout): key-gen/export errors coded via internal/apierr; install scripts + self-extracting installer + CLI output Englishized; no artifact/signing-format change. -->

## Responsibility
Materialize each compiled node's config bundle as an on-disk directory (configs + `manifest.json` + canonical `checksums.sha256`) and, when a signing key is configured, attach a detached Ed25519 signature (`bundle.sig`) plus the verifying public key over that canonical checksum content.

## Files
- `internal/artifacts/export.go:40-253` — `Export(result *compiler.CompileResult, outputDir string) (*ExportResult, error)`: per-node dirs, file writes, checksums, manifest, optional signing, project-level deploy scripts.
- `internal/artifacts/export.go:257-274` — `validateSafeName`: rejects node names that would enable path traversal (empty, `.`/`..`, separators, absolute).
- `internal/bundlesig/bundlesig.go:1-221` — stdlib-only leaf package: `YAOG_BUNDLE_SIGNING_KEY` loading, `ConfigSigner` seam, `Canonicalize`, `Sign`/`Verify`, PEM marshal/parse.

## Inputs
- `*compiler.CompileResult` from the render pipeline (see specs/compiler-allocation.md, specs/render-keys.md): `WireGuardConfigs` keyed `"nodeID:interfaceName"` (internal/artifacts/export.go:84-97), `BabelConfigs`, `SysctlConfigs`, `InstallScripts` per node ID, `DeployScripts` per filename, and `Manifest` metadata (internal/artifacts/export.go:202-215).
- Env var `YAOG_BUNDLE_SIGNING_KEY` (internal/bundlesig/bundlesig.go:36): path to an Ed25519 private key in PKCS#8 PEM (`openssl genpkey -algorithm ed25519`); unset/empty means signing is off.
- Callers of `Export`: the CLI (cmd/compiler/main.go:85), the air-gap export endpoint (internal/api/handler.go:211, see specs/airgap-api.md), and controller staging (internal/controller/compile.go:207, see specs/controller-stage-promote.md).

## Outputs
- Per-node directory `<outputDir>/<nodeName>/` containing `wireguard/<iface>.conf` (0600), `babel/babeld.conf` (non-client only), `sysctl/99-overlay.conf`, `install.sh` (0755), `checksums.sha256`, `manifest.json`, `README.txt` (internal/artifacts/export.go:64-235); plus project-level deploy scripts at the export root (internal/artifacts/export.go:241-250). Layout documented in docs/spec/artifacts/export-bundle.md.
- When signing is on: `bundle.sig` (base64 of the raw 64-byte Ed25519 signature over the exact `checksums.sha256` bytes) and `signing-pubkey.pem` (PKIX PEM) in each node dir (internal/artifacts/export.go:180-195).
- `manifest.json` fields: node identity/role/domain, project id/name/version, `compiled_at`, compiler checksum, `architecture` (`per-peer-interface` vs `single-interface` for clients), and the `files` list (internal/artifacts/export.go:197-215).
- Downstream consumers: controller staging reads each node dir back into a file map and binds the SHA-256 of `checksums.sha256` into the keystone manifest (internal/controller/compile.go:211-246, see specs/controller-store.md, specs/keystone-trustlist.md); the agent verifies `bundle.sig` with `bundlesig.Verify` against the pinned key (internal/agent/verify.go:148, see specs/agent.md); the air-gap API zips the dirs and signs self-extracting installer payloads through the same `ConfigSigner` seam (internal/api/handler.go:348, 457).
- `bundlesig` primitives are also reused by the trust-list signer/verifier (internal/trustlist/ed25519.go:48-52, internal/trustlist/verify.go:82).

Load-bearing signatures:
- `bundlesig.Canonicalize(files map[string]string) []byte` — sorted-by-path `sha256sum -c` lines, LF-terminated, deterministic regardless of map order (internal/bundlesig/bundlesig.go:155-172).
- `bundlesig.LoadConfigSignerFromEnv() (ConfigSigner, error)` — `(nil, nil)` when unset; error on unreadable/unparsable key (internal/bundlesig/bundlesig.go:132-141).
- `ConfigSigner { Sign(message []byte) ([]byte, error); PublicKeyPEM() []byte }` — the KMS/HSM swap seam (internal/bundlesig/bundlesig.go:99-105); docs/spec/controller/signing.md covers the tiering.

## Decision points (if any)
- **Sign or hash-only:** signer resolved once before any node dir is touched; `nil` signer keeps the export byte-for-byte the pre-signing output, a malformed key fails the whole export early (internal/artifacts/export.go:52-56, internal/bundlesig/bundlesig.go:59-76).
- **Client vs non-client:** clients get no `babel/` dir and the `single-interface` architecture label (internal/artifacts/export.go:65-74, 197-200).
- **What is checksummed:** only the rendered artifacts — WireGuard confs, babeld.conf, sysctl, `install.sh`. `manifest.json` is excluded (carries `compiled_at` timestamps); `bundle.sig`/`signing-pubkey.pem` are excluded by construction (they sign/anchor the set, so cannot be members) (internal/artifacts/export.go:128-156).
- **Trust-list files are never exported here:** the keystone manifest binds each node's `checksums.sha256` digest, so `trustlist.json`/`trustlist.sig` cannot live inside that checksum set; the controller appends them to the served file map at `/config` time (internal/artifacts/export.go:33-39, internal/controller/compile.go:198-201; see specs/controller-agent-api.md).

## Invariants
- Signatures are produced ONLY over `Canonicalize` output — never over the compiler's non-canonical `fmt.Sprintf("%v")` checksum (internal/bundlesig/bundlesig.go:6-13). `checksums.sha256` content and the signed message are the identical bytes (internal/artifacts/export.go:158-188).
- Integrity anchors in Go-emitted constants, not in files the payload carries: `install.sh` is inside the checksummed set, and the verifying pubkey is pinned into `install.sh` at render time, not read from the bundle (PRINCIPLES.md "Generated scripts run as root on fleets"; internal/artifacts/export.go:130-136).
- `bundlesig` stays a stdlib-only leaf package; KMS/HSM clients implement `ConfigSigner` in their own packages (internal/bundlesig/bundlesig.go:1-14, 84-98; PRINCIPLES.md "Minimal dependencies" scoped exception).

## Gotchas (optional)
- The pubkey pinning into `install.sh` does NOT happen in this subsystem: `render.All` reads the same env var independently and passes the PEM to `RenderInstallScriptSigned` / `RenderClientInstallScriptSigned` (internal/render/render.go:185-192, internal/renderer/script.go:774, 1316; see specs/render-keys.md). Export and render must therefore see the same `YAOG_BUNDLE_SIGNING_KEY` or the shipped `signing-pubkey.pem` and the script-pinned key diverge.
- `bundle.sig` and `signing-pubkey.pem` ARE listed in `manifest.json`'s `files` array but are NOT in `checksums.sha256` (internal/artifacts/export.go:194) — tooling that equates the two lists will false-alarm on signed bundles.
- The checksummed file set is rebuilt as a second in-memory map (internal/artifacts/export.go:140-156) mirroring the write loop above it (internal/artifacts/export.go:84-121); hashes describe the in-memory strings, not re-read disk bytes, and the two blocks must stay in lockstep. Tier-1 signing with an on-controller key proves internal consistency, not provenance — the off-host keystone manifest is the provenance layer (docs/spec/security/security.md:54-77, see specs/controller-stage-promote.md).
