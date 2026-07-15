// @vitest-environment node
//
// wasmEngine.test.ts — load/roundtrip pin for the opt-in in-browser Go/WASM engine
// (framework-refactor plan-3). PERPETUAL.
//
// It proves wasmEngine.ts actually drives the real web/yaog.wasm end-to-end: it builds the wasm
// (if absent), instantiates it via the toolchain wasm_exec.js so globalThis.yaog is present, then
// calls the module's public surface (validate / compile / deployScripts / export) over a real
// corpus topology and asserts the FE shapes come back. Because it sets globalThis.yaog BEFORE the
// module's ensureWasm() runs, wasmEngine.ts reuses that instance and never touches the DOM fetch
// path — so this exercises the shaping + the shim bridge, not the browser loader.
//
// It NEVER silently skips (plan-0 killed green-by-invisibility): a missing toolchain or a broken
// wasm build FAILS the suite. The byte-for-byte WASM==Go proof is the separate headless gate
// (scripts/wasm-conformance-gate.mjs); this is the FE-integration half.

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';
import { beforeAll, describe, expect, it } from 'vitest';

import type { Topology } from '../types/topology';
import { compile, deployScripts, exportArtifacts, validate } from './wasmEngine';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
const wasmPath = join(repoRoot, 'web/yaog.wasm');

// A real, known-good corpus topology (the same fixtures the conformance gate freezes).
function corpusTopology(name: string): Topology {
  const raw = readFileSync(
    join(repoRoot, 'internal/localcompile/testdata/contract/topologies', name),
    'utf8',
  );
  return JSON.parse(raw).topology as Topology;
}

beforeAll(async () => {
  // Always rebuild: web/yaog.wasm is an ignored cache, and existence alone cannot prove that it
  // contains the current shim/dependency bytes. A stale local cache could otherwise make this
  // integration test green while exercising the previous export contract.
  execFileSync('go', ['build', '-o', 'web/yaog.wasm', './cmd/wasm'], {
    cwd: repoRoot,
    env: { ...process.env, GOOS: 'js', GOARCH: 'wasm', GOSUMDB: 'sum.golang.org' },
    stdio: 'inherit',
  });
  // Instantiate the wasm via the toolchain's wasm_exec.js and set globalThis.yaog, so the module
  // under test reuses it (skipping its browser fetch loader).
  const goroot = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim();
  vm.runInThisContext(readFileSync(join(goroot, 'lib/wasm/wasm_exec.js'), 'utf8'), {
    filename: 'wasm_exec.js',
  });
  const GoCtor = (globalThis as unknown as { Go: new () => { importObject: WebAssembly.Imports; run(i: WebAssembly.Instance): Promise<void> } }).Go;
  const go = new GoCtor();
  const { instance } = await WebAssembly.instantiate(readFileSync(wasmPath), go.importObject);
  void go.run(instance); // registers globalThis.yaog synchronously, then parks on select{}
  // A cold `GOOS=js GOARCH=wasm` build takes well over vitest's default 10s hook budget, so the
  // freshness-safe rebuild above needs a generous timeout.
}, 180_000);

describe('wasmEngine (in-browser Go/WASM local engine)', () => {
  it('registers the yaog API on globalThis after instantiation', () => {
    const api = (globalThis as unknown as { yaog?: Record<string, unknown> }).yaog;
    expect(api).toBeDefined();
    for (const fn of ['compile', 'validate', 'deployScript', 'exportZip', 'buildManifest']) {
      expect(typeof api?.[fn]).toBe('function');
    }
  });

  it('validate returns a well-formed ValidateResponse (arrays, not omitted)', async () => {
    const res = await validate(corpusTopology('01-single-primary-link.json'));
    expect(typeof res.valid).toBe('boolean');
    expect(Array.isArray(res.errors)).toBe(true);
    expect(Array.isArray(res.warnings)).toBe(true);
  });

  it('compile returns the snake_case CompileResponse shape with rendered configs', async () => {
    const res = await compile(corpusTopology('01-single-primary-link.json'));
    expect(res.topology).toBeDefined();
    expect(res.topology.nodes.length).toBeGreaterThan(0);
    expect(res.wireguard_configs).toBeDefined();
    expect(Object.keys(res.wireguard_configs).length).toBeGreaterThan(0);
    expect(res.babel_configs).toBeDefined();
    expect(res.sysctl_configs).toBeDefined();
    expect(res.install_scripts).toBeDefined();
    expect(res.deploy_scripts['deploy-all.sh']).toContain('#!');
    expect(res.manifest.node_count).toBe(res.topology.nodes.length);
  });

  it('deployScripts returns both bash and PowerShell bodies', async () => {
    const { sh, ps1 } = await deployScripts(corpusTopology('01-single-primary-link.json'));
    expect(sh).toContain('#!');
    expect(sh.length).toBeGreaterThan(0);
    expect(ps1.length).toBeGreaterThan(0);
  });

  it('exportArtifacts returns complete, checksummed node bundles', async () => {
    const topology = corpusTopology('01-single-primary-link.json');
    const deploy = await deployScripts(topology);
    const blob = await exportArtifacts(topology);
    expect(blob).toBeInstanceOf(Blob);
    expect(blob.size).toBeGreaterThan(0);

    // Inspect the bytes produced by the real WASM shim, not a mocked bridge. The first node's
    // install.sh and mandatory checksum manifest must share the node-ID directory deploy-all reads,
    // and the manifest must cover the exact installer bytes. This closes the framework-refactor
    // regression where the browser exported only Files and omitted checksums.sha256, making its own
    // fail-closed installer undeployable.
    const dir = mkdtempSync(join(tmpdir(), 'yaog-wasm-export-'));
    const archive = join(dir, 'artifacts.zip');
    try {
      writeFileSync(archive, Buffer.from(await blob.arrayBuffer()));
      const nodeID = topology.nodes[0].id;
      const installEntry = `${nodeID}/install.sh`;
      const checksumEntry = `${nodeID}/checksums.sha256`;
      const entries = execFileSync('unzip', ['-Z1', archive], { encoding: 'utf8' })
        .split('\n')
        .filter(Boolean);
      expect(entries).toContain(installEntry);
      expect(entries).toContain(checksumEntry);
      expect(entries).toContain('deploy-all.sh');
      expect(entries).toContain('deploy-all.ps1');

      const install = execFileSync('unzip', ['-p', archive, installEntry]);
      const checksums = execFileSync('unzip', ['-p', archive, checksumEntry], {
        encoding: 'utf8',
      });
      expect(install.length).toBeGreaterThan(0);
      expect(checksums).toContain(`${createHash('sha256').update(install).digest('hex')}  install.sh`);
      expect(execFileSync('unzip', ['-p', archive, 'deploy-all.sh'], { encoding: 'utf8' }))
        .toBe(deploy.sh);
      expect(execFileSync('unzip', ['-p', archive, 'deploy-all.ps1'], { encoding: 'utf8' }))
        .toBe(deploy.ps1);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it('surfaces a compile error as a thrown Error (not a silent bad shape)', async () => {
    // An empty topology fails validation; the shim returns the {"error":...} envelope, which the
    // module rethrows.
    await expect(compile({} as Topology)).rejects.toThrow(/wasm compile failed/);
  });
});
