import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
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
  // Local compile (POST /api/compile) generates/reconstructs WireGuard keys client-side and
  // requires private keys in the design. Controller mode is zero-knowledge: the hydrated design
  // is public-keys-only (the agents hold the private keys) and compilation happens SERVER-SIDE
  // during Deploy (stage/promote). So the air-gap Compile action is local-mode only — in
  // controller mode it would fail on every node ("pinned public key, no private key"). Deploy
  // is the controller-mode path.
  const mode = useControllerStore((s) => s.mode);

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
      {/* Compile is a LOCAL/air-gap action only — see the mode note above. In controller mode
          the user deploys from the Deploy page (server-side compile), so no Compile button here. */}
      {mode === 'local' && (
        <button
          type="button"
          onClick={() => compile()}
          disabled={isCompiling || nodeCount === 0}
          className="h-8 rounded bg-green-600 px-3 text-sm hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400"
        >
          {isCompiling ? txt(language, '编译中...', 'Compiling...') : txt(language, '🔨 编译', '🔨 Compile')}
        </button>
      )}
    </div>
  );
}
