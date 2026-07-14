import { afterEach, describe, expect, it, vi } from 'vitest';
import { downloadManualNodeBundle } from './controllerClient';
import type { ControllerConfig } from './controllerClient';

// Unit coverage for the manual-node bundle download (mixed-controller-local-mode plan-6): the
// operator-route URL construction (operator namespace + url-encoded node id), the bearer attachment,
// and the Content-Disposition filename parse (+ fallback). The bundle production + zero-knowledge are
// covered by the Go handler_manual_node tests; this pins the FE client contract.

const cfg: ControllerConfig = {
  baseURL: 'http://ctl.test',
  pathPrefix: '',
  operatorToken: 'op-bearer',
  csrfToken: '',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubFetch(resp: Response) {
  const fn = vi.fn(async () => resp);
  vi.stubGlobal('fetch', fn);
  return fn;
}

describe('downloadManualNodeBundle', () => {
  it('GETs the operator route with the url-encoded node id + bearer, and parses the filename header', async () => {
    const blob = new Blob(['PKzip-bytes'], { type: 'application/zip' });
    const fetchFn = stubFetch(
      new Response(blob, {
        status: 200,
        headers: { 'Content-Disposition': 'attachment; filename="alpha node-bundle.zip"' },
      }),
    );

    const out = await downloadManualNodeBundle(cfg, 'alpha node');

    const [url, init] = fetchFn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('http://ctl.test/api/v1/operator/manual-node-bundle?node=alpha%20node');
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer op-bearer');
    expect(init.credentials).toBe('include');
    expect(out.filename).toBe('alpha node-bundle.zip');
    expect(out.blob.size).toBeGreaterThan(0);
  });

  it('falls back to <node>-bundle.zip when the response carries no Content-Disposition', async () => {
    stubFetch(new Response(new Blob(['z']), { status: 200 }));
    const out = await downloadManualNodeBundle(cfg, 'mike');
    expect(out.filename).toBe('mike-bundle.zip');
  });

  it('rejects on a non-2xx (e.g. 404 — node not yet promoted, or not a manual node)', async () => {
    stubFetch(new Response('{"error":{"code":"config_not_found","message":"no manual node"}}', { status: 404 }));
    await expect(downloadManualNodeBundle(cfg, 'ghost')).rejects.toBeTruthy();
  });
});
