// @vitest-environment node
//
// deployMode.test.ts — pins the flag → descriptor mapping for lib/deployMode.ts (plan-0, FE
// ratchet-hygiene). deployMode() is the single source of truth for the deployment-shaping build
// flags. This suite locks the EXACT VITE_LOCAL_ONLY truthiness rule (behaviour-identical to the
// former localOnly() predicate) AND that descriptor.localEngine is fixed to 'wasm' — framework-
// refactor plan-9 retired the Go air-gap `backend` escape hatch, so local-mode compute is always
// the in-browser WASM engine (no engine choice). vi.stubEnv sets/clears flags per-test — the same
// mechanism the local-only and local-engine seam suites use — so a memoize-at-module-load regression
// (which would break vi.stubEnv) is caught here.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { deployMode } from './deployMode';
import { localOnly } from './localOnly';
import { localEngineEnabled } from './localEngine';

afterEach(() => {
  vi.unstubAllEnvs();
});

describe('deployMode() descriptor', () => {
  it('both flags unset ⇒ default all-in-one controller build, in-browser WASM engine', () => {
    expect(deployMode()).toEqual({ localOnly: false, localEngine: 'wasm' });
  });

  describe('localOnly (VITE_LOCAL_ONLY truthiness)', () => {
    it.each(['1', 'true', 'yes', 'anything'])('truthy literal %j ⇒ localOnly true', (v) => {
      vi.stubEnv('VITE_LOCAL_ONLY', v);
      expect(deployMode().localOnly).toBe(true);
    });

    it.each(['', '0', 'false'])('falsy literal %j ⇒ localOnly false', (v) => {
      vi.stubEnv('VITE_LOCAL_ONLY', v);
      expect(deployMode().localOnly).toBe(false);
    });
  });

  describe('localEngine is fixed to wasm (the sole engine since plan-9 retired the backend hatch)', () => {
    it('unset ⇒ wasm (the in-browser Go/WASM engine)', () => {
      expect(deployMode().localEngine).toBe('wasm');
    });

    it('localEngineEnabled() is always true — local-mode compute is always in-browser WASM', () => {
      expect(localEngineEnabled()).toBe(true);
    });

    // A stale VITE_YAOG_LOCAL_ENGINE from before plan-9 (the retired 'backend', the older 'ts', or
    // any stray value) is IGNORED — the selector is no longer read, so local-mode compute stays
    // WASM and never crashes on an unknown value. This also guards against a regression that
    // re-introduces a read of that flag and flips the engine.
    it.each(['backend', 'ts', 'local', 'wasm', 'nonsense'])(
      'a stale VITE_YAOG_LOCAL_ENGINE=%j is ignored ⇒ wasm + enabled',
      (v) => {
        vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', v);
        expect(deployMode().localEngine).toBe('wasm');
        expect(localEngineEnabled()).toBe(true);
      },
    );
  });

  // Rewire guard: the two former predicates must project the descriptor EXACTLY, so every existing
  // caller of localOnly()/localEngineEnabled() is behaviour-identical.
  describe('projections agree with the descriptor', () => {
    it.each<string | undefined>(['1', '0', undefined])('VITE_LOCAL_ONLY=%j', (lo) => {
      if (lo !== undefined) vi.stubEnv('VITE_LOCAL_ONLY', lo);
      const d = deployMode();
      expect(localOnly()).toBe(d.localOnly);
      expect(localEngineEnabled()).toBe(d.localEngine === 'wasm');
    });
  });
});
