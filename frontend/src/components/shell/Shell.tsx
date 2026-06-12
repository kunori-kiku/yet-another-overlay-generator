import { useEffect, useState } from 'react';
import { Outlet } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { Topbar } from './Topbar';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { LoginPage } from '../auth/LoginPage';
import { txt, STRINGS } from '../../i18n';
import { FOCUS_RING } from './styles';

// Persistent app-shell chrome: collapsible sidebar + top app bar wrapping the
// routed MAIN content (<Outlet/>). The shell stays mounted across navigation so
// sidebar/topbar state survives route changes.
//
// plan-4 (D2): in controller mode the shell is GATED — entering the panel with
// persisted controller mode lands on a full-viewport LoginPage before any chrome
// renders. Until the mount checkSession() resolves, a quiet splash shows instead
// (no canvas flash for an operator whose cookie session is actually valid). The
// requested deep link stays in the router; once the gate opens, the route renders.
// Break-glass (operatorToken set) passes the gate without being a login — the
// recovery path must not be locked out by the login door. Local mode: unaffected.
export function Shell() {
  const mode = useControllerStore((s) => s.mode);
  const checkSession = useControllerStore((s) => s.checkSession);
  const loggedIn = useControllerStore(selectLoggedIn);
  const operatorToken = useControllerStore((s) => s.operatorToken);
  const hydrationNotice = useControllerStore((s) => s.hydrationNotice);
  const dismissHydrationNotice = useControllerStore((s) => s.dismissHydrationNotice);
  const language = useTopologyStore((s) => s.language);

  // sessionChecked guards the gate against a flash: neither the canvas (cookie may
  // be valid) nor the login page (it may not) is shown until the FIRST probe in
  // controller mode resolves. Subsequent mode flips re-probe asynchronously without
  // re-raising the splash (same-session login state is already known).
  const [sessionChecked, setSessionChecked] = useState(false);

  // P5: restore login state from the httpOnly session cookie after a page refresh.
  // Runs once on mount and whenever the workflow switches into controller mode.
  useEffect(() => {
    if (mode !== 'controller') {
      return;
    }
    let cancelled = false;
    void checkSession().finally(() => {
      if (!cancelled) setSessionChecked(true);
    });
    return () => {
      cancelled = true;
    };
  }, [mode, checkSession]);

  if (mode === 'controller') {
    if (!sessionChecked) {
      // Quiet splash while the cookie probe runs — no chrome, no canvas.
      return (
        <div
          className="grid h-screen place-items-center bg-[var(--surface)] text-sm text-[var(--content-muted)]"
          role="status"
        >
          {txt(language, '正在检查会话…', 'Checking session…')}
        </div>
      );
    }
    if (!loggedIn && operatorToken === '') {
      return <LoginPage />;
    }
  }

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
        {/* plan-4 (D9): one-time notice that the local design was replaced by the
            server copy and a backup file was downloaded. Dismissible. */}
        {hydrationNotice && (
          <div
            className="flex items-start justify-between gap-3 border-b border-[var(--hairline)] bg-[var(--surface-sunken)] px-4 py-2 text-sm text-[var(--content)]"
            role="status"
          >
            <span>{hydrationNotice}</span>
            <button
              type="button"
              onClick={dismissHydrationNotice}
              aria-label={txt(language, '关闭提示', 'Dismiss notice')}
              className={`shrink-0 rounded px-2 text-[var(--content-muted)] hover:text-[var(--content)] ${FOCUS_RING}`}
            >
              ✕
            </button>
          </div>
        )}
        <main id="main-content" tabIndex={-1} className="flex-1 overflow-hidden outline-none">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
