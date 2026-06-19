import type { ReactNode } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';

// /overview — a thin landing dashboard: topology counts (local) + controller
// fleet summary (last deploy / last synced). Read-only; deep links elsewhere.
function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="rounded-2xl border border-[var(--hairline)] bg-[var(--surface-elevated)] p-4">
      <div className="text-2xl font-semibold text-[var(--content)]">{value}</div>
      <div className="mt-1 text-xs text-[var(--content-muted)]">{label}</div>
    </div>
  );
}

export function OverviewPage() {
  const language = useTopologyStore((s) => s.language);
  const domains = useTopologyStore((s) => s.domains);
  const nodes = useTopologyStore((s) => s.nodes);
  const edges = useTopologyStore((s) => s.edges);
  const fleetNodes = useControllerStore((s) => s.nodes);
  const lastDeploy = useControllerStore((s) => s.lastDeploy);
  const lastSyncedAt = useControllerStore((s) => s.lastSyncedAt);

  const heading = 'mb-3 text-xs font-semibold uppercase tracking-wide text-[var(--content-muted)]';

  return (
    <div className="h-full overflow-y-auto p-3 sm:p-6">
      <div className="mx-auto max-w-4xl space-y-8">
        <section>
          <h2 className={heading}>{t(language, 'overviewTopologyHeading')}</h2>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            <Stat label={t(language, 'overviewDomains')} value={domains.length} />
            <Stat label={t(language, 'overviewNodes')} value={nodes.length} />
            <Stat label={t(language, 'overviewEdges')} value={edges.length} />
          </div>
        </section>

        <section>
          <h2 className={heading}>{t(language, 'overviewControllerHeading')}</h2>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            <Stat label={t(language, 'overviewFleetNodes')} value={fleetNodes.length} />
            <Stat
              label={t(language, 'overviewLastDeploy')}
              value={lastDeploy ? '✓' : '—'}
            />
            <Stat
              label={t(language, 'overviewLastSynced')}
              value={
                lastSyncedAt !== null ? (
                  <span className="text-base font-normal">
                    {new Date(lastSyncedAt).toLocaleString()}
                  </span>
                ) : (
                  '—'
                )
              }
            />
          </div>
          {lastSyncedAt === null && (
            <p className="mt-3 text-xs text-[var(--content-muted)]">
              {t(language, 'overviewNotSynced')}
            </p>
          )}
        </section>
      </div>
    </div>
  );
}
