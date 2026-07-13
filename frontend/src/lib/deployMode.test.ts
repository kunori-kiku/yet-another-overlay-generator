// @vitest-environment node
//
// deployMode.test.ts — pins the flag-combo → descriptor mapping for lib/deployMode.ts (plan-0, FE
// ratchet-hygiene). deployMode() is the single source of truth collapsing the two build-time env
// flags VITE_LOCAL_ONLY + VITE_YAOG_LOCAL_ENGINE. This suite locks the EXACT truthiness/normalization
// rules (behaviour-identical to the former localOnly()/localEngineEnabled() predicates) AND that the
// two rewired projections agree with the descriptor. vi.stubEnv sets/clears the flags per-test — the
// same mechanism the local-only and local-engine seam suites use — so a memoize-at-module-load
// regression (which would break vi.stubEnv) is caught here.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { deployMode } from './deployMode';
import { localOnly } from './localOnly';
import { localEngineEnabled } from '../compiler/localEngine';

afterEach(() => {
  vi.unstubAllEnvs();
});

describe('deployMode() descriptor', () => {
  it('both flags unset ⇒ default all-in-one controller build, in-browser TS engine', () => {
    expect(deployMode()).toEqual({ localOnly: false, localEngine: 'ts' });
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

  describe('localEngine (VITE_YAOG_LOCAL_ENGINE selector, default-ON)', () => {
    it('unset ⇒ ts (in-browser compiler)', () => {
      expect(deployMode().localEngine).toBe('ts');
    });

    it("'local' ⇒ ts (in-browser compiler)", () => {
      vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
      expect(deployMode().localEngine).toBe('ts');
    });

    it("only the exact 'backend' ⇒ backend (the Go air-gap escape hatch)", () => {
      vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'backend');
      expect(deployMode().localEngine).toBe('backend');
    });

    it("'wasm' ⇒ wasm (the opt-in in-browser Go/WASM engine, plan-3)", () => {
      vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'wasm');
      expect(deployMode().localEngine).toBe('wasm');
    });

    it("'wasm' keeps localEngineEnabled() true (browser path, not the air-gap escape hatch)", () => {
      vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'wasm');
      expect(localEngineEnabled()).toBe(true);
    });
  });

  it('the two flags are independent (localOnly + backend engine compose)', () => {
    vi.stubEnv('VITE_LOCAL_ONLY', '1');
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'backend');
    expect(deployMode()).toEqual({ localOnly: true, localEngine: 'backend' });
  });

  // Rewire guard: the two former predicates must project the descriptor EXACTLY, so every existing
  // caller of localOnly()/localEngineEnabled() is behaviour-identical after the collapse.
  describe('projections agree with the descriptor (behaviour-identical rewire)', () => {
    const combos: ReadonlyArray<[string | undefined, 'local' | 'backend' | undefined]> = [
      [undefined, undefined],
      ['1', undefined],
      [undefined, 'backend'],
      ['1', 'backend'],
      ['0', 'local'],
    ];

    it.each(combos)('VITE_LOCAL_ONLY=%j VITE_YAOG_LOCAL_ENGINE=%j', (lo, eng) => {
      if (lo !== undefined) vi.stubEnv('VITE_LOCAL_ONLY', lo);
      if (eng !== undefined) vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', eng);
      const d = deployMode();
      expect(localOnly()).toBe(d.localOnly);
      expect(localEngineEnabled()).toBe(d.localEngine !== 'backend');
    });
  });
});
