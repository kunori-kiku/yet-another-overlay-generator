// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Topology } from '../types/topology';
import { useTopologyStore } from './topologyStore';
import { useControllerStore } from './controllerStore';

function response(status: number, body: unknown): Response {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: new Headers(),
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
    blob: async () => (body instanceof Blob ? body : new Blob([String(body)])),
  } as unknown as Response;
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function topology(id: string): Topology {
  return {
    project: { id, name: id },
    domains: [{
      id: `${id}-domain`,
      name: `${id}-domain`,
      cidr: '10.42.0.0/24',
      allocation_mode: 'auto',
      routing_mode: 'babel',
    }],
    nodes: [{
      id: `${id}-node`,
      name: `${id}-node`,
      role: 'peer',
      domain_id: `${id}-domain`,
      capabilities: {
        can_accept_inbound: false,
        can_forward: false,
        can_relay: false,
        has_public_ip: false,
      },
    }],
    edges: [],
  };
}

const originalTopologyActions = {
  exportProject: useTopologyStore.getState().exportProject,
};

function loadTopologyState(topo: Topology): void {
  useTopologyStore.setState({
    ...originalTopologyActions,
    project: topo.project,
    domains: topo.domains,
    nodes: topo.nodes,
    edges: topo.edges,
    allocSchemaVersion: topo.alloc_schema_version ?? 0,
    canvasFromServer: false,
    compileResult: null,
    language: 'en',
  });
}

function seedControllerContext(): void {
  useControllerStore.setState({
    mode: 'controller',
    baseURL: 'https://old-controller.example',
    pathPrefix: '',
    agentBaseURL: 'https://old-agent.example',
    operatorToken: '',
    sessionToken: 'old-session',
    csrfToken: 'old-csrf',
    loggedIn: true,
    operatorName: 'alice',
    authGeneration: 0,
    nodes: [],
    audit: [],
    auditVerified: false,
    settings: null,
    lastSyncedAt: null,
    lastFleetSyncedAt: null,
    lastSyncedSnapshot: null,
    lastSyncedTopology: null,
    loading: false,
    saving: false,
    previewing: false,
    deployPreview: null,
    deployPreviewing: false,
    deployPreviewError: null,
    pendingShrink: null,
    error: null,
  });
}

beforeEach(() => {
  localStorage.clear();
  seedControllerContext();
  loadTopologyState(topology('current'));
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useControllerStore.setState({
    mode: 'local',
    baseURL: 'http://localhost:8080',
    pathPrefix: '',
    operatorToken: '',
    sessionToken: '',
    csrfToken: '',
    loggedIn: false,
    operatorName: null,
    authGeneration: 0,
    nodes: [],
    audit: [],
    settings: null,
    loading: false,
    saving: false,
    previewing: false,
    deployPreview: null,
    deployPreviewing: false,
    pendingShrink: null,
    error: null,
  });
  loadTopologyState(topology('reset'));
});

