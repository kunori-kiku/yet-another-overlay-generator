import { describe, expect, it } from 'vitest';
import {
  formatProbeTarget,
  isValidProbeURL,
  mapProbeResults,
  probeDisplayName,
  probeExpectedStatusInvalid,
  probeResultMatchesPolicy,
  sameTelemetryPolicy,
  summarizeProbeResults,
} from './probeResults';

describe('URL policy validation', () => {
  it('matches the signed Go policy for raw authority and expected-status boundaries', () => {
    expect(isValidProbeURL('http://127.0.0.1:8080/health')).toBe(true);
    expect(isValidProbeURL('https://[::1]:8443/status?check=yes')).toBe(true);
    for (const invalid of [
      'HTTPS://example.test/',
      'https://@example.test/',
      'https://user@example.test/',
      'https://example.test:/',
      'https://example.test:0/',
      'https://example.test:65536/',
      'https://example.test/\u0085',
    ]) {
      expect(isValidProbeURL(invalid), invalid).toBe(false);
    }

    expect(probeExpectedStatusInvalid({ id: 'default', type: 'url', url: 'https://example.test/' })).toBe(false);
    expect(probeExpectedStatusInvalid({ id: 'low', type: 'url', url: 'https://example.test/', expected_status: 99 })).toBe(true);
    expect(probeExpectedStatusInvalid({ id: 'high', type: 'url', url: 'https://example.test/', expected_status: 600 })).toBe(true);
    expect(probeExpectedStatusInvalid({ id: 'icmp', type: 'icmp', host: 'example.test' })).toBe(false);
  });
});

describe('mapProbeResults', () => {
  it('maps the closed ICMP/TCP result contract without exposing resolved addresses', () => {
    expect(
      mapProbeResults([
        {
          id: 'dns',
          type: 'icmp',
          host: 'resolver.example',
          status: 'failure',
          checked_at: '2026-07-16T01:02:03Z',
          failure_reason: 'dns_failed',
        },
        {
          id: 'tls',
          type: 'tcp',
          host: '192.0.2.4',
          port: 443,
          status: 'success',
          checked_at: '2026-07-16T01:02:04Z',
          latency_ms: 12.25,
        },
      ]),
    ).toEqual([
      {
        id: 'dns',
        type: 'icmp',
        host: 'resolver.example',
        status: 'failure',
        checkedAt: '2026-07-16T01:02:03Z',
        failureReason: 'dns_failed',
      },
      {
        id: 'tls',
        type: 'tcp',
        host: '192.0.2.4',
        port: 443,
        status: 'success',
        checkedAt: '2026-07-16T01:02:04Z',
        latencyMS: 12.25,
      },
    ]);
  });

  it('drops malformed rows, invalid type/port combinations, and duplicate IDs', () => {
    expect(
      mapProbeResults([
        { id: 'icmp-port', type: 'icmp', host: 'example.test', port: 7, status: 'success' },
        { id: 'tcp-no-port', type: 'tcp', host: 'example.test', status: 'success' },
        { id: 'bad-port', type: 'tcp', host: 'example.test', port: 70000, status: 'success' },
        { id: 'bad-status', type: 'icmp', host: 'example.test', status: 'maybe' },
        { id: 'bad id', type: 'icmp', host: 'example.test', status: 'success' },
        { id: 'ok', type: 'icmp', host: 'example.test', status: 'pending' },
        { id: 'ok', type: 'icmp', host: 'different.test', status: 'success' },
      ]),
    ).toEqual([{ id: 'ok', type: 'icmp', host: 'example.test', status: 'pending' }]);
  });

  it('returns an empty list for non-array telemetry', () => {
    expect(mapProbeResults(null)).toEqual([]);
    expect(mapProbeResults({})).toEqual([]);
  });

  it('maps strict URL success and mismatch outcomes with categorical latest codes', () => {
    expect(mapProbeResults([{
      id: 'ok',
      type: 'url',
      url: 'https://service.example/health',
      expected_status: 204,
      actual_status: 204,
      status: 'success',
      latency_ms: 12.5,
      checked_at: '2026-07-17T10:00:00Z',
    }, {
      id: 'mismatch',
      type: 'url',
      url: 'https://service.example/health',
      expected_status: 200,
      actual_status: 500,
      status: 'failure',
      latency_ms: 19.25,
      checked_at: '2026-07-17T10:00:01Z',
      failure_reason: 'unexpected_status',
    }])).toEqual([{
      id: 'ok',
      type: 'url',
      url: 'https://service.example/health',
      expectedStatus: 204,
      actualStatus: 204,
      status: 'success',
      latencyMS: 12.5,
      checkedAt: '2026-07-17T10:00:00Z',
    }, {
      id: 'mismatch',
      type: 'url',
      url: 'https://service.example/health',
      expectedStatus: 200,
      actualStatus: 500,
      status: 'failure',
      latencyMS: 19.25,
      checkedAt: '2026-07-17T10:00:01Z',
      failureReason: 'unexpected_status',
    }]);
  });

  it('rejects mixed URL fields and invalid status/result combinations', () => {
    expect(mapProbeResults([
      { id: 'host', type: 'url', url: 'https://example.test', host: 'example.test', expected_status: 200, status: 'pending' },
      { id: 'range', type: 'url', url: 'https://example.test', expected_status: 99, status: 'pending' },
      { id: 'equal-mismatch', type: 'url', url: 'https://example.test', expected_status: 500, actual_status: 500, status: 'failure', latency_ms: 1, failure_reason: 'unexpected_status' },
      { id: 'transport-code', type: 'url', url: 'https://example.test', expected_status: 200, actual_status: 500, status: 'failure', failure_reason: 'timeout' },
    ])).toEqual([]);
  });
});

