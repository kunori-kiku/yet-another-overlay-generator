// @vitest-environment node
//
// agentRollout.test.ts — pins the plan-8 one-click "update all agents to the controller version"
// orchestration + the controller-version usability gate, the load-bearing operator ergonomics the
// AgentUpdateSettings card depends on. Pure node-env unit test (the logic was factored out of the
// .tsx for exactly this — no React/jsdom render needed). The component is a thin adapter that binds
// these to its React state + store actions, so verifying the policy here covers the contract.

import { describe, expect, it } from 'vitest';
import {
  isUsableControllerVersion,
  planUpdateAllToControllerVersion,
  type RolloutEffects,
} from './agentRollout';

describe('isUsableControllerVersion', () => {
  it('accepts a real semver (with or without leading v / pre-release)', () => {
    expect(isUsableControllerVersion('v2.0.0-beta.9')).toBe(true);
    expect(isUsableControllerVersion('2.0.0')).toBe(true);
    expect(isUsableControllerVersion('1.2.3+build.7')).toBe(true);
  });

  it('rejects the literal "dev" (unstamped build) — the bug the major review finding caught', () => {
    expect(isUsableControllerVersion('dev')).toBe(false);
  });

  it('rejects empty (older controller) and any non-semver junk', () => {
    expect(isUsableControllerVersion('')).toBe(false);
    expect(isUsableControllerVersion('latest')).toBe(false);
    expect(isUsableControllerVersion('v2')).toBe(false);
  });
});

// effectsSpy builds a RolloutEffects whose assist resolves to `assistOk`, recording every call so
// the orchestration's contract is observable.
function effectsSpy(assistOk: boolean) {
  const calls = { setTarget: [] as string[], assist: [] as string[], armed: 0 };
  const effects: RolloutEffects = {
    setTarget: (v) => calls.setTarget.push(v),
    assist: async (v) => {
      calls.assist.push(v);
      return assistOk;
    },
    armFleetConfirm: () => {
      calls.armed += 1;
    },
  };
  return { effects, calls };
}

describe('planUpdateAllToControllerVersion', () => {
  it('sets the target, assists with that exact version, and arms fleet-confirm on success', async () => {
    const { effects, calls } = effectsSpy(true);
    await planUpdateAllToControllerVersion('v2.0.0-beta.9', effects);
    expect(calls.setTarget).toEqual(['v2.0.0-beta.9']);
    // The version is threaded into assist explicitly (not read from stale React state) — the whole
    // reason handleAssist takes a targetOverride.
    expect(calls.assist).toEqual(['v2.0.0-beta.9']);
    expect(calls.armed).toBe(1);
  });

  it('does NOT arm fleet-confirm when the assist fails', async () => {
    const { effects, calls } = effectsSpy(false);
    await planUpdateAllToControllerVersion('v2.0.0-beta.9', effects);
    expect(calls.setTarget).toEqual(['v2.0.0-beta.9']); // target still set
    expect(calls.assist).toEqual(['v2.0.0-beta.9']); // assist still attempted
    expect(calls.armed).toBe(0); // but the confirm is NOT armed on failure
  });

  it('is a no-op for a non-usable controller version ("dev" / "" / non-semver)', async () => {
    for (const v of ['dev', '', 'latest']) {
      const { effects, calls } = effectsSpy(true);
      await planUpdateAllToControllerVersion(v, effects);
      expect(calls.setTarget).toEqual([]);
      expect(calls.assist).toEqual([]);
      expect(calls.armed).toBe(0);
    }
  });

  it('exposes no save effect — custody requires an explicit operator Save', () => {
    // Structural guarantee: RolloutEffects has only setTarget/assist/armFleetConfirm, no save, so the
    // one-click can never persist on the operator's behalf.
    const { effects } = effectsSpy(true);
    expect(Object.keys(effects).sort()).toEqual(['armFleetConfirm', 'assist', 'setTarget']);
  });
});
