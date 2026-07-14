import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Reject-recovery (plan-2): a FAILED wasm load must NOT be cached permanently. One transient fetch blip
// (or an asset momentarily not-yet-served) would otherwise brick ALL local compute for the session.
// ensureWasm resets its in-flight promise on rejection so a later call re-attempts. The identical guard
// protects lib/localEngine.ts's dynamic-import promise.
describe('wasmEngine ensureWasm reject-recovery', () => {
  let savedYaog: unknown;
  let savedGo: unknown;

  beforeEach(() => {
    const g = globalThis as Record<string, unknown>;
    savedYaog = g.yaog;
    savedGo = g.Go;
    delete g.yaog;
    // A Go stub that throws on construction forces loadWasm to fail DETERMINISTICALLY at `new Go()`,
    // without touching injectScript (jsdom will not load /wasm_exec.js → would hang) or fetch.
    g.Go = function GoStub() {
      throw new Error('test: forced wasm load failure');
    };
    vi.resetModules(); // fresh module → loadPromise starts null, not a resolved promise from another test
  });

  afterEach(() => {
    const g = globalThis as Record<string, unknown>;
    if (savedYaog === undefined) delete g.yaog;
    else g.yaog = savedYaog;
    if (savedGo === undefined) delete g.Go;
    else g.Go = savedGo;
  });

  it('resets the cached promise on failure so a later call re-attempts and succeeds', async () => {
    const { ensureWasm } = await import('./wasmEngine');

    await expect(ensureWasm()).rejects.toThrow('forced wasm load failure');

    // The engine is now available (asset served, or a prior load registered it): provide the JSON-string
    // API the shim would have registered on globalThis.
    (globalThis as Record<string, unknown>).yaog = {
      compile: () => '{}',
      validate: () => '{}',
      deployScript: () => '',
      exportZip: () => '',
      buildManifest: () => '{}',
    };

    // A cached REJECTED promise would rethrow here. The reset makes ensureWasm re-run loadWasm, which
    // now finds globalThis.yaog (skipping the throwing Go stub) and resolves.
    await expect(ensureWasm()).resolves.toBeDefined();
  });
});
