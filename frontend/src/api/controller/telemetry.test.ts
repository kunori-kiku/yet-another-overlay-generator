import { afterEach, describe, expect, it, vi } from 'vitest';
import { nodeHistory } from './telemetry';
import type { ControllerConfig } from './transport';

const cfg: ControllerConfig = {
  baseURL: 'https://controller.test',
  pathPrefix: '',
  operatorToken: 'operator-token',
  csrfToken: '',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('nodeHistory', () => {
  it('bypasses browser caches while preserving the credentialed operator request', async () => {
    const fetchFn = vi.fn(async () =>
      new Response(JSON.stringify({ step: '30s', disabled: false, buckets: [] }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchFn);

    const history = await nodeHistory(
      cfg,
      'node a',
      '2026-07-16T00:00:00.000Z',
      '2026-07-16T06:00:00.000Z',
    );

    const [url, init] = fetchFn.mock.calls[0] as [string, RequestInit];
    const query = new URL(url);
    expect(query.pathname).toBe('/api/v1/operator/node-history');
    expect(query.searchParams.get('node')).toBe('node a');
    expect(init.cache).toBe('no-store');
    expect(init.credentials).toBe('include');
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer operator-token');
    expect(history).toEqual({ step: '30s', disabled: false, buckets: [] });
  });
});
