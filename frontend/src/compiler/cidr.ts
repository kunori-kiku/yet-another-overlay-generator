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
    // Reject empty, non-digit, or >255 octets. Go's net.ParseIP also rejects a leading-zero octet
    // (e.g. "010", "04") — its dtoi parser stops at a non-canonical decimal — so reject any octet that
    // has a redundant leading zero, matching ParseIP byte-for-byte.
    if (p.length === 0 || !/^[0-9]+$/.test(p)) return null;
    if (p.length > 1 && p[0] === '0') return null;
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

// --- IP / CIDR family classification (the validator's three-way "invalid | not-IPv4 | IPv4" split) ---
//
// The schema validator (internal/validator/schema.go) leans on Go's net.ParseIP / net.ParseCIDR plus
// .To4() to classify an address into exactly three outcomes: unparseable, parseable-but-not-IPv4, and
// parseable IPv4. Go's net package accepts BOTH families (IPv4 + IPv6) where the validator does, so the
// IPv4-only parsers above (ipv4ToUint32 / parseCIDR) are insufficient to reproduce the not-IPv4 branch.
// These helpers add the family-aware classification, mirroring net.ParseIP / net.ParseCIDR + To4().

// parseIPFamily reports the address family of an IP string, mirroring net.ParseIP + To4():
//   - null              → net.ParseIP returns nil (unparseable: bad octet, >4 IPv4 fields, zone id,
//                          multiple "::", >8 IPv6 groups, >4 hex digits per group, etc.)
//   - { isIPv4: true }  → a parseable IPv4 address, OR an IPv4-mapped IPv6 (::ffff:a.b.c.d), whose
//                          To4() is non-nil (matching Go)
//   - { isIPv4: false } → a parseable IPv6 address whose To4() is nil
export function parseIPFamily(s: string): { isIPv4: boolean } | null {
  // IPv4 dotted-quad fast path: net.ParseIP treats a string containing a '.' before any ':' as IPv4.
  if (s.indexOf('.') >= 0 && (s.indexOf(':') < 0 || s.indexOf('.') < s.indexOf(':'))) {
    return ipv4ToUint32(s) !== null ? { isIPv4: true } : null;
  }
  const v6 = parseIPv6(s);
  if (v6 === null) return null;
  // Go's To4() returns non-nil for an IPv4-mapped IPv6 (::ffff:a.b.c.d), reported as IPv4 by the
  // validator's .To4() != nil check.
  return { isIPv4: isIPv4MappedV6(v6) };
}

// parseCIDRFamily reports the prefix length + address family of a CIDR string, mirroring
// net.ParseCIDR + ipNet.Mask.Size() + ipNet.IP.To4():
//   - null              → net.ParseCIDR returns an error (unparseable address or out-of-range prefix)
//   - { ones, isIPv4 }  → a parseable CIDR; ones is the prefix length AS GO REPORTS IT (for an IPv6
//                          mask that is the IPv6 ones count, e.g. ::ffff:10.0.0.0/120 → ones=120,
//                          isIPv4=true), and isIPv4 mirrors ipNet.IP.To4() != nil.
export function parseCIDRFamily(s: string): { ones: number; isIPv4: boolean } | null {
  const slash = s.indexOf('/');
  if (slash < 0) return null;
  const addrStr = s.slice(0, slash);
  const bitsStr = s.slice(slash + 1);
  if (bitsStr.length === 0 || !/^[0-9]+$/.test(bitsStr)) return null;
  const ones = Number(bitsStr);

  // IPv4 CIDR: address is dotted-quad and prefix ∈ [0,32].
  if (addrStr.indexOf('.') >= 0 && (addrStr.indexOf(':') < 0 || addrStr.indexOf('.') < addrStr.indexOf(':'))) {
    if (ipv4ToUint32(addrStr) === null) return null;
    if (ones < 0 || ones > 32) return null;
    return { ones, isIPv4: true };
  }
  // IPv6 CIDR: address is a valid IPv6 and prefix ∈ [0,128].
  const v6 = parseIPv6(addrStr);
  if (v6 === null) return null;
  if (ones < 0 || ones > 128) return null;
  // Go's net.ParseCIDR returns the MASKED network address; ipNet.IP.To4() then classifies THAT, not the
  // input. So apply the /ones IPv6 mask before the IPv4-mapped check — e.g. ::ffff:10.0.0.0/24 masks to
  // ::, whose To4() is nil (isIPv4=false), whereas /120 retains the ::ffff: prefix (isIPv4=true).
  const masked = maskV6(v6, ones);
  return { ones, isIPv4: isIPv4MappedV6(masked) };
}

