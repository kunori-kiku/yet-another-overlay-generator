import { useTopologyStore } from '../../stores/topologyStore';
import { txt, STRINGS } from '../../i18n';
import { deriveCapabilitiesFromRole, type NodeRole } from '../../lib/roleCapabilities';
import { resolveEdgeInterface } from '../../lib/compiledInterfaces';

function previewText(content: string | undefined, maxLines = 4, maxChars = 220): string {
  if (!content) return 'N/A';
  const lines = content.split('\n').slice(0, maxLines).join('\n');
  if (lines.length > maxChars) {
    return `${lines.slice(0, maxChars)}...`;
  }
  return lines;
}

export function RightPanel() {
  const {
    selectedDomainId,
    selectedNodeId,
    selectedEdgeId,
    nodes,
    edges,
    domains,
    updateDomain,
    removeDomain,
    updateNode,
    removeNode,
    updateEdge,
    removeEdge,
    addBackupEdge,
    reconcileEdgeEndpoints,
    compileResult,
    compile,
    exportArtifacts,
    downloadDeployScript,
    isCompiling,
    language,
  } = useTopologyStore();

  const selectedDomain = domains.find((d) => d.id === selectedDomainId);
  const selectedNode = nodes.find((n) => n.id === selectedNodeId);
  const selectedEdge = edges.find((e) => e.id === selectedEdgeId);
  const selectedEdgeTarget = selectedEdge
    ? nodes.find((n) => n.id === selectedEdge.to_node_id)
    : undefined;

  const targetEndpointOptions = selectedEdgeTarget?.public_endpoints || [];
  // Deduplicate hosts from target's public endpoints for IP picker
  const targetHostOptions = Array.from(
    new Set(targetEndpointOptions.map((ep) => ep.host).filter(Boolean))
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

  const updateNodeEndpoint = (
    nodeId: string,
    endpointId: string,
    updates: { host?: string; port?: number; note?: string }
  ) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const previous = (node.public_endpoints || []).find((ep) => ep.id === endpointId);
    updateNode(nodeId, {
      public_endpoints: (node.public_endpoints || []).map((ep) =>
        ep.id === endpointId ? { ...ep, ...updates } : ep
      ),
    });
    // 主机被改名时，同步指向该节点、且快照了旧主机的 edge，避免拨向陈旧目标
    if (
      updates.host !== undefined &&
      previous?.host &&
      updates.host !== previous.host
    ) {
      reconcileEdgeEndpoints(nodeId, previous.host, updates.host);
    }
  };

  const addNodeEndpoint = (nodeId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const newEndpoint = {
      id: `${nodeId}-ep-${crypto.randomUUID()}`,
      host: '',
      port: node.listen_port || 51820,
      note: '',
    };
    updateNode(nodeId, {
      public_endpoints: [...(node.public_endpoints || []), newEndpoint],
    });
  };

  const removeNodeEndpoint = (nodeId: string, endpointId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const removed = (node.public_endpoints || []).find((ep) => ep.id === endpointId);
    updateNode(nodeId, {
      public_endpoints: (node.public_endpoints || []).filter((ep) => ep.id !== endpointId),
    });
    // 该主机被移除时，清空指向它的 edge 的 endpoint，让连接退回后端自动解析
    if (removed?.host) {
      reconcileEdgeEndpoints(nodeId, removed.host, null);
    }
  };

  // extra_prefixes 是纯字符串数组（无稳定 id），因此按下标增删改，
  // 与上面的公网地址列表（对象数组、按 id 操作）形成对照。
  const addExtraPrefix = (nodeId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      extra_prefixes: [...(node.extra_prefixes || []), ''],
    });
  };

  const updateExtraPrefix = (nodeId: string, index: number, value: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      extra_prefixes: (node.extra_prefixes || []).map((prefix, i) =>
        i === index ? value : prefix
      ),
    });
  };

  const removeExtraPrefix = (nodeId: string, index: number) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      extra_prefixes: (node.extra_prefixes || []).filter((_, i) => i !== index),
    });
  };

  return (
    <div className="p-3 space-y-4">
      {/* 操作按钮 */}
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {txt(language, '操作', 'Actions')}
        </h2>
        <div className="space-y-2">
          <button
            onClick={() => compile()}
            disabled={isCompiling || nodes.length === 0}
            className="w-full py-1.5 bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {isCompiling ? txt(language, '编译中...', 'Compiling...') : txt(language, '🔨 编译', '🔨 Compile')}
          </button>
          <button
            onClick={() => exportArtifacts()}
            disabled={nodes.length === 0}
            className="w-full py-1.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {txt(language, '📦 导出产物包', '📦 Export Artifacts')}
          </button>
          <div className="flex gap-1">
            <button
              onClick={() => downloadDeployScript('sh')}
              disabled={nodes.length === 0}
              className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
            >
              {txt(language, '🚀 部署脚本 .sh', '🚀 Deploy .sh')}
            </button>
            <button
              onClick={() => downloadDeployScript('ps1')}
              disabled={nodes.length === 0}
              className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
            >
              {txt(language, '🚀 部署脚本 .ps1', '🚀 Deploy .ps1')}
            </button>
          </div>
        </div>
      </section>

      <hr className="border-gray-700" />

      {/* 选中域属性 */}
      {selectedDomain && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            {txt(language, '网络域属性', 'Domain Properties')}
          </h2>
          <div className="space-y-2">
            <div>
              <label className="text-xs text-gray-400">{txt(language, '名称', 'Name')}</label>
              <input
                type="text"
                value={selectedDomain.name}
                onChange={(e) => updateDomain(selectedDomain.id, { name: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">CIDR</label>
              <input
                type="text"
                value={selectedDomain.cidr}
                onChange={(e) => updateDomain(selectedDomain.id, { cidr: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, 'Transit CIDR (可选)', 'Transit CIDR (optional)')}</label>
              <input
                type="text"
                value={selectedDomain.transit_cidr || ''}
                onChange={(e) =>
                  updateDomain(selectedDomain.id, {
                    transit_cidr: e.target.value.trim() || undefined,
                  })
                }
                pattern="^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$"
                title={txt(
                  language,
                  'IPv4 CIDR 格式，例: 10.10.0.0/24；留空使用默认 10.10.0.0/24',
                  'IPv4 CIDR format, e.g. 10.10.0.0/24; empty uses default 10.10.0.0/24',
                )}
                placeholder="10.10.0.0/24"
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '路由模式', 'Routing Mode')}</label>
              <select
                value={selectedDomain.routing_mode}
                onChange={(e) =>
                  updateDomain(selectedDomain.id, {
                    routing_mode: e.target.value as 'babel' | 'static' | 'none',
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="babel">Babel</option>
                <option value="static">Static</option>
                <option value="none">None</option>
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '分配模式', 'Allocation Mode')}</label>
              <select
                value={selectedDomain.allocation_mode}
                onChange={(e) =>
                  updateDomain(selectedDomain.id, {
                    allocation_mode: e.target.value as 'auto' | 'manual',
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="auto">Auto</option>
                <option value="manual">Manual</option>
              </select>
            </div>
            <button
              onClick={() => removeDomain(selectedDomain.id)}
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
            >
              {txt(language, '删除网络域', 'Delete Domain')}
            </button>
          </div>
        </section>
      )}

      {/* 选中节点属性 */}
      {selectedNode && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            {txt(language, '节点属性', 'Node Properties')}
          </h2>
          <div className="space-y-2">
            <div>
              <label className="text-xs text-gray-400">{txt(language, '名称', 'Name')}</label>
              <input
                type="text"
                value={selectedNode.name}
                onChange={(e) => updateNode(selectedNode.id, { name: e.target.value })}
                pattern="^[A-Za-z0-9 ._-]+$"
                title={txt(
                  language,
                  '仅允许字母、数字、空格、点(.)、下划线(_)、连字符(-)，禁止引号、反引号、$、; 等 shell 元字符',
                  'Only letters, digits, space, . _ - are allowed; no quotes, backticks, $, ; or other shell metacharacters',
                )}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '主机名 (可选)', 'Hostname (optional)')}</label>
              <input
                type="text"
                value={selectedNode.hostname || ''}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    hostname: e.target.value || undefined,
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '角色', 'Role')}</label>
              <select
                value={selectedNode.role}
                onChange={(e) => {
                  const newRole = e.target.value as NodeRole;
                  // 每次角色变更都重新推导 can_forward/can_relay/can_accept_inbound（D54），
                  // 与 NodeForm/roles.go 的推断保持一致；client 角色强制 has_public_ip=false，
                  // 其余角色保留操作员已设置的 has_public_ip。
                  const operatorHasPublicIP =
                    newRole === 'client' ? false : selectedNode.capabilities.has_public_ip;
                  updateNode(selectedNode.id, {
                    role: newRole,
                    capabilities: deriveCapabilitiesFromRole(newRole, operatorHasPublicIP),
                  });
                }}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="peer">Peer</option>
                <option value="router">Router</option>
                <option value="relay">Relay</option>
                <option value="gateway">Gateway</option>
                <option value="client">Client</option>
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '网络域', 'Domain')}</label>
              <select
                value={selectedNode.domain_id}
                onChange={(e) => updateNode(selectedNode.id, { domain_id: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                {domains.map((d) => (
                  <option key={d.id} value={d.id}>{d.name}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, 'Overlay IP (留空自动分配)', 'Overlay IP (empty for auto)')}</label>
              <input
                type="text"
                value={selectedNode.overlay_ip || ''}
                onChange={(e) => updateNode(selectedNode.id, { overlay_ip: e.target.value || undefined })}
                placeholder={txt(language, '自动分配', 'Auto assigned')}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '监听端口', 'Listen Port')}</label>
              <input
                type="number"
                value={selectedNode.listen_port || 51820}
                onChange={(e) => updateNode(selectedNode.id, { listen_port: parseInt(e.target.value) || 51820 })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, 'MTU (留空使用默认值 1420)', 'MTU (empty for default 1420)')}</label>
              <input
                type="number"
                min={576}
                max={65535}
                value={selectedNode.mtu || ''}
                onChange={(e) => updateNode(selectedNode.id, { mtu: parseInt(e.target.value) || undefined })}
                placeholder="1420"
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            {selectedNode.role !== 'client' && (
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedNode.capabilities.has_public_ip}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    capabilities: {
                      ...selectedNode.capabilities,
                      has_public_ip: e.target.checked,
                      can_accept_inbound: e.target.checked,
                    },
                  })
                }
              />
              {txt(language, '公网可达', 'Publicly Reachable')}
            </label>
            )}
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedNode.fixed_private_key || false}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    fixed_private_key: e.target.checked,
                    ...(e.target.checked
                      ? {}
                      : {
                          wireguard_private_key: undefined,
                          wireguard_public_key: undefined,
                        }),
                  })
                }
              />
              {txt(language, '固定私钥（编译后持久化）', 'Pin private key (persist after compile)')}
            </label>
            {selectedNode.fixed_private_key && (
              <div className="p-2 bg-gray-700 rounded space-y-1">
                <p className="text-xs text-gray-300">{txt(language, '固定密钥状态', 'Pinned key status')}</p>
                <p className="text-xs text-gray-400 break-all">
                  {txt(language, '公钥', 'Public key')}: {selectedNode.wireguard_public_key || txt(language, '将在下次编译后生成', 'Will be generated on next compile')}
                </p>
                <p className="text-xs text-gray-500 break-all">
                  {txt(language, '私钥', 'Private key')}: {selectedNode.wireguard_private_key ? txt(language, '已生成并持久化', 'Generated and persisted') : txt(language, '尚未生成', 'Not generated yet')}
                </p>
              </div>
            )}
            {selectedNode.role !== 'client' && (
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedNode.capabilities.can_forward}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    capabilities: {
                      ...selectedNode.capabilities,
                      can_forward: e.target.checked,
                    },
                  })
                }
              />
              {txt(language, '可转发流量', 'Can Forward Traffic')}
            </label>
            )}
            {selectedNode.role !== 'client' && (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label className="text-xs text-gray-400">{txt(language, '公网地址（其他节点如何访问）', 'Public addresses (how peers reach this node)')}</label>
                <button
                  onClick={() => addNodeEndpoint(selectedNode.id)}
                  className="text-xs px-2 py-0.5 rounded bg-blue-600 hover:bg-blue-500"
                >
                  + {txt(language, '添加', 'Add')}
                </button>
              </div>
              {(selectedNode.public_endpoints || []).length === 0 && (
                <p className="text-xs text-gray-500 italic">{txt(language, '尚未配置公网地址', 'No public addresses configured')}</p>
              )}
              {(selectedNode.public_endpoints || []).map((ep) => (
                <div key={ep.id} className="p-2 bg-gray-700 rounded space-y-1">
                  <div className="grid grid-cols-3 gap-1">
                    <input
                      type="text"
                      value={ep.host}
                      onChange={(e) =>
                        updateNodeEndpoint(selectedNode.id, ep.id, { host: e.target.value })
                      }
                      placeholder={txt(language, 'IP/域名', 'IP/Domain')}
                      className="col-span-2 px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500"
                    />
                    <input
                      type="number"
                      value={ep.port}
                      onChange={(e) =>
                        updateNodeEndpoint(selectedNode.id, ep.id, {
                          port: parseInt(e.target.value, 10) || 51820,
                        })
                      }
                      placeholder={txt(language, '端口', 'Port')}
                      className="px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500"
                    />
                  </div>
                  <div className="flex gap-1">
                    <input
                      type="text"
                      value={ep.note || ''}
                      onChange={(e) =>
                        updateNodeEndpoint(selectedNode.id, ep.id, { note: e.target.value })
                      }
                      placeholder={txt(language, '备注 (可选)', 'Note (optional)')}
                      className="flex-1 px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500"
                    />
                    <button
                      onClick={() => removeNodeEndpoint(selectedNode.id, ep.id)}
                      className="px-2 py-1 text-xs bg-red-600 hover:bg-red-500 rounded"
                    >
                      {txt(language, '删除', 'Delete')}
                    </button>
                  </div>
                </div>
              ))}
            </div>
            )}
            {/* 对外通告的局域网网段（extra_prefixes）。角色门控严格对应 roles.go 的
                BabelAnnounce.AnnounceExtraPrefixes：gateway 恒为 true（始终展示，无提示）；
                router/relay 在设置后才通告（展示并附提示）；peer/client 为 no-op（不展示）。 */}
            {(selectedNode.role === 'gateway' ||
              selectedNode.role === 'router' ||
              selectedNode.role === 'relay') && (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label className="text-xs text-gray-400">{txt(language, '对外通告的局域网网段', 'Advertised LAN prefixes')}</label>
                <button
                  onClick={() => addExtraPrefix(selectedNode.id)}
                  className="text-xs px-2 py-0.5 rounded bg-blue-600 hover:bg-blue-500"
                >
                  + {txt(language, '添加', 'Add')}
                </button>
              </div>
              {(selectedNode.role === 'router' || selectedNode.role === 'relay') && (
                <p className="text-[10px] text-gray-500">
                  {txt(
                    language,
                    '设置后该节点会通告这些网段（未设置则不通告）',
                    'When set, this node announces these prefixes (no-op if left empty)',
                  )}
                </p>
              )}
              {(selectedNode.extra_prefixes || []).length === 0 && (
                <p className="text-xs text-gray-500 italic">{txt(language, '尚未配置局域网网段', 'No LAN prefixes configured')}</p>
              )}
              {(selectedNode.extra_prefixes || []).map((prefix, index) => (
                <div key={`extra-prefix-${index}`} className="flex gap-1">
                  <input
                    type="text"
                    value={prefix}
                    onChange={(e) => updateExtraPrefix(selectedNode.id, index, e.target.value)}
                    pattern="^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$"
                    title={txt(
                      language,
                      'IPv4 CIDR 格式，例: 192.168.1.0/24',
                      'IPv4 CIDR format, e.g. 192.168.1.0/24',
                    )}
                    placeholder="192.168.1.0/24"
                    className="flex-1 px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500 focus:border-blue-400 outline-none"
                  />
                  <button
                    onClick={() => removeExtraPrefix(selectedNode.id, index)}
                    className="px-2 py-1 text-xs bg-red-600 hover:bg-red-500 rounded"
                  >
                    {txt(language, '删除', 'Delete')}
                  </button>
                </div>
              ))}
            </div>
            )}
            {/* SSH Connection Details (collapsible) */}
            <details className="bg-gray-700/50 rounded p-2">
              <summary className="text-xs cursor-pointer text-gray-400 font-semibold">
                {txt(language, 'SSH 连接配置 (自动部署)', 'SSH Connection (Auto-Deploy)')}
              </summary>
              <div className="mt-2 space-y-2">
                <div>
                  <label className="text-xs text-gray-400">{txt(language, 'SSH 别名 (ssh_config Host)', 'SSH Alias (ssh_config Host)')}</label>
                  <input
                    type="text"
                    value={selectedNode.ssh_alias || ''}
                    onChange={(e) =>
                      updateNode(selectedNode.id, {
                        ssh_alias: e.target.value || undefined,
                      })
                    }
                    pattern="^[A-Za-z0-9._:@-]+$"
                    title={txt(
                      language,
                      '仅允许字母、数字、点(.)、下划线(_)、冒号(:)、@、连字符(-)，禁止空白与 shell 元字符',
                      'Only letters, digits, . _ : @ - are allowed; no whitespace or shell metacharacters',
                    )}
                    placeholder={txt(language, '如 my-server', 'e.g. my-server')}
                    className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                  />
                  <p className="text-[10px] text-gray-500 mt-0.5">
                    {txt(language, '设置别名后将忽略下方手动配置', 'If set, overrides manual host/port/user/key below')}
                  </p>
                </div>
                <div>
                  <label className="text-xs text-gray-400">{txt(language, 'SSH 主机', 'SSH Host')}</label>
                  <input
                    type="text"
                    value={selectedNode.ssh_host || ''}
                    onChange={(e) =>
                      updateNode(selectedNode.id, {
                        ssh_host: e.target.value || undefined,
                      })
                    }
                    pattern="^[A-Za-z0-9._:@-]+$"
                    title={txt(
                      language,
                      '仅允许字母、数字、点(.)、下划线(_)、冒号(:)、@、连字符(-)，禁止空白与 shell 元字符',
                      'Only letters, digits, . _ : @ - are allowed; no whitespace or shell metacharacters',
                    )}
                    placeholder={txt(language, 'IP 或域名', 'IP or hostname')}
                    className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                  />
                </div>
                <div className="grid grid-cols-2 gap-2">
                  <div>
                    <label className="text-xs text-gray-400">{txt(language, 'SSH 端口', 'SSH Port')}</label>
                    <input
                      type="number"
                      min={1}
                      max={65535}
                      value={selectedNode.ssh_port || ''}
                      onChange={(e) =>
                        updateNode(selectedNode.id, {
                          ssh_port: parseInt(e.target.value) || undefined,
                        })
                      }
                      placeholder="22"
                      className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                    />
                  </div>
                  <div>
                    <label className="text-xs text-gray-400">{txt(language, 'SSH 用户', 'SSH User')}</label>
                    <input
                      type="text"
                      value={selectedNode.ssh_user || ''}
                      onChange={(e) =>
                        updateNode(selectedNode.id, {
                          ssh_user: e.target.value || undefined,
                        })
                      }
                      pattern="^[A-Za-z0-9._:@-]+$"
                      title={txt(
                        language,
                        '仅允许字母、数字、点(.)、下划线(_)、冒号(:)、@、连字符(-)，禁止空白与 shell 元字符',
                        'Only letters, digits, . _ : @ - are allowed; no whitespace or shell metacharacters',
                      )}
                      placeholder="root"
                      className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                    />
                  </div>
                </div>
                <div>
                  <label className="text-xs text-gray-400">{txt(language, 'SSH 密钥路径', 'SSH Key Path')}</label>
                  <input
                    type="text"
                    value={selectedNode.ssh_key_path || ''}
                    onChange={(e) =>
                      updateNode(selectedNode.id, {
                        ssh_key_path: e.target.value || undefined,
                      })
                    }
                    placeholder={txt(language, '如 ~/.ssh/id_ed25519', 'e.g. ~/.ssh/id_ed25519')}
                    className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                  />
                </div>
              </div>
            </details>
            <button
              onClick={() => removeNode(selectedNode.id)}
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
            >
              {txt(language, '删除节点', 'Delete Node')}
            </button>
          </div>
        </section>
      )}

      {/* 选中边属性 */}
      {selectedEdge && (
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
                <option value="tcp">TCP</option>
              </select>
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
      )}

      {/* 配置预览 */}
      {compileResult && !selectedDomain && !selectedNode && !selectedEdge && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            {txt(language, '编译结果', 'Compile Result')}
          </h2>
          <div className="text-xs text-gray-300 space-y-1">
            <p>{txt(language, '节点数', 'Node count')}: {compileResult.manifest.node_count}</p>
            <p>Checksum: {compileResult.manifest.checksum}</p>
            <p>{txt(language, '编译时间', 'Compiled at')}: {compileResult.manifest.compiled_at}</p>
          </div>
          {/* 编译告警：语义校验产生的非致命提示（双重 NAT、缺少端点的边、孤立节点等），
              在编译成功后展示，避免操作员在一个“绿色”编译上发布事实上不可达的覆盖网络。 */}
          {compileResult.warnings && compileResult.warnings.length > 0 && (
            <div className="mt-2 space-y-1">
              <h3 className="text-xs font-semibold text-yellow-400 uppercase tracking-wider">
                {txt(language, '编译告警', 'Compile Warnings')}
              </h3>
              {compileResult.warnings.map((w, i) => (
                <div
                  key={`compile-warn-${i}`}
                  className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded"
                >
                  ⚠️ [{w.field}] {w.message}
                </div>
              ))}
            </div>
          )}
          <div className="mt-2 space-y-2">
            {compileResult.topology.nodes.map((n) => (
              <details key={n.id} className="bg-gray-700 rounded p-2">
                <summary className="text-sm cursor-pointer text-blue-300">
                  {n.name} ({n.overlay_ip})
                </summary>

                <div className="mt-2 space-y-2">
                  {/* WireGuard per-peer interface configs */}
                  {Object.entries(compileResult.wireguard_configs)
                    .filter(([key]) => key.startsWith(n.id + ':'))
                    .map(([key, config]) => {
                      const interfaceName = key.split(':').slice(1).join(':');
                      const portMatch = config?.match(/ListenPort\s*=\s*(\d+)/);
                      const portLabel = portMatch ? ` (port: ${portMatch[1]})` : '';
                      return (
                        <details key={key} className="bg-gray-800/70 rounded p-2">
                          <summary className="text-xs cursor-pointer text-cyan-300">
                            wireguard/{interfaceName}.conf{portLabel}
                          </summary>
                          <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                            {txt(language, '预览', 'Preview')}:{'\n'}{previewText(config)}
                          </pre>
                          <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                            {config || txt(language, '无内容', 'No content')}
                          </pre>
                        </details>
                      );
                    })}
                  {Object.keys(compileResult.wireguard_configs)
                    .filter((key) => key.startsWith(n.id + ':')).length === 0 && (
                    <details className="bg-gray-800/70 rounded p-2">
                      <summary className="text-xs cursor-pointer text-cyan-300 text-gray-500">
                        wireguard/ ({txt(language, '无配置', 'No configs')})
                      </summary>
                    </details>
                  )}

                  <details className="bg-gray-800/70 rounded p-2">
                    <summary className="text-xs cursor-pointer text-amber-300">
                      babel/babeld.conf
                    </summary>
                    <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                      {txt(language, '预览', 'Preview')}:{'\n'}{previewText(compileResult.babel_configs[n.id])}
                    </pre>
                    <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                      {compileResult.babel_configs[n.id] || txt(language, '无内容', 'No content')}
                    </pre>
                  </details>

                  <details className="bg-gray-800/70 rounded p-2">
                    <summary className="text-xs cursor-pointer text-lime-300">
                      sysctl/99-overlay.conf
                    </summary>
                    <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                      {txt(language, '预览', 'Preview')}:{'\n'}{previewText(compileResult.sysctl_configs[n.id])}
                    </pre>
                    <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                      {compileResult.sysctl_configs[n.id] || txt(language, '无内容', 'No content')}
                    </pre>
                  </details>

                  <details className="bg-gray-800/70 rounded p-2">
                    <summary className="text-xs cursor-pointer text-fuchsia-300">
                      scripts/install.sh
                    </summary>
                    <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                      {txt(language, '预览', 'Preview')}:{'\n'}{previewText(compileResult.install_scripts[n.id])}
                    </pre>
                    <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                      {compileResult.install_scripts[n.id] || txt(language, '无内容', 'No content')}
                    </pre>
                  </details>
                </div>
              </details>
            ))}
          </div>
          {/* Deploy Scripts (project-wide) */}
          {compileResult.deploy_scripts && Object.keys(compileResult.deploy_scripts).length > 0 && (
            <div className="mt-3 space-y-2">
              <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider">
                {txt(language, '自动部署脚本', 'Auto-Deploy Scripts')}
              </h3>
              {Object.entries(compileResult.deploy_scripts).map(([name, script]) => (
                <details key={name} className="bg-gray-700 rounded p-2">
                  <summary className="text-sm cursor-pointer text-orange-300">
                    {name}
                  </summary>
                  <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                    {script}
                  </pre>
                </details>
              ))}
            </div>
          )}
        </section>
      )}

      {/* 无选中时提示 */}
      {!selectedDomain && !selectedNode && !selectedEdge && !compileResult && (
        <p className="text-xs text-gray-500 italic">
          {txt(language, '点击左侧列表或画布元素查看并编辑属性', 'Click items in the left list or canvas to view/edit properties')}
        </p>
      )}
    </div>
  );
}
