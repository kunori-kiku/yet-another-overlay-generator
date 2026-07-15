// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { selectLoggedIn, useControllerStore } from './controllerStore';

function response(status: number, body: unknown): Response {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  } as unknown as Response;
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const originalActions = {
  hydrateKeystoneStatus: useControllerStore.getState().hydrateKeystoneStatus,
  hydrateFromServer: useControllerStore.getState().hydrateFromServer,
  refresh: useControllerStore.getState().refresh,
  loadTOTPStatus: useControllerStore.getState().loadTOTPStatus,
  loadPasskeyStatus: useControllerStore.getState().loadPasskeyStatus,
};

function seedStaleAccountContext() {
  useControllerStore.setState({
    ...originalActions,
    mode: 'local',
    baseURL: 'https://old-controller.example',
    pathPrefix: '',
    operatorToken: '',
    sessionToken: 'stale-session',
    csrfToken: 'stale-csrf',
    loggedIn: true,
    operatorName: 'alice',
    sessionExpiresAt: '2030-01-01T00:00:00Z',
    controllerVersion: 'v2.0.0-rc.6',
    totpRequired: true,
    totpEnabled: true,
    passkeyRegistered: true,
    authGeneration: 0,
    loading: true,
    loginCeremony: true,
    enrolling: true,
    signing: true,
    pendingLoginPasskeyEnrollment: null,
    pendingKeystoneEnrollment: null,
  });
}

beforeEach(() => {
  localStorage.clear();
  seedStaleAccountContext();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useControllerStore.setState({
    ...originalActions,
    mode: 'local',
    sessionToken: '',
    csrfToken: '',
    loggedIn: false,
    operatorName: null,
    sessionExpiresAt: null,
    totpRequired: false,
    totpEnabled: null,
    passkeyRegistered: null,
    authGeneration: 0,
    loading: false,
    loginCeremony: false,
    enrolling: false,
    signing: false,
  });
});

describe('account-security status ordering', () => {
  it('lets only the most recently-started TOTP status probe update state', async () => {
    const older = deferred<Response>();
    const newer = deferred<Response>();
    let calls = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (!url.endsWith('/totp/status')) throw new Error(`unexpected request: ${url}`);
      calls += 1;
      return calls === 1 ? older.promise : newer.promise;
    }));

    const olderProbe = useControllerStore.getState().loadTOTPStatus();
    const newerProbe = useControllerStore.getState().loadTOTPStatus();
    newer.resolve(response(200, { enabled: true }));
    await newerProbe;
    older.resolve(response(200, { enabled: false }));
    await olderProbe;

    expect(useControllerStore.getState().totpEnabled).toBe(true);
  });

  it('does not let a probe started before successful TOTP confirmation restore disabled state', async () => {
    const staleStatus = deferred<Response>();
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/totp/status')) return staleStatus.promise;
      if (url.endsWith('/totp/confirm')) return response(200, {});
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const staleProbe = useControllerStore.getState().loadTOTPStatus();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    await useControllerStore.getState().confirmTOTP('secret', '123456');
    staleStatus.resolve(response(200, { enabled: false }));
    await staleProbe;

    expect(useControllerStore.getState().totpEnabled).toBe(true);
  });

  it('does not let a probe started before successful TOTP disable restore enabled state', async () => {
    const staleStatus = deferred<Response>();
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/totp/status')) return staleStatus.promise;
      if (url.endsWith('/totp/disable')) return response(200, {});
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const staleProbe = useControllerStore.getState().loadTOTPStatus();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    await useControllerStore.getState().disableTOTP('123456');
    staleStatus.resolve(response(200, { enabled: true }));
    await staleProbe;

    expect(useControllerStore.getState().totpEnabled).toBe(false);
  });
});

