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
    expect(history).toEqual({ step: '30s', disabled: false, buckets: [], probes: [] });
  });

  it('threads an exact probe selector and AbortSignal without widening the browser request', async () => {
    const fetchFn = vi.fn(async () =>
      new Response(JSON.stringify({ step: '30s', disabled: false, buckets: [], probes: [] }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchFn);
    const controller = new AbortController();

    await nodeHistory(
      cfg,
      'node-a',
      '2026-07-16T00:00:00.000Z',
      '2026-07-16T06:00:00.000Z',
      '5m',
      {
        probe: { id: 'db', type: 'tcp', host: 'db.example', port: 5432 },
        signal: controller.signal,
      },
    );

    const [rawURL, init] = fetchFn.mock.calls[0] as [string, RequestInit];
    const query = new URL(rawURL).searchParams;
    expect(query.get('step')).toBe('5m');
    expect(query.get('probe_id')).toBe('db');
    expect(query.get('probe_type')).toBe('tcp');
    expect(query.get('probe_host')).toBe('db.example');
    expect(query.get('probe_port')).toBe('5432');
    expect(query.has('include_probes')).toBe(false);
    expect(init.signal).toBe(controller.signal);
  });

  it('can request resource history without probe series', async () => {
    const fetchFn = vi.fn(async () =>
      new Response(JSON.stringify({ step: '30s', disabled: false, buckets: [], probes: [] }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchFn);

    await nodeHistory(
      cfg,
      'node-a',
      '2026-07-16T00:00:00.000Z',
      '2026-07-16T06:00:00.000Z',
      undefined,
      { includeProbes: false },
    );

    const [rawURL] = fetchFn.mock.calls[0] as [string, RequestInit];
    expect(new URL(rawURL).searchParams.get('include_probes')).toBe('false');
  });
});
