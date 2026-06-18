// CIDR + IP arithmetic — the TypeScript mirror of the Go allocator/peer IP math
// (internal/allocator/ip.go + internal/compiler/peers.go: allocateTransitPair :918, transitPoolPairCount
// :977, gapFillTransitPair :1017, allocateLinkLocalPair :1100, gapFillLinkLocalPair :1045).
//
// THE #1 FOOTGUN: JS bitwise operators coerce to a SIGNED 32-bit int, so any IP/uint32 result must be
// coerced back to unsigned with `>>> 0`. Every arithmetic result here that represents a uint32 IPv4
// address (or a mask) is coerced with `>>> 0` to match Go's uint32 modular arithmetic byte-for-byte,
// including integer wraparound (Go addr2 < addr1 wraparound → pool-exhausted; the >>> 0 result wraps
// identically).
//
// IPv4 addresses are carried as unsigned 32-bit numbers; IPv6 link-locals only ever need the
// fe80::<hex> form the Go allocator emits, so they are produced as strings directly.

import { CompileCode, CompileError } from './errors';
import { DefaultTransitCIDR } from './allocconst';

// ipv4ToUint32 parses a dotted-quad "a.b.c.d" into an unsigned 32-bit number, or null if it is not a
// well-formed IPv4 address (each octet 0-255, exactly four octets, no leading-zero ambiguity beyond
// what Go's net.ParseIP accepts). Mirrors the big-endian packing Go's binary.BigEndian.Uint32 does on
// net.IP.To4().
export function ipv4ToUint32(ip: string): number | null {
  const parts = ip.split('.');
  if (parts.length !== 4) return null;
  let out = 0;
  for (const p of parts) {
    // Reject empty, non-digit, or >255 octets. Go's net.ParseIP also rejects these.
    if (p.length === 0 || !/^[0-9]+$/.test(p)) return null;
    const n = Number(p);
    if (n > 255) return null;
    out = ((out << 8) | n) >>> 0; // coerce: << is signed-32, the high octet would go negative.
  }
  return out >>> 0;
}

// uint32ToIPv4 serializes an unsigned 32-bit number back to dotted-quad, mirroring Go's
// net.IP.String() for a 4-byte IPv4 (plain dotted decimal, no zero-padding).
export function uint32ToIPv4(n: number): string {
  const u = n >>> 0;
  return `${(u >>> 24) & 0xff}.${(u >>> 16) & 0xff}.${(u >>> 8) & 0xff}.${u & 0xff}`;
}

// CIDRInfo is the parsed form of an IPv4 CIDR: the masked NETWORK address (uint32), the mask bit count,
// and the derived broadcast address (network | hostMask). Mirrors what net.ParseCIDR yields (ipNet.IP
// is the MASKED network address, not the input host address) plus the mask-derived broadcast.
export interface CIDRInfo {
  network: number; // uint32, masked network address
  maskBits: number; // prefix length (the "ones" count)
  broadcast: number; // uint32, network | (2^hostBits - 1)
  hostBits: number; // 32 - maskBits
}

// parseCIDR parses "a.b.c.d/n" into a CIDRInfo, masking the address down to its network address exactly
// as Go's net.ParseCIDR does. Returns null when the string is not a well-formed IPv4 CIDR (bad address,
// missing/out-of-range prefix). The network address is `addr & mask` (coerced >>> 0).
export function parseCIDR(cidr: string): CIDRInfo | null {
  const slash = cidr.indexOf('/');
  if (slash < 0) return null;
  const addrStr = cidr.slice(0, slash);
  const bitsStr = cidr.slice(slash + 1);
  if (bitsStr.length === 0 || !/^[0-9]+$/.test(bitsStr)) return null;
  const maskBits = Number(bitsStr);
  if (maskBits < 0 || maskBits > 32) return null;
  const addr = ipv4ToUint32(addrStr);
  if (addr === null) return null;

  // mask = the top `maskBits` bits set. maskBits===0 → mask 0 (Go: a /0 masks to 0.0.0.0).
  const mask = maskBits === 0 ? 0 : (0xffffffff << (32 - maskBits)) >>> 0;
  const network = (addr & mask) >>> 0;
  const hostBits = 32 - maskBits;
  const hostMask = hostBits === 0 ? 0 : ((1 << hostBits) >>> 0) - 1;
  const broadcast = (network | hostMask) >>> 0;
  return { network, maskBits, broadcast, hostBits };
}

