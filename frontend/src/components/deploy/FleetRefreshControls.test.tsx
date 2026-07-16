// @vitest-environment node

import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FleetRefreshControls } from './FleetRefreshControls';
import type { FleetLiveRefreshState } from '../../hooks/useFleetLiveRefresh';

function state(overrides: Partial<FleetLiveRefreshState> = {}): FleetLiveRefreshState {
  return {
    live: true,
    setLive: vi.fn(),
    refreshNow: vi.fn(async () => undefined),
    refreshing: false,
    hidden: false,
    consecutiveFailures: 0,
    lastSyncedAt: 95_000,
    nextRefreshAt: 110_000,
    ...overrides,
  };
}

function visibleHealthTag(html: string): string {
  return html.match(/<span[^>]*data-testid="fleet-refresh-visible-health"[^>]*>/)?.[0] ?? '';
}

describe('FleetRefreshControls', () => {
  beforeEach(() => {
    vi.spyOn(Date, 'now').mockReturnValue(100_000);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('shows healthy age/countdown silently and keeps the idle refresh icon static', () => {
    const html = renderToStaticMarkup(createElement(FleetRefreshControls, {
      state: state(), language: 'en', refreshTestID: 'refresh-test',
    }));
    expect(visibleHealthTag(html)).not.toContain('aria-live');
    expect(html).toContain('data-testid="fleet-refresh-announcement"></span>');
    expect(html).toContain('Live on');
    expect(html).toContain('Updated 5s ago');
    expect(html).toContain('Next refresh in 10s');
    expect(html).not.toContain('animate-spin');
  });

  it('animates only an in-flight refresh and includes a reduced-motion fallback', () => {
    const html = renderToStaticMarkup(createElement(FleetRefreshControls, {
      state: state({ refreshing: true }), language: 'en', refreshTestID: 'refresh-test',
    }));
    expect(html).toContain('motion-safe:animate-spin');
    expect(html).toContain('motion-reduce:animate-none');
    expect(html).toContain('Refreshing');
    expect(visibleHealthTag(html)).not.toContain('aria-live');
    expect(html).toContain('data-testid="fleet-refresh-announcement"></span>');
  });

  it('exposes paused and stale states instead of presenting them as healthy Live', () => {
    const paused = renderToStaticMarkup(createElement(FleetRefreshControls, {
      state: state({ hidden: true, nextRefreshAt: null }), language: 'en', refreshTestID: 'refresh-test',
    }));
    expect(paused).toContain('paused');
    expect(visibleHealthTag(paused)).not.toContain('aria-live');
    expect(paused).toContain('data-testid="fleet-refresh-announcement">Live paused while this tab is hidden</span>');
    expect(paused).not.toContain('Next refresh');

    const stale = renderToStaticMarkup(createElement(FleetRefreshControls, {
      state: state({ consecutiveFailures: 2 }), language: 'en', refreshTestID: 'refresh-test',
    }));
    expect(stale).toContain('Live data is stale');
    expect(visibleHealthTag(stale)).not.toContain('aria-live');
    expect(stale).toContain('data-testid="fleet-refresh-announcement">Live data is stale</span>');
  });
});
