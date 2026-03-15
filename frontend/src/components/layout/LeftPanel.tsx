import { useTopologyStore } from '../../stores/topologyStore';
import { DomainList } from '../domains/DomainList';
import { DomainForm } from '../domains/DomainForm';
import { NodeForm } from '../nodes/NodeForm';
import { NodeList } from '../nodes/NodeList';
import { txt } from '../../i18n';

export function LeftPanel() {
  const { domains, nodes, language } = useTopologyStore();

  return (
    <div className="p-3 space-y-4">
      {/* Domain 管理 */}
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {txt(language, '网络域', 'Domains')} ({domains.length})
        </h2>
        <DomainForm />
        <DomainList />
      </section>

      <hr className="border-gray-700" />

      {/* 节点管理 */}
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {txt(language, '节点', 'Nodes')} ({nodes.length})
        </h2>
        <NodeForm />
        <NodeList />
      </section>
    </div>
  );
}
