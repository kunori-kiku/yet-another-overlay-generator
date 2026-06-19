// index.ts — the PURE public library surface of the TypeScript local/air-gap compiler. This is the ONLY
// module plan-6 (the store rewire) imports from; everything else under frontend/src/compiler/ is
// internal. The four entry points mirror the four backend air-gap routes byte-for-byte in BEHAVIOUR and
// in RESPONSE SHAPE, so plan-6 wires them with NO shape translation:
//
//   - compile(topo, custody?): CompileResult — the full local/air-gap pipeline (the oracle output the
//     conformance harness and the export builders consume). `toCompileResponse(result)` projects it into
//     the snake_case CompileResponse the store assigns into `compileResult` (/api/compile shape).
//   - validate(topo): ValidateResponse — schema-then-semantic, returning { valid, errors, warnings }
//     exactly as /api/validate (HandleValidate). Assignable straight into the store's `validateResult`.
//   - exportArtifacts(topo): Promise<Blob> — the per-node bundle ZIP (re-exported from ./export),
//     matching the Blob the store gets from /api/export.
//   - deployScript(topo, format): string — one project-level deploy script (bash | PowerShell), matching
//     the single-script body /api/deploy-script?format=sh|ps1 returns.
//
// PURE: every entry operates on a deep-enough COPY of its input (the caller's topology is never mutated)
// and injects no store, flag, clock, or network. Store wiring, the VITE_YAOG_LOCAL_ENGINE flag, the dev
// canary, and the production cutover are plan-6 — NONE of them live here.
//
// ── The compile pipeline ──
// compile() is the TypeScript mirror of internal/compiler/compiler.go CompileAt (the air-gap / local-mode
// compile pipeline) plus the key-derivation pre-pass of internal/render/render.go GenerateKeysWith
// (AirGap custody). It takes a Topology and returns a CompileResult (the compiled topology + per-node
// PeerMap + client configs + every rendered config/script) WITHOUT touching any store, flag, or clock.
//
// Go is the authoritative oracle. compile() reproduces CompileAt's orchestration exactly:
//   validate (schema THEN semantic, MUTATING the topology copy's routing_mode/transport defaults)
//     → AllocateIPs (overlay IPs)
//     → InferCapabilitiesFromRole (per-node capability normalization)
//     → derivePeers (Pass 1 reserve-then-gap-fill + Pass 2 forward/reverse PeerInfo)
//     → DeriveClientConfigs (client wg0 configs)
//     → direction-aware write-back of the six pinned_* fields + CompiledPort, keyed by LinkKey(edge)
//     → stamp AllocSchemaVersion.
// Every allocated value (ports / transit IPs / link-locals / overlay IPs / derived public keys) is
// byte/value-identical to the Go side — a wrong value silently disagrees with the controller.
//
// The renderers (WireGuard / Babel / sysctl / install scripts / deploy scripts) and the export bundle
// are fully wired into compile() / exportArtifacts(): every rendered file is pinned byte-for-byte against
// the Go golden by the conformance harness (plan-5).

import { allocateIPs } from './allocator';
import { inferCapabilitiesFromRole } from './capabilities';
import { CompileCode, CompileError } from './errors';
import { derivePublic, parseAndNormalize } from './keygen';
import { linkKey } from './linkid';
import type {
  CompileManifest,
  CompileResult,
  Edge,
  KeyPair,
  Node,
  Topology,
} from './model';
import { deriveClientConfigs, derivePeers } from './peers';
import type { PairAllocation } from './peers';
import { renderAllBabelConfigs } from './renderers/babel';
import { renderDeployScripts } from './renderers/deploy';
import { renderAllInstallScripts } from './renderers/script';
import { renderAllSysctlConfigs } from './renderers/sysctl';
import {
  renderAllWireGuardConfigs,
  renderClientWireGuardConfig,
} from './renderers/wireguard';
import { validateSchema, validateSemantic } from './validator';
import type { ValidationError } from './validator';
import type { CompileResponse, ValidateResponse } from '../types/topology';

// AllocationSchemaVersion is the sticky-pin allocation-scheme version this build stamps onto the
// compiled topology's alloc_schema_version (invariant I10). Mirrors compiler.AllocationSchemaVersion
// (compiler.go:23) = model.CurrentAllocSchemaVersion (model/topology.go:11) = 1.
const AllocationSchemaVersion = 1;

