// wgPeers.test.ts — the pure relative-handshake bucketing + status counts behind the per-peer panel.
import { describe, expect, it } from 'vitest';
import { handshakeAge, peerStatusCounts } from './wgPeers';
import type { WireGuardPeer } from '../types/controller';

describe('handshakeAge', () => {
  const now = 1_000_000; // arbitrary fixed "now" (unix seconds)
  it("reports 'never' for a 0/absent handshake or a never status regardless of the value", () => {
    expect(handshakeAge(now, 0, 'never')).toEqual({ kind: 'never' });
    expect(handshakeAge(now, now - 5, 'never')).toEqual({ kind: 'never' }); // status wins over a value
    expect(handshakeAge(now, 0, 'up')).toEqual({ kind: 'never' }); // 0 handshake is never
  });
  it('buckets seconds / minutes / hours / days', () => {
    expect(handshakeAge(now, now - 5, 'up')).toEqual({ kind: 'seconds', value: 5 });
    expect(handshakeAge(now, now - 120, 'stale')).toEqual({ kind: 'minutes', value: 2 });
    expect(handshakeAge(now, now - 3 * 3600, 'up')).toEqual({ kind: 'hours', value: 3 });
    expect(handshakeAge(now, now - 2 * 86400, 'up')).toEqual({ kind: 'days', value: 2 });
  });
  it('never goes negative when the agent clock is ahead of the panel', () => {
    expect(handshakeAge(now, now + 100, 'up')).toEqual({ kind: 'seconds', value: 0 });
  });
});

describe('peerStatusCounts', () => {
  it('tallies up/stale/never', () => {
    const peers = [
      { peer: 'a', interface: 'wg-a', endpoint: '', lastHandshake: 1, status: 'up' },
      { peer: 'b', interface: 'wg-b', endpoint: '', lastHandshake: 0, status: 'never' },
      { peer: 'c', interface: 'wg-c', endpoint: '', lastHandshake: 1, status: 'stale' },
      { peer: 'd', interface: 'wg-d', endpoint: '', lastHandshake: 0, status: 'never' },
    ] as WireGuardPeer[];
    expect(peerStatusCounts(peers)).toEqual({ up: 1, stale: 1, never: 2 });
  });
});
