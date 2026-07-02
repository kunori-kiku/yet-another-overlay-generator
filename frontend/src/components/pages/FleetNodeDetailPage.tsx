import type { ReactNode } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { useFleetLiveRefresh } from '../../hooks/useFleetLiveRefresh';
import { t } from '../../i18n';
import { UpdateStatusChip } from '../deploy/UpdateStatusChip';
import { NodeConditions } from '../deploy/NodeConditions';
import { WireGuardPeersPanel } from '../deploy/WireGuardPeersPanel';
import { ResourcePanel } from '../deploy/ResourcePanel';
import { ControllerErrorBanner } from '../deploy/ControllerErrorBanner';

// last_seen / enrolled_at are RFC3339 strings; the zero value ("0001-01-01T00:00:00Z") is
// displayed as "—".
function fmtTime(iso: string): string {
  if (!iso || iso.startsWith('0001-01-01')) return '—';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-[var(--content-muted)]">{label}</dt>
      <dd className="text-[var(--content)] font-mono break-all">{children}</dd>
    </>
  );
}

// /fleet/nodes/:id — detail for a single controller-registered node. Mirrors the
// selection-driven detail pattern; the registry's node-id cell links here.
export function FleetNodeDetailPage() {
  const { id } = useParams();
  const language = useTopologyStore((s) => s.language);
  const node = useControllerStore((s) => s.nodes.find((n) => n.nodeId === id));
  const settings = useControllerStore((s) => s.settings);
  const refresh = useControllerStore((s) => s.refresh);
  const loading = useControllerStore((s) => s.loading);
  const lastSyncedAt = useControllerStore((s) => s.lastSyncedAt);
  // Refresh-on-mount + the opt-in Live poll (beta.16): this deep-linked route previously rendered a
  // FROZEN cache snapshot (no refresh-on-mount), so an operator watching a node saw stale status that
  // never advanced. Shared with /fleet via the hook so the two stay behaviorally identical.
  const { live, setLive } = useFleetLiveRefresh();

  return (
    <div className="h-full overflow-y-auto bg-[var(--surface)] text-[var(--content)] p-3 sm:p-6 space-y-4">
      {/* Surface a failed refresh (an expired session, or the controller's 502s) — this page actively
          calls refresh() (mount, Live poll, manual button), so without the banner a failed fetch would
          silently stop the lastSynced stamp and look identical to a quiet node (mirrors FleetPage). */}
      <ControllerErrorBanner />
      <Link to="/fleet" className="text-sm text-[var(--info)] hover:underline">
        {t(language, 'fleetBack')}
      </Link>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-xl font-semibold text-[var(--content)]">
          {t(language, 'fleetNodeDetailTitle')}
        </h1>
        {/* Server-truth controls: a manual Refresh, the opt-in Live poll, and a "last synced" stamp so
            the operator knows this is a snapshot (and how fresh) rather than a live mirror. */}
        <div className="flex items-center gap-3 text-xs text-[var(--content-muted)]">
          {lastSyncedAt && (
            <span>
              {t(language, 'fleetNodeDetailPage.lastSynced')}: {new Date(lastSyncedAt).toLocaleTimeString()}
            </span>
          )}
          <label className="flex items-center gap-1">
            <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />
            {t(language, 'updateStatus.live')}
          </label>
          <button
            onClick={() => void refresh()}
            disabled={loading}
            data-testid="node-detail-refresh"
            className="px-2 py-1 rounded border border-[var(--hairline)] text-[var(--info)] hover:bg-[var(--control)] disabled:text-[var(--content-muted)]"
          >
            {t(language, 'fleetNodeDetailPage.refresh')}
          </button>
        </div>
      </div>

      {!node ? (
        <p className="text-sm text-[var(--content-muted)]">{t(language, 'fleetNodeNotFound')}</p>
      ) : (
        <section className="max-w-2xl space-y-3 rounded-lg border border-[var(--hairline)] bg-[var(--surface-elevated)] p-4">
          <h2 className="break-all font-mono text-lg font-semibold text-[var(--info)]">{node.nodeId}</h2>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <Field label={t(language, 'fleetNodeDetailPage.status')}>{node.status}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.genAppliedDesired')}>
              {node.appliedGeneration} / {node.desiredGeneration}
            </Field>
            <Field label={t(language, 'fleetNodeDetailPage.health')}>{node.lastHealth || '—'}</Field>
            <Field label={t(language, 'fleetNodeDetailPage.conditions')}>
              {(node.conditions?.length ?? 0) > 0 ? <NodeConditions conditions={node.conditions} language={language} /> : '—'}
            </Field>
            <Field label={t(language, 'fleetNodeDetailPage.agentVersion')}>{node.agentVersion || '—'}</Field>
            <Field label={t(language, 'updateStatus.label')}>
              <UpdateStatusChip node={node} settings={settings} language={language} />
            </Field>
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
          {/* Unconditional + nullish-coerced: a node persisted by a pre-beta.12 panel (in
              localStorage) has no wireguardPeers key, and the refresh-on-mount above completes
              asynchronously (the first paint is the cache), so guarding on node.wireguardPeers.length
              would crash on a reload after upgrade. The panel itself renders nothing for an empty list. */}
          <WireGuardPeersPanel peers={node.wireguardPeers ?? []} language={language} />
          <ResourcePanel resource={node.resource} language={language} />
        </section>
      )}
    </div>
  );
}
