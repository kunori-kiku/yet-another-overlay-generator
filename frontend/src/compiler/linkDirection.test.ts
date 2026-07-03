import { describe, it, expect } from 'vitest';
import { derivePeers } from './peers';
import { validateSchema, validateSemantic, Code, type ValidationResult, type CodeValue } from './validator';
import type { Edge, Node, Topology } from '../types/topology';
import type { KeyPair } from './model';

// TS half of the link-direction CONTRACT — mirrors internal/compiler/link_direction_test.go and
// internal/validator/link_direction_test.go case-for-case so the Go↔TS behavior stays pinned from
// both sides (the conformance corpus pins the bytes; these pin the rules at unit granularity).

function publicRouter(id: string, name: string, host: string): Node {
  return {
    id,
    name,
    role: 'router',
    domain_id: 'domain-1',
    capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true },
    public_endpoints: [{ id: `${id}-ep`, host, port: 51820 }],
  } as Node;
}

function linkDirectionTopology(direction: string | undefined, endpointHost = 'accel.example'): Topology {
  const edge: Edge = {
    id: 'e1',
    from_node_id: 'node-a',
    to_node_id: 'node-b',
    type: 'public-endpoint',
    transport: 'udp',
    is_enabled: true,
  };
  if (endpointHost !== '') edge.endpoint_host = endpointHost;
  if (direction !== undefined) (edge as { link_direction?: string }).link_direction = direction;
  return {
    project: { id: 'link-dir', name: 'Link Direction' },
    domains: [{ id: 'domain-1', name: 'dir-net', cidr: '10.47.0.0/24', allocation_mode: 'auto', routing_mode: 'babel' }],
    nodes: [publicRouter('node-a', 'alpha', 'a.example'), publicRouter('node-b', 'beta', 'b.example')],
    edges: [edge],
  } as Topology;
}

function testKeys(): Map<string, KeyPair> {
  return new Map([
    ['node-a', { privateKey: 'privkey-a-fake', publicKey: 'pubkey-a-fake' }],
    ['node-b', { privateKey: 'privkey-b-fake', publicKey: 'pubkey-b-fake' }],
  ]);
}

function hasCode(result: ValidationResult, code: CodeValue): boolean {
  return result.errors.some((e) => e.code === code);
}

describe('link_direction — compiler suppression (mirrors TestLinkDirection_*)', () => {
  const cases: Array<[string, string | undefined, string]> = [
    // [name, direction, wantReverseEndpoint]
    ['forward suppresses the public-endpoint fallback', 'forward', ''],
    ['explicit both keeps the fallback', 'both', 'a.example:51820'],
    ['absent field keeps the fallback', undefined, 'a.example:51820'],
    ['unrecognized value floors to both', 'one-way', 'a.example:51820'],
  ];
  it.each(cases)('%s', (_name, direction, wantReverseEndpoint) => {
    const { peerMap } = derivePeers(linkDirectionTopology(direction), testKeys());

    // The forward dial is never affected by the direction.
    const fwd = peerMap['node-a'].find((p) => p.nodeID === 'node-b');
    expect(fwd?.endpoint).toBe('accel.example:51820');

    // The reverse peer keeps its full stanza; only its Endpoint is gated.
    const rev = peerMap['node-b'].find((p) => p.nodeID === 'node-a');
    expect(rev).toBeDefined();
    expect(rev?.endpoint).toBe(wantReverseEndpoint);
    expect(rev?.allowedIPs.length).toBeGreaterThan(0);
    expect(rev?.listenPort).toBeGreaterThan(0);
    expect(rev?.localTransitIP).not.toBe('');
  });

  it('forward suppresses the explicit-reverse-edge branch too (compiler determinism)', () => {
    const topo = linkDirectionTopology('forward');
    topo.edges.push({
      id: 'e2',
      from_node_id: 'node-b',
      to_node_id: 'node-a',
      type: 'public-endpoint',
      endpoint_host: 'a-nat.example',
      transport: 'udp',
      is_enabled: true,
    } as Edge);
    const { peerMap } = derivePeers(topo, testKeys());
    const rev = peerMap['node-b'].find((p) => p.nodeID === 'node-a');
    expect(rev?.endpoint).toBe('');
  });

  it('allocation is direction-invariant (only the reverse Endpoint differs)', () => {
    const base = derivePeers(linkDirectionTopology(undefined), testKeys());
    const directed = derivePeers(linkDirectionTopology('forward'), testKeys());
    for (const nodeID of ['node-a', 'node-b']) {
      expect(directed.peerMap[nodeID].length).toBe(base.peerMap[nodeID].length);
      base.peerMap[nodeID].forEach((b, i) => {
        const d = directed.peerMap[nodeID][i];
        expect([d.listenPort, d.localTransitIP, d.remoteTransitIP, d.localLinkLocal, d.remoteLinkLocal, d.persistentKeepalive, d.interfaceName])
          .toEqual([b.listenPort, b.localTransitIP, b.remoteTransitIP, b.localLinkLocal, b.remoteLinkLocal, b.persistentKeepalive, b.interfaceName]);
      });
    }
    const baseRev = base.peerMap['node-b'].find((p) => p.nodeID === 'node-a');
    const dirRev = directed.peerMap['node-b'].find((p) => p.nodeID === 'node-a');
    expect(baseRev?.endpoint).not.toBe('');
    expect(dirRev?.endpoint).toBe('');
  });
});

