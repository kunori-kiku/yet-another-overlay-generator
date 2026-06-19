// Compile orchestration — the TypeScript mirror of internal/compiler/compiler.go CompileAt (the
// air-gap / local-mode compile pipeline) plus the key-derivation pre-pass of internal/render/render.go
// GenerateKeysWith (AirGap custody). This is the PURE library entry the conformance harness (plan-5)
// and the store rewire (plan-6) consume: it takes a Topology and returns a CompileResult (the compiled
// topology + per-node PeerMap + client configs) WITHOUT mutating the caller's input and WITHOUT touching
// any store, flag, or clock.
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
// Renderers (WireGuard / Babel / sysctl / script / deploy configs) are Phase 4: compile() returns the
// compiled topology + peer map with EMPTY rendered-file maps for now, matching the CompileResult shape.

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

  // The WireGuard / Babel / sysctl maps are now populated by the Phase-4 renderers above; install.sh,
  // artifacts.json, and the deploy scripts are later substeps and stay empty until then.
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
// returns the combined findings. PURE — the caller's topology is never mutated (the in-place
// normalization happens on the copy, matching the conformance validator gate). Re-exported here so the
// public library surface mirrors the topologyStore /api/validate response shape (plan-6 wires it).
export function validate(topo: Topology): {
  errors: ValidationError[];
  warnings: ValidationError[];
} {
  const copy = copyTopology(topo);
  const schema = validateSchema(copy);
  const semantic = validateSemantic(copy);
  return {
    errors: [...schema.errors, ...semantic.errors],
    warnings: [...schema.warnings, ...semantic.warnings],
  };
}

export { generateRouterID } from './peers';

// Re-export the export-bundle surface so the public library exposes the per-node ZIP builder
// (exportArtifacts, mirroring /api/export) plus the in-memory files/checksums builders the conformance
// harness compares byte-for-byte against the Go golden.
export {
  exportArtifacts,
  buildFiles,
  buildChecksums,
  canonicalize,
  bundleFiles,
} from './export';
