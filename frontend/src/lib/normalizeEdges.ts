import type { Edge } from '../types/topology';

// healDuplicatePinnedBackups repairs the legacy "backup edge carrying its sibling primary's
// allocation pins" corruption. A role:'backup' edge is a DISTINCT link from its same-pair primary
// (the compiler keys a backup as `<pair>#<edgeID>`, separate from the primary's `<pair>`), so it must
// own a SEPARATE allocation — a distinct transit /30, listen port, and link-locals. Older builds'
// EdgeEditor flipped an already-compiled edge primary->backup while clearing only compiled_port, so
// the backup kept the primary's exact pins (e.g. transit 10.10.0.1/.2, port 51820, fe80::1/::2). The
// semantic validator then (correctly) reports "transit IP / port / link-local pin occupied by two
// different links". We strip such a backup's allocation pins so it re-allocates fresh on the next
// compile; a backup that already owns DISTINCT pins is left untouched (no allocation churn). Pure and
// idempotent — returns the same array reference when nothing needs healing.
const PIN_FIELDS = [
  'compiled_port',
  'pinned_from_port',
  'pinned_to_port',
  'pinned_from_transit_ip',
  'pinned_to_transit_ip',
  'pinned_from_link_local',
  'pinned_to_link_local',
] as const;

// samePair: the two edges connect the same unordered node pair (direction-agnostic, matching how the
// compiler folds A->B and B->A of a primary-class link into one bidirectional tunnel).
function samePair(a: Edge, b: Edge): boolean {
  return (
    (a.from_node_id === b.from_node_id && a.to_node_id === b.to_node_id) ||
    (a.from_node_id === b.to_node_id && a.to_node_id === b.from_node_id)
  );
}

function transitIPs(e: Edge): string[] {
  return [e.pinned_from_transit_ip, e.pinned_to_transit_ip].filter((v): v is string => !!v);
}

function stripPins(e: Edge): Edge {
  const out: Edge = { ...e };
  for (const f of PIN_FIELDS) delete (out as Record<string, unknown>)[f];
  return out;
}

export function healDuplicatePinnedBackups(edges: Edge[]): Edge[] {
  const primaries = edges.filter((e) => e.role !== 'backup');
  if (primaries.length === edges.length) return edges; // no backups -> nothing to heal
  let changed = false;
  const healed = edges.map((e) => {
    if (e.role !== 'backup') return e;
    const mine = transitIPs(e);
    if (mine.length === 0) return e; // no transit pins -> nothing stale to collide
    const collides = primaries.some(
      (p) => samePair(p, e) && transitIPs(p).some((t) => mine.includes(t)),
    );
    if (!collides) return e;
    changed = true;
    return stripPins(e);
  });
  return changed ? healed : edges;
}
