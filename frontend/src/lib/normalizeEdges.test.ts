import { describe, it, expect } from 'vitest';
import {
  clearedPinFields,
  healCollidingPins,
  sanitizeLinkDirection,
} from './normalizeEdges';
import {
  PERSISTED_ALLOCATION_PIN_FIELDS,
  SERVER_ALLOCATION_FIELDS,
} from './allocationFields';
import type { Edge, Node } from '../types/topology';

// sanitizeLinkDirection unit contract: the panel-load coercion of out-of-enum link_direction
// values to undefined (≡ "both"). healCollidingPins' byte-parity with the Go heal is pinned
// separately by heal.conformance.test.ts; this suite covers only the direction sanitize.
//
// clearedPinFields contract: the single-sourced allocation-clear payload used by deliberate reset
// sites. The two independent oracles below distinguish the six sticky pins from compiled_port.

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
  const EXPECTED_PERSISTED_FIELDS = [
    'pinned_from_port',
    'pinned_to_port',
    'pinned_from_transit_ip',
    'pinned_to_transit_ip',
    'pinned_from_link_local',
    'pinned_to_link_local',
  ];
  const EXPECTED_SERVER_FIELDS = ['compiled_port', ...EXPECTED_PERSISTED_FIELDS];

  it('keeps exactly the six persisted sticky pin fields', () => {
    expect([...PERSISTED_ALLOCATION_PIN_FIELDS]).toEqual(EXPECTED_PERSISTED_FIELDS);
  });

  it('keeps exactly the seven server-derived allocation fields', () => {
    expect([...SERVER_ALLOCATION_FIELDS]).toEqual(EXPECTED_SERVER_FIELDS);
  });

  it('defines the server fields as compiled_port followed by the persisted pins', () => {
    expect([...SERVER_ALLOCATION_FIELDS]).toEqual([
      'compiled_port',
      ...PERSISTED_ALLOCATION_PIN_FIELDS,
    ]);
  });

  it('clears exactly the server-derived set — keys PRESENT with undefined values', () => {
    const payload = clearedPinFields();
    expect(Object.keys(payload).sort()).toEqual([...SERVER_ALLOCATION_FIELDS].sort());
    // Each key is PRESENT (so spreading it over an edge actually RESETS the field, not merely
    // leaving a stale value in place) AND holds undefined.
    for (const f of SERVER_ALLOCATION_FIELDS) {
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
    for (const f of SERVER_ALLOCATION_FIELDS) {
      expect((cleared as Record<string, unknown>)[f]).toBeUndefined();
    }
    // Non-pin fields survive the spread.
    expect(cleared.notes).toBe('keep me');
    expect(cleared.id).toBe('e1');
    // The input edge is not mutated.
    expect(pinned.compiled_port).toBe(51820);
  });
});

describe('healCollidingPins client migration', () => {
  it('clears only the client endpoint port and preserves the live allocation', () => {
    const nodes: Node[] = [
      { id: 'client', name: 'client', role: 'client', domain_id: 'domain' },
      { id: 'router', name: 'router', role: 'router', domain_id: 'domain' },
    ];
    const legacy = edge({
      from_node_id: 'client',
      to_node_id: 'router',
      compiled_port: 51829,
      pinned_from_port: 51900,
      pinned_to_port: 51829,
      pinned_from_transit_ip: '10.10.0.9',
      pinned_to_transit_ip: '10.10.0.10',
      pinned_from_link_local: 'fe80::9',
      pinned_to_link_local: 'fe80::a',
      notes: 'keep me',
    });

    const input = [legacy];
    const out = healCollidingPins(input, nodes);
    expect(out).not.toBe(input);
    expect(out[0].compiled_port).toBe(51829);
    expect(out[0].notes).toBe('keep me');
    expect('pinned_from_port' in out[0]).toBe(false);
    expect(out[0].pinned_to_port).toBe(51829);
    expect(out[0].pinned_from_transit_ip).toBe('10.10.0.9');
    expect(out[0].pinned_to_transit_ip).toBe('10.10.0.10');
    expect(out[0].pinned_from_link_local).toBe('fe80::9');
    expect(out[0].pinned_to_link_local).toBe('fe80::a');
    expect(healCollidingPins(out, nodes)).toBe(out);
  });

  it('leaves non-positive ordinary-link ports for validation instead of claiming them', () => {
    const nodes: Node[] = [
      { id: 'a', name: 'a', role: 'router', domain_id: 'domain' },
      { id: 'b', name: 'b', role: 'router', domain_id: 'domain' },
      { id: 'c', name: 'c', role: 'router', domain_id: 'domain' },
    ];
    const edges = [
      edge({
        id: 'a-b',
        from_node_id: 'a',
        to_node_id: 'b',
        pinned_from_port: -1,
        pinned_to_port: -2,
        pinned_from_transit_ip: '10.10.0.1',
        pinned_to_transit_ip: '10.10.0.2',
      }),
      edge({
        id: 'a-c',
        from_node_id: 'a',
        to_node_id: 'c',
        pinned_from_port: -1,
        pinned_to_port: -3,
        pinned_from_transit_ip: '10.10.0.3',
        pinned_to_transit_ip: '10.10.0.4',
      }),
    ];

    expect(healCollidingPins(edges, nodes)).toBe(edges);
  });
});