describe('controller action context generation', () => {
  it('does not repopulate fleet state or start the next read from a deferred old refresh', async () => {
    const nodesResponse = deferred<Response>();
    const auditResponse = deferred<Response>();
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/nodes')) return nodesResponse.promise;
      if (url.endsWith('/audit')) return auditResponse.promise;
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const refresh = useControllerStore.getState().refresh();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });

    nodesResponse.resolve(response(200, [{
      node_id: 'old-node',
      status: 'approved',
      has_wg_public_key: true,
      desired_generation: 2,
      applied_generation: 2,
      last_checksum: 'old',
      last_health: 'ok',
      last_seen: '2030-01-01T00:00:00Z',
      enrolled_at: '2030-01-01T00:00:00Z',
      rekey_requested: false,
    }]));
    auditResponse.resolve(response(200, {
      entries: [{ timestamp: '2030-01-01T00:00:00Z', actor: 'old', action: 'old', node_id: 'old-node' }],
      verified: true,
    }));
    await refresh;

    const state = useControllerStore.getState();
    expect(state.baseURL).toBe('https://new-controller.example');
    expect(state.nodes).toEqual([]);
    expect(state.audit).toEqual([]);
    expect(state.settings).toBeNull();
    expect(state.lastSyncedAt).toBeNull();
    expect(state.lastFleetSyncedAt).toBeNull();
    expect(state.loading).toBe(false);
    // getSettings/hydrateKeystoneStatus must not start after the old nodes/audit leg settles.
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it('does not replace or download the canvas from a deferred old hydration', async () => {
    const oldHydration = deferred<Response>();
    const fetch = vi.fn(async () => oldHydration.promise);
    const exportProject = vi.fn();
    useTopologyStore.setState({ exportProject });
    vi.stubGlobal('fetch', fetch);

    const hydrate = useControllerStore.getState().hydrateFromServer();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });
    oldHydration.resolve(response(200, topology('old-server')));
    await hydrate;

    expect(useTopologyStore.getState().project.id).toBe('current');
    expect(useTopologyStore.getState().canvasFromServer).toBe(false);
    expect(exportProject).not.toHaveBeenCalled();
    expect(useControllerStore.getState().lastSyncedSnapshot).toBeNull();
    expect(useControllerStore.getState().lastSyncedTopology).toBeNull();
  });

  it('invalidates deferred work and resets transients across both workflow-mode transitions', async () => {
    const previewResponse = deferred<Response>();
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/compile-preview')) return previewResponse.promise;
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const preview = useControllerStore.getState().compilePreview();
    await vi.waitFor(() => {
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(useControllerStore.getState().previewing).toBe(true);
    });

    useControllerStore.setState({
      saving: true,
      deployPreviewing: true,
      signing: true,
      enrolling: true,
      loginCeremony: true,
      totpRequired: true,
      pendingKeystoneRotate: true,
      error: 'old controller error',
    });
    useControllerStore.getState().setMode('local');

    let state = useControllerStore.getState();
    expect(state.mode).toBe('local');
    expect(state.authGeneration).toBe(1);
    expect(state.loading).toBe(false);
    expect(state.saving).toBe(false);
    expect(state.previewing).toBe(false);
    expect(state.deployPreviewing).toBe(false);
    expect(state.signing).toBe(false);
    expect(state.enrolling).toBe(false);
    expect(state.loginCeremony).toBe(false);
    expect(state.totpRequired).toBe(false);
    expect(state.pendingKeystoneRotate).toBe(false);
    expect(state.error).toBeNull();
    expect(useTopologyStore.getState().project.id).toBe('current');

    previewResponse.resolve(response(200, {
      topology: topology('old-preview'),
      wireguard_configs: { 'old-preview-node': '[Interface]' },
      babel_configs: {},
      sysctl_configs: {},
      install_scripts: {},
      deploy_scripts: {},
      manifest: {
        project_id: 'old-preview',
        project_name: 'old-preview',
        version: '1',
        compiled_at: '2030-01-01T00:00:00Z',
        node_count: 1,
        checksum: 'old-preview',
      },
    }));
    await preview;

    // The old controller response cannot re-populate the now-local compile surface or canvas.
    expect(useTopologyStore.getState().compileResult).toBeNull();
    expect(useTopologyStore.getState().project.id).toBe('current');
    expect(useControllerStore.getState().previewing).toBe(false);

    useControllerStore.setState({ loading: true, deployPreviewing: true, signing: true });
    useControllerStore.getState().setMode('controller');

    state = useControllerStore.getState();
    expect(state.mode).toBe('controller');
    expect(state.authGeneration).toBe(2);
    expect(state.loading).toBe(false);
    expect(state.deployPreviewing).toBe(false);
    expect(state.signing).toBe(false);
    expect(useTopologyStore.getState().project.id).toBe('current');
  });

  it('stops a deploy after an in-flight old update instead of issuing stage', async () => {
    const updateResponse = deferred<Response>();
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/topology')) return response(404, '');
      if (url.endsWith('/update-topology')) return updateResponse.promise;
      if (url.endsWith('/stage')) {
        return response(200, { staged: [], unchanged: [], skipped_unenrolled: [], generation: 1 });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const deploy = useControllerStore.getState().deploy();
    await vi.waitFor(() => {
      expect(fetch.mock.calls.some(([input]) => String(input).endsWith('/update-topology'))).toBe(true);
    });
    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });
    updateResponse.resolve(response(200, {}));
    await deploy;

    expect(fetch.mock.calls.filter(([input]) => String(input).endsWith('/stage'))).toHaveLength(0);
    expect(useTopologyStore.getState().canvasFromServer).toBe(false);
    expect(useControllerStore.getState().lastDeploy).toBeNull();
    expect(useControllerStore.getState().loading).toBe(false);
  });

  it('rejects a direct-return secret after its controller context changes', async () => {
    const tokenResponse = deferred<Response>();
    vi.stubGlobal('fetch', vi.fn(async () => tokenResponse.promise));

    const token = useControllerStore.getState().mintToken('node-a', 60);
    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });
    tokenResponse.resolve(response(200, { token: 'old-controller-token' }));

    await expect(token).rejects.toThrow('Retry on the current controller');
  });

  it('does not trigger a manual-bundle download after its controller context changes', async () => {
    const bundleResponse = deferred<Response>();
    const createElement = vi.fn();
    vi.stubGlobal('document', { createElement });
    vi.stubGlobal('fetch', vi.fn(async () => bundleResponse.promise));

    const download = useControllerStore.getState().downloadManualNodeBundle('manual-a');
    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });
    bundleResponse.resolve(response(200, new Blob(['old-controller-bundle'])));
    await download;

    expect(createElement).not.toHaveBeenCalled();
    expect(useControllerStore.getState().loading).toBe(false);
  });

  it('keeps routine hydration quiet but reports an explicit conflict re-sync failure', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('controller unreachable');
    }));

    await expect(useControllerStore.getState().hydrateFromServer()).resolves.toBe(false);
    expect(useControllerStore.getState().error).toBeNull();

    await expect(
      useControllerStore.getState().hydrateFromServer({ reportError: true }),
    ).resolves.toBe(false);
    expect(useControllerStore.getState().error).toBeTruthy();
    expect(useTopologyStore.getState().project.id).toBe('current');
  });
});
