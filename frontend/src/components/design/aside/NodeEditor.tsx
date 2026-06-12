import { useTopologyStore } from '../../../stores/topologyStore';
import { txt, STRINGS } from '../../../i18n';
import { deriveCapabilitiesFromRole, type NodeRole } from '../../../lib/roleCapabilities';
import { uuid } from '../../../lib/uuid';

// 节点属性编辑器（从 RightPanel 的选中节点区块原样抽出，含公网地址 / 通告网段 / SSH 配置，
// 以及对 reconcileEdgeEndpoints 的耦合）。供选择驱动的 Design 右侧 aside 使用。
export function NodeEditor() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const domains = useTopologyStore((s) => s.domains);
  const selectedNodeId = useTopologyStore((s) => s.selectedNodeId);
  const updateNode = useTopologyStore((s) => s.updateNode);
  const removeNode = useTopologyStore((s) => s.removeNode);
  const reconcileEdgeEndpoints = useTopologyStore((s) => s.reconcileEdgeEndpoints);

  const selectedNode = nodes.find((n) => n.id === selectedNodeId);

  const updateNodeEndpoint = (
    nodeId: string,
    endpointId: string,
    updates: { host?: string; port?: number; note?: string },
  ) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const previous = (node.public_endpoints || []).find((ep) => ep.id === endpointId);
    updateNode(nodeId, {
      public_endpoints: (node.public_endpoints || []).map((ep) =>
        ep.id === endpointId ? { ...ep, ...updates } : ep,
      ),
    });
    // 主机被改名时，同步指向该节点、且快照了旧主机的 edge，避免拨向陈旧目标
    if (updates.host !== undefined && previous?.host && updates.host !== previous.host) {
      reconcileEdgeEndpoints(nodeId, previous.host, updates.host);
    }
  };

  const addNodeEndpoint = (nodeId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const newEndpoint = {
      id: `${nodeId}-ep-${uuid()}`,
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
      extra_prefixes: (node.extra_prefixes || []).map((prefix, i) => (i === index ? value : prefix)),
    });
  };

  const removeExtraPrefix = (nodeId: string, index: number) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      extra_prefixes: (node.extra_prefixes || []).filter((_, i) => i !== index),
    });
  };

  if (!selectedNode) return null;

  return (
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
        {/* mimic XDP 模式：仅当该节点有 transport=tcp 的链路时才起作用。默认 skb（通用，
            兼容不支持 native 的 VPS 网卡）；操作员确认网卡支持时可选 native 以提升性能。 */}
        <div>
          <label className="text-xs text-gray-400">{txt(language, ...STRINGS.xdpModeLabel)}</label>
          <select
            value={selectedNode.xdp_mode || 'skb'}
            onChange={(e) => updateNode(selectedNode.id, { xdp_mode: e.target.value === 'native' ? 'native' : undefined })}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="skb">{txt(language, 'skb（通用，默认）', 'skb (generic, default)')}</option>
            <option value="native">{txt(language, 'native（更快，需网卡支持）', 'native (faster, needs NIC support)')}</option>
          </select>
          <p className="mt-1 text-xs text-gray-500">{txt(language, ...STRINGS.xdpModeHint)}</p>
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
  );
}
