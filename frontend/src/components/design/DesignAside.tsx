import { useTopologyStore } from '../../stores/topologyStore';
import { DomainEditor } from './aside/DomainEditor';
import { NodeEditor } from './aside/NodeEditor';
import { EdgeEditor } from './aside/EdgeEditor';

// Selection-driven right aside for the Design route. Renders the editor for the
// selected entity; returns null when nothing is selected so the canvas is
// full-width. Selection state remains mutually exclusive in the store, but each
// editor is gated independently (mirroring the former RightPanel).
export function DesignAside() {
  const selectedDomainId = useTopologyStore((s) => s.selectedDomainId);
  const selectedNodeId = useTopologyStore((s) => s.selectedNodeId);
  const selectedEdgeId = useTopologyStore((s) => s.selectedEdgeId);

  if (!selectedDomainId && !selectedNodeId && !selectedEdgeId) return null;

  return (
    <aside className="w-80 shrink-0 overflow-y-auto border-l border-[var(--hairline)] bg-[var(--surface-elevated)]">
      <div className="p-3 space-y-4">
        {selectedDomainId && <DomainEditor />}
        {selectedNodeId && <NodeEditor />}
        {selectedEdgeId && <EdgeEditor />}
      </div>
    </aside>
  );
}
