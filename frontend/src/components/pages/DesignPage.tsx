import { useState } from 'react';
import { TopologyCanvas } from '../canvas/TopologyCanvas';
import { BottomBar } from '../layout/BottomBar';
import { CanvasToolbar } from '../design/CanvasToolbar';
import { ElementsLists } from '../design/ElementsLists';
import { DesignAside } from '../design/DesignAside';

// /design — topology editing. Node manipulation is demoted from an always-on
// docked panel to: a canvas toolbar (create entry points + Domains&Nodes list
// drawer) and a selection-driven right aside (DesignAside, only when something is
// selected). The canvas is full-width otherwise. BottomBar stays as the
// validation footer. Mounted under a route-scoped ReactFlowProvider (App.tsx).
export function DesignPage() {
  const [listsOpen, setListsOpen] = useState(false);

  return (
    <div className="flex h-full flex-col bg-gray-900 text-gray-100">
      <CanvasToolbar listsOpen={listsOpen} onToggleLists={() => setListsOpen((open) => !open)} />
      <div className="flex flex-1 overflow-hidden">
        {listsOpen && (
          <aside className="w-72 shrink-0 overflow-y-auto border-r border-gray-700 bg-gray-800">
            <ElementsLists />
          </aside>
        )}
        <main className="relative flex-1 overflow-auto bg-gray-900">
          <TopologyCanvas />
        </main>
        <DesignAside />
      </div>
      <footer className="h-40 shrink-0 overflow-y-auto border-t border-gray-700 bg-gray-800">
        <BottomBar />
      </footer>
    </div>
  );
}
