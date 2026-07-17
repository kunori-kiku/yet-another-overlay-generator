// @vitest-environment node

import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useUiStore } from './uiStore';
import { CustomEdge } from '../components/canvas/CustomEdge';
import { CustomNode } from '../components/canvas/CustomNode';

// Keep the test focused on YAOG's canvas presentation contract. React Flow's geometry and store
// behavior are covered upstream; these light stubs retain the edge path/label structure while
// allowing the two custom renderers to run in the node test environment.
vi.mock('@xyflow/react', async () => {
  const { createElement: element, Fragment } = await import('react');
  return {
    BaseEdge: ({ id }: { id: string }) => element('span', { 'data-testid': `edge-path-${id}` }),
    EdgeLabelRenderer: ({ children }: { children: ReturnType<typeof element> }) =>
      element(Fragment, null, children),
    getBezierPath: () => ['M 0 0', 10, 10],
    Handle: () => element('span', { 'data-testid': 'node-handle' }),
    Position: { Bottom: 'bottom', Top: 'top' },
    useStoreApi: () => ({
      getState: () => ({
        addSelectedEdges: () => undefined,
        elementsSelectable: true,
      }),
    }),
  };
});

function renderEdge(showLinkAddresses: boolean): string {
  return renderToStaticMarkup(
    createElement(CustomEdge, {
      id: 'edge-1',
      sourceX: 0,
      sourceY: 0,
      targetX: 20,
      targetY: 20,
      selected: false,
      data: {
        edgeType: 'direct',
        label: '203.0.113.8',
        port: 51820,
        sourceNodeName: 'alpha',
        targetNodeName: 'beta',
        showLinkAddresses,
      },
    } as never),
  );
}

function renderNode(showOverlayIps: boolean): string {
  return renderToStaticMarkup(
    createElement(CustomNode, {
      selected: false,
      data: {
        label: 'alpha',
        role: 'peer',
        overlayIp: '10.20.0.8',
        showOverlayIps,
        domainName: 'mesh',
      },
    } as never),
  );
}

afterEach(() => {
  useUiStore.setState({ showLinkAddresses: true, showOverlayIps: true });
});

describe('canvas display preferences', () => {
  it('hides link and overlay addresses independently without removing the topology', () => {
    useUiStore.setState({ showLinkAddresses: true, showOverlayIps: true });
    expect(renderEdge(true)).toContain('203.0.113.8');
    expect(renderEdge(true)).toContain(':51820');
    expect(renderNode(true)).toContain('10.20.0.8');

    useUiStore.getState().setShowLinkAddresses(false);
    const hiddenLink = renderEdge(false);
    expect(hiddenLink).not.toContain('203.0.113.8');
    expect(hiddenLink).not.toContain(':51820');
    expect(hiddenLink).toContain('alpha → beta');
    expect(hiddenLink).toContain('data-testid="edge-label-edge-1"');
    expect(hiddenLink).toContain('data-testid="edge-path-edge-1"');
    expect(renderNode(true)).toContain('10.20.0.8');

    useUiStore.getState().setShowOverlayIps(false);
    expect(renderNode(false)).not.toContain('10.20.0.8');

    const partialize = useUiStore.persist.getOptions().partialize;
    expect(partialize?.(useUiStore.getState())).toMatchObject({
      showLinkAddresses: false,
      showOverlayIps: false,
    });
  });
});
