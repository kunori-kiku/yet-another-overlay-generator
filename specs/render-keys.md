# Render & Key Custody

<!-- last-verified: 2026-06-15 -->
<!-- 2026-06-15 (extensible-i18n closeout): key-gen/export errors coded via internal/apierr; install scripts + self-extracting installer + CLI output Englishized; no artifact/signing-format change. -->

## Responsibility
Prepare each node's WireGuard key material under the selected custody model (AirGap vs AgentHeld), then render a compile result into every deployable artifact: per-peer/client WireGuard configs, Babel configs, sysctl configs, per-node install scripts (with optional signature-verify and key-splice blocks), and deploy-all scripts.

## Files
- `internal/render/render.go:1-231` — shared "key prep + full render" layer for all three entry points; owns `KeyCustody`, `PrivateKeyPlaceholder`, `GenerateKeys`, and `All`.
- `internal/renderer/wireguard.go:1-222` — Go-template rendering of per-peer WG confs (`wireguard.go:52-86`) and the client single-`wg0` conf (`wireguard.go:89-111`); shared `renderTemplate` helper (`wireguard.go:210-222`).
- `internal/renderer/babel.go:1-225` — babeld.conf rendering: role-preset timers, per-tunnel interfaces, and redistribute-rule classification (`babel.go:88-174`).
- `internal/renderer/script.go:1-1353` — install.sh templates for per-peer nodes (`script.go:76-734`) and clients (`script.go:926-1296`), incl. signature-verify, mimic provisioning, SNAT rules, and the AgentHeld key-splice block.

## Inputs
- `*model.Topology` + custody mode → `GenerateKeys(topo, custody) (map[string]compiler.KeyPair, error)` (`internal/render/render.go:70`). Callers: air-gap CLI `cmd/compiler/main.go:47` and HTTP API `internal/api/handler.go:138,186,251` pass `AirGap` (see specs/airgap-api.md); the controller stage pipeline passes `AgentHeld` (`internal/controller/compile.go:180`, see specs/controller-stage-promote.md).
- `*compiler.CompileResult` (PeerMap, ClientConfigs, Topology) from `compiler.Compile` plus the `keys` map → `All(result, keys) error` (`internal/render/render.go:147`); `compiler.KeyPair` is `{PrivateKey, PublicKey string}` (`internal/compiler/peers.go:64-67`). See specs/compiler-allocation.md.
- Optional Ed25519 signer from env `YAOG_BUNDLE_SIGNING_KEY` via `bundlesig.LoadConfigSignerFromEnv` (`internal/render/render.go:185-192`, `internal/bundlesig/bundlesig.go:36`). See specs/artifacts-signing.md.

## Outputs
- `GenerateKeys` writes resolved keys back onto `topo.Nodes` (`wireguard_private_key`/`wireguard_public_key`, `internal/model/topology.go:89-90`) so they persist in the topology JSON; under AgentHeld it instead clears the private key field (`internal/render/render.go:95-96`).
- `All` mutates the result's maps in place: `WireGuardConfigs` keyed `"nodeID:interfaceName"` (`internal/renderer/wireguard.go:202`, client entry at `internal/render/render.go:161`), `BabelConfigs`, `SysctlConfigs`, `InstallScripts`, and `DeployScripts["deploy-all.sh"/"deploy-all.ps1"]` (`internal/render/render.go:147-231`; sysctl and deploy renderers live in `internal/renderer/sysctl.go:47` and `internal/renderer/deploy.go:38`).
- Downstream: the export/signing layer packages these into per-node bundles with `checksums.sha256` + `bundle.sig` (see specs/artifacts-signing.md); the agent fetches and executes the rendered install.sh (see specs/agent.md).

## Decision points
- **Custody branch in `GenerateKeys`** (`internal/render/render.go:75-131`):
  - *AgentHeld* (`render.go:75-102`): registered public key is authoritative; a stray private key is only used to derive a missing public half, then discarded; neither key present → hard error ("agent must register a public key first", `render.go:86`). Emitted pair is always `{PrivateKeyPlaceholder, pub}`.
  - *AirGap* (`render.go:104-131`): (a) private key present → derive + write back public key (`render.go:105-114`); (b) public key only → hard error, stateless compiler cannot reconstruct it (`render.go:116-119`); (c) both empty → generate fresh pair and persist (`render.go:121-131`).