// canonicalIP normalizes an address string into a comparable canonical form, mirroring the validator's
// canonicalIP (semantic.go:758-763): net.ParseIP(value).String() when parseable, else the value
// unchanged (so deduplication degrades to string equality, while the invalid-address rule owns the
// finding). For IPv4 the canonical form is the plain dotted-quad; for IPv6 it is the lowercase
// "::"-compressed form Go's net.IP.String() emits (e.g. "fe80:0:0:0:0:0:0:1" → "fe80::1"). An
// IPv4-mapped IPv6 (::ffff:a.b.c.d) renders in the dotted-quad form Go uses for a To4()-non-nil address.
export function canonicalIP(value: string): string {
  const fam = parseIPFamily(value);
  if (fam === null) {
    return value; // unparseable: identity, matching net.ParseIP(value) == nil
  }
  if (fam.isIPv4) {
    // An IPv4 (or IPv4-mapped IPv6) canonicalizes to dotted-quad — net.IP.String() prints To4() forms
    // as a.b.c.d. Re-derive from the parsed bytes so a mapped form (::ffff:10.0.0.1) and the bare
    // dotted-quad (10.0.0.1) collapse to the SAME key, exactly as Go's String() does.
    if (value.indexOf(':') < 0) {
      // Pure dotted-quad: ipv4ToUint32 already validated it; round-trip to drop any quirk.
      const u = ipv4ToUint32(value);
      return u === null ? value : uint32ToIPv4(u);
    }
    const v6 = parseIPv6(value);
    if (v6 === null) return value;
    return `${v6[12]}.${v6[13]}.${v6[14]}.${v6[15]}`;
  }
  // IPv6: emit Go net.IP.String()'s canonical lowercase, "::"-compressed form.
  const v6 = parseIPv6(value);
  if (v6 === null) return value;
  return ipv6ToCanonical(v6);
}

// ipv6ToCanonical renders 16 IPv6 bytes in Go net.IP.String()'s canonical text form: lowercase hex,
// each group minimal (no leading zeros), and the single LONGEST run of 2+ zero groups compressed to
// "::" (the first such run on a tie), mirroring the RFC 5952 form Go produces.
function ipv6ToCanonical(b: number[]): string {
  const groups: number[] = [];
  for (let i = 0; i < 16; i += 2) {
    groups.push(((b[i] << 8) | b[i + 1]) >>> 0);
  }
  // Find the longest run of consecutive zero groups (length >= 2); earliest on a tie.
  let bestStart = -1;
  let bestLen = 0;
  let curStart = -1;
  let curLen = 0;
  for (let i = 0; i < 8; i++) {
    if (groups[i] === 0) {
      if (curStart < 0) curStart = i;
      curLen++;
      if (curLen > bestLen) {
        bestLen = curLen;
        bestStart = curStart;
      }
    } else {
      curStart = -1;
      curLen = 0;
    }
  }
  if (bestLen < 2) {
    bestStart = -1;
  }
  let out = '';
  for (let i = 0; i < 8; i++) {
    if (bestStart >= 0 && i === bestStart) {
      out += i === 0 ? '::' : ':';
      i += bestLen - 1;
      continue;
    }
    out += groups[i].toString(16);
    if (i !== 7) out += ':';
  }
  // Trailing "::" leaves a dangling token handled by the loop (the last appended ':' before the run).
  return out;
}

// isIPv4MappedV6 reports whether a 16-byte IPv6 address is the IPv4-mapped form (::ffff:a.b.c.d), whose
// Go net.IP.To4() returns non-nil. Mirrors net.IP.To4's check for the 0:0:0:0:0:ffff prefix.
function isIPv4MappedV6(b: number[]): boolean {
  for (let i = 0; i < 10; i++) {
    if (b[i] !== 0) return false;
  }
  return b[10] === 0xff && b[11] === 0xff;
}

