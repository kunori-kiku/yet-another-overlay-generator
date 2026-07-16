// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useControllerStore } from './controllerStore';
import { useTopologyStore } from './topologyStore';
import type { Topology } from '../types/topology';
import { CONTROLLER_TELEMETRY_POLICY_V2_CAPABILITY } from '../lib/deployPreview';

const originalRefresh = useControllerStore.getState().refresh;

function response(status: number, body: unknown): Response {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: new Headers(),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

function sessionResponse(capabilities: string[] = [CONTROLLER_TELEMETRY_POLICY_V2_CAPABILITY]): Response {
  return response(200, {
    operator: 'admin',
    expires_at: '2026-07-18T00:00:00Z',
    csrf_token: 'csrf',
    controller_version: 'v2.0.0-rc.12',
    controller_capabilities: capabilities,
  });
}

function topology(nodeCount = 1): Topology {
  const nodes = Array.from({ length: nodeCount }, (_, index) => ({
    id: `node-${index + 1}`,
    name: `Node ${index + 1}`,
    role: 'peer' as const,
    domain_id: 'domain',
    capabilities: {
      can_accept_inbound: false,
      can_forward: false,
      can_relay: false,
      has_public_ip: false,
    },
    ...(index === 0 ? { telemetry_devices: { mode: 'all-eligible-v1' as const } } : {}),
  }));
  return {
    project: { id: 'project', name: 'Project' },
    domains: [{
      id: 'domain',
      name: 'Domain',
      cidr: '10.42.0.0/24',
      allocation_mode: 'auto',
      routing_mode: 'babel',
    }],
    nodes,
    edges: [],
  };
}

function loadCanvas(topo: Topology): void {
  useTopologyStore.setState({
    project: topo.project,
    domains: topo.domains,
    nodes: topo.nodes,
    edges: topo.edges,
    language: 'en',
    canvasFromServer: true,
  });
}

function seed(): void {
  loadCanvas(topology());
  useControllerStore.setState({
    mode: 'controller',
    baseURL: 'https://controller.example',
    pathPrefix: '',
    operatorToken: '',
    sessionToken: 'session',
    csrfToken: 'csrf',
    authGeneration: 0,
    controllerCapabilities: [CONTROLLER_TELEMETRY_POLICY_V2_CAPABILITY],
    loading: false,
    deployPreview: null,
    deployPreviewing: false,
    deployPreviewMode: 'normal',
    deployPreviewError: null,
    telemetryPolicyUpgradeOffer: null,
    pendingShrink: null,
    error: null,
    refresh: async () => {},
  });
}

beforeEach(seed);

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useControllerStore.setState({
    mode: 'local',
    loading: false,
    deployPreview: null,
    deployPreviewing: false,
    deployPreviewMode: 'normal',
    deployPreviewError: null,
    telemetryPolicyUpgradeOffer: null,
    pendingShrink: null,
    error: null,
    controllerCapabilities: [],
    refresh: originalRefresh,
  });
});

