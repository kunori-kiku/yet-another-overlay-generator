import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { Node } from '../../types/topology';
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
    { id: 'dns-ping', type: 'icmp', host: 'resolver.example' },
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
    expect(html.match(/type="text"/g)).toHaveLength(1);
    expect(html).toContain('value="resolver.example"');
  });
});
