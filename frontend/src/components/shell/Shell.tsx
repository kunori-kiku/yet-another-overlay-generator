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
  const importPlaceholdered = useTopologyStore((s) => s.importPlaceholdered);
  const dismissImportNotice = useTopologyStore((s) => s.dismissImportNotice);
  const language = useTopologyStore((s) => s.language);

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
        {/* plan-4 (D9): notice that the local design was replaced by the server copy
            and a backup file was downloaded. Rendered live via txt() (not a frozen
            pre-localized string) and dismissible. */}
        {hydrationNotice && (
          <div
            className="flex items-start justify-between gap-3 border-b border-[var(--hairline)] bg-[var(--surface-sunken)] px-4 py-2 text-sm text-[var(--content)]"
            role="status"
          >
            <span>
              {txt(
                language,
                '本地设计已被服务端副本覆盖（控制器模式下服务端是唯一权威）。原本地设计已自动下载为备份文件。',
                'Your local design was replaced by the server copy (the server is authoritative in controller mode). A backup of the previous local design was downloaded.',
              )}
            </span>
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
        {/* plan-5 (D5): notice that a controller-mode import placeholdered private
            keys. Rendered live via txt() and dismissible. */}
        {importPlaceholdered > 0 && (
          <div
            className="flex items-start justify-between gap-3 border-b border-[var(--hairline)] bg-[var(--surface-sunken)] px-4 py-2 text-sm text-[var(--content)]"
            role="status"
          >
            <span>
              {txt(
                language,
                `控制器模式导入：已将 ${importPlaceholdered} 个私钥替换为占位（节点将使用自持的 agent 密钥）。`,
                `Imported under controller mode: ${importPlaceholdered} private key(s) replaced by placeholders — nodes will use their agent-held keys.`,
              )}
            </span>
            <button
              type="button"
              onClick={dismissImportNotice}
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
