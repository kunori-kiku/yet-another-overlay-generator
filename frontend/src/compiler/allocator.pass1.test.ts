// Allocator + peer-derivation Pass-1 conformance sanity (subject-scoped S1).
//
// Pins frontend/src/compiler/allocator.ts (allocateIPs) + peers.ts (derivePass1) value-for-value
// against the Go oracle's frozen golden allocations (internal/conformance/testdata/golden/<name>.json,
// projected by oracle.go allocationsFrom). The fixtures are copied verbatim from plan-3's contract
// corpus (internal/localcompile/testdata/contract/topologies) + the matching golden allocation slices.
//
// This is the early local guard for the HARD core: a wrong overlay IP / port / transit IP / link-local
// silently disagrees with the controller (worse than a crash). The full conformance harness (plan-5)
// subsumes this once the whole pipeline (Pass 2 + renderers) lands; until then this catches a Pass-1
// value divergence at the substep boundary. Retirement → tests/legacy/ts-compiler/ when S1 closes.
//
// Orientation matching without Pass-2 keying: transit IPs are globally unique per pool, so each golden
// directed-interface entry is indexed by its local_transit_ip. For each derived link, the entry whose
// local_transit_ip === alloc.localTransit MUST carry the from-side port + LLs; the entry whose
// local_transit_ip === alloc.remoteTransit MUST carry the to-side port + LLs. That checks the full
// oriented PairAllocation (ports + transit pair + link-local pair) exactly.

import { describe, expect, it } from 'vitest';
import { allocateIPs } from './allocator';
import { derivePass1, type PairAllocation } from './peers';
import type { Topology } from './model';

import singlePrimaryInput from './testdata/pass1_input_single-primary-link.json';
import singlePrimaryGolden from './testdata/pass1_golden_single-primary-link.json';
import multiDomainInput from './testdata/pass1_input_multi-domain.json';
import multiDomainGolden from './testdata/pass1_golden_multi-domain.json';
import pinnedInput from './testdata/pass1_input_pinned-pins-roundtrip.json';
import pinnedGolden from './testdata/pass1_golden_pinned-pins-roundtrip.json';
import backupInput from './testdata/pass1_input_backup-link.json';
import backupGolden from './testdata/pass1_golden_backup-link.json';

// GoldenPeer is one directed interface entry in a golden's allocations.peers map (oracle.go
// PeerAllocation). Only the Pass-1 resources are asserted here (ports + transit pair + LL pair);
// public key / interface name / overlay IP are Pass-2 surfaces verified by the full harness.
interface GoldenPeer {
  remote_node_id: string;
  listen_port: number;
  local_transit_ip: string;
  remote_transit_ip: string;
  local_link_local: string;
  remote_link_local: string;
}

interface GoldenAllocations {
  node_overlay_ips: Record<string, string>;
  peers: Record<string, GoldenPeer>;
}

// indexByLocalTransit maps each golden directed interface to its local_transit_ip. Transit IPs are
// globally unique per pool (the allocator never reuses one), so this is an unambiguous index for
// orientation matching against the derived alloc.
function indexByLocalTransit(g: GoldenAllocations): Map<string, GoldenPeer> {
  const out = new Map<string, GoldenPeer>();
  for (const key of Object.keys(g.peers)) {
    const p = g.peers[key];
    out.set(p.local_transit_ip, p);
  }
  return out;
}

function runCase(name: string, input: unknown, golden: GoldenAllocations): void {
  describe(`Pass 1 conformance: ${name}`, () => {
    const topo = input as Topology;

    it('allocateIPs reproduces the golden node overlay IPs', () => {
      const nodes = allocateIPs(topo);
      const got: Record<string, string> = {};
      for (const n of nodes) {
        got[n.id] = n.overlay_ip ?? '';
      }
      expect(got).toEqual(golden.node_overlay_ips);
    });

    it('derivePass1 reproduces every golden per-link allocation (ports + transit + link-local), oriented', () => {
      // Pass 1 derives off the ALLOCATED topology (overlay IPs are unrelated to ports/transit, but Go
      // runs allocation first, so mirror the pipeline order for a faithful value).
      const allocated = allocateIPs(topo);
      const result = derivePass1({ ...topo, nodes: allocated });

      const byLocalTransit = indexByLocalTransit(golden);

      // Collect the distinct per-link allocations (skip the directed "from->to" aliases, which point at
      // the same object as the linkKey entry — dedup by reference).
      const seen = new Set<PairAllocation>();
      const linkAllocs: PairAllocation[] = [];
      for (const link of result.links) {
        const a = result.allocations.get(link.linkKey);
        expect(a, `alloc for linkKey ${link.linkKey}`).toBeDefined();
        if (a !== undefined && !seen.has(a)) {
          seen.add(a);
          linkAllocs.push(a);
        }
      }

      // Every golden transit IP must be claimed by exactly one derived link side, and the oriented
      // resources must match. Track which golden entries we matched to assert completeness.
      const matched = new Set<string>();

      for (const a of linkAllocs) {
        // A client-touching link keeps the client side's port at 0 and is not represented as a peer
        // entry on the client; these fixtures are router-router (no client side), so both sides exist.
        const fromEntry = byLocalTransit.get(a.localTransit);
        const toEntry = byLocalTransit.get(a.remoteTransit);
        expect(fromEntry, `golden entry for localTransit ${a.localTransit}`).toBeDefined();
        expect(toEntry, `golden entry for remoteTransit ${a.remoteTransit}`).toBeDefined();
        if (fromEntry === undefined || toEntry === undefined) continue;

        // From side (owned by alloc.fromNodeID): its local transit/LL + listen port match the alloc.
        expect(fromEntry.remote_node_id).toBe(a.toNodeID);
        expect(fromEntry.listen_port).toBe(a.fromPort);
        expect(fromEntry.local_transit_ip).toBe(a.localTransit);
        expect(fromEntry.remote_transit_ip).toBe(a.remoteTransit);
        expect(fromEntry.local_link_local).toBe(a.localLL);
        expect(fromEntry.remote_link_local).toBe(a.remoteLL);

        // To side (owned by alloc.toNodeID): resources are mirrored.
        expect(toEntry.remote_node_id).toBe(a.fromNodeID);
        expect(toEntry.listen_port).toBe(a.toPort);
        expect(toEntry.local_transit_ip).toBe(a.remoteTransit);
        expect(toEntry.remote_transit_ip).toBe(a.localTransit);
        expect(toEntry.local_link_local).toBe(a.remoteLL);
        expect(toEntry.remote_link_local).toBe(a.localLL);

        matched.add(a.localTransit);
        matched.add(a.remoteTransit);
      }

      // Completeness: every golden directed interface was matched by a derived link side (no golden
      // allocation left unexplained, no extra derived allocation).
      expect([...matched].sort()).toEqual([...byLocalTransit.keys()].sort());
    });
  });
}

runCase('single-primary-link', singlePrimaryInput, singlePrimaryGolden as GoldenAllocations);
runCase('multi-domain', multiDomainInput, multiDomainGolden as GoldenAllocations);
runCase('pinned-pins-roundtrip', pinnedInput, pinnedGolden as GoldenAllocations);
runCase('backup-link', backupInput, backupGolden as GoldenAllocations);
