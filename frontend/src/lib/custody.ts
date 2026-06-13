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
