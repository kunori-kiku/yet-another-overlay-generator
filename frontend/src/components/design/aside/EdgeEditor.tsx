import { useTopologyStore } from '../../../stores/topologyStore';
import { t } from '../../../i18n';
import { resolveEdgeInterface } from '../../../lib/compiledInterfaces';

// MIN_PINNED_PORT mirrors the backend's minPinnedPort (validator) — the lower bound for an
// operator-chosen pinned listen port. Auto-allocation still starts at 51820, but a port-
// restricted NAT VPS may forward a fixed range below it, so manual pins go down to 1024.
const MIN_PINNED_PORT = 1024;

// Default transit pool when a domain leaves transit_cidr empty (mirrors the backend default).
const DEFAULT_TRANSIT_CIDR = '10.10.0.0/24';

// ipv4ToInt parses an IPv4 dotted-quad to a uint32, or null when malformed.
function ipv4ToInt(ip: string): number | null {
  const parts = ip.trim().split('.');
  if (parts.length !== 4) return null;
  let n = 0;
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return null;
    const o = parseInt(p, 10);
    if (o < 0 || o > 255) return null;
    n = (n << 8) | o;
  }
  return n >>> 0;
}

// ipv4InCidr reports whether an IPv4 address falls within an IPv4 CIDR. IPv4-only (transit pools
// are IPv4); returns false on malformed input so the UI flags it (the backend validator is the
// authoritative check — this is just early, inline feedback before Save).
function ipv4InCidr(ip: string, cidr: string): boolean {
  const [net, bitsStr] = cidr.split('/');
  const bits = parseInt(bitsStr, 10);
  if (isNaN(bits) || bits < 0 || bits > 32) return false;
  const ipInt = ipv4ToInt(ip);
  const netInt = ipv4ToInt(net);
  if (ipInt === null || netInt === null) return false;
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return (ipInt & mask) === (netInt & mask);
}

