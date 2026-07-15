import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t, type MessageKey } from '../../i18n';
import type { ControllerNode, ControllerNodeStatus } from '../../types/controller';
import type { Node } from '../../types/topology';
import { nodeNameMap, nodeDisplayName } from '../../lib/nodeName';
import { UpdateStatusChip } from './UpdateStatusChip';
import { NodeConditions } from './NodeConditions';
import { ManualKitApplyGuide } from './ManualKitApplyGuide';

// manualEndpoint formats a manual node's first public endpoint as host:port (port omitted when auto/0),
// or "—" when none is set.
function manualEndpoint(n: Node): string {
  const ep = n.public_endpoints?.[0];
  if (!ep || !ep.host) return '—';
  return ep.port ? `${ep.host}:${ep.port}` : ep.host;
}

// truncKey shows the head of a WG public key (full keys are 44 base64 chars — too wide for the card).
function truncKey(k: string): string {
  return k.length > 14 ? `${k.slice(0, 14)}…` : k;
}

// isDrifting reports whether a node's applied-vs-desired generation has drifted (an approved node
// whose applied lags desired ⇒ it has not yet fetched/applied the latest generation of config).
function isDrifting(applied: number, desired: number): boolean {
  return applied < desired;
}

// statusClass returns the status-badge color via the semantic status tokens (legible in both
// themes): approved → success, pending → warning, revoked → danger.
function statusClass(status: ControllerNodeStatus): string {
  switch (status) {
    case 'approved':
      return 'bg-[var(--success-bg)] text-[var(--success)] border-[var(--success-border)]';
    case 'pending':
      return 'bg-[var(--warning-bg)] text-[var(--warning)] border-[var(--warning-border)]';
    case 'revoked':
      return 'bg-[var(--danger-bg)] text-[var(--danger)] border-[var(--danger-border)]';
  }
}

