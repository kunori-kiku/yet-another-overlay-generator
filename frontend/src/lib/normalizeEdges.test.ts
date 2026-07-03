import { describe, it, expect } from 'vitest';
import { sanitizeLinkDirection } from './normalizeEdges';
import type { Edge } from '../types/topology';

// sanitizeLinkDirection unit contract: the panel-load coercion of out-of-enum link_direction
// values to undefined (≡ "both"). healCollidingPins' byte-parity with the Go heal is pinned
// separately by heal.conformance.test.ts; this suite covers only the direction sanitize.

function edge(overrides: Partial<Edge> & { link_direction?: unknown }): Edge {
  return {
    id: 'e1',
    from_node_id: 'a',
    to_node_id: 'b',
    type: 'direct',
    is_enabled: true,
    ...overrides,
  } as Edge;
}

describe('sanitizeLinkDirection', () => {
  it('returns the SAME array reference when nothing needs coercing (no store churn)', () => {
    const edges = [
      edge({}),
      edge({ id: 'e2', link_direction: 'forward' }),
      edge({ id: 'e3', link_direction: 'both' }),
      edge({ id: 'e4', link_direction: '' as never }),
    ];
    expect(sanitizeLinkDirection(edges)).toBe(edges);
  });

  it.each([
    ['the never-released reverse (dropped by D11)', 'reverse'],
    ['a garbled hand-edit', 'one-way'],
    ['a case typo', 'Forward'],
    ['a non-string value', 42],
  ])('coerces %s to undefined', (_name, bad) => {
    const edges = [edge({ link_direction: bad })];
    const out = sanitizeLinkDirection(edges);
    expect(out).not.toBe(edges);
    expect('link_direction' in out[0]).toBe(false);
    // The offending edge is replaced, not mutated (the input stays intact).
    expect((edges[0] as { link_direction?: unknown }).link_direction).toBe(bad);
  });

  it('preserves every other field and leaves clean edges by reference', () => {
    const clean = edge({ id: 'e-clean', link_direction: 'forward' });
    const dirty = edge({ id: 'e-dirty', link_direction: 'reverse', endpoint_host: 'h.example', notes: 'keep me' });
    const out = sanitizeLinkDirection([clean, dirty]);
    expect(out[0]).toBe(clean);
    expect(out[1].endpoint_host).toBe('h.example');
    expect(out[1].notes).toBe('keep me');
    expect(out[1].id).toBe('e-dirty');
  });

  it('is idempotent', () => {
    const once = sanitizeLinkDirection([edge({ link_direction: 'reverse' })]);
    expect(sanitizeLinkDirection(once)).toBe(once);
  });
});
