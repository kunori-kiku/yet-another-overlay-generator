import { useEffect, useState } from 'react';
import { Outlet } from 'react-router-dom';
import { Sidebar } from './Sidebar';
import { Topbar } from './Topbar';
import { Drawer } from './Drawer';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { useUiStore } from '../../stores/uiStore';
import { LoginPage } from '../auth/LoginPage';
import { NoticeBanner } from './NoticeBanner';
import { t } from '../../i18n';
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
  const importKeysDropped = useTopologyStore((s) => s.importKeysDropped);
  const importClearedKeys = useTopologyStore((s) => s.importClearedKeys);
  const dismissImportNotice = useTopologyStore((s) => s.dismissImportNotice);
  const language = useTopologyStore((s) => s.language);
  const mobileNavOpen = useUiStore((s) => s.mobileNavOpen);
  const closeMobileNav = useUiStore((s) => s.closeMobileNav);

  // The gate must not flash: until the session probe for the CURRENT controller-mode
  // entry resolves, show neither the canvas (cookie may be valid) nor the login page
  // (it may not). probedMode records which mode the last resolved probe was for; it
  // is RESET on leaving controller mode (during render — the React-sanctioned
  // "adjust state when a prop changes" pattern) so a controller→local→controller
  // round-trip re-raises the splash and re-probes instead of trusting a stale
  // loggedIn (review: an expired session would otherwise flash the canvas).
  const [probedMode, setProbedMode] = useState<'local' | 'controller' | null>(null);
  if (mode !== 'controller' && probedMode !== null) {
    setProbedMode(null);
  }
  const sessionChecked = probedMode === 'controller';

  // P5: restore login state from the httpOnly session cookie after a page refresh.
  // Runs once on mount and whenever the workflow switches into controller mode.
  useEffect(() => {
    if (mode !== 'controller') {
      return;
    }
    let cancelled = false;
    void checkSession().finally(() => {
      if (!cancelled) setProbedMode('controller');
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
          {t(language, 'shell.checkingSession')}
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
        {t(language, 'skipToContent')}
      </a>
      {/* Desktop (lg+): docked sidebar. Below lg it is hidden in favor of the
          off-canvas Drawer below, opened by the Topbar hamburger. */}
      <Sidebar className="hidden lg:flex" />
      {/* Off-canvas mobile nav. Strictly in the post-gate tree (never the login
          /splash branches) so a logged-out 360px viewport leaks no chrome. */}
      <Drawer
        open={mobileNavOpen}
        onClose={closeMobileNav}
        side="left"
        ariaLabel={t(language, 'shell.closeNav')}
        id="mobile-nav-drawer"
      >
        <Sidebar variant="drawer" />
      </Drawer>
      <div className="flex flex-1 flex-col overflow-hidden">
        <Topbar />
        {/* plan-4 (D9): the local design was replaced by the server copy + a backup
            downloaded. plan-5/3.5e: a controller-mode import dropped the design's keys
            (importKeysDropped), or a local import cleared stranded pubkey-only nodes
            (importClearedKeys). All render live via t() through the shared NoticeBanner. */}
        {hydrationNotice && (
          <NoticeBanner
            message={t(language, 'shell.yourLocalDesignWas')}
            onDismiss={dismissHydrationNotice}
            dismissLabel={t(language, 'shell.dismissNotice')}
          />
        )}
        {importKeysDropped > 0 && (
          <NoticeBanner
            message={t(language, 'shell.importKeysDropped', { count: importKeysDropped })}
            onDismiss={dismissImportNotice}
            dismissLabel={t(language, 'shell.dismissNotice_2')}
          />
        )}
        {importClearedKeys > 0 && (
          <NoticeBanner
            message={t(language, 'shell.importClearedKeys', { count: importClearedKeys })}
            onDismiss={dismissImportNotice}
            dismissLabel={t(language, 'shell.dismissNotice_2')}
          />
        )}
        <main id="main-content" tabIndex={-1} className="flex-1 overflow-hidden outline-none">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
