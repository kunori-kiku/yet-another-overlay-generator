import { useState } from 'react';
import {
  useControllerStore,
  selectRekeyingCount,
  selectOperatorEnrolled,
  selectHasAuth,
} from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

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
  // 部署后孤儿清单（plan-6）：仍在 fleet 注册表、但不在「刚刚发布的那一代」里的已审批节点。
  const ctlNodes = useControllerStore((s) => s.nodes);
  const revoke = useControllerStore((s) => s.revoke);
  // 缩水部署确认（plan-5）与「已剥离 N 个私钥」提示。
  const pendingShrink = useControllerStore((s) => s.pendingShrink);
  const cancelShrinkConfirm = useControllerStore((s) => s.cancelShrinkConfirm);
  const lastStrippedKeys = useControllerStore((s) => s.lastStrippedKeys);
  const dismissStripNotice = useControllerStore((s) => s.dismissStripNotice);
  const [shrinkTyped, setShrinkTyped] = useState('');
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
      t(language, 'deployBar.thisRequestsAWireGuard'),
    );
    if (ok) {
      rollKeys();
    }
  };

  // Orphans (plan-6): approved fleet nodes that were NOT in the just-promoted
  // generation. Computed against lastDeploy.staged — the node-ids actually deployed —
  // NOT the live canvas (which can drift from what was promoted after a local edit;
  // plan-6 review). They still hold a valid token and poll, but this deploy didn't
  // include them. One-click manual revoke (never automatic — D10). Only meaningful
  // alongside lastDeploy, so the list renders inside that block.
  const deployedIds = new Set(lastDeploy?.staged ?? []);
  const orphans = lastDeploy
    ? ctlNodes.filter((n) => n.status === 'approved' && !deployedIds.has(n.nodeId))
    : [];

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold text-teal-400">
          {t(language, 'deployBar.deployToFleet')}
        </h3>
        <div className="flex items-center gap-2">
          <button
            onClick={onRollKeys}
            disabled={loading || noAuth}
            className="px-4 py-1.5 text-sm bg-purple-700 hover:bg-purple-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {t(language, 'deployBar.rollKeys')}
          </button>
          <button
            onClick={() => deploy()}
            disabled={loading || noAuth || anyRekeying}
            title={
              anyRekeying
                ? t(language, 'deployBar.rekeyingTitle', { count: rekeyingCount })
                : undefined
            }
            className="px-4 py-1.5 text-sm bg-teal-600 hover:bg-teal-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {loading
              ? t(language, 'deployBar.deploying')
              : t(language, 'deployBar.deploy')}
          </button>
        </div>
      </div>

      <p className="text-sm text-gray-400">
        {t(language, 'deployBar.uploadTheCurrentTopology')}
      </p>

      <p className="text-xs text-purple-300/80">
        {t(language, 'deployBar.rollKeysAsksEach')}
      </p>

      {/* KEYSTONE（plan-5.1d）：off-host operator 签名密钥（passkey / YubiKey）。
          回显是否已注册 + 注册按钮；并提示 keystone 开启时 Deploy 会要求触碰安全密钥。 */}
      <div className="p-3 bg-gray-900 border border-gray-700 rounded space-y-2">
        <div className="flex items-center justify-between gap-2">
          <h4 className="text-sm font-semibold text-amber-300">
            {t(language, 'deployBar.operatorSigningKey')}
          </h4>
          {operatorEnrolled ? (
            <span className="text-xs text-green-300 bg-green-900/20 px-2 py-0.5 rounded">
              {t(language, 'deployBar.enrolled')}
              {operatorCredentialAlg ? ` (${operatorCredentialAlg})` : ''}
            </span>
          ) : (
            <span className="text-xs text-gray-400 bg-gray-800 px-2 py-0.5 rounded">
              {t(language, 'deployBar.notEnrolled')}
            </span>
          )}
        </div>
        <p className="text-xs text-gray-400">
          {t(language, 'deployBar.pinAnOffHost')}
        </p>
        <button
          onClick={() => enrollOperator()}
          disabled={enrolling || loading || noAuth}
          className="px-4 py-1.5 text-sm bg-amber-600 hover:bg-amber-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
        >
          {enrolling
            ? t(language, 'deployBar.waitingForSecurityKey')
            : operatorEnrolled
              ? t(language, 'deployBar.reEnrollSigningKey')
              : t(language, 'deployBar.enrollSigningKeyPasskey')}
        </button>
        <p className="text-[10px] text-gray-500">
          {t(language, 'deployBar.whenTheKeystoneIs')}
        </p>
      </div>

      {/* WebAuthn 提示弹出、等待用户触碰安全密钥时的醒目提示。文案区分 enroll（注册签名
          密钥，此时并无部署在进行）与 deploy 签名（授权本次部署）两种 ceremony。 */}
      {(signing || enrolling) && (
        <p className="text-sm text-amber-200 bg-amber-900/30 border border-amber-700/50 px-3 py-2 rounded animate-pulse">
          {enrolling
            ? t(language, 'deployBar.touchYourSecurityKey')
            : t(language, 'deployBar.touchYourSecurityKey_2')}
        </p>
      )}

      {noAuth && (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'deployBar.signInAboveFirst')}
        </p>
      )}

      {anyRekeying && (
        <p className="text-xs text-purple-300 bg-purple-900/20 px-2 py-1 rounded">
          {t(language, 'deployBar.rekeyingBanner', { count: rekeyingCount })}
        </p>
      )}

      {error && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">
          ⚠️ {error}
        </p>
      )}

      {/* 已剥离 N 个私钥提示（plan-5，D4）：控制器模式零知识，上传前剥离了私钥。可关闭。 */}
      {lastStrippedKeys > 0 && (
        <div className="flex items-start justify-between gap-2 text-xs text-sky-300 bg-sky-900/20 px-2 py-1 rounded">
          <span>
            {t(language, 'deployBar.strippedKeys', { count: lastStrippedKeys })}
          </span>
          <button
            type="button"
            onClick={dismissStripNotice}
            aria-label={t(language, 'deployBar.dismissNotice')}
            className="shrink-0 px-1 text-sky-400 hover:text-sky-200"
          >
            ✕
          </button>
        </div>
      )}

      {lastDeploy && (
        <div className="p-3 bg-gray-900 border border-gray-700 rounded space-y-2 text-sm">
          <p className="text-gray-300">
            {t(language, 'deployBar.lastDeploy')} —{' '}
            <span className="font-mono text-cyan-300">
              {t(language, 'deployBar.generation')} {lastDeploy.generation}
            </span>
          </p>
          <div>
            <p className="text-xs text-gray-400">
              {t(language, 'deployBar.stagedNodes')} ({lastDeploy.staged.length})
            </p>
            {lastDeploy.staged.length === 0 ? (
              <p className="text-xs text-gray-500 italic">{t(language, 'deployBar.none')}</p>
            ) : (
              <p className="text-xs text-green-300 font-mono break-all">
                {lastDeploy.staged.join(', ')}
              </p>
            )}
          </div>
          {lastDeploy.skippedUnenrolled.length > 0 && (
            <div>
              <p className="text-xs text-gray-400">
                {t(language, 'deployBar.skippedUnenrolled')} (
                {lastDeploy.skippedUnenrolled.length})
              </p>
              <p className="text-xs text-yellow-300 font-mono break-all">
                {lastDeploy.skippedUnenrolled.join(', ')}
              </p>
            </div>
          )}
          {/* plan-6 身份对账：已入网但不在本次设计里的节点。它们没有被部署到，却仍持有令牌
              并在轮询——逐行提供一键「驱逐」（仅手动，绝不自动，D10）。 */}
          {orphans.length > 0 && (
            <div>
              <p className="text-xs text-orange-300">
                {t(language, 'deployBar.enrolledButNotIn')} (
                {orphans.length})
              </p>
              <ul className="mt-1 space-y-1">
                {orphans.map((o) => (
                  <li key={o.nodeId} className="flex items-center justify-between gap-2 bg-orange-900/10 px-2 py-1 rounded">
                    <span className="text-xs text-orange-200 font-mono break-all">{o.nodeId}</span>
                    <button
                      onClick={() => revoke(o.nodeId)}
                      disabled={loading}
                      className="shrink-0 px-2 py-0.5 text-xs bg-red-700 hover:bg-red-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white"
                    >
                      {t(language, 'deployBar.revoke')}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}

      {/* 缩水/清空部署的键入确认（plan-5）：这次发布会把服务端设计大幅缩水（清空或丢弃过半
          节点）。要求键入项目名以确认，防止一次误点把整套 fleet 设计覆盖成空（审计的「一键
          销毁」场景）。版本历史（plan-2）是事后兜底，本守卫是事前预防。 */}
      {pendingShrink && (
        <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4" role="dialog" aria-modal="true">
          <div className="w-full max-w-md space-y-4 rounded-lg border border-red-700 bg-gray-800 p-5">
            <h4 className="text-base font-semibold text-red-400">
              {t(language, 'deployBar.thisDeployShrinksThe')}
            </h4>
            <p className="text-sm text-gray-300">
              {t(language, 'deployBar.shrinkSummary', {
                server: pendingShrink.serverNodeCount,
                canvas: pendingShrink.canvasNodeCount,
              })}
            </p>
            <p className="text-xs text-gray-400">
              {t(language, 'deployBar.shrinkConfirmPrompt', { phrase: pendingShrink.confirmPhrase })}
            </p>
            <input
              type="text"
              value={shrinkTyped}
              onChange={(e) => setShrinkTyped(e.target.value)}
              placeholder={pendingShrink.confirmPhrase}
              autoFocus
              className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-red-400 outline-none"
            />
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => {
                  setShrinkTyped('');
                  cancelShrinkConfirm();
                }}
                className="rounded border border-gray-600 px-3 py-1.5 text-sm text-gray-300 hover:bg-gray-700"
              >
                {t(language, 'deployBar.cancel')}
              </button>
              <button
                type="button"
                disabled={shrinkTyped !== pendingShrink.confirmPhrase || loading}
                onClick={() => {
                  setShrinkTyped('');
                  void deploy({ confirmedShrink: true });
                }}
                className="rounded bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-500 disabled:bg-gray-600 disabled:text-gray-400"
              >
                {t(language, 'deployBar.confirmDeploy')}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
