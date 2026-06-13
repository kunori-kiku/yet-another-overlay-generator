import { useEffect, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { t } from '../../i18n';

// 登录 Passkey（plan-5.2）：让已用密码登录的 operator 注册/移除一个 WebAuthn 登录 passkey
// ——phishing-resistant 的第二因子，且可用于无密码登录。它与 keystone 的签名 passkey 是不同
// 的凭据（不同的钥匙、不同的用途：这个只用于登录）。仅对密码 session 可用——break-glass token
// 无账户（后端返回 403），此时 passkeyRegistered 保持 null，UI 提示「请用密码登录」。
//
// 注册复用 navigator.credentials.create()（只有公钥离开 authenticator）；移除需要一次新鲜
// 断言（防被劫持的 session 直接摘掉因子）。两者都会弹出 authenticator——loginCeremony 标志
// 驱动「触碰你的安全密钥」提示（与 keystone 的 signing/enrolling 分开，不点亮 DeployBar 部署
// 横幅）。错误就地展示（store 动作向此处抛出），与 TwoFactorSettings 的本地错误一致。
export function PasskeySettings() {
  const language = useTopologyStore((s) => s.language);
  const loggedIn = useControllerStore(selectLoggedIn);
  const passkeyRegistered = useControllerStore((s) => s.passkeyRegistered);
  const loadPasskeyStatus = useControllerStore((s) => s.loadPasskeyStatus);
  const registerPasskey = useControllerStore((s) => s.registerPasskey);
  const disablePasskey = useControllerStore((s) => s.disablePasskey);
  const loginCeremony = useControllerStore((s) => s.loginCeremony);

  const [localError, setLocalError] = useState<string | null>(null);

  // 已登录但状态未知时拉取一次（store 动作，非 setState——与 BootstrapSettings 同型）。
  useEffect(() => {
    if (loggedIn && passkeyRegistered === null) {
      void loadPasskeyStatus();
    }
  }, [loggedIn, passkeyRegistered, loadPasskeyStatus]);

  const handleRegister = async () => {
    setLocalError(null);
    try {
      await registerPasskey();
    } catch (err) {
      setLocalError(err instanceof Error ? err.message : 'Failed to register passkey');
    }
  };

  const handleDisable = async () => {
    setLocalError(null);
    try {
      await disablePasskey();
    } catch (err) {
      setLocalError(err instanceof Error ? err.message : 'Failed to disable passkey');
    }
  };

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-fuchsia-400">
        {t(language, 'passkeySettings.loginPasskey')}
      </h3>
      <p className="text-sm text-gray-400">
        {t(language, 'passkeySettings.registerAWebAuthnPasskey')}
      </p>

      {!loggedIn ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'passkeySettings.signInWithYour')}
        </p>
      ) : passkeyRegistered === null ? (
        <p className="text-xs text-gray-500">{t(language, 'passkeySettings.checkingStatus')}</p>
      ) : passkeyRegistered ? (
        <div className="space-y-2">
          <p className="text-xs text-green-300 bg-green-900/20 px-2 py-1 rounded">
            {t(language, 'passkeySettings.aLoginPasskeyIs')}
          </p>
          <button
            onClick={() => void handleDisable()}
            disabled={loginCeremony}
            className="px-4 py-1.5 text-sm bg-red-700 hover:bg-red-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {loginCeremony
              ? t(language, 'passkeySettings.touchYourSecurityKey')
              : t(language, 'passkeySettings.removePasskey')}
          </button>
          <p className="text-[10px] text-gray-500">
            {t(language, 'passkeySettings.removalRequiresAFresh')}
          </p>
        </div>
      ) : (
        <button
          onClick={() => void handleRegister()}
          disabled={loginCeremony}
          className="px-4 py-1.5 text-sm bg-fuchsia-600 hover:bg-fuchsia-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
        >
          {loginCeremony
            ? t(language, 'passkeySettings.touchYourSecurityKey_2')
            : t(language, 'passkeySettings.registerALoginPasskey')}
        </button>
      )}

      {localError && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">⚠️ {localError}</p>
      )}
    </section>
  );
}
