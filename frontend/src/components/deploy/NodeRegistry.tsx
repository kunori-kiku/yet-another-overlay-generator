import { Link } from 'react-router-dom';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import type { ControllerNodeStatus } from '../../types/controller';
import { UpdateStatusChip } from './UpdateStatusChip';

// isDrifting reports whether a node's applied-vs-desired generation has drifted (an approved node
// whose applied lags desired ⇒ it has not yet fetched/applied the latest generation of config).
function isDrifting(applied: number, desired: number): boolean {
  return applied < desired;
}

// statusClass returns the status-badge color: approved green, pending yellow, revoked red.
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

// fmtTime formats an RFC3339 string (last_seen / enrolled_at); the zero value
// ("0001-01-01T00:00:00Z") renders as "—".
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
  const clearRekey = useControllerStore((s) => s.clearRekey);
  const loading = useControllerStore((s) => s.loading);
  // The configured rollout drives the per-node update-status chip (plan-5). null (settings not yet
  // loaded) ⇒ deriveUpdateState returns 'off' ⇒ a muted dash, never a misleading chip.
  const settings = useControllerStore((s) => s.settings);

  // The controller registry is indexed by nodeId (the --node-id used at agent enroll is the topology
  // node id).
  const statusByNodeId = new Map<string, ControllerNodeStatus>(
    ctlNodes.map((n) => [n.nodeId, n.status]),
  );
  // Topology node-name lookup (edge readiness shows names so the operator can match them up).
  const nameByNodeId = new Map<string, string>(topoNodes.map((n) => [n.id, n.name]));
  // The set of node ids in the current design: a node-id present in the registry but absent from the
  // design is an "orphan" — it is still in the fleet (holds a valid token, fetches config) but no
  // longer belongs to the current design (plan-6, identity reconciliation).
  // Only judge orphans when a design actually exists locally (topoNodes non-empty): in the
  // enroll-first-then-design flow the canvas may be empty (hydration keeps an empty canvas when the
  // server has no design), and we must not then mislabel every node as "not in design" (the backend
  // deliberately does not warn on "minting a token with no design", so the frontend must not
  // contradict it) — plan-6 review.
  const designNodeIds = new Set<string>(topoNodes.map((n) => n.id));
  const designLoaded = topoNodes.length > 0;
  const isOrphan = (nodeId: string): boolean => designLoaded && !designNodeIds.has(nodeId);

  // Edge readiness: ready iff both endpoint nodes are approved in the controller registry.
  const edgeReady = (fromId: string, toId: string): boolean =>
    statusByNodeId.get(fromId) === 'approved' && statusByNodeId.get(toId) === 'approved';

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-4">
      <h3 className="text-lg font-semibold text-blue-400">
        {t(language, 'nodeRegistry.nodeRegistry')}
      </h3>

      {ctlNodes.length === 0 ? (
        <p className="text-sm text-gray-500 italic">
          {t(language, 'nodeRegistry.noRegisteredNodesConfigure')}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm text-left">
            <thead className="text-xs text-gray-400 uppercase tracking-wider border-b border-gray-700">
              <tr>
                <th className="py-2 pr-3">{t(language, 'nodeRegistry.node')}</th>
                <th className="py-2 pr-3">{t(language, 'nodeRegistry.status')}</th>
                <th className="py-2 pr-3">{t(language, 'nodeRegistry.genAppliedDesired')}</th>
                <th className="py-2 pr-3">{t(language, 'nodeRegistry.health')}</th>
                <th className="py-2 pr-3">{t(language, 'nodeRegistry.agentVersion')}</th>
                <th className="py-2 pr-3">{t(language, 'updateStatus.label')}</th>
                <th className="py-2 pr-3">{t(language, 'nodeRegistry.lastSeen')}</th>
                <th className="py-2 pr-3">{t(language, 'nodeRegistry.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {ctlNodes.map((n) => {
                const drift = isDrifting(n.appliedGeneration, n.desiredGeneration);
                return (
                  <tr key={n.nodeId} className="border-b border-gray-700/50">
                    <td className="py-2 pr-3 font-mono break-all">
                      <Link
                        to={`/fleet/nodes/${encodeURIComponent(n.nodeId)}`}
                        className="text-blue-300 hover:underline"
                      >
                        {n.nodeId}
                      </Link>
                    </td>
                    <td className="py-2 pr-3">
                      <span className={`px-2 py-0.5 rounded text-xs border ${statusClass(n.status)}`}>
                        {n.status}
                      </span>
                      {/* plan-4.6: the operator has requested this node rotate its WG key; waiting for
                          the agent to regenerate and register a new public key. */}
                      {n.rekeyRequested && (
                        <span className="ml-1 px-2 py-0.5 rounded text-xs border bg-purple-900/40 text-purple-300 border-purple-700">
                          {t(language, 'nodeRegistry.rekeying')}
                        </span>
                      )}
                      {/* plan-6: this node is in the fleet registry but not in the current design — an
                          identity-reconciliation marker telling the operator it has left the design
                          (revoke it on the right to remove it from the fleet). */}
                      {isOrphan(n.nodeId) && n.status !== 'revoked' && (
                        <span className="ml-1 px-2 py-0.5 rounded text-xs border bg-orange-900/40 text-orange-300 border-orange-700">
                          {t(language, 'nodeRegistry.notInDesign')}
                        </span>
                      )}
                    </td>
                    <td className="py-2 pr-3 font-mono">
                      <span className={drift ? 'text-yellow-400' : 'text-gray-300'}>
                        {n.appliedGeneration} / {n.desiredGeneration}
                      </span>
                      {drift && (
                        <span className="ml-1 text-[10px] text-yellow-400">
                          {t(language, 'nodeRegistry.drift')}
                        </span>
                      )}
                    </td>
                    <td className="py-2 pr-3 text-gray-300">{n.lastHealth || '—'}</td>
                    <td className="py-2 pr-3 font-mono text-xs text-gray-400">{n.agentVersion || '—'}</td>
                    <td className="py-2 pr-3">
                      <UpdateStatusChip node={n} settings={settings} language={language} />
                    </td>
                    <td className="py-2 pr-3 text-gray-400 text-xs">{fmtTime(n.lastSeen)}</td>
                    <td className="py-2 pr-3 whitespace-nowrap">
                      {/* Cancel rekey: release a stuck "Roll keys" straggler WITHOUT evicting it
                          (clears the flag; node keeps its approval + token). Shown only while the
                          node still owes a rotation — it is the non-destructive fix for a node that
                          never re-registered and is wedging the Deploy gate. */}
                      {n.rekeyRequested && n.status === 'approved' && (
                        <button
                          onClick={() => clearRekey(n.nodeId)}
                          disabled={loading}
                          title={t(language, 'nodeRegistry.cancelRekeyHint')}
                          className="mr-2 px-2 py-1 text-xs bg-purple-800 hover:bg-purple-700 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white"
                        >
                          {t(language, 'nodeRegistry.cancelRekey')}
                        </button>
                      )}
                      <button
                        onClick={() => {
                          // Revocation evicts a node from the fleet — confirm before firing (no
                          // immediate, single-click destructive action).
                          if (window.confirm(t(language, 'nodeRegistry.revokeConfirm', { node: n.nodeId }))) {
                            revoke(n.nodeId);
                          }
                        }}
                        disabled={loading || n.status === 'revoked'}
                        className="px-2 py-1 text-xs bg-red-700 hover:bg-red-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white"
                      >
                        {t(language, 'nodeRegistry.revoke')}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Per-edge readiness: an edge is "ready" only when both endpoint nodes are approved (its link
          can then be compiled into the fleet). */}
      <div className="space-y-2">
        <h4 className="text-sm font-semibold text-gray-400">
          {t(language, 'nodeRegistry.edgeReadiness')}
        </h4>
        {edges.length === 0 ? (
          <p className="text-xs text-gray-500 italic">
            {t(language, 'nodeRegistry.theCurrentTopologyHas')}
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
                        ({t(language, 'nodeRegistry.backup')})
                      </span>
                    )}
                  </span>
                  {ready ? (
                    <span className="px-2 py-0.5 rounded border bg-green-900/40 text-green-300 border-green-700">
                      {t(language, 'nodeRegistry.ready')}
                    </span>
                  ) : (
                    <span className="px-2 py-0.5 rounded border bg-gray-800 text-gray-400 border-gray-600">
                      {t(language, 'nodeRegistry.notReady')}
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