describe('link_direction — validator rules (mirrors TestValidate_LinkDirection*)', () => {
  it('schema enum: "", both, forward accepted; reverse and garbage rejected', () => {
    for (const v of ['', 'both', 'forward']) {
      expect(hasCode(validateSchema(linkDirectionTopology(v)), Code.EdgeLinkDirectionInvalid)).toBe(false);
    }
    for (const v of ['reverse', 'Forward', 'one-way', 'single']) {
      expect(hasCode(validateSchema(linkDirectionTopology(v)), Code.EdgeLinkDirectionInvalid)).toBe(true);
    }
  });

  it('happy path: a single enabled forward edge with a host carries no direction finding', () => {
    const topo = linkDirectionTopology('forward');
    for (const code of [Code.EdgeLinkDirectionInvalid, Code.EdgeLinkDirectionConflict, Code.EdgeLinkDirectionForwardNoEndpoint, Code.EdgeLinkDirectionClientEdge]) {
      expect(hasCode(validateSchema(topo), code)).toBe(false);
      expect(hasCode(validateSemantic(topo), code)).toBe(false);
    }
  });

  const sibling = (overrides: Partial<Edge>): Edge =>
    ({
      id: 'e2',
      from_node_id: 'node-b',
      to_node_id: 'node-a',
      type: 'public-endpoint',
      transport: 'udp',
      is_enabled: true,
      ...overrides,
    }) as Edge;

  it.each([
    ['opposite-direction sibling conflicts', sibling({}), true],
    ['same-direction duplicate conflicts', sibling({ from_node_id: 'node-a', to_node_id: 'node-b' }), true],
    ['disabled sibling does not conflict', sibling({ is_enabled: false }), false],
    ['backup sibling does not conflict', sibling({ from_node_id: 'node-a', to_node_id: 'node-b', role: 'backup' }), false],
  ] as Array<[string, Edge, boolean]>)('%s', (_name, sib, wantConflict) => {
    const topo = linkDirectionTopology('forward');
    topo.edges.push(sib);
    expect(hasCode(validateSemantic(topo), Code.EdgeLinkDirectionConflict)).toBe(wantConflict);
  });

  it('a direction-bearing backup edge never pair-conflicts', () => {
    const topo = linkDirectionTopology(undefined);
    topo.edges.push(sibling({ from_node_id: 'node-a', to_node_id: 'node-b', role: 'backup', endpoint_host: 'accel.example', link_direction: 'forward' } as Partial<Edge>));
    expect(hasCode(validateSemantic(topo), Code.EdgeLinkDirectionConflict)).toBe(false);
  });

  it('forward without endpoint_host errors; disabled edge and both-mode are exempt', () => {
    expect(hasCode(validateSemantic(linkDirectionTopology('forward', '')), Code.EdgeLinkDirectionForwardNoEndpoint)).toBe(true);

    const disabled = linkDirectionTopology('forward', '');
    disabled.edges[0].is_enabled = false;
    expect(hasCode(validateSemantic(disabled), Code.EdgeLinkDirectionForwardNoEndpoint)).toBe(false);

    expect(hasCode(validateSemantic(linkDirectionTopology('both', '')), Code.EdgeLinkDirectionForwardNoEndpoint)).toBe(false);
  });

  it('a direction on a client-touching edge errors, root cause first', () => {
    // Deliberately NO endpoint_host so the skip is OBSERVABLE: without the client branch's
    // early-continue, the forward-no-endpoint rule WOULD fire — the second assertion pins
    // exactly that suppression (mirrors TestValidate_LinkDirectionClientEdge).
    const topo = linkDirectionTopology('forward', '');
    topo.nodes[0] = { ...topo.nodes[0], role: 'client', capabilities: {}, public_endpoints: undefined } as Node;
    const result = validateSemantic(topo);
    expect(hasCode(result, Code.EdgeLinkDirectionClientEdge)).toBe(true);
    expect(hasCode(result, Code.EdgeLinkDirectionForwardNoEndpoint)).toBe(false);
  });
});
