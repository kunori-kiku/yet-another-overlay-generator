import { useEffect, useState } from 'react';
import { emptyControllerSettings, type ControllerSettings } from '../../api/controllerClient';
import { useControllerStore, selectHasAuth } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t, type UILanguage } from '../../i18n';

// Bootstrap settings (plan-5.2): the server-persisted public agent URL / GitHub proxy / agent release
// base URL. They are baked into the defaults of the one-shot install script returned by GET
// /bootstrap. Operator-editable only.
//
// Design: the parent component handles loading/saving (store actions) and renders a controlled form
// child keyed on settings, with the child lazily initializing its local inputs from props — so there
// is no need to setState in an effect to sync server values (when settings changes the child remounts
// and reinitializes from the new value).
export function BootstrapSettings() {
  const language = useTopologyStore((s) => s.language);
  const settings = useControllerStore((s) => s.settings);
  const loadSettings = useControllerStore((s) => s.loadSettings);
  const saveSettings = useControllerStore((s) => s.saveSettings);
  const loading = useControllerStore((s) => s.loading);
  const hasAuth = useControllerStore(selectHasAuth);

  // Fetch once on first having auth and not yet loaded (loadSettings is a store action, not a useState
  // setter).
  useEffect(() => {
    if (hasAuth && settings === null) {
      void loadSettings();
    }
  }, [hasAuth, settings, loadSettings]);

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <h3 className="text-lg font-semibold text-emerald-400">
        {t(language, 'bootstrapSettings.bootstrapSettings')}
      </h3>
      <p className="text-sm text-gray-400">
        {t(language, 'bootstrapSettings.theseArePersistedServer')}
      </p>
      {!hasAuth ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'bootstrapSettings.signInToRead')}
        </p>
      ) : (
        <SettingsForm
          key={settings ? JSON.stringify(settings) : 'empty'}
          initial={settings ?? emptyControllerSettings()}
          loading={loading}
          language={language}
          onSave={saveSettings}
        />
      )}
    </section>
  );
}

// SettingsForm is keyed on the loaded settings, so its useState initializes from the
// server values on (re)mount — no setState-in-effect sync needed.
function SettingsForm({
  initial,
  loading,
  language,
  onSave,
}: {
  initial: ControllerSettings;
  loading: boolean;
  language: UILanguage;
  // saveSettings now returns the localized error (or null) so cards can surface failures locally;
  // this form fire-and-forgets it (the global error banner covers it where mounted).
  onSave: (s: ControllerSettings) => Promise<string | null>;
}) {
  const [publicAgentURL, setPublicAgentURL] = useState(initial.publicAgentURL);
  const [githubProxy, setGithubProxy] = useState(initial.githubProxy);
  const [agentReleaseBaseURL, setAgentReleaseBaseURL] = useState(initial.agentReleaseBaseURL);

  const field = (
    label: string,
    value: string,
    set: (v: string) => void,
    placeholder: string,
    hint: string,
  ) => (
    <div>
      <label className="text-xs text-gray-400">{label}</label>
      <input
        type="text"
        value={value}
        onChange={(e) => set(e.target.value)}
        placeholder={placeholder}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
      />
      <p className="text-[10px] text-gray-500 mt-0.5">{hint}</p>
    </div>
  );

  return (
    <>
      <div className="grid grid-cols-1 gap-3">
        {field(
          t(language, 'bootstrapSettings.publicAgentURL'),
          publicAgentURL,
          setPublicAgentURL,
          'https://overlay.example.com:9090',
          t(language, 'bootstrapSettings.theNodeReachableAgent'),
        )}
        {field(
          t(language, 'bootstrapSettings.githubProxyOptional'),
          githubProxy,
          setGithubProxy,
          'https://gh-proxy.com/',
          t(language, 'bootstrapSettings.prefixForGitHubDownloads'),
        )}
        {field(
          t(language, 'bootstrapSettings.agentReleaseBaseURL'),
          agentReleaseBaseURL,
          setAgentReleaseBaseURL,
          'https://github.com/.../releases/latest/download',
          t(language, 'bootstrapSettings.whereThePerArch'),
        )}
      </div>
      <button
        onClick={() =>
          // Spread ...initial first: POST /settings is FULL-REPLACE, so this form (which edits
          // only the three bootstrap fields) MUST carry every other persisted field — the rollout
          // + mimic config, translucency, the read-only agentPathPrefix — through untouched, or
          // saving here would wipe them.
          void onSave({
            ...initial,
            publicAgentURL: publicAgentURL.trim(),
            githubProxy: githubProxy.trim(),
            agentReleaseBaseURL: agentReleaseBaseURL.trim(),
          })
        }
        disabled={loading}
        className="mt-3 px-4 py-1.5 text-sm bg-emerald-600 hover:bg-emerald-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
      >
        {t(language, 'bootstrapSettings.saveSettings')}
      </button>
    </>
  );
}
