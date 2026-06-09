import { useEffect } from 'react';
import { Outlet } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { Topbar } from './Topbar';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt, STRINGS } from '../../i18n';
import { FOCUS_RING } from './styles';

// Persistent app-shell chrome: collapsible sidebar + top app bar wrapping the
// routed MAIN content (<Outlet/>). The shell stays mounted across navigation so
// sidebar/topbar state survives route changes.
export function Shell() {
  const mode = useControllerStore((s) => s.mode);
  const checkSession = useControllerStore((s) => s.checkSession);
  const language = useTopologyStore((s) => s.language);

  // P5: restore login state from the httpOnly session cookie after a page refresh.
  // Runs once on mount and whenever the workflow switches into controller mode.
  useEffect(() => {
    if (mode === 'controller') void checkSession();
  }, [mode, checkSession]);

  return (
    <div className="flex h-screen bg-[var(--surface)] text-[var(--content)]">
      {/* a11y: keyboard skip-link to the routed content. */}
      <a
        href="#main-content"
        className={`sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded-lg focus:bg-[var(--accent)] focus:px-3 focus:py-1.5 focus:text-sm focus:text-[var(--accent-fg)] ${FOCUS_RING}`}
      >
        {txt(language, ...STRINGS.skipToContent)}
      </a>
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Topbar />
        <main id="main-content" className="flex-1 overflow-hidden">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
