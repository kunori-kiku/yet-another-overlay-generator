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
//   - the FIRST edge (in array order) to claim a value keeps it; a later, different-link edge that
//     needs any already-claimed value is stripped as a whole (an allocation is a unit).
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

// pinKey is direction-agnostic (A|B === B|A), matching how the compiler folds a primary-class link's
// A->B and B->A into one bidirectional tunnel.
function pinKey(a: string, b: string): string {
  return a <= b ? `${a}|${b}` : `${b}|${a}`;
}

// linkKey is the per-edge link identity: a backup edge is its own link (pair + "#id"); every other
// edge of a pair shares the pair key. Mirrors internal/linkid.LinkKey.
function linkKey(e: Edge): string {
  const pair = pinKey(e.from_node_id, e.to_node_id);
  return e.role === 'backup' ? `${pair}#${e.id}` : pair;
}

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

  // Claim tables: each maps a resource to the linkKey that first claimed it.
  const portOwner = new Map<string, string>(); // `${nodeId}:${port}` -> linkKey
  const transitOwner = new Map<string, string>(); // ip -> linkKey
  const llOwner = new Map<string, string>(); // ip -> linkKey

  let changed = false;
  const healed = edges.map((e) => {
    // Match Go's zero-value semantics: a missing is_enabled (undefined, e.g. in a hand-edited import)
    // is DISABLED, like Go's bool zero — so skip unless strictly true. (=== false would wrongly treat
    // undefined as enabled and could claim/strip on a logically-disabled edge.)
    if (e.is_enabled !== true || isClientTouched(e)) return e;
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
      changed = true;
      return stripPins(e); // claim nothing — the stripped edge holds no resources
    }

    // Phase 2: no collision — claim every resource for this link.
    for (const c of claims) c.table.set(c.key, link);
    return e;
  });

  return changed ? healed : edges;
}
