import { beforeEach, describe, expect, it } from 'vitest';
import type { Node } from '../types/topology';
import { useTopologyStore } from './topologyStore';

function node(id: string, probes?: Node['telemetry_probes']): Node {
  return {
    id,
    name: id,
    role: 'router',
    domain_id: 'domain-default',
    capabilities: {
      can_accept_inbound: true,
      can_forward: true,
      can_relay: false,
      has_public_ip: true,
    },
    telemetry_probes: probes,
  };
}

describe('topologyStore telemetry probe destinations', () => {
  beforeEach(() => {
    useTopologyStore.setState({
      nodes: [
        node('alpha', [
          { id: 'dns-ping', type: 'icmp', host: 'edge.example.net' },
          { id: 'ip-tcp', type: 'tcp', host: '192.0.2.8', port: 443 },
        ]),
        node('beta'),
      ],
      edges: [],
      selectedNodeId: null,
    });
  });

  it('does not couple a hand-entered host to topology node deletion', () => {
    useTopologyStore.getState().removeNode('beta');

    const alpha = useTopologyStore.getState().nodes.find((candidate) => candidate.id === 'alpha');
    expect(alpha?.telemetry_probes).toEqual([
      { id: 'dns-ping', type: 'icmp', host: 'edge.example.net' },
      { id: 'ip-tcp', type: 'tcp', host: '192.0.2.8', port: 443 },
    ]);
  });

  it('removes the policy with its source node', () => {
    useTopologyStore.getState().removeNode('alpha');

    expect(useTopologyStore.getState().nodes).toEqual([node('beta')]);
  });
});
