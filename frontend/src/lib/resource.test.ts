import { describe, it, expect } from 'vitest';
import { memUsedPercent, memSeverity, cpuSeverity, formatLoad, formatKB, formatPct } from './resource';
import type { NodeResource } from '../types/controller';

const res = (over: Partial<NodeResource> = {}): NodeResource => ({
  load1: 0.5,
  load5: 0.4,
  load15: 0.3,
  memTotalKB: 2048,
  memAvailableKB: 1024,
  ...over,
});

describe('memUsedPercent', () => {
  it('computes used percent', () => {
    expect(memUsedPercent(res({ memTotalKB: 1000, memAvailableKB: 250 }))).toBe(75);
  });
  it('null when total is unknown/zero (old kernel / failed mem read)', () => {
    expect(memUsedPercent(res({ memTotalKB: 0 }))).toBeNull();
  });
  it('clamps to 0..100 (avail > total should not go negative)', () => {
    expect(memUsedPercent(res({ memTotalKB: 100, memAvailableKB: 200 }))).toBe(0);
  });
});

describe('memSeverity', () => {
  it('buckets memory pressure; null is ok (no false alarm)', () => {
    expect(memSeverity(95)).toBe('danger');
    expect(memSeverity(80)).toBe('warn');
    expect(memSeverity(50)).toBe('ok');
    expect(memSeverity(null)).toBe('ok');
  });
});

describe('cpuSeverity', () => {
  it('buckets CPU utilization; undefined is ok (no false alarm for an old agent / first beat)', () => {
    expect(cpuSeverity(95)).toBe('danger');
    expect(cpuSeverity(80)).toBe('warn');
    expect(cpuSeverity(50)).toBe('ok');
    expect(cpuSeverity(undefined)).toBe('ok');
  });
});

describe('formatPct', () => {
  it('renders to one decimal with a % suffix', () => {
    expect(formatPct(40)).toBe('40.0%');
    expect(formatPct(3.14159)).toBe('3.1%');
    expect(formatPct(100)).toBe('100.0%');
  });
});

describe('formatLoad / formatKB', () => {
  it('load renders to two decimals', () => {
    expect(formatLoad(1)).toBe('1.00');
    expect(formatLoad(0.523)).toBe('0.52');
  });
  it('kB renders as MiB/GiB (non-positive → 0)', () => {
    expect(formatKB(0)).toBe('0');
    expect(formatKB(-5)).toBe('0');
    expect(formatKB(2048)).toBe('2 MiB');
    expect(formatKB(2 * 1024 * 1024)).toBe('2.0 GiB');
  });
});