// maskV6 applies a /ones IPv6 prefix mask to a 16-byte address, returning the masked network address
// (mirroring net.ParseCIDR's ipNet.IP = ip.Mask(mask)). The top `ones` bits are kept, the rest zeroed.
function maskV6(b: number[], ones: number): number[] {
  const out = b.slice();
  for (let i = 0; i < 16; i++) {
    const bitStart = i * 8;
    if (bitStart >= ones) {
      out[i] = 0;
    } else if (bitStart + 8 > ones) {
      const keep = ones - bitStart; // number of high bits to keep in this byte
      const mask = (0xff << (8 - keep)) & 0xff;
      out[i] = b[i] & mask;
    }
  }
  return out;
}

// parseIPv6 parses an IPv6 textual address into its 16 bytes, mirroring the subset of forms Go's
// net.ParseIP accepts: exactly one "::" elision, 1-4 hex digits per group, at most 8 groups, an
// optional trailing embedded IPv4 (a.b.c.d) consuming the last two groups, NO zone id. Returns null
// for anything net.ParseIP rejects.
function parseIPv6(s: string): number[] | null {
  if (s.indexOf(':') < 0) return null;
  if (s.indexOf('%') >= 0) return null; // Go's ParseIP rejects a zone id

  const out = new Array<number>(16).fill(0);
  let pos = 0; // byte offset into out
  let ellipsis = -1; // byte offset where "::" was seen, or -1

  let i = 0;
  // Leading "::".
  if (s.length >= 2 && s[0] === ':' && s[1] === ':') {
    ellipsis = 0;
    i = 2;
    if (i === s.length) return out; // "::" alone
  } else if (s[0] === ':') {
    return null; // a single leading ':' that is not "::"
  }

  while (i < s.length) {
    // Parse a hex group (1-4 hex digits).
    let val = 0;
    let digits = 0;
    while (i < s.length && isHexDigit(s[i])) {
      val = val * 16 + hexVal(s[i]);
      digits++;
      if (digits > 4) return null;
      i++;
    }
    if (digits === 0) return null;

    // Embedded trailing IPv4 (a.b.c.d) — only valid as the final 4 bytes. The run of hex digits just
    // consumed is actually the leading decimal octet, so re-parse from its textual start.
    if (i < s.length && s[i] === '.') {
      const v4 = ipv4ToUint32(s.slice(i - digits));
      if (v4 === null) return null;
      if (pos + 4 > 16) return null;
      out[pos] = (v4 >>> 24) & 0xff;
      out[pos + 1] = (v4 >>> 16) & 0xff;
      out[pos + 2] = (v4 >>> 8) & 0xff;
      out[pos + 3] = v4 & 0xff;
      pos += 4;
      i = s.length; // the embedded IPv4 consumes the remainder of the string
      break;
    }

    // Store the 16-bit group big-endian.
    if (pos + 2 > 16) return null;
    out[pos] = (val >> 8) & 0xff;
    out[pos + 1] = val & 0xff;
    pos += 2;

    if (i === s.length) break;
    if (s[i] !== ':') return null; // must be a separator
    i++;
    if (i === s.length) return null; // trailing single ':'
    if (s[i] === ':') {
      // "::" — at most one allowed.
      if (ellipsis >= 0) return null;
      ellipsis = pos;
      i++;
      if (i === s.length) break; // trailing "::"
    }
  }

  // Expand "::" if present.
  if (ellipsis >= 0) {
    if (pos === 16) return null; // "::" with a full address is invalid
    const n = pos - ellipsis;
    for (let j = n - 1; j >= 0; j--) {
      out[16 - n + j] = out[ellipsis + j];
      out[ellipsis + j] = 0;
    }
    pos = 16;
  } else if (pos !== 16) {
    return null; // not enough groups and no "::"
  }
  return out;
}

function isHexDigit(c: string): boolean {
  return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');
}

function hexVal(c: string): number {
  if (c >= '0' && c <= '9') return c.charCodeAt(0) - 48;
  if (c >= 'a' && c <= 'f') return c.charCodeAt(0) - 87;
  return c.charCodeAt(0) - 55; // A-F
}
