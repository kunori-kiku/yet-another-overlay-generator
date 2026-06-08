import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { useUiStore, type ThemePref } from '../../stores/uiStore';
import { txt, STRINGS } from '../../i18n';
import { ConnectionSettings } from '../deploy/ConnectionSettings';
import { BootstrapSettings } from '../deploy/BootstrapSettings';

// /settings — Mode (local/controller) · Connection (endpoints + sign-in) ·
// Bootstrap · Appearance. Mode persistence + the Appearance controls land in P4.
export function SettingsPage() {
  const language = useTopologyStore((s) => s.language);
  const mode = useControllerStore((s) => s.mode);
  const setMode = useControllerStore((s) => s.setMode);
  const theme = useUiStore((s) => s.theme);
  const setTheme = useUiStore((s) => s.setTheme);
  const translucency = useUiStore((s) => s.translucency);
  const setTranslucency = useUiStore((s) => s.setTranslucency);
  const settings = useControllerStore((s) => s.settings);
  const saveSettings = useControllerStore((s) => s.saveSettings);

  // Translucency is applied via uiStore (the appearance source ThemeProvider reads). In
  // controller mode the server is the source of truth, so also persist it there
  // (merging the current bootstrap settings); local mode persists client-side only.
  const onTranslucencyChange = (on: boolean) => {
    setTranslucency(on);
    if (mode === 'controller' && settings) {
      void saveSettings({ ...settings, translucency: on });
    }
  };

  const seg = (selected: boolean) =>
    `px-3 py-1.5 text-sm ${
      selected ? 'bg-blue-600 text-white' : 'text-gray-300 hover:bg-gray-600'
    }`;

  const themeOptions: ReadonlyArray<{ value: ThemePref; label: readonly [string, string] }> = [
    { value: 'system', label: STRINGS.themeSystem },
    { value: 'light', label: STRINGS.themeLight },
    { value: 'dark', label: STRINGS.themeDark },
  ];

  return (
    <div className="h-full overflow-y-auto bg-gray-900 text-gray-100 p-6 space-y-6">
      <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
        <h3 className="text-lg font-semibold text-blue-400">
          {txt(language, ...STRINGS.settingsModeHeading)}
        </h3>
        <p className="text-sm text-gray-400">{txt(language, ...STRINGS.settingsModeHint)}</p>
        <div className="flex w-fit items-center overflow-hidden rounded border border-gray-600 bg-gray-700">
          <button type="button" onClick={() => setMode('local')} className={seg(mode === 'local')}>
            {txt(language, ...STRINGS.modeLocal)}
          </button>
          <button
            type="button"
            onClick={() => setMode('controller')}
            className={seg(mode === 'controller')}
          >
            {txt(language, ...STRINGS.modeController)}
          </button>
        </div>
      </section>

      <ConnectionSettings />

      <BootstrapSettings />

      <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-4 max-w-2xl">
        <h3 className="text-lg font-semibold text-blue-400">
          {txt(language, ...STRINGS.settingsAppearanceHeading)}
        </h3>

        {/* Theme — mirrors the top-right toggle (client-persisted, per device). */}
        <div className="space-y-1">
          <label className="text-xs text-gray-400">{txt(language, ...STRINGS.appearanceTheme)}</label>
          <div className="flex w-fit items-center overflow-hidden rounded border border-gray-600 bg-gray-700">
            {themeOptions.map((opt) => (
              <button
                key={opt.value}
                type="button"
                onClick={() => setTheme(opt.value)}
                aria-pressed={theme === opt.value}
                className={seg(theme === opt.value)}
              >
                {txt(language, ...opt.label)}
              </button>
            ))}
          </div>
        </div>

        {/* Translucency — client toggle in P4; P5 makes it server-backed in
            controller mode with this as the local fallback. */}
        <div className="space-y-1">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={translucency}
              onChange={(e) => onTranslucencyChange(e.target.checked)}
            />
            {txt(language, ...STRINGS.appearanceTranslucency)}
          </label>
          <p className="text-xs text-gray-500">
            {txt(language, ...STRINGS.appearanceTranslucencyHint)}
          </p>
        </div>
      </section>
    </div>
  );
}
