import { afterEach, describe, expect, it, vi } from 'vitest';
import { deployPreview, mapAgentCapabilities, stage } from './controllerClient';
import type { ControllerConfig } from './controllerClient';

// Unit coverage for the plan-6 deploy-preview client contract (feat/deploy-force-preview): the route
// is now a POST that carries the CURRENT canvas as the body (exactly like compilePreview /
// updateTopology), and the response no longer carries topology_version. The changed/unchanged LOGIC is
// proven in the Go regression suite; this pins the FE boundary: the HTTP method + body, the bearer +
// credentialed request, the snake_case→camelCase mapping, and the defensive null handling.

const cfg: ControllerConfig = {
  baseURL: 'http://ctl.test',
  pathPrefix: '',
  operatorToken: 'op-bearer',
  csrfToken: '',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubFetch(body: unknown, status = 200) {
  const fn = vi.fn(async () => new Response(JSON.stringify(body), { status }));
  vi.stubGlobal('fetch', fn);
  return fn;
}

describe('deployPreview', () => {
  it('POSTs the topology body to the operator route with the bearer, and maps the response', async () => {
    const fetchFn = stubFetch({
      keystone_full_restage: false,
      nodes: [
        { node_id: 'n1', name: 'router', changed: true },
        { node_id: 'n2', name: 'peer', changed: false },
      ],
      skipped_unenrolled: ['n9'],
      telemetry_policy_omitted_node_ids: [],
    });

    const topoJSON = JSON.stringify({ project: { id: 'p', name: 'P' }, domains: [], nodes: [], edges: [] });
    const out = await deployPreview(cfg, topoJSON);

    const [url, init] = fetchFn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('http://ctl.test/api/v1/operator/deploy-preview');
    expect(init.method).toBe('POST');
    // The current canvas rides as the body verbatim (public-keys-only; the caller strips first).
    expect(init.body).toBe(topoJSON);
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer op-bearer');
    expect(headers.get('Content-Type')).toBe('application/json');
    expect(init.credentials).toBe('include');

    // Mapping: snake_case → camelCase; topology_version is gone from both the wire and the type.
    expect(out.keystoneFullRestage).toBe(false);
    expect(out.nodes).toEqual([
      { nodeId: 'n1', name: 'router', changed: true },
      { nodeId: 'n2', name: 'peer', changed: false },
    ]);
    expect(out.skippedUnenrolled).toEqual(['n9']);
    expect(out.telemetryPolicyOmittedNodeIDs).toEqual([]);
    expect('topologyVersion' in out).toBe(false);
  });

  it('defensively coerces null nodes / skipped_unenrolled to empty arrays', async () => {
    stubFetch({
      keystone_full_restage: true,
      nodes: null,
      skipped_unenrolled: null,
      telemetry_policy_omitted_node_ids: null,
    });
    const out = await deployPreview(cfg, '{}');
    expect(out.keystoneFullRestage).toBe(true);
    expect(out.nodes).toEqual([]);
    expect(out.skippedUnenrolled).toEqual([]);
    expect(out.telemetryPolicyOmittedNodeIDs).toEqual([]);
  });

  it('adds the explicit phase-one query and maps the exactly omitted successor-policy nodes', async () => {
    const fetchFn = stubFetch({
      keystone_full_restage: false,
      nodes: [],
      skipped_unenrolled: [],
      telemetry_policy_omitted_node_ids: ['n-device'],
    });

    const out = await deployPreview(cfg, '{}', 'upgrade-agents-first');

    const [url, init] = fetchFn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(
      'http://ctl.test/api/v1/operator/deploy-preview?telemetry_policy_mode=upgrade-agents-first'
    );
    expect(init.body).toBe('{}');
    expect(out.telemetryPolicyOmittedNodeIDs).toEqual(['n-device']);
  });

  it('rejects on a non-2xx (e.g. an older controller 405/404s the POST route)', async () => {
    stubFetch({ error: { code: 'method_not_allowed', message: 'POST' } }, 405);
    await expect(deployPreview(cfg, '{}')).rejects.toBeTruthy();
  });
});

describe('stage telemetry policy mode', () => {
  it('preserves the legacy empty request body and maps absent omitted-node IDs', async () => {
    const fetchFn = stubFetch({
      staged: ['n1'],
      unchanged: null,
      skipped_unenrolled: null,
      generation: 4,
    });

    const out = await stage(cfg);

    const [url, init] = fetchFn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('http://ctl.test/api/v1/operator/stage');
    expect(init.body).toBe('');
    expect(out.telemetryPolicyOmittedNodeIDs).toEqual([]);
  });

  it('combines force selection with the explicit upgrade-agents-first mode', async () => {
    const fetchFn = stubFetch({
      staged: ['n1'],
      unchanged: [],
      skipped_unenrolled: [],
      telemetry_policy_omitted_node_ids: ['n-device'],
      generation: 5,
    });

    const out = await stage(cfg, { forceNodes: ['n1'] }, 'upgrade-agents-first');

    const [, init] = fetchFn.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      force_nodes: ['n1'],
      telemetry_policy_mode: 'upgrade-agents-first',
    });
    expect(out.telemetryPolicyOmittedNodeIDs).toEqual(['n-device']);
  });
});

describe('agent capability mapping', () => {
  it('preserves an exact canonical bounded set, including a confirmed empty set', () => {
    expect(mapAgentCapabilities({ capabilities: [] })).toEqual([]);
    expect(
      mapAgentCapabilities({ capabilities: ['device-telemetry-v1', 'telemetry-policy-v2'] })
    ).toEqual(['device-telemetry-v1', 'telemetry-policy-v2']);
  });

  it('rejects malformed, unsorted, duplicate, or oversized readiness evidence', () => {
    expect(mapAgentCapabilities(undefined)).toBeUndefined();
    expect(mapAgentCapabilities({ capabilities: [], extra: true })).toBeUndefined();
    expect(mapAgentCapabilities({ capabilities: ['telemetry-policy-v2', 'device-telemetry-v1'] })).toBeUndefined();
    expect(mapAgentCapabilities({ capabilities: ['telemetry-policy-v2', 'telemetry-policy-v2'] })).toBeUndefined();
    expect(mapAgentCapabilities({ capabilities: ['Telemetry-Policy-V2'] })).toBeUndefined();
    const oversized = Array.from({ length: 17 }, (_, i) => `cap-${String(i).padStart(2, '0')}`);
    expect(mapAgentCapabilities({ capabilities: oversized })).toBeUndefined();
  });
});
