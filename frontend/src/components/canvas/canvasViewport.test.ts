// @vitest-environment node

import { getViewportForBounds } from '@xyflow/react';
import { describe, expect, it } from 'vitest';
import { MIN_CANVAS_ZOOM } from './canvasViewport';

describe('large-topology viewport', () => {
  it('can fit the supported 2,000-node default layout below the old zoom floor', () => {
    // TopologyCanvas initially lays nodes out in four columns, 250px apart. At the schema's
    // 2,000-node ceiling that produces a roughly 125,000px-high graph before Auto layout.
    const bounds = { x: 0, y: 0, width: 1_020, height: 124_860 };
    const viewport = getViewportForBounds(
      bounds,
      1_200,
      700,
      MIN_CANVAS_ZOOM,
      2,
      0.2,
    );

    expect(viewport.zoom).toBeGreaterThanOrEqual(MIN_CANVAS_ZOOM);
    expect(viewport.zoom).toBeLessThan(0.01);
    expect(bounds.height * viewport.zoom).toBeLessThan(700);
  });
});
