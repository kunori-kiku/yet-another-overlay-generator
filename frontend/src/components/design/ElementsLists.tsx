import { useTopologyStore } from '../../stores/topologyStore';
import { DomainList } from '../domains/DomainList';
import { NodeList } from '../nodes/NodeList';
import { txt } from '../../i18n';

// 域与节点列表（从原 LeftPanel 拆出的列表部分）。供 Design 工具栏的「域与节点」抽屉使用；
// 创建入口（DomainForm/NodeForm）放在画布工具栏里。
export function ElementsLists() {
  const domains = useTopologyStore((s) => s.domains);
  const nodes = useTopologyStore((s) => s.nodes);
  const language = useTopologyStore((s) => s.language);

  return (
    <div className="p-3 space-y-4">
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {txt(language, '网络域', 'Domains')} ({domains.length})
        </h2>
        <DomainList />
      </section>

      <hr className="border-gray-700" />

      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {txt(language, '节点', 'Nodes')} ({nodes.length})
        </h2>
        <NodeList />
      </section>
    </div>
  );
}
