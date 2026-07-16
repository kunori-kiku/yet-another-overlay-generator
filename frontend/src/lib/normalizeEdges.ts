import { linkKey } from './linkid';
import type { Edge, Node, Topology } from '../types/topology';
import { SERVER_ALLOCATION_FIELDS } from './allocationFields';

export { PERSISTED_ALLOCATION_PIN_FIELDS, SERVER_ALLOCATION_FIELDS } from './allocationFields';

// healCollidingPins is the browser-side migration mirror of Go normalize.HealCollidingPins. It:
//   - removes only a port attached to a client endpoint, preserving the valid non-client-side port,
//     full transit/link-local pairs, and compiled_port; and
//   - repairs the "pin occupied by two different links" corruption by stripping all seven
//     server-derived allocation fields from the colliding edge so it re-allocates fresh.
//
// The cross-link pass is the inverse of the semantic validator's dedup. Historical corruption
// sources covered are:
//   - a role change that left a now-client endpoint carrying its old per-link listen port;
//   - a backup edge that kept its sibling primary's pins (legacy EdgeEditor role-flip bug), and
//   - two DIFFERENT links (cross-pair, or a pair's primary vs a new same-pair link) handed the same
//     allocation by successive incremental-enrollment subgraph compiles (the controller root cause).
//
// It mirrors the validator/Go heal precisely:
//   - an invalid client-endpoint port is cleared even on disabled edges, so a later enable cannot
//     revive corruption;
//   - disabled edges are skipped by the cross-link collision pass, while valid client-link
//     allocations participate like the backend validator/allocator;
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

// clearedPinFields returns the allocation-clear payload: an object mapping every
// SERVER_ALLOCATION_FIELDS entry to undefined (keys PRESENT with undefined values, so spreading it over an edge — or passing it to
// updateEdge — actually resets those fields, exactly as the prior hand-written `{ compiled_port:
// undefined, ... }` literals did). It is the ONE definition of "clear the allocation pins", routed
// through by all three deliberate-reset sites (EdgeEditor's role-change clear + unpin clear, and the
// store's purgeModeBoundaryState edge scrub), so adding a field to SERVER_ALLOCATION_FIELDS clears
// it everywhere automatically instead of drifting across three literals.
//
// SCOPE NOTE: this covers ONLY the seven server-derived edge allocation fields: the six persisted
// sticky pins plus compiled_port. purgeModeBoundaryState ALSO scrubs
// NODE-SECRET fields (wireguard_private_key, wireguard_public_key, fixed_private_key, overlay_ip) —
// that is a SEPARATE concern and stays its own explicit list there; node secrets are intentionally
// NOT folded into SERVER_ALLOCATION_FIELDS. And edgeDirection.ts's flipEdge enumerates the same pin
// set as a SWAP
// map (pinned_from_* ⇄ pinned_to_*) — a different shape (mirror, not clear), so it is intentionally
// not expressed through this helper; if SERVER_ALLOCATION_FIELDS grows, that swap map is the related
// site to revisit.
export function clearedPinFields(): Partial<Edge> {
  const cleared: Partial<Edge> = {};
  // Assign (not delete) so the keys are PRESENT with an undefined value, matching the literals these
  // sites used. Route the index through unknown — Edge does not structurally overlap Record<string,
  // unknown> (tsc -b TS2352); the keys are the literal, all-optional server allocation fields, so
  // it is well-formed.
  for (const f of SERVER_ALLOCATION_FIELDS) {
    (cleared as unknown as Record<string, unknown>)[f] = undefined;
  }
  return cleared;
}

// pinKey / linkKey are the canonical link-identity functions, lifted to ./linkid.ts (the single
// source of truth mirroring internal/linkid). This module imports linkKey from there; the prior
// in-file duplicates were retired. pinKey is no longer referenced here directly (linkKey folds it
// in), so it is not re-imported.

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
  // all-optional server allocation fields, so the delete is well-formed.
  for (const f of SERVER_ALLOCATION_FIELDS) {
    delete (out as unknown as Record<string, unknown>)[f];
  }
  return out;
}

