import { useEffect, useState } from 'react';
import type { ControllerSettings } from '../../api/controllerClient';
import { useControllerStore, selectHasAuth } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt, type UILanguage } from '../../i18n';

// Bootstrap 设置（plan-5.2）：服务端持久化的 public agent URL / GitHub 代理 / agent 发布
// 基址。它们被烘焙进 GET /bootstrap 返回的一键安装脚本的默认值里。仅操作员可改。
//
// 设计：父组件负责加载/保存（store 动作），用 settings 作为 key 渲染一个受控表单子组件，
// 子组件用 props 惰性初始化本地输入——这样无需在 effect 里 setState 同步服务端值
// （settings 变化时子组件 remount 并从新值重新初始化）。
export function BootstrapSettings() {
  const language = useTopologyStore((s) => s.language);
  const settings = useControllerStore((s) => s.settings);
  const loadSettings = useControllerStore((s) => s.loadSettings);
  const saveSettings = useControllerStore((s) => s.saveSettings);
  const loading = useControllerStore((s) => s.loading);
  const hasAuth = useControllerStore(selectHasAuth);

  // 首次有鉴权且尚未加载时拉取一次（loadSettings 是 store 动作，不是 useState setter）。
  useEffect(() => {
    if (hasAuth && settings === null) {
      void loadSettings();
    }
  }, [hasAuth, settings, loadSettings]);

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <h3 className="text-lg font-semibold text-emerald-400">
        {txt(language, 'Bootstrap 设置', 'Bootstrap Settings')}
      </h3>
      <p className="text-sm text-gray-400">
        {txt(
          language,
          '这些设置由服务端持久化，并烘焙进「一键安装」脚本的默认值。修改后保存即对后续注册生效。',
          'These are persisted server-side and baked into the one-shot install script. Save to apply them to subsequent enrollments.',
        )}
      </p>
      {!hasAuth ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {txt(language, '请先登录以读取/修改设置。', 'Sign in to read/edit settings.')}
        </p>
      ) : (
        <SettingsForm
          key={settings ? JSON.stringify(settings) : 'empty'}
          initial={
            settings ?? { publicAgentURL: '', githubProxy: '', agentReleaseBaseURL: '', translucency: true }
          }
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
  onSave: (s: ControllerSettings) => Promise<void>;
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
          txt(language, '公开 Agent 地址', 'Public Agent URL'),
          publicAgentURL,
          setPublicAgentURL,
          'https://overlay.example.com:9090',
          txt(
            language,
            '节点可达的 agent 端口地址（一键脚本从这里 curl，agent 也连到这里）。',
            'The node-reachable agent URL the install script curls from and the agent connects to.',
          ),
        )}
        {field(
          txt(language, 'GitHub 代理（可选）', 'GitHub Proxy (optional)'),
          githubProxy,
          setGithubProxy,
          'https://gh-proxy.com/',
          txt(
            language,
            '为 GitHub 下载加前缀（如 gh-proxy.com/https://github.com/...）。留空=关闭。',
            'Prefix for GitHub downloads (e.g. gh-proxy.com/https://github.com/...). Empty = off.',
          ),
        )}
        {field(
          txt(language, 'Agent 发布基址', 'Agent Release Base URL'),
          agentReleaseBaseURL,
          setAgentReleaseBaseURL,
          'https://github.com/.../releases/latest/download',
          txt(
            language,
            'per-arch 的 yaog-agent 二进制下载基址。一般无需改。',
            'Where the per-arch yaog-agent binary is fetched from. Usually leave as default.',
          ),
        )}
      </div>
      <button
        onClick={() =>
          void onSave({
            publicAgentURL: publicAgentURL.trim(),
            githubProxy: githubProxy.trim(),
            agentReleaseBaseURL: agentReleaseBaseURL.trim(),
            // Preserve the appearance setting — this form edits only the bootstrap fields.
            translucency: initial.translucency,
          })
        }
        disabled={loading}
        className="mt-3 px-4 py-1.5 text-sm bg-emerald-600 hover:bg-emerald-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
      >
        {txt(language, '保存设置', 'Save settings')}
      </button>
    </>
  );
}
