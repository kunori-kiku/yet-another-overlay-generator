import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { compile } from './index';
import type { KeyCustody } from './index';
import type { Topology } from '../types/topology';

// Renderer byte-equality conformance gate (plan-4 Phase 4, substeps 18-21). For every SUCCESS fixture
// it drives compile() (which wires the Phase-4 WireGuard / Babel / sysctl renderers into the
// CompileResult exactly as render.AllWith does), then asserts each rendered file === the Go golden's
// files[nodeID][relpath] BYTE-FOR-BYTE.
//
// This is the load-bearing renderer drift gate: a single byte diff is a port bug fixed at root, never
// shimmed. The babel C1 edge-reorder fixtures (edge-reorder-forward / edge-reorder-reversed) MUST
// produce byte-identical babeld.conf — they pin the sort-by-interface-name stability.
//
// Path mapping (mirrors internal/artifacts/export.go BundleFiles):
//   - wireguard/<iface>.conf  <- renderAllWireGuardConfigs keyed "nodeID:iface" (+ client wg0 keyed
//     "nodeID:wg0", added the same way render.go AllWith does at render.go:313-320);
//   - babel/babeld.conf       <- renderAllBabelConfigs[nodeID] (non-client nodes only);
//   - sysctl/99-overlay.conf  <- renderAllSysctlConfigs[nodeID].
// The install.sh / artifacts.json / deploy files are out of scope for THIS step (script + deploy +
// export are substeps 22-25).

const thisDir = dirname(fileURLToPath(import.meta.url));
// <repo>/frontend/src/compiler/renderers.conformance.test.ts -> repo root is ../../.. from src/compiler.
const repoRoot = join(thisDir, '..', '..', '..');

const successFixturesDir = join(
  repoRoot,
  'internal/localcompile/testdata/contract/topologies',
);
const successGoldenDir = join(repoRoot, 'internal/conformance/testdata/golden');

interface OnDiskFixture {
  name?: string;
  custody?: string;
  topology: Topology;
}

interface GoldenManifest {
  verdict: { validator: string[]; apierr: string[] };
  files: Record<string, Record<string, string>> | null;
  // deploy carries the project-level deploy scripts (deploy-all.sh / deploy-all.ps1) — name->content.
  deploy: Record<string, string> | null;
  // signatures is populated only when the fixture opts into bundle signing (signing-on). A signed
  // bundle's install.sh embeds the SigningPubkeyPEM verify block, which local-mode (no signer) does NOT
  // reproduce — so install.sh byte-equality is SKIPPED for those fixtures (the splice block IS
  // reproduced via custody, but the signing block is plan-4 out-of-scope per the io-contract).
  signatures: Record<string, string> | null;
}

// resolveCustody mirrors golden_test.go:132-138 ("airgap"|""->airgap, "agentheld"->agentheld).
function resolveCustody(s: string | undefined): KeyCustody {
  return s === 'agentheld' ? 'agentheld' : 'airgap';
}

// loadFixtures reads every *.json fixture in dir sorted by file name (the Go loader's stable order).
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

function readGolden(name: string): GoldenManifest {
  return JSON.parse(
    readFileSync(join(successGoldenDir, `${name}.json`), 'utf8'),
  ) as GoldenManifest;
}

// renderBundleFilesFrom reshapes a compile() result's rendered-config maps into the per-node
// nodeID -> relpath -> content shape the Go golden's `files` block uses, mirroring the subset of
// internal/artifacts/export.go BundleFiles this step owns (wireguard/<iface>.conf [incl. wg0],
// babel/babeld.conf, sysctl/99-overlay.conf). The WireGuard map is keyed "nodeID:iface" (per-peer) and
// "nodeID:wg0" (client) — split on the first ':' into the node ID and the interface name.
function renderBundleFilesFrom(
  result: ReturnType<typeof compile>,
): Record<string, Record<string, string>> {
  const out: Record<string, Record<string, string>> = {};
  const ensure = (nodeID: string): Record<string, string> => {
    if (out[nodeID] === undefined) {
      out[nodeID] = {};
    }
    return out[nodeID];
  };

  for (const configKey of Object.keys(result.wireGuardConfigs)) {
    const idx = configKey.indexOf(':');
    const nodeID = configKey.slice(0, idx);
    const iface = configKey.slice(idx + 1);
    ensure(nodeID)['wireguard/' + iface + '.conf'] =
      result.wireGuardConfigs[configKey];
  }
  for (const nodeID of Object.keys(result.babelConfigs)) {
    ensure(nodeID)['babel/babeld.conf'] = result.babelConfigs[nodeID];
  }
  for (const nodeID of Object.keys(result.sysctlConfigs)) {
    ensure(nodeID)['sysctl/99-overlay.conf'] = result.sysctlConfigs[nodeID];
  }
  for (const nodeID of Object.keys(result.installScripts)) {
    ensure(nodeID)['install.sh'] = result.installScripts[nodeID];
  }
  return out;
}

