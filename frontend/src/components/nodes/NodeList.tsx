import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

export function NodeList() {
  const { nodes, domains, reorderNodes, selectNode, selectedNodeId, removeNode, language } = useTopologyStore();
  const [draggingId, setDraggingId] = useState<string | null>(null);

  if (nodes.length === 0) {
    return <p className="text-xs text-gray-500 italic">{txt(language, '尚未添加节点', 'No nodes yet')}</p>;
  }

  const domainName = (domainId: string) => domains.find((d) => d.id === domainId)?.name || txt(language, '未分配域', 'Unassigned domain');

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
              ? 'bg-blue-900/30 border-blue-500'
              : 'bg-gray-700 border-transparent hover:border-gray-500'
          }`}
          title={txt(language, '点击编辑，拖拽排序', 'Click to edit, drag to reorder')}
        >
          <div className="flex items-center justify-between">
            <span className="font-medium text-green-300">☰ {node.name}</span>
            <button
              onClick={(e) => {
                e.stopPropagation();
                removeNode(node.id);
              }}
              className="text-red-400 hover:text-red-300 text-xs"
              title={txt(language, '删除', 'Delete')}
            >
              ✕
            </button>
          </div>
          <div className="text-xs text-gray-400">
            {node.role} | {domainName(node.domain_id)}
          </div>
        </div>
      ))}
    </div>
  );
}
