// designs.ts — programmatic topology builders for the operator-journey specs. They use UNIQUE
// node ids per run (so the shared keystone FileStore stays collision-free across specs/runs)
// while REUSING the seed topology's confidential strings (hostname / endpoint) so leakOracle's
// FIXTURE_SENTINELS catch a persisted leak.

export interface BuiltDesign {
  topo: unknown
  router: string
  peer: string
}

// uniqueRouterPeer builds a minimal valid router+peer+edge design with unique node ids.
export function uniqueRouterPeer(runId: string): BuiltDesign {
  const router = `r-${runId}`
  const peer = `p-${runId}`
  const topo = {
    project: { id: `e2e-${runId}`, name: `E2E ${runId}` },
    domains: [
      { id: 'd1', name: 'net', cidr: '10.62.0.0/24', allocation_mode: 'auto', routing_mode: 'babel' },
    ],
    nodes: [
      {
        id: router,
        name: 'router',
        hostname: 'router.example.com',
        role: 'router',
        domain_id: 'd1',
        capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true },
      },
      { id: peer, name: 'peer', role: 'peer', domain_id: 'd1', capabilities: {} },
    ],
    edges: [
      {
        id: 'e1',
        from_node_id: peer,
        to_node_id: router,
        type: 'public-endpoint',
        endpoint_host: '198.51.100.1',
        endpoint_port: 0,
        transport: 'udp',
        is_enabled: true,
      },
    ],
  }
  return { topo, router, peer }
}

// runId returns a per-test unique token suitable for node ids (lowercase base36).
export function runId(pid: number, workerIndex: number, now: number): string {
  return `${pid}w${workerIndex}t${now.toString(36)}`
}

export interface BuiltColliding {
  topo: unknown
  router: string
  peerA: string
  peerB: string
  // collidingTransitIp is the transit IP BOTH edges pin to the router — the deliberate collision
  // healCollidingPins repairs (two enabled non-client edges claiming the same transit resource).
  collidingTransitIp: string
}

// uniqueColliding builds a 3-node star (router + two peers) whose TWO enabled edges deliberately
// pin the SAME router-side transit IP — the corruption HealCollidingPins / healCollidingPins
// repairs by stripping the later edge's pins so a clean re-allocate produces a deployable graph.
// Unique node ids per run keep the shared FileStore collision-free across runs.
export function uniqueColliding(id: string): BuiltColliding {
  const router = `cr-${id}`
  const peerA = `ca-${id}`
  const peerB = `cb-${id}`
  const collidingTransitIp = '10.10.0.1'
  const topo = {
    project: { id: `e2e-collide-${id}`, name: `E2E Collide ${id}` },
    domains: [
      { id: 'd1', name: 'net', cidr: '10.64.0.0/24', allocation_mode: 'auto', routing_mode: 'babel' },
    ],
    nodes: [
      {
        id: router,
        name: 'router',
        hostname: 'router.example.com',
        role: 'router',
        domain_id: 'd1',
        capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true },
      },
      { id: peerA, name: 'peer-a', role: 'peer', domain_id: 'd1', capabilities: {} },
      { id: peerB, name: 'peer-b', role: 'peer', domain_id: 'd1', capabilities: {} },
    ],
    edges: [
      {
        id: 'e-a',
        from_node_id: peerA,
        to_node_id: router,
        type: 'public-endpoint',
        endpoint_host: '198.51.100.1',
        endpoint_port: 0,
        transport: 'udp',
        is_enabled: true,
        pinned_from_transit_ip: '10.10.0.2',
        pinned_to_transit_ip: collidingTransitIp,
        pinned_from_port: 51820,
        pinned_to_port: 51821,
      },
      {
        id: 'e-b',
        from_node_id: peerB,
        to_node_id: router,
        type: 'public-endpoint',
        endpoint_host: '198.51.100.2',
        endpoint_port: 0,
        transport: 'udp',
        is_enabled: true,
        pinned_from_transit_ip: '10.10.0.3',
        // SAME router-side transit IP as edge e-a → the deliberate cross-link collision.
        pinned_to_transit_ip: collidingTransitIp,
        pinned_from_port: 51822,
        pinned_to_port: 51823,
      },
    ],
  }
  return { topo, router, peerA, peerB, collidingTransitIp }
}
