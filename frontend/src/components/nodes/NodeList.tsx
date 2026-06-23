import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

export function NodeList() {
  const { nodes, domains, reorderNodes, selectNode, selectedNodeId, removeNode, language } = useTopologyStore();
  const [draggingId, setDraggingId] = useState<string | null>(null);

  if (nodes.length === 0) {
    return <p className="text-xs text-[var(--content-muted)] italic">{t(language, 'nodeList.noNodesYet')}</p>;
  }

  const domainName = (domainId: string) => domains.find((d) => d.id === domainId)?.name || t(language, 'nodeList.unassignedDomain');

  return (
    <div className="space-y-2">
      {nodes.map((node) => (
        <div
          key={node.id}
          draggable
          onDragStart={() => setDraggingId(node.id)}
          onDragOver={(e) => e.preventDefault()}
          onDrop={() => {
            if (draggingId) {
              reorderNodes(draggingId, node.id);
            }
            setDraggingId(null);
          }}
          onDragEnd={() => setDraggingId(null)}
          onClick={() => selectNode(node.id)}
          className={`p-2 rounded text-sm space-y-1 cursor-pointer border ${
            selectedNodeId === node.id
              ? 'bg-[var(--control-hover)] border-[var(--accent)]'
              : 'bg-[var(--control)] border-transparent hover:border-[var(--hairline)]'
          }`}
          title={t(language, 'nodeList.clickToEditDrag')}
        >
          <div className="flex items-center justify-between">
            <span className="font-medium text-[var(--content)]">☰ {node.name}</span>
            <button
              onClick={(e) => {
                e.stopPropagation();
                removeNode(node.id);
              }}
              className="text-[var(--danger)] hover:text-[var(--danger)] text-xs"
              title={t(language, 'nodeList.delete')}
            >
              ✕
            </button>
          </div>
          <div className="text-xs text-[var(--content-muted)]">
            {node.role} | {domainName(node.domain_id)}
          </div>
        </div>
      ))}
    </div>
  );
}