// fmtTime formats an RFC3339 string (last_seen / enrolled_at); the zero value
// ("0001-01-01T00:00:00Z") renders as "—".
function fmtTime(iso: string): string {
  if (!iso || iso.startsWith('0001-01-01')) return '—';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

// NodeCell is one per-node status field. The single descriptor array (nodeCells, below) is the shared
// spine BOTH the desktop table and the below-lg mobile cards iterate: the table renders each cell as a
// <td>, the cards render the same cell as a labeled key/value row. Adding/removing/reordering a column
// edits this one array — the two presentations can never drift in which fields they show (the
// structural single-source-of-truth this plan owes, 2.1 step 5).
interface NodeCell {
  // labelKey is the column header (table <th>) / row label (mobile card).
  labelKey: MessageKey;
  // value is the per-node cell content; identical JSX in both presentations.
  value: ReactNode;
}

// CELL_LABEL_KEYS is the column header order, derived from the descriptor so the table <thead> needs
// no node data to render its labels (and the header can never list a different set than the body).
const CELL_LABEL_KEYS: readonly MessageKey[] = [
  'nodeRegistry.status',
  'nodeRegistry.genAppliedDesired',
  'nodeRegistry.health',
  'nodeRegistry.conditions',
  'nodeRegistry.agentVersion',
  'updateStatus.label',
  'nodeRegistry.lastSeen',
];

// nodeCells builds the descriptor array for one node from the same module-scope helpers (isDrifting,
// statusClass, fmtTime), the UpdateStatusChip, and the rekeying/orphan badges the table used inline.
// settings drives the UpdateStatusChip (plan-5); orphan marks a node in the registry but not in the
// current design (computed by the component-scope isOrphan closure and passed in).
function nodeCells(
  n: ControllerNode,
  settings: Parameters<typeof UpdateStatusChip>[0]['settings'],
  language: Parameters<typeof UpdateStatusChip>[0]['language'],
  orphan: boolean,
): NodeCell[] {
  const drift = isDrifting(n.appliedGeneration, n.desiredGeneration);
  return [
    {
      labelKey: 'nodeRegistry.status',
      value: (
        <>
          <span className={`px-2 py-0.5 rounded text-xs border ${statusClass(n.status)}`}>
            {n.status}
          </span>
          {/* plan-4.6: the operator has requested this node rotate its WG key; waiting for the agent
              to regenerate and register a new public key. */}
          {n.rekeyRequested && (
            <span className="ml-1 px-2 py-0.5 rounded text-xs border bg-[var(--info-bg)] text-[var(--info)] border-[var(--info-border)]">
              {t(language, 'nodeRegistry.rekeying')}
            </span>
          )}
          {/* plan-6: this node is in the fleet registry but not in the current design — an
              identity-reconciliation marker telling the operator it has left the design (revoke it to
              remove it from the fleet). */}
          {orphan && n.status !== 'revoked' && (
            <span className="ml-1 px-2 py-0.5 rounded text-xs border bg-[var(--warning-bg)] text-[var(--warning)] border-[var(--warning-border)]">
              {t(language, 'nodeRegistry.notInDesign')}
            </span>
          )}
        </>
      ),
    },
    {
      labelKey: 'nodeRegistry.genAppliedDesired',
      value: (
        <span className="font-mono">
          <span className={drift ? 'text-[var(--warning)]' : 'text-[var(--content)]'}>
            {n.appliedGeneration} / {n.desiredGeneration}
          </span>
          {drift && (
            <span className="ml-1 text-[10px] text-[var(--warning)]">{t(language, 'nodeRegistry.drift')}</span>
          )}
        </span>
      ),
    },
    {
      labelKey: 'nodeRegistry.health',
      value: <span className="text-[var(--content)]">{n.lastHealth || '—'}</span>,
    },
    {
      // plan-2: the structured conditions strip — the curated channel that supersedes string-matching
      // the free-form health line. Renders nothing (NodeConditions returns null) when the node has none.
      labelKey: 'nodeRegistry.conditions',
      value: <NodeConditions conditions={n.conditions} language={language} />,
    },
    {
      labelKey: 'nodeRegistry.agentVersion',
      value: <span className="font-mono text-xs text-[var(--content-muted)]">{n.agentVersion || '—'}</span>,
    },
    {
      labelKey: 'updateStatus.label',
      value: <UpdateStatusChip node={n} settings={settings} language={language} />,
    },
    {
      labelKey: 'nodeRegistry.lastSeen',
      value: <span className="text-[var(--content-muted)] text-xs">{fmtTime(n.lastSeen)}</span>,
    },
  ];
}

export function NodeRegistry() {
  const language = useTopologyStore((s) => s.language);
  const topoNodes = useTopologyStore((s) => s.nodes);
  const edges = useTopologyStore((s) => s.edges);

  const ctlNodes = useControllerStore((s) => s.nodes);
  const revoke = useControllerStore((s) => s.revoke);
  const clearRekey = useControllerStore((s) => s.clearRekey);
  const downloadManualBundle = useControllerStore((s) => s.downloadManualNodeBundle);
  const loading = useControllerStore((s) => s.loading);
  // Manual AgentHeld installs need the pinned operator PUBLIC descriptor through a channel
  // independent of the candidate bundle. These values were already hydrated into controller
  // state by the authenticated keystone-status probe; rendering/copying performs no ad-hoc fetch.
  const serverOperatorPinned = useControllerStore((s) => s.serverOperatorPinned);
  const serverOperatorAlg = useControllerStore((s) => s.serverOperatorAlg);
  const serverOperatorRpId = useControllerStore((s) => s.serverOperatorRpId);
  const serverOperatorOrigin = useControllerStore((s) => s.serverOperatorOrigin);
  const serverOperatorPublicKeyPEM = useControllerStore((s) => s.serverOperatorPublicKeyPEM);
  const serverOperatorFingerprint = useControllerStore((s) => s.serverOperatorFingerprint);
  const manualKitTrust = {
    pinned: serverOperatorPinned,
    alg: serverOperatorAlg,
    rpId: serverOperatorRpId,
    origin: serverOperatorOrigin,
    publicKeyPEM: serverOperatorPublicKeyPEM,
  };
  // The configured rollout drives the per-node update-status chip (plan-5). null (settings not yet
  // loaded) ⇒ deriveUpdateState returns 'off' ⇒ a muted dash, never a misleading chip.
  const settings = useControllerStore((s) => s.settings);

  // The controller registry is indexed by nodeId (the --node-id used at agent enroll is the topology
  // node id).
  const statusByNodeId = new Map<string, ControllerNodeStatus>(
    ctlNodes.map((n) => [n.nodeId, n.status]),
  );
  // Topology node-name lookup. The fleet registry is keyed by node_id (the enrolled identity), but the
  // operator reads the friendly design name — nodeDisplayName resolves id→name with an id fallback
  // (orphan / no design / blank name). The node labels and the edge-readiness list share this one map.
  const nameByNodeId = nodeNameMap(topoNodes);
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

  // Manual nodes (deployment_mode==='manual') are hand-deployed and agent-less: they carry an
  // operator-asserted identity in the DESIGN but never enroll, so they have NO registry record and
  // never appear in the monitored table above (nor in the convergence/edge-readiness gating — they
  // are intentionally unmonitored, D3). Derive them from the topology and surface them separately so
  // the operator can see them and download each one's bundle to install by hand.
  const manualNodes = topoNodes.filter((n) => n.deployment_mode === 'manual');
  const manualNodeIds = new Set<string>(manualNodes.map((n) => n.id));

  // Edge readiness: an endpoint is ready when it is approved in the registry OR it is a MANUAL node.
  // A manual node never enrolls (no registry record), so it can never be 'approved' — but it is
  // excluded from convergence/edge-readiness gating (D3): its readiness is operator-asserted (the
  // operator hand-deploys it), so we treat it as satisfied rather than reporting every manual-touching
  // link as perpetually "Not ready".
  const endpointReady = (id: string): boolean => manualNodeIds.has(id) || statusByNodeId.get(id) === 'approved';
  const edgeReady = (fromId: string, toId: string): boolean => endpointReady(fromId) && endpointReady(toId);

  // The per-node action cluster (Cancel-rekey + Revoke), shared by the desktop table row and the
  // below-lg mobile card so the two presentations stay behaviorally identical. `fullWidth` makes the
  // buttons stretch in the narrow card (easier phone tap targets) while staying inline in the table.
  const actions = (n: ControllerNode, fullWidth: boolean): ReactNode => {
    // In the below-lg mobile card, stretch the buttons AND give them a >=44px tap height (min-h-11)
    // so Revoke / Cancel-rekey meet the phone tap-target minimum (plan-17 / 3.5 verifies this). The
    // desktop table keeps the compact inline height (fullWidth=false).
    const btn = fullWidth ? 'flex-1 text-center min-h-11' : '';
    return (
      <>
        {/* Cancel rekey: release a stuck "Roll keys" straggler WITHOUT evicting it (clears the flag;
            node keeps its approval + token). Shown only while the node still owes a rotation — it is
            the non-destructive fix for a node that never re-registered and is wedging the Deploy gate. */}
        {n.rekeyRequested && n.status === 'approved' && (
          <button
            onClick={() => clearRekey(n.nodeId)}
            disabled={loading}
            title={t(language, 'nodeRegistry.cancelRekeyHint')}
            className={`${btn} px-3 py-2 text-xs bg-[var(--info-solid)] hover:bg-[var(--info-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--info-solid-fg)]`}
          >
            {t(language, 'nodeRegistry.cancelRekey')}
          </button>
        )}
        <button
          onClick={() => {
            // Revocation evicts a node from the fleet — confirm before firing (no immediate,
            // single-click destructive action).
            if (window.confirm(t(language, 'nodeRegistry.revokeConfirm', { node: n.nodeId }))) {
              revoke(n.nodeId);
            }
          }}
          disabled={loading || n.status === 'revoked'}
          className={`${btn} px-3 py-2 text-xs bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--danger-solid-fg)]`}
        >
          {t(language, 'nodeRegistry.revoke')}
        </button>
      </>
    );
  };

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-4">
      <h3 className="text-lg font-semibold text-[var(--info)]">
        {t(language, 'nodeRegistry.nodeRegistry')}
      </h3>

      {ctlNodes.length === 0 ? (
        <p className="text-sm text-[var(--content-muted)] italic">
          {t(language, 'nodeRegistry.noRegisteredNodesConfigure')}
        </p>
      ) : (
        <>
          {/* Desktop / large-tablet: the 8-column table (lg+, ≥1024px). Hidden below lg, where the
              card list (next block) takes over — both iterate the SAME nodeCells descriptor spine, so
              the column set never drifts between presentations. */}
          <div className="hidden lg:block overflow-x-auto">
            <table className="w-full text-sm text-left">
              <thead className="text-xs text-[var(--content-muted)] uppercase tracking-wider border-b border-[var(--hairline)]">
                <tr>
                  <th className="py-2 pr-3">{t(language, 'nodeRegistry.node')}</th>
                  {CELL_LABEL_KEYS.map((labelKey) => (
                    <th key={labelKey} className="py-2 pr-3">
                      {t(language, labelKey)}
                    </th>
                  ))}
                  <th className="py-2 pr-3">{t(language, 'nodeRegistry.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {ctlNodes.map((n) => (
                  <tr key={n.nodeId} className="border-b border-[var(--hairline)]">
                    <td className="py-2 pr-3 break-all">
                      <Link
                        to={`/fleet/nodes/${encodeURIComponent(n.nodeId)}`}
                        data-testid={`fleet-node-${n.nodeId}`}
                        className="text-[var(--info)] hover:underline"
                      >
                        <span className="font-medium">{nodeDisplayName(n.nodeId, nameByNodeId)}</span>
                        {/* Keep the node_id visible as a muted reference — it is the identity the operator
                            uses to revoke / troubleshoot — but only when a friendly name replaced it. */}
                        {nodeDisplayName(n.nodeId, nameByNodeId) !== n.nodeId && (
                          <span className="ml-2 font-mono text-xs text-[var(--content-muted)]">
                            {n.nodeId}
                          </span>
                        )}
                      </Link>
                    </td>
                    {nodeCells(n, settings, language, isOrphan(n.nodeId)).map((c) => (
                      <td key={c.labelKey} className="py-2 pr-3">
                        {c.value}
                      </td>
                    ))}
                    <td className="py-2 pr-3">
                      <div className="flex items-center gap-2 whitespace-nowrap">{actions(n, false)}</div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* Phone / small-tablet (below lg): the same nodes reflowed into stacked cards — node-id
              heading, the nodeCells descriptor as labeled key/value rows, then the action cluster. */}
          <div className="lg:hidden space-y-3">
            {ctlNodes.map((n) => (
              <div
                key={n.nodeId}
                className="rounded-lg border border-[var(--hairline)] bg-[var(--surface)] p-3 space-y-2"
              >
                <Link
                  to={`/fleet/nodes/${encodeURIComponent(n.nodeId)}`}
                  data-testid={`fleet-node-${n.nodeId}`}
                  className="block text-sm text-[var(--info)] hover:underline break-all"
                >
                  <span className="font-medium">{nodeDisplayName(n.nodeId, nameByNodeId)}</span>
                  {nodeDisplayName(n.nodeId, nameByNodeId) !== n.nodeId && (
                    <span className="ml-2 font-mono text-xs text-[var(--content-muted)]">{n.nodeId}</span>
                  )}
                </Link>
                <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 text-sm">
                  {nodeCells(n, settings, language, isOrphan(n.nodeId)).map((c) => (
                    <div key={c.labelKey} className="contents">
                      <dt className="text-xs text-[var(--content-muted)]">{t(language, c.labelKey)}</dt>
                      <dd className="text-right">{c.value}</dd>
                    </div>
                  ))}
                </dl>
                <div className="flex items-center gap-2 pt-1">{actions(n, true)}</div>
              </div>
            ))}
          </div>
        </>
      )}

      {/* Manual nodes (hand-deployed, agent-less): derived from the design, NOT the registry. They are
          deliberately unmonitored — the operator downloads each one's signed bundle and installs it by
          hand. Shown only when the design has manual nodes. */}
      {manualNodes.length > 0 && (
        <div className="space-y-2">
          <h4 className="text-sm font-semibold text-[var(--content-muted)]">
            {t(language, 'nodeRegistry.manualNodes')}
          </h4>
          <p className="text-xs text-[var(--content-muted)] italic">
            {t(language, 'nodeRegistry.manualNodesHint')}
          </p>
          <ManualKitApplyGuide
            language={language}
            nodes={manualNodes}
            trust={manualKitTrust}
            fingerprint={serverOperatorFingerprint}
          />
          <ul className="space-y-2">
            {manualNodes.map((n) => (
              <li
                key={n.id}
                data-testid="manual-node-card"
                className="rounded-lg border border-[var(--hairline)] bg-[var(--surface)] p-3 space-y-2"
              >
                <div className="flex items-center justify-between gap-2 flex-wrap">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="font-mono text-sm text-[var(--content)] break-all">
                      {n.name || n.id}
                    </span>
                    <span className="px-2 py-0.5 rounded text-xs border bg-[var(--info-bg)] text-[var(--info)] border-[var(--info-border)] whitespace-nowrap">
                      {t(language, 'nodeRegistry.manualUnmonitored')}
                    </span>
                    <span className="text-xs text-[var(--content-muted)]">{n.role}</span>
                  </div>
                  <button
                    onClick={() => downloadManualBundle(n.id)}
                    disabled={loading}
                    data-testid="download-manual-bundle"
                    title={t(language, 'nodeRegistry.downloadBundleHint')}
                    className="px-3 py-2 text-xs bg-[var(--info-solid)] hover:bg-[var(--info-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--info-solid-fg)] min-h-11 lg:min-h-0"
                  >
                    {t(language, 'nodeRegistry.downloadBundle')}
                  </button>
                </div>
                <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
                  <dt className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.publicKey')}</dt>
                  <dd className="text-right font-mono break-all">
                    {n.wireguard_public_key ? (
                      <span className="text-[var(--content)]">{truncKey(n.wireguard_public_key)}</span>
                    ) : (
                      <span className="px-2 py-0.5 rounded border bg-[var(--warning-bg)] text-[var(--warning)] border-[var(--warning-border)]">
                        {t(language, 'nodeRegistry.noPublicKey')}
                      </span>
                    )}
                  </dd>
                  <dt className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.endpoint')}</dt>
                  <dd className="text-right font-mono break-all text-[var(--content)]">
                    {manualEndpoint(n)}
                  </dd>
                </dl>
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* Per-edge readiness: each endpoint qualifies when it is agent-approved OR manual; both ends
          must qualify before their link can be compiled into the deploy subgraph. */}
      <div className="space-y-2">
        <h4 className="text-sm font-semibold text-[var(--content-muted)]">
          {t(language, 'nodeRegistry.edgeReadiness')}
        </h4>
        {edges.length === 0 ? (
          <p className="text-xs text-[var(--content-muted)] italic">
            {t(language, 'nodeRegistry.theCurrentTopologyHas')}
          </p>
        ) : (
          <ul className="space-y-1">
            {edges.map((e) => {
              const fromName = nodeDisplayName(e.from_node_id, nameByNodeId);
              const toName = nodeDisplayName(e.to_node_id, nameByNodeId);
              const ready = edgeReady(e.from_node_id, e.to_node_id);
              return (
                <li
                  key={e.id}
                  className="flex items-center justify-between text-xs bg-[var(--control)] px-2 py-1 rounded"
                >
                  <span className="text-[var(--content)]">
                    {fromName} → {toName}
                    {e.role === 'backup' && (
                      <span className="ml-1 text-[var(--content-muted)]">
                        ({t(language, 'nodeRegistry.backup')})
                      </span>
                    )}
                  </span>
                  {ready ? (
                    <span className="px-2 py-0.5 rounded border bg-[var(--success-bg)] text-[var(--success)] border-[var(--success-border)]">
                      {t(language, 'nodeRegistry.ready')}
                    </span>
                  ) : (
                    <span className="px-2 py-0.5 rounded border bg-[var(--surface-elevated)] text-[var(--content-muted)] border-[var(--hairline)]">
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
