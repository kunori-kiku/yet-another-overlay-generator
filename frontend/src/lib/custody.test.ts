import { describe, expect, it } from 'vitest';
import { stripLiveTelemetry } from './custody';
import type { ControllerNode } from '../types/controller';

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
