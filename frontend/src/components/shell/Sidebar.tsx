import { NavLink } from 'react-router-dom';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { useUiStore } from '../../stores/uiStore';
import { t } from '../../i18n';
import { ChevronLeftIcon } from './icons';
import { navItemsForMode } from './nav';
import { FOCUS_RING } from './styles';

// Collapsible left sidebar. Default expanded; the fold button persists the
// collapsed state. Nav items are <NavLink>s — the active section is derived from
// the current route (NavLink sets aria-current="page" automatically).
//
// Two hosts share the inner chrome (brand / nav / fold):
//   - 'docked'  (default): the desktop ≥lg docked aside (Shell hides it below lg).
//   - 'drawer': rendered inside the off-canvas mobile <Drawer> (Shell, below lg).
//     Always expanded; the desktop-only fold button is hidden (the drawer is
//     dismissed via the Drawer's own close/backdrop/Esc, not by folding).
interface SidebarProps {
  variant?: 'docked' | 'drawer';
  /** Extra classes for the docked <aside> (Shell uses this for the `hidden lg:flex`
   *  responsive treatment). Ignored in drawer mode. */
  className?: string;
}

export function Sidebar({ variant = 'docked', className = '' }: SidebarProps) {
  const language = useTopologyStore((s) => s.language);
  const mode = useControllerStore((s) => s.mode);
  const collapsedPref = useUiStore((s) => s.sidebarCollapsed);
  const toggleSidebar = useUiStore((s) => s.toggleSidebar);
  const navItems = navItemsForMode(mode);
  // In drawer mode the sidebar is always expanded (no icon-only state on phone).
  const collapsed = variant === 'docked' && collapsedPref;
  const foldLabel = collapsed
    ? t(language, 'sidebarExpand')
    : t(language, 'sidebarCollapse');

  const inner = (
    <>
      <div className="flex h-14 items-center gap-2 border-b border-[var(--hairline)] px-3">
        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-lg bg-[var(--accent)] font-semibold text-[var(--accent-fg)]">
          Y
        </div>
        {!collapsed && (
          <span className="truncate font-semibold">{t(language, 'brandName')}</span>
        )}
      </div>

      <nav
        aria-label={t(language, 'primaryNavLabel')}
        className="flex-1 space-y-1 overflow-y-auto p-2"
      >
        {navItems.map(({ key, path, labelKey, Icon }) => {
          const itemLabel = t(language, labelKey);
          return (
            <NavLink
              key={key}
              to={path}
              aria-label={itemLabel}
              title={collapsed ? itemLabel : undefined}
              className={({ isActive }) =>
                `flex h-10 w-full items-center gap-3 rounded-lg text-sm transition-colors ${FOCUS_RING} ${
                  collapsed ? 'justify-center px-0' : 'px-3'
                } ${
                  isActive
                    ? 'bg-[var(--surface-sunken)] font-medium text-[var(--content)]'
                    : 'text-[var(--content-muted)] hover:bg-[var(--surface-sunken)] hover:text-[var(--content)]'
                }`
              }
            >
              <Icon />
              {!collapsed && <span className="truncate">{itemLabel}</span>}
            </NavLink>
          );
        })}
      </nav>

      {variant === 'docked' && (
        <div className="border-t border-[var(--hairline)] p-2">
          <button
            type="button"
            onClick={toggleSidebar}
            aria-label={foldLabel}
            title={foldLabel}
            className={`flex h-10 w-full items-center gap-3 rounded-lg text-sm text-[var(--content-muted)] transition-colors hover:bg-[var(--surface-sunken)] hover:text-[var(--content)] ${FOCUS_RING} ${
              collapsed ? 'justify-center px-0' : 'px-3'
            }`}
          >
            <ChevronLeftIcon className={collapsed ? 'rotate-180' : ''} />
            {!collapsed && <span className="truncate">{foldLabel}</span>}
          </button>
        </div>
      )}
    </>
  );

  // Drawer host: the <Drawer> owns the surface/positioning/scroll, so render the
  // chrome as a plain flex column that fills the panel.
  if (variant === 'drawer') {
    return <div className="flex h-full flex-col">{inner}</div>;
  }

  return (
    <aside
      className={`${
        collapsed ? 'w-16' : 'w-60'
      } app-chrome flex shrink-0 flex-col border-r border-[var(--hairline)] transition-[width] duration-200 ease-[var(--ease-quiet)]${
        className ? ` ${className}` : ''
      }`}
    >
      {inner}
    </aside>
  );
}
