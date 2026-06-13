import type { Topology } from '../types/topology';

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