// KeyCustody selects the WireGuard key-custody model, mirroring render.KeyCustody (render.go:38-49).
// The local/air-gap browser path is AirGap (the default): private keys round-trip through the topology
// JSON (localStorage), so each node's public key is derived from its private key. AgentHeld is the
// controller's zero-knowledge custody where the registered public key is authoritative and no real
// private key is ever emitted; it is supported here only so the conformance harness can pin the
// custody-independent allocations of its AgentHeld fixtures (the allocations are identical across
// custody modes — only the source of the public key differs).
export type KeyCustody = 'airgap' | 'agentheld';

// PrivateKeyPlaceholder is the sentinel emitted on a node's [Interface] PrivateKey line under AgentHeld
// custody. Mirrors render.PrivateKeyPlaceholder (render.go:56). Intentionally NOT valid base64.
const PrivateKeyPlaceholder = 'PRIVATEKEY_PLACEHOLDER';

// copyTopology returns a deep-enough copy of the input topology so compile() never mutates the caller's
// object: the node and edge slices are duplicated and each element shallow-cloned (so the per-element
// mutations — capability inference onto nodes, the six pinned_* + compiled_port onto edges, the keygen
// write-back onto nodes — never alias the caller's structs). project/domains/route_policies are carried
// by reference (compile reads them but does not mutate them, matching Go's CompileAt which reuses
// topo.Project / topo.Domains / topo.RoutePolicies in the compiled topology verbatim).
function copyTopology(topo: Topology): Topology {
  return {
    project: topo.project,
    domains: topo.domains,
    nodes: topo.nodes.map((n) => ({ ...n, capabilities: { ...n.capabilities } })),
    edges: topo.edges.map((e) => ({ ...e })),
    route_policies: topo.route_policies,
    alloc_schema_version: topo.alloc_schema_version,
  };
}

// generateKeys builds the per-node KeyPair map under the selected custody model, mirroring
// render.GenerateKeysWith (render.go:154-231). The keygen seam (keygen.ts) reproduces wgtypes'
// ParseAndNormalize / DerivePublic byte-for-byte, pinned by the X25519 KAT.
//
// It MUTATES the supplied nodes in place (they are the compile copy's nodes — never the caller's), so
// the compiled topology carries the canonicalized / placeholder keys, matching Go where GenerateKeys
// rewrites the node's key fields before the topology is compiled.
//
//   - AirGap (the local-mode default): private keys round-trip through the topology JSON.
//     (a) private key present: normalize it, derive the public key, reuse + write both back;
//     (b) public key but no private key: hard error (the stateless compiler cannot reconstruct it);
//     (c) both empty: hard error — the pure library does not consume a CSPRNG on the conformance path
//         (fixtures always carry keys); the store-mode caller (plan-6) generates + persists a node's
//         keys before compile, exactly as localStorage already does today.
//   - AgentHeld (controller zero-knowledge custody): the registered public key is authoritative. When
//     present it is used verbatim; when absent, the public half is derived from a stray private key and
//     that private key is discarded (hard error if neither is present). The private half becomes
//     PrivateKeyPlaceholder and the node's private key is cleared, so the topology never carries one.
function generateKeys(
  nodes: Node[],
  custody: KeyCustody,
): Map<string, KeyPair> {
  const keys = new Map<string, KeyPair>();
  for (const node of nodes) {
    if (custody === 'agentheld') {
      let pub = node.wireguard_public_key ?? '';
      if (pub === '') {
        if (!node.wireguard_private_key) {
          throw new CompileError(CompileCode.KeygenMissingPubkey, {
            node: node.id,
          });
        }
        try {
          pub = derivePublic(node.wireguard_private_key);
        } catch (e) {
          throw new CompileError(CompileCode.KeygenPrivkeyParse, {
            node: node.id,
            detail: e instanceof Error ? e.message : String(e),
          });
        }
      }
      node.wireguard_public_key = pub;
      node.wireguard_private_key = '';
      keys.set(node.id, {
        privateKey: PrivateKeyPlaceholder,
        publicKey: pub,
      });
      continue;
    }

    // AirGap.
    if (node.wireguard_private_key) {
      // Case (a): the private key is present. Normalize it, derive the public key, reuse the whole pair,
      // and write the derived public key back (fixing a missing or stale public key).
      let normalized: string;
      let pub: string;
      try {
        normalized = parseAndNormalize(node.wireguard_private_key);
        pub = derivePublic(node.wireguard_private_key);
      } catch (e) {
        throw new CompileError(CompileCode.KeygenPrivkeyParse, {
          node: node.id,
          detail: e instanceof Error ? e.message : String(e),
        });
      }
      node.wireguard_private_key = normalized;
      node.wireguard_public_key = pub;
    } else if (node.wireguard_public_key) {
      // Case (b): a public key but no private key — a hard error (mirrors apierr CodeKeygenPinnedNoPrivkey).
      throw new CompileError(CompileCode.KeygenPinnedNoPrivkey, {
        node: node.id,
      });
    } else {
      // Case (c): both empty — see the doc comment; the pure library fails loudly rather than rendering
      // an empty key.
      throw new CompileError(CompileCode.KeygenPinnedNoPrivkey, {
        node: node.id,
      });
    }

    keys.set(node.id, {
      privateKey: node.wireguard_private_key ?? '',
      publicKey: node.wireguard_public_key ?? '',
    });
  }
  return keys;
}

