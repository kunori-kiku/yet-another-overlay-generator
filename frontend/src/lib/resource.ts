import type { NodeResource } from '../types/controller';

// Pure host-resource formatting for ResourcePanel (mirrors lib/wgPeers.ts: all math here, node-testable,
// no rendering). Load averages are shown as-is (no core-count is reported, so they are NOT colored —
// coloring load without knowing the CPU count would be misleading); memory pressure IS bucketed.

// memUsedPercent returns used-memory percent (0..100), or null when total is unknown/zero (an old
// kernel without MemAvailable, or a metric that failed to read memory).
export function memUsedPercent(r: NodeResource): number | null {
  if (!r.memTotalKB || r.memTotalKB <= 0) return null;
  const used = r.memTotalKB - r.memAvailableKB;
  return Math.max(0, Math.min(100, (used / r.memTotalKB) * 100));
}

// memSeverity buckets memory pressure for coloring: danger >90%, warn >75%, else ok (null → ok, since
// unknown memory should not raise a false alarm).
export function memSeverity(pct: number | null): 'ok' | 'warn' | 'danger' {
  if (pct === null) return 'ok';
  if (pct > 90) return 'danger';
  if (pct > 75) return 'warn';
  return 'ok';
}

// cpuSeverity buckets CPU utilization for coloring, consistent with memSeverity (danger >90%, warn
// >75%). undefined (a pre-plan-1 agent, or the first beat before a delta exists) → ok: unknown CPU must
// not raise a false alarm.
export function cpuSeverity(pct: number | undefined): 'ok' | 'warn' | 'danger' {
  if (pct === undefined) return 'ok';
  if (pct > 90) return 'danger';
  if (pct > 75) return 'warn';
  return 'ok';
}

// formatPct renders a CPU percent to one decimal (matching the agent's 0.1 resolution).
export function formatPct(pct: number): string {
  return pct.toFixed(1) + '%';
}

// formatLoad renders a load average to two decimals.
export function formatLoad(n: number): string {
  return n.toFixed(2);
}

// formatKB renders a kB count as a human MiB/GiB string (0 for a non-positive count).
export function formatKB(kb: number): string {
  if (kb <= 0) return '0';
  const mib = kb / 1024;
  if (mib >= 1024) return (mib / 1024).toFixed(1) + ' GiB';
  return Math.round(mib) + ' MiB';
}
