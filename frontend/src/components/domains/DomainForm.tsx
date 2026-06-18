import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import { uuid } from '../../lib/uuid';

export function DomainForm() {
  const { addDomain, language } = useTopologyStore();
  const [isOpen, setIsOpen] = useState(false);
  const [name, setName] = useState('');
  const [cidr, setCidr] = useState('');
  const [transitCidr, setTransitCidr] = useState('');
  const [routingMode, setRoutingMode] = useState<'babel' | 'static' | 'none'>('babel');
  const [error, setError] = useState('');

  // Simple IPv4 CIDR format check, same as for cidr
  const cidrRegex = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$/;

  const handleSubmit = () => {
    if (!name.trim()) {
      setError(t(language, 'domainForm.nameIsRequired'));
      return;
    }
    if (!cidr.trim()) {
      setError(t(language, 'domainForm.cidrIsRequired'));
      return;
    }
    // Simple CIDR format check
    if (!cidrRegex.test(cidr)) {
      setError(t(language, 'domainForm.invalidCIDRFormatE'));
      return;
    }
    // transit_cidr is optional; when filled in, validate it with the same format check
    if (transitCidr.trim() && !cidrRegex.test(transitCidr.trim())) {
      setError(t(language, 'domainForm.invalidTransitCIDRFormat'));
      return;
    }

    const id = `domain-${uuid()}`;
    addDomain({
      id,
      name: name.trim(),
      cidr: cidr.trim(),
      transit_cidr: transitCidr.trim() || undefined,
      allocation_mode: 'auto',
      routing_mode: routingMode,
    });

    setName('');
    setCidr('');
    setTransitCidr('');
    setError('');
    setIsOpen(false);
  };

  if (!isOpen) {
    return (
      <button
        onClick={() => setIsOpen(true)}
        className="w-full py-1.5 px-3 bg-blue-600 hover:bg-blue-500 rounded text-sm mb-2"
      >
        + {t(language, 'domainForm.newDomain')}
      </button>
    );
  }

  return (
    <div className="p-2 bg-gray-700 rounded space-y-2 mb-2">
      <input
        type="text"
        placeholder={t(language, 'domainForm.domainName')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <input
        type="text"
        placeholder={t(language, 'domainForm.cidrEG10')}
        value={cidr}
        onChange={(e) => setCidr(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <input
        type="text"
        placeholder={t(language, 'domainForm.transitCIDROptionalDefault')}
        value={transitCidr}
        onChange={(e) => setTransitCidr(e.target.value)}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <select
        value={routingMode}
        onChange={(e) => setRoutingMode(e.target.value as 'babel' | 'static' | 'none')}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
      >
        <option value="babel">{t(language, 'domainForm.babelDynamicRouting')}</option>
        <option value="static">{t(language, 'domainForm.staticRouting')}</option>
        <option value="none">None</option>
      </select>
      {error && <p className="text-xs text-red-400">{error}</p>}
      <div className="flex gap-2">
        <button
          onClick={handleSubmit}
          className="flex-1 py-1 bg-green-600 hover:bg-green-500 rounded text-sm"
        >
          {t(language, 'domainForm.confirm')}
        </button>
        <button
          onClick={() => { setIsOpen(false); setError(''); }}
          className="flex-1 py-1 bg-gray-600 hover:bg-gray-500 rounded text-sm"
        >
          {t(language, 'domainForm.cancel')}
        </button>
      </div>
    </div>
  );
}
