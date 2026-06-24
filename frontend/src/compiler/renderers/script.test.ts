import { describe, it, expect } from 'vitest';
import { collectMimicRemotes } from './script';
import type { PeerInfo } from '../model';

// Direct parity unit test for the TS collectMimicRemotes, mirroring the Go
// TestCollectMimicRemotes (internal/renderer/script_mimic_test.go). collectMimicRemotes reads only
// `mimic` + `endpoint`, so a minimal cast keeps the cases readable. This CI-locks the Go↔TS parity
// the adversarial review verified one-off: inbound-only / unparseable / zero / out-of-range ports are
// skipped, entries are deduped, and the order is host-then-port (matching Go's byte-ordered sort).
function mk(endpoint: string, mimic = true): PeerInfo {
  return { mimic, endpoint } as unknown as PeerInfo;
}

describe('collectMimicRemotes (Go parity)', () => {
  it('dedups, sorts host-then-port, and skips inbound/unparseable/zero/out-of-range', () => {
    const peers: PeerInfo[] = [
      mk('203.0.113.9:51820'),
      mk('203.0.113.1:51820'), // out of order -> sorts before .9
      mk('203.0.113.1:51820'), // duplicate -> deduped
      mk('[2001:db8::1]:51900'), // IPv6 -> host parsed bracket-free
      mk(''), // inbound-only -> skipped
      mk('garbage-no-port'), // unparseable -> skipped
      mk('203.0.113.2:0'), // zero port -> skipped
      mk('203.0.113.3:70000'), // out of range -> skipped
      mk('198.51.100.1:51820', false), // non-mimic -> skipped
    ];
    expect(collectMimicRemotes(peers)).toEqual([
      { Host: '2001:db8::1', Port: 51900 },
      { Host: '203.0.113.1', Port: 51820 },
      { Host: '203.0.113.9', Port: 51820 },
    ]);
  });

  it('returns empty for no mimic peers', () => {
    expect(collectMimicRemotes([mk('203.0.113.1:51820', false)])).toEqual([]);
  });
});
