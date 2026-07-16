// @vitest-environment node

import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { TelemetryProbeResults } from './TelemetryProbeResults';

describe('TelemetryProbeResults display names', () => {
  it('shows a configured display name while retaining immutable ids and exact targets', () => {
    const html = renderToStaticMarkup(createElement(TelemetryProbeResults, {
      configured: [{
        id: 'dns',
        name: 'Primary resolver',
        type: 'icmp' as const,
        host: 'resolver.example',
      }],
      results: [
        {
          id: 'dns',
          type: 'icmp' as const,
          host: 'resolver.example',
          status: 'success' as const,
          latencyMS: 8.5,
        },
        {
          id: 'retired',
          type: 'tcp' as const,
          host: 'old.example',
          port: 443,
          status: 'failure' as const,
        },
      ],
      language: 'en' as const,
    }));

    expect(html).toContain('Primary resolver');
    expect(html).toContain('resolver.example');
    expect(html).toContain('dns');
    expect(html).toContain('Success');
    expect(html).toContain('old.example:443');
    expect(html).toContain('retired');
    expect(html).toContain('(previous deployed policy)');
  });
});
