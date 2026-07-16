import type { Topology } from '../types/topology';
import type { ControllerNode } from '../types/controller';

// custody.ts — client-side custody helpers for controller mode (plan-5, D4/D5).
// Controller mode is zero-knowledge: a WireGuard private key must never reach the
// server (the panel strips before every update-topology POST — the client mirror of
// the server's 400 in plan-1) and must never enter the design via import. These are
// pure functions; most operate over a Topology (the upload/import custody primitives),
// and stripLiveTelemetry operates over a ControllerNode (the persist-custody primitive
// that keeps fleet-confidential live telemetry out of localStorage). The dialogs/notices
// live in the stores/components.

export interface StripResult {
  topo: Topology;
  stripped: number;
}

// stripPrivateKeys returns a shallow-but-safe copy of topo with every node's
// wireguard_private_key cleared, plus the count of keys actually removed. Only the
// secret VALUE is touched: fixed_private_key (a UI flag, not a secret — see plan-1's
// model note) and wireguard_public_key (the agent's registered public half) are left
// as-is. Unmodified nodes are shared by reference; modified nodes are fresh objects,
// and the nodes array is new, so the input is never mutated.
//
// Scope note (plan-5 review): the SSH auto-deploy fields (ssh_alias/ssh_host/ssh_user/
// ssh_port/ssh_key_path) are DELIBERATELY not stripped. They are local/air-gap
// deploy-script metadata — never WireGuard key material, never used by the
// controller's agent-pull model — and ssh_key_path is a path on the OPERATOR's own
// machine, not a fleet-node secret. The zero-knowledge custody principle is about
// fleet-node WireGuard private keys (a controller breach must not expose the mesh);
// the controller is already trusted with the operator's design. Stripping these would
// also destroy the SSH config that LOCAL mode needs on a controller→local switch.
export function stripPrivateKeys(topo: Topology): StripResult {
  let stripped = 0;
  const nodes = topo.nodes.map((n) => {
    if (n.wireguard_private_key && n.wireguard_private_key !== '') {
      stripped++;
      return { ...n, wireguard_private_key: '' };
    }
    return n;
  });
  return { topo: { ...topo, nodes }, stripped };
}

// dropAllKeys returns a copy of topo with each node's NON-AUTHORITATIVE WireGuard key material removed,
// plus the count of nodes whose material was dropped. Used on CONTROLLER-mode import. Every node always
// loses its private key + the fixed_private_key pin flag (zero-knowledge; the API gate bars private
// keys). A MANAGED node also loses its public key — the controller is server-authoritative for managed
// keys (the agent supplies the public key at enrollment, stamped at compile by enrolledSubgraph), so an
// imported managed public key is non-authoritative and only confuses. A MANUAL (deployment_mode=manual,
// hand-deployed, agent-less) node KEEPS its public key: it never enrolls, so the operator-asserted
// public key in the design IS its authoritative identity (enrolledSubgraph admits the manual node by it,
// and the off-host-signed membership manifest binds it). Mixed-controller-local-mode plan-6.
// (Contrast stripPrivateKeys, the upload/local custody primitive that removes ONLY the secret value and
// keeps every public key; and clearStrandedKeys in topologyStore, the LOCAL-import path.)
export function dropAllKeys(topo: Topology): { topo: Topology; dropped: number } {
  let dropped = 0;
  const nodes = topo.nodes.map((n) => {
    const isManual = n.deployment_mode === 'manual';
    const hasPrivate = (n.wireguard_private_key && n.wireguard_private_key !== '') || !!n.fixed_private_key;
    // A managed node's public key is non-authoritative → dropped; a manual node's IS its identity → kept.
    const hasNonAuthoritativePublic = !isManual && !!n.wireguard_public_key && n.wireguard_public_key !== '';
    if (hasPrivate || hasNonAuthoritativePublic) {
      dropped++;
      return {
        ...n,
        wireguard_private_key: undefined,
        fixed_private_key: false,
        wireguard_public_key: isManual ? n.wireguard_public_key : undefined,
      };
    }
    return n;
  });
  return { topo: { ...topo, nodes }, dropped };
}

// stripLiveTelemetry clears a node's LIVE telemetry before it enters the
// persisted controller-storage cache. It is live-only, never persisted, for two reasons: (1) custody —
// wireguardPeers carries each peer's raw endpoint and probeResults carries operator-authorized probe
// destinations, both fleet-confidential network topology of the same class the server-held design
// blank keeps out of localStorage; (2) honesty — a handshake age, load value, or probe outcome frozen
// at persist time is stale and misleading after a reload. The aggregate conditions still persist for
// instant coloring; details are re-fetched live on refresh. Setting fields to undefined makes
// JSON.stringify omit their keys (same idiom as dropAllKeys).
//
// MAINTENANCE: this clears every live-only telemetry projection. If a future
// Sampler's metric is lifted by mapNode into a NEW live ControllerNode field, clear it here too
// (rather than allowlisting it in leakOracle) — the leakOracle e2e guard is the backstop that fails
// the build if a new non-allowlisted field reaches the persisted cache. resource carries no
// endpoint/IP/key material, but a load average frozen at persist time is stale (honesty), so it is
// live-only for the same reason as wireguardPeers.
export function stripLiveTelemetry(node: ControllerNode): ControllerNode {
  return {
    ...node,
    wireguardPeers: undefined,
    resource: undefined,
    probeResults: undefined,
    nativeXDP: undefined,
    mimicCapability: undefined,
  };
}
