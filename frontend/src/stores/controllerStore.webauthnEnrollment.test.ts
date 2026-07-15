// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useControllerStore } from './controllerStore';

const challenge = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'; // 32 zero bytes, base64url
const rawId = Uint8Array.from([1, 2, 3, 4]);
const spki = Uint8Array.from([48, 3, 1, 2, 3]);

const originalActions = {
  hydrateKeystoneStatus: useControllerStore.getState().hydrateKeystoneStatus,
  hydrateFromServer: useControllerStore.getState().hydrateFromServer,
  refresh: useControllerStore.getState().refresh,
  loadTOTPStatus: useControllerStore.getState().loadTOTPStatus,
  loadPasskeyStatus: useControllerStore.getState().loadPasskeyStatus,
};

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
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function installBrowserCredentials(
  create: ReturnType<typeof vi.fn>,
  get: ReturnType<typeof vi.fn>,
) {
  vi.stubGlobal('PublicKeyCredential', class PublicKeyCredential {});
  const testLocation = {
    hostname: 'panel.example',
    origin: 'https://panel.example',
    port: '',
  };
  vi.stubGlobal('location', testLocation);
  vi.stubGlobal('window', { location: testLocation });
  vi.stubGlobal('navigator', { credentials: { create, get } });
}

function createdCredential() {
  return {
    rawId: rawId.buffer,
    response: {
      getPublicKeyAlgorithm: () => -7,
      getPublicKey: () => spki.buffer,
    },
  };
}

function assertionCredential() {
  return {
    rawId: rawId.buffer,
    response: {
      signature: Uint8Array.from([9, 8]).buffer,
      authenticatorData: Uint8Array.from([7, 6]).buffer,
      clientDataJSON: Uint8Array.from([5, 4]).buffer,
    },
  };
}

function matchingLoginStatus() {
  const pending = useControllerStore.getState().pendingLoginPasskeyEnrollment;
  if (!pending) throw new Error('test expected a pending login candidate');
  return {
    registered: true,
    alg: pending.alg,
    credential_id: pending.credentialId,
    public_key_pem: pending.publicKeyPEM,
    rpid: 'panel.example',
    origin: 'https://panel.example',
  };
}

beforeEach(() => {
  localStorage.clear();
  useControllerStore.setState({
    ...originalActions,
    mode: 'controller',
    baseURL: 'https://controller.example',
    pathPrefix: '',
    agentBaseURL: 'https://agent.example',
    operatorToken: '',
    sessionToken: 'session',
    csrfToken: '',
    loggedIn: true,
    operatorName: 'admin',
    authGeneration: 0,
    loading: false,
    error: null,
    passkeyRegistered: false,
    pendingLoginPasskeyEnrollment: null,
    loginCeremony: false,
    operatorCredentialId: null,
    operatorCredentialAlg: null,
    operatorRpId: null,
    operatorPublicKeyPEM: null,
    pendingKeystoneEnrollment: null,
    pendingKeystoneRotate: false,
    serverOperatorPinned: false,
    serverOperatorAlg: null,
    serverOperatorRpId: null,
    serverOperatorOrigin: null,
    serverOperatorPublicKeyPEM: null,
    serverOperatorFingerprint: null,
    serverRedeployRequired: false,
    signing: false,
    enrolling: false,
    lastSyncedSnapshot: null,
    lastSyncedTopology: null,
  });
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
    authGeneration: 0,
    loading: false,
    error: null,
    passkeyRegistered: null,
    pendingLoginPasskeyEnrollment: null,
    loginCeremony: false,
    pendingKeystoneEnrollment: null,
    pendingKeystoneRotate: false,
    operatorCredentialId: null,
    operatorCredentialAlg: null,
    operatorRpId: null,
    operatorPublicKeyPEM: null,
    serverOperatorPinned: null,
    serverOperatorAlg: null,
    serverOperatorRpId: null,
    serverOperatorOrigin: null,
    serverOperatorPublicKeyPEM: null,
    serverOperatorFingerprint: null,
    serverRedeployRequired: false,
    enrolling: false,
  });
});

