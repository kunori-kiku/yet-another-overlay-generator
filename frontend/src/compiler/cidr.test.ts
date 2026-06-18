import { describe, expect, it } from 'vitest';

import {
  allocateLinkLocalPair,
  allocateTransitPair,
  contains,
  gapFillLinkLocalPair,
  gapFillTransitPair,
  ipv4ToUint32,
  parseCIDR,
  transitPoolPairCount,
  uint32ToIPv4,
} from './cidr';
import { CompileCode, CompileError } from './errors';

// CIDR / uint32 IP-math sweep (plan-4 substep 4, subject-scoped). Pins the leaf allocation primitives
// against the Go oracle values (computed from internal/compiler/peers.go: allocateTransitPair :918,
// transitPoolPairCount :977, allocateLinkLocalPair :1100). The expected values below are the literal
// outputs of the Go functions for the same inputs — the signed-32 bitwise footgun (`>>> 0` coercion),
// non-/24 CIDRs, reserved network/broadcast exclusion, and the /31//32 special-case all live here.

describe('ipv4ToUint32 / uint32ToIPv4 (>>> 0 coercion)', () => {
  it('round-trips dotted quads, including high-bit addresses that would go negative without >>> 0', () => {
    // 255.255.255.255 = 0xFFFFFFFF: a signed-32 read of this is -1; the unsigned coercion keeps it
    // 4294967295 so the serialization round-trips.
    expect(ipv4ToUint32('255.255.255.255')).toBe(4294967295);
    expect(uint32ToIPv4(4294967295)).toBe('255.255.255.255');
    expect(ipv4ToUint32('10.10.0.1')).toBe(168427521);
    expect(uint32ToIPv4(168427521)).toBe('10.10.0.1');
    expect(ipv4ToUint32('192.168.1.5')).toBe(3232235781);
    expect(uint32ToIPv4(3232235781)).toBe('192.168.1.5');
    // 128.0.0.0 = 0x80000000: the sign bit; >>> 0 keeps it 2147483648.
    expect(ipv4ToUint32('128.0.0.0')).toBe(2147483648);
    expect(uint32ToIPv4(2147483648)).toBe('128.0.0.0');
  });

  it('rejects malformed addresses (wrong octet count, out-of-range, non-digit)', () => {
    expect(ipv4ToUint32('10.10.0')).toBeNull();
    expect(ipv4ToUint32('10.10.0.0.0')).toBeNull();
    expect(ipv4ToUint32('10.10.0.256')).toBeNull();
    expect(ipv4ToUint32('10.10.0.')).toBeNull();
    expect(ipv4ToUint32('10.10.0.x')).toBeNull();
  });
});

describe('parseCIDR (masks to the network address like net.ParseCIDR)', () => {
  it('masks a host address down to its network and derives the broadcast', () => {
    // 10.10.0.5/24 → network 10.10.0.0, broadcast 10.10.0.255 (Go net.ParseCIDR yields the masked IP).
    const info = parseCIDR('10.10.0.5/24');
    expect(info).not.toBeNull();
    expect(uint32ToIPv4(info!.network)).toBe('10.10.0.0');
    expect(info!.maskBits).toBe(24);
    expect(info!.hostBits).toBe(8);
    expect(uint32ToIPv4(info!.broadcast)).toBe('10.10.0.255');
  });

  it('handles a /16 (broadcast .255.255) and the signed-32 mask boundary', () => {
    const info = parseCIDR('10.0.0.0/16');
    expect(uint32ToIPv4(info!.network)).toBe('10.0.0.0');
    expect(uint32ToIPv4(info!.broadcast)).toBe('10.0.255.255');
  });

  it('handles /31 and /32 host-bit edges without overflow', () => {
    const i31 = parseCIDR('10.0.0.0/31');
    expect(i31!.hostBits).toBe(1);
    expect(uint32ToIPv4(i31!.broadcast)).toBe('10.0.0.1');
    const i32 = parseCIDR('10.0.0.0/32');
    expect(i32!.hostBits).toBe(0);
    expect(uint32ToIPv4(i32!.broadcast)).toBe('10.0.0.0');
  });

  it('rejects malformed CIDRs', () => {
    expect(parseCIDR('10.10.0.0')).toBeNull(); // no slash
    expect(parseCIDR('10.10.0.0/33')).toBeNull(); // prefix out of range
    expect(parseCIDR('10.10.0.0/x')).toBeNull();
    expect(parseCIDR('not-an-ip/24')).toBeNull();
  });
});

