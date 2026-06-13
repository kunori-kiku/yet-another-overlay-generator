import { useState } from 'react';
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
  const purgeModeBoundaryState = useTopologyStore((s) => s.purgeModeBoundaryState);
  const clearModeNotices = useControllerStore((s) => s.clearModeNotices);
  // controller→local is a LOSSY switch (plan-5, D6): confirm before purging.
  const [showSwitchToLocal, setShowSwitchToLocal] = useState(false);
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
    purgeModeBoundaryState(); // graph survives; keys/pins/compile-history purged
    clearModeNotices(); // drop any controller-mode banners (hydration/strip/shrink)
    setMode('local');
    setShowSwitchToLocal(false);
  };

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
          <button type="button" onClick={onSelectLocal} className={seg(mode === 'local')}>
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

      {/* controller→local 有损切换确认（plan-5，D6）：具体列出保留什么、清空什么。 */}
      {showSwitchToLocal && (
        <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4" role="dialog" aria-modal="true">
          <div className="w-full max-w-md space-y-4 rounded-lg border border-gray-700 bg-gray-800 p-5">
            <h4 className="text-base font-semibold text-amber-400">
              {txt(language, '切换到本地模式？', 'Switch to local mode?')}
            </h4>
            <p className="text-sm text-gray-300">
              {txt(
                language,
                '设计图会保留（项目、网络域、节点与连线）。但以下内容将被清除，下次本地编译时重新生成：',
                'Your design graph is kept (project, domains, nodes, edges). The following will be cleared and regenerated on the next local compile:',
              )}
            </p>
            <ul className="list-disc space-y-1 pl-5 text-sm text-gray-400">
              <li>{txt(language, 'WireGuard 公私钥（重新生成一套新密钥）', 'WireGuard public/private keys (a fresh keypair is generated)')}</li>
              <li>{txt(language, '分配 pin（overlay IP、端口、transit/链路本地地址将重新分配）', 'Allocation pins (overlay IPs, ports, transit/link-local addresses are reassigned)')}</li>
              <li>{txt(language, '编译历史与上次编译/校验结果', 'Compile history and the last compile/validate result')}</li>
            </ul>
            <p className="text-xs text-amber-300/80">
              {txt(
                language,
                '这样可保证舰队（fleet）用过的密钥绝不残留在本地。此操作不可撤销。',
                'This guarantees no fleet-used keys linger locally. This cannot be undone.',
              )}
            </p>
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setShowSwitchToLocal(false)}
                className="rounded border border-gray-600 px-3 py-1.5 text-sm text-gray-300 hover:bg-gray-700"
              >
                {txt(language, '取消', 'Cancel')}
              </button>
              <button
                type="button"
                onClick={confirmSwitchToLocal}
                className="rounded bg-amber-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-amber-500"
              >
                {txt(language, '切换并清除', 'Switch and clear')}
              </button>
            </div>
          </div>
        </div>
      )}

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
              // In controller mode the server is the source of truth; until settings load
              // a toggle could not be persisted (and would be clobbered by the server
              // value), so gate on settings being present.
              disabled={mode === 'controller' && !settings}
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
