// Link-identity authority — the single source of truth for how an Edge maps to a stable link key.
//
// This is the TypeScript mirror of internal/linkid/linkid.go (PinKey :28, LinkKey :53, IsBackup :64).
// It survives the plan-5 TS-compiler deletion because it is FE heal logic, NOT the compiler: the
// browser-side normalizeEdges.healCollidingPins needs the SAME link identity the Go allocator/heal
// use, so this one place encodes the rule, byte-identical to the Go oracle.
//
// It subsumes the historical pinKey/linkKey that lived inline in ./normalizeEdges.ts — those were a
// hand-mirror of the same rule; normalizeEdges.ts imports them from here (the duplicate is retired).
// The heal-conformance canary (./heal.conformance.test.ts) pins this byte-equal to the Go heal.

import type { Edge } from '../types/topology';

// pinKey computes a link's canonical identity: the two node IDs sorted and joined with "|". It is
// direction-agnostic — pinKey(A, B) === pinKey(B, A) — so reversing an edge's draw direction does not
// change its allocation identity (I3). Mirrors linkid.PinKey (linkid.go:28).
export function pinKey(a: string, b: string): string {
  return a <= b ? `${a}|${b}` : `${b}|${a}`;
}

// linkKey computes the link identity of a single edge, generalizing pinKey from the node pair to the
// edge so that parallel links (a pair carrying one primary link plus backups) are distinguishable.
//
//   linkKey(e) = pinKey(from, to)            // e.role !== "backup"  (primary class)
//   linkKey(e) = pinKey(from, to) + "#" + e.id   // e.role === "backup"
//
// All enabled non-backup edges of a pair share the same linkKey (they collapse to one primary link
// under the unify rule); every backup edge gets a distinct linkKey keyed by its own edge ID. Mirrors
// linkid.LinkKey (linkid.go:53). An empty/absent role is primary class (matches the Go EdgeRoleBackup
// "backup" comparison, where empty and "primary" both fall through to the pair key).
export function linkKey(e: Edge): string {
  const pair = pinKey(e.from_node_id, e.to_node_id);
  return e.role === 'backup' ? `${pair}#${e.id}` : pair;
}

// isBackup reports whether an edge is a backup link (its own discriminated link identity), i.e.
// e.role === "backup". Empty role and "primary" are both primary class and return false. Mirrors
// linkid.IsBackup (linkid.go:64).
export function isBackup(e: Edge): boolean {
  return e.role === 'backup';
}
