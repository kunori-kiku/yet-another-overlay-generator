import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt, STRINGS } from '../../i18n';

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
        {txt(language, ...STRINGS.fleetBack)}
      </Link>

      <h1 className="text-xl font-semibold text-gray-100">
        {txt(language, ...STRINGS.fleetNodeDetailTitle)}
      </h1>

      {!node ? (
        <p className="text-sm text-gray-400">{txt(language, ...STRINGS.fleetNodeNotFound)}</p>
      ) : (
        <section className="max-w-2xl space-y-3 rounded-lg border border-gray-700 bg-gray-800 p-4">
          <h2 className="break-all font-mono text-lg font-semibold text-blue-400">{node.nodeId}</h2>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <Field label={txt(language, '状态', 'Status')}>{node.status}</Field>
            <Field label={txt(language, '代号 (已应用/期望)', 'Gen (applied/desired)')}>
              {node.appliedGeneration} / {node.desiredGeneration}
            </Field>
            <Field label={txt(language, '健康', 'Health')}>{node.lastHealth || '—'}</Field>
            <Field label={txt(language, '最近一次心跳', 'Last Seen')}>{fmtTime(node.lastSeen)}</Field>
            <Field label={txt(language, '注册时间', 'Enrolled At')}>{fmtTime(node.enrolledAt)}</Field>
            <Field label={txt(language, '已注册公钥', 'WG public key')}>
              {node.hasWGPublicKey ? '✓' : '—'}
            </Field>
            <Field label={txt(language, '最近校验和', 'Last checksum')}>{node.lastChecksum || '—'}</Field>
            <Field label={txt(language, '轮换密钥中', 'Rekeying')}>
              {node.rekeyRequested ? txt(language, '是', 'yes') : txt(language, '否', 'no')}
            </Field>
          </dl>
        </section>
      )}
    </div>
  );
}