// contains reports whether the IPv4 address ip falls within the CIDR, mirroring net.IPNet.Contains:
// (ip & mask) === network. Returns false for an unparseable ip.
export function contains(info: CIDRInfo, ip: string): boolean {
  const addr = ipv4ToUint32(ip);
  if (addr === null) return false;
  const mask =
    info.maskBits === 0 ? 0 : (0xffffffff << (32 - info.maskBits)) >>> 0;
  return ((addr & mask) >>> 0) === info.network;
}

// allocateTransitPair allocates a pair of transit IPv4 addresses by index within transitCIDR. Mirrors
// internal/compiler/peers.go:918 (allocateTransitPair) EXACTLY:
//   - empty transitCIDR falls back to DefaultTransitCIDR (10.10.0.0/24);
//   - the base is the MASKED network address (so 10.10.0.5/24 behaves as 10.10.0.0/24);
//   - pair N occupies (network + 2N+1, network + 2N+2);
//   - the usable host range is the OPEN interval (network, broadcast) — the network and broadcast
//     addresses are never allocated (audit D48);
//   - hostBits < 2 (/31, /32) → pool cannot hold any pair → exhausted;
//   - integer wraparound (addr2 < addr1, modular uint32) or landing on/outside the bounds → exhausted.
// Throws CompileError(transit_cidr_invalid / transit_cidr_not_ipv4 / transit_pool_exhausted).
export function allocateTransitPair(index: number, transitCIDR: string): [string, string] {
  const cidr = transitCIDR === '' ? DefaultTransitCIDR : transitCIDR;
  const info = parseCIDR(cidr);
  if (info === null) {
    // net.ParseCIDR failure in Go → "invalid transit CIDR". The IPv4-only guard (To4()==nil) is folded
    // in: parseCIDR only accepts IPv4 forms, so a non-IPv4 string is reported as invalid here too,
    // matching allocateTransitPair's two-error structure where ParseCIDR runs first.
    throw new CompileError(CompileCode.TransitCIDRInvalid, { cidr });
  }

  const networkAddr = info.network >>> 0;
  const hostBits = info.hostBits;
  // hostBits < 2 (/31, /32): the pool cannot hold a pair (Go declares exhausted).
  if (hostBits < 2) {
    throw new CompileError(CompileCode.TransitPoolExhausted, { cidr, index: String(index) });
  }
  const broadcastAddr = info.broadcast >>> 0;

  const offset = (2 * index + 1) >>> 0;
  const addr1 = (networkAddr + offset) >>> 0;
  const addr2 = (networkAddr + offset + 1) >>> 0;

  // Out of range (including addr2 < addr1 from uint32 wraparound), or hitting network/broadcast → pool
  // exhausted. Usable host range is the open interval (network, broadcast). Unsigned compares (the
  // operands are already >>> 0 coerced, so < / <= / >= are unsigned comparisons here).
  if (
    addr2 < addr1 ||
    addr1 <= networkAddr ||
    addr1 >= broadcastAddr ||
    addr2 <= networkAddr ||
    addr2 >= broadcastAddr
  ) {
    throw new CompileError(CompileCode.TransitPoolExhausted, { cidr, index: String(index) });
  }

  return [uint32ToIPv4(addr1), uint32ToIPv4(addr2)];
}

