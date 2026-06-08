import { useTopologyStore } from '../../stores/topologyStore';
import { txt, STRINGS } from '../../i18n';
import { DomainForm } from '../domains/DomainForm';
import { NodeForm } from '../nodes/NodeForm';

// Canvas toolbar that replaces the docked LeftPanel: the [+ Domain] / [+ Node]
// create entry points (the self-contained collapsible forms), a toggle for the
// Domains & Nodes list drawer, and a Compile action. The canvas stays full-width
// by default — the list drawer is opt-in.
export function CanvasToolbar({
  listsOpen,
  onToggleLists,
}: {
  listsOpen: boolean;
  onToggleLists: () => void;
}) {
  const language = useTopologyStore((s) => s.language);
  const compile = useTopologyStore((s) => s.compile);
  const isCompiling = useTopologyStore((s) => s.isCompiling);
  const nodeCount = useTopologyStore((s) => s.nodes.length);

  return (
    <div className="flex flex-wrap items-start gap-2 border-b border-gray-700 bg-gray-800 px-3 py-2">
      <div className="w-56">
        <DomainForm />
      </div>
      <div className="w-56">
        <NodeForm />
      </div>
      <button
        type="button"
        onClick={onToggleLists}
        aria-pressed={listsOpen}
        aria-controls="design-lists-drawer"
        className={`h-8 rounded px-3 text-sm ${
          listsOpen ? 'bg-blue-600 text-white' : 'bg-gray-700 text-gray-200 hover:bg-gray-600'
        }`}
      >
        <span aria-hidden="true">☰</span> {txt(language, ...STRINGS.toolbarLists)}
      </button>
      <div className="flex-1" />
      <button
        type="button"
        onClick={() => compile()}
        disabled={isCompiling || nodeCount === 0}
        className="h-8 rounded bg-green-600 px-3 text-sm hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400"
      >
        {isCompiling ? txt(language, '编译中...', 'Compiling...') : txt(language, '🔨 编译', '🔨 Compile')}
      </button>
    </div>
  );
}
