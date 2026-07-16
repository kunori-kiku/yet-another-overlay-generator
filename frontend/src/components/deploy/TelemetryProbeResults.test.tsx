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

  it('shows expected and actual codes for a completed mismatch while retaining latency', () => {
    const html = renderToStaticMarkup(createElement(TelemetryProbeResults, {
      configured: [{
        id: 'health',
        name: 'Customer API',
        type: 'url' as const,
        url: 'https://service.example/health',
        expected_status: 204,
      }],
      results: [{
        id: 'health',
        type: 'url' as const,
        url: 'https://service.example/health',
        expectedStatus: 204,
        actualStatus: 500,
        status: 'failure' as const,
        latencyMS: 31.5,
        checkedAt: '2026-07-17T10:00:00Z',
        failureReason: 'unexpected_status' as const,
      }],
      language: 'en' as const,
    }));

    expect(html).toContain('Customer API');
    expect(html).toContain('https://service.example/health');
    expect(html).toContain('Expected HTTP status');
    expect(html).toContain('Latest HTTP status');
    expect(html).toContain('204');
    expect(html).toContain('500');
    expect(html).toContain('31.5 ms');
    expect(html).toContain('Unexpected HTTP status');
    expect(html).not.toContain('status-code-chart');
  });

  it('distinguishes a URL transport failure by leaving the actual code unavailable', () => {
    const html = renderToStaticMarkup(createElement(TelemetryProbeResults, {
      configured: [{ id: 'health', type: 'url' as const, url: 'https://service.example/' }],
      results: [{
        id: 'health',
        type: 'url' as const,
        url: 'https://service.example/',
        expectedStatus: 200,
        status: 'failure' as const,
        checkedAt: '2026-07-17T10:00:00Z',
        failureReason: 'timeout' as const,
      }],
      language: 'en' as const,
    }));

    expect(html).toContain('Latest HTTP status');
    expect(html).toContain('Timed out');
    expect(html).not.toContain('Unexpected HTTP status');
  });
});
