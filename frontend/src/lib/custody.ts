import type { Topology } from '../types/topology';
import type { ControllerNode } from '../types/controller';

// custody.ts — client-side key-custody helpers for controller mode (plan-5, D4/D5).
// Controller mode is zero-knowledge: a WireGuard private key must never reach the
// server (the panel strips before every update-topology POST — the client mirror of
// the server's 400 in plan-1) and must never enter the design via import. These are
// pure functions over a Topology; the dialogs/notices live in the stores/components.

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

// dropAllKeys returns a copy of topo with EVERY node's WireGuard key material removed — private key,
// public key, AND the fixed_private_key pin flag — plus the count of nodes that carried any. Used on
// CONTROLLER-mode import: the controller is server-authoritative for keys, so each node's public key is
// supplied by its agent at enrollment and stamped at compile (enrolledSubgraph), and any private key is
// barred by custody. A design's imported keys are therefore non-authoritative — overwritten or dropped
// before any deploy — and keeping them only confuses the operator. (Contrast stripPrivateKeys, the
// upload/local custody primitive that removes ONLY the secret value; and clearStrandedKeys in
// topologyStore, the LOCAL-import path that drops pubkey-only nodes but keeps valid round-trip keypairs.)
export function dropAllKeys(topo: Topology): { topo: Topology; dropped: number } {
  let dropped = 0;
  const nodes = topo.nodes.map((n) => {
    const hasKeyMaterial =
      (n.wireguard_private_key && n.wireguard_private_key !== '') ||
      (n.wireguard_public_key && n.wireguard_public_key !== '') ||
      n.fixed_private_key;
    if (hasKeyMaterial) {
      dropped++;
      return { ...n, wireguard_private_key: undefined, wireguard_public_key: undefined, fixed_private_key: false };
    }
    return n;
  });
  return { topo: { ...topo, nodes }, dropped };
}

// stripLiveTelemetry clears a node's LIVE telemetry (beta.12 wireguardPeers) before it enters the
// persisted controller-storage cache. It is live-only, never persisted, for two reasons: (1) custody —
// wireguardPeers carries each peer's raw endpoint (IP:port), fleet-confidential network topology of the
// same class the server-held design blank keeps out of localStorage; (2) honesty — a handshake age
// frozen at persist time is stale and misleading after a reload. The aggregate wireguard CONDITION
// (curated, endpoint-free) still persists for instant coloring; the per-link detail is re-fetched live
// on refresh. Setting the field to undefined makes JSON.stringify omit the key (same idiom as
// dropAllKeys), so a rehydrated node has no wireguardPeers and every reader coerces (?? []).
export function stripLiveTelemetry(node: ControllerNode): ControllerNode {
  return { ...node, wireguardPeers: undefined };
}
