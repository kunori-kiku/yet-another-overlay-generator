import { describe, it, expect } from 'vitest';
import { flipEdge, reverseDialSource } from './edgeDirection';
import type { Edge, Node } from '../types/topology';

// edgeDirection unit contract (D11): the flip must be a pure mirror that never moves an
// allocated value to a different NODE (pins swap fields together with from/to, so each node
// keeps its own port/transit/link-local), and the reverse-dial readout must mirror the
// compiler's resolution order exactly.

function fullEdge(): Edge {
  return {
    id: 'e1',
    from_node_id: 'a',
    to_node_id: 'b',
    type: 'public-endpoint',
    endpoint_host: 'b.example',
    endpoint_port: 51900,
    compiled_port: 51900,
    priority: 7,
    weight: 3,
    transport: 'tcp',
    mimic_fallback: 'udp',
    is_enabled: true,
    notes: 'keep me',
    pinned_from_port: 51820,
    pinned_to_port: 51821,
    pinned_from_transit_ip: '10.10.0.1',
    pinned_to_transit_ip: '10.10.0.2',
    pinned_from_link_local: 'fe80::1',
    pinned_to_link_local: 'fe80::2',
  } as Edge;
}

describe('flipEdge', () => {
  it('swaps from/to, mirrors the three pin pairs, clears the stale dial fields', () => {
    const src = fullEdge();
    const out = flipEdge(src);

    expect([out.from_node_id, out.to_node_id]).toEqual(['b', 'a']);
    // Each NODE keeps its own allocated values: a's port 51820 was on the from side, and after
    // the flip a IS the to side — so 51820 must now sit on pinned_to_port.
    expect(out.pinned_from_port).toBe(51821);
    expect(out.pinned_to_port).toBe(51820);
    expect(out.pinned_from_transit_ip).toBe('10.10.0.2');
    expect(out.pinned_to_transit_ip).toBe('10.10.0.1');
    expect(out.pinned_from_link_local).toBe('fe80::2');
    expect(out.pinned_to_link_local).toBe('fe80::1');
    // Stale dial fields cleared (the old dial target is now the dialer).
    expect(out.endpoint_host).toBeUndefined();
    expect(out.endpoint_port).toBeUndefined();
    expect(out.compiled_port).toBeUndefined();
  });

  it('passes every non-directional field through and never mutates the input', () => {
    const src = fullEdge();
    const before = JSON.stringify(src);
    const out = flipEdge(src);
    expect(JSON.stringify(src)).toBe(before);
    expect([out.id, out.type, out.transport, out.mimic_fallback, out.priority, out.weight, out.notes, out.is_enabled])
      .toEqual(['e1', 'public-endpoint', 'tcp', 'udp', 7, 3, 'keep me', true]);
  });

  it('double flip restores from/to and every pin (dial fields stay cleared)', () => {
    const src = fullEdge();
    const twice = flipEdge(flipEdge(src));
    expect([twice.from_node_id, twice.to_node_id]).toEqual(['a', 'b']);
    expect(twice.pinned_from_port).toBe(src.pinned_from_port);
    expect(twice.pinned_to_port).toBe(src.pinned_to_port);
    expect(twice.pinned_from_transit_ip).toBe(src.pinned_from_transit_ip);
    expect(twice.pinned_to_transit_ip).toBe(src.pinned_to_transit_ip);
    expect(twice.pinned_from_link_local).toBe(src.pinned_from_link_local);
    expect(twice.pinned_to_link_local).toBe(src.pinned_to_link_local);
    expect(twice.endpoint_host).toBeUndefined();
  });
});

describe('reverseDialSource', () => {
  const edge = { id: 'e1', from_node_id: 'a', to_node_id: 'b', type: 'direct', is_enabled: true } as Edge;
  const fromWithEndpoint = {
    id: 'a', name: 'a', role: 'router', domain_id: 'd1', capabilities: {},
    public_endpoints: [{ id: 'a-ep', host: 'a.example', port: 51820 }],
  } as Node;

  it('an enabled primary-class explicit reverse edge with a host wins', () => {
    const rev = { id: 'e2', from_node_id: 'b', to_node_id: 'a', type: 'direct', endpoint_host: 'a-nat.example', is_enabled: true } as Edge;
    expect(reverseDialSource(edge, fromWithEndpoint, [edge, rev]))
      .toEqual({ kind: 'reverse-edge', host: 'a-nat.example' });
  });

  it.each([
    ['disabled', { is_enabled: false }],
    ['backup role', { role: 'backup' }],
    ['no host', { endpoint_host: undefined }],
  ])('a reverse edge that is %s falls through to the node endpoint', (_n, o) => {
    const rev = { id: 'e2', from_node_id: 'b', to_node_id: 'a', type: 'direct', endpoint_host: 'a-nat.example', is_enabled: true, ...o } as Edge;
    expect(reverseDialSource(edge, fromWithEndpoint, [edge, rev]))
      .toEqual({ kind: 'node-endpoint', host: 'a.example' });
  });

  it('no reverse edge and no node endpoint resolves to null (passive)', () => {
    const bare = { ...fromWithEndpoint, public_endpoints: undefined } as Node;
    expect(reverseDialSource(edge, bare, [edge])).toBeNull();
    expect(reverseDialSource(edge, undefined, [edge])).toBeNull();
  });
});
