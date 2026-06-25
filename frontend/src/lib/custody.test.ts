import { describe, expect, it } from 'vitest';
import { stripLiveTelemetry, dropAllKeys } from './custody';
import type { ControllerNode } from '../types/controller';
import type { Node, Topology } from '../types/topology';

// custody.test.ts — pins the persist-custody invariant that LIVE telemetry never enters the persisted
// controller-storage cache (beta.12 regression: wireguardPeers, which carries raw peer endpoints, leaked
// into localStorage and tripped the e2e leakOracle allowlist). stripLiveTelemetry is the partialize
// node-mapper; the contract is that the SERIALIZED node has no wireguardPeers key (JSON.stringify omits
// the undefined-valued field) while every other field round-trips untouched.

function node(overrides: Partial<ControllerNode> = {}): ControllerNode {
  return {
    nodeId: 'node-1',
    status: 'approved',
    hasWGPublicKey: true,
    desiredGeneration: 3,
    appliedGeneration: 3,
    lastChecksum: 'csum-3',
    lastHealth: 'applied',
    agentVersion: 'v2.0.0-beta.12',
    lastSeen: '2026-06-23T12:00:00Z',
    enrolledAt: '2026-06-01T00:00:00Z',
    rekeyRequested: false,
    inRollout: false,
    conditions: [
      {
        type: 'wireguard',
        status: 'warn',
        reason: 'SomePeersDown',
        message: '1/2 peers down (no handshake)',
        since: '2026-06-23T12:00:00Z',
        observedAt: '2026-06-23T12:00:25Z',
      },
    ],
    wireguardPeers: [
      { peer: 'bravo', interface: 'wg-bravo', endpoint: '203.0.113.7:51820', lastHandshake: 1782820825, status: 'up' },
      { peer: 'charlie', interface: 'wg-charlie', endpoint: '', lastHandshake: 0, status: 'never' },
    ],
    ...overrides,
  };
}

describe('stripLiveTelemetry (persist custody)', () => {
  it('omits wireguardPeers from the SERIALIZED node (no raw endpoint reaches localStorage)', () => {
    const serialized = JSON.parse(JSON.stringify(stripLiveTelemetry(node())));
    expect('wireguardPeers' in serialized).toBe(false);
    // The raw peer endpoint — fleet-confidential network topology — must not survive into the blob.
    expect(JSON.stringify(serialized).includes('203.0.113.7')).toBe(false);
  });

  it('keeps the aggregate wireguard condition and every other field intact', () => {
    const out = stripLiveTelemetry(node());
    expect(out.nodeId).toBe('node-1');
    expect(out.appliedGeneration).toBe(3);
    expect(out.lastChecksum).toBe('csum-3');
    // The curated, endpoint-free condition still persists for instant coloring after a reload.
    expect(out.conditions).toHaveLength(1);
    expect(out.conditions[0].reason).toBe('SomePeersDown');
  });

  it('does not mutate the input node (the live in-memory node keeps its telemetry)', () => {
    const input = node();
    stripLiveTelemetry(input);
    expect(input.wireguardPeers).toHaveLength(2);
  });

  it('is a no-op shape for a node that already has no telemetry (legacy/beta.11 cache)', () => {
    const serialized = JSON.parse(JSON.stringify(stripLiveTelemetry(node({ wireguardPeers: undefined }))));
    expect('wireguardPeers' in serialized).toBe(false);
    expect(serialized.nodeId).toBe('node-1');
  });
});

function topoNode(overrides: Partial<Node> = {}): Node {
  return {
    id: 'n',
    name: 'n',
    role: 'router',
    domain_id: 'd1',
    capabilities: { can_accept_inbound: false, can_forward: false, can_relay: false, has_public_ip: false },
    ...overrides,
  };
}

function topo(nodes: Node[]): Topology {
  return { project: { id: 'p', name: 'p' }, domains: [], nodes, edges: [] };
}

describe('dropAllKeys (controller import custody)', () => {
  it('keeps a MANUAL node public key (its identity) but drops its private key + fixed pin', () => {
    const { topo: out, dropped } = dropAllKeys(
      topo([
        topoNode({
          id: 'mike',
          deployment_mode: 'manual',
          wireguard_public_key: 'MANUAL_PUB',
          wireguard_private_key: 'MANUAL_PRIV',
          fixed_private_key: true,
        }),
      ]),
    );
    const mike = out.nodes.find((n) => n.id === 'mike')!;
    expect(mike.wireguard_public_key).toBe('MANUAL_PUB'); // kept — operator-asserted identity
    expect(mike.wireguard_private_key).toBeUndefined(); // dropped — zero-knowledge
    expect(mike.fixed_private_key).toBe(false); // dropped
    expect(dropped).toBe(1); // a key field was removed (the private)
  });

  it('drops a MANAGED node entire key material (public is non-authoritative)', () => {
    const { topo: out } = dropAllKeys(
      topo([topoNode({ id: 'alpha', wireguard_public_key: 'MANAGED_PUB', wireguard_private_key: 'MANAGED_PRIV' })]),
    );
    const alpha = out.nodes.find((n) => n.id === 'alpha')!;
    expect(alpha.wireguard_public_key).toBeUndefined(); // managed public → dropped
    expect(alpha.wireguard_private_key).toBeUndefined();
  });

  it('a manual node with only a public key is left untouched (nothing to drop)', () => {
    const { dropped } = dropAllKeys(
      topo([topoNode({ id: 'mike', deployment_mode: 'manual', wireguard_public_key: 'MANUAL_PUB' })]),
    );
    expect(dropped).toBe(0); // keeping the manual public key is not a drop
  });
});
