// @vitest-environment node
//
// controllerStore.controllerVersion.test.ts — pins the plan-8 controller-version plumbing: the
// store captures the controller's own build version (server truth from GET /session + login) and
// clears it on a genuine session loss / logout, but KEEPS it under a break-glass token (authed,
// just "not a login"). The panel reads controllerVersion to display it (UserMenu) and to drive the
// one-click "update all agents to the controller's version" + the refuse-newer advisory.
//
// Node-env store-seam test (no jsdom): global.fetch is stubbed so checkSession() runs end-to-end.
// We drive the BREAK-GLASS shape (empty csrf_token) because it does NOT trigger hydrateFromServer
// (which would need the whole topology/nodes surface mocked) — only the best-effort keystone probe,
// which swallows any shape mismatch. The version is set in the same info-present branch the genuine
// cookie path takes, so this also covers the login case's set.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useControllerStore } from './controllerStore';

// A minimal fetch Response stand-in exposing only what getSession + the best-effort hydrators read
// (status / ok / json / text). Avoids depending on a global Response in the node test env.
function resp(status: number, body: unknown) {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
  } as unknown as Response;
}

// stubFetch routes /session to sessionBody (or a 401 when null); every other URL (the keystone
// status probe) gets a benign {} so the best-effort hydrator neither throws nor mutates the version.
function stubFetch(sessionBody: unknown | null) {
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString();
    if (/\/session(\?|$)/.test(url)) {
      return sessionBody === null ? resp(401, '') : resp(200, sessionBody);
    }
    return resp(200, {});
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

beforeEach(() => {
  useControllerStore.setState({ mode: 'controller', controllerVersion: '', controllerCapabilities: [] });
});

afterEach(() => {
  vi.unstubAllGlobals();
  useControllerStore.setState({
    mode: 'local', controllerVersion: '', controllerCapabilities: [], loggedIn: false,
  });
});

describe('controllerVersion plumbing (plan-8)', () => {
  it('checkSession captures controller_version from an authed /session probe', async () => {
    stubFetch({
      operator: 'breakglass', expires_at: '', csrf_token: '', controller_version: 'v2.0.0-beta.9',
      controller_capabilities: ['telemetry-policy-v2-topology'],
    });
    await useControllerStore.getState().checkSession();
    // Break-glass (empty csrf) is authed but not a login: version retained, loggedIn stays false.
    expect(useControllerStore.getState().controllerVersion).toBe('v2.0.0-beta.9');
    expect(useControllerStore.getState().controllerCapabilities).toEqual(['telemetry-policy-v2-topology']);
    expect(useControllerStore.getState().loggedIn).toBe(false);
  });

  it('checkSession keeps the literal "dev" identity from an unstamped controller', async () => {
    // A dev/`go run` controller normalizes its empty BuildVersion to "dev" and sends it verbatim;
    // the store holds it as-is (the panel decides usability — "dev" is non-semver, so the one-click
    // affordance is gated off at the component via isUsableControllerVersion, not here).
    stubFetch({ operator: 'breakglass', expires_at: '', csrf_token: '', controller_version: 'dev' });
    await useControllerStore.getState().checkSession();
    expect(useControllerStore.getState().controllerVersion).toBe('dev');
  });

  it('checkSession leaves controllerVersion empty when an older controller omits the field', async () => {
    // Forward-compat: a controller predating the controller_version field sends no key at all; the
    // `?? ''` mapping collapses the absence to ''. (This is NOT the dev-build shape above.)
    stubFetch({ operator: 'breakglass', expires_at: '', csrf_token: '' });
    await useControllerStore.getState().checkSession();
    expect(useControllerStore.getState().controllerVersion).toBe('');
    expect(useControllerStore.getState().controllerCapabilities).toEqual([]);
  });

  it('checkSession clears a stale controllerVersion on a logged-out (401) probe', async () => {
    useControllerStore.setState({
      controllerVersion: 'v2.0.0-beta.9', controllerCapabilities: ['telemetry-policy-v2-topology'],
    });
    stubFetch(null); // 401 → getSession returns null → genuine session loss
    await useControllerStore.getState().checkSession();
    expect(useControllerStore.getState().controllerVersion).toBe('');
    expect(useControllerStore.getState().controllerCapabilities).toEqual([]);
  });

  it('logout clears controllerVersion', async () => {
    useControllerStore.setState({
      controllerVersion: 'v2.0.0-beta.9', controllerCapabilities: ['telemetry-policy-v2-topology'], loggedIn: true,
    });
    stubFetch({ operator: 'x', expires_at: '', csrf_token: '' }); // logout best-effort POSTs; tolerated
    await useControllerStore.getState().logout();
    expect(useControllerStore.getState().controllerVersion).toBe('');
    expect(useControllerStore.getState().controllerCapabilities).toEqual([]);
  });
});
