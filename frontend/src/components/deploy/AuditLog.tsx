import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// 审计日志：展示控制器审计链（操作员/agent 的关键动作）+ 哈希链是否完整的徽标。
// 条目本身由后端按 Seq 顺序返回（最早在前）。verified=false 表示链被篡改或断裂。
function fmtTime(iso: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function AuditLog() {
  const language = useTopologyStore((s) => s.language);
  const audit = useControllerStore((s) => s.audit);
  const auditVerified = useControllerStore((s) => s.auditVerified);

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold text-orange-400">
          {t(language, 'auditLog.auditLog')}
        </h3>
        {audit.length > 0 &&
          (auditVerified ? (
            <span className="px-2 py-0.5 rounded text-xs border bg-green-900/40 text-green-300 border-green-700">
              {t(language, 'auditLog.verified')}
            </span>
          ) : (
            <span className="px-2 py-0.5 rounded text-xs border bg-red-900/40 text-red-300 border-red-700">
              {t(language, 'auditLog.unverified')}
            </span>
          ))}
      </div>

      {audit.length === 0 ? (
        <p className="text-sm text-gray-500 italic">
          {t(language, 'auditLog.noAuditEntriesConfigure')}
        </p>
      ) : (
        <div className="max-h-80 overflow-y-auto">
          <table className="w-full text-sm text-left">
            <thead className="text-xs text-gray-400 uppercase tracking-wider border-b border-gray-700 sticky top-0 bg-gray-800">
              <tr>
                <th className="py-2 pr-3">{t(language, 'auditLog.time')}</th>
                <th className="py-2 pr-3">{t(language, 'auditLog.actor')}</th>
                <th className="py-2 pr-3">{t(language, 'auditLog.action')}</th>
                <th className="py-2 pr-3">{t(language, 'auditLog.node')}</th>
              </tr>
            </thead>
            <tbody>
              {audit.map((e, i) => (
                <tr key={`${e.timestamp}-${i}`} className="border-b border-gray-700/50">
                  <td className="py-1.5 pr-3 text-gray-400 text-xs whitespace-nowrap">
                    {fmtTime(e.timestamp)}
                  </td>
                  <td className="py-1.5 pr-3 text-gray-300 font-mono text-xs break-all">{e.actor}</td>
                  <td className="py-1.5 pr-3 text-cyan-300 text-xs">{e.action}</td>
                  <td className="py-1.5 pr-3 text-gray-300 font-mono text-xs break-all">
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