// writeBackPins writes each enabled edge's allocated resources back into its six pinned_* fields
// (oriented by the edge's from/to direction) and the read-only compiled_port. Mirrors the write-back
// loop in compiler.go CompileAt (compiler.go:193-243):
//   - look up the edge's PairAllocation by linkid.LinkKey(edge);
//   - isForward = alloc.fromNodeID === edge.from_node_id; forward edges take values verbatim, reversed
//     edges mirror them;
//   - compiled_port is written only for edges with endpoint_host: an explicit endpoint_port override
//     wins, otherwise the peer (toNode) interface's allocated listen port.
function writeBackPins(
  edges: Edge[],
  allocations: Map<string, PairAllocation>,
): void {
  for (const edge of edges) {
    if (!edge.is_enabled) {
      continue;
    }

    const alloc = allocations.get(linkKey(edge));
    if (alloc === undefined) {
      continue;
    }

    const isForward = alloc.fromNodeID === edge.from_node_id;
    if (isForward) {
      edge.pinned_from_port = alloc.fromPort;
      edge.pinned_to_port = alloc.toPort;
      edge.pinned_from_transit_ip = alloc.localTransit;
      edge.pinned_to_transit_ip = alloc.remoteTransit;
      edge.pinned_from_link_local = alloc.localLL;
      edge.pinned_to_link_local = alloc.remoteLL;
    } else {
      edge.pinned_from_port = alloc.toPort;
      edge.pinned_to_port = alloc.fromPort;
      edge.pinned_from_transit_ip = alloc.remoteTransit;
      edge.pinned_to_transit_ip = alloc.localTransit;
      edge.pinned_from_link_local = alloc.remoteLL;
      edge.pinned_to_link_local = alloc.localLL;
    }

    // compiled_port: only for edges with endpoint_host (matching the rendered Endpoint port).
    if (!edge.endpoint_host) {
      continue;
    }
    if ((edge.endpoint_port ?? 0) > 0) {
      edge.compiled_port = edge.endpoint_port as number;
      continue;
    }
    edge.compiled_port = isForward ? alloc.toPort : alloc.fromPort;
  }
}

