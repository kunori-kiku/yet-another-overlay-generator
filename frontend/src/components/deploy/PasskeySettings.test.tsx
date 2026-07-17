import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { shouldShowWebAuthnEnrollmentNotice } from './webauthnEnrollmentPolicy';
import { t } from '../../i18n';
import { PasskeySettings } from './PasskeySettings';
import { DeployBar } from './DeployBar';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';

// These are render-only component tests, not hydration tests. Zustand normally asks React's server
// renderer for the store's immutable creation-time snapshot. Preserve its real vanilla store and
// middleware, but bind selectors to the seeded live snapshot so the authenticated surfaces can be
// exercised without adding a browser DOM dependency to the Node suite.
vi.mock('zustand', async (importOriginal) => {
  const actual = await importOriginal<typeof import('zustand')>();
  type Creator = Parameters<typeof actual.create>[0];
  const createLiveStore = (creator: Creator) => {
    const api = actual.createStore(creator);
    const boundStore = (selector?: (state: ReturnType<typeof api.getState>) => unknown) => (
      selector ? selector(api.getState()) : api.getState()
    );
    return Object.assign(boundStore, api);
  };
  return {
    ...actual,
    create: ((creator?: Creator) => (creator ? createLiveStore(creator) : createLiveStore)) as typeof actual.create,
  };
});

beforeEach(() => {
  localStorage.clear();
  useTopologyStore.setState({ language: 'en' });
  useControllerStore.setState({
    mode: 'controller',
    sessionToken: 'session',
    loggedIn: true,
    passkeyRegistered: true,
    pendingLoginPasskeyEnrollment: null,
    loginCeremony: false,
    operatorToken: '',
    loading: false,
    signing: false,
    enrolling: false,
    error: null,
    serverOperatorPinned: true,
    serverOperatorAlg: 'webauthn-es256',
    serverOperatorFingerprint: 'already-enrolled',
    serverRedeployRequired: false,
    pendingKeystoneEnrollment: null,
    pendingKeystoneRotate: false,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  useTopologyStore.setState({ language: 'en' });
  useControllerStore.setState({
    mode: 'local',
    sessionToken: '',
    loggedIn: false,
    passkeyRegistered: null,
    serverOperatorPinned: null,
    serverOperatorAlg: null,
    serverOperatorFingerprint: null,
    deployPreview: null,
  });
});

describe('PasskeySettings enrollment policy notice', () => {
  it('warns an already-registered user about grandfathered/non-UV credential risk', () => {
    expect(shouldShowWebAuthnEnrollmentNotice(true, true)).toBe(true);
    expect(shouldShowWebAuthnEnrollmentNotice(true, false)).toBe(true);
    expect(shouldShowWebAuthnEnrollmentNotice(true, null)).toBe(false);
    expect(shouldShowWebAuthnEnrollmentNotice(false, true)).toBe(false);

    const warning = t('en', 'security.webAuthnEnrollmentWarning');
    expect(warning).toContain('existing accounts and deployed fleets are not locked out');
    expect(warning).toContain('A non-UV credential may be duplicable');
  });

  it.each([
    ['en', 'A non-UV credential may be duplicable'],
    ['zh', '非 UV 凭据可能被复制'],
  ] as const)(
    'renders the %s warning on both grandfathered login and operator credentials',
    (language, distinctiveCopy) => {
      useTopologyStore.setState({ language });

      const loginPasskeyMarkup = renderToStaticMarkup(createElement(PasskeySettings));
      const operatorKeystoneMarkup = renderToStaticMarkup(createElement(DeployBar));

      expect(loginPasskeyMarkup).toContain(distinctiveCopy);
      expect(operatorKeystoneMarkup).toContain(distinctiveCopy);
    },
  );
});

describe('DeployBar preview containment', () => {
  it('keeps long rollout warnings and the confirmation controls inside a scrollable viewport', () => {
    useControllerStore.setState({
      deployPreview: {
        keystoneFullRestage: false,
        nodes: [],
        skippedUnenrolled: [],
        telemetryPolicyOmittedNodeIDs: ['node-' + 'x'.repeat(120)],
      },
    });

    const markup = renderToStaticMarkup(createElement(DeployBar));

    expect(markup).toContain('max-h-[calc(100dvh-2rem)]');
    expect(markup).toContain('max-h-24 overflow-y-auto break-all');
    expect(markup).toContain('aria-labelledby="deploy-preview-title"');
    expect(markup).toContain('data-testid="deploy-preview-confirm"');
  });
});
