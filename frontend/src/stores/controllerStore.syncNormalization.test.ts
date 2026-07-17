import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Edge, Topology } from '../types/topology';
import { useTopologyStore } from './topologyStore';
import { isDesignDirty, useControllerStore } from './controllerStore';

function response(body: unknown): Response {
  return {
    status: 200,
    ok: true,
    headers: new Headers(),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

function serverTopology(): Topology {
  return {
    project: { id: 'project', name: 'Project' },
    domains: [{
      id: 'domain',
      name: 'Mesh',
      cidr: '10.42.0.0/24',
      allocation_mode: 'auto',
      routing_mode: 'babel',
    }],
    nodes: [
      {
        id: 'client',
        name: 'Client',
        role: 'client',
        domain_id: 'domain',
        capabilities: {
          can_accept_inbound: false,
          can_forward: false,
          can_relay: false,
          has_public_ip: false,
        },
      },
      {
        id: 'router',
        name: 'Router',
        role: 'router',
        domain_id: 'domain',
        capabilities: {
          can_accept_inbound: true,
          can_forward: true,
          can_relay: false,
          has_public_ip: true,
        },
      },
    ],
    edges: [{
      id: 'client-router',
      from_node_id: 'client',
      to_node_id: 'router',
      type: 'public-endpoint',
      endpoint_host: 'router.example.com',
      transport: 'udp',
      is_enabled: true,
      compiled_port: 51901,
      pinned_from_port: 51900,
      pinned_to_port: 51901,
      pinned_from_transit_ip: '10.10.0.5',
      pinned_to_transit_ip: '10.10.0.6',
      pinned_from_link_local: 'fe80::5',
      pinned_to_link_local: 'fe80::6',
    }],
  };
}

function assertNormalizedAllocation(edge: Edge): void {
  expect('pinned_from_port' in edge).toBe(false);
  expect(edge.pinned_to_port).toBe(51901);
  expect(edge.compiled_port).toBe(51901);
  expect(edge.pinned_from_transit_ip).toBe('10.10.0.5');
  expect(edge.pinned_to_transit_ip).toBe('10.10.0.6');
  expect(edge.pinned_from_link_local).toBe('fe80::5');
  expect(edge.pinned_to_link_local).toBe('fe80::6');
}

beforeEach(() => {
  localStorage.clear();
  const server = serverTopology();
  useTopologyStore.setState({
    project: server.project,
    domains: server.domains,
    nodes: [],
    edges: [],
    allocSchemaVersion: 0,
    canvasFromServer: false,
    compileResult: null,
    language: 'en',
  });
  useControllerStore.setState({
    mode: 'controller',
    baseURL: 'https://controller.example',
    pathPrefix: '',
    operatorToken: '',
    sessionToken: 'session',
    csrfToken: 'csrf',
    loggedIn: true,
    authGeneration: 0,
    lastSyncedSnapshot: null,
    lastSyncedTopology: null,
    lastSyncedAt: null,
    loading: false,
    saving: false,
    error: null,
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('controller topology synchronization normalization', () => {
  it('records the normalized canvas as its baseline and makes immediate Save a no-op', async () => {
    const topology = serverTopology();
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/topology')) return response(topology);
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await expect(useControllerStore.getState().hydrateFromServer()).resolves.toBe(true);
    expect(fetch).toHaveBeenCalledTimes(1);

    const canvas = useTopologyStore.getState().getTopology();
    assertNormalizedAllocation(canvas.edges[0]);

    const synced = useControllerStore.getState();
    expect(synced.lastSyncedTopology).not.toBeNull();
    assertNormalizedAllocation(synced.lastSyncedTopology!.edges[0]);
    expect(isDesignDirty(canvas, synced.lastSyncedSnapshot)).toBe(false);

    await useControllerStore.getState().saveDesign();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(useControllerStore.getState().saving).toBe(false);
  });
});
