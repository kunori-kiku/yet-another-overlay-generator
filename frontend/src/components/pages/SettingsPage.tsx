import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { txt, STRINGS } from '../../i18n';
import { ConnectionSettings } from '../deploy/ConnectionSettings';
import { BootstrapSettings } from '../deploy/BootstrapSettings';

// /settings — Mode (local/controller) · Connection (endpoints + sign-in) ·
// Bootstrap · Appearance. Mode persistence + the Appearance controls land in P4.
export function SettingsPage() {
  const language = useTopologyStore((s) => s.language);
  const mode = useControllerStore((s) => s.mode);
  const setMode = useControllerStore((s) => s.setMode);

  const seg = (selected: boolean) =>
    `px-3 py-1.5 text-sm ${
      selected ? 'bg-blue-600 text-white' : 'text-gray-300 hover:bg-gray-600'
    }`;

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

      <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-2 max-w-2xl">
        <h3 className="text-lg font-semibold text-blue-400">
          {txt(language, ...STRINGS.settingsAppearanceHeading)}
        </h3>
        <p className="text-sm text-gray-400">
          {txt(language, ...STRINGS.settingsAppearanceComingSoon)}
        </p>
      </section>
    </div>
  );
}
