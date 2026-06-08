import {
  useControllerStore,
  selectRekeyingCount,
  selectOperatorEnrolled,
  selectHasAuth,
} from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

// 部署条：把当前拓扑发布到 fleet。controllerStore.deploy() 串联
// update-topology → stage →（KEYSTONE 签名）→ promote → refresh，整套 promote-to-fleet。
// 这里负责触发、回显结果（staged / skippedUnenrolled / generation）+ 错误，并提供
// off-host operator 签名密钥（passkey / YubiKey）的注册入口与「触碰安全密钥」提示。
// KEYSTONE 签名钩子（plan-5.1d）在 store 内部、stage 之后 promote 之前（仅当节点要求签名）。
export function DeployBar() {
  const language = useTopologyStore((s) => s.language);

  const deploy = useControllerStore((s) => s.deploy);
  const rollKeys = useControllerStore((s) => s.rollKeys);
  const enrollOperator = useControllerStore((s) => s.enrollOperator);
  const loading = useControllerStore((s) => s.loading);
  const signing = useControllerStore((s) => s.signing);
  const enrolling = useControllerStore((s) => s.enrolling);
  const error = useControllerStore((s) => s.error);
  const lastDeploy = useControllerStore((s) => s.lastDeploy);
  // 是否已 pin off-host 签名凭据（决定签名区回显与提示）。
  const operatorEnrolled = useControllerStore(selectOperatorEnrolled);
  const operatorCredentialAlg = useControllerStore((s) => s.operatorCredentialAlg);
  // 仍处于 rekey_requested 的节点数：>0 时禁用 Deploy（见下方说明）。
  const rekeyingCount = useControllerStore(selectRekeyingCount);

  // 未登录且未填 break-glass token 时无法发起 operator 请求，禁用按钮并给出提示。
  // 用 selectHasAuth（session || token），不能只看 operatorToken——否则登录后会被误拦。
  const noAuth = !useControllerStore(selectHasAuth);

  // 轮换收口前（仍有节点 rekey_requested）禁用 Deploy：此时各 agent 尚未全部重生密钥并
  // 重新注册新公钥，若此刻 Deploy 会用「旧+新」混合公钥重编译，导致 fleet 收敛错乱。
  // 待所有「轮换中」徽标消失（节点已重新注册）后再 Deploy 一次即可收敛。
  const anyRekeying = rekeyingCount > 0;

  // 「Roll keys」是 plan-4.6 ROUTINE tier 的全 fleet 密钥轮换：标记每个已审批节点 rekey，
  // 各 agent 会自行重生本地 WG 私钥并注册新公钥（控制器从不接触私钥）。操作不可一键完成
  // ——节点重新注册后还需再 Deploy 一次才会收敛——故先 confirm 再触发。
  const onRollKeys = () => {
    const ok = window.confirm(
      txt(
        language,
        '将为整个 fleet 请求 WireGuard 密钥轮换。流程：① 各节点重生本地私钥并向控制器重新注册新公钥（注册表里的「🔑 轮换中」徽标随之逐个消失）；② 待所有徽标消失后，再「发布」一次——新一代配置携带全员新公钥，使 fleet 收敛（滚动应用期间各链路会短暂抖动）。请勿在仍有节点轮换中时发布。是否继续？',
        'This requests a WireGuard key rotation across the whole fleet. Sequence: (1) each node regenerates its local private key and re-registers a new public key with the controller (its "🔑 rekeying" badge in the registry clears one by one); (2) once every badge has cleared, Deploy once — the new generation carries everyone’s new public keys and the fleet converges (links flap briefly during the rolling apply). Do not Deploy while any node is still rotating. Continue?',
      ),
    );
    if (ok) {
      rollKeys();
    }
  };

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold text-teal-400">
          {txt(language, '发布到 Fleet', 'Deploy to Fleet')}
        </h3>
        <div className="flex items-center gap-2">
          <button
            onClick={onRollKeys}
            disabled={loading || noAuth}
            className="px-4 py-1.5 text-sm bg-purple-700 hover:bg-purple-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {txt(language, '🔑 轮换密钥', '🔑 Roll keys')}
          </button>
          <button
            onClick={() => deploy()}
            disabled={loading || noAuth || anyRekeying}
            title={
              anyRekeying
                ? txt(
                    language,
                    `${rekeyingCount} 个节点仍在轮换密钥——待全部重新注册后再发布`,
                    `${rekeyingCount} node(s) still rotating keys — Deploy when all have re-registered`,
                  )
                : undefined
            }
            className="px-4 py-1.5 text-sm bg-teal-600 hover:bg-teal-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {loading
              ? txt(language, '发布中...', 'Deploying...')
              : txt(language, '🚀 发布', '🚀 Deploy')}
          </button>
        </div>
      </div>

      <p className="text-sm text-gray-400">
        {txt(
          language,
          '将当前拓扑上传到控制器，编译已注册节点的子图，并提升为新一代配置（已注册节点会自动拉取）。',
          'Upload the current topology, compile the enrolled subgraph, and promote it as a new generation (enrolled nodes pull automatically).',
        )}
      </p>

      <p className="text-xs text-purple-300/80">
        {txt(
          language,
          '「轮换密钥」会请求各节点重生 WireGuard 密钥；待节点重新注册新公钥后，再「发布」一次以使 fleet 收敛。',
          'Roll keys asks each node to regenerate its WireGuard key; once nodes re-register their new public keys, Deploy once more to converge the fleet.',
        )}
      </p>

      {/* KEYSTONE（plan-5.1d）：off-host operator 签名密钥（passkey / YubiKey）。
          回显是否已注册 + 注册按钮；并提示 keystone 开启时 Deploy 会要求触碰安全密钥。 */}
      <div className="p-3 bg-gray-900 border border-gray-700 rounded space-y-2">
        <div className="flex items-center justify-between gap-2">
          <h4 className="text-sm font-semibold text-amber-300">
            {txt(language, '🔐 操作员签名密钥', '🔐 Operator signing key')}
          </h4>
          {operatorEnrolled ? (
            <span className="text-xs text-green-300 bg-green-900/20 px-2 py-0.5 rounded">
              {txt(language, '已注册', 'Enrolled')}
              {operatorCredentialAlg ? ` (${operatorCredentialAlg})` : ''}
            </span>
          ) : (
            <span className="text-xs text-gray-400 bg-gray-800 px-2 py-0.5 rounded">
              {txt(language, '未注册', 'Not enrolled')}
            </span>
          )}
        </div>
        <p className="text-xs text-gray-400">
          {txt(
            language,
            '在浏览器外（passkey / YubiKey）pin 一个签名凭据，用于为每次发布的成员清单签名。私钥永不离开你的安全密钥；控制器只保存它的公钥。',
            'Pin an off-host credential (passkey / YubiKey) used to sign each deploy’s trust-list. The private key never leaves your security key; the controller stores only its public key.',
          )}
        </p>
        <button
          onClick={() => enrollOperator()}
          disabled={enrolling || loading || noAuth}
          className="px-4 py-1.5 text-sm bg-amber-600 hover:bg-amber-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
        >
          {enrolling
            ? txt(language, '等待安全密钥...', 'Waiting for security key...')
            : operatorEnrolled
              ? txt(language, '🔐 重新注册签名密钥（passkey / YubiKey）', '🔐 Re-enroll signing key (passkey / YubiKey)')
              : txt(language, '🔐 注册签名密钥（passkey / YubiKey）', '🔐 Enroll signing key (passkey / YubiKey)')}
        </button>
        <p className="text-[10px] text-gray-500">
          {txt(
            language,
            'keystone 开启时，发布会要求你触碰一次安全密钥以授权本次部署。',
            'When the keystone is on, Deploy will prompt for a tap on your security key to authorize the deploy.',
          )}
        </p>
      </div>

      {/* WebAuthn 提示弹出、等待用户触碰安全密钥时的醒目提示。文案区分 enroll（注册签名
          密钥，此时并无部署在进行）与 deploy 签名（授权本次部署）两种 ceremony。 */}
      {(signing || enrolling) && (
        <p className="text-sm text-amber-200 bg-amber-900/30 border border-amber-700/50 px-3 py-2 rounded animate-pulse">
          {enrolling
            ? txt(
                language,
                '👆 请触碰你的安全密钥以注册签名密钥...',
                '👆 Touch your security key to enroll your signing key...',
              )
            : txt(
                language,
                '👆 请触碰你的安全密钥以授权本次部署...',
                '👆 Touch your security key to authorize this deploy...',
              )}
        </p>
      )}

      {noAuth && (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {txt(
            language,
            '请先在上方登录（或填写 break-glass Operator Token）。',
            'Sign in above first (or enter the break-glass operator token).',
          )}
        </p>
      )}

      {anyRekeying && (
        <p className="text-xs text-purple-300 bg-purple-900/20 px-2 py-1 rounded">
          {txt(
            language,
            `${rekeyingCount} 个节点仍在轮换密钥——待全部重新注册后再发布（否则会用旧+新混合公钥重编译）。`,
            `${rekeyingCount} node(s) still rotating keys — Deploy when all have re-registered (deploying now would recompile with mixed old+new public keys).`,
          )}
        </p>
      )}

      {error && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">
          ⚠️ {error}
        </p>
      )}

      {lastDeploy && (
        <div className="p-3 bg-gray-900 border border-gray-700 rounded space-y-2 text-sm">
          <p className="text-gray-300">
            {txt(language, '最近一次发布', 'Last deploy')} —{' '}
            <span className="font-mono text-cyan-300">
              {txt(language, '代号', 'generation')} {lastDeploy.generation}
            </span>
          </p>
          <div>
            <p className="text-xs text-gray-400">
              {txt(language, '已编译节点', 'Staged nodes')} ({lastDeploy.staged.length})
            </p>
            {lastDeploy.staged.length === 0 ? (
              <p className="text-xs text-gray-500 italic">{txt(language, '（无）', '(none)')}</p>
            ) : (
              <p className="text-xs text-green-300 font-mono break-all">
                {lastDeploy.staged.join(', ')}
              </p>
            )}
          </div>
          {lastDeploy.skippedUnenrolled.length > 0 && (
            <div>
              <p className="text-xs text-gray-400">
                {txt(language, '因未注册被跳过', 'Skipped (unenrolled)')} (
                {lastDeploy.skippedUnenrolled.length})
              </p>
              <p className="text-xs text-yellow-300 font-mono break-all">
                {lastDeploy.skippedUnenrolled.join(', ')}
              </p>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
