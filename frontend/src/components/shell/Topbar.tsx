import { useTopologyStore } from '../../stores/topologyStore';
import { txt, STRINGS } from '../../i18n';
import { ThemeToggle } from './ThemeToggle';
import { UserMenu } from './UserMenu';

// Top app bar. Left: a section label that becomes a real breadcrumb in P2.
// Right: the standard theme toggle + account menu cluster.
export function Topbar() {
  const language = useTopologyStore((s) => s.language);

  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-[var(--hairline)] bg-[var(--surface-elevated)] px-4">
      <span className="text-sm font-medium text-[var(--content)]">
        {txt(language, ...STRINGS.navDesign)}
      </span>
      <div className="flex-1" />
      <ThemeToggle />
      <UserMenu />
    </header>
  );
}