describe('contains', () => {
  it('matches net.IPNet.Contains semantics', () => {
    const info = parseCIDR('10.10.0.0/24')!;
    expect(contains(info, '10.10.0.1')).toBe(true);
    expect(contains(info, '10.10.0.255')).toBe(true);
    expect(contains(info, '10.10.1.0')).toBe(false);
    expect(contains(info, '9.255.255.255')).toBe(false);
    expect(contains(info, 'garbage')).toBe(false);
  });
});

describe('allocateTransitPair (network/broadcast excluded; pair N = network+2N+1, +2N+2)', () => {
  it('matches the Go oracle for /24 including the last usable pair and exhaustion', () => {
    expect(allocateTransitPair(0, '10.10.0.0/24')).toEqual(['10.10.0.1', '10.10.0.2']);
    expect(allocateTransitPair(1, '10.10.0.0/24')).toEqual(['10.10.0.3', '10.10.0.4']);
    expect(allocateTransitPair(5, '10.10.0.0/24')).toEqual(['10.10.0.11', '10.10.0.12']);
    // idx 126 → .253/.254 (the last pair before the .255 broadcast); idx 127 would hit broadcast.
    expect(allocateTransitPair(126, '10.10.0.0/24')).toEqual(['10.10.0.253', '10.10.0.254']);
    expect(() => allocateTransitPair(127, '10.10.0.0/24')).toThrow(CompileError);
  });

  it('falls back to DefaultTransitCIDR (10.10.0.0/24) on empty', () => {
    expect(allocateTransitPair(0, '')).toEqual(['10.10.0.1', '10.10.0.2']);
  });

  it('treats a host address in the CIDR as its network (10.10.0.5/24 === 10.10.0.0/24)', () => {
    expect(allocateTransitPair(0, '10.10.0.5/24')).toEqual(['10.10.0.1', '10.10.0.2']);
  });

  it('matches the Go oracle for a /29 pool (3 usable pairs)', () => {
    expect(allocateTransitPair(0, '192.168.1.0/29')).toEqual(['192.168.1.1', '192.168.1.2']);
    expect(allocateTransitPair(1, '192.168.1.0/29')).toEqual(['192.168.1.3', '192.168.1.4']);
    expect(allocateTransitPair(2, '192.168.1.0/29')).toEqual(['192.168.1.5', '192.168.1.6']);
    expect(() => allocateTransitPair(3, '192.168.1.0/29')).toThrow(CompileError);
  });

  it('matches the Go oracle for a /30 pool (1 usable pair)', () => {
    expect(allocateTransitPair(0, '172.16.0.0/30')).toEqual(['172.16.0.1', '172.16.0.2']);
    expect(() => allocateTransitPair(1, '172.16.0.0/30')).toThrow(CompileError);
  });

  it('treats /31 and /32 as exhausted (hostBits < 2)', () => {
    expect(() => allocateTransitPair(0, '10.0.0.0/31')).toThrow(CompileError);
    expect(() => allocateTransitPair(0, '10.0.0.0/32')).toThrow(CompileError);
  });

  it('throws the transit_pool_exhausted code on exhaustion', () => {
    try {
      allocateTransitPair(127, '10.10.0.0/24');
      throw new Error('expected throw');
    } catch (e) {
      expect(e).toBeInstanceOf(CompileError);
      expect((e as CompileError).code).toBe(CompileCode.TransitPoolExhausted);
    }
  });

  it('throws transit_cidr_invalid on an unparseable CIDR', () => {
    try {
      allocateTransitPair(0, 'not-a-cidr');
      throw new Error('expected throw');
    } catch (e) {
      expect(e).toBeInstanceOf(CompileError);
      expect((e as CompileError).code).toBe(CompileCode.TransitCIDRInvalid);
    }
  });
});

