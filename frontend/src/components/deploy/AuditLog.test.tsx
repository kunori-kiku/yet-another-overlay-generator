// @vitest-environment node

import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { AuditLog } from './AuditLog';

// Render-only component test: make Zustand selectors read the live seeded state instead of React's
// immutable creation-time SSR snapshot. The vanilla store/middleware behavior remains real.
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

const originalAudit = useControllerStore.getState().audit;
const originalVerified = useControllerStore.getState().auditVerified;
const originalLanguage = useTopologyStore.getState().language;

afterEach(() => {
  useControllerStore.setState({ audit: originalAudit, auditVerified: originalVerified });
  useTopologyStore.setState({ language: originalLanguage });
});

describe('AuditLog operational-noise filtering', () => {
  it('hides legacy agent reports while retaining meaningful events and complete-chain status', () => {
    useTopologyStore.setState({ language: 'en' });
    useControllerStore.setState({
      auditVerified: true,
      audit: [
        { timestamp: '2026-07-17T00:00:00Z', actor: 'agent:node-1', action: 'report', nodeId: 'node-1' },
        { timestamp: '2026-07-17T00:01:00Z', actor: 'operator:admin', action: 'promote', nodeId: '' },
        { timestamp: '2026-07-17T00:02:00Z', actor: 'agent:node-1', action: 'rekey', nodeId: 'node-1' },
      ],
    });

    const html = renderToStaticMarkup(createElement(AuditLog));

    expect(html).not.toContain('>report<');
    expect(html).toContain('>promote<');
    expect(html).toContain('>rekey<');
    expect(html).toContain('✓ Verified');
  });

  it('shows the empty state when the retained chain contains only legacy reports', () => {
    useTopologyStore.setState({ language: 'en' });
    useControllerStore.setState({
      auditVerified: true,
      audit: [
        { timestamp: '2026-07-17T00:00:00Z', actor: 'agent:node-1', action: 'report', nodeId: 'node-1' },
      ],
    });

    const html = renderToStaticMarkup(createElement(AuditLog));

    expect(html).not.toContain('<table');
    expect(html).not.toContain('>report<');
    expect(html).toContain('No operator or security audit events yet');
    expect(html).toContain('✓ Verified');
  });
});