- **Per-node custody detection in `All`**: custody is inferred per node by comparing the rendered private key to `PrivateKeyPlaceholder` (`internal/render/render.go:200-201`) — no flag is threaded through; air-gap nodes carry real keys so no splice block is emitted.
- **Client vs non-client install script** (`internal/render/render.go:202-219`): clients get `RenderClientInstallScriptSigned` with their `ClientPeerInfo` (mimic on the single wg0 link); others get `RenderInstallScriptSigned(node, peers, hasBabel, pubPEM, splice, transitCIDRs...)` (`internal/renderer/script.go:774`, `script.go:1316`).
- **Signing on/off**: nil signer → empty `signingPubkeyPEM` → the `*Signed` renderers emit byte-identical output to the plain renderers (`internal/render/render.go:184-192`, `internal/renderer/script.go:765-780`); a misconfigured key fails closed (`render.go:186-188`).
- **Splice block in install.sh** (gated by `CustodySplice{Enabled, Token}`, `internal/renderer/script.go:758-763`): after each conf is copied to `/etc/wireguard`, the literal line `PrivateKey = <token>` in the COPY is replaced with the key read from `/etc/wireguard/agent.key` — grep-guarded, line-by-line rewrite (no sed/regex), hard error if `agent.key` is missing/empty (per-peer: `script.go:563-594`; client wg0: `script.go:1201-1232`).
- **Babel rendering** (`internal/renderer/babel.go`): skip entirely for clients or non-babel routing mode (`babel.go:215-225`); skip client-facing tunnels as babel interfaces (D73, `babel.go:118-123`); classify announces into `redistribute local` (self-/32 `babel.go:140-142`, domain CIDR `babel.go:148-150`, client-/32 `babel.go:167-171`) vs kernel-route `redistribute ip` (extra prefixes, 0.0.0.0/0 default, `babel.go:154-162`); rxcost = edge `LinkCost` else role-preset default (`babel.go:124-127`).
- **Transit-CIDR resolution for SNAT**: `NodeTransitCIDRs` resolves the node's domain `transit_cidr`, falling back to `10.10.0.0/24` (`internal/renderer/script.go:860-874`, default at `script.go:737`); install.sh emits one SNAT rule per distinct CIDR.

## Invariants
- **Zero-knowledge custody**: no controller-rendered bundle ever contains a parseable WireGuard private key — `GenerateKeys(AgentHeld)` never returns one, and `PrivateKeyPlaceholder` (`PRIVATEKEY_PLACEHOLDER`, `internal/render/render.go:49`) is intentionally not valid base64. Perpetual guard: `internal/render/custody_guard_test.go`; contract in `docs/spec/controller/key-custody.md`.
- **Key stability (I5)** — PRINCIPLES.md "Allocation stability": AirGap round-trips private keys through the topology JSON so recompiles reuse them; AgentHeld preserves I5 via the stable registered public key (`docs/spec/controller/key-custody.md` §"Invariant I5 under AgentHeld").
- **Air-gap output is frozen**: with signing off and splice disabled, rendered artifacts are byte-identical to the pre-signing/pre-splice output (`internal/render/render.go:184-199`, `internal/renderer/script.go:756`, pinned by `internal/renderer/script_signature_test.go` and `internal/render/custody_diff_test.go` — AgentHeld differs from AirGap only on the node's own `PrivateKey` line). Cross-cuts PRINCIPLES.md "Generated configs must be deployable" and the protected self-/32 Babel announce path (`internal/renderer/babel.go:138-142`).

## Gotchas
- The splice rewrites only the conf **copied** to `/etc/wireguard`, never the bundled file — the signed bundle stays pristine so re-runs keep passing signature/`sha256sum -c`, and each re-run re-splices deterministically (`internal/renderer/script.go:564-570`). The agent must have written `/etc/wireguard/agent.key` first (`agent keygen`, `internal/agent/keygen.go:15`; see specs/agent.md) or install.sh exits 1.
- The placeholder propagates through compile/render with zero special-casing because a node's private key appears in exactly one rendered location — its own `[Interface] PrivateKey =` line (`internal/renderer/wireguard.go:57`, `wireguard.go:94`); consequently `checksums.sha256`/`bundle.sig` legitimately differ between AirGap and AgentHeld renders of the same topology (`docs/spec/controller/key-custody.md` §"The placeholder contract").
- `GenerateKeys(AgentHeld)` clearing `node.WireGuardPrivateKey` (`internal/render/render.go:95-96`) is a render-time, in-memory strip — the controller store itself does not enforce the "public-keys-only" claim in `docs/spec/controller/persistence.md` (known doc drift; see specs/controller-store.md). Also note per-peer conf MTU comes from `peer.MTU` (mimic links are node MTU −12), not `node.MTU` (`internal/renderer/wireguard.go:146-158`).
