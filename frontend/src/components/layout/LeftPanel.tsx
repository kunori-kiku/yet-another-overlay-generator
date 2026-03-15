import { useTopologyStore } from '../../stores/topologyStore';
import { DomainList } from '../domains/DomainList';
import { DomainForm } from '../domains/DomainForm';
import { NodeForm } from '../nodes/NodeForm';

export function LeftPanel() {
  const { domains, nodes } = useTopologyStore();

  return (
    <div className="p-3 space-y-4">
      {/* Domain 管理 */}
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          网络域 ({domains.length})
        </h2>
        <DomainForm />
        <DomainList />
      </section>

      <hr className="border-gray-700" />

      {/* 节点管理 */}
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          节点 ({nodes.length})
        </h2>
        <NodeForm />
      </section>
    </div>
  );
}