describe('login-passkey enrollment recovery', () => {
  it('reuses the created candidate after second-prompt cancellation', async () => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi
      .fn()
      .mockRejectedValueOnce(new DOMException('cancelled', 'NotAllowedError'))
      .mockResolvedValueOnce(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/passkey/register')) return response(200, {});
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await expect(useControllerStore.getState().registerPasskey()).rejects.toMatchObject({
      kind: 'enrollment-verification-failed',
    });
    expect(useControllerStore.getState().pendingLoginPasskeyEnrollment?.credentialId).toBe('AQIDBA');

    await useControllerStore.getState().registerPasskey();

    expect(create).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls.filter(([input]) => String(input).endsWith('/webauthn/enrollment/begin'))).toHaveLength(2);
    expect(fetch.mock.calls.filter(([input]) => String(input).endsWith('/passkey/register'))).toHaveLength(1);
    expect(useControllerStore.getState().pendingLoginPasskeyEnrollment).toBeNull();
    expect(useControllerStore.getState().passkeyRegistered).toBe(true);
  });

  it('accepts a lost POST response only when status exactly matches its candidate', async () => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/passkey/register')) throw new TypeError('response lost');
      if (url.endsWith('/passkey/status')) return response(200, matchingLoginStatus());
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await useControllerStore.getState().registerPasskey();

    expect(useControllerStore.getState().passkeyRegistered).toBe(true);
    expect(useControllerStore.getState().pendingLoginPasskeyEnrollment).toBeNull();
  });

  it('does not silently claim a mismatched credential after a lost response', async () => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/passkey/register')) throw new TypeError('response lost');
      if (url.endsWith('/passkey/status')) {
        return response(200, {
          ...matchingLoginStatus(),
          credential_id: 'another-tab-won',
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await expect(useControllerStore.getState().registerPasskey()).rejects.toThrow('response lost');
    expect(useControllerStore.getState().passkeyRegistered).toBe(true);
    expect(useControllerStore.getState().pendingLoginPasskeyEnrollment).toBeNull();
  });

  it('converges a CAS conflict without retaining an implicit replacement candidate', async () => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/passkey/register')) {
        return response(409, {
          error: { code: 'login_credential_changed', message: 'changed', params: {} },
        });
      }
      if (url.endsWith('/passkey/status')) {
        return response(200, {
          registered: true,
          alg: 'webauthn-es256',
          credential_id: 'winner',
          public_key_pem: 'winner-public-key',
          rpid: 'panel.example',
          origin: 'https://panel.example',
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await expect(useControllerStore.getState().registerPasskey()).rejects.toMatchObject({ status: 409 });
    expect(useControllerStore.getState().passkeyRegistered).toBe(true);
    expect(useControllerStore.getState().pendingLoginPasskeyEnrollment).toBeNull();

    await useControllerStore.getState().registerPasskey();
    expect(fetch.mock.calls.filter(([input]) => String(input).endsWith('/webauthn/enrollment/begin'))).toHaveLength(1);
    expect(create).toHaveBeenCalledTimes(1);
  });

  it('cannot repopulate a candidate after logout while begin is deferred', async () => {
    const begin = deferred<Response>();
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return begin.promise;
      if (url.endsWith('/logout')) return response(200, {});
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const enrollment = useControllerStore.getState().registerPasskey();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    await useControllerStore.getState().logout();
    begin.resolve(response(200, { challenge }));
    await enrollment;

    expect(create).not.toHaveBeenCalled();
    expect(useControllerStore.getState().pendingLoginPasskeyEnrollment).toBeNull();
    expect(useControllerStore.getState().loggedIn).toBe(false);
  });

  it('drops a synthetic double invocation while the first begin is pending', async () => {
    const begin = deferred<Response>();
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return begin.promise;
      if (url.endsWith('/passkey/register')) return response(200, {});
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const first = useControllerStore.getState().registerPasskey();
    const second = useControllerStore.getState().registerPasskey();
    begin.resolve(response(200, { challenge }));
    await Promise.all([first, second]);

    expect(fetch.mock.calls.filter(([input]) => String(input).endsWith('/webauthn/enrollment/begin'))).toHaveLength(1);
    expect(create).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledTimes(1);
  });
});

describe('login-passkey status ordering', () => {
  it('lets only the most recently-started status probe update registration state', async () => {
    const older = deferred<Response>();
    const newer = deferred<Response>();
    let calls = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (!url.endsWith('/passkey/status')) throw new Error(`unexpected request: ${url}`);
      calls += 1;
      return calls === 1 ? older.promise : newer.promise;
    }));

    const olderProbe = useControllerStore.getState().loadPasskeyStatus();
    const newerProbe = useControllerStore.getState().loadPasskeyStatus();
    newer.resolve(response(200, { registered: true }));
    await newerProbe;
    older.resolve(response(200, { registered: false }));
    await olderProbe;

    expect(useControllerStore.getState().passkeyRegistered).toBe(true);
  });

  it('does not let a probe started before successful registration re-expose Register', async () => {
    const staleStatus = deferred<Response>();
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/passkey/status')) return staleStatus.promise;
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/passkey/register')) return response(200, {});
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const staleProbe = useControllerStore.getState().loadPasskeyStatus();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    await useControllerStore.getState().registerPasskey();
    staleStatus.resolve(response(200, { registered: false }));
    await staleProbe;

    expect(useControllerStore.getState().passkeyRegistered).toBe(true);
  });

  it('does not let a probe started before successful disable restore registered state', async () => {
    const staleStatus = deferred<Response>();
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(vi.fn(), get);
    useControllerStore.setState({ passkeyRegistered: true });
    let disableCalls = 0;

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/passkey/status')) return staleStatus.promise;
      if (url.endsWith('/passkey/disable')) {
        disableCalls += 1;
        return disableCalls === 1
          ? response(200, {
              challenge,
              allow_credentials: [{ type: 'public-key', id: 'AQIDBA' }],
              rpid: 'panel.example',
              alg: 'webauthn-es256',
            })
          : response(200, { registered: false });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const staleProbe = useControllerStore.getState().loadPasskeyStatus();
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    await useControllerStore.getState().disablePasskey();
    staleStatus.resolve(response(200, { registered: true }));
    await staleProbe;

    expect(useControllerStore.getState().passkeyRegistered).toBe(false);
  });
});

