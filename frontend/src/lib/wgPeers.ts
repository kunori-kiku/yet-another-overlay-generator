// wgPeers.ts — pure logic behind the collapsible per-peer WireGuard panel (beta.12). Kept out of the
// component so it is unit-testable in the node vitest env (the project has no jsdom/RTL): the
// relative-handshake bucketing + the at-a-glance peer-status counts. The component owns rendering +
// i18n; this owns the math.

import type { WireGuardPeer } from '../types/controller';

// HandshakeAge is the bucketed age of a peer's last handshake, ready for the component to localize
// (the lib stays i18n-free + pure). 'never' is a 0/absent handshake; the rest carry a rounded value.
export type HandshakeAge =
  | { kind: 'never' }
  | { kind: 'seconds' | 'minutes' | 'hours' | 'days'; value: number };

// handshakeAge buckets a peer's last-handshake (unix seconds) relative to nowSec (also unix seconds),
// honoring the agent's status: a 'never'/0 handshake is 'never' regardless. Pure + deterministic
// (now is injected, not read), so it is unit-testable.
export function handshakeAge(nowSec: number, hsSec: number, status: string): HandshakeAge {
  if (status === 'never' || !hsSec || hsSec <= 0) return { kind: 'never' };
  const secs = Math.max(0, Math.floor(nowSec) - Math.floor(hsSec));
  if (secs < 60) return { kind: 'seconds', value: secs };
  const mins = Math.floor(secs / 60);
  if (mins < 60) return { kind: 'minutes', value: mins };
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return { kind: 'hours', value: hrs };
  return { kind: 'days', value: Math.floor(hrs / 24) };
}

// peerStatusCounts tallies the per-status peer counts for the panel's at-a-glance summary.
export function peerStatusCounts(peers: WireGuardPeer[]): { up: number; stale: number; never: number } {
  const c = { up: 0, stale: 0, never: 0 };
  for (const p of peers) {
    if (p.status === 'up') c.up++;
    else if (p.status === 'stale') c.stale++;
    else c.never++;
  }
  return c;
}
