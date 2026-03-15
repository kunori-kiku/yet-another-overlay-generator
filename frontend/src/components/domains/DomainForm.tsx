import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

export function DomainForm() {
  const { addDomain, language } = useTopologyStore();
  const [isOpen, setIsOpen] = useState(false);
  const [name, setName] = useState('');
  const [cidr, setCidr] = useState('');
  const [routingMode, setRoutingMode] = useState<'babel' | 'static' | 'none'>('babel');
  const [error, setError] = useState('');

  const handleSubmit = () => {
    if (!name.trim()) {
      setError(txt(language, '名称不能为空', 'Name is required'));
      return;
    }
    if (!cidr.trim()) {
      setError(txt(language, 'CIDR 不能为空', 'CIDR is required'));
      return;
    }
    // 简单 CIDR 格式校验
    const cidrRegex = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$/;
    if (!cidrRegex.test(cidr)) {
      setError(txt(language, 'CIDR 格式无效，例: 10.10.0.0/24', 'Invalid CIDR format, e.g. 10.10.0.0/24'));
      return;
    }

    const id = `domain-${Date.now()}`;
    addDomain({
      id,
      name: name.trim(),
      cidr: cidr.trim(),
      allocation_mode: 'auto',
      routing_mode: routingMode,
    });

    setName('');
    setCidr('');
    setError('');
    setIsOpen(false);
  };

  if (!isOpen) {
    return (
      <button
        onClick={() => setIsOpen(true)}
        className="w-full py-1.5 px-3 bg-blue-600 hover:bg-blue-500 rounded text-sm mb-2"
      >
        + {txt(language, '新建网络域', 'New Domain')}
      </button>
    );
  }

  return (
    <div className="p-2 bg-gray-700 rounded space-y-2 mb-2">
      <input
        type="text"
        placeholder={txt(language, '域名称', 'Domain name')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <input
        type="text"
        placeholder={txt(language, 'CIDR (如 10.10.0.0/24)', 'CIDR (e.g. 10.10.0.0/24)')}
        value={cidr}
        onChange={(e) => setCidr(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <select
        value={routingMode}
        onChange={(e) => setRoutingMode(e.target.value as 'babel' | 'static' | 'none')}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
      >
        <option value="babel">{txt(language, 'Babel (动态路由)', 'Babel (dynamic routing)')}</option>
        <option value="static">{txt(language, 'Static (静态路由)', 'Static routing')}</option>
        <option value="none">None</option>
      </select>
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