export function healCollidingPins(edges: Edge[], nodes: Node[]): Edge[] {
  const roleByNode = new Map<string, string>();
  for (const n of nodes) roleByNode.set(n.id, n.role);

  const healed = edges.slice(); // identity refs until a migration replaces one; discarded if unchanged
  let changed = false;
  for (let i = 0; i < edges.length; i++) {
    const edge = edges[i];
    let next = edge;
    if (roleByNode.get(edge.from_node_id) === 'client' && edge.pinned_from_port !== undefined) {
      next = { ...next };
      delete (next as unknown as Record<string, unknown>).pinned_from_port;
    }
    if (roleByNode.get(edge.to_node_id) === 'client' && edge.pinned_to_port !== undefined) {
      if (next === edge) next = { ...next };
      delete (next as unknown as Record<string, unknown>).pinned_to_port;
    }
    if (next !== edge) {
      healed[i] = next;
      changed = true;
    }
  }

  // Process enabled edges in reserve-first (linkKey-SORTED) order — the same priority
  // the Go allocator's gap-fill uses — so the kept claimant of a contested slot is the smaller-linkKey
  // (reserve-first) owner, not whichever edge comes first in array order. Disabled edges are skipped
  // entirely (Go's bool zero treats a missing is_enabled as DISABLED, so require strictly true).
  // We sort INDICES (ties broken by original index for determinism — a primary class's
  // forward/reverse edges share a linkKey and never collide with each other) so the healed result
  // stays in the caller's original edge order. Mirrors internal/normalize/pins.go.
  const order: number[] = [];
  for (let i = 0; i < edges.length; i++) {
    const e = edges[i];
    if (e.is_enabled !== true) continue;
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

  for (const i of order) {
    const e = edges[i];
    const link = linkKey(e);

    // The resources this edge would claim. Ordinary ports and all address allocations require
    // complete pairs; a client link deliberately claims its one non-client-side port alone.
    const claims: Array<{ table: Map<string, string>; key: string }> = [];
    const fromClient = roleByNode.get(e.from_node_id) === 'client';
    const toClient = roleByNode.get(e.to_node_id) === 'client';
    if (fromClient && !toClient && e.pinned_to_port !== undefined && e.pinned_to_port > 0) {
      claims.push({ table: portOwner, key: `${e.to_node_id}:${e.pinned_to_port}` });
    } else if (toClient && !fromClient && e.pinned_from_port !== undefined && e.pinned_from_port > 0) {
      claims.push({ table: portOwner, key: `${e.from_node_id}:${e.pinned_from_port}` });
    } else if (
      !fromClient &&
      !toClient &&
      e.pinned_from_port !== undefined &&
      e.pinned_from_port > 0 &&
      e.pinned_to_port !== undefined &&
      e.pinned_to_port > 0
    ) {
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

// normalizeTopologyForCanvas applies every edge migration used by loadTopology while preserving
// the original topology reference on a no-op. Controller synchronization uses this before recording
// a server/save baseline, so the baseline and the normalized canvas can never diverge.
export function normalizeTopologyForCanvas(topo: Topology): Topology {
  const healed = healCollidingPins(topo.edges, topo.nodes);
  const edges = sanitizeLinkDirection(healed);
  return edges === topo.edges ? topo : { ...topo, edges };
}

// sanitizeLinkDirection coerces an out-of-enum link_direction to undefined (≡ "both") on every
// topology load path (file import, server hydrate, localStorage rehydrate) — the owner's
// "existing configs auto-convert, remember sanitizing" contract. Recognized values pass through
// untouched: '', 'both', 'forward' (and an absent field). Anything else — a garbled hand-edit, a
// value from a future/foreign schema, or a never-released 'reverse' (dropped by design decision
// D11: one spelling; single-linking the other way is expressed by flipping the edge) — is dropped
// so the edge falls back to today's doubly-linked behavior instead of tripping the schema
// validator on someone else's stored data. (The validator still rejects invalid values arriving
// via compile/deploy inputs; this sanitize covers only the panel's own load paths, mirroring
// healCollidingPins' placement.)
//
// Pure and idempotent: returns the SAME array reference when nothing needs coercing (no
// React/zustand churn), otherwise a new array with the offending edges' field removed.
export function sanitizeLinkDirection(edges: Edge[]): Edge[] {
  const valid = new Set(['both', 'forward']);
  let changed = false;
  const out = edges.map((e) => {
    const dir = (e as { link_direction?: unknown }).link_direction;
    if (dir === undefined || dir === '' || (typeof dir === 'string' && valid.has(dir))) {
      return e;
    }
    changed = true;
    const clean = { ...e };
    delete (clean as unknown as Record<string, unknown>).link_direction;
    return clean;
  });
  return changed ? out : edges;
}
