import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { Node } from '../../types/topology';
import { telemetryProbeWithType } from '../../lib/probeResults';
import { TelemetryProbeEditor } from './TelemetryProbeEditor';

const node: Node = {
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
  telemetry_probes: [
    { id: 'dns-ping', name: 'Primary resolver', type: 'icmp', host: 'resolver.example' },
  ],
};

describe('TelemetryProbeEditor', () => {
  it('shows the outbound/DNS warning and uses one shared IP-or-hostname field', () => {
    const html = renderToStaticMarkup(
      createElement(TelemetryProbeEditor, {
        node,
        keystonePinned: false,
        language: 'en',
        updateNode: () => undefined,
      }),
    );

    expect(html).toContain('outbound traffic');
    expect(html).toContain('DNS names are resolved by the node for every attempt');
    expect(html).toContain('Enroll an operator keystone before deployment');
    expect(html.match(/type="text"/g)).toHaveLength(2);
    expect(html).toContain('value="Primary resolver"');
    expect(html).toContain('dns-ping');
    expect(html).toContain('value="resolver.example"');
  });

  it('marks a newly added blank destination as an accessible incomplete draft', () => {
    const html = renderToStaticMarkup(
      createElement(TelemetryProbeEditor, {
        node: {
          ...node,
          telemetry_probes: [{ id: 'unfinished', type: 'icmp', host: '' }],
        },
        keystonePinned: true,
        language: 'en',
        updateNode: () => undefined,
      }),
    );

    expect(html).toContain('required=""');
    expect(html).toContain('aria-invalid="true"');
    expect(html).toContain('role="alert"');
    expect(html).toContain('Destination is required');
    expect(html).toContain('telemetry-probe-0-destination-error telemetry-probe-0-destination-hint');
  });

  it('marks an invalid display name with accessible feedback', () => {
    const html = renderToStaticMarkup(
      createElement(TelemetryProbeEditor, {
        node: {
          ...node,
          telemetry_probes: [{ id: 'dns-ping', name: 'Primary\u00a0resolver', type: 'icmp', host: 'resolver.example' }],
        },
        keystonePinned: true,
        language: 'en',
        updateNode: () => undefined,
      }),
    );

    expect(html).toContain('aria-invalid="true"');
    expect(html).toContain('aria-describedby="telemetry-probe-dns-ping-name-error"');
    expect(html).toContain('id="telemetry-probe-dns-ping-name-error"');
    expect(html).toContain('role="alert"');
    expect(html).toContain('Use at most 128 printable characters without leading or trailing spaces');
  });

  it('renders one accessible fixed-GET URL and exact expected-status editor', () => {
    const html = renderToStaticMarkup(
      createElement(TelemetryProbeEditor, {
        node: {
          ...node,
          telemetry_probes: [{
            id: 'health',
            name: 'Customer API',
            type: 'url',
            url: 'https://service.example/health',
            expected_status: 204,
          }],
        },
        keystonePinned: true,
        language: 'en',
        updateNode: () => undefined,
      }),
    );

    expect(html).toContain('URL (fixed HTTP GET)');
    expect(html).toContain('type="url"');
    expect(html).toContain('value="https://service.example/health"');
    expect(html).toContain('Expected HTTP status');
    expect(html).toContain('value="204"');
    expect(html).toContain('does not follow redirects');
  });

  it('marks an out-of-range expected status as an accessible invalid policy field', () => {
    const html = renderToStaticMarkup(
      createElement(TelemetryProbeEditor, {
        node: {
          ...node,
          telemetry_probes: [{
            id: 'health',
            type: 'url',
            url: 'https://service.example/health',
            expected_status: 600,
          }],
        },
        keystonePinned: true,
        language: 'en',
        updateNode: () => undefined,
      }),
    );

    expect(html).toContain('value="600"');
    expect(html).toContain('aria-invalid="true"');
    expect(html).toContain('aria-describedby="telemetry-probe-0-expected-status-error"');
    expect(html).toContain('id="telemetry-probe-0-expected-status-error"');
    expect(html).toContain('Expected status must be a whole number from 100 through 599');
  });

  it('constructs a new discriminated destination when the check type changes', () => {
    const tcp = {
      id: 'service',
      name: 'Service',
      type: 'tcp' as const,
      host: 'service.example',
      port: 8443,
      interval_seconds: 30,
    };
    expect(telemetryProbeWithType(tcp, 'url')).toEqual({
      id: 'service',
      name: 'Service',
      type: 'url',
      url: '',
      expected_status: 200,
      interval_seconds: 30,
    });
    expect(telemetryProbeWithType(
      { id: 'service', type: 'url', url: 'https://service.example/', expected_status: 204 },
      'icmp',
    )).toEqual({ id: 'service', type: 'icmp', host: '' });
  });
});