describe('summarizeProbeResults', () => {
  it('counts a configured probe without a result as waiting', () => {
    expect(
      summarizeProbeResults(
        [
          { id: 'icmp', type: 'icmp', host: 'one.test' },
          { id: 'tcp', type: 'tcp', host: 'example.test', port: 443 },
        ],
        [{ id: 'tcp', type: 'tcp', host: 'example.test', port: 443, status: 'success' }],
      ),
    ).toEqual({
      state: 'pending',
      total: 2,
      success: 1,
      pending: 1,
      failure: 0,
    });
  });

  it('prioritizes failure and includes a just-retired reported result', () => {
    expect(
      summarizeProbeResults(
        [{ id: 'current', type: 'icmp', host: 'one.test' }],
        [
          { id: 'current', type: 'icmp', host: 'one.test', status: 'success' },
          {
            id: 'retired',
            type: 'tcp',
            host: 'two.test',
            port: 22,
            status: 'failure',
            failureReason: 'connection_refused',
          },
        ],
      ),
    ).toEqual({
      state: 'failure',
      total: 2,
      success: 1,
      pending: 0,
      failure: 1,
    });
  });

  it('keeps a previous deployed failure visible without attributing it to an edited target', () => {
    expect(
      summarizeProbeResults(
        [{ id: 'service', type: 'tcp', host: 'new.example', port: 443 }],
        [{ id: 'service', type: 'tcp', host: 'old.example', port: 443, status: 'failure' }],
      ),
    ).toEqual({ state: 'failure', total: 2, success: 0, pending: 1, failure: 1 });
  });
});

describe('policy/result identity', () => {
  it('matches executable destination fields and compares complete policy fields', () => {
    const probe = { id: 'tls', name: 'Customer API', type: 'tcp' as const, host: 'service.example', port: 443 };
    expect(probeResultMatchesPolicy(probe, { ...probe, status: 'success' })).toBe(true);
    expect(probeResultMatchesPolicy({ ...probe, name: 'Renamed API' }, { ...probe, status: 'success' })).toBe(true);
    expect(probeResultMatchesPolicy(probe, { ...probe, host: 'other.example', status: 'success' })).toBe(false);
    expect(sameTelemetryPolicy([probe], [{ ...probe }])).toBe(true);
    expect(sameTelemetryPolicy([probe], [{ ...probe, name: 'Renamed API' }])).toBe(false);
    expect(sameTelemetryPolicy([probe], [{ ...probe, timeout_milliseconds: 1000 }])).toBe(false);
  });

  it('matches URL results by exact URL and effective expected status while ignoring display name', () => {
    const probe = { id: 'health', name: 'API', type: 'url' as const, url: 'https://service.example/' };
    const result = {
      id: 'health',
      type: 'url' as const,
      url: 'https://service.example/',
      expectedStatus: 200,
      status: 'pending' as const,
    };
    expect(probeResultMatchesPolicy(probe, result)).toBe(true);
    expect(probeResultMatchesPolicy({ ...probe, name: 'Renamed' }, result)).toBe(true);
    expect(probeResultMatchesPolicy({ ...probe, expected_status: 204 }, result)).toBe(false);
    expect(probeResultMatchesPolicy({ ...probe, url: 'https://other.example/' }, result)).toBe(false);
    expect(sameTelemetryPolicy([probe], [{ ...probe, expected_status: 200 }])).toBe(true);
    expect(sameTelemetryPolicy([probe], [{ ...probe, expected_status: 204 }])).toBe(false);
  });
});

describe('formatProbeTarget', () => {
  it('brackets IPv6 literals when a TCP port is shown', () => {
    expect(formatProbeTarget('2001:db8::1', 443)).toBe('[2001:db8::1]:443');
    expect(formatProbeTarget('192.0.2.10', 443)).toBe('192.0.2.10:443');
    expect(formatProbeTarget('resolver.example', undefined)).toBe('resolver.example');
  });

  it('uses a configured display name and falls back to the immutable ID', () => {
    expect(probeDisplayName({ id: 'dns', name: 'Primary resolver' })).toBe('Primary resolver');
    expect(probeDisplayName({ id: 'dns' })).toBe('dns');
    expect(probeDisplayName({ id: 'dns', name: '' })).toBe('dns');
  });
});
