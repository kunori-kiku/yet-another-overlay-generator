import { describe, it, expect, beforeEach } from 'vitest';
import { useTopologyStore } from './topologyStore';
import type { Edge } from '../types/topology';

// plan-6: the edge inspector writes edge.mimic_fallback via the store's updateEdge. These pin that the
// per-link policy sets/clears correctly and — per the allocation-stability superset rule — NEVER
// perturbs an allocation pin (it is pure renderer policy).

function tcpEdge(): Edge {
  return {
    id: 'e1',
    from_node_id: 'a',
    to_node_id: 'b',
    type: 'direct',
    transport: 'tcp',
    is_enabled: true,
    compiled_port: 51820,
  } as Edge;
}

describe('updateEdge mimic_fallback (plan-6)', () => {
  beforeEach(() => {
    useTopologyStore.setState({ edges: [tcpEdge()] });
  });

  it('sets the per-link policy', () => {
    useTopologyStore.getState().updateEdge('e1', { mimic_fallback: 'udp' });
    expect(useTopologyStore.getState().edges[0].mimic_fallback).toBe('udp');
  });

  it('clears back to inherit (undefined)', () => {
    const store = useTopologyStore.getState();
    store.updateEdge('e1', { mimic_fallback: 'none' });
    store.updateEdge('e1', { mimic_fallback: undefined });
    expect(useTopologyStore.getState().edges[0].mimic_fallback).toBeUndefined();
  });

  it('does NOT perturb the allocation pin (superset rule)', () => {
    useTopologyStore.getState().updateEdge('e1', { mimic_fallback: 'udp' });
    const e = useTopologyStore.getState().edges[0];
    expect(e.compiled_port).toBe(51820);
    expect(e.transport).toBe('tcp');
  });
});
