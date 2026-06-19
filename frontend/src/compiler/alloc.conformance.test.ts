import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { compile } from './index';
import type { KeyCustody } from './index';
import { pinKey, linkKey } from './linkid';
import type { CompileResult, Edge } from './model';
import type { Topology } from '../types/topology';

// Allocation-drift conformance gate (plan-4 substep 17 — the HARD-core gate). For every SUCCESS fixture
// run the TS compile(), project the result into the SAME allocations shape the Go oracle's
// allocationsFrom produces (internal/conformance/oracle.go:179-231), and assert it === the Go golden
// manifest's `allocations` block. For the apierr FAIL fixture(s) assert TS compile() raises the SAME
// apierr code the golden's verdict.apierr carries.
//
// This is the load-bearing answer to drift_risk: every allocated value (overlay IPs, per-peer listen
// ports / transit IPs / link-locals / interface names / derived public keys) MUST be byte/value-
// identical to the Go controller. A divergence here is a port bug fixed at root — never shimmed.
//
// The Go oracle (internal/conformance) freezes one canonical manifest per fixture. The `allocations`
// projection:
//   - node_overlay_ips: nodeID -> compiled overlay IP (from result.Topology.Nodes);
//   - peers: "<linkKey>|<owner>" -> PeerAllocation, where linkKey is the bare pinKey(owner, remote) for
//     a single-link pair, or pinKey + "#" + interfaceName for a parallel pair (primary + backups). The
//     owner suffix is the PeerInfo owner's node ID. PeerAllocation drops the echoed-input fields and
//     keeps only the allocator-assigned surface.

const thisDir = dirname(fileURLToPath(import.meta.url));
// <repo>/frontend/src/compiler/alloc.conformance.test.ts -> repo root is ../../.. from src/compiler.
const repoRoot = join(thisDir, '..', '..', '..');

const successFixturesDir = join(
  repoRoot,
  'internal/localcompile/testdata/contract/topologies',
);
const successGoldenDir = join(repoRoot, 'internal/conformance/testdata/golden');
const failFixturesDir = join(repoRoot, 'internal/conformance/testdata/fail');
const failGoldenDir = join(repoRoot, 'internal/conformance/testdata/golden/fail');

// onDiskFixture is the JSON shape of a corpus fixture: an optional name, the custody string (the Go
// conformance loader resolves "airgap"|""->AirGap, "agentheld"->AgentHeld; golden_test.go:132-138), and
// the topology under test. The custody knob drives the key-custody pre-pass exactly as the Go oracle's
// CompileRequest does.
interface OnDiskFixture {
  name?: string;
  custody?: string;
  topology: Topology;
}

// resolveCustody maps the fixture's custody string to the KeyCustody the TS compile() takes, mirroring
// golden_test.go:132-138 ("airgap"|""->airgap, "agentheld"->agentheld).
function resolveCustody(s: string | undefined): KeyCustody {
  return s === 'agentheld' ? 'agentheld' : 'airgap';
}

// PeerAllocationGolden mirrors internal/conformance/manifest.go PeerAllocation field-for-field (the
// snake_case JSON keys).
interface PeerAllocationGolden {
  remote_node_id: string;
  public_key: string;
  overlay_ip: string;
  interface_name: string;
  listen_port: number;
  local_transit_ip: string;
  remote_transit_ip: string;
  local_link_local: string;
  remote_link_local: string;
}

// AllocationsGolden mirrors internal/conformance/manifest.go Allocations.
interface AllocationsGolden {
  node_overlay_ips: Record<string, string>;
  peers: Record<string, PeerAllocationGolden>;
}

interface GoldenManifest {
  verdict: { validator: string[]; apierr: string[] };
  allocations: AllocationsGolden | null;
}

// loadFixtures reads every *.json fixture in dir sorted by file name (the Go loader's stable order),
// resolving the fixture name to the explicit `name` field or the file name minus ".json".
function loadFixtures(
  dir: string,
): Array<{ name: string; fixture: OnDiskFixture }> {
  return readdirSync(dir)
    .filter((f) => f.endsWith('.json'))
    .sort()
    .map((file) => {
      const fixture = JSON.parse(
        readFileSync(join(dir, file), 'utf8'),
      ) as OnDiskFixture;
      const name =
        fixture.name && fixture.name !== ''
          ? fixture.name
          : file.replace(/\.json$/, '');
      return { name, fixture };
    });
}

