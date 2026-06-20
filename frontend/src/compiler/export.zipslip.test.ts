import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import { exportArtifacts } from './export';
import type { Topology } from '../types/topology';

// Zip-slip / path-traversal guard parity (plan-21 / 4.2 finding, N1 invariant). The schema name-charset
// validator (validator.ts) ALLOWS '.', so a node named "." / ".." / "a..b" passes validation — the Go
// exporter applies a SECOND export-time guard (internal/artifacts/export.go validateSafeName,
// CodeExportUnsafeName) and the TS port must match it, or the browser exporter would be security-weaker
// than Go (a malicious shared-project node name could write a ZIP entry outside its node directory).
// Black-box: drive exportArtifacts() over a real SUCCESS fixture with the first node renamed.

const thisDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(thisDir, '..', '..', '..');
const topoDir = join(repoRoot, 'internal/localcompile/testdata/contract/topologies');

// The contract fixtures wrap the topology: { name, doc, custody, signing, topology }.
function aSuccessTopology(): Topology {
  const file = readdirSync(topoDir).find((n) => n.endsWith('.json'));
  if (!file) throw new Error(`no fixture topology under ${topoDir}`);
  const fixture = JSON.parse(readFileSync(join(topoDir, file), 'utf8')) as { topology: Topology };
  return fixture.topology;
}

describe('export zip-slip guard (plan-21 / 4.2)', () => {
  // These all PASS the name-charset validator (alphanumerics + dots) yet are unsafe ZIP path
  // components — exactly the gap the export-time guard closes.
  for (const unsafe of ['..', '.', 'a..b']) {
    it(`rejects the charset-valid but path-unsafe node name ${JSON.stringify(unsafe)}`, async () => {
      const topo = aSuccessTopology();
      topo.nodes[0].name = unsafe;
      await expect(exportArtifacts(topo)).rejects.toThrow(/unsafe|path separator|\.\./);
    });
  }

  it('exports cleanly when node names are safe', async () => {
    const topo = aSuccessTopology();
    const blob = await exportArtifacts(topo);
    expect(blob.size).toBeGreaterThan(0);
  });
});
