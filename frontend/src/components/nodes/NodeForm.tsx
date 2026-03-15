import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

export function NodeForm() {
  const { addNode, domains, language } = useTopologyStore();
  const [isOpen, setIsOpen] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState<'peer' | 'router' | 'relay' | 'gateway'>('peer');
  const [domainId, setDomainId] = useState('');
  const [hostname, setHostname] = useState('');
  const [listenPort, setListenPort] = useState(51820);
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

    const id = `node-${Date.now()}`;
    addNode({
      id,
      name: name.trim(),
      hostname: hostname.trim() || undefined,
      role,
      domain_id: targetDomain,
      listen_port: listenPort,
      mtu: mtu > 0 ? mtu : undefined,
      capabilities: {
        can_accept_inbound: hasPublicIP,
        can_forward: canForward || role === 'router' || role === 'relay' || role === 'gateway',
        can_relay: role === 'relay',
        has_public_ip: hasPublicIP,
      },
      fixed_private_key: fixedPrivateKey,
      public_endpoints:
        hasPublicIP && endpointHost.trim()
          ? [{ id: `${id}-ep-1`, host: endpointHost.trim(), port: endpointPort || 51820 }]
          : [],
    });

    setName('');
    setHostname('');
    setFixedPrivateKey(false);
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
        disabled={domains.length === 0}
        title={domains.length === 0 ? txt(language, '请先创建网络域', 'Please create a domain first') : ''}
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
        onChange={(e) => setRole(e.target.value as typeof role)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
      >
        <option value="peer">Peer</option>
        <option value="router">Router</option>
        <option value="relay">Relay</option>
        <option value="gateway">Gateway</option>
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
        placeholder={txt(language, 'MTU (留空使用默认值 1420)', 'MTU (leave empty for default 1420)')}
        value={mtu || ''}
        onChange={(e) => setMtu(parseInt(e.target.value) || 0)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={hasPublicIP}
          onChange={(e) => setHasPublicIP(e.target.checked)}
          className="rounded"
        />
        {txt(language, '公网可达', 'Publicly Reachable')}
      </label>
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
          <p className="text-xs text-gray-300">{txt(language, '默认公网映射 (可后续在右侧添加多组)', 'Default public endpoint mapping (add more later in right panel)')}</p>
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
