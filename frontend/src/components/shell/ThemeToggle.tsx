import { useTopologyStore } from '../../stores/topologyStore';
import { useUiStore } from '../../stores/uiStore';
import { t } from '../../i18n';
import { MonitorIcon, SunIcon, MoonIcon } from './icons';
import { FOCUS_RING } from './styles';

// Top-right theme control. Cycles System → Light → Dark and shows the icon for
// the current preference (standard top-bar placement, not a deep settings panel).
export function ThemeToggle() {
  const language = useTopologyStore((s) => s.language);
  const theme = useUiStore((s) => s.theme);
  const cycleTheme = useUiStore((s) => s.cycleTheme);

  const current = {
    system: { Icon: MonitorIcon, label: t(language, 'themeSystem') },
    light: { Icon: SunIcon, label: t(language, 'themeLight') },
    dark: { Icon: MoonIcon, label: t(language, 'themeDark') },
  }[theme];
  const Icon = current.Icon;
  const toggleLabel = t(language, 'themeToggleLabel');

  return (
    <button
      type="button"
      onClick={cycleTheme}
      title={`${toggleLabel} · ${current.label}`}
      aria-label={`${toggleLabel}: ${current.label}`}
      className={`inline-flex h-9 w-9 items-center justify-center rounded-lg text-[var(--content-muted)] transition-colors hover:bg-[var(--surface-sunken)] hover:text-[var(--content)] ${FOCUS_RING}`}
    >
      <Icon />
    </button>
  );
}
