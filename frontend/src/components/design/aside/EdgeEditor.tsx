import { useTopologyStore } from '../../../stores/topologyStore';
import { txt, STRINGS } from '../../../i18n';
import { resolveEdgeInterface } from '../../../lib/compiledInterfaces';

// 连接（边）属性编辑器（从 RightPanel 的选中边区块原样抽出，含目标端点选择 / 传输协议 /
// 链路角色 / 优先级 / 权重 / 备份链路 / 已固定分配 / 编译后实际值）。供 Design 右侧 aside 使用。
export function EdgeEditor() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
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

  return (
    <section>
      <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
        {txt(language, '连接属性', 'Edge Properties')}
      </h2>
      <div className="space-y-2">
        <div>
          <label className="text-xs text-gray-400">{txt(language, '类型', 'Type')}</label>
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
            <option value="direct">Direct</option>
            <option value="public-endpoint">Public Endpoint</option>
            <option value="relay-path">Relay Path</option>
            <option value="candidate">Candidate</option>
          </select>
        </div>
        {/* Endpoint IP — pick from target's public IPs or manual */}
        <div>
          <label className="text-xs text-gray-400">{txt(language, '目标 IP (从目标节点公网地址选择)', 'Endpoint IP (from target public IPs)')}</label>
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
              <option value="__none__">{txt(language, '不设置', 'Unset')}</option>
              {targetHostOptions.map((host) => (
                <option key={host} value={`host:${host}`}>
                  {host}
                </option>
              ))}
              <option value="__manual__">{txt(language, '手动输入', 'Manual input')}</option>
            </select>
          )}
          <input
            key={`ep-host-${selectedEdge.id}`}
            type="text"
            value={selectedEdge.endpoint_host || ''}
            onChange={(e) => updateEdge(selectedEdge.id, { endpoint_host: e.target.value || undefined, compiled_port: undefined })}
            placeholder={txt(language, 'IP 或域名', 'IP or hostname')}
            className="w-full mt-1 px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        {/* Endpoint Port — 0 or empty = auto, nonzero = NAT/port-forward override */}
        <div>
          <label className="text-xs text-gray-400">{txt(language, '目标端口 (0 = 自动分配, 非零 = NAT 覆盖)', 'Endpoint Port (0 = auto, nonzero = NAT override)')}</label>
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
              placeholder={txt(language, '0 = 自动', '0 = auto')}
              className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
            />
          </div>
          {compiledEdgePort && (
            <p className="text-[10px] text-cyan-400 mt-0.5 font-mono">
              {txt(language, '编译后端口', 'Compiled port')}: {compiledEdgePort}
              {selectedEdge.endpoint_port && selectedEdge.endpoint_port > 0 && selectedEdge.endpoint_port !== compiledEdgePort && (
                <span className="text-yellow-400 ml-1">
                  ({txt(language, 'NAT 覆盖生效', 'NAT override active')})
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
              <p className="text-xs text-gray-400 font-semibold">{txt(language, '编译后实际值', 'Compiled values')}</p>
              <p className="text-xs text-cyan-300 font-mono break-all">{txt(language, '本端接口', 'Local interface')}: {fromIface.interfaceName}</p>
              {endpointMatch && (
                <p className="text-xs text-cyan-300 font-mono break-all">{txt(language, '实际 Endpoint', 'Endpoint')}: {endpointMatch[1]}</p>
              )}
              <p className="text-xs text-cyan-300 font-mono">{txt(language, '本端 ListenPort', 'Local ListenPort')}: {fromIface.listenPort}</p>
            </div>
          );
        })()}
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={selectedEdge.is_enabled}
            onChange={(e) => updateEdge(selectedEdge.id, { is_enabled: e.target.checked })}
          />
          {txt(language, '启用', 'Enabled')}
        </label>
        {/* 传输协议 / 优先级 / 权重 / 备注（D68）。priority 与 weight 影响 Babel 的链路开销。 */}
        <div>
          <label className="text-xs text-gray-400">{txt(language, '传输协议', 'Transport')}</label>
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
            <option value="tcp">{txt(language, 'TCP（mimic）', 'TCP (mimic)')}</option>
          </select>
          {selectedEdge.transport === 'tcp' && (
            <p className="mt-1 text-xs text-gray-400">
              {txt(language, ...STRINGS.mimicHint)}
            </p>
          )}
        </div>
        {/* 链路角色（edge.md 并行链路）：空 = 主链路类；backup = 独立的备份链路。
            角色变更会改变链路身份（重新 key），属拨号相关编辑 ⇒ 与本文件其他拨号相关编辑一致，
            一并清空陈旧的 compiled_port。 */}
        <div>
          <label className="text-xs text-gray-400">{txt(language, ...STRINGS.roleLabel)}</label>
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
            <option value="">{txt(language, ...STRINGS.rolePrimary)} ({txt(language, '默认', 'default')})</option>
            <option value="primary">{txt(language, ...STRINGS.rolePrimary)}</option>
            <option value="backup">{txt(language, ...STRINGS.roleBackup)}</option>
          </select>
          <p className="text-[10px] text-gray-500 mt-0.5">
            {txt(
              language,
              '备份链路默认开销 384（高于主链路），仅在主链路失效时切换；显式设置的优先级/权重会覆盖此默认值。',
              'Backup links default to cost 384 (higher than primary), used only when the primary fails; an explicit priority/weight overrides this default.',
            )}
          </p>
        </div>
        <div>
          <label className="text-xs text-gray-400">
            {txt(language, '优先级 (影响 Babel 链路开销)', 'Priority (drives Babel link cost)')}
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
            placeholder={txt(language, '默认', 'default')}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">
            {txt(language, '权重 (影响 Babel 链路开销)', 'Weight (drives Babel link cost)')}
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
            placeholder={txt(language, '默认', 'default')}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">{txt(language, '备注', 'Notes')}</label>
          <input
            type="text"
            value={selectedEdge.notes || ''}
            onChange={(e) =>
              updateEdge(selectedEdge.id, { notes: e.target.value || undefined })
            }
            placeholder={txt(language, '备注 (可选)', 'Notes (optional)')}
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
            + {txt(language, ...STRINGS.addBackupLink)}
          </button>
        )}
        {showBackupEndpointNudge && (
          <p className="text-[10px] text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
            {txt(language, ...STRINGS.backupEndpointNudge)}
          </p>
        )}
        {/* 已固定的分配：编译器写回的 pin 值（端口 / transit IP / 链路本地地址）。
            只读展示；操作员可显式解除固定以便下次编译重新分配。
            参见 docs/spec/compiler/allocation-stability.md。 */}
        {(selectedEdge.pinned_from_port !== undefined ||
          selectedEdge.pinned_to_port !== undefined ||
          selectedEdge.pinned_from_transit_ip !== undefined ||
          selectedEdge.pinned_to_transit_ip !== undefined ||
          selectedEdge.pinned_from_link_local !== undefined ||
          selectedEdge.pinned_to_link_local !== undefined) && (
          <div className="p-2 bg-gray-700/50 rounded space-y-1">
            <p className="text-xs text-gray-400 font-semibold">
              {txt(language, '已固定的分配', 'Pinned allocation')}
            </p>
            {(selectedEdge.pinned_from_port !== undefined ||
              selectedEdge.pinned_to_port !== undefined) && (
              <p className="text-xs text-cyan-300 font-mono">
                {txt(language, '端口', 'Ports')}: {selectedEdge.pinned_from_port ?? '—'} → {selectedEdge.pinned_to_port ?? '—'}
              </p>
            )}
            {(selectedEdge.pinned_from_transit_ip !== undefined ||
              selectedEdge.pinned_to_transit_ip !== undefined) && (
              <p className="text-xs text-cyan-300 font-mono break-all">
                {txt(language, 'Transit IP', 'Transit IPs')}: {selectedEdge.pinned_from_transit_ip ?? '—'} → {selectedEdge.pinned_to_transit_ip ?? '—'}
              </p>
            )}
            {(selectedEdge.pinned_from_link_local !== undefined ||
              selectedEdge.pinned_to_link_local !== undefined) && (
              <p className="text-xs text-cyan-300 font-mono break-all">
                {txt(language, '链路本地地址', 'Link-locals')}: {selectedEdge.pinned_from_link_local ?? '—'} → {selectedEdge.pinned_to_link_local ?? '—'}
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
              {txt(language, '解除固定（下次编译重新分配）', 'Unpin - reallocate on next compile')}
            </button>
          </div>
        )}
        <button
          onClick={() => removeEdge(selectedEdge.id)}
          className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
        >
          {txt(language, '删除连接', 'Delete Edge')}
        </button>
      </div>
    </section>
  );
}
