// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Topology } from '../types/topology';
import { useControllerStore } from './controllerStore';
import { useTopologyStore } from './topologyStore';

function response(status: number, body: unknown): Response {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: new Headers(),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

const topology: Topology = {
  project: { id: 'project', name: 'Project' },
  domains: [{
    id: 'domain',
    name: 'Domain',
    cidr: '10.42.0.0/24',
    allocation_mode: 'auto',
    routing_mode: 'babel',
  }],
  nodes: [],
  edges: [],
};

function seedStore(): void {
  useTopologyStore.setState({
    project: topology.project,
    domains: topology.domains,
    nodes: topology.nodes,
    edges: topology.edges,
    language: 'en',
  });
  useControllerStore.setState({
    mode: 'controller',
    baseURL: 'https://controller.example',
    pathPrefix: '',
    operatorToken: '',
    sessionToken: 'session',
    csrfToken: 'csrf',
    authGeneration: 0,
    loading: false,
    deployPreview: null,
    deployPreviewing: false,
    deployPreviewError: null,
    error: null,
  });
}

beforeEach(() => {
  seedStore();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  useControllerStore.setState({
    mode: 'local',
    deployPreview: null,
    deployPreviewing: false,
    deployPreviewError: null,
    error: null,
  });
});

describe('deploy preview error routing', () => {
  it.each([
    { status: 404, compatibility: true },
    { status: 405, compatibility: true },
    { status: 422, compatibility: false },
    { status: 500, compatibility: false },
  ])('routes HTTP $status to the correct error surface', async ({ status, compatibility }) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(status, {
      error: {
        code: status === 422 ? 'topology_validation_failed' : 'internal',
        message: 'request failed',
        params: status === 422 ? {
          field: 'nodes[0].telemetry_probes',
          validation_code: 'node_telemetry_probes_invalid',
          validation_message: 'invalid telemetry probe',
          validation_param_detail: 'invalid host ""',
        } : {},
      },
    })));

    await useControllerStore.getState().openDeployPreview();

    const state = useControllerStore.getState();
    expect(state.deployPreviewing).toBe(false);
    expect(state.deployPreviewError !== null).toBe(compatibility);
    expect(state.error !== null).toBe(!compatibility);
  });

  it('clears a stale compatibility error on both a successful retry and a blocking retry', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(response(200, {
        keystone_full_restage: false,
        nodes: [],
        skipped_unenrolled: [],
      }))
      .mockResolvedValueOnce(response(422, {
        error: {
          code: 'topology_validation_failed',
          message: 'request failed',
          params: {
            field: 'nodes[0].telemetry_probes',
            validation_code: 'node_telemetry_probes_invalid',
            validation_message: 'invalid telemetry probe',
            validation_param_detail: 'invalid host ""',
          },
        },
      }));
    vi.stubGlobal('fetch', fetch);

    useControllerStore.setState({ deployPreviewError: 'old compatibility error' });
    await useControllerStore.getState().openDeployPreview();
    expect(useControllerStore.getState().deployPreviewError).toBeNull();
    expect(useControllerStore.getState().deployPreview).not.toBeNull();

    useControllerStore.setState({
      deployPreview: null,
      deployPreviewError: 'old compatibility error',
      error: null,
    });
    await useControllerStore.getState().openDeployPreview();
    expect(useControllerStore.getState().deployPreviewError).toBeNull();
    expect(useControllerStore.getState().error).not.toBeNull();
  });
});
