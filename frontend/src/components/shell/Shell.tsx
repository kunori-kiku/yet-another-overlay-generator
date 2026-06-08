import { useEffect } from 'react';
import { Outlet } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { Topbar } from './Topbar';
import { useControllerStore } from '../../stores/controllerStore';

// Persistent app-shell chrome: collapsible sidebar + top app bar wrapping the
// routed MAIN content (<Outlet/>). The shell stays mounted across navigation so
// sidebar/topbar state survives route changes.
export function Shell() {
  const mode = useControllerStore((s) => s.mode);
  const checkSession = useControllerStore((s) => s.checkSession);

  // P5: restore login state from the httpOnly session cookie after a page refresh.
  // Runs once on mount and whenever the workflow switches into controller mode.
  useEffect(() => {
    if (mode === 'controller') void checkSession();
  }, [mode, checkSession]);

  return (
    <div className="flex h-screen bg-[var(--surface)] text-[var(--content)]">
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Topbar />
        <main className="flex-1 overflow-hidden">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
