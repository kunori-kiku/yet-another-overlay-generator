import { useRef } from 'react';
import { useLocation } from 'react-router-dom';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { useUiStore } from '../../stores/uiStore';
import { t } from '../../i18n';
import { ThemeToggle } from './ThemeToggle';
import { LanguageToggle } from './LanguageToggle';
import { UserMenu } from './UserMenu';
import { MenuIcon } from './icons';
import { activeNavItem } from './nav';
import { FOCUS_RING } from './styles';

// Top app bar. Left: the active section name (breadcrumb, derived from the
// route). Right: the project-I/O cluster (import/export/flush — only on the
// Design route), the language toggle, the theme toggle, and the account menu.
// This absorbs the retired layout/TopBar.
export function Topbar() {
  const location = useLocation();
  const language = useTopologyStore((s) => s.language);
  const exportProject = useTopologyStore((s) => s.exportProject);
  const importProject = useTopologyStore((s) => s.importProject);
  const flushWorkspace = useTopologyStore((s) => s.flushWorkspace);
  const mode = useControllerStore((s) => s.mode);
  const importDesignToServer = useControllerStore((s) => s.importDesignToServer);
  const mobileNavOpen = useUiStore((s) => s.mobileNavOpen);
  const setMobileNavOpen = useUiStore((s) => s.setMobileNavOpen);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const active = activeNavItem(location.pathname);
  const onDesign = active?.key === 'design';
  // Export + Import are available in BOTH modes on the Design route, but their meaning is
  // mode-aware (they are NOT a local-mode-only concept masked off elsewhere):
  //   - Export downloads the current design JSON — a backup the operator owns, on their machine.
  //   - Import: LOCAL mode loads the file into the canvas as a disposable draft (importProject);
  //     CONTROLLER mode is server-authoritative — it strips keys, writes the design to the
  //     controller as a new version (which heals colliding pins), and re-hydrates the canvas from
  //     the server (importDesignToServer). A naive local-style import in controller mode would flip
  //     the canvas to local-only and PERSIST fleet IPs/SSH to localStorage — the leak we avoid.
  // Flush stays LOCAL-only: in controller mode it would clear only the disposable mirror (the false
  // "cannot be undone" leak), and persistence there is the Save button on the canvas (saveDesign).
  const showIOCluster = onDesign;
  const showFlush = onDesign && mode === 'local';

  const handleImportClick = () => fileInputRef.current?.click();
  const handleFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) {
      if (mode === 'controller') {
        // Confirm before overwriting the server's working design (non-destructive: a new retained
        // version, and hydrateFromServer backs up the current canvas first). Never auto-deploys.
        if (window.confirm(t(language, 'topbar.importToServerConfirm'))) {
          await importDesignToServer(file);
        }
      } else {
        await importProject(file);
      }
    }
    if (fileInputRef.current) fileInputRef.current.value = '';
  };
  const handleFlush = () => {
    const confirmed = window.confirm(
      t(language, 'topbar.thisWillClearProject'),
    );
    if (confirmed) flushWorkspace();
  };

  const ioBtn =
    'px-2.5 py-1 text-xs rounded-lg text-[var(--content-muted)] transition-colors hover:bg-[var(--surface-sunken)] hover:text-[var(--content)] ' +
    FOCUS_RING;

  return (
    <header className="app-chrome flex h-14 shrink-0 items-center gap-3 border-b border-[var(--hairline)] px-4">
      {/* Hamburger: opens the off-canvas sidebar drawer below lg. The docked
          sidebar takes over at lg+, so this is hidden there. ≥44px tap target. */}
      <button
        type="button"
        onClick={() => setMobileNavOpen(true)}
        aria-label={t(language, 'shell.openNav')}
        aria-controls="mobile-nav-drawer"
        aria-expanded={mobileNavOpen}
        className={`-ml-1.5 grid h-11 w-11 shrink-0 place-items-center rounded-lg text-[var(--content-muted)] transition-colors hover:bg-[var(--surface-sunken)] hover:text-[var(--content)] lg:hidden ${FOCUS_RING}`}
      >
        <MenuIcon />
      </button>
      <span className="text-sm font-medium text-[var(--content)]">
        {active ? t(language, active.labelKey) : ''}
      </span>
      <div className="flex-1" />

      {showIOCluster && (
        <div className="flex items-center gap-1">
          <input
            type="file"
            accept=".json"
            ref={fileInputRef}
            className="hidden"
            onChange={handleFileChange}
          />
          <button type="button" onClick={handleImportClick} className={ioBtn}>
            {t(language, 'topbar.import')}
          </button>
          <button type="button" onClick={() => exportProject()} className={ioBtn}>
            {t(language, 'topbar.export')}
          </button>
          {showFlush && (
            <button
              type="button"
              onClick={handleFlush}
              className={`px-2.5 py-1 text-xs rounded-lg text-[var(--danger)] transition-colors hover:bg-[var(--danger-bg)] ${FOCUS_RING}`}
            >
              {t(language, 'topbar.flush')}
            </button>
          )}
          <span className="mx-1 h-5 w-px bg-[var(--hairline)]" />
        </div>
      )}

      <LanguageToggle />

      <ThemeToggle />
      <UserMenu />
    </header>
  );
}
