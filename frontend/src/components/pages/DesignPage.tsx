import { useState } from 'react';
import { TopologyCanvas } from '../canvas/TopologyCanvas';
import { BottomBar } from '../layout/BottomBar';
import { CanvasToolbar } from '../design/CanvasToolbar';
import { ElementsLists } from '../design/ElementsLists';
import { DesignAside } from '../design/DesignAside';
import { CanvasGate } from '../design/CanvasGate';
import { useTopologyStore } from '../../stores/topologyStore';
import { useIsDesktop } from '../../lib/useMediaQuery';
import { t } from '../../i18n';

// /design — topology editing. Node manipulation is demoted from an always-on
// docked panel to: a canvas toolbar (create entry points + Domains&Nodes list
// drawer) and a selection-driven right aside (DesignAside, only when something is
// selected). The canvas is full-width otherwise. BottomBar stays as the
// validation footer. Mounted under a route-scoped ReactFlowProvider (App.tsx).
//
// Below lg (1024px) the editor chrome is desktop-shaped, so the route switches to a
// read-only pan/zoom preview behind a CanvasGate interstitial — the full edit layout
// is rendered byte-for-byte unchanged at lg and up.
export function DesignPage() {
  const language = useTopologyStore((s) => s.language);
  const isDesktop = useIsDesktop();
  const [listsOpen, setListsOpen] = useState(false);

  // Small screens: a full-bleed read-only canvas + the gate. None of the editing
  // chrome (CanvasToolbar, lists drawer, DesignAside, validation footer) is mounted,
  // so the read-only canvas gets the full narrow width.
  if (!isDesktop) {
    return (
      <div className="relative h-full bg-[var(--surface)] text-[var(--content)]">
        <TopologyCanvas editable={false} />
        <CanvasGate />
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col bg-[var(--surface)] text-[var(--content)]">
      <CanvasToolbar listsOpen={listsOpen} onToggleLists={() => setListsOpen((open) => !open)} />
      <div className="flex flex-1 overflow-hidden">
        {listsOpen && (
          <aside
            id="design-lists-drawer"
            aria-label={t(language, 'toolbarLists')}
            className="w-72 shrink-0 overflow-y-auto border-r border-[var(--hairline)] bg-[var(--surface-elevated)]"
          >
            <ElementsLists />
          </aside>
        )}
        <main className="relative flex-1 overflow-auto bg-[var(--surface)]">
          <TopologyCanvas />
        </main>
        <DesignAside />
      </div>
      <footer className="h-40 shrink-0 overflow-y-auto border-t border-[var(--hairline)] bg-[var(--surface-elevated)]">
        <BottomBar />
      </footer>
    </div>
  );
}
