import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { useUiStore, type ThemePref } from '../../stores/uiStore';
import { t, type MessageKey } from '../../i18n';
import { ConnectionSettings } from '../deploy/ConnectionSettings';
import { BootstrapSettings } from '../deploy/BootstrapSettings';

// /settings — Mode (local/controller) · Connection (endpoints + sign-in) ·
// Bootstrap · Appearance. Mode persistence + the Appearance controls land in P4.
export function SettingsPage() {
  const language = useTopologyStore((s) => s.language);
  const mode = useControllerStore((s) => s.mode);
  const setMode = useControllerStore((s) => s.setMode);
  // controller→local goes through the shared, serverHeld-aware switch (plan-10 / T1) so the
  // login gate and this page can never diverge (the old purge-only path here leaked the
  // server-held fleet design into localStorage). canvasFromServer drives the confirm copy.
  const switchToLocal = useControllerStore((s) => s.switchToLocal);
  const canvasFromServer = useTopologyStore((s) => s.canvasFromServer);
  // controller→local is a LOSSY switch (plan-5, D6): confirm before purging.
  const [showSwitchToLocal, setShowSwitchToLocal] = useState(false);
  const theme = useUiStore((s) => s.theme);
  const setTheme = useUiStore((s) => s.setTheme);
  const translucency = useUiStore((s) => s.translucency);
  const setTranslucency = useUiStore((s) => s.setTranslucency);
  const applyServerTranslucency = useUiStore((s) => s.applyServerTranslucency);
  const settings = useControllerStore((s) => s.settings);
  const saveSettings = useControllerStore((s) => s.saveSettings);

  // Translucency is applied via uiStore (the appearance source ThemeProvider reads). In
  // controller mode the server is the source of truth: set only the EFFECTIVE value
  // (applyServerTranslucency leaves the user's local preference intact — A3) and persist
  // to the server. In local mode setTranslucency records the local preference.
  const onTranslucencyChange = (on: boolean) => {
    if (mode === 'controller') {
      applyServerTranslucency(on);
      if (settings) void saveSettings({ ...settings, translucency: on });
    } else {
      setTranslucency(on);
    }
  };

  const seg = (selected: boolean) =>
    `px-3 py-1.5 text-sm ${
      selected ? 'bg-blue-600 text-white' : 'text-gray-300 hover:bg-gray-600'
    }`;

  // Local-mode click while in controller mode: open the lossy-switch confirm dialog
  // (D6). Switching the other way (or re-selecting the current mode) is a no-op flip;
  // local→controller needs no purge — the login gate + server hydration take over.
  const onSelectLocal = () => {
    if (mode === 'controller') {
      setShowSwitchToLocal(true);
    } else {
      setMode('local');
    }
  };
  const confirmSwitchToLocal = () => {
    // Shared serverHeld-aware switch (plan-10 / T1): flushes a server-held mirror (no fleet
    // secret leak) or purges local-original work (graph survives), clears notices, restores
    // local translucency, sets mode=local — identical to the login-gate path.
    switchToLocal();
    setShowSwitchToLocal(false);
  };

  const themeOptions: ReadonlyArray<{ value: ThemePref; labelKey: MessageKey }> = [
    { value: 'system', labelKey: 'themeSystem' },
    { value: 'light', labelKey: 'themeLight' },
    { value: 'dark', labelKey: 'themeDark' },
  ];

  return (
    <div className="h-full overflow-y-auto bg-gray-900 text-gray-100 p-6 space-y-6">
      <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
        <h3 className="text-lg font-semibold text-blue-400">
          {t(language, 'settingsModeHeading')}
        </h3>
        <p className="text-sm text-gray-400">{t(language, 'settingsModeHint')}</p>
        <div className="flex w-fit items-center overflow-hidden rounded border border-gray-600 bg-gray-700">
          <button type="button" onClick={onSelectLocal} className={seg(mode === 'local')}>
            {t(language, 'modeLocal')}
          </button>
          <button
            type="button"
            onClick={() => setMode('controller')}
            className={seg(mode === 'controller')}
          >
            {t(language, 'modeController')}
          </button>
        </div>
      </section>

      {/* controller→local 有损切换确认（plan-5，D6）：具体列出保留什么、清空什么。 */}
      {showSwitchToLocal && (
        <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4" role="dialog" aria-modal="true">
          <div className="w-full max-w-md space-y-4 rounded-lg border border-gray-700 bg-gray-800 p-5">
            <h4 className="text-base font-semibold text-amber-400">
              {t(language, 'settingsPage.switchToLocalMode')}
            </h4>
            {/* Copy forks on canvasFromServer (plan-10 / T1): a server-held mirror is FLUSHED
                (graph not kept) — say so accurately instead of the local-original "graph is
                kept, secrets purged" copy, which would be false for a server mirror. */}
            {canvasFromServer ? (
              <p className="text-sm text-gray-300">
                {t(language, 'settingsPage.serverHeldClearsLocal')}
              </p>
            ) : (
              <>
                <p className="text-sm text-gray-300">
                  {t(language, 'settingsPage.yourDesignGraphIs')}
                </p>
                <ul className="list-disc space-y-1 pl-5 text-sm text-gray-400">
                  <li>{t(language, 'settingsPage.wireguardPublicPrivateKeys')}</li>
                  <li>{t(language, 'settingsPage.allocationPinsOverlayIPs')}</li>
                  <li>{t(language, 'settingsPage.compileHistoryAndThe')}</li>
                </ul>
                <p className="text-xs text-amber-300/80">
                  {t(language, 'settingsPage.thisGuaranteesNoFleet')}
                </p>
              </>
            )}
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setShowSwitchToLocal(false)}
                className="rounded border border-gray-600 px-3 py-1.5 text-sm text-gray-300 hover:bg-gray-700"
              >
                {t(language, 'settingsPage.cancel')}
              </button>
              <button
                type="button"
                onClick={confirmSwitchToLocal}
                className="rounded bg-amber-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-amber-500"
              >
                {t(language, 'settingsPage.switchAndClear')}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Connection (controller endpoints + sign-in) and Bootstrap (agent URLs, GitHub proxy)
          are controller-only configuration — meaningless in local/air-gap mode. Gate them to
          controller mode (plan-11 / T5) so they don't render as dead controls in local mode. */}
      {mode === 'controller' && (
        <>
          <ConnectionSettings />
          <BootstrapSettings />
        </>
      )}

      <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-4 max-w-2xl">
        <h3 className="text-lg font-semibold text-blue-400">
          {t(language, 'settingsAppearanceHeading')}
        </h3>

        {/* Theme — mirrors the top-right toggle (client-persisted, per device). */}
        <div className="space-y-1">
          <label className="text-xs text-gray-400">{t(language, 'appearanceTheme')}</label>
          <div className="flex w-fit items-center overflow-hidden rounded border border-gray-600 bg-gray-700">
            {themeOptions.map((opt) => (
              <button
                key={opt.value}
                type="button"
                onClick={() => setTheme(opt.value)}
                aria-pressed={theme === opt.value}
                className={seg(theme === opt.value)}
              >
                {t(language, opt.labelKey)}
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
              // In controller mode the server is the source of truth; until settings load
              // a toggle could not be persisted (and would be clobbered by the server
              // value), so gate on settings being present.
              disabled={mode === 'controller' && !settings}
            />
            {t(language, 'appearanceTranslucency')}
          </label>
          <p className="text-xs text-gray-500">
            {t(language, 'appearanceTranslucencyHint')}
          </p>
        </div>
      </section>
    </div>
  );
}
