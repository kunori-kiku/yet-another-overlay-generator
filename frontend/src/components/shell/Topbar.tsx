import { useRef } from 'react';
import { useLocation } from 'react-router-dom';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import { ThemeToggle } from './ThemeToggle';
import { LanguageToggle } from './LanguageToggle';
import { UserMenu } from './UserMenu';
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
  const fileInputRef = useRef<HTMLInputElement>(null);

  const active = activeNavItem(location.pathname);
  const onDesign = active?.key === 'design';

  const handleImportClick = () => fileInputRef.current?.click();
  const handleFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) await importProject(file);
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
      <span className="text-sm font-medium text-[var(--content)]">
        {active ? t(language, active.labelKey) : ''}
      </span>
      <div className="flex-1" />

      {onDesign && (
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
          <button
            type="button"
            onClick={handleFlush}
            className={`px-2.5 py-1 text-xs rounded-lg text-red-500 transition-colors hover:bg-red-500/10 ${FOCUS_RING}`}
          >
            {t(language, 'topbar.flush')}
          </button>
          <span className="mx-1 h-5 w-px bg-[var(--hairline)]" />
        </div>
      )}

      <LanguageToggle />

      <ThemeToggle />
      <UserMenu />
    </header>
  );
}
