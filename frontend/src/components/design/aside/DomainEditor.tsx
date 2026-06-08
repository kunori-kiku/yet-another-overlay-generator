import { useTopologyStore } from '../../../stores/topologyStore';
import { txt } from '../../../i18n';

// 网络域属性编辑器（从 RightPanel 的选中域区块原样抽出，供选择驱动的 Design 右侧 aside 使用）。
export function DomainEditor() {
  const language = useTopologyStore((s) => s.language);
  const domains = useTopologyStore((s) => s.domains);
  const selectedDomainId = useTopologyStore((s) => s.selectedDomainId);
  const updateDomain = useTopologyStore((s) => s.updateDomain);
  const removeDomain = useTopologyStore((s) => s.removeDomain);

  const selectedDomain = domains.find((d) => d.id === selectedDomainId);
  if (!selectedDomain) return null;

  return (
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
  );
}
