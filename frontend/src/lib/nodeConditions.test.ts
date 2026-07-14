import { describe, it, expect } from 'vitest';
import { conditionVisual, conditionLabel, mapNodeConditions } from './nodeConditions';
import type { NodeCondition } from '../types/controller';

// plan-2: the PURE node-conditions logic that lets later plans add producers with zero render code.
// Tested in the node vitest env (no jsdom) per the updateStatus.ts precedent.

describe('conditionVisual', () => {
  it('maps each canonical status to a distinct, non-empty class', () => {
    const ok = conditionVisual('ok');
    const warn = conditionVisual('warn');
    const error = conditionVisual('error');
    const unknown = conditionVisual('unknown');
    for (const c of [ok, warn, error, unknown]) expect(c.length).toBeGreaterThan(0);
    expect(new Set([ok, warn, error, unknown]).size).toBe(4); // all distinct
  });

  it('is total: an unrecognized status falls back to the unknown class', () => {
    expect(conditionVisual('something-new')).toBe(conditionVisual('unknown'));
    expect(conditionVisual('')).toBe(conditionVisual('unknown'));
  });
});

describe('conditionLabel', () => {
  const base: NodeCondition = { type: 'mimic', status: 'warn', reason: '', message: '', since: '', observedAt: '' };
  it('renders "type: reason" when reason is present', () => {
    expect(conditionLabel({ ...base, reason: 'FellBackToUDP' })).toBe('mimic: FellBackToUDP');
  });
  it('renders type alone when reason is empty', () => {
    expect(conditionLabel(base)).toBe('mimic');
  });
});

describe('mapNodeConditions', () => {
  it('returns [] for an absent (undefined) wire array', () => {
    expect(mapNodeConditions(undefined)).toEqual([]);
  });
  it('maps snake_case observed_at -> observedAt and defaults absent message/since to ""', () => {
    const got = mapNodeConditions([
      { type: 'configapply', status: 'ok', reason: 'Applied', observed_at: '2026-06-22T12:00:00Z' },
    ]);
    expect(got).toEqual([
      { type: 'configapply', status: 'ok', reason: 'Applied', message: '', since: '', observedAt: '2026-06-22T12:00:00Z' },
    ]);
  });
});