// transitPoolPairCount returns the number of usable pairs in a transit CIDR pool (the pair-index upper
// bound). Mirrors internal/compiler/peers.go:977: usableHosts = 2^hostBits - 2, pairs = usableHosts/2.
// /24 → 127, /29 → 3, /30 → 1; hostBits < 2 (/31, /32) → 0. Throws on an invalid / non-IPv4 CIDR with
// the matching apierr code.
export function transitPoolPairCount(transitCIDR: string): number {
  const cidr = transitCIDR === '' ? DefaultTransitCIDR : transitCIDR;
  const info = parseCIDR(cidr);
  if (info === null) {
    // Go distinguishes parse-failure (CodeTransitCIDRInvalid, with a detail) from a parsed-but-not-IPv4
    // CIDR (CodeTransitCIDRNotIPv4). parseCIDR rejects every non-IPv4 form at the parse step, so the
    // only reachable failure here is the invalid case; the not-IPv4 code is reserved for completeness.
    throw new CompileError(CompileCode.TransitCIDRInvalid, { cidr });
  }
  if (info.hostBits < 2) {
    return 0;
  }
  // usableHosts = 2^hostBits - 2. Use Math.pow (not <<) for hostBits up to 32 to avoid the signed-32
  // shift overflow at hostBits===31/32 (1<<31 is negative, 1<<32 wraps to 1). The division floors.
  const usableHosts = Math.pow(2, info.hostBits) - 2;
  return Math.floor(usableHosts / 2);
}

// transitUsedFn is the per-CIDR reservation predicate the gap-fill consults: used(cidr, ip) reports
// whether ip is already reserved in that pool. Mirrors the transitUsed closure in peers.go.
export type transitUsedFn = (cidr: string, ip: string) => boolean;

// gapFillTransitPair allocates the lowest-free transit pair in the per-CIDR pool for an unpinned link.
// Mirrors internal/compiler/peers.go:1017: scan upward from index 0 to poolPairs-1, skip any pair where
// either address is already reserved, return the first pair where both ends are free; whole pool full →
// CodeTransitPoolExhausted. An empty pool (poolPairs <= 0) is exhausted up front.
export function gapFillTransitPair(transitCIDR: string, used: transitUsedFn): [string, string] {
  const cidr = transitCIDR === '' ? DefaultTransitCIDR : transitCIDR;
  const poolPairs = transitPoolPairCount(cidr);
  if (poolPairs <= 0) {
    throw new CompileError(CompileCode.TransitPoolExhausted, { cidr });
  }
  for (let index = 0; index < poolPairs; index++) {
    let pair: [string, string];
    try {
      pair = allocateTransitPair(index, cidr);
    } catch {
      // Every index within the pool should be usable; defensively skip an unexpected out-of-range index
      // (mirrors the Go `continue` on allocateTransitPair error inside the scan).
      continue;
    }
    if (used(cidr, pair[0]) || used(cidr, pair[1])) {
      continue;
    }
    return pair;
  }
  throw new CompileError(CompileCode.TransitPoolExhausted, { cidr });
}

// allocateLinkLocalPair allocates a pair of IPv6 link-local addresses by index. Mirrors
// internal/compiler/peers.go:1100 — fmt.Sprintf("fe80::%x", base) with base = 2*index+1, so the index is
// rendered in LOWERCASE HEX (D70: %x not %d, so index 5 → fe80::b / fe80::c, not fe80::11/12).
//   pair 0: fe80::1, fe80::2
//   pair 1: fe80::3, fe80::4
//   pair 5: fe80::b, fe80::c
export function allocateLinkLocalPair(index: number): [string, string] {
  const base = 2 * index + 1;
  return [`fe80::${base.toString(16)}`, `fe80::${(base + 1).toString(16)}`];
}

// gapFillLinkLocalPair allocates the lowest-free IPv6 link-local pair for an unpinned link. Mirrors
// internal/compiler/peers.go:1045 — scan upward from index 0, skip any pair where either end is already
// reserved (usedLinkLocals), return the first pair where both ends are free. fe80::/10 is effectively
// unlimited for any real fleet, so the scan always terminates. usedLinkLocals is a Set of link-local
// strings (the Go map[string]bool).
export function gapFillLinkLocalPair(usedLinkLocals: Set<string>): [string, string] {
  for (let index = 0; ; index++) {
    const [local, remote] = allocateLinkLocalPair(index);
    if (usedLinkLocals.has(local) || usedLinkLocals.has(remote)) {
      continue;
    }
    return [local, remote];
  }
}
