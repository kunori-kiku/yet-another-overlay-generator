import { useTopologyStore } from '../../stores/topologyStore';

export function DomainList() {
  const { domains, removeDomain } = useTopologyStore();

  if (domains.length === 0) {
    return (
      <p className="text-xs text-gray-500 italic">尚未创建网络域</p>
    );
  }

  return (
    <div className="space-y-2">
      {domains.map((domain) => (
        <div
          key={domain.id}
          className="p-2 bg-gray-700 rounded text-sm space-y-1"
        >
          <div className="flex items-center justify-between">
            <span className="font-medium text-blue-300">{domain.name}</span>
            <button
              onClick={() => removeDomain(domain.id)}
              className="text-red-400 hover:text-red-300 text-xs"
              title="删除"
            >
              ✕
            </button>
          </div>
          <div className="text-xs text-gray-400">
            CIDR: {domain.cidr} | {domain.routing_mode} | {domain.allocation_mode}
          </div>
        </div>
      ))}
    </div>
  );
}