// compile runs the full local/air-gap compile pipeline on a topology and returns a CompileResult.
// PURE: it operates on a deep-enough COPY of the input (the caller's topology is never mutated) and
// injects no clock, store, or feature flag. Mirrors compiler.go CompileAt + the AirGap key pre-pass.
//
// On a validation failure it throws a CompileError with code "compile_topology_validation_failed"
// carrying the first error code as `detail` — the controller channels a validation rejection as a plain
// fmt.Errorf wrap (NOT an apierr), and the conformance harness routes that to the validator channel
// (which it already populates by running validate() directly). On an allocation/key failure it throws
// the coded CompileError the leaf primitive raised (e.g. compile_transit_pool_exhausted), which the
// harness routes to the apierr channel.
//
// custody defaults to AirGap (the local-mode model). AgentHeld is accepted so the conformance harness
// can compile its AgentHeld fixtures; the store rewire (plan-6) always uses the default.
export function compile(
  topo: Topology,
  custody: KeyCustody = 'airgap',
): CompileResult {
  // Work on a copy so the caller's topology is never mutated.
  const compiledTopo = copyTopology(topo);

  // Pass 1: schema validation (MUTATES the copy's routing_mode/transport defaults in place).
  const schemaResult = validateSchema(compiledTopo);
  if (schemaResult.errors.length > 0) {
    throw new CompileError(CompileCode.TopologyValidationFailed, {
      stage: 'schema',
      detail: schemaResult.errors[0].code,
    });
  }

  // Pass 2: semantic validation.
  const semanticResult = validateSemantic(compiledTopo);
  if (semanticResult.errors.length > 0) {
    throw new CompileError(CompileCode.TopologyValidationFailed, {
      stage: 'semantic',
      detail: semanticResult.errors[0].code,
    });
  }

  const warnings: ValidationError[] = [
    ...schemaResult.warnings,
    ...semanticResult.warnings,
  ];

  // Derive / normalize keys: write the canonical / placeholder keys back onto the copy's nodes before
  // allocation, mirroring render.GenerateKeysWith's pre-pass for the selected custody model.
  const keys = generateKeys(compiledTopo.nodes, custody);

  // Pass 3: IP allocation. Returns a fresh node slice with overlay IPs filled; install it onto the copy.
  const allocatedNodes = allocateIPs(compiledTopo);
  compiledTopo.nodes = allocatedNodes;

  // Pass 3: infer capabilities per node.
  for (const node of compiledTopo.nodes) {
    node.capabilities = inferCapabilitiesFromRole(node);
  }

  // Pass 3: derive peers (Pass 1 reserve-then-gap-fill + Pass 2 PeerInfo). Local/full compiles pass no
  // reservation set (the cross-subgraph reservation is a controller-only concern).
  const { peerMap, pass1 } = derivePeers(compiledTopo, keys);

  // Client configs (client-role wg0).
  const clientConfigs = deriveClientConfigs(
    compiledTopo,
    keys,
    pass1.allocations,
  );

  // Write the six pinned_* + compiled_port back onto each enabled edge (direction-aware).
  writeBackPins(compiledTopo.edges, pass1.allocations);

  // Stamp the allocation-scheme version (invariant I10).
  compiledTopo.alloc_schema_version = AllocationSchemaVersion;

  // Renderers (the byte-exact half of Phase 4). Mirrors render.AllWith (render.go:305-334) ordering:
  // per-peer WireGuard configs + client wg0, Babel configs, sysctl configs. The renderers consume the
  // `keys` map built above (NOT the topology's private-key field) so the AgentHeld placeholder
  // (render.PrivateKeyPlaceholder) is emitted verbatim on the PrivateKey line, exactly as the Go path
  // does. install.sh / artifacts.json / deploy scripts are later Phase-4 substeps (22-25) and stay
  // empty here. The conformance harness pins every rendered file byte-for-byte against the Go golden.
  const wireGuardConfigs = renderAllWireGuardConfigs(compiledTopo, peerMap, keys);
  for (const nodeID of Object.keys(clientConfigs)) {
    wireGuardConfigs[nodeID + ':wg0'] = renderClientWireGuardConfig(
      clientConfigs[nodeID],
    );
  }
  const babelConfigs = renderAllBabelConfigs(compiledTopo, peerMap);
  const sysctlConfigs = renderAllSysctlConfigs(compiledTopo);

  // Install scripts (per-node) + deploy scripts (project-level bash + ps1). Mirrors render.AllWith's
  // tail (render.go:356-401): the install-script loop (signing is a NO-OP in local mode; the AgentHeld
  // splice is detected per-node from the keys map's placeholder private key) and RenderDeployScripts.
  // artifacts.json stays empty in local mode (no catalog → the D4 guard omits the file).
  const installScripts = renderAllInstallScripts(
    compiledTopo,
    peerMap,
    babelConfigs,
    clientConfigs,
    keys,
  );
  const { bash: deployBash, ps1: deployPS1 } = renderDeployScripts(
    compiledTopo,
    peerMap,
    babelConfigs,
  );
  const deployScripts: Record<string, string> = {
    'deploy-all.sh': deployBash,
    'deploy-all.ps1': deployPS1,
  };

  // The compile manifest. compiled_at + checksum are display-only and OUT of the conformance byte set
  // (the harness excludes them); they are filled by the Phase-4 / plan-6 caller that owns the clock.
  const manifest: CompileManifest = {
    project_id: topo.project.id,
    project_name: topo.project.name,
    version: topo.project.version ?? '',
    compiled_at: '',
    node_count: compiledTopo.nodes.length,
    checksum: '',
  };

  // All rendered surfaces are populated: per-peer WireGuard configs + client wg0, Babel, sysctl, the
  // per-node install scripts, and the project-level deploy scripts. artifactsJSON stays empty in local
  // mode (no mimic catalog → the D4 guard omits artifacts.json, keeping the bundle byte-identical to the
  // Go air-gap export).
  return {
    topology: compiledTopo,
    peerMap,
    wireGuardConfigs,
    babelConfigs,
    sysctlConfigs,
    installScripts,
    artifactsJSON: {},
    deployScripts,
    clientConfigs,
    warnings,
    manifest,
  };
}

