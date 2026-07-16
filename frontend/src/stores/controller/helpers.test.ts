import { describe, expect, it } from 'vitest';
import type { TelemetryProbe, Topology } from '../../types/topology';
import { canonicalDesign } from './helpers';

function topologyWithProbe(probe: TelemetryProbe): Topology {
  return {
    project: { id: 'project', name: 'Project' },
    domains: [],
    nodes: [
      {
        id: 'node-1',
        name: 'Node One',
        role: 'peer',
        domain_id: 'domain-1',
        capabilities: {
          can_accept_inbound: true,
          can_forward: false,
          can_relay: false,
          has_public_ip: true,
        },
        telemetry_probes: [probe],
      },
    ],
    edges: [],
  };
}

describe('canonicalDesign telemetry probes', () => {
  it('matches the controller canonical form when nested omitempty defaults are explicit zeros', () => {
    const explicitZeros = topologyWithProbe({
      id: 'gateway-ping',
      type: 'icmp',
      host: '192.0.2.1',
      port: 0,
      interval_seconds: 0,
      timeout_milliseconds: 0,
    });
    const omittedDefaults = topologyWithProbe({
      id: 'gateway-ping',
      type: 'icmp',
      host: '192.0.2.1',
    });

    expect(canonicalDesign(explicitZeros)).toBe(canonicalDesign(omittedDefaults));
  });

  it('preserves non-default nested probe values as meaningful design changes', () => {
    const defaults = topologyWithProbe({
      id: 'service',
      type: 'tcp',
      host: 'service.example',
      port: 443,
    });
    const scheduled = topologyWithProbe({
      id: 'service',
      type: 'tcp',
      host: 'service.example',
      port: 443,
      interval_seconds: 30,
      timeout_milliseconds: 1000,
    });

    expect(canonicalDesign(scheduled)).not.toBe(canonicalDesign(defaults));
  });

  it('omits an empty display name but preserves a configured name as a design change', () => {
    const omitted = topologyWithProbe({
      id: 'service',
      type: 'tcp',
      host: 'service.example',
      port: 443,
    });
    const empty = topologyWithProbe({
      id: 'service',
      name: '',
      type: 'tcp',
      host: 'service.example',
      port: 443,
    });
    const named = topologyWithProbe({
      id: 'service',
      name: 'Customer API',
      type: 'tcp',
      host: 'service.example',
      port: 443,
    });

    expect(canonicalDesign(empty)).toBe(canonicalDesign(omitted));
    expect(canonicalDesign(named)).not.toBe(canonicalDesign(omitted));
  });

  it('mirrors URL probe omitempty fields without erasing an explicit success contract', () => {
    const omittedDefault = topologyWithProbe({
      id: 'health',
      type: 'url',
      url: 'https://service.example/health',
    });
    const explicitZero = topologyWithProbe({
      id: 'health',
      type: 'url',
      url: 'https://service.example/health',
      expected_status: 0,
    });
    const explicitSuccess = topologyWithProbe({
      id: 'health',
      type: 'url',
      url: 'https://service.example/health',
      expected_status: 204,
    });

    expect(canonicalDesign(explicitZero)).toBe(canonicalDesign(omittedDefault));
    expect(canonicalDesign(explicitSuccess)).not.toBe(canonicalDesign(omittedDefault));
  });
});
