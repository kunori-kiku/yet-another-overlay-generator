import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { buildChecksums, buildFiles, compile } from './index';
import type { KeyCustody } from './index';
import type { Topology } from '../types/topology';

// Full bundle byte-equality conformance gate (plan-4 Phase 4, substeps 24-25). For every SUCCESS fixture
// it drives compile() then the export builders (buildFiles / buildChecksums, mirroring
// internal/artifacts/export.go BundleFiles + internal/bundlesig/bundlesig.go Canonicalize) and asserts:
//   - the COMPLETE per-node files map === golden.files (every relpath present + byte-equal — the full
//     bundle file SET, not just the renderer subset);
//   - the per-node checksums.sha256 === golden.checksums (the canonical checksums output);
//   - the project-level deploy scripts === golden.deploy.
//
// This is the load-bearing full files+checksums+deploy drift gate: a single byte diff in any file (or any
// hash line) reds the harness — that IS the gate. It is the export-layer complement to
// renderers.conformance.test.ts (which pins the rendered files individually); together they pin the whole
// bundle the Go air-gap exporter writes.
//
// SIGNING EXCLUSION (plan-4 out-of-scope): a bundle-signing fixture (golden.signatures non-empty) embeds
// the SigningPubkeyPEM verify block in its golden install.sh — local mode has no signer, so install.sh
// CANNOT match byte-for-byte (signing is a documented NO-OP). Because install.sh is a member of the
// checksummed set, the golden's checksums.sha256 (computed over the SIGNED install.sh) likewise differs.
// So for a signing fixture both install.sh AND the whole node checksums are skipped, exactly the way
// renderers.conformance.test.ts skips install.sh. Every OTHER file (incl. the AgentHeld splice block,
// which local mode DOES reproduce) and the deploy scripts stay pinned.

const thisDir = dirname(fileURLToPath(import.meta.url));
// <repo>/frontend/src/compiler/export.conformance.test.ts -> repo root is ../../.. from src/compiler.
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
  checksums: Record<string, string> | null;
  deploy: Record<string, string> | null;
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

const fixtures = loadFixtures(successFixturesDir);

describe('export gate: TS bundle == Go golden (files + checksums + deploy)', () => {
  it('reads a non-empty corpus', () => {
    expect(fixtures.length).toBeGreaterThan(0);
  });

  for (const { name, fixture } of fixtures) {
    const golden = readGolden(name);

    // Only the SUCCESS corpus carries a files/checksums set; skip an apierr/validation fail fixture.
    if (
      golden.verdict.apierr.length > 0 ||
      golden.files === null ||
      golden.checksums === null
    ) {
      continue;
    }

    const isSigning =
      golden.signatures !== null && Object.keys(golden.signatures).length > 0;

    it(`${name}`, () => {
      const result = compile(fixture.topology, resolveCustody(fixture.custody));
      const tsFiles = buildFiles(result);
      const tsChecksums = buildChecksums(result);

      const goldenFiles = golden.files as Record<
        string,
        Record<string, string>
      >;
      const goldenChecksums = golden.checksums as Record<string, string>;

      // The complete per-node bundle file set must match the golden node-for-node.
      expect(Object.keys(tsFiles).sort(), 'bundle node set').toEqual(
        Object.keys(goldenFiles).sort(),
      );

      for (const nodeID of Object.keys(goldenFiles)) {
        const goldenNode = goldenFiles[nodeID];
        const tsNode = tsFiles[nodeID] ?? {};

        // For a signing fixture install.sh is excluded (local mode emits no signing block); for every
        // other fixture the full relpath set (incl. install.sh) must match exactly — no missing/extra
        // wireguard interface, no missing babel/sysctl/artifacts member.
        const wanted = (relpath: string): boolean =>
          !(isSigning && relpath === 'install.sh');

        const goldenRelpaths = Object.keys(goldenNode).filter(wanted).sort();
        const tsRelpaths = Object.keys(tsNode).filter(wanted).sort();
        expect(tsRelpaths, `node ${nodeID} bundle relpath set`).toEqual(
          goldenRelpaths,
        );

        for (const relpath of goldenRelpaths) {
          expect(tsNode[relpath], `node ${nodeID} file ${relpath}`).toBe(
            goldenNode[relpath],
          );
        }

        // checksums.sha256: the canonical content over the SAME members. A signing fixture's golden
        // checksums cover the SIGNED install.sh (a byte local mode cannot reproduce), so the whole node
        // checksums block is skipped for those — pinned for every other fixture.
        if (!isSigning) {
          expect(tsChecksums[nodeID], `node ${nodeID} checksums.sha256`).toBe(
            goldenChecksums[nodeID],
          );
        }
      }

      // Project-level deploy scripts (deploy-all.sh / deploy-all.ps1) — byte-equal for ALL success
      // fixtures (deploy is signing-independent).
      if (golden.deploy !== null) {
        for (const deployName of Object.keys(golden.deploy)) {
          expect(result.deployScripts[deployName], `deploy ${deployName}`).toBe(
            golden.deploy[deployName],
          );
        }
      }
    });
  }
});
