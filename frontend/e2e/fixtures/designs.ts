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
