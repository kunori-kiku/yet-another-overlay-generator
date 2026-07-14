import { describe, it, expect } from 'vitest';
import { clearedPinFields, PIN_FIELDS, sanitizeLinkDirection } from './normalizeEdges';
import type { Edge } from '../types/topology';

// sanitizeLinkDirection unit contract: the panel-load coercion of out-of-enum link_direction
// values to undefined (≡ "both"). healCollidingPins' byte-parity with the Go heal is pinned
// separately by heal.conformance.test.ts; this suite covers only the direction sanitize.
//
// clearedPinFields contract: the single-sourced pin-clear payload used by the three deliberate
// pin-reset sites (EdgeEditor role-change / unpin, store purgeModeBoundaryState) — pinned so it
// stays exactly the PIN_FIELDS set and can't silently drift.

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

describe('clearedPinFields', () => {
  // The seven allocation-pin edge fields, hard-coded as an INDEPENDENT oracle: if PIN_FIELDS grows
  // or shrinks, this forces a deliberate test update rather than the helper drifting silently.
  const EXPECTED_PIN_FIELDS = [
    'compiled_port',
    'pinned_from_port',
    'pinned_to_port',
    'pinned_from_transit_ip',
    'pinned_to_transit_ip',
    'pinned_from_link_local',
    'pinned_to_link_local',
  ];

  it('PIN_FIELDS is exactly the seven allocation-pin fields (single source of truth)', () => {
    expect([...PIN_FIELDS].sort()).toEqual([...EXPECTED_PIN_FIELDS].sort());
  });

  it('clears exactly the PIN_FIELDS set — keys PRESENT with undefined values', () => {
    const payload = clearedPinFields();
    // Exactly the PIN_FIELDS keys — no more, no fewer.
    expect(Object.keys(payload).sort()).toEqual([...PIN_FIELDS].sort());
    // Each key is PRESENT (so spreading it over an edge actually RESETS the field, not merely
    // leaving a stale value in place) AND holds undefined.
    for (const f of PIN_FIELDS) {
      expect(f in payload).toBe(true);
      expect((payload as Record<string, unknown>)[f]).toBeUndefined();
    }
  });

  it('never carries a node-secret field (that scrub is a separate concern)', () => {
    // CUSTODY boundary: purgeModeBoundaryState scrubs node secrets via its OWN explicit list, never
    // through this helper, so the pin payload must not smuggle in a node-secret key.
    const payload = clearedPinFields();
    for (const secret of [
      'wireguard_private_key',
      'wireguard_public_key',
      'fixed_private_key',
      'overlay_ip',
    ]) {
      expect(secret in payload).toBe(false);
    }
  });

  it('spreading the payload over a pinned edge resets every pin, preserving other fields', () => {
    const pinned = edge({
      compiled_port: 51820,
      pinned_from_port: 51820,
      pinned_to_port: 51821,
      pinned_from_transit_ip: '10.10.0.1',
      pinned_to_transit_ip: '10.10.0.2',
      pinned_from_link_local: 'fe80::1',
      pinned_to_link_local: 'fe80::2',
      notes: 'keep me',
    });
    const cleared: Edge = { ...pinned, ...clearedPinFields() };
    for (const f of PIN_FIELDS) {
      expect((cleared as Record<string, unknown>)[f]).toBeUndefined();
    }
    // Non-pin fields survive the spread.
    expect(cleared.notes).toBe('keep me');
    expect(cleared.id).toBe('e1');
    // The input edge is not mutated.
    expect(pinned.compiled_port).toBe(51820);
  });
});
