import { useTopologyStore } from '../../stores/topologyStore';
import { useUiStore } from '../../stores/uiStore';
import { txt, STRINGS } from '../../i18n';
import { MonitorIcon, SunIcon, MoonIcon } from './icons';

// Top-right theme control. Cycles System → Light → Dark and shows the icon for
// the current preference (standard top-bar placement, not a deep settings panel).
export function ThemeToggle() {
  const language = useTopologyStore((s) => s.language);
  const theme = useUiStore((s) => s.theme);
  const cycleTheme = useUiStore((s) => s.cycleTheme);

  const current = {
    system: { Icon: MonitorIcon, label: txt(language, ...STRINGS.themeSystem) },
    light: { Icon: SunIcon, label: txt(language, ...STRINGS.themeLight) },
    dark: { Icon: MoonIcon, label: txt(language, ...STRINGS.themeDark) },
  }[theme];
  const Icon = current.Icon;
  const toggleLabel = txt(language, ...STRINGS.themeToggleLabel);

  return (
    <button
      type="button"
      onClick={cycleTheme}
      title={`${toggleLabel} · ${current.label}`}
      aria-label={`${toggleLabel}: ${current.label}`}
      className="inline-flex h-9 w-9 items-center justify-center rounded-lg text-[var(--content-muted)] transition-colors hover:bg-[var(--surface-sunken)] hover:text-[var(--content)]"
    >
      <Icon />
    </button>
  );
}