describe('keystone enrollment recovery', () => {
  it('reuses one candidate after second-prompt cancellation', async () => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi
      .fn()
      .mockRejectedValueOnce(new DOMException('cancelled', 'NotAllowedError'))
      .mockResolvedValueOnce(assertionCredential());
    installBrowserCredentials(create, get);
    let committed: ReturnType<typeof useControllerStore.getState>['pendingKeystoneEnrollment'] = null;
    useControllerStore.setState({ refresh: vi.fn(async () => undefined) });

    const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/operator-credential')) {
        if (init?.method === 'POST') {
          committed = useControllerStore.getState().pendingKeystoneEnrollment;
          return response(200, { ok: true });
        }
        return response(200, committed ? {
          pinned: true,
          alg: committed.alg,
          credential_id: committed.credentialId,
          public_key_pem: committed.publicKeyPEM,
          rpid: committed.rpId,
          origin: committed.origin,
          fingerprint: 'abc',
          redeploy_required: false,
        } : { pinned: false });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await useControllerStore.getState().enrollOperator();
    expect(useControllerStore.getState().pendingKeystoneEnrollment?.credentialId).toBe('AQIDBA');

    await useControllerStore.getState().enrollOperator();

    expect(create).toHaveBeenCalledTimes(1);
    expect(get).toHaveBeenCalledTimes(2);
    expect(useControllerStore.getState().pendingKeystoneEnrollment).toBeNull();
  });

  it('clears a lost-response candidate only for an exact server match', async () => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/operator-credential') && init?.method === 'POST') {
        throw new TypeError('response lost');
      }
      if (url.endsWith('/operator-credential')) {
        const pending = useControllerStore.getState().pendingKeystoneEnrollment;
        if (!pending) throw new Error('expected pending keystone candidate');
        return response(200, {
          pinned: true,
          alg: pending.alg,
          credential_id: pending.credentialId,
          public_key_pem: pending.publicKeyPEM,
          rpid: 'panel.example',
          origin: 'https://panel.example',
          fingerprint: 'abc',
          redeploy_required: false,
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await useControllerStore.getState().enrollOperator();

    const state = useControllerStore.getState();
    expect(state.pendingKeystoneEnrollment).toBeNull();
    expect(state.error).toBeNull();
    expect(state.operatorCredentialId).toBe('AQIDBA');
    expect(state.operatorCredentialAlg).toBe('webauthn-es256');
    expect(state.operatorRpId).toBe('panel.example');
    expect(state.operatorPublicKeyPEM).toBeTruthy();
  });

  it.each([
    ['RP ID', { rpid: 'other-rp.example' }],
    ['origin', { origin: 'https://other-origin.example' }],
  ])('retains a same-key candidate when the server %s differs', async (_label, bindingOverride) => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/operator-credential') && init?.method === 'POST') {
        throw new TypeError('response lost');
      }
      if (url.endsWith('/operator-credential')) {
        const pending = useControllerStore.getState().pendingKeystoneEnrollment;
        if (!pending) throw new Error('expected pending keystone candidate');
        return response(200, {
          pinned: true,
          alg: pending.alg,
          credential_id: pending.credentialId,
          public_key_pem: pending.publicKeyPEM,
          rpid: pending.rpId,
          origin: pending.origin,
          fingerprint: 'same-key-different-binding',
          redeploy_required: false,
          ...bindingOverride,
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await useControllerStore.getState().enrollOperator();

    expect(useControllerStore.getState().pendingKeystoneEnrollment?.credentialId).toBe('AQIDBA');
    expect(useControllerStore.getState().error).not.toBeNull();
  });

  it('retains a mismatched candidate after a lost response', async () => {
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);

    const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/operator-credential') && init?.method === 'POST') {
        throw new TypeError('response lost');
      }
      if (url.endsWith('/operator-credential')) {
        return response(200, {
          pinned: true,
          alg: 'webauthn-es256',
          credential_id: 'another-tab-won',
          public_key_pem: 'winner-public-key',
          rpid: 'panel.example',
          origin: 'https://panel.example',
          fingerprint: 'winner',
          redeploy_required: false,
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    await useControllerStore.getState().enrollOperator();

    expect(useControllerStore.getState().pendingKeystoneEnrollment?.credentialId).toBe('AQIDBA');
    expect(useControllerStore.getState().serverOperatorFingerprint).toBe('winner');
    // The mismatched candidate remains a separate explicit-rotation candidate, while the active
    // browser signing handle atomically follows the server winner.
    expect(useControllerStore.getState().operatorCredentialId).toBe('another-tab-won');
    expect(useControllerStore.getState().operatorPublicKeyPEM).toBe('winner-public-key');
    expect(useControllerStore.getState().error).not.toBeNull();
  });

  it('does not let a status probe started before enrollment clear the committed handle', async () => {
    const oldStatus = deferred<Response>();
    const create = vi.fn().mockResolvedValue(createdCredential());
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(create, get);
    useControllerStore.setState({ refresh: vi.fn(async () => undefined) });
    let credentialPosted = false;
    let statusCalls = 0;

    const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/webauthn/enrollment/begin')) return response(200, { challenge });
      if (url.endsWith('/operator-credential') && init?.method === 'POST') {
        credentialPosted = true;
        return response(200, { ok: true });
      }
      if (url.endsWith('/operator-credential')) {
        statusCalls += 1;
        if (statusCalls === 1) return oldStatus.promise;
        const committed = useControllerStore.getState();
        if (
          !credentialPosted
          || !committed.operatorCredentialId
          || !committed.operatorCredentialAlg
          || !committed.operatorRpId
          || !committed.operatorPublicKeyPEM
        ) throw new Error('expected the committed credential descriptor');
        return response(200, {
          pinned: true,
          alg: committed.operatorCredentialAlg,
          credential_id: committed.operatorCredentialId,
          public_key_pem: committed.operatorPublicKeyPEM,
          rpid: committed.operatorRpId,
          origin: 'https://panel.example',
          fingerprint: 'committed',
          redeploy_required: false,
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    const staleProbe = useControllerStore.getState().hydrateKeystoneStatus();
    await vi.waitFor(() => expect(statusCalls).toBe(1));
    await useControllerStore.getState().enrollOperator();
    oldStatus.resolve(response(200, { pinned: false }));
    await staleProbe;

    const state = useControllerStore.getState();
    expect(state.serverOperatorPinned).toBe(true);
    expect(state.serverOperatorFingerprint).toBe('committed');
    expect(state.operatorCredentialId).toBe('AQIDBA');
    expect(state.operatorPublicKeyPEM).toBeTruthy();
    expect(state.pendingKeystoneEnrollment).toBeNull();
  });
});

describe('remaining login ceremony re-entry guards', () => {
  it('runs one passwordless chain under a direct double invocation', async () => {
    const begin = deferred<Response>();
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(vi.fn(), get);
    useControllerStore.setState({
      ...originalActions,
      hydrateFromServer: vi.fn(async () => undefined),
      refresh: vi.fn(async () => undefined),
      loadTOTPStatus: vi.fn(async () => undefined),
      loadPasskeyStatus: vi.fn(async () => undefined),
    });

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith('/login/passkey/begin')) return begin.promise;
      if (url.endsWith('/login/passkey/finish')) {
        return response(200, {
          session_token: 'new-session',
          csrf_token: 'csrf',
          operator: 'admin',
          expires_at: '2030-01-01T00:00:00Z',
          controller_version: 'v2.0.0-rc.7',
        });
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetch);

    useControllerStore.setState({ sessionToken: '', loggedIn: false, passkeyRegistered: null });
    const first = useControllerStore.getState().loginWithPasskey('admin');
    const second = useControllerStore.getState().loginWithPasskey('admin');
    begin.resolve(response(200, {
      challenge,
      allow_credentials: [{ type: 'public-key', id: 'AQIDBA' }],
      rpid: 'panel.example',
      alg: 'webauthn-es256',
    }));
    await Promise.all([first, second]);

    expect(fetch.mock.calls.filter(([input]) => String(input).endsWith('/login/passkey/begin'))).toHaveLength(1);
    expect(fetch.mock.calls.filter(([input]) => String(input).endsWith('/login/passkey/finish'))).toHaveLength(1);
    expect(get).toHaveBeenCalledTimes(1);
  });

  it('runs one passkey-disable chain under a direct double invocation', async () => {
    const begin = deferred<Response>();
    const get = vi.fn().mockResolvedValue(assertionCredential());
    installBrowserCredentials(vi.fn(), get);
    let disableCalls = 0;

    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (!url.endsWith('/passkey/disable')) throw new Error(`unexpected request: ${url}`);
      disableCalls += 1;
      if (disableCalls === 1) return begin.promise;
      return response(200, { registered: false });
    });
    vi.stubGlobal('fetch', fetch);
    useControllerStore.setState({ passkeyRegistered: true });

    const first = useControllerStore.getState().disablePasskey();
    const second = useControllerStore.getState().disablePasskey();
    begin.resolve(response(200, {
      challenge,
      allow_credentials: [{ type: 'public-key', id: 'AQIDBA' }],
      rpid: 'panel.example',
      alg: 'webauthn-es256',
    }));
    await Promise.all([first, second]);

    expect(disableCalls).toBe(2); // one begin + one finish, not two parallel chains
    expect(get).toHaveBeenCalledTimes(1);
    expect(useControllerStore.getState().passkeyRegistered).toBe(false);
  });
});
