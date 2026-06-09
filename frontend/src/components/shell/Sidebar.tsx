import { NavLink } from 'react-router-dom';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { useUiStore } from '../../stores/uiStore';
import { txt, STRINGS } from '../../i18n';
import { ChevronLeftIcon } from './icons';
import { navItemsForMode } from './nav';
import { FOCUS_RING } from './styles';

// Collapsible left sidebar. Default expanded; the fold button persists the
// collapsed state. Nav items are <NavLink>s — the active section is derived from
// the current route (NavLink sets aria-current="page" automatically).
export function Sidebar() {
  const language = useTopologyStore((s) => s.language);
  const mode = useControllerStore((s) => s.mode);
  const collapsed = useUiStore((s) => s.sidebarCollapsed);
  const toggleSidebar = useUiStore((s) => s.toggleSidebar);
  const navItems = navItemsForMode(mode);
  const foldLabel = collapsed
    ? txt(language, ...STRINGS.sidebarExpand)
    : txt(language, ...STRINGS.sidebarCollapse);

  return (
    <aside
      className={`${
        collapsed ? 'w-16' : 'w-60'
      } app-chrome flex shrink-0 flex-col border-r border-[var(--hairline)] transition-[width] duration-200 ease-[var(--ease-quiet)]`}
    >
      <div className="flex h-14 items-center gap-2 border-b border-[var(--hairline)] px-3">
        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-lg bg-[var(--accent)] font-semibold text-[var(--accent-fg)]">
          Y
        </div>
        {!collapsed && (
          <span className="truncate font-semibold">{txt(language, ...STRINGS.brandName)}</span>
        )}
      </div>

      <nav
        aria-label={txt(language, ...STRINGS.primaryNavLabel)}
        className="flex-1 space-y-1 overflow-y-auto p-2"
      >
        {navItems.map(({ key, path, label, Icon }) => {
          const itemLabel = txt(language, ...label);
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
    </aside>
  );
}
