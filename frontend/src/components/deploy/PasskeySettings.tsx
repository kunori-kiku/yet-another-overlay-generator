import { useEffect } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { txt } from '../../i18n';

// 登录 Passkey（plan-5.2）：让已用密码登录的 operator 注册/移除一个 WebAuthn 登录 passkey
// ——phishing-resistant 的第二因子，且可用于无密码登录。它与 keystone 的签名 passkey 是不同
// 的凭据（不同的钥匙、不同的用途：这个只用于登录）。仅对密码 session 可用——break-glass token
// 无账户（后端返回 403），此时 passkeyRegistered 保持 null，UI 提示「请用密码登录」。
//
// 注册复用 navigator.credentials.create()（只有公钥离开 authenticator）；移除需要一次新鲜
// 断言（防被劫持的 session 直接摘掉因子）。两者都会弹出 authenticator——signing/enrolling
// 标志驱动「触碰你的安全密钥」提示。
export function PasskeySettings() {
  const language = useTopologyStore((s) => s.language);
  const loggedIn = useControllerStore(selectLoggedIn);
  const passkeyRegistered = useControllerStore((s) => s.passkeyRegistered);
  const loadPasskeyStatus = useControllerStore((s) => s.loadPasskeyStatus);
  const registerPasskey = useControllerStore((s) => s.registerPasskey);
  const disablePasskey = useControllerStore((s) => s.disablePasskey);
  const enrolling = useControllerStore((s) => s.enrolling);
  const signing = useControllerStore((s) => s.signing);

  // 已登录但状态未知时拉取一次（store 动作，非 setState——与 BootstrapSettings 同型）。
  useEffect(() => {
    if (loggedIn && passkeyRegistered === null) {
      void loadPasskeyStatus();
    }
  }, [loggedIn, passkeyRegistered, loadPasskeyStatus]);

  const busy = enrolling || signing;

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-fuchsia-400">
        {txt(language, '登录 Passkey', 'Login Passkey')}
      </h3>
      <p className="text-sm text-gray-400">
        {txt(
          language,
          '注册一个 WebAuthn passkey 作为登录第二因子，并支持「用 passkey 登录」无密码方式。仅用于登录，与 keystone 的签名密钥不同。',
          'Register a WebAuthn passkey as a phishing-resistant second factor — and to sign in passwordless. For login only; distinct from the keystone signing key.',
        )}
      </p>

      {!loggedIn ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {txt(
            language,
            '请先用密码登录以管理登录 passkey（break-glass 令牌没有账户）。',
            'Sign in with your password to manage a login passkey (the break-glass token has no account).',
          )}
        </p>
      ) : passkeyRegistered === null ? (
        <p className="text-xs text-gray-500">{txt(language, '正在读取状态...', 'Checking status...')}</p>
      ) : passkeyRegistered ? (
        <div className="space-y-2">
          <p className="text-xs text-green-300 bg-green-900/20 px-2 py-1 rounded">
            {txt(language, '✅ 已注册登录 passkey。', '✅ A login passkey is registered.')}
          </p>
          <button
            onClick={() => void disablePasskey()}
            disabled={busy}
            className="px-4 py-1.5 text-sm bg-red-700 hover:bg-red-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {signing
              ? txt(language, '请触碰安全密钥...', 'Touch your security key...')
              : txt(language, '移除 passkey', 'Remove passkey')}
          </button>
          <p className="text-[10px] text-gray-500">
            {txt(
              language,
              '移除需要一次新鲜断言（再次触碰你的安全密钥），以防被劫持的会话直接摘掉因子。',
              'Removal requires a fresh assertion (touch your key again) so a hijacked session cannot strip the factor.',
            )}
          </p>
        </div>
      ) : (
        <button
          onClick={() => void registerPasskey()}
          disabled={busy}
          className="px-4 py-1.5 text-sm bg-fuchsia-600 hover:bg-fuchsia-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
        >
          {enrolling
            ? txt(language, '请触碰安全密钥...', 'Touch your security key...')
            : txt(language, '注册登录 passkey', 'Register a login passkey')}
        </button>
      )}
    </section>
  );
}