// validate runs the schema + semantic passes (in /api/validate order) over a COPY of the topology and
// returns the combined findings in the EXACT shape /api/validate returns (handler.go HandleValidate /
// ValidateResponse: { valid, errors, warnings }). PURE — the caller's topology is never mutated (the
// in-place normalization happens on the copy, matching the conformance validator gate). This IS the
// public library entry the store rewire (plan-6) assigns straight into `validateResult: ValidateResponse`
// with NO shape translation: `valid` is `len(allErrors) == 0` exactly as the Go handler computes it
// (handler.go:129), and `errors` / `warnings` carry the same validator.ValidationError shape. (The
// internal schema-then-semantic primitive lives in ./validator; this is the store-shaped wrapper.)
export function validate(topo: Topology): ValidateResponse {
  const copy = copyTopology(topo);
  const schema = validateSchema(copy);
  const semantic = validateSemantic(copy);
  const errors: ValidationError[] = [...schema.errors, ...semantic.errors];
  const warnings: ValidationError[] = [...schema.warnings, ...semantic.warnings];
  return {
    valid: errors.length === 0,
    errors,
    warnings,
  };
}

// toCompileResponse projects a rich CompileResult (the oracle output compile() returns, which the
// conformance harness and the export builders consume) into the snake_case CompileResponse shape that
// /api/compile returns and that the store assigns into `compileResult: CompileResponse`
// (handler.go:169-178). The projection is a pure key-rename: the library-internal fields the wire
// response never carried (peerMap, clientConfigs, artifactsJSON) are dropped, and the camelCase
// config/script maps become their snake_case wire keys. `manifest` and `warnings` are re-exported types
// shared with ../types/topology, so they pass through unchanged. plan-6 wires
// `compile(topo)` → `toCompileResponse(...)` and assigns the result with NO further translation; the
// rich CompileResult stays available (compile() returns it) for the export path and the harness.
export function toCompileResponse(result: CompileResult): CompileResponse {
  return {
    topology: result.topology,
    wireguard_configs: result.wireGuardConfigs,
    babel_configs: result.babelConfigs,
    sysctl_configs: result.sysctlConfigs,
    install_scripts: result.installScripts,
    deploy_scripts: result.deployScripts,
    manifest: result.manifest,
    warnings: result.warnings,
  };
}

// deployScript renders ONE project-level deploy script (bash or PowerShell) for a topology, matching the
// single-script body /api/deploy-script?format=sh|ps1 returns (handler.go HandleDeployScript:277-292
// selects result.DeployScripts["deploy-all.{sh,ps1}"] by the format query). It runs the full pure
// compile() (the deploy renderer needs the derived peer map) and returns the selected script as a string;
// the store's downloadDeployScript (topologyStore.ts:874-892) wraps the fetched body in a Blob for the
// download, so plan-6 wires `new Blob([deployScript(topo, format)])` with no shape translation. PURE: no
// store, no clock, no network. `format` mirrors the query parameter exactly ('sh' → deploy-all.sh, 'ps1'
// → deploy-all.ps1).
export function deployScript(
  topo: Topology,
  format: 'sh' | 'ps1',
  custody: KeyCustody = 'airgap',
): string {
  const result = compile(topo, custody);
  return format === 'ps1'
    ? (result.deployScripts['deploy-all.ps1'] ?? '')
    : (result.deployScripts['deploy-all.sh'] ?? '');
}

export { generateRouterID } from './peers';

// Re-export the export-bundle surface so the public library exposes the per-node ZIP builder
// (exportArtifacts, mirroring /api/export — the store does res.blob() on the response, so plan-6 assigns
// the returned Blob with no shape translation) plus the in-memory files/checksums builders the
// conformance harness compares byte-for-byte against the Go golden.
export {
  exportArtifacts,
  buildFiles,
  buildChecksums,
  canonicalize,
  bundleFiles,
} from './export';
