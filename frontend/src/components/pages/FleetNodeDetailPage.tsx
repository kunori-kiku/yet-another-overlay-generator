import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// last_seen / enrolled_at 是 RFC3339 字符串；零值（"0001-01-01T00:00:00Z"）显示为「—」。
function fmtTime(iso: string): string {
  if (!iso || iso.startsWith('0001-01-01')) return '—';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-gray-400">{label}</dt>
      <dd className="text-gray-200 font-mono break-all">{children}</dd>
    </>
  );
}

// /fleet/nodes/:id — detail for a single controller-registered node. Mirrors the
// selection-driven detail pattern; the registry's node-id cell links here.
export function FleetNodeDetailPage() {
  const { id } = useParams();
  const language = useTopologyStore((s) => s.language);
  const node = useControllerStore((s) => s.nodes.find((n) => n.nodeId === id));

  return (
    <div className="h-full overflow-y-auto bg-gray-900 text-gray-100 p-6 space-y-4">
      <Link to="/fleet" className="text-sm text-blue-400 hover:underline">
        {t(language, 'fleetBack')}
      </Link>

      <h1 className="text-xl font-semibold text-gray-100">
        {t(language, 'fleetNodeDetailTitle')}
      </h1>

      {!node ? (
        <p className="text-sm text-gray-400">{t(language, 'fleetNodeNotFound')}</p>
      ) : (
        <section className="max-w-2xl space-y-3 rounded-lg border border-gray-700 bg-gray-800 p-4">
          <h2 className="break-all font-mono text-lg font-semibold text-blue-400">{node.nodeId}</h2>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <Field label={t(language, 'fleetNodeDetailPage.status')}>{node.status}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.genAppliedDesired')}>
              {node.appliedGeneration} / {node.desiredGeneration}
            </Field>
            <Field label={t(language, 'fleetNodeDetailPage.health')}>{node.lastHealth || '—'}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.agentVersion')}>{node.agentVersion || '—'}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.lastSeen')}>{fmtTime(node.lastSeen)}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.enrolledAt')}>{fmtTime(node.enrolledAt)}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.wgPublicKey')}>
              {node.hasWGPublicKey ? '✓' : '—'}
            </Field>
            <Field label={t(language, 'fleetNodeDetailPage.lastChecksum')}>{node.lastChecksum || '—'}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.rekeying')}>
              {node.rekeyRequested ? t(language, 'fleetNodeDetailPage.yes') : t(language, 'fleetNodeDetailPage.no')}
            </Field>
          </dl>
        </section>
      )}
    </div>
  );
}
