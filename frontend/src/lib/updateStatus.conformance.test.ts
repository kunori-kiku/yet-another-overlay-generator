import { describe, it, expect } from 'vitest';
import { deriveUpdateState } from './updateStatus';
import type { ControllerNode, NodeCondition } from '../types/controller';
import type { ControllerSettings } from '../api/controllerClient';

// Contract test (perpetual) — the panel's self-update chip semantics across the precedence:
// structured selfupdate condition → legacy lastHealth string (old agents only) → version compare.
// Pure, node-env (no jsdom); the .conformance.test.ts suffix is REQUIRED for the vitest glob.

const NOW = Date.parse('2026-06-22T10:00:00Z');
const FRESH = '2026-06-22T09:59:00Z'; // 1m ago — within STALE_MS (3m)
const STALE_SEEN = '2026-06-22T09:50:00Z'; // 10m ago — past STALE_MS

function settings(target: string): ControllerSettings {
  return { targetAgentVersion: target } as unknown as ControllerSettings;
}

function selfUpdate(reason: string): NodeCondition {
  return { type: 'selfupdate', status: 'warn', reason, message: 'm', since: '', observedAt: '' };
}

function node(over: Partial<ControllerNode>): ControllerNode {
  return {
    nodeId: 'n1',
    status: 'approved',
    hasWGPublicKey: true,
    desiredGeneration: 1,
    appliedGeneration: 1,
    lastChecksum: 'c',
    lastHealth: '',
    agentVersion: '',
    lastSeen: FRESH,
    enrolledAt: FRESH,
    rekeyRequested: false,
    inRollout: true,
    conditions: [],
    ...over,
  };
}

describe('deriveUpdateState — gating', () => {
  it("empty target ⇒ 'off' even for an in-rollout node", () => {
    expect(deriveUpdateState(node({ inRollout: true }), settings(''), NOW)).toBe('off');
    expect(deriveUpdateState(node({}), null, NOW)).toBe('off');
  });
  it("a not-in-rollout node ⇒ 'not-targeted'", () => {
    expect(deriveUpdateState(node({ inRollout: false }), settings('v2.0.0'), NOW)).toBe('not-targeted');
  });
});

describe('deriveUpdateState — structured selfupdate condition (preferred)', () => {
  it('Abandoned ⇒ failed', () => {
    expect(deriveUpdateState(node({ conditions: [selfUpdate('Abandoned')] }), settings('v2.0.0'), NOW)).toBe('failed');
  });
  it('Active / HealthConfirmedProbationary ⇒ applying', () => {
    expect(deriveUpdateState(node({ conditions: [selfUpdate('Active')] }), settings('v2.0.0'), NOW)).toBe('applying');
    expect(deriveUpdateState(node({ conditions: [selfUpdate('HealthConfirmedProbationary')] }), settings('v2.0.0'), NOW)).toBe('applying');
  });
  it('Updated ⇒ applied', () => {
    expect(deriveUpdateState(node({ conditions: [selfUpdate('Updated')] }), settings('v2.0.0'), NOW)).toBe('applied');
  });
  it('an unknown future reason falls through to the version compare (forward-compat)', () => {
    // unknown reason + a reported version >= target ⇒ applied via the version fallback.
    expect(deriveUpdateState(node({ conditions: [selfUpdate('SomethingNew')], agentVersion: 'v2.0.0' }), settings('v2.0.0'), NOW)).toBe('applied');
    // unknown reason + below target, fresh ⇒ pending.
    expect(deriveUpdateState(node({ conditions: [selfUpdate('SomethingNew')], agentVersion: 'v1.0.0' }), settings('v2.0.0'), NOW)).toBe('pending');
  });
});

describe('deriveUpdateState — legacy lastHealth fallback (old agents, no conditions)', () => {
  it('the historical markers still derive the same states (no regression)', () => {
    expect(deriveUpdateState(node({ lastHealth: 'self-update to v2 abandoned: cap' }), settings('v2.0.0'), NOW)).toBe('failed');
    expect(deriveUpdateState(node({ lastHealth: 'self-update to v2 health-confirmed (probationary)' }), settings('v2.0.0'), NOW)).toBe('applying');
    expect(deriveUpdateState(node({ lastHealth: 'self-updated to v2.0.0' }), settings('v2.0.0'), NOW)).toBe('applied');
  });
  it('a structured condition overrides a stale legacy lastHealth marker', () => {
    // new agent: conditions present (Updated) wins over a leftover legacy 'abandoned:' string.
    expect(
      deriveUpdateState(node({ conditions: [selfUpdate('Updated')], lastHealth: 'self-update to v2 abandoned: x' }), settings('v2.0.0'), NOW),
    ).toBe('applied');
  });
});

describe('deriveUpdateState — version compare + staleness fallback', () => {
  it('no self-update signal, version >= target ⇒ applied', () => {
    expect(deriveUpdateState(node({ agentVersion: 'v2.0.0' }), settings('v2.0.0'), NOW)).toBe('applied');
  });
  it('below target + fresh ⇒ pending; below target + quiet ⇒ stale', () => {
    expect(deriveUpdateState(node({ agentVersion: 'v1.0.0', lastSeen: FRESH }), settings('v2.0.0'), NOW)).toBe('pending');
    expect(deriveUpdateState(node({ agentVersion: 'v1.0.0', lastSeen: STALE_SEEN }), settings('v2.0.0'), NOW)).toBe('stale');
  });
});
