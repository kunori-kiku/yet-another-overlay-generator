// @vitest-environment node
//
// controllerStore.signingRecovery.test.ts — pins the plan-3 signing-handle auto-recovery:
// hydrateKeystoneStatus, after recording server truth, recovers the NON-SECRET WebAuthn signing
// descriptor (credentialId + alg + rpId + public PEM, now served by GET /operator-credential) into
// the local browser handle so a cleared/fresh browser can re-prompt the authenticator on Deploy
// WITHOUT a fleet-stranding re-pin. The server tuple is reconciled atomically: it replaces stale or
// partial local public descriptors wholesale, while raw-ed25519/incomplete/unpinned status clears
// an incompatible browser handle. Only public material is restored; YAOG never handles plaintext
// private-key material.
//
// Node-env store-seam test (no jsdom): global.fetch is stubbed so hydrateKeystoneStatus runs the
// real getOperatorCredentialStatus boundary mapping (snake_case → camelCase) end to end.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useControllerStore, selectHasLocalSigningKey } from './controllerStore';

// A minimal fetch Response stand-in exposing only what request()/getOperatorCredentialStatus read.
function resp(status: number, body: unknown) {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  } as unknown as Response;
}

// credStatus builds the SERVER (snake_case) GET /operator-credential body, WebAuthn-ES256 by
// default, overridable per case.
function credStatus(over: Record<string, unknown>) {
  return {
    pinned: true,
    alg: 'webauthn-es256',
    credential_id: 'cred-A',
    rpid: 'rp.example',
    origin: 'https://rp.example',
    fingerprint: 'fp-A',
    public_key_pem: 'PEM-A',
    redeploy_required: false,
    ...over,
  };
}

