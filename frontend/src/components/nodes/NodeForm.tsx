import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { deriveCapabilitiesFromRole, type NodeRole } from '../../lib/roleCapabilities';
import { uuid } from '../../lib/uuid';
import type { Node } from '../../types/topology';

const DEFAULT_LISTEN_PORT = 51820;

// UX-5：把顶层“公网地址”输入框（形如 IP:端口 或 域名:端口）解析为 host + port。
// - 仅当字符串恰好包含一个冒号、且冒号后是纯数字时，才把它当作 :端口 后缀拆出，
//   既支持 203.0.113.10:51820 / example.com:51820，又不会误伤裸 IPv6（多冒号）。
// - 端口缺省/非法时回落到 51820。
// - 入参为空（去空白后）时返回 null，表示该节点位于 NAT 之后（不写 public_endpoints）。
function parsePublicAddress(
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

export function NodeForm() {
  const { addNode, domains, language } = useTopologyStore();
  // fixed_private_key ("Pin private key") is a LOCAL/air-gap custody primitive — meaningless in
  // zero-knowledge controller mode (the agent holds the key). Gate the create-form control too,
  // mirroring the NodeEditor gate (plan-11 / T4 review): without this the create form is a
  // parallel reachable path that defeats the edit-form gate.
  const mode = useControllerStore((s) => s.mode);
  const [isOpen, setIsOpen] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState<NodeRole>('peer');
  const [domainId, setDomainId] = useState('');
  const [hostname, setHostname] = useState('');
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
      setError(t(language, 'nodeForm.nameIsRequired'));
      return;
    }
    const targetDomain = domainId || (domains.length > 0 ? domains[0].id : '');
    if (!targetDomain) {
      setError(t(language, 'nodeForm.pleaseCreateADomain'));
      return;
    }

    const id = `node-${uuid()}`;

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
        id: `${id}-ep-${uuid()}`,
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
          id: `${id}-ep-${uuid()}`,
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
      mtu: mtu > 0 ? mtu : undefined,
      capabilities,
      // Defense-in-depth: never write the local-only pin flag from controller mode, even if the
      // checkbox state somehow lingered (it is hidden there).
      fixed_private_key: mode === 'local' ? fixedPrivateKey : false,
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
        + {t(language, 'nodeForm.addNode')}
      </button>
    );
  }

  return (
    <div className="p-2 bg-gray-700 rounded space-y-2 mb-2">
      <input
        type="text"
        placeholder={t(language, 'nodeForm.nodeName')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      {/* UX-5：公网地址主入口。非空即派生 has_public_ip。client 角色无此概念，故隐藏。 */}
      {role !== 'client' && (
        <div className="space-y-1">
          <label className="block text-xs text-gray-300">
            {t(language, 'publicAddressLabel')}
          </label>
          <input
            type="text"
            placeholder={t(language, 'publicAddressPlaceholder')}
            value={publicAddress}
            onChange={(e) => setPublicAddress(e.target.value)}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
          {!publicAddress.trim() && (
            <p className="text-xs text-gray-400">
              {t(language, 'publicAddressHint')}
            </p>
          )}
        </div>
      )}
      <input
        type="text"
        placeholder={t(language, 'nodeForm.hostnameOptional')}
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
        min={576}
        max={65535}
        placeholder={t(language, 'nodeForm.mtuLeaveEmptyFor')}
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
          {t(language, 'nodeForm.advancedAddMorePublic')}
        </label>
      )}
      {mode === 'local' && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={fixedPrivateKey}
            onChange={(e) => setFixedPrivateKey(e.target.checked)}
            className="rounded"
          />
          {t(language, 'nodeForm.pinPrivateKeyPersist')}
        </label>
      )}
      {hasPublicIP && (
        <div className="space-y-2 p-2 bg-gray-800 rounded border border-gray-600">
          <p className="text-xs text-gray-300">{t(language, 'nodeForm.additionalPublicEndpointMapping')}</p>
          <input
            type="text"
            placeholder={t(language, 'nodeForm.publicIPOrDomain')}
            value={endpointHost}
            onChange={(e) => setEndpointHost(e.target.value)}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
          <input
            type="number"
            placeholder={t(language, 'nodeForm.port')}
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
        {t(language, 'nodeForm.canForwardTraffic')}
      </label>
      {error && <p className="text-xs text-red-400">{error}</p>}
      <div className="flex gap-2">
        <button
          onClick={handleSubmit}
          className="flex-1 py-1 bg-green-600 hover:bg-green-500 rounded text-sm"
        >
          {t(language, 'nodeForm.confirm')}
        </button>
        <button
          onClick={() => { setIsOpen(false); setError(''); }}
          className="flex-1 py-1 bg-gray-600 hover:bg-gray-500 rounded text-sm"
        >
          {t(language, 'nodeForm.cancel')}
        </button>
      </div>
    </div>
  );
}
