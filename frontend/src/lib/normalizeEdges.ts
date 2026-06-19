import { linkKey } from '../compiler/linkid';
import type { Edge, Node } from '../types/topology';

// healCollidingPins repairs the "pin occupied by two different links" corruption: an edge whose
// pinned listen port / transit IP / link-local collides with an EARLIER edge of a DIFFERENT link
// identity gets ALL its allocation pins stripped, so it re-allocates fresh on the next compile.
// This is the browser-side mirror of the Go normalize.HealCollidingPins (applied on the controller
// write path) and the inverse of the semantic validator's cross-link dedup — what it strips is
// exactly what the validator would flag. Two corruption sources are covered:
//   - a backup edge that kept its sibling primary's pins (legacy EdgeEditor role-flip bug), and
//   - two DIFFERENT links (cross-pair, or a pair's primary vs a new same-pair link) handed the same
//     allocation by successive incremental-enrollment subgraph compiles (the controller root cause).
//
// It mirrors the validator/Go heal precisely:
//   - disabled edges and client-touched edges are skipped (single wg0, no per-peer resources);
//   - link identity = linkKey (primary class folds A->B / B->A and same-pair primaries into one
//     link, so their legitimately-mirrored equal values are NOT a collision; each backup is its own
//     link via the "#id" suffix);
//   - claims are processed in reserve-first (linkKey-SORTED) order, the same priority the Go
//     allocator's gap-fill uses; the FIRST edge in THAT order to claim a value keeps it, so the kept
//     claimant of a contested slot is the SMALLER-linkKey (reserve-first) owner — exactly what
//     compiler allocation reproduces — NOT whichever edge happens to come first in array order. A
//     later, different-link edge that needs any already-claimed value is stripped as a whole (an
//     allocation is a unit). (Array order would wrongly keep a stale re-enabled pin and strip the
//     live incumbent: disable A-B, add A-C into the freed slot, re-enable A-B — A-B sits EARLIER in
//     the array. This mirrors the Go discriminator in internal/normalize/pins.go.)
//
// Pure and idempotent: returns the SAME array reference when nothing needs healing (no React/zustand
// churn), otherwise a new array with the offending edges' pins cleared.

const PIN_FIELDS = [
  'compiled_port',
  'pinned_from_port',
  'pinned_to_port',
  'pinned_from_transit_ip',
  'pinned_to_transit_ip',
  'pinned_from_link_local',
  'pinned_to_link_local',
] as const;

// pinKey / linkKey are the canonical link-identity functions, now lifted to ../compiler/linkid.ts
// (the single source of truth mirroring internal/linkid). This module imports linkKey from there;
// the prior in-file duplicates were retired. pinKey is no longer referenced here directly (linkKey
// folds it in), so it is not re-imported.

// canonicalIP mirrors Go's net.IP.String() (used by the validator + Go heal's canonicalIP) so the
// browser heal detects the SAME address collisions regardless of spelling — e.g. "FE80::1" and
// "fe80::1", or an expanded "fe80:0:0:0:0:0:0:1", all canonicalize to "fe80::1". The WHATWG URL
// parser implements the same RFC 5952 IPv6 serialization Go does. Unparseable values fall through
// unchanged (matching Go, where net.ParseIP returns nil and canonicalIP keeps the raw string), so a
// non-canonical IPv4 like "10.10.0.001" stays raw in both — the validator's invalid-IP rule owns it.
function canonicalIP(value: string): string {
  if (!value.includes(':')) return value; // IPv4 / non-IP: identity (canonical IPv4 already matches Go)
  try {
    const host = new URL(`http://[${value}]`).hostname; // "[fe80::1]"
    return host.startsWith('[') ? host.slice(1, -1) : host;
  } catch {
    return value;
  }
}

function stripPins(e: Edge): Edge {
  const out: Edge = { ...e };
  // Delete each pin by dynamic key. Edge does not structurally overlap Record<string, unknown>
  // (tsc -b TS2352), so route the index access through unknown — the keys are the literal,
  // all-optional PIN_FIELDS, so the delete is well-formed.
  for (const f of PIN_FIELDS) delete (out as unknown as Record<string, unknown>)[f];
  return out;
}

export function healCollidingPins(edges: Edge[], nodes: Node[]): Edge[] {
  const roleByNode = new Map<string, string>();
  for (const n of nodes) roleByNode.set(n.id, n.role);
  const isClientTouched = (e: Edge) =>
    roleByNode.get(e.from_node_id) === 'client' || roleByNode.get(e.to_node_id) === 'client';

  // Process enabled, non-client edges in reserve-first (linkKey-SORTED) order — the same priority
  // the Go allocator's gap-fill uses — so the kept claimant of a contested slot is the smaller-linkKey
  // (reserve-first) owner, not whichever edge comes first in array order. Disabled/client edges are
  // skipped entirely (Go's bool zero treats a missing is_enabled as DISABLED, so require strictly
  // true). We sort INDICES (ties broken by original index for determinism — a primary class's
  // forward/reverse edges share a linkKey and never collide with each other) so the healed result
  // stays in the caller's original edge order. Mirrors internal/normalize/pins.go.
  const order: number[] = [];
  for (let i = 0; i < edges.length; i++) {
    const e = edges[i];
    if (e.is_enabled !== true || isClientTouched(e)) continue;
    order.push(i);
  }
  order.sort((a, b) => {
    const la = linkKey(edges[a]);
    const lb = linkKey(edges[b]);
    if (la !== lb) return la < lb ? -1 : 1;
    return a - b;
  });

  // Claim tables: each maps a resource to the linkKey that first claimed it (in linkKey-sorted order).
  const portOwner = new Map<string, string>(); // `${nodeId}:${port}` -> linkKey
  const transitOwner = new Map<string, string>(); // ip -> linkKey
  const llOwner = new Map<string, string>(); // ip -> linkKey

  const healed = edges.slice(); // identity refs until a strip replaces one; discarded if unchanged
  let changed = false;
  for (const i of order) {
    const e = edges[i];
    const link = linkKey(e);

    // The resources this edge would claim (only complete pin pairs, matching the allocator).
    const claims: Array<{ table: Map<string, string>; key: string }> = [];
    if (e.pinned_from_port && e.pinned_to_port) {
      claims.push({ table: portOwner, key: `${e.from_node_id}:${e.pinned_from_port}` });
      claims.push({ table: portOwner, key: `${e.to_node_id}:${e.pinned_to_port}` });
    }
    if (e.pinned_from_transit_ip && e.pinned_to_transit_ip) {
      claims.push({ table: transitOwner, key: canonicalIP(e.pinned_from_transit_ip) });
      claims.push({ table: transitOwner, key: canonicalIP(e.pinned_to_transit_ip) });
    }
    if (e.pinned_from_link_local && e.pinned_to_link_local) {
      claims.push({ table: llOwner, key: canonicalIP(e.pinned_from_link_local) });
      claims.push({ table: llOwner, key: canonicalIP(e.pinned_to_link_local) });
    }

    // Phase 1: does any resource already belong to a DIFFERENT link?
    const collides = claims.some((c) => {
      const owner = c.table.get(c.key);
      return owner !== undefined && owner !== link;
    });
    if (collides) {
      healed[i] = stripPins(e); // claim nothing — the stripped edge holds no resources
      changed = true;
      continue;
    }

    // Phase 2: no collision — claim every resource for this link.
    for (const c of claims) c.table.set(c.key, link);
  }

  return changed ? healed : edges;
}
