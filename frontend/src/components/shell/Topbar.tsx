import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';
import { ThemeToggle } from './ThemeToggle';
import { UserMenu } from './UserMenu';
import { NAV_ITEMS, ACTIVE_NAV_KEY } from './nav';

// Top app bar. Left: the active section label (becomes a real breadcrumb in P2,
// derived from the route). Right: the standard theme toggle + account menu.
export function Topbar() {
  const language = useTopologyStore((s) => s.language);
  const active = NAV_ITEMS.find((item) => item.key === ACTIVE_NAV_KEY) ?? NAV_ITEMS[0];

  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-[var(--hairline)] bg-[var(--surface-elevated)] px-4">
      <span className="text-sm font-medium text-[var(--content)]">
        {txt(language, ...active.label)}
      </span>
      <div className="flex-1" />
      <ThemeToggle />
      <UserMenu />
    </header>
  );
}
