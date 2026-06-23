// @vitest-environment node
//
// controllerStore.signingRecovery.test.ts — pins the plan-3 signing-handle auto-recovery:
// hydrateKeystoneStatus, after recording server truth, recovers the NON-SECRET WebAuthn signing
// descriptor (credentialId + alg + rpId + public PEM, now served by GET /operator-credential) into
// the EMPTY local slots so a cleared/fresh browser can re-prompt the authenticator on Deploy
// WITHOUT a fleet-stranding re-pin. The recovery is fill-empty-only (never clobbers a freshly
// enrolled local cache) and WebAuthn-only (a raw-ed25519 CLI keystone signs off-host and is left
// untouched). The private key never leaves the authenticator; only public material is restored.
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

const EMPTY_LOCAL = {
  operatorCredentialId: null,
  operatorCredentialAlg: null,
  operatorRpId: null,
  operatorPublicKeyPEM: null,
} as const;

beforeEach(() => {
  useControllerStore.setState({
    mode: 'controller',
    ...EMPTY_LOCAL,
    serverOperatorPinned: false,
    serverOperatorAlg: null,
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
    // The deploy() signing path can now proceed (it would re-prompt the authenticator for a tap).
    expect(selectHasLocalSigningKey(s)).toBe(true);
  });

  it('does NOT clobber a freshly-enrolled local descriptor (fill-empty-only)', async () => {
    useControllerStore.setState({
      operatorCredentialId: 'cred-LOCAL',
      operatorCredentialAlg: 'webauthn-eddsa',
      operatorRpId: 'local.rp',
      operatorPublicKeyPEM: 'PEM-LOCAL',
    });
    stubFetch(credStatus({ credential_id: 'cred-SERVER', public_key_pem: 'PEM-SERVER' }));
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBe('cred-LOCAL');
    expect(s.operatorCredentialAlg).toBe('webauthn-eddsa');
    expect(s.operatorPublicKeyPEM).toBe('PEM-LOCAL');
  });

  it('skips a raw-ed25519 (CLI) keystone — not browser-signable', async () => {
    stubFetch(credStatus({ alg: 'ed25519' }));
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBeNull();
    expect(s.operatorPublicKeyPEM).toBeNull();
    // Server truth is still recorded (the badge reads "enrolled"); only browser recovery is skipped.
    expect(s.serverOperatorPinned).toBe(true);
  });

  it('skips when the controller has nothing pinned', async () => {
    stubFetch({ pinned: false });
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBeNull();
    expect(selectHasLocalSigningKey(s)).toBe(false);
  });

  it('fills only the empty slots when the local descriptor is partial', async () => {
    // id+alg already present (e.g. an older record) but the PEM + rpId missing → recover only those.
    useControllerStore.setState({
      operatorCredentialId: 'cred-A',
      operatorCredentialAlg: 'webauthn-es256',
      operatorRpId: null,
      operatorPublicKeyPEM: null,
    });
    stubFetch(credStatus({}));
    await useControllerStore.getState().hydrateKeystoneStatus();
    const s = useControllerStore.getState();
    expect(s.operatorCredentialId).toBe('cred-A'); // unchanged
    expect(s.operatorPublicKeyPEM).toBe('PEM-A'); // filled
    expect(s.operatorRpId).toBe('rp.example'); // filled
  });
});