// stubFetch routes the operator-credential probe to body; any other URL gets a benign {}.
function stubFetch(body: unknown) {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString();
    if (/operator-credential(\?|$)/.test(url)) return resp(200, body);
    return resp(200, {});
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const EMPTY_LOCAL = {
  operatorCredentialId: null,
  operatorCredentialAlg: null,
  operatorRpId: null,
  operatorPublicKeyPEM: null,
} as const;

beforeEach(() => {
  useControllerStore.setState({
    mode: 'controller',
    authGeneration: 0,
    ...EMPTY_LOCAL,
    pendingKeystoneEnrollment: null,
    pendingKeystoneRotate: false,
    serverOperatorPinned: false,
    serverOperatorAlg: null,
    serverOperatorRpId: null,
    serverOperatorOrigin: null,
    serverOperatorPublicKeyPEM: null,
    serverOperatorFingerprint: null,
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  useControllerStore.setState({ mode: 'local', ...EMPTY_LOCAL, loggedIn: false });
});

describe('signing-handle auto-recovery (plan-3)', () => {
  it('recovers the WebAuthn descriptor into empty local slots on a fresh browser', async () => {
    stubFetch(credStatus({}));
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBe('cred-A');
    expect(s.operatorCredentialAlg).toBe('webauthn-es256');
    expect(s.operatorRpId).toBe('rp.example');
    expect(s.operatorPublicKeyPEM).toBe('PEM-A');
    expect(s.serverOperatorRpId).toBe('rp.example');
    expect(s.serverOperatorOrigin).toBe('https://rp.example');
    expect(s.serverOperatorPublicKeyPEM).toBe('PEM-A');
    // The deploy() signing path can now proceed (it would re-prompt the authenticator for a tap).
    expect(selectHasLocalSigningKey(s)).toBe(true);
  });

  it('atomically replaces a complete stale local descriptor with server truth', async () => {
    useControllerStore.setState({
      operatorCredentialId: 'cred-LOCAL',
      operatorCredentialAlg: 'webauthn-eddsa',
      operatorRpId: 'local.rp',
      operatorPublicKeyPEM: 'PEM-LOCAL',
    });
    stubFetch(credStatus({ credential_id: 'cred-SERVER', public_key_pem: 'PEM-SERVER' }));
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBe('cred-SERVER');
    expect(s.operatorCredentialAlg).toBe('webauthn-es256');
    expect(s.operatorRpId).toBe('rp.example');
    expect(s.operatorPublicKeyPEM).toBe('PEM-SERVER');
  });

  it('keeps an already-exact browser handle aligned with server truth', async () => {
    useControllerStore.setState({
      operatorCredentialId: 'cred-A',
      operatorCredentialAlg: 'webauthn-es256',
      operatorRpId: 'rp.example',
      operatorPublicKeyPEM: 'PEM-A',
    });
    stubFetch(credStatus({}));

    await useControllerStore.getState().hydrateKeystoneStatus();

    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBe('cred-A');
    expect(s.operatorCredentialAlg).toBe('webauthn-es256');
    expect(s.operatorRpId).toBe('rp.example');
    expect(s.operatorPublicKeyPEM).toBe('PEM-A');
    expect(selectHasLocalSigningKey(s)).toBe(true);
  });

  it('keeps a raw-ed25519 public descriptor for manual kit use without treating it as browser-signable', async () => {
    useControllerStore.setState({
      operatorCredentialId: 'stale-browser-id',
      operatorCredentialAlg: 'webauthn-es256',
      operatorRpId: 'stale.example',
      operatorPublicKeyPEM: 'STALE-PEM',
    });
    stubFetch(credStatus({ alg: 'ed25519', rpid: '', origin: '' }));
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBeNull();
    expect(s.operatorPublicKeyPEM).toBeNull();
    // Server truth and PUBLIC manual-kit material are retained; only the browser signing handle
    // recovery is skipped because a raw-ed25519 private key cannot be invoked through WebAuthn.
    expect(s.serverOperatorPinned).toBe(true);
    expect(s.serverOperatorAlg).toBe('ed25519');
    expect(s.serverOperatorRpId).toBeNull();
    expect(s.serverOperatorOrigin).toBeNull();
    expect(s.serverOperatorPublicKeyPEM).toBe('PEM-A');
  });

  it('skips when the controller has nothing pinned', async () => {
    useControllerStore.setState({
      operatorCredentialId: 'stale-browser-id',
      operatorCredentialAlg: 'webauthn-es256',
      operatorRpId: 'stale.example',
      operatorPublicKeyPEM: 'STALE-PEM',
    });
    stubFetch({ pinned: false });
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBeNull();
    expect(selectHasLocalSigningKey(s)).toBe(false);
  });

  it('does not turn a malformed status response into authoritative keystone-off', async () => {
    useControllerStore.setState({ serverOperatorPinned: null });
    stubFetch({});
    await useControllerStore.getState().hydrateKeystoneStatus();
    expect(useControllerStore.getState().serverOperatorPinned).toBeNull();
  });

  it('replaces a partial stale descriptor wholesale instead of splicing fields', async () => {
    useControllerStore.setState({
      operatorCredentialId: 'stale-id',
      operatorCredentialAlg: 'webauthn-eddsa',
      operatorRpId: null,
      operatorPublicKeyPEM: null,
    });
    stubFetch(credStatus({}));
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBe('cred-A');
    expect(s.operatorCredentialAlg).toBe('webauthn-es256');
    expect(s.operatorPublicKeyPEM).toBe('PEM-A');
    expect(s.operatorRpId).toBe('rp.example');
  });

  it.each([
    ['credential id', { credential_id: '' }],
    ['RP ID', { rpid: '' }],
    ['public key', { public_key_pem: '' }],
  ])('clears a stale browser handle when the pinned WebAuthn status lacks %s', async (_label, over) => {
    useControllerStore.setState({
      operatorCredentialId: 'stale-browser-id',
      operatorCredentialAlg: 'webauthn-es256',
      operatorRpId: 'stale.example',
      operatorPublicKeyPEM: 'STALE-PEM',
    });
    stubFetch(credStatus(over));

    await useControllerStore.getState().hydrateKeystoneStatus();

    expect(selectHasLocalSigningKey(useControllerStore.getState())).toBe(false);
    expect(useControllerStore.getState().operatorCredentialId).toBeNull();
    expect(useControllerStore.getState().operatorRpId).toBeNull();
  });

  it('lets only the most recently-started status probe reconcile the browser handle', async () => {
    const older = deferred<Response>();
    const newer = deferred<Response>();
    let calls = 0;
    vi.stubGlobal('fetch', vi.fn(async () => {
      calls += 1;
      return calls === 1 ? older.promise : newer.promise;
    }));

    const olderProbe = useControllerStore.getState().hydrateKeystoneStatus();
    const newerProbe = useControllerStore.getState().hydrateKeystoneStatus();
    newer.resolve(resp(200, credStatus({
      credential_id: 'cred-NEW',
      rpid: 'new.example',
      origin: 'https://new.example',
      public_key_pem: 'PEM-NEW',
      fingerprint: 'fp-NEW',
    })));
    await newerProbe;
    older.resolve(resp(200, credStatus({})));
    await olderProbe;

    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBe('cred-NEW');
    expect(s.operatorRpId).toBe('new.example');
    expect(s.operatorPublicKeyPEM).toBe('PEM-NEW');
    expect(s.serverOperatorFingerprint).toBe('fp-NEW');
  });
});
