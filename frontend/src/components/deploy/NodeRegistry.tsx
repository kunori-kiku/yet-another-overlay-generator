import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';
import type { ControllerNodeStatus } from '../../types/controller';

// 注册表里某节点的 applied-vs-desired 代号是否漂移（已审批节点的 applied 落后于 desired
// ⇒ 该节点尚未拉取/应用最新一代配置）。
function isDrifting(applied: number, desired: number): boolean {
  return applied < desired;
}

// 状态徽标配色：approved 绿、pending 黄、revoked 红。
function statusClass(status: ControllerNodeStatus): string {
  switch (status) {
    case 'approved':
      return 'bg-green-900/40 text-green-300 border-green-700';
    case 'pending':
      return 'bg-yellow-900/40 text-yellow-300 border-yellow-700';
    case 'revoked':
      return 'bg-red-900/40 text-red-300 border-red-700';
  }
}

// last_seen / enrolled_at 是 RFC3339 字符串；零值（"0001-01-01T00:00:00Z"）显示为「—」。
function fmtTime(iso: string): string {
  if (!iso || iso.startsWith('0001-01-01')) return '—';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function NodeRegistry() {
  const language = useTopologyStore((s) => s.language);
  const topoNodes = useTopologyStore((s) => s.nodes);
  const edges = useTopologyStore((s) => s.edges);

  const ctlNodes = useControllerStore((s) => s.nodes);
  const revoke = useControllerStore((s) => s.revoke);
  const loading = useControllerStore((s) => s.loading);

  // controller 注册表按 nodeId 索引（agent enroll 时用的 --node-id 即拓扑节点 id）。
  const statusByNodeId = new Map<string, ControllerNodeStatus>(
    ctlNodes.map((n) => [n.nodeId, n.status]),
  );
  // 拓扑节点名查找（边的就绪状态用名字展示，便于操作员对应）。
  const nameByNodeId = new Map<string, string>(topoNodes.map((n) => [n.id, n.name]));

  // 边就绪：当且仅当两个端点节点在控制器注册表中都是 approved。
  const edgeReady = (fromId: string, toId: string): boolean =>
    statusByNodeId.get(fromId) === 'approved' && statusByNodeId.get(toId) === 'approved';

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-4">
      <h3 className="text-lg font-semibold text-blue-400">
        {txt(language, '节点注册表', 'Node Registry')}
      </h3>

      {ctlNodes.length === 0 ? (
        <p className="text-sm text-gray-500 italic">
          {txt(
            language,
            '暂无已注册节点。配置控制器连接并点击「刷新」，或在下方为节点签发注册令牌。',
            'No registered nodes. Configure the controller connection and click Refresh, or mint an enrollment token below.',
          )}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm text-left">
            <thead className="text-xs text-gray-400 uppercase tracking-wider border-b border-gray-700">
              <tr>
                <th className="py-2 pr-3">{txt(language, '节点', 'Node')}</th>
                <th className="py-2 pr-3">{txt(language, '状态', 'Status')}</th>
                <th className="py-2 pr-3">{txt(language, '代号 (已应用/期望)', 'Gen (applied/desired)')}</th>
                <th className="py-2 pr-3">{txt(language, '健康', 'Health')}</th>
                <th className="py-2 pr-3">{txt(language, '最近一次心跳', 'Last Seen')}</th>
                <th className="py-2 pr-3">{txt(language, '操作', 'Actions')}</th>
              </tr>
            </thead>
            <tbody>
              {ctlNodes.map((n) => {
                const drift = isDrifting(n.appliedGeneration, n.desiredGeneration);
                return (
                  <tr key={n.nodeId} className="border-b border-gray-700/50">
                    <td className="py-2 pr-3 font-mono text-gray-200 break-all">{n.nodeId}</td>
                    <td className="py-2 pr-3">
                      <span className={`px-2 py-0.5 rounded text-xs border ${statusClass(n.status)}`}>
                        {n.status}
                      </span>
                      {/* plan-4.6：operator 已请求该节点轮换 WG 密钥，等待 agent 重生并注册新公钥。 */}
                      {n.rekeyRequested && (
                        <span className="ml-1 px-2 py-0.5 rounded text-xs border bg-purple-900/40 text-purple-300 border-purple-700">
                          {txt(language, '🔑 轮换中', '🔑 rekeying')}
                        </span>
                      )}
                    </td>
                    <td className="py-2 pr-3 font-mono">
                      <span className={drift ? 'text-yellow-400' : 'text-gray-300'}>
                        {n.appliedGeneration} / {n.desiredGeneration}
                      </span>
                      {drift && (
                        <span className="ml-1 text-[10px] text-yellow-400">
                          {txt(language, '漂移', 'drift')}
                        </span>
                      )}
                    </td>
                    <td className="py-2 pr-3 text-gray-300">{n.lastHealth || '—'}</td>
                    <td className="py-2 pr-3 text-gray-400 text-xs">{fmtTime(n.lastSeen)}</td>
                    <td className="py-2 pr-3">
                      <button
                        onClick={() => revoke(n.nodeId)}
                        disabled={loading || n.status === 'revoked'}
                        className="px-2 py-1 text-xs bg-red-700 hover:bg-red-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white"
                      >
                        {txt(language, '驱逐', 'Revoke')}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* 每条边的就绪状态：两端节点均 approved 才算「就绪」（其链路可被编译进 fleet）。 */}
      <div className="space-y-2">
        <h4 className="text-sm font-semibold text-gray-400">
          {txt(language, '链路就绪状态', 'Edge Readiness')}
        </h4>
        {edges.length === 0 ? (
          <p className="text-xs text-gray-500 italic">
            {txt(language, '当前拓扑没有链路。', 'The current topology has no edges.')}
          </p>
        ) : (
          <ul className="space-y-1">
            {edges.map((e) => {
              const fromName = nameByNodeId.get(e.from_node_id) || e.from_node_id;
              const toName = nameByNodeId.get(e.to_node_id) || e.to_node_id;
              const ready = edgeReady(e.from_node_id, e.to_node_id);
              return (
                <li
                  key={e.id}
                  className="flex items-center justify-between text-xs bg-gray-700/40 px-2 py-1 rounded"
                >
                  <span className="text-gray-300">
                    {fromName} → {toName}
                    {e.role === 'backup' && (
                      <span className="ml-1 text-gray-500">
                        ({txt(language, '备份', 'backup')})
                      </span>
                    )}
                  </span>
                  {ready ? (
                    <span className="px-2 py-0.5 rounded border bg-green-900/40 text-green-300 border-green-700">
                      {txt(language, '✓ 就绪', '✓ Ready')}
                    </span>
                  ) : (
                    <span className="px-2 py-0.5 rounded border bg-gray-800 text-gray-400 border-gray-600">
                      {txt(language, '未就绪', 'Not ready')}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </section>
  );
}
