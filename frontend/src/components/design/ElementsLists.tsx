import { useTopologyStore } from '../../stores/topologyStore';
import { DomainList } from '../domains/DomainList';
import { NodeList } from '../nodes/NodeList';
import { t } from '../../i18n';

// Domain and node lists (the list portion split out of the former LeftPanel). Used by the
// Design toolbar's "Domains & Nodes" drawer; the create entry points (DomainForm/NodeForm)
// live in the canvas toolbar.
export function ElementsLists() {
  const domains = useTopologyStore((s) => s.domains);
  const nodes = useTopologyStore((s) => s.nodes);
  const language = useTopologyStore((s) => s.language);

  return (
    <div className="p-3 space-y-4">
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {t(language, 'elementsLists.domains')} ({domains.length})
        </h2>
        <DomainList />
      </section>

      <hr className="border-gray-700" />

      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {t(language, 'elementsLists.nodes')} ({nodes.length})
        </h2>
        <NodeList />
      </section>
    </div>
  );
}
