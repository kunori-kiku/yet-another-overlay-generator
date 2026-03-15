import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';

export function NodeForm() {
  const { addNode, domains } = useTopologyStore();
  const [isOpen, setIsOpen] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState<'peer' | 'router' | 'relay' | 'gateway'>('peer');
  const [domainId, setDomainId] = useState('');
  const [hostname, setHostname] = useState('');
  const [listenPort, setListenPort] = useState(51820);
  const [hasPublicIP, setHasPublicIP] = useState(false);
  const [canForward, setCanForward] = useState(false);
  const [error, setError] = useState('');

  const handleSubmit = () => {
    if (!name.trim()) {
      setError('名称不能为空');
      return;
    }
    const targetDomain = domainId || (domains.length > 0 ? domains[0].id : '');
    if (!targetDomain) {
      setError('请先创建一个网络域');
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
      capabilities: {
        can_accept_inbound: hasPublicIP,
        can_forward: canForward || role === 'router' || role === 'relay' || role === 'gateway',
        can_relay: role === 'relay',
        has_public_ip: hasPublicIP,
      },
    });

    setName('');
    setHostname('');
    setError('');
    setIsOpen(false);
  };

  if (!isOpen) {
    return (
      <button
        onClick={() => setIsOpen(true)}
        className="w-full py-1.5 px-3 bg-blue-600 hover:bg-blue-500 rounded text-sm mb-2"
        disabled={domains.length === 0}
        title={domains.length === 0 ? '请先创建网络域' : ''}
      >
        + 添加节点
      </button>
    );
  }

  return (
    <div className="p-2 bg-gray-700 rounded space-y-2 mb-2">
      <input
        type="text"
        placeholder="节点名称"
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <input
        type="text"
        placeholder="主机名 (可选)"
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
        placeholder="监听端口"
        value={listenPort}
        onChange={(e) => setListenPort(parseInt(e.target.value) || 51820)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={hasPublicIP}
          onChange={(e) => setHasPublicIP(e.target.checked)}
          className="rounded"
        />
        有公网 IP
      </label>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={canForward}
          onChange={(e) => setCanForward(e.target.checked)}
          className="rounded"
        />
        可转发流量
      </label>
      {error && <p className="text-xs text-red-400">{error}</p>}
      <div className="flex gap-2">
        <button
          onClick={handleSubmit}
          className="flex-1 py-1 bg-green-600 hover:bg-green-500 rounded text-sm"
        >
          确定
        </button>
        <button
          onClick={() => { setIsOpen(false); setError(''); }}
          className="flex-1 py-1 bg-gray-600 hover:bg-gray-500 rounded text-sm"
        >
          取消
        </button>
      </div>
    </div>
  );
}
