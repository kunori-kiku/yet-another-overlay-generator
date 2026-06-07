import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt, STRINGS } from '../../i18n';
import type { NodeCapabilities, Node } from '../../types/topology';

export type NodeRole = Node['role'];

const DEFAULT_LISTEN_PORT = 51820;

// UX-5：把顶层“公网地址”输入框（形如 IP:端口 或 域名:端口）解析为 host + port。
// - 仅当字符串恰好包含一个冒号、且冒号后是纯数字时，才把它当作 :端口 后缀拆出，
//   既支持 203.0.113.10:51820 / example.com:51820，又不会误伤裸 IPv6（多冒号）。
// - 端口缺省/非法时回落到 51820。
// - 入参为空（去空白后）时返回 null，表示该节点位于 NAT 之后（不写 public_endpoints）。
export function parsePublicAddress(
  raw: string
): { host: string; port: number } | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;

  const colonCount = (trimmed.match(/:/g) || []).length;
  if (colonCount === 1) {
    const [host, portStr] = trimmed.split(':');
    if (host && /^\d+$/.test(portStr)) {
      const port = parseInt(portStr, 10);
      if (port > 0 && port <= 65535) {
        return { host, port };
      }
    }
  }
  // 没有可识别的端口后缀（裸 host / IPv6 / 非法端口）：整串作为 host，端口取默认。
  return { host: trimmed, port: DEFAULT_LISTEN_PORT };
}

// 从角色推导 capabilities，与后端 roles.go 的 InferCapabilitiesFromRole 保持一致：
// router/relay/gateway 可转发；relay 额外接受入站并中继；client 全部为 false。
// 这样前端发送的 caps 不会与角色推断相矛盾（D69/D54）。保留操作员显式设置的 has_public_ip。
export function deriveCapabilitiesFromRole(role: NodeRole, hasPublicIP: boolean): NodeCapabilities {
  switch (role) {
    case 'router':
      return {
        can_forward: true,
        // router/gateway 在具备公网 IP 时接受入站连接（与后端 D49 一致）
        can_accept_inbound: hasPublicIP,
        can_relay: false,
        has_public_ip: hasPublicIP,
      };
    case 'gateway':
      return {
        can_forward: true,
        can_accept_inbound: hasPublicIP,
        can_relay: false,
        has_public_ip: hasPublicIP,
      };
    case 'relay':
      return {
        can_forward: true,
        can_accept_inbound: true,
        can_relay: true,
        has_public_ip: hasPublicIP,
      };
    case 'client':
      return {
        can_forward: false,
        can_accept_inbound: false,
        can_relay: false,
        has_public_ip: false,
      };
    default: // 'peer'
      return {
        can_forward: false,
        can_accept_inbound: hasPublicIP,
        can_relay: false,
        has_public_ip: hasPublicIP,
      };
  }
}

