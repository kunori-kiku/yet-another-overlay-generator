// @vitest-environment node
//
// controllerStore.local-only.test.ts — pins the VITE_LOCAL_ONLY mode lock (plan-7 Phase 3,
// milestone 1.7). The static-local-design build is a backend-free SPA, so controller mode must
// be UNREACHABLE: the store's setMode/switchToController are guarded no-ops toward controller
// mode when VITE_LOCAL_ONLY is set, and the default build (flag unset) keeps mode fully
// operator-selectable. This is the load-bearing half of the lock (the hidden affordances are
// the cosmetic half); a deep link / programmatic call must not escape local mode.
//
// localOnly() reads import.meta.env.VITE_LOCAL_ONLY, which vitest's vi.stubEnv sets/clears
// per-test (same mechanism the local-engine seam suite uses for VITE_YAOG_LOCAL_ENGINE).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useControllerStore } from './controllerStore';

beforeEach(() => {
  // Start each test from a known local-mode baseline (the store's default mode), independent of
  // any prior test's mutation. switchToController also touches topologyStore via clearStranded
  // helpers, but only on the default-build (allowed) path; the assertions below read mode only.
  useControllerStore.setState({ mode: 'local' });
});

afterEach(() => {
  vi.unstubAllEnvs();
  useControllerStore.setState({ mode: 'local' });
});

describe('VITE_LOCAL_ONLY mode lock', () => {
  it('default build (flag unset): setMode and switchToController reach controller mode', () => {
    // No stubEnv ⇒ VITE_LOCAL_ONLY undefined ⇒ localOnly() false ⇒ the toggle is operative.
    useControllerStore.getState().setMode('controller');
    expect(useControllerStore.getState().mode).toBe('controller');

    useControllerStore.setState({ mode: 'local' });
    useControllerStore.getState().switchToController();
    expect(useControllerStore.getState().mode).toBe('controller');
  });

  it('local-only build: setMode("controller") is a no-op (stays local)', () => {
    vi.stubEnv('VITE_LOCAL_ONLY', '1');
    useControllerStore.getState().setMode('controller');
    expect(useControllerStore.getState().mode).toBe('local');
  });

  it('local-only build: switchToController is a no-op (stays local)', () => {
    vi.stubEnv('VITE_LOCAL_ONLY', '1');
    useControllerStore.getState().switchToController();
    expect(useControllerStore.getState().mode).toBe('local');
  });

  it('local-only build: setMode("local") still works (the lock only blocks controller)', () => {
    vi.stubEnv('VITE_LOCAL_ONLY', '1');
    useControllerStore.setState({ mode: 'controller' }); // simulate a stale persisted mode
    useControllerStore.getState().setMode('local');
    expect(useControllerStore.getState().mode).toBe('local');
  });

  it('falsy literals ("", "0", "false") do NOT engage the lock (treated as unset)', () => {
    for (const v of ['', '0', 'false']) {
      vi.stubEnv('VITE_LOCAL_ONLY', v);
      useControllerStore.setState({ mode: 'local' });
      useControllerStore.getState().setMode('controller');
      expect(useControllerStore.getState().mode).toBe('controller');
      vi.unstubAllEnvs();
    }
  });
});