describe('authentication-context invalidation', () => {
  it('clears the in-memory bearer and account status after a 401 session probe', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response(401, '')));

    await useControllerStore.getState().checkSession();

    const state = useControllerStore.getState();
    expect(selectLoggedIn(state)).toBe(false);
    expect(state.sessionToken).toBe('');
    expect(state.csrfToken).toBe('');
    expect(state.operatorName).toBeNull();
    expect(state.totpRequired).toBe(false);
    expect(state.totpEnabled).toBeNull();
    expect(state.passkeyRegistered).toBeNull();
    expect(state.loading).toBe(false);
    expect(state.loginCeremony).toBe(false);
    expect(state.enrolling).toBe(false);
    expect(state.signing).toBe(false);
  });

  it('clears the in-memory bearer and CSRF token after a failed session probe', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('network unavailable');
    }));

    await useControllerStore.getState().checkSession();

    const state = useControllerStore.getState();
    expect(selectLoggedIn(state)).toBe(false);
    expect(state.sessionToken).toBe('');
    expect(state.csrfToken).toBe('');
    expect(state.totpEnabled).toBeNull();
    expect(state.passkeyRegistered).toBeNull();
  });

  it('does not carry account-security status into a replacement cookie identity', async () => {
    const hydrateKeystoneStatus = vi.fn(async () => undefined);
    const hydrateFromServer = vi.fn(async () => undefined);
    useControllerStore.setState({ hydrateKeystoneStatus, hydrateFromServer, loading: false });
    vi.stubGlobal('fetch', vi.fn(async () => response(200, {
      operator: 'bob',
      expires_at: '2031-01-01T00:00:00Z',
      csrf_token: 'bob-csrf',
      controller_version: 'v2.0.0-rc.7',
    })));

    await useControllerStore.getState().checkSession();

    const state = useControllerStore.getState();
    expect(state.loggedIn).toBe(true);
    expect(state.operatorName).toBe('bob');
    expect(state.sessionToken).toBe('');
    expect(state.csrfToken).toBe('bob-csrf');
    expect(state.totpRequired).toBe(false);
    expect(state.totpEnabled).toBeNull();
    expect(state.passkeyRegistered).toBeNull();
    expect(hydrateKeystoneStatus).toHaveBeenCalledTimes(1);
    expect(hydrateFromServer).toHaveBeenCalledTimes(1);
  });

  it('releases global loading and ignores a deferred login after the endpoint changes', async () => {
    useControllerStore.setState({
      sessionToken: '',
      csrfToken: '',
      loggedIn: false,
      operatorName: null,
      loading: false,
    });
    const loginResponse = deferred<Response>();
    vi.stubGlobal('fetch', vi.fn(async () => loginResponse.promise));

    const login = useControllerStore.getState().login('alice', 'password');
    await vi.waitFor(() => expect(useControllerStore.getState().loading).toBe(true));
    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });

    expect(useControllerStore.getState().loading).toBe(false);
    expect(useControllerStore.getState().baseURL).toBe('https://new-controller.example');
    loginResponse.resolve(response(200, {
      session_token: 'late-session',
      csrf_token: 'late-csrf',
      operator: 'alice',
      expires_at: '2030-01-01T00:00:00Z',
      controller_version: 'v2.0.0-rc.7',
    }));
    await login;

    const state = useControllerStore.getState();
    expect(state.sessionToken).toBe('');
    expect(state.csrfToken).toBe('');
    expect(state.loggedIn).toBe(false);
    expect(state.loading).toBe(false);
  });

  it('stops a successful login hydration chain when its established context changes', async () => {
    const hydration = deferred<void>();
    const hydrateFromServer = vi.fn(async () => hydration.promise);
    const refresh = vi.fn(async () => undefined);
    const loadTOTPStatus = vi.fn(async () => undefined);
    const loadPasskeyStatus = vi.fn(async () => undefined);
    useControllerStore.setState({
      sessionToken: '',
      csrfToken: '',
      loggedIn: false,
      operatorName: null,
      loading: false,
      hydrateFromServer,
      refresh,
      loadTOTPStatus,
      loadPasskeyStatus,
    });
    vi.stubGlobal('fetch', vi.fn(async () => response(200, {
      session_token: 'new-session',
      csrf_token: 'new-csrf',
      operator: 'alice',
      expires_at: '2030-01-01T00:00:00Z',
      controller_version: 'v2.0.0-rc.7',
    })));

    const login = useControllerStore.getState().login('alice', 'password');
    await vi.waitFor(() => expect(hydrateFromServer).toHaveBeenCalledTimes(1));
    useControllerStore.getState().setConfig({ baseURL: 'https://replacement-controller.example' });
    hydration.resolve();
    await login;

    expect(refresh).not.toHaveBeenCalled();
    expect(loadTOTPStatus).not.toHaveBeenCalled();
    expect(loadPasskeyStatus).not.toHaveBeenCalled();
    expect(useControllerStore.getState().sessionToken).toBe('');
    expect(useControllerStore.getState().baseURL).toBe('https://replacement-controller.example');
  });

  it('logs out locally before revocation returns and cannot erase a newer context', async () => {
    const revocation = deferred<Response>();
    vi.stubGlobal('fetch', vi.fn(async () => revocation.promise));

    const logout = useControllerStore.getState().logout();
    const immediatelyLoggedOut = useControllerStore.getState();
    expect(selectLoggedIn(immediatelyLoggedOut)).toBe(false);
    expect(immediatelyLoggedOut.sessionToken).toBe('');
    expect(immediatelyLoggedOut.csrfToken).toBe('');
    expect(immediatelyLoggedOut.nodes).toEqual([]);

    useControllerStore.getState().setConfig({ baseURL: 'https://new-controller.example' });
    useControllerStore.setState({
      sessionToken: 'new-session',
      csrfToken: 'new-csrf',
      loggedIn: true,
      operatorName: 'bob',
    });
    revocation.resolve(response(200, {}));
    await logout;

    const state = useControllerStore.getState();
    expect(state.baseURL).toBe('https://new-controller.example');
    expect(state.sessionToken).toBe('new-session');
    expect(state.csrfToken).toBe('new-csrf');
    expect(state.loggedIn).toBe(true);
    expect(state.operatorName).toBe('bob');
  });
});
