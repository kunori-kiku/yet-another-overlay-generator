import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// AuditLog displays the controller audit chain's meaningful operator/security/lifecycle actions + a
// badge for whether the COMPLETE hash chain is intact. Older controllers stored high-frequency bare
// agent "report" entries; retain them in the fetched chain for verification/API compatibility, but
// omit them from the operator table because their useful state already lives in Fleet.
function fmtTime(iso: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function AuditLog() {
  const language = useTopologyStore((s) => s.language);
  const audit = useControllerStore((s) => s.audit);
  const auditVerified = useControllerStore((s) => s.auditVerified);
  const visibleAudit = audit.filter((entry) => entry.action !== 'report');

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold text-[var(--warning)]">
          {t(language, 'auditLog.auditLog')}
        </h3>
        {audit.length > 0 &&
          (auditVerified ? (
            <span className="px-2 py-0.5 rounded text-xs border bg-[var(--success-bg)] text-[var(--success)] border-[var(--success-border)]">
              {t(language, 'auditLog.verified')}
            </span>
          ) : (
            <span className="px-2 py-0.5 rounded text-xs border bg-[var(--danger-bg)] text-[var(--danger)] border-[var(--danger-border)]">
              {t(language, 'auditLog.unverified')}
            </span>
          ))}
      </div>

      {visibleAudit.length === 0 ? (
        <p className="text-sm text-[var(--content-muted)] italic">
          {t(language, 'auditLog.noAuditEntriesConfigure')}
        </p>
      ) : (
        <div className="max-h-80 overflow-y-auto">
          <table className="w-full text-sm text-left">
            <thead className="text-xs text-[var(--content-muted)] uppercase tracking-wider border-b border-[var(--hairline)] sticky top-0 bg-[var(--surface-elevated)]">
              <tr>
                <th className="py-2 pr-3">{t(language, 'auditLog.time')}</th>
                <th className="py-2 pr-3">{t(language, 'auditLog.actor')}</th>
                <th className="py-2 pr-3">{t(language, 'auditLog.action')}</th>
                <th className="py-2 pr-3">{t(language, 'auditLog.node')}</th>
              </tr>
            </thead>
            <tbody>
              {visibleAudit.map((e, i) => (
                <tr key={`${e.timestamp}-${i}`} className="border-b border-[var(--hairline)]">
                  <td className="py-1.5 pr-3 text-[var(--content-muted)] text-xs whitespace-nowrap">
                    {fmtTime(e.timestamp)}
                  </td>
                  <td className="py-1.5 pr-3 text-[var(--content)] font-mono text-xs break-all">{e.actor}</td>
                  <td className="py-1.5 pr-3 text-[var(--info)] text-xs">{e.action}</td>
                  <td className="py-1.5 pr-3 text-[var(--content)] font-mono text-xs break-all">
                    {e.nodeId || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
