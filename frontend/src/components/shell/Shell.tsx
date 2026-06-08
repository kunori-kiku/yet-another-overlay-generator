import { Outlet } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { Topbar } from './Topbar';

// Persistent app-shell chrome: collapsible sidebar + top app bar wrapping the
// routed MAIN content (<Outlet/>). The shell stays mounted across navigation so
// sidebar/topbar state survives route changes.
export function Shell() {
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
