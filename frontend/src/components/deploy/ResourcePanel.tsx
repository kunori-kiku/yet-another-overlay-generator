import type { NodeResource } from '../../types/controller';
import { t, type UILanguage } from '../../i18n';
import { memUsedPercent, memSeverity, cpuSeverity, formatLoad, formatKB, formatPct } from '../../lib/resource';

// ResourcePanel (plan-10): a compact host load + memory readout — the detail behind the node's live
// telemetry metric (node.resource). Data is the agent's /telemetry metrics["resource"]; absent ⇒
// nothing rendered (a pre-plan-10 agent, or a host whose /proc read failed). Observability only — no
// endpoint/IP/key material.

const SEVERITY_TEXT: Record<'ok' | 'warn' | 'danger', string> = {
  ok: 'text-[var(--content)]',
  warn: 'text-[var(--warning)]',
  danger: 'text-[var(--danger)]',
};

export function ResourcePanel({
  resource,
  language,
}: {
  resource: NodeResource | undefined;
  language: UILanguage;
}) {
  if (!resource) return null;
  const pct = memUsedPercent(resource);
  // pct === null means memory is UNKNOWN (an old kernel without MemAvailable, or a failed /proc/meminfo
  // read leaves memTotalKB=0) — show an em dash, not formatKB(0)='0' which would misread as "0 bytes".
  const memText =
    pct === null
      ? '—'
      : t(language, 'resourcePanel.memUsed', {
          used: formatKB(resource.memTotalKB - resource.memAvailableKB),
          total: formatKB(resource.memTotalKB),
          pct: String(Math.round(pct)),
        });

  return (
    <div className="rounded border border-[var(--hairline)] bg-[var(--surface)] px-3 py-2 text-sm">
      <div className="text-[var(--content)]">{t(language, 'resourcePanel.heading')}</div>
      <dl className="mt-1 flex flex-wrap gap-x-6 gap-y-1 text-xs">
        <div className="flex gap-1.5">
          <dt className="text-[var(--content-muted)]">{t(language, 'resourcePanel.cpu')}</dt>
          <dd
            className={`font-mono ${SEVERITY_TEXT[cpuSeverity(resource.cpuPct)]}`}
            data-testid="resource-cpu"
          >
            {resource.cpuPct === undefined ? '—' : formatPct(resource.cpuPct)}
          </dd>
        </div>
        <div className="flex gap-1.5">
          <dt className="text-[var(--content-muted)]">{t(language, 'resourcePanel.load')}</dt>
          <dd className="font-mono text-[var(--content)]">
            {formatLoad(resource.load1)} / {formatLoad(resource.load5)} / {formatLoad(resource.load15)}
          </dd>
        </div>
        <div className="flex gap-1.5">
          <dt className="text-[var(--content-muted)]">{t(language, 'resourcePanel.memory')}</dt>
          <dd className={`font-mono ${SEVERITY_TEXT[memSeverity(pct)]}`}>{memText}</dd>
        </div>
      </dl>
    </div>
  );
}