// 连接（边）属性编辑器（从 RightPanel 的选中边区块原样抽出，含目标端点选择 / 传输协议 /
// 链路角色 / 优先级 / 权重 / 备份链路 / 已固定分配 / 编译后实际值）。供 Design 右侧 aside 使用。
export function EdgeEditor() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const domains = useTopologyStore((s) => s.domains);
  const edges = useTopologyStore((s) => s.edges);
  const selectedEdgeId = useTopologyStore((s) => s.selectedEdgeId);
  const updateEdge = useTopologyStore((s) => s.updateEdge);
  const removeEdge = useTopologyStore((s) => s.removeEdge);
  const addBackupEdge = useTopologyStore((s) => s.addBackupEdge);
  const compileResult = useTopologyStore((s) => s.compileResult);

  const selectedEdge = edges.find((e) => e.id === selectedEdgeId);
  const selectedEdgeTarget = selectedEdge
    ? nodes.find((n) => n.id === selectedEdge.to_node_id)
    : undefined;

  const targetEndpointOptions = selectedEdgeTarget?.public_endpoints || [];
  // Deduplicate hosts from target's public endpoints for IP picker
  const targetHostOptions = Array.from(
    new Set(targetEndpointOptions.map((ep) => ep.host).filter(Boolean)),
  );
  const matchedTargetHost = selectedEdge?.endpoint_host
    ? targetHostOptions.includes(selectedEdge.endpoint_host)
      ? `host:${selectedEdge.endpoint_host}`
      : '__manual__'
    : '__none__';

  // Get the compiled port for the selected edge from the compiled topology
  const compiledEdgePort = (() => {
    if (!compileResult || !selectedEdge) return undefined;
    const compiledEdge = compileResult.topology.edges?.find((e) => e.id === selectedEdge.id);
    return compiledEdge?.compiled_port || undefined;
  })();

  // 并行链路（edge.md）：备份链路从主链路派生。
  // 选中边的源节点（client 角色门控备份按钮：后端拒绝 client 上的备份链路）。
  const selectedEdgeFrom = selectedEdge
    ? nodes.find((n) => n.id === selectedEdge.from_node_id)
    : undefined;
  const selectedEdgeIsBackup = selectedEdge?.role === 'backup';
  const selectedEdgeTouchesClient =
    selectedEdgeFrom?.role === 'client' || selectedEdgeTarget?.role === 'client';
  // 备份按钮：源/目标任一为 client 时隐藏（后端拒绝），选中边本身已是 backup 时隐藏
  //（备份从主链路添加，而非从备份再派生）。
  const showAddBackupButton = !!selectedEdge && !selectedEdgeIsBackup && !selectedEdgeTouchesClient;
  // 路径分集提示：选中的备份链路与同一节点对的另一条边共用了同一公网地址，
  // 说明备份未指向独立路径（addBackupEdge 复制了主链路的 endpoint_host），提示操作员另指地址。
  const showBackupEndpointNudge =
    !!selectedEdge &&
    selectedEdgeIsBackup &&
    !!selectedEdge.endpoint_host &&
    edges.some(
      (e) =>
        e.id !== selectedEdge.id &&
        e.from_node_id === selectedEdge.from_node_id &&
        e.to_node_id === selectedEdge.to_node_id &&
        e.endpoint_host === selectedEdge.endpoint_host,
    );

  if (!selectedEdge) return null;

  // Directional NAT target (PR2): the internal listen port a NAT forward must hit. The
  // compiler renders a forward edge's endpoint UNCONDITIONALLY at the to-side port —
  // formatEndpoint(edge.EndpointHost, alloc.toPort), written back to pinned_to_port and
  // echoed as compiled_port (compiler peers.go / compiler.go); it never branches on which
  // node owns the host string. endpoint_host on the canvas is likewise always a snapshot of
  // the TO node (reconcileEdgeEndpoints only writes it for the edge's target). So a forward
  // edge always dials the to-node at pinned_to_port — mirror that here. Sourced from the
  // edge's own fields — independent of the controller-null compileResult.
  const natTargetPort = selectedEdge.pinned_to_port;
  const natTargetNode = selectedEdgeTarget;
  // External dial port: the NAT-override endpoint_port when set, else the compiled echo (or the
  // internal listen port when nothing else is known). When it differs from the internal listen
  // port an external→internal forward is required — surface the hint then.
  const natDialPort =
    selectedEdge.endpoint_port && selectedEdge.endpoint_port > 0
      ? selectedEdge.endpoint_port
      : selectedEdge.compiled_port ?? natTargetPort;
  const natForwardActive = natTargetPort !== undefined && natDialPort !== natTargetPort;
  const hasPinnedPort =
    selectedEdge.pinned_from_port !== undefined || selectedEdge.pinned_to_port !== undefined;

  // PR7 — operator-settable pin validation (inline early feedback; the backend validator is the
  // authoritative gate at Validate/Compile/Deploy). The transit pool is resolved from the edge's
  // from-node domain (default 10.10.0.0/24), matching the backend's edgeTransitCIDR resolution.
  const edgeTransitCidr =
    (selectedEdgeFrom && domains.find((d) => d.id === selectedEdgeFrom.domain_id)?.transit_cidr) ||
    DEFAULT_TRANSIT_CIDR;
  const portPairIncomplete =
    (selectedEdge.pinned_from_port !== undefined) !== (selectedEdge.pinned_to_port !== undefined);
  const portOutOfRange = [selectedEdge.pinned_from_port, selectedEdge.pinned_to_port].some(
    (p) => p !== undefined && (p < MIN_PINNED_PORT || p > 65535),
  );
  const transitPairIncomplete =
    !!selectedEdge.pinned_from_transit_ip !== !!selectedEdge.pinned_to_transit_ip;
  const transitOutOfPool = [
    selectedEdge.pinned_from_transit_ip,
    selectedEdge.pinned_to_transit_ip,
  ].some((ip) => !!ip && !ipv4InCidr(ip, edgeTransitCidr));
  const hasLinkLocalPin =
    selectedEdge.pinned_from_link_local !== undefined ||
    selectedEdge.pinned_to_link_local !== undefined;
  // The pinned-allocation editor shows once the edge carries ANY pin (the common post-Compile /
  // post-Deploy state) so the operator can adjust the NAT-relevant values, then Save.
  const hasAnyPin =
    hasPinnedPort ||
    selectedEdge.pinned_from_transit_ip !== undefined ||
    selectedEdge.pinned_to_transit_ip !== undefined ||
    hasLinkLocalPin;

  // setPinPort maps a number input's raw value to a pin field value: '' clears the pin
  // (undefined); a valid integer sets it; anything else is ignored (keeps the prior value).
  const setPinPort = (field: 'pinned_from_port' | 'pinned_to_port', raw: string) => {
    if (raw === '') {
      updateEdge(selectedEdge.id, { [field]: undefined });
      return;
    }
    const parsed = parseInt(raw, 10);
    if (!isNaN(parsed)) updateEdge(selectedEdge.id, { [field]: parsed });
  };
  const setPinTransit = (
    field: 'pinned_from_transit_ip' | 'pinned_to_transit_ip',
    raw: string,
  ) => updateEdge(selectedEdge.id, { [field]: raw || undefined });

  return (
    <section>
      <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
        {t(language, 'edgeEditor.edgeProperties')}
      </h2>
      <div className="space-y-2">
        <div>
          <label className="text-xs text-gray-400">{t(language, 'edgeEditor.type')}</label>
          <select
            value={selectedEdge.type}
            onChange={(e) =>
              updateEdge(selectedEdge.id, {
                type: e.target.value as 'direct' | 'public-endpoint' | 'relay-path' | 'candidate',
                // 清空陈旧的编译端口，画布标签随即反映最新意图（直到重新编译）
                compiled_port: undefined,
              })
            }
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="direct">{t(language, 'edgeEditor.typeDirect')}</option>
            <option value="public-endpoint">{t(language, 'edgeEditor.typePublicEndpoint')}</option>
            <option value="relay-path">{t(language, 'edgeEditor.typeRelayPath')}</option>
            <option value="candidate">{t(language, 'edgeEditor.typeCandidate')}</option>
          </select>
        </div>
        {/* Endpoint IP — pick from target's public IPs or manual */}
        <div>
          <label className="text-xs text-gray-400">{t(language, 'edgeEditor.endpointIPFromTarget')}</label>
          {targetHostOptions.length > 0 && (
            <select
              value={matchedTargetHost}
              onChange={(e) => {
                const value = e.target.value;
                if (value === '__none__') {
                  updateEdge(selectedEdge.id, {
                    endpoint_host: undefined,
                    compiled_port: undefined,
                  });
                  return;
                }
                if (value === '__manual__') {
                  // Keep the current value — user will type in the text input below
                  return;
                }
                const host = value.replace('host:', '');
                updateEdge(selectedEdge.id, {
                  endpoint_host: host,
                  compiled_port: undefined,
                });
              }}
              className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
            >
              <option value="__none__">{t(language, 'edgeEditor.unset')}</option>
              {targetHostOptions.map((host) => (
                <option key={host} value={`host:${host}`}>
                  {host}
                </option>
              ))}
              <option value="__manual__">{t(language, 'edgeEditor.manualInput')}</option>
            </select>
          )}
          <input
            key={`ep-host-${selectedEdge.id}`}
            type="text"
            value={selectedEdge.endpoint_host || ''}
            onChange={(e) => updateEdge(selectedEdge.id, { endpoint_host: e.target.value || undefined, compiled_port: undefined })}
            placeholder={t(language, 'edgeEditor.ipOrHostname')}
            className="w-full mt-1 px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        {/* Endpoint Port — 0 or empty = auto, nonzero = NAT/port-forward override */}
        <div>
          <label className="text-xs text-gray-400">{t(language, 'edgeEditor.endpointPort0Auto')}</label>
          <div className="flex gap-1 items-center">
            <input
              key={`ep-port-${selectedEdge.id}`}
              type="number"
              value={selectedEdge.endpoint_port ?? ''}
              onChange={(e) => {
                const raw = e.target.value;
                if (raw === '') {
                  updateEdge(selectedEdge.id, { endpoint_port: undefined, compiled_port: undefined });
                } else {
                  const parsed = parseInt(raw, 10);
                  if (!isNaN(parsed)) {
                    updateEdge(selectedEdge.id, { endpoint_port: parsed, compiled_port: undefined });
                  }
                }
              }}
              placeholder={t(language, 'edgeEditor.0Auto')}
              className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
            />
          </div>
          {compiledEdgePort && (
            <p className="text-[10px] text-cyan-400 mt-0.5 font-mono">
              {t(language, 'edgeEditor.compiledPort')}: {compiledEdgePort}
              {selectedEdge.endpoint_port && selectedEdge.endpoint_port > 0 && selectedEdge.endpoint_port !== compiledEdgePort && (
                <span className="text-yellow-400 ml-1">
                  ({t(language, 'edgeEditor.natOverrideActive')})
                </span>
              )}
            </p>
          )}
        </div>
        {compileResult && (() => {
          // Spec（naming.md / Decisions #12）禁止前端重建接口名（>12 字符时后端走 hash
          // 后缀分支，并行链路下 backup 还把 edge.ID 折进 hash，前端无从复现）。改用共享解析器
          // resolveEdgeInterface 按 pin 的端口反查后端实际生成的接口（端口在单节点内唯一 ⇒
          // 确定性匹配），再用解析出的接口名从 wireguard_configs（键格式 "<nodeID>:<接口名>"）
          // 取出本端配置体读出 Endpoint 行。取本边的 from 侧接口（from_node_id + pinned_from_port）。
          const fromIface = resolveEdgeInterface(
            selectedEdge,
            true,
            compileResult.wireguard_configs,
          );
          if (!fromIface) return null;
          const config =
            compileResult.wireguard_configs[`${selectedEdge.from_node_id}:${fromIface.interfaceName}`];
          const endpointMatch = config?.match(/Endpoint\s*=\s*(.+)/);
          return (
            <div className="p-2 bg-gray-700/50 rounded space-y-1">
              <p className="text-xs text-gray-400 font-semibold">{t(language, 'edgeEditor.compiledValues')}</p>
              <p className="text-xs text-cyan-300 font-mono break-all">{t(language, 'edgeEditor.localInterface')}: {fromIface.interfaceName}</p>
              {endpointMatch && (
                <p className="text-xs text-cyan-300 font-mono break-all">{t(language, 'edgeEditor.endpoint')}: {endpointMatch[1]}</p>
              )}
              <p className="text-xs text-cyan-300 font-mono">{t(language, 'edgeEditor.localListenPort')}: {fromIface.listenPort}</p>
            </div>
          );
        })()}
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={selectedEdge.is_enabled}
            onChange={(e) => updateEdge(selectedEdge.id, { is_enabled: e.target.checked })}
          />
          {t(language, 'edgeEditor.enabled')}
        </label>
        {/* 传输协议 / 优先级 / 权重 / 备注（D68）。priority 与 weight 影响 Babel 的链路开销。 */}
        <div>
          <label className="text-xs text-gray-400">{t(language, 'edgeEditor.transport')}</label>
          <select
            value={selectedEdge.transport || 'udp'}
            onChange={(e) =>
              updateEdge(selectedEdge.id, {
                transport: e.target.value as 'udp' | 'tcp',
              })
            }
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="udp">UDP</option>
            <option value="tcp">{t(language, 'edgeEditor.tcpMimic')}</option>
          </select>
          {selectedEdge.transport === 'tcp' && (
            <p className="mt-1 text-xs text-gray-400">
              {t(language, 'mimicHint')}
            </p>
          )}
        </div>
        {/* 链路角色（edge.md 并行链路）：空 = 主链路类；backup = 独立的备份链路。
            角色变更会改变链路身份（重新 key），属拨号相关编辑 ⇒ 与本文件其他拨号相关编辑一致，
            一并清空陈旧的 compiled_port。 */}
        <div>
          <label className="text-xs text-gray-400">{t(language, 'roleLabel')}</label>
          <select
            value={selectedEdge.role || ''}
            onChange={(e) => {
              const value = e.target.value;
              updateEdge(selectedEdge.id, {
                role: value === '' ? undefined : (value as 'primary' | 'backup'),
                compiled_port: undefined,
              });
            }}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="">{t(language, 'rolePrimary')} ({t(language, 'edgeEditor.default')})</option>
            <option value="primary">{t(language, 'rolePrimary')}</option>
            <option value="backup">{t(language, 'roleBackup')}</option>
          </select>
          <p className="text-[10px] text-gray-500 mt-0.5">
            {t(language, 'edgeEditor.backupLinksDefaultTo')}
          </p>
          {hasPinnedPort && (
            <p className="text-[10px] text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded mt-1">
              {t(language, 'edgeEditor.roleChangeRealloc')}
            </p>
          )}
        </div>
        <div>
          <label className="text-xs text-gray-400">
            {t(language, 'edgeEditor.priorityDrivesBabelLink')}
          </label>
          <input
            type="number"
            value={selectedEdge.priority ?? ''}
            onChange={(e) => {
              const raw = e.target.value;
              if (raw === '') {
                updateEdge(selectedEdge.id, { priority: undefined });
                return;
              }
              const parsed = parseInt(raw, 10);
              if (!isNaN(parsed)) {
                updateEdge(selectedEdge.id, { priority: parsed });
              }
            }}
            placeholder={t(language, 'edgeEditor.default_2')}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">
            {t(language, 'edgeEditor.weightDrivesBabelLink')}
          </label>
          <input
            type="number"
            value={selectedEdge.weight ?? ''}
            onChange={(e) => {
              const raw = e.target.value;
              if (raw === '') {
                updateEdge(selectedEdge.id, { weight: undefined });
                return;
              }
              const parsed = parseInt(raw, 10);
              if (!isNaN(parsed)) {
                updateEdge(selectedEdge.id, { weight: parsed });
              }
            }}
            placeholder={t(language, 'edgeEditor.default_3')}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">{t(language, 'edgeEditor.notes')}</label>
          <input
            type="text"
            value={selectedEdge.notes || ''}
            onChange={(e) =>
              updateEdge(selectedEdge.id, { notes: e.target.value || undefined })
            }
            placeholder={t(language, 'edgeEditor.notesOptional')}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        {/* 添加备份链路（edge.md 并行链路）：从当前（主）链路派生一条 role=backup 的并行边，
            由 store 的 addBackupEdge 复制 from/to/type/transport/endpoint_host（不复制端口与 pin）
            并自动选中。源/目标任一为 client 时隐藏（后端拒绝 client 上的备份），
            选中边本身已是备份时也隐藏（备份从主链路添加）。 */}
        {showAddBackupButton && (
          <button
            onClick={() => addBackupEdge(selectedEdge.id)}
            className="w-full py-1 bg-blue-600 hover:bg-blue-500 rounded text-sm"
          >
            + {t(language, 'addBackupLink')}
          </button>
        )}
        {showBackupEndpointNudge && (
          <p className="text-[10px] text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
            {t(language, 'backupEndpointNudge')}
          </p>
        )}
        {/* 已固定的分配（PR7）：编译器/服务端写回的 pin，现可由操作员手填——把内部监听端口与
            transit IP 钉到端口受限 NAT VPS 允许的范围内；Save 后持久化、下次编译/部署粘性沿用。
            link-local 仍只读（自动 fe80::）。下方校验为即时行内反馈，后端校验器（Validate/Compile/
            Deploy）才是权威闸门。参见 docs/spec/compiler/allocation-stability.md。 */}
        {hasAnyPin && (
          <div className="p-2 bg-gray-700/50 rounded space-y-2">
            <p className="text-xs text-gray-400 font-semibold">
              {t(language, 'edgeEditor.pinnedAllocation')}
            </p>
            {/* Directional NAT readout (info): which internal port the external→internal forward
                must target. Shown when the edge dials a host (endpoint_host). */}
            {hasPinnedPort && selectedEdge.endpoint_host && (
              <div className="space-y-0.5">
                <p className="text-xs text-cyan-300 font-mono break-all">
                  {t(language, 'edgeEditor.natForwardTitle')}: {selectedEdge.endpoint_host}:{natDialPort ?? '—'} → {natTargetNode?.name ? `${natTargetNode.name} ` : ''}{natTargetPort ?? '—'}
                </p>
                {natForwardActive && (
                  <p className="text-[10px] text-gray-400">{t(language, 'edgeEditor.natForwardHint')}</p>
                )}
              </div>
            )}
            {/* Editable listen ports (from → to). */}
            <div>
              <label className="text-xs text-gray-400">{t(language, 'edgeEditor.ports')}</label>
              <div className="flex items-center gap-1">
                <input
                  type="number"
                  value={selectedEdge.pinned_from_port ?? ''}
                  onChange={(e) => setPinPort('pinned_from_port', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinFrom')}
                  className="w-full px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500 focus:border-blue-400 outline-none"
                />
                <span className="text-gray-500">→</span>
                <input
                  type="number"
                  value={selectedEdge.pinned_to_port ?? ''}
                  onChange={(e) => setPinPort('pinned_to_port', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinTo')}
                  className="w-full px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500 focus:border-blue-400 outline-none"
                />
              </div>
              {portPairIncomplete && (
                <p className="text-[10px] text-yellow-400 mt-0.5">{t(language, 'edgeEditor.pinPairBoth')}</p>
              )}
              {portOutOfRange && (
                <p className="text-[10px] text-yellow-400 mt-0.5">
                  {t(language, 'edgeEditor.pinPortRange', { min: MIN_PINNED_PORT })}
                </p>
              )}
            </div>
            {/* Editable transit IPs (from → to), chosen from the edge's transit pool. */}
            <div>
              <label className="text-xs text-gray-400">{t(language, 'edgeEditor.transitIPs')}</label>
              <div className="flex items-center gap-1">
                <input
                  type="text"
                  value={selectedEdge.pinned_from_transit_ip ?? ''}
                  onChange={(e) => setPinTransit('pinned_from_transit_ip', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinFrom')}
                  className="w-full px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500 focus:border-blue-400 outline-none font-mono"
                />
                <span className="text-gray-500">→</span>
                <input
                  type="text"
                  value={selectedEdge.pinned_to_transit_ip ?? ''}
                  onChange={(e) => setPinTransit('pinned_to_transit_ip', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinTo')}
                  className="w-full px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500 focus:border-blue-400 outline-none font-mono"
                />
              </div>
              <p className="text-[10px] text-gray-500 mt-0.5">
                {t(language, 'edgeEditor.transitPoolPick', { cidr: edgeTransitCidr })}
              </p>
              {transitPairIncomplete && (
                <p className="text-[10px] text-yellow-400 mt-0.5">{t(language, 'edgeEditor.pinPairBoth')}</p>
              )}
              {transitOutOfPool && (
                <p className="text-[10px] text-yellow-400 mt-0.5">{t(language, 'edgeEditor.transitOutOfPool')}</p>
              )}
            </div>
            {/* Link-locals stay read-only (auto fe80::; manual editing is error-prone). */}
            {hasLinkLocalPin && (
              <p className="text-xs text-cyan-300 font-mono break-all">
                {t(language, 'edgeEditor.linkLocals')}: {selectedEdge.pinned_from_link_local ?? '—'} → {selectedEdge.pinned_to_link_local ?? '—'}
              </p>
            )}
            <button
              onClick={() =>
                updateEdge(selectedEdge.id, {
                  pinned_from_port: undefined,
                  pinned_to_port: undefined,
                  pinned_from_transit_ip: undefined,
                  pinned_to_transit_ip: undefined,
                  pinned_from_link_local: undefined,
                  pinned_to_link_local: undefined,
                  compiled_port: undefined,
                })
              }
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-xs"
            >
              {t(language, 'edgeEditor.unpinReallocateOnNext')}
            </button>
          </div>
        )}
        <button
          onClick={() => removeEdge(selectedEdge.id)}
          className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
        >
          {t(language, 'edgeEditor.deleteEdge')}
        </button>
      </div>
    </section>
  );
}