describe('transitPoolPairCount', () => {
  it('matches the Go oracle across prefix sizes (no signed-32 shift overflow for large pools)', () => {
    expect(transitPoolPairCount('10.10.0.0/24')).toBe(127);
    expect(transitPoolPairCount('192.168.1.0/29')).toBe(3);
    expect(transitPoolPairCount('172.16.0.0/30')).toBe(1);
    expect(transitPoolPairCount('10.0.0.0/31')).toBe(0);
    expect(transitPoolPairCount('10.0.0.0/32')).toBe(0);
    // /16 = 32767 pairs: 2^16 - 2 = 65534 usable hosts / 2. A naive (1<<16) is fine, but a /1 etc.
    // would overflow a signed-32 shift — Math.pow keeps it correct.
    expect(transitPoolPairCount('10.0.0.0/16')).toBe(32767);
    expect(transitPoolPairCount('')).toBe(127); // default
  });
});

describe('gapFillTransitPair (lowest-free pair, skip reserved)', () => {
  it('returns index-0 pair when nothing is reserved', () => {
    const used = new Set<string>();
    const pred = (_cidr: string, ip: string) => used.has(ip);
    expect(gapFillTransitPair('10.10.0.0/24', pred)).toEqual(['10.10.0.1', '10.10.0.2']);
  });

  it('skips a pair when EITHER address is reserved and takes the next free pair', () => {
    const used = new Set<string>(['10.10.0.2']); // half of pair 0 reserved → skip pair 0
    const pred = (_cidr: string, ip: string) => used.has(ip);
    expect(gapFillTransitPair('10.10.0.0/24', pred)).toEqual(['10.10.0.3', '10.10.0.4']);
  });

  it('exhausts a fully-reserved /30 pool with the transit_pool_exhausted code', () => {
    const used = new Set<string>(['172.16.0.1', '172.16.0.2']);
    const pred = (_cidr: string, ip: string) => used.has(ip);
    try {
      gapFillTransitPair('172.16.0.0/30', pred);
      throw new Error('expected throw');
    } catch (e) {
      expect(e).toBeInstanceOf(CompileError);
      expect((e as CompileError).code).toBe(CompileCode.TransitPoolExhausted);
    }
  });

  it('exhausts /31 (zero-pair pool) up front', () => {
    expect(() => gapFillTransitPair('10.0.0.0/31', () => false)).toThrow(CompileError);
  });
});

describe('allocateLinkLocalPair (%x lowercase hex, base = 2*index+1)', () => {
  it('matches the Go oracle, rendering the index in HEX not decimal (D70)', () => {
    expect(allocateLinkLocalPair(0)).toEqual(['fe80::1', 'fe80::2']);
    expect(allocateLinkLocalPair(1)).toEqual(['fe80::3', 'fe80::4']);
    expect(allocateLinkLocalPair(5)).toEqual(['fe80::b', 'fe80::c']);
    // index 8 → base 17 → 0x11 → fe80::11 (NOT decimal 11). This is the %x-vs-%d trap.
    expect(allocateLinkLocalPair(8)).toEqual(['fe80::11', 'fe80::12']);
  });
});

describe('gapFillLinkLocalPair (lowest-free, skip reserved)', () => {
  it('returns index-0 pair when nothing is reserved', () => {
    expect(gapFillLinkLocalPair(new Set())).toEqual(['fe80::1', 'fe80::2']);
  });

  it('skips a pair when either end is reserved', () => {
    // Reserve fe80::2 (half of pair 0) → skip to pair 1.
    expect(gapFillLinkLocalPair(new Set(['fe80::2']))).toEqual(['fe80::3', 'fe80::4']);
    // Reserve pairs 0 and 1 entirely → land on pair 2 (fe80::5 / fe80::6).
    expect(gapFillLinkLocalPair(new Set(['fe80::1', 'fe80::3', 'fe80::4']))).toEqual([
      'fe80::5',
      'fe80::6',
    ]);
  });
});