describe('successor telemetry policy rollout bridge', () => {
  it('offers phase one only for the exact structured readiness error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(412, {
      error: {
        code: 'telemetry_policy_upgrade_required',
        message: 'upgrade required',
        params: { count: '1', nodes: 'node-1' },
      },
    })));

    await useControllerStore.getState().openDeployPreview();

    const state = useControllerStore.getState();
    expect(state.telemetryPolicyUpgradeOffer).not.toBeNull();
    expect(state.telemetryPolicyUpgradeOffer?.fingerprint).not.toBe('');
    expect(state.error).toBeNull();
    expect(state.deployPreviewError).toBeNull();
    expect(state.deployPreview).toBeNull();
  });

  it('keeps upgrade-mode 404 blocking instead of exposing the old-controller bypass', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(404, {
      error: { code: 'method_not_allowed', message: 'missing route' },
    })));

    await useControllerStore.getState().openDeployPreview('upgrade-agents-first');

    const state = useControllerStore.getState();
    expect(state.deployPreviewError).toBeNull();
    expect(state.telemetryPolicyUpgradeOffer).toBeNull();
    expect(state.error).not.toBeNull();
  });

  it('keeps the old-controller fallback unavailable for a successor-bearing draft', async () => {
    useControllerStore.setState({ controllerCapabilities: [] });
    const fetchMock = vi.fn().mockResolvedValue(response(404, {
      error: { code: 'method_not_allowed', message: 'missing route' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await useControllerStore.getState().openDeployPreview();

    const state = useControllerStore.getState();
    expect(state.deployPreviewError).toBeNull();
    expect(state.telemetryPolicyUpgradeOffer).toBeNull();
    expect(state.error).not.toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('rechecks the exact fallback snapshot after a legacy preview failure', async () => {
    useControllerStore.setState({ controllerCapabilities: [] });
    const legacy = topology();
    delete legacy.nodes[0].telemetry_devices;
    loadCanvas(legacy);
    const fetchMock = vi.fn().mockResolvedValue(response(404, {
      error: { code: 'method_not_allowed', message: 'missing route' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await useControllerStore.getState().openDeployPreview();
    expect(useControllerStore.getState().deployPreviewError).not.toBeNull();

    loadCanvas(topology());
    await useControllerStore.getState().deploy({ legacyPreviewFallback: true });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(useControllerStore.getState().error).not.toBeNull();
    expect(useControllerStore.getState().deployPreviewError).toBeNull();
  });

  it('re-probes and blocks save, import, and force-deploy after a controller rollback despite a cached capability', async () => {
    const full = topology();
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/topology')) return response(200, full);
      if (url.endsWith('/session')) return sessionResponse([]);
      throw new Error(`unexpected mutation ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);

    await useControllerStore.getState().saveDesign({ force: true });
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/update-topology'))).toHaveLength(0);

    fetchMock.mockClear();
    const file = { text: async () => JSON.stringify(full) } as File;
    await useControllerStore.getState().importDesignToServer(file);
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      'https://controller.example/api/v1/operator/session',
    ]);

    fetchMock.mockClear();
    await useControllerStore.getState().forceRedeployNode('node-1');
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      'https://controller.example/api/v1/operator/topology',
      'https://controller.example/api/v1/operator/session',
    ]);
    expect(useControllerStore.getState().error).not.toBeNull();
  });

  it('uploads the full successor draft while staging only the reviewed legacy projection', async () => {
    const full = topology();
    loadCanvas(full);
    const requests: Array<{ url: string; body: string }> = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      requests.push({ url, body: String(init?.body ?? '') });
      if (url.includes('/deploy-preview?')) {
        return response(200, {
          keystone_full_restage: false,
          nodes: [{ node_id: 'node-1', name: 'Node 1', changed: true }],
          skipped_unenrolled: [],
          telemetry_policy_omitted_node_ids: ['node-1'],
        });
      }
      if (url.endsWith('/session')) return sessionResponse();
      if (url.endsWith('/topology')) return response(200, full);
      if (url.endsWith('/update-topology')) return response(200, {});
      if (url.endsWith('/stage')) {
        return response(200, {
          staged: [],
          unchanged: [],
          skipped_unenrolled: [],
          telemetry_policy_omitted_node_ids: ['node-1'],
          generation: 0,
        });
      }
      throw new Error(`unexpected request ${url}`);
    }));

    await useControllerStore.getState().openDeployPreview('upgrade-agents-first');
    expect(useControllerStore.getState().deployPreview?.telemetryPolicyOmittedNodeIDs).toEqual(['node-1']);
    expect(useControllerStore.getState().deployPreviewMode).toBe('upgrade-agents-first');

    await useControllerStore.getState().deploy({ telemetryPolicyMode: 'upgrade-agents-first' });

    const previewRequest = requests.find((request) => request.url.includes('/deploy-preview?'))!;
    expect(previewRequest.url).toContain('telemetry_policy_mode=upgrade-agents-first');
    const updateRequest = requests.find((request) => request.url.endsWith('/update-topology'))!;
    expect(JSON.parse(updateRequest.body).nodes[0].telemetry_devices).toEqual({ mode: 'all-eligible-v1' });
    const stageRequest = requests.find((request) => request.url.endsWith('/stage'))!;
    expect(JSON.parse(stageRequest.body)).toEqual({ telemetry_policy_mode: 'upgrade-agents-first' });
    expect(useTopologyStore.getState().nodes[0].telemetry_devices).toEqual({ mode: 'all-eligible-v1' });
    expect(useControllerStore.getState().lastDeploy?.telemetryPolicyOmittedNodeIDs).toEqual(['node-1']);
  });

  it('carries the rollout mode through the shrink confirmation continuation', async () => {
    const canvas = topology();
    const server = topology(4);
    loadCanvas(canvas);
    const requests: Array<{ url: string; body: string }> = [];
    let topologyReads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      requests.push({ url, body: String(init?.body ?? '') });
      if (url.endsWith('/topology')) {
        topologyReads++;
        return response(200, topologyReads === 1 ? server : canvas);
      }
      if (url.endsWith('/session')) return sessionResponse();
      if (url.endsWith('/update-topology')) return response(200, {});
      if (url.endsWith('/stage')) {
        return response(200, {
          staged: [], unchanged: [], skipped_unenrolled: [], generation: 0,
          telemetry_policy_omitted_node_ids: ['node-1'],
        });
      }
      throw new Error(`unexpected request ${url}`);
    }));

    await useControllerStore.getState().deploy({
      telemetryPolicyMode: 'upgrade-agents-first',
      force: { forceNodes: ['node-1'] },
    });
    expect(useControllerStore.getState().pendingShrink?.telemetryPolicyMode).toBe('upgrade-agents-first');

    await useControllerStore.getState().deploy({ confirmedShrink: true });
    const stageRequest = requests.find((request) => request.url.endsWith('/stage'))!;
    expect(JSON.parse(stageRequest.body)).toEqual({
      force_nodes: ['node-1'],
      telemetry_policy_mode: 'upgrade-agents-first',
    });
  });
});
