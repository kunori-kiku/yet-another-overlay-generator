import { useEffect, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { txt } from '../../i18n';
import type { TOTPEnrollment } from '../../api/controllerClient';

// 两步验证（TOTP 2FA，plan-5.2）：让已用密码登录的 operator 自助开启/关闭一个时间口令第二因子。
// 仅对密码 session 可用——break-glass token 无账户（后端 currentOperator 返回 403），此时
// totpEnabled 保持 null，UI 提示「请用密码登录」。
//
// 录入方式（无二维码依赖、绝不外泄密钥）：展示 otpauth:// URI + 分组的 base32 setup key，
// 操作员在验证器里「手动输入密钥」即可。二维码会引入额外依赖，且把密钥发往第三方 QR 服务
// 是泄密——故有意不做。TOTP 仅用于登录，绝非签名机制（见 docs/spec/controller/operator-auth.md）。
export function TwoFactorSettings() {
  const language = useTopologyStore((s) => s.language);
  const loggedIn = useControllerStore(selectLoggedIn);
  const totpEnabled = useControllerStore((s) => s.totpEnabled);
  const loadTOTPStatus = useControllerStore((s) => s.loadTOTPStatus);
  const enrollTOTP = useControllerStore((s) => s.enrollTOTP);
  const confirmTOTP = useControllerStore((s) => s.confirmTOTP);
  const disableTOTP = useControllerStore((s) => s.disableTOTP);

  // enroll ceremony 的本地状态：pending=刚 mint 的 secret+uri（确认前不持久化），code=验证码
  // 输入（confirm 与 disable 共用），busy=请求中，localError=就地错误（不污染全局 banner）。
  const [pending, setPending] = useState<TOTPEnrollment | null>(null);
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [localError, setLocalError] = useState<string | null>(null);
  const [copied, setCopied] = useState<'' | 'secret' | 'uri'>('');

  // 已登录但状态未知时拉取一次（loadTOTPStatus 是 store 动作，不是 useState setter——与
  // BootstrapSettings 的 loadSettings 守卫 effect 同型，不触发 set-state-in-effect）。
  useEffect(() => {
    if (loggedIn && totpEnabled === null) {
      void loadTOTPStatus();
    }
  }, [loggedIn, totpEnabled, loadTOTPStatus]);

  // 只保留数字、最多 6 位（TOTP 是 6 位十进制码）。
  const onCodeChange = (v: string) => setCode(v.replace(/\D/g, '').slice(0, 6));

  const handleEnroll = async () => {
    setBusy(true);
    setLocalError(null);
    setCode('');
    setCopied('');
    try {
      setPending(await enrollTOTP());
    } catch (err) {
      setLocalError(err instanceof Error ? err.message : 'Failed to start 2FA enrollment');
    } finally {
      setBusy(false);
    }
  };

  const handleConfirm = async () => {
    if (!pending || code.length < 6) return;
    setBusy(true);
    setLocalError(null);
    try {
      await confirmTOTP(pending.secret, code);
      // 激活成功：丢弃 pending（密钥已落服务端），清空输入。totpEnabled 由 store 置 true。
      setPending(null);
      setCode('');
    } catch (err) {
      setLocalError(err instanceof Error ? err.message : 'Failed to enable 2FA');
    } finally {
      setBusy(false);
    }
  };

  const handleCancelEnroll = () => {
    // 放弃未确认的 enroll：服务端从未持久化该密钥，纯本地丢弃即可。
    setPending(null);
    setCode('');
    setLocalError(null);
    setCopied('');
  };

  const handleDisable = async () => {
    if (code.length < 6) return;
    setBusy(true);
    setLocalError(null);
    try {
      await disableTOTP(code);
      setCode('');
    } catch (err) {
      setLocalError(err instanceof Error ? err.message : 'Failed to disable 2FA');
    } finally {
      setBusy(false);
    }
  };

  const copyText = async (text: string, which: 'secret' | 'uri') => {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopied(which);
    } catch {
      // 剪贴板不可用（非安全上下文等）：保持文本可手动选中，不报错。
      setCopied('');
    }
  };

  // 把 base32 密钥每 4 字符分组，便于在验证器里手动誊录（不改变值，仅展示）。
  const groupedSecret = (s: string) => s.replace(/(.{4})/g, '$1 ').trim();

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-sky-400">
        {txt(language, '两步验证 (TOTP)', 'Two-Factor (TOTP)')}
      </h3>
      <p className="text-sm text-gray-400">
        {txt(
          language,
          '为登录增加一个基于验证器 App 的时间口令第二因子。仅用于登录，不用于签名。',
          'Add a time-based one-time code from an authenticator app as a second login factor. Used for login only, never for signing.',
        )}
      </p>

      {!loggedIn ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {txt(
            language,
            '请先用密码登录以管理两步验证（break-glass 令牌没有账户，无法管理 2FA）。',
            'Sign in with your password to manage 2FA (the break-glass token has no account).',
          )}
        </p>
      ) : totpEnabled === null ? (
        <p className="text-xs text-gray-500">{txt(language, '正在读取状态...', 'Checking status...')}</p>
      ) : totpEnabled ? (
        // 已启用：展示状态 + 需当前码才能关闭。
        <div className="space-y-2">
          <p className="text-xs text-green-300 bg-green-900/20 px-2 py-1 rounded">
            {txt(language, '✅ 两步验证已启用。', '✅ Two-factor is enabled.')}
          </p>
          <label className="text-xs text-gray-400 block">
            {txt(language, '关闭两步验证需输入当前验证码：', 'Enter a current code to disable:')}
          </label>
          <div className="flex gap-2">
            <input
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => onCodeChange(e.target.value)}
              placeholder="123456"
              className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm font-mono tracking-widest border border-gray-500 focus:border-blue-400 outline-none"
            />
            <button
              onClick={() => void handleDisable()}
              disabled={busy || code.length < 6}
              className="px-4 py-1.5 text-sm bg-red-700 hover:bg-red-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
            >
              {busy ? txt(language, '处理中...', 'Working...') : txt(language, '关闭 2FA', 'Disable 2FA')}
            </button>
          </div>
        </div>
      ) : pending === null ? (
        // 未启用且未开始 enroll：一个开启按钮。
        <button
          onClick={() => void handleEnroll()}
          disabled={busy}
          className="px-4 py-1.5 text-sm bg-sky-600 hover:bg-sky-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
        >
          {busy ? txt(language, '准备中...', 'Preparing...') : txt(language, '开启两步验证', 'Enable two-factor')}
        </button>
      ) : (
        // enroll 进行中：展示 setup key + otpauth URI，收一个码完成激活。
        <div className="space-y-3 p-3 bg-gray-900 border border-gray-700 rounded">
          <p className="text-xs text-gray-300">
            {txt(
              language,
              '1) 在验证器 App 中「手动输入密钥」或导入下方 otpauth 链接；2) 输入 App 生成的 6 位码并确认。',
              '1) Add the key below to your authenticator (manual entry) or import the otpauth link; 2) enter the 6-digit code it generates and confirm.',
            )}
          </p>
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-gray-500 uppercase tracking-wider">
                {txt(language, '密钥 (Setup key)', 'Setup key')}
              </label>
              <button
                onClick={() => void copyText(pending.secret, 'secret')}
                className="px-2 py-0.5 text-xs bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {copied === 'secret' ? txt(language, '已复制', 'Copied') : txt(language, '复制', 'Copy')}
              </button>
            </div>
            <pre className="text-sm text-sky-200 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded tracking-widest">
              {groupedSecret(pending.secret)}
            </pre>
          </div>
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-gray-500 uppercase tracking-wider">
                {txt(language, 'otpauth 链接', 'otpauth link')}
              </label>
              <button
                onClick={() => void copyText(pending.otpauthURI, 'uri')}
                className="px-2 py-0.5 text-xs bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {copied === 'uri' ? txt(language, '已复制', 'Copied') : txt(language, '复制', 'Copy')}
              </button>
            </div>
            <pre className="text-[11px] text-gray-400 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {pending.otpauthURI}
            </pre>
          </div>
          <div>
            <label className="text-xs text-gray-400 block mb-1">
              {txt(language, '验证器生成的 6 位码：', '6-digit code from your authenticator:')}
            </label>
            <div className="flex gap-2">
              <input
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                value={code}
                onChange={(e) => onCodeChange(e.target.value)}
                placeholder="123456"
                className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm font-mono tracking-widest border border-gray-500 focus:border-blue-400 outline-none"
              />
              <button
                onClick={() => void handleConfirm()}
                disabled={busy || code.length < 6}
                className="px-4 py-1.5 text-sm bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
              >
                {busy ? txt(language, '验证中...', 'Verifying...') : txt(language, '确认并启用', 'Confirm & enable')}
              </button>
              <button
                onClick={handleCancelEnroll}
                disabled={busy}
                className="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 disabled:bg-gray-700 rounded text-gray-200"
              >
                {txt(language, '取消', 'Cancel')}
              </button>
            </div>
          </div>
        </div>
      )}

      {localError && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">⚠️ {localError}</p>
      )}
    </section>
  );
}