// allocationsFrom projects a TS CompileResult into the Go oracle's Allocations shape, mirroring
// internal/conformance/oracle.go allocationsFrom (oracle.go:179-231) keying logic EXACTLY:
//   - node_overlay_ips from the compiled topology's nodes;
//   - pairLinkCount counts distinct enabled-edge linkKeys per node pair (so a parallel pair is
//     recognized and disambiguated by interface name; a single-link pair keys by the bare pinKey);
//   - each owner's peers key by "<linkKey>|<owner>" where linkKey is pinKey(owner, remote) or, for a
//     parallel pair, pinKey + "#" + the PeerInfo's interfaceName.
function allocationsFrom(result: CompileResult): AllocationsGolden {
  const out: AllocationsGolden = { node_overlay_ips: {}, peers: {} };

  for (const n of result.topology.nodes) {
    out.node_overlay_ips[n.id] = n.overlay_ip ?? '';
  }

  // Count distinct enabled-edge linkKeys per node pair (oracle.go:193-208).
  const pairLinkCount = new Map<string, number>();
  const seen = new Set<string>();
  for (const e of result.topology.edges as Edge[]) {
    if (!e.is_enabled) {
      continue;
    }
    const lk = linkKey(e);
    if (seen.has(lk)) {
      continue;
    }
    seen.add(lk);
    const pair = pinKey(e.from_node_id, e.to_node_id);
    pairLinkCount.set(pair, (pairLinkCount.get(pair) ?? 0) + 1);
  }

  for (const ownerID of Object.keys(result.peerMap)) {
    for (const p of result.peerMap[ownerID]) {
      const pair = pinKey(ownerID, p.nodeID);
      let lk = pair;
      if ((pairLinkCount.get(pair) ?? 0) > 1) {
        lk = pair + '#' + p.interfaceName;
      }
      out.peers[lk + '|' + ownerID] = {
        remote_node_id: p.nodeID,
        public_key: p.publicKey,
        overlay_ip: p.overlayIP,
        interface_name: p.interfaceName,
        listen_port: p.listenPort,
        local_transit_ip: p.localTransitIP,
        remote_transit_ip: p.remoteTransitIP,
        local_link_local: p.localLinkLocal,
        remote_link_local: p.remoteLinkLocal,
      };
    }
  }
  return out;
}

// readGolden loads the Go-oracle golden manifest for a fixture name from the given golden dir.
function readGolden(goldenDir: string, name: string): GoldenManifest {
  return JSON.parse(
    readFileSync(join(goldenDir, `${name}.json`), 'utf8'),
  ) as GoldenManifest;
}

// ---- SUCCESS corpus: TS compile() allocations === Go golden.allocations ----
{
  const fixtures = loadFixtures(successFixturesDir);

  describe('allocation gate: TS compile() allocations == Go golden (success corpus)', () => {
    it('reads a non-empty corpus', () => {
      expect(fixtures.length).toBeGreaterThan(0);
    });

    for (const { name, fixture } of fixtures) {
      const golden = readGolden(successGoldenDir, name);

      // The conformance success corpus is exactly the SUCCESS fixtures: skip any fixture whose golden
      // carries an apierr (a misplaced fail fixture) so this suite stays the allocation-equality gate.
      if (golden.verdict.apierr.length > 0) {
        continue;
      }

      it(`${name}`, () => {
        const result = compile(
          fixture.topology,
          resolveCustody(fixture.custody),
        );
        const got = allocationsFrom(result);
        expect(got).toEqual(golden.allocations);
      });
    }
  });
}

// ---- FAIL corpus: TS compile() raises the golden's apierr code ----
{
  const fixtures = loadFixtures(failFixturesDir);

  describe('apierr gate: TS compile() raises the Go golden apierr code (fail corpus)', () => {
    it('reads a non-empty corpus', () => {
      expect(fixtures.length).toBeGreaterThan(0);
    });

    for (const { name, fixture } of fixtures) {
      const golden = readGolden(failGoldenDir, name);

      // Only the apierr-channel fail fixtures are this gate's concern: a fixture whose verdict carries an
      // apierr code must throw a CompileError with that exact code. Validation-channel fail fixtures
      // (empty apierr, non-empty verdict.validator) are pinned by the validator conformance gate — they
      // fail compile() with the ts_topology_validation_failed sentinel (NOT a Go apierr code), so they
      // are out of scope here.
      if (golden.verdict.apierr.length === 0) {
        continue;
      }

      it(`${name}`, () => {
        let raisedCode: string | undefined;
        try {
          compile(fixture.topology, resolveCustody(fixture.custody));
        } catch (e) {
          raisedCode = (e as { code?: string }).code;
        }
        expect(raisedCode).toBeDefined();
        // The golden's apierr channel is a sorted SET; a coded compile failure surfaces exactly one
        // code, so the set has a single member here.
        expect([raisedCode]).toEqual(golden.verdict.apierr);
      });
    }
  });
}