export function NodeForm() {
  const { addNode, domains, language } = useTopologyStore();
  const [isOpen, setIsOpen] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState<NodeRole>('peer');
  const [domainId, setDomainId] = useState('');
  const [hostname, setHostname] = useState('');
  const [listenPort, setListenPort] = useState(51820);
  // UX-5：顶层“公网地址”输入（主入口）。非空即派生 has_public_ip=true 并生成 public_endpoints[0]。
  const [publicAddress, setPublicAddress] = useState('');
  // 复选框现降为“高级”路径，揭示多端点（多组公网映射）编辑区；不再是公网可达的唯一开关。
  const [hasPublicIP, setHasPublicIP] = useState(false);
  const [mtu, setMtu] = useState(0);
  const [canForward, setCanForward] = useState(false);
  const [fixedPrivateKey, setFixedPrivateKey] = useState(false);
  const [endpointHost, setEndpointHost] = useState('');
  const [endpointPort, setEndpointPort] = useState(51820);
  const [error, setError] = useState('');

  const handleSubmit = () => {
    if (!name.trim()) {
      setError(txt(language, '名称不能为空', 'Name is required'));
      return;
    }
    const targetDomain = domainId || (domains.length > 0 ? domains[0].id : '');
    if (!targetDomain) {
      setError(txt(language, '请先创建一个网络域', 'Please create a domain first'));
      return;
    }

    const id = `node-${crypto.randomUUID()}`;

    // UX-5：顶层“公网地址”是公网可达的主入口。client 角色永不可达；其余角色只要
    // 顶层地址非空或勾选了高级复选框，即视为有公网 IP。
    const parsedPublic = role !== 'client' ? parsePublicAddress(publicAddress) : null;
    const effectiveHasPublicIP = role !== 'client' && (parsedPublic !== null || hasPublicIP);

    const capabilities = deriveCapabilitiesFromRole(role, effectiveHasPublicIP);
    // 保留操作员显式勾选的“可转发”（与后端保留显式置位 true 的行为一致）；
    // client 角色不允许转发，因此不叠加。
    if (canForward && role !== 'client') {
      capabilities.can_forward = true;
    }

    // 组装 public_endpoints：顶层地址（若有）作为 public_endpoints[0]；
    // 高级区（复选框揭示）作为附加的多端点编辑器 —— 仅在主机非空、且与顶层地址不重复时追加。
    const publicEndpoints: NonNullable<Node['public_endpoints']> = [];
    if (parsedPublic) {
      publicEndpoints.push({
        id: `${id}-ep-${crypto.randomUUID()}`,
        host: parsedPublic.host,
        port: parsedPublic.port,
      });
    }
    if (role !== 'client' && hasPublicIP && endpointHost.trim()) {
      const advHost = endpointHost.trim();
      const advPort = endpointPort || DEFAULT_LISTEN_PORT;
      const duplicate = publicEndpoints.some(
        (ep) => ep.host === advHost && ep.port === advPort
      );
      if (!duplicate) {
        publicEndpoints.push({
          id: `${id}-ep-${crypto.randomUUID()}`,
          host: advHost,
          port: advPort,
        });
      }
    }

    addNode({
      id,
      name: name.trim(),
      hostname: hostname.trim() || undefined,
      role,
      domain_id: targetDomain,
      listen_port: listenPort,
      mtu: mtu > 0 ? mtu : undefined,
      capabilities,
      fixed_private_key: fixedPrivateKey,
      public_endpoints: publicEndpoints,
    });

    setName('');
    setHostname('');
    setFixedPrivateKey(false);
    setPublicAddress('');
    setHasPublicIP(false);
    setEndpointHost('');
    setEndpointPort(51820);
    setError('');
    setIsOpen(false);
  };

  if (!isOpen) {
    return (
      <button
        onClick={() => setIsOpen(true)}
        className="w-full py-1.5 px-3 bg-blue-600 hover:bg-blue-500 rounded text-sm mb-2"
      >
        + {txt(language, '添加节点', 'Add Node')}
      </button>
    );
  }

  return (
    <div className="p-2 bg-gray-700 rounded space-y-2 mb-2">
      <input
        type="text"
        placeholder={txt(language, '节点名称', 'Node Name')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      {/* UX-5：公网地址主入口。非空即派生 has_public_ip。client 角色无此概念，故隐藏。 */}
      {role !== 'client' && (
        <div className="space-y-1">
          <label className="block text-xs text-gray-300">
            {txt(language, ...STRINGS.publicAddressLabel)}
          </label>
          <input
            type="text"
            placeholder={txt(language, ...STRINGS.publicAddressPlaceholder)}
            value={publicAddress}
            onChange={(e) => setPublicAddress(e.target.value)}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
          {!publicAddress.trim() && (
            <p className="text-xs text-gray-400">
              {txt(language, ...STRINGS.publicAddressHint)}
            </p>
          )}
        </div>
      )}
      <input
        type="text"
        placeholder={txt(language, '主机名 (可选)', 'Hostname (optional)')}
        value={hostname}
        onChange={(e) => setHostname(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <select
        value={domainId || (domains.length > 0 ? domains[0].id : '')}
        onChange={(e) => setDomainId(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
      >
        {domains.map((d) => (
          <option key={d.id} value={d.id}>
            {d.name} ({d.cidr})
          </option>
        ))}
      </select>
      <select
        value={role}
        onChange={(e) => setRole(e.target.value as NodeRole)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
      >
        <option value="peer">Peer</option>
        <option value="router">Router</option>
        <option value="relay">Relay</option>
        <option value="gateway">Gateway</option>
        <option value="client">Client</option>
      </select>
      <input
        type="number"
        placeholder={txt(language, '监听端口', 'Listen Port')}
        value={listenPort}
        onChange={(e) => setListenPort(parseInt(e.target.value) || 51820)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <input
        type="number"
        min={576}
        max={65535}
        placeholder={txt(language, 'MTU (留空使用默认值 1420)', 'MTU (leave empty for default 1420)')}
        value={mtu || ''}
        onChange={(e) => setMtu(parseInt(e.target.value) || 0)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      {/* UX-5：复选框降级为高级路径，揭示多端点（多组公网映射）编辑器。
          公网可达性本身已由上方“公网地址”输入派生，此处不再是唯一开关。
          client 角色无公网端点概念，故隐藏。 */}
      {role !== 'client' && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={hasPublicIP}
            onChange={(e) => setHasPublicIP(e.target.checked)}
            className="rounded"
          />
          {txt(language, '高级：添加更多公网端点映射', 'Advanced: add more public endpoint mappings')}
        </label>
      )}
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={fixedPrivateKey}
          onChange={(e) => setFixedPrivateKey(e.target.checked)}
          className="rounded"
        />
        {txt(language, '固定私钥（编译后持久化）', 'Pin private key (persist after compile)')}
      </label>
      {hasPublicIP && (
        <div className="space-y-2 p-2 bg-gray-800 rounded border border-gray-600">
          <p className="text-xs text-gray-300">{txt(language, '附加公网映射（与上方“公网地址”并存；可后续在右侧添加更多）', 'Additional public endpoint mapping (in addition to the address above; add more later in right panel)')}</p>
          <input
            type="text"
            placeholder={txt(language, '公网 IP 或域名', 'Public IP or domain')}
            value={endpointHost}
            onChange={(e) => setEndpointHost(e.target.value)}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
          <input
            type="number"
            placeholder={txt(language, '端口', 'Port')}
            value={endpointPort}
            onChange={(e) => setEndpointPort(parseInt(e.target.value, 10) || 51820)}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
      )}
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={canForward}
          onChange={(e) => setCanForward(e.target.checked)}
          className="rounded"
        />
        {txt(language, '可转发流量', 'Can Forward Traffic')}
      </label>
      {error && <p className="text-xs text-red-400">{error}</p>}
      <div className="flex gap-2">
        <button
          onClick={handleSubmit}
          className="flex-1 py-1 bg-green-600 hover:bg-green-500 rounded text-sm"
        >
          {txt(language, '确定', 'Confirm')}
        </button>
        <button
          onClick={() => { setIsOpen(false); setError(''); }}
          className="flex-1 py-1 bg-gray-600 hover:bg-gray-500 rounded text-sm"
        >
          {txt(language, '取消', 'Cancel')}
        </button>
      </div>
    </div>
  );
}
