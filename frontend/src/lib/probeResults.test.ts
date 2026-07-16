import { describe, expect, it } from 'vitest';
import {
  formatProbeTarget,
  mapProbeResults,
  probeResultMatchesPolicy,
  sameTelemetryPolicy,
  summarizeProbeResults,
} from './probeResults';

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
    const probe = { id: 'tls', type: 'tcp' as const, host: 'service.example', port: 443 };
    expect(probeResultMatchesPolicy(probe, { ...probe, status: 'success' })).toBe(true);
    expect(probeResultMatchesPolicy(probe, { ...probe, host: 'other.example', status: 'success' })).toBe(false);
    expect(sameTelemetryPolicy([probe], [{ ...probe }])).toBe(true);
    expect(sameTelemetryPolicy([probe], [{ ...probe, timeout_milliseconds: 1000 }])).toBe(false);
  });
});

describe('formatProbeTarget', () => {
  it('brackets IPv6 literals when a TCP port is shown', () => {
    expect(formatProbeTarget('2001:db8::1', 443)).toBe('[2001:db8::1]:443');
    expect(formatProbeTarget('192.0.2.10', 443)).toBe('192.0.2.10:443');
    expect(formatProbeTarget('resolver.example', undefined)).toBe('resolver.example');
  });
});