// The relpaths the renderers produce: wireguard/<iface>.conf (incl. wg0), babel/babeld.conf,
// sysctl/99-overlay.conf, and install.sh (substep 22). artifacts.json is excluded — it is omitted in
// local mode (no catalog → D4) and is the export substep's concern, never rendered here.
const RENDERED_RELPATHS = new Set([
  'babel/babeld.conf',
  'sysctl/99-overlay.conf',
  'install.sh',
]);
const isRenderedRelpath = (relpath: string): boolean =>
  RENDERED_RELPATHS.has(relpath) || relpath.startsWith('wireguard/');

const fixtures = loadFixtures(successFixturesDir);

describe('renderer gate: TS renderers == Go golden files (wireguard + babel + sysctl)', () => {
  it('reads a non-empty corpus', () => {
    expect(fixtures.length).toBeGreaterThan(0);
  });

  for (const { name, fixture } of fixtures) {
    const golden = readGolden(name);

    // Only the SUCCESS corpus carries a `files` set; skip an apierr/validation fail fixture.
    if (golden.verdict.apierr.length > 0 || golden.files === null) {
      continue;
    }

    // A bundle-signing fixture (signatures present) embeds the SigningPubkeyPEM verify block in its
    // golden install.sh — local mode has no signer, so install.sh CANNOT match byte-for-byte (signing is
    // a plan-4-scoped NO-OP). Drop install.sh from the comparison for those fixtures; every other
    // rendered file (incl. the AgentHeld splice, which local mode DOES reproduce) is still pinned.
    const skipInstallSh =
      golden.signatures !== null && Object.keys(golden.signatures).length > 0;
    const compareRelpath = (relpath: string): boolean =>
      isRenderedRelpath(relpath) && !(skipInstallSh && relpath === 'install.sh');

    it(`${name}`, () => {
      const result = compile(fixture.topology, resolveCustody(fixture.custody));
      const rendered = renderBundleFilesFrom(result);
      const goldenFiles = golden.files as Record<
        string,
        Record<string, string>
      >;

      // For every golden node, assert every rendered relpath (wg/babel/sysctl/install.sh) matches
      // byte-for-byte, AND that the TS produced exactly the same SET of rendered relpaths the golden
      // carries for that node (no missing / extra wg interface).
      for (const nodeID of Object.keys(goldenFiles)) {
        const goldenNode = goldenFiles[nodeID];
        const renderedNode = rendered[nodeID] ?? {};

        const goldenRendered = Object.keys(goldenNode)
          .filter(compareRelpath)
          .sort();
        const tsRendered = Object.keys(renderedNode)
          .filter(compareRelpath)
          .sort();
        expect(tsRendered, `node ${nodeID} rendered relpath set`).toEqual(
          goldenRendered,
        );

        for (const relpath of goldenRendered) {
          expect(
            renderedNode[relpath],
            `node ${nodeID} file ${relpath}`,
          ).toBe(goldenNode[relpath]);
        }
      }

      // Project-level deploy scripts (deploy-all.sh / deploy-all.ps1) — byte-equal for ALL success
      // fixtures (deploy is signing-independent, so signed fixtures are included here).
      if (golden.deploy !== null) {
        for (const name of Object.keys(golden.deploy)) {
          expect(
            result.deployScripts[name],
            `deploy ${name}`,
          ).toBe(golden.deploy[name]);
        }
      }
    });
  }
});
