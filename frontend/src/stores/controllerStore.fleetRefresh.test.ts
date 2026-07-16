// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useControllerStore } from './controllerStore';

function response(status: number, body: unknown): Response {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: new Headers(),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

function seed(): void {
  useControllerStore.setState({
    mode: 'controller',
    baseURL: 'https://controller.example',
    pathPrefix: '',
    operatorToken: '',
    sessionToken: 'session',
    csrfToken: 'csrf',
    loggedIn: true,
    authGeneration: 0,
    nodes: [],
    audit: [],
    auditVerified: false,
    settings: null,
    lastSyncedAt: null,
    lastFleetSyncedAt: null,
    loading: false,
    error: 'mutation failed',
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

beforeEach(seed);

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useControllerStore.setState({
    mode: 'local',
    sessionToken: '',
    csrfToken: '',
    loggedIn: false,
    nodes: [],
    audit: [],
    settings: null,
    lastSyncedAt: null,
    lastFleetSyncedAt: null,
    loading: false,
    error: null,
  });
});

describe('isolated Fleet observation refresh', () => {
  it('joins background reads across overlapping Fleet route instances', async () => {
    const nodes = deferred<Response>();
    const fetch = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/nodes')) return nodes.promise;
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const first = useControllerStore.getState().refreshFleetView();
    const second = useControllerStore.getState().refreshFleetView();
    expect(second).toBe(first);
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));

    nodes.resolve(response(200, []));
    await expect(Promise.all([first, second])).resolves.toEqual([true, true]);
    expect(fetch.mock.calls[0]?.[0].toString()).toContain('/nodes');
    expect(useControllerStore.getState().lastFleetSyncedAt).not.toBeNull();
  });

  it('does not start while a mutation owns the global loading gate', async () => {
    const fetch = vi.fn();
    vi.stubGlobal('fetch', fetch);
    useControllerStore.setState({ loading: true, error: 'deploy failed' });

    await expect(useControllerStore.getState().refreshFleetView()).resolves.toBe(false);
    expect(fetch).not.toHaveBeenCalled();
    expect(useControllerStore.getState().loading).toBe(true);
    expect(useControllerStore.getState().error).toBe('deploy failed');
  });

  it('preserves mutation loading/error state on both success and failure', async () => {
    let fail = false;
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/nodes')) {
        return fail ? response(503, { code: 'unavailable' }) : response(200, []);
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await expect(useControllerStore.getState().refreshFleetView()).resolves.toBe(true);
    expect(useControllerStore.getState().loading).toBe(false);
    expect(useControllerStore.getState().error).toBe('mutation failed');
    expect(useControllerStore.getState().lastSyncedAt).not.toBeNull();
    expect(useControllerStore.getState().lastFleetSyncedAt).not.toBeNull();

    fail = true;
    useControllerStore.setState({ lastSyncedAt: null, lastFleetSyncedAt: null });
    await expect(useControllerStore.getState().refreshFleetView()).rejects.toBeDefined();
    expect(useControllerStore.getState().loading).toBe(false);
    expect(useControllerStore.getState().error).toBe('mutation failed');
    expect(useControllerStore.getState().lastSyncedAt).toBeNull();
    expect(useControllerStore.getState().lastFleetSyncedAt).toBeNull();
  });

  it('cannot release or clear a mutation that starts after the background read', async () => {
    const nodes = deferred<Response>();
    const fetch = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/nodes')) return nodes.promise;
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const refresh = useControllerStore.getState().refreshFleetView();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));

    // Model a deploy/revoke/save beginning while the observation request is already in flight.
    useControllerStore.setState({ loading: true, error: 'deploy still pending' });
    nodes.resolve(response(200, []));

    await expect(refresh).resolves.toBe(true);
    expect(useControllerStore.getState().loading).toBe(true);
    expect(useControllerStore.getState().error).toBe('deploy still pending');
    expect(useControllerStore.getState().lastSyncedAt).not.toBeNull();
    expect(useControllerStore.getState().lastFleetSyncedAt).not.toBeNull();
  });

  it('cannot replace a mutation error when an earlier background read fails later', async () => {
    const nodes = deferred<Response>();
    const fetch = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/nodes')) return nodes.promise;
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const refresh = useControllerStore.getState().refreshFleetView();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    useControllerStore.setState({ loading: true, error: 'save conflict requires attention' });
    nodes.reject(new Error('temporary fleet read failure'));

    await expect(refresh).rejects.toThrow('temporary fleet read failure');
    expect(useControllerStore.getState().loading).toBe(true);
    expect(useControllerStore.getState().error).toBe('save conflict requires attention');
    expect(useControllerStore.getState().lastSyncedAt).toBeNull();
    expect(useControllerStore.getState().lastFleetSyncedAt).toBeNull();
  });

  it('starts a fresh background read after the controller/auth context changes', async () => {
    const oldNodes = deferred<Response>();
    const newNodes = deferred<Response>();
    const fetch = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith('https://controller.example/') && url.endsWith('/nodes')) return oldNodes.promise;
      if (url.startsWith('https://new-controller.example/') && url.endsWith('/nodes')) return newNodes.promise;
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const oldRead = useControllerStore.getState().refreshFleetView();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));

    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });
    useControllerStore.setState({ sessionToken: 'new-session', loggedIn: true });
    const newRead = useControllerStore.getState().refreshFleetView();
    expect(newRead).not.toBe(oldRead);
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));

    newNodes.resolve(response(200, []));
    await expect(newRead).resolves.toBe(true);
    oldNodes.resolve(response(200, []));
    await expect(oldRead).resolves.toBe(false);
    expect(useControllerStore.getState().baseURL).toBe('https://new-controller.example');
    expect(useControllerStore.getState().lastFleetSyncedAt).not.toBeNull();
  });
});
