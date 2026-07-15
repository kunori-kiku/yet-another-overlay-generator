import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ManualKitApplyGuide } from './ManualKitApplyGuide';

const nodes = [{ id: 'manual-1', name: 'Manual One' }];

describe('ManualKitApplyGuide', () => {
  it('renders public-credential actions and the complete WebAuthn command from state', () => {
    const html = renderToStaticMarkup(
      createElement(ManualKitApplyGuide, {
        language: 'en',
        nodes,
        fingerprint: 'sha256:fingerprint',
        trust: {
          pinned: true,
          alg: 'webauthn-es256',
          rpId: 'panel.example',
          origin: 'https://panel.example',
          publicKeyPEM: 'PUBLIC PEM',
        },
      }),
    );
    expect(html).toContain('Copy public credential');
    expect(html).toContain('Download public credential');
    expect(html).toContain('Copy exact flags');
    expect(html).toContain('--operator-rpid');
    expect(html).toContain('--operator-origin');
    expect(html).not.toContain('--dangerously-allow-no-keystone');
  });

  it('does not expose the legacy bypass while status is unknown or pinned metadata is incomplete', () => {
    for (const trust of [
      { pinned: null, alg: null, rpId: null, origin: null, publicKeyPEM: null },
      { pinned: true, alg: 'ed25519', rpId: null, origin: null, publicKeyPEM: null },
    ] as const) {
      const html = renderToStaticMarkup(
        createElement(ManualKitApplyGuide, { language: 'en', nodes, fingerprint: null, trust }),
      );
      expect(html).not.toContain('--dangerously-allow-no-keystone');
      expect(html).not.toContain('Copy public credential');
    }
  });

  it('renders the visually separate dangerous path only for authoritative keystone-off', () => {
    const html = renderToStaticMarkup(
      createElement(ManualKitApplyGuide, {
        language: 'zh',
        nodes,
        fingerprint: null,
        trust: { pinned: false, alg: null, rpId: null, origin: null, publicKeyPEM: null },
      }),
    );
    expect(html).toContain('危险的旧版路径');
    expect(html).toContain('--dangerously-allow-no-keystone');
    expect(html).not.toContain('--operator-cred ');
  });
});
