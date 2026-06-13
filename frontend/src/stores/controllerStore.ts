import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../types/controller';
import type {
  ControllerConfig,
  WebAuthnAlg,
  ControllerSettings,
  TOTPEnrollment,
  MintTokenResult,
} from '../api/controllerClient';
import {
  getNodes,
  getAudit,
  mintEnrollmentToken,
  updateTopology,
  stage,
  promote,
  revoke,
  rekeyAll,
  getTrustlist,
  postTrustlistSignature,
  postOperatorCredential,
  login as ctlLogin,
  logout as ctlLogout,
  getSettings,
  postSettings,
  getTOTPStatus,
  enrollTOTP as ctlEnrollTOTP,
  confirmTOTP as ctlConfirmTOTP,
  disableTOTP as ctlDisableTOTP,
  getPasskeyStatus,
  registerPasskey as ctlRegisterPasskey,
  disablePasskeyBegin,
  disablePasskeyFinish,
  passkeyLoginBegin,
  passkeyLoginFinish,
  getSession,
  getTopology as ctlGetTopology,
} from '../api/controllerClient';
import type { Topology } from '../types/topology';
import { enrollOperatorCredential, signManifest, assertLogin } from '../lib/webauthn';
import { stripPrivateKeys } from '../lib/custody';
import { useTopologyStore } from './topologyStore';
import { useUiStore } from './uiStore';

// loadSlices projects a Topology down to exactly the fields loadTopology consumes
// (project/domains/nodes/edges + the schema version), so the hydration diff compares
// what an overwrite would actually change — nothing else.
function loadSlices(t: Topology): Record<string, unknown> {
  return {
    project: t.project,
    domains: t.domains,
    nodes: t.nodes,
    edges: t.edges,
    alloc_schema_version: t.alloc_schema_version ?? 0,
  };
}

// stableStringify serializes with recursively-sorted object keys so two structurally
// equal designs compare equal regardless of key order (the server's canonical JSON vs
// the panel's getTopology() key order). Array order is preserved (significant). Used
// only for the hydration diff; being conservative (a reorder reads as "differs") is
// safe — it backs up + reloads, never loses data.
function stableStringify(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return '[' + value.map(stableStringify).join(',') + ']';
  const obj = value as Record<string, unknown>;
  return (
    '{' +
    Object.keys(obj)
      .sort()
      .map((k) => JSON.stringify(k) + ':' + stableStringify(obj[k]))
      .join(',') +
    '}'
  );
}

// 控制器面板（Mode B）状态。它是 controller 连接 + fleet 视图的单一来源，独立于
// topologyStore（后者仍是拓扑数据的唯一来源）。deploy() 时从 topologyStore 读取当前
// 拓扑并复用 compile() 发送的同一 model.Topology JSON 形状。
interface ControllerState {
  // 连接配置（baseURL/pathPrefix/operatorToken 组成 ControllerConfig；agentBaseURL 是
  // EnrollmentFlow 给节点用的 agent 端口地址，仅作展示，不参与 operator 请求构造）。
  baseURL: string;
  pathPrefix: string;
  agentBaseURL: string;
  // operatorToken 是可选的 BREAK-GLASS 令牌（恢复用），仅在内存中。日常鉴权用密码登录
  // 换来的 session（sessionToken）。两者都不持久化（密钥不落 localStorage）。
  operatorToken: string;

  // 工作流模式：local（本地/手动）或 controller（控制器机群）。P2 把它从 DeployPanel 的
  // useState 提升到 store，供各路由页读取（导航可见性 / 落地页 / 部署区分流）。P2 仅存内存
  // （刷新回到 local，与原 useState 行为一致）；P4 把它加入 partialize 实现持久化。
  mode: 'local' | 'controller';
  setMode: (mode: 'local' | 'controller') => void;

  // 登录会话（plan-5.2 + appshell-P5）：密码登录后服务端签发的 bearer session token，仅在
  // 内存中保存，绝不落 localStorage。P5 起 session 同时写入 httpOnly cookie，刷新后由
  // checkSession() 经 GET /session 探测恢复登录态（loggedIn），无需在 JS 里读 token。
  // operatorName/sessionExpiresAt 仅用于回显「已登录为 X，到期时间」。
  sessionToken: string;
  operatorName: string | null;
  sessionExpiresAt: string | null;
  // csrfToken 是双提交 CSRF 令牌（来自 login / GET /session 响应），仅内存，绝不持久化。
  // 在 cookie 鉴权的状态改写请求上作为 X-CSRF-Token 头回显（见 configOf）。
  csrfToken: string;
  // loggedIn 由 GET /session 探测派生：cookie 会话在刷新后仍有效时为真（此时 sessionToken
  // 已随内存丢失）。selectLoggedIn = sessionToken !== '' || loggedIn。
  loggedIn: boolean;

  // TOTP 2FA（plan-5.2）：totpRequired 表示上次登录密码正确但缺/错二次码（后端 401
  // totp_required），登录表单据此显示验证码输入框。totpEnabled 是当前已登录 operator 账户
  // 是否启用了 2FA（null=未知/未拉取；break-glass token 无账户 → 状态保持 null）。两者均仅
  // 内存，绝不持久化。
  totpRequired: boolean;
  totpEnabled: boolean | null;

  // 登录 passkey（plan-5.2）：当前已登录 operator 是否注册了登录 passkey（null=未知/未拉取；
  // break-glass token 无账户 → 保持 null）。passkey 2FA 步骤不需要单独的 *Required 标志：
  // login() 收到 passkey_required 后会就地弹出 authenticator 并重提交（signing 标志驱动 UI）。
  passkeyRegistered: boolean | null;

  // fleet 视图
  nodes: ControllerNode[];
  audit: ControllerAuditEntry[];
  auditVerified: boolean;
  lastDeploy: StageResult | null;

  // bootstrap 设置（plan-5.2，服务端持久化）：public agent URL / GitHub 代理 / agent 发布
  // 基址。null 表示尚未从服务端加载（refresh 时拉取）。
  settings: ControllerSettings | null;

  // 服务端权威 hydration（plan-4，D1）：每次登录/会话恢复后用 GET /topology 覆盖本地画布。
  // 当一次覆盖会丢弃「非空且与服务端不同」的本地设计时，先下载 pre-hydration-backup-<date>
  // .json 作为保险（D9，review 修正：每次分歧覆盖都备份，而非每浏览器一次——后者会静默丢弃
  // 未部署的本地编辑）。hydrationNotice 为真时 Shell 显示一条可关闭提示（用 txt() 实时本地化，
  // 不冻结语言）。
  hydrationNotice: boolean;

  // 部署 custody 与防缩水（plan-5，D4 + 审计「一键销毁」场景）：
  // - lastStrippedKeys：上次 deploy 上传前从画布剥离的私钥数量（0=无提示）。控制器模式零
  //   知识——私钥绝不上送（服务端 plan-1 的 400 在客户端的镜像），剥离后给一条信息提示。
  // - pendingShrink：当一次 deploy 会让服务端设计大幅缩水（清空，或丢弃过半已存在节点）时，
  //   先把待确认信息存这里、暂不部署；DeployBar 弹出「键入项目名确认」对话框，确认后以
  //   confirmedShrink 重新调用 deploy。版本历史（plan-2）是事后兜底，本守卫是事前预防。
  lastStrippedKeys: number;
  // pendingShrink carries the typed-confirm phrase (project name, or a non-empty
  // sentinel when the project is unnamed — an empty phrase would let an empty input
  // match and bypass the gate), the node-count delta for the dialog copy, and a
  // SNAPSHOT of the exact stripped design the warning was computed from (so the
  // confirmed deploy binds to what the operator was warned about, not a since-changed
  // canvas) plus its stripped-key count.
  pendingShrink: {
    serverNodeCount: number;
    canvasNodeCount: number;
    confirmPhrase: string;
    snapshot: Topology;
    stripped: number;
  } | null;

  // KEYSTONE（plan-5.1d）：已 pin 的 off-host operator 签名凭据（passkey / YubiKey）。
  // 仅持久化非密信息——credential_id（base64url(rawId)）、alg、rpId——它们不是密钥材料
  // （私钥从不离开 authenticator），但记住它们让面板能跨刷新驱动后续签名（allowCredentials）
  // 并回显「已注册签名密钥」。未注册时三者为 null。pinned PEM 不在浏览器持久化：签名时
  // public_key 字段是 audit-only，且节点只信任服务端 pin 的 PEM——故无需在前端保留它。
  operatorCredentialId: string | null;
  operatorCredentialAlg: WebAuthnAlg | null;
  operatorRpId: string | null;
  // pinned 公钥 PEM：非密（公钥），持久化它只为把签名 artifact 的 audit-only public_key
  // 字段填成自描述的实际公钥；节点永远只信任服务端 pin 的 PEM，从不信任此字段。
  operatorPublicKeyPEM: string | null;

  // 易失 UI 状态
  loading: boolean;
  error: string | null;
  lastSyncedAt: number | null;
  // signing 为真表示 WebAuthn 提示已弹出、正在等待用户触碰安全密钥（enroll 或 deploy 期间）。
  // UI 用它显示「触碰你的安全密钥」提示。enrolling 区分 enroll 与 deploy-sign 两种 ceremony。
  // 注意：signing/enrolling 专属 KEYSTONE 流程（deploy 签名 / 签名密钥注册），DeployBar 的
  // 「授权本次部署」横幅据此显示。登录 passkey 的 ceremony 用下面独立的 loginCeremony 标志，
  // 以免在登录/注册/移除登录 passkey 时错误点亮「授权部署」横幅。
  signing: boolean;
  enrolling: boolean;
  // loginCeremony 为真表示一次「登录 passkey」WebAuthn 提示进行中（password+passkey 2FA、
  // 无密码登录、注册或移除登录 passkey）。与 keystone 的 signing/enrolling 分开，驱动登录区/
  // 账户安全区的「触碰你的安全密钥」提示，但不触发 DeployBar 的部署横幅。
  loginCeremony: boolean;

  // actions
  setConfig: (partial: Partial<ControllerConfig & { agentBaseURL: string }>) => void;
  loadSettings: () => Promise<void>;
  saveSettings: (s: ControllerSettings) => Promise<void>;
  refresh: () => Promise<void>;
  login: (username: string, password: string, totp?: string) => Promise<void>;
  logout: () => Promise<void>;
  // checkSession probes GET /session to restore login state from the httpOnly cookie
  // after a refresh (P5). Sets loggedIn + operator + expiry + csrfToken, or clears them.
  checkSession: () => Promise<void>;
  // 服务端权威 hydration（plan-4，D1）：GET /topology → loadTopology 覆盖本地画布。404
  //（尚无服务端拓扑——首次部署前）保留本地画布。login/loginWithPasskey/checkSession 的
  // 成功路径都会调用它。覆盖前若本地有「非空且与服务端不同」的设计，先导出一次备份（D9）。
  hydrateFromServer: () => Promise<void>;
  dismissHydrationNotice: () => void;
  // 无密码 passkey 登录（plan-5.2）：begin → assertLogin → finish。
  loginWithPasskey: (username: string) => Promise<void>;
  // 登录 passkey 自助管理（仅密码 session 有效）。
  loadPasskeyStatus: () => Promise<void>;
  registerPasskey: () => Promise<void>;
  disablePasskey: () => Promise<void>;
  // 复位待处理的二次验证步骤：当操作员改动了凭据对（或一次硬失败之后），让验证码框只对
  // 后端实际标记的那一对 username+password 出现，而非粘滞到换了账号/改了密码之后。
  resetTOTPChallenge: () => void;
  // TOTP 2FA 自助管理（plan-5.2）：仅对密码 session 有效（break-glass token 无账户）。
  loadTOTPStatus: () => Promise<void>;
  enrollTOTP: () => Promise<TOTPEnrollment>;
  confirmTOTP: (secret: string, code: string) => Promise<void>;
  disableTOTP: (code: string) => Promise<void>;
  mintToken: (nodeId: string, ttl: number) => Promise<MintTokenResult>;
  enrollOperator: () => Promise<void>;
  // deploy 上传当前画布（先剥离私钥）→ stage → (keystone 签名) → promote。当一次部署会让
  // 服务端设计大幅缩水时，除非 confirmedShrink，否则设置 pendingShrink 并暂不部署（等键入
  // 项目名确认）。
  deploy: (opts?: { confirmedShrink?: boolean }) => Promise<void>;
  // 取消待确认的缩水部署（用户在确认框点了取消）。
  cancelShrinkConfirm: () => void;
  // 关闭「已剥离 N 个私钥」提示。
  dismissStripNotice: () => void;
  // 清掉 controller 模式相关的临时提示（hydration / 剥离 / 待确认缩水）。controller→local
  // 切换时调用，避免本地模式下还残留控制器模式的横幅（plan-5 review）。
  clearModeNotices: () => void;
  revoke: (nodeId: string) => Promise<void>;
  rollKeys: () => Promise<void>;
}

// 从连接字段切出 controllerClient 需要的 ControllerConfig（不含 agentBaseURL）。
// EFFECTIVE bearer = 登录 session 优先，否则 break-glass operatorToken。这样客户端层
// 无需感知会话/令牌的区别——它只附上 operatorToken 字段作为 Bearer。
function configOf(state: ControllerState): ControllerConfig {
  return {
    baseURL: state.baseURL,
    pathPrefix: state.pathPrefix,
    operatorToken: state.sessionToken || state.operatorToken,
    csrfToken: state.csrfToken,
  };
}

// 派生选择器：是否已通过密码登录（持有有效 session）。DeployPanel 用它在登录区切换
// 「登录表单 / 已登录为 X」。break-glass operatorToken 不算「已登录」（它是恢复路径）。
export function selectLoggedIn(state: ControllerState): boolean {
  // sessionToken is the in-memory bearer (this tab's login); loggedIn is derived from the
  // GET /session cookie probe (survives a refresh that drops the in-memory token).
  return state.sessionToken !== '' || state.loggedIn;
}

// 派生选择器：是否持有任一可用的 operator 凭据（登录 session 或 break-glass token）。
// configOf 的 EFFECTIVE bearer 正是 sessionToken || operatorToken，所以任一非空即可发起
// operator 请求。DeployBar 用它决定是否禁用 Deploy/Roll-keys（不能再只看 operatorToken，
// 否则登录后的操作员会被错误地拦住）。
export function selectHasAuth(state: ControllerState): boolean {
  // A cookie-restored session (loggedIn) can make operator requests too — the cookie is
  // attached automatically — so it counts as auth even when the in-memory token is empty.
  return state.sessionToken !== '' || state.loggedIn || state.operatorToken.trim() !== '';
}

// 把标准 base64（带 padding，GET /trustlist 的 trustlist_json 编码）解码回原始字节。
// 这些字节就是 canonical manifest 字节，其 SHA-256 即 WebAuthn challenge。
//
// FOOTGUN: the input MUST be STANDARD (padded) base64 because it pairs with Go's
// base64.StdEncoding on trustlist_json (handler_controller.go ~:1103) — this is a
// DIFFERENT dialect from the base64url (no-pad) used for every SignedTrustList
// field. atob() here requires the std alphabet + padding; if the Go side ever
// switches trustlist_json to base64url, this mis-decodes and the node rejects
// with ErrChallengeMismatch. Keep both sides on std base64 in lockstep.
function base64StdToBytes(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}

// 派生选择器：fleet 中是否仍有节点处于 rekey_requested（已请求轮换、尚未重新注册新公钥）。
// DeployBar 用它在轮换收口前禁用 Deploy——否则中途 Deploy 会用「旧+新」混合公钥重编译，
// 导致 fleet 收敛错乱。返回仍在轮换中的节点数，便于回显「N 个节点仍在轮换密钥」。
export function selectRekeyingCount(state: ControllerState): number {
  // Only APPROVED nodes can re-register (a revoked node never clears its flag), so
  // exclude non-approved to avoid permanently gating Deploy on a stale flag.
  return state.nodes.filter((n) => n.rekeyRequested && n.status === 'approved').length;
}

// 派生选择器：是否已 pin off-host operator 签名凭据。DeployPanel 用它在签名区回显
// 「已注册 / 未注册签名密钥」，并在 keystone 开启而无凭据时给出可执行的报错。
// 要求三者齐备（credential_id + alg + PEM）：deploy() 的签名守卫用同一组字段，从而
// UI 与 deploy 对「已注册」的判定一致，且签名时绝不会发出空的 audit-only public_key。
export function selectOperatorEnrolled(state: ControllerState): boolean {
  return (
    state.operatorCredentialId !== null &&
    state.operatorCredentialAlg !== null &&
    !!state.operatorPublicKeyPEM
  );
}

export const useControllerStore = create<ControllerState>()(
  persist(
    (set, get) => ({
      // 默认连接配置（见 DESIGN：operator 默认 :8080，agent 默认 :9090）。
      baseURL: 'http://localhost:8080',
      pathPrefix: '',
      agentBaseURL: 'http://localhost:9090',
      operatorToken: '',

      mode: 'local',

      sessionToken: '',
      operatorName: null,
      sessionExpiresAt: null,
      csrfToken: '',
      loggedIn: false,

      totpRequired: false,
      totpEnabled: null,
      passkeyRegistered: null,

      nodes: [],
      audit: [],
      auditVerified: false,
      lastDeploy: null,
      settings: null,

      hydrationNotice: false,
      lastStrippedKeys: 0,
      pendingShrink: null,

      operatorCredentialId: null,
      operatorCredentialAlg: null,
      operatorRpId: null,
      operatorPublicKeyPEM: null,

      loading: false,
      error: null,
      lastSyncedAt: null,
      signing: false,
      enrolling: false,
      loginCeremony: false,

      setConfig: (partial) => set(partial),

      setMode: (mode) => set({ mode }),

      // 刷新 fleet 视图：并行拉取 nodes + audit + bootstrap 设置。任一失败则记录 error，
      // 并保持已有视图不变。settings 拉取失败不影响 nodes/audit（best-effort，单独 catch）。
      refresh: async () => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());
          const [nodes, audit] = await Promise.all([getNodes(cfg), getAudit(cfg)]);
          set({
            nodes,
            audit: audit.entries,
            auditVerified: audit.verified,
            loading: false,
            lastSyncedAt: Date.now(),
          });
          // 顺带刷新 bootstrap 设置（不阻塞 fleet 视图；失败保留旧值）。controller 模式下
          // 服务端是 translucency 的权威，拉到后同步到外观 store（与 loadSettings 一致），
          // 避免设置页复选框与服务端值发散。
          try {
            const settings = await getSettings(cfg);
            set({ settings });
            if (get().mode === 'controller') {
              useUiStore.getState().setTranslucency(settings.translucency);
            }
          } catch {
            /* 设置拉取失败：保留已有 settings，不覆盖 fleet 视图的成功状态。 */
          }
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Failed to refresh controller state',
            loading: false,
          });
        }
      },

      // 加载 bootstrap 设置（独立入口，供设置区首次渲染时用）。
      loadSettings: async () => {
        try {
          const settings = await getSettings(configOf(get()));
          set({ settings });
          // In controller mode the server is the source of truth for translucency; apply
          // it to the appearance store. In local mode the client uiStore value stands.
          if (get().mode === 'controller') {
            useUiStore.getState().setTranslucency(settings.translucency);
          }
        } catch (err) {
          set({ error: err instanceof Error ? err.message : 'Failed to load settings' });
        }
      },

      // 保存 bootstrap 设置：POST /settings，回写服务端归一化后的值。
      saveSettings: async (s) => {
        set({ loading: true, error: null });
        try {
          const saved = await postSettings(configOf(get()), s);
          set({ settings: saved, loading: false });
        } catch (err) {
          set({ error: err instanceof Error ? err.message : 'Failed to save settings', loading: false });
        }
      },

      // 操作员密码登录（plan-5.2）：POST /login 换取 session token，仅存内存。成功后立即
      // refresh 拉取 fleet 视图。session 优先于 break-glass token（见 configOf）。失败把
      // 控制器原始报错（401 invalid username or password / 429 too many attempts）回显。
      login: async (username, password, totp) => {
        set({ loading: true, error: null });
        try {
          const outcome = await ctlLogin(configOf(get()), username, password, totp);
          if (outcome.kind === 'passkey_required') {
            // 密码正确但需要 passkey：就地弹出 authenticator 并用断言重提交（password 仍在闭包
            // 里）。signing 标志驱动「触碰你的安全密钥」提示。整个 2FA passkey 步骤对 UI 透明
            // ——登录表单无需 passkey 输入框，store 自动完成 ceremony。
            const ch = outcome.challenge;
            if (!ch.credentialId || !ch.alg) {
              set({ error: 'This account requires a passkey, but none is registered.', loading: false });
              return;
            }
            set({ loginCeremony: true });
            try {
              const assertion = await assertLogin(
                ch.challenge,
                ch.credentialId,
                ch.alg,
                ch.rpid || window.location.hostname,
              );
              const after = await ctlLogin(configOf(get()), username, password, undefined, assertion);
              if (after.kind === 'success') {
                set({
                  sessionToken: after.result.sessionToken,
                  csrfToken: after.result.csrfToken,
                  loggedIn: true,
                  operatorName: after.result.operator,
                  sessionExpiresAt: after.result.expiresAt,
                  totpRequired: false,
                  loginCeremony: false,
                  loading: false,
                });
                await get().hydrateFromServer();
                await get().refresh();
                await get().loadTOTPStatus();
                await get().loadPasskeyStatus();
                return;
              }
              // A passkey resubmit should either succeed or throw; anything else is unexpected.
              set({ error: 'Passkey login did not complete — please try again.', loginCeremony: false, loading: false });
            } catch (err) {
              set({
                error: err instanceof Error ? err.message : 'Passkey login failed',
                loginCeremony: false,
                loading: false,
              });
            }
            return;
          }
          if (outcome.kind === 'totp_required') {
            // 密码正确但需要二次码：让登录表单收集 TOTP 码后重试。后端对「缺码」与「码错」
            // 返回同一个 totp_required（不开 oracle）；但我们本地知道是否带了码——若带了码仍被
            // 要求，就是码错/过期，给个温和提示（用户已在 2FA 步，不算信息泄露）。首次（未带码）
            // 不写 error，仅展开验证码框。
            const submittedCode = !!(totp && totp.trim() !== '');
            set({
              totpRequired: true,
              error: submittedCode
                ? 'Two-factor code not accepted — check the code and your device clock, then try again.'
                : null,
              loading: false,
            });
            return;
          }
          set({
            sessionToken: outcome.result.sessionToken,
            csrfToken: outcome.result.csrfToken,
            loggedIn: true,
            operatorName: outcome.result.operator,
            sessionExpiresAt: outcome.result.expiresAt,
            totpRequired: false,
            loading: false,
          });
          await get().hydrateFromServer();
          await get().refresh();
          // 拉取本账户的 2FA / passkey 状态（供「账户安全」区回显）。失败不阻塞登录。
          await get().loadTOTPStatus();
          await get().loadPasskeyStatus();
        } catch (err) {
          // 硬失败（密码错 / 429 锁定 / 网络 / 500，均在到达「需二次码」之前抛出）：复位
          // totpRequired，回到纯密码表单——避免「输入用户名或密码错误」却仍显示验证码框的
          // 错位提示。真正需要二次码的下一次（密码正确）会重新干净地触发 totp_required。
          set({
            error: err instanceof Error ? err.message : 'Login failed',
            totpRequired: false,
            loading: false,
          });
        }
      },

      // 复位二次验证步骤（见接口注释）：仅清 totpRequired；验证码输入框的本地值由组件清空。
      resetTOTPChallenge: () => set({ totpRequired: false }),

      // 服务端权威 hydration（plan-4，D1）：服务端的拓扑是唯一权威，本地缓存只是可弃置的
      // 镜像——每次登录/会话恢复后覆盖。失败（网络/解析）保留本地画布并安静返回：hydration
      // 是登录的附属动作，不能让一次拉取失败挡住登录本身；下次登录/刷新会再试。
      hydrateFromServer: async () => {
        try {
          const raw = await ctlGetTopology(configOf(get()));
          if (raw === null) {
            return; // 服务端尚无拓扑（首次部署前）：保留本地画布。
          }
          const topo = raw as Topology;
          if (!topo || typeof topo !== 'object' || !topo.project || !topo.domains || !topo.nodes || !topo.edges) {
            return; // 形状不符：不覆盖（服务端字节由 update-topology 的 custody 门保证，这只是防御）。
          }
          const topoStore = useTopologyStore.getState();
          const local = topoStore.getTopology();
          // 语义比较（plan-4 review）：只比 loadTopology 实际消费的四个切片 + 版本号，且对
          // 对象键排序后再比，避免「服务端 canonical 键序」与「前端 getTopology 键序」不同
          // 造成的假性差异（数组顺序保留=保守，宁可多备份也不漏）。
          const differs = stableStringify(loadSlices(local)) !== stableStringify(loadSlices(topo));
          if (!differs) {
            return; // 与本地完全一致：跳过覆盖（不无谓清空历史/选中）。
          }
          // 备份保险（D9，review 修正）：每当一次覆盖会丢弃「非空且与服务端不同」的本地设计
          // 时都先导出备份——不再是「每浏览器一次」（那会在第二次起静默丢弃未部署的本地编辑）。
          // 稳态下 differs=false，本分支根本不触发，故不会刷屏下载；只有真有分歧的未部署改动
          // 才会备份，正是该保护的场景。控制器模式下「持久化=部署」，未部署的本地改动是易失的。
          const localHasWork = local.nodes.length > 0 || local.edges.length > 0;
          if (localHasWork) {
            const stamp = new Date().toISOString().slice(0, 10);
            topoStore.exportProject(`pre-hydration-backup-${stamp}.json`);
            set({ hydrationNotice: true });
          }
          topoStore.loadTopology(topo);
        } catch {
          // 拉取失败：保留本地画布，不阻塞登录（见函数注释）。
        }
      },

      dismissHydrationNotice: () => set({ hydrationNotice: false }),

      // 登出：best-effort 调 POST /logout 撤销服务端 session，然后无论成败都清空本地
      // session + fleet 视图（本地登出必须生效，即使网络/服务端撤销失败）。
      logout: async () => {
        try {
          // 有内存 session 或 cookie 会话（loggedIn）时都要调服务端撤销 + 清 cookie。
          if (get().sessionToken || get().loggedIn) {
            await ctlLogout(configOf(get()));
          }
        } catch {
          // 撤销失败不阻塞本地登出（session 仍会在服务端按 TTL 过期）。
        }
        set({
          sessionToken: '',
          csrfToken: '',
          loggedIn: false,
          operatorName: null,
          sessionExpiresAt: null,
          // 清掉 2FA 会话态：totpRequired 复位，totpEnabled 回到「未知」，下一位用密码登录的
          // 操作员会重新拉取自己账户的状态（TwoFactorSettings 的守卫 effect 在 null 时再触发）。
          totpRequired: false,
          totpEnabled: null,
          passkeyRegistered: null,
          nodes: [],
          audit: [],
          auditVerified: false,
          // Clear settings too, so a different operator signing in re-fetches them
          // (the guarded loadSettings effect re-fires on settings===null).
          settings: null,
          error: null,
        });
      },

      // 刷新后恢复登录态（P5）：GET /session 用 httpOnly cookie 探测当前会话。命中则置
      // loggedIn + 身份 + 到期 + csrfToken（后续状态改写请求据此带 X-CSRF-Token）；未命中
      // （401/403）则清空登录态。探测失败（网络/未配置）也清 loggedIn。仅恢复登录态——不主动
      // 拉 fleet（由持久化缓存即时上色，用户按「连接 / 刷新」取实时态）。
      checkSession: async () => {
        try {
          const info = await getSession(configOf(get()));
          // Only a GENUINE cookie session counts as "logged in". GET /session also answers
          // 200 for a break-glass Bearer token (it authenticates operator routes), but
          // break-glass mints no session/CSRF cookie, so its probe returns an EMPTY
          // csrf_token. Gate on a non-empty csrf_token to keep break-glass a recovery path
          // (selectHasAuth still enables Deploy via operatorToken), preserving the
          // "break-glass is not a login" invariant.
          if (info && info.csrfToken !== '') {
            const wasLoggedIn = get().loggedIn;
            set({
              loggedIn: true,
              operatorName: info.operator,
              sessionExpiresAt: info.expiresAt || null,
              csrfToken: info.csrfToken,
            });
            // 服务端权威 hydration（D1）：会话恢复也覆盖本地画布——但仅在登录态由假变真的
            // 那次（mount/刷新恢复），避免 Shell 的 mode 翻转 effect 重复触发重复覆盖。
            if (!wasLoggedIn) {
              await get().hydrateFromServer();
            }
          } else {
            set({ loggedIn: false, csrfToken: '' });
          }
        } catch {
          set({ loggedIn: false });
        }
      },

      // 拉取本账户的 TOTP 状态。403（break-glass token 无账户）或网络错误时保持 totpEnabled=null
      // （UI 据此提示「请用密码登录以管理 2FA」），不污染全局 error。
      loadTOTPStatus: async () => {
        try {
          set({ totpEnabled: await getTOTPStatus(configOf(get())) });
        } catch {
          set({ totpEnabled: null });
        }
      },

      // 开始 enroll：mint 一个尚未激活的 secret + otpauth URI，返回给组件展示（确认前不持久化，
      // 也不改全局状态）。错误向调用方抛出，由 TwoFactorSettings 就地展示。
      enrollTOTP: async () => {
        return ctlEnrollTOTP(configOf(get()));
      },

      // 确认 enroll：用 enroll 拿到的 secret + 一个当前码激活 2FA。成功后 totpEnabled=true。
      // 失败（如码错）向调用方抛出，由组件就地展示。
      confirmTOTP: async (secret, code) => {
        await ctlConfirmTOTP(configOf(get()), secret, code);
        set({ totpEnabled: true });
      },

      // 关闭 2FA：需当前码（防被劫持的 session 直接摘掉二次因子）。成功后 totpEnabled=false。
      disableTOTP: async (code) => {
        await ctlDisableTOTP(configOf(get()), code);
        set({ totpEnabled: false });
      },

      // 无密码 passkey 登录：begin 取挑战 → assertLogin 弹 authenticator → finish 换 session。
      // 失败（无 passkey / 断言失败 / 取消）就地展示。成功后刷新视图 + 拉取账户安全状态。
      loginWithPasskey: async (username) => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());
          const ch = await passkeyLoginBegin(cfg, username);
          if (!ch.credentialId || !ch.alg) {
            // 空 allow_credentials = 该用户名没有注册 passkey（后端返回 decoy）。
            set({ error: 'No passkey is registered for this account.', loading: false });
            return;
          }
          set({ loginCeremony: true });
          let assertion;
          try {
            assertion = await assertLogin(
              ch.challenge,
              ch.credentialId,
              ch.alg,
              ch.rpid || window.location.hostname,
            );
          } finally {
            set({ loginCeremony: false });
          }
          const result = await passkeyLoginFinish(cfg, username, assertion);
          set({
            sessionToken: result.sessionToken,
            csrfToken: result.csrfToken,
            loggedIn: true,
            operatorName: result.operator,
            sessionExpiresAt: result.expiresAt,
            loading: false,
          });
          await get().hydrateFromServer();
          await get().refresh();
          await get().loadTOTPStatus();
          await get().loadPasskeyStatus();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Passkey login failed',
            loading: false,
            loginCeremony: false,
          });
        }
      },

      // 拉取本账户的登录 passkey 状态。403（break-glass token 无账户）或错误时保持 null。
      loadPasskeyStatus: async () => {
        try {
          set({ passkeyRegistered: await getPasskeyStatus(configOf(get())) });
        } catch {
          set({ passkeyRegistered: null });
        }
      },

      // 注册登录 passkey：复用 keystone 的 create() ceremony（enrollOperatorCredential 取
      // SPKI + alg），POST /passkey/register 存公钥。仅公钥离开 authenticator。loginCeremony
      // 驱动「触碰你的安全密钥」提示（不触发 DeployBar 部署横幅）。错误向调用方抛出，由
      // PasskeySettings 就地展示（与 TwoFactorSettings 的本地错误一致）。
      registerPasskey: async () => {
        const rpId = window.location.hostname;
        const origin = window.location.origin;
        set({ loginCeremony: true });
        try {
          const cred = await enrollOperatorCredential(rpId, origin);
          await ctlRegisterPasskey(configOf(get()), {
            alg: cred.alg,
            credentialId: cred.credentialId,
            publicKeyPEM: cred.publicKeyPEM,
            rpId,
            origin,
          });
          set({ passkeyRegistered: true, loginCeremony: false });
        } catch (err) {
          set({ loginCeremony: false });
          throw err;
        }
      },

      // 关闭登录 passkey（两段式）：begin 取再认证挑战 → assertLogin → finish 删除凭据。需新鲜
      // 断言，防被劫持的 session 直接摘掉因子。begin 返回 done 表示本就没有 passkey（幂等）。
      // 错误向调用方抛出，由 PasskeySettings 就地展示。
      disablePasskey: async () => {
        set({ loginCeremony: true });
        try {
          const cfg = configOf(get());
          const begin = await disablePasskeyBegin(cfg);
          if (begin.kind === 'done') {
            set({ passkeyRegistered: false, loginCeremony: false });
            return;
          }
          const ch = begin.challenge;
          if (!ch.credentialId || !ch.alg) {
            set({ loginCeremony: false });
            throw new Error('Cannot disable: no credential to re-authenticate with.');
          }
          const assertion = await assertLogin(
            ch.challenge,
            ch.credentialId,
            ch.alg,
            ch.rpid || window.location.hostname,
          );
          await disablePasskeyFinish(cfg, assertion);
          set({ passkeyRegistered: false, loginCeremony: false });
        } catch (err) {
          set({ loginCeremony: false });
          throw err;
        }
      },

      // 为某节点铸造一次性 enrollment token，返回明文 token（仅此一次可见）。
      mintToken: async (nodeId, ttl) => {
        return mintEnrollmentToken(configOf(get()), nodeId, ttl);
      },

      // KEYSTONE enroll（plan-5.1d）：pin off-host operator 签名凭据（passkey / YubiKey），
      // 把 keystone 打开。流程：navigator.credentials.create()（getPublicKey/getPublicKeyAlgorithm
      // 取 SPKI + COSE alg，避免 CBOR）→ POST /operator-credential 把 PKIX PEM + credential_id +
      // rpid(=location.hostname) + origin pin 到控制器。rpid 必须等于 create() 的 rp.id——节点
      // 校验 SHA256(rpid)==assertion 的 rpIdHash。成功后只在 localStorage 留下非密的
      // credential_id/alg/rpId，供后续签名设置 allowCredentials。
      enrollOperator: async () => {
        // rp.id 必须是注册域（location.hostname）；WebAuthn 在非安全上下文不可用。
        const rpId = window.location.hostname;
        const origin = window.location.origin;
        set({ enrolling: true, error: null });
        try {
          const cred = await enrollOperatorCredential(rpId, origin);
          await postOperatorCredential(configOf(get()), {
            alg: cred.alg,
            credentialId: cred.credentialId,
            publicKeyPEM: cred.publicKeyPEM,
            rpId,
            origin,
          });
          set({
            operatorCredentialId: cred.credentialId,
            operatorCredentialAlg: cred.alg,
            operatorRpId: rpId,
            operatorPublicKeyPEM: cred.publicKeyPEM,
            enrolling: false,
          });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Failed to enroll operator signing key',
            enrolling: false,
          });
        }
      },

      // 部署当前拓扑到 fleet：复用 topologyStore.compile() 发往 /api/compile 的同一
      // model.Topology JSON 形状（getTopology() → {project,domains,nodes,edges,...}），
      // 经 update-topology → stage →（KEYSTONE 签名）→ promote → refresh。
      //
      // KEYSTONE 分支（plan-5.1d，替代旧的 requireUserKey() seam）：stage 之后 GET /trustlist。
      //   - 返回 manifest（keystone ON）：base64-decode 标准 base64 的 trustlist_json 取回
      //     canonical 字节 → signManifest()（challenge = SHA256(那些字节)，rpid 绑定 = 节点
      //     校验 SHA256(rpid)==rpIdHash）→ POST /trustlist-signature → promote。promote 的
      //     keystone gate 要求一个有效 off-host 签名，否则 422——所以必须先签后 promote。
      //   - 返回 null（keystone OFF / 404）：直接 promote（今日行为，无需签名）。
      // 若 keystone ON 但本地尚未 enroll operator 凭据，给出可执行报错（先注册签名密钥）。
      deploy: async (opts) => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());

          // Resolve the design to upload + its stripped-key count. On a confirmed
          // shrink we deploy the SNAPSHOT the warning was computed from (binds the
          // confirmation to what the operator actually saw, not a since-changed
          // canvas); otherwise we strip the live canvas now.
          let cleanTopo: Topology;
          let stripped: number;
          const confirming = opts?.confirmedShrink ? get().pendingShrink : null;
          if (confirming) {
            cleanTopo = confirming.snapshot;
            stripped = confirming.stripped;
          } else {
            // Custody strip (plan-5, D4): never send a private key to the server (the
            // client mirror of the server's update-topology 400). In controller mode
            // the hydrated design is already key-free, but a locally-compiled/imported
            // design could carry keys — this is the fail-safe.
            const local = useTopologyStore.getState().getTopology();
            const r = stripPrivateKeys(local);
            cleanTopo = r.topo;
            stripped = r.stripped;

            // Shrink/empty guard (plan-5): before mutating the server design, compare
            // the canvas against the server copy. Emptying it, or dropping ≥50% of the
            // server's node-IDs, requires a typed confirmation (the audit's
            // one-click-destruction scenario). Version history (plan-2) is the recovery
            // backstop; this is the prevention layer.
            //
            // The server read is best-effort: a 404 means no server topology yet (no
            // shrink possible), and ANY other read failure (5xx/timeout/CSRF) must NOT
            // abort an otherwise-valid deploy — we proceed and rely on version history,
            // rather than blocking a legitimate upload on a transient guard-read error.
            let server: Topology | null = null;
            try {
              server = (await ctlGetTopology(cfg)) as Topology | null;
            } catch {
              server = null; // guard read failed → skip the guard (history is the backstop)
            }
            if (server && Array.isArray(server.nodes) && server.nodes.length > 0) {
              const canvasIds = new Set(cleanTopo.nodes.map((n) => n.id));
              const dropped = server.nodes.filter((n) => !canvasIds.has(n.id)).length;
              const emptied = cleanTopo.nodes.length === 0;
              const majorityDropped = dropped / server.nodes.length >= 0.5;
              if (emptied || majorityDropped) {
                set({
                  pendingShrink: {
                    serverNodeCount: server.nodes.length,
                    canvasNodeCount: cleanTopo.nodes.length,
                    // Never empty: an empty phrase would let an empty input match and
                    // one-click past the gate. Fall back to a typed sentinel.
                    confirmPhrase: cleanTopo.project.name || cleanTopo.project.id || 'DELETE',
                    snapshot: cleanTopo,
                    stripped,
                  },
                  loading: false,
                });
                return; // wait for the typed confirmation, then deploy({confirmedShrink:true})
              }
            }
          }

          const topoJSON = JSON.stringify(cleanTopo);
          await updateTopology(cfg, topoJSON);
          const result = await stage(cfg);
          // 当没有已注册节点时 stage 不产生任何 bundle（staged 为空），此时 promote 会
          // 返回 409 ErrNoStagedBundle —— 那不是错误，而是「还没有节点入网」。直接展示
          // skippedUnenrolled，跳过 promote（也跳过签名），避免把正常情况渲染成报错。
          if (result.staged.length > 0) {
            // KEYSTONE：取回待签 manifest。null = keystone OFF，直接 promote。
            const toSign = await getTrustlist(cfg);
            if (toSign !== null) {
              const credentialId = get().operatorCredentialId;
              const alg = get().operatorCredentialAlg;
              const pem = get().operatorPublicKeyPEM;
              if (credentialId === null || alg === null || !pem) {
                // keystone 已开启（节点需要签名）但本地没有 pin 的完整签名凭据
                // （credential_id + alg + PEM）：这是可执行的前置条件失败，而非内部错误。
                throw new Error(
                  'This deploy requires an off-host signature, but no operator signing key is enrolled — enroll your signing key first.',
                );
              }
              // rpId 必须等于 enroll 时 pin 的值（节点校验 SHA256(rpid)==assertion 的
              // rpIdHash）；老记录可能缺 rpId，回退到当前 hostname（enroll 时即用它）。
              const rpId = get().operatorRpId ?? window.location.hostname;
              // 把标准 base64 的 canonical bytes 解码回原始字节：它们的 SHA-256 就是
              // WebAuthn challenge（节点比对 base64url(SHA256(Canonical(manifest)))）。
              const manifestBytes = base64StdToBytes(toSign.trustlistJson);
              set({ signing: true });
              let signed;
              try {
                signed = await signManifest(manifestBytes, credentialId, alg, rpId, pem);
              } finally {
                set({ signing: false });
              }
              // 提交签名前用服务端 substitution guard 再核对一遍 trustlist_json
              // （原样回传我们刚签的标准 base64 字节）。
              await postTrustlistSignature(cfg, {
                trustlistJson: toSign.trustlistJson,
                signed,
              });
            }
            await promote(cfg);
          }
          // Clear any pending shrink-confirm (a confirmed deploy consumes it) and
          // surface how many private keys were stripped before upload (0 = no notice).
          set({ lastDeploy: result, loading: false, pendingShrink: null, lastStrippedKeys: stripped });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Deploy failed',
            loading: false,
            signing: false,
          });
        }
      },

      cancelShrinkConfirm: () => set({ pendingShrink: null }),
      dismissStripNotice: () => set({ lastStrippedKeys: 0 }),
      clearModeNotices: () => set({ hydrationNotice: false, lastStrippedKeys: 0, pendingShrink: null }),

      // 驱逐一个节点后刷新视图。
      revoke: async (nodeId) => {
        set({ loading: true, error: null });
        try {
          await revoke(configOf(get()), nodeId);
          set({ loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Revoke failed',
            loading: false,
          });
        }
      },

      // 为整个 fleet 请求 WG 密钥轮换（plan-4.6 ROUTINE tier）：把每个已审批节点标记为
      // rekey_requested，随后刷新视图（注册表里会显示 rekeying 徽标）。这只是 zero-knowledge
      // 轮换流程的第一步——各 agent 会自行重生密钥并经 /rekey 注册新公钥；待节点重新注册后，
      // operator 需再 Deploy 一次，新一代配置携带全员新公钥使 fleet 收敛。
      rollKeys: async () => {
        set({ loading: true, error: null });
        try {
          await rekeyAll(configOf(get()));
          set({ loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Roll keys failed',
            loading: false,
          });
        }
      },
    }),
    {
      name: 'controller-storage',
      // 仅持久化连接端点 + 已 pin 的 operator 签名凭据的非密标识（credential_id/alg/rpId/
      // 公钥 PEM 都不是密钥材料——私钥从不离开 authenticator）。绝不持久化 operatorToken /
      // sessionToken / CSRF（密钥不落 localStorage），也不持久化 loading / error / signing。
      //
      // P4 新增的非密缓存（mode / nodes / settings / lastSyncedAt）仅供刷新后「即时上色」。
      // nodes 只含 nodeId/状态/代号/时间戳等非密字段，不含任何密钥材料。缓存是 advisory：
      // 唯一一处 nodes 参与门控的地方（selectRekeyingCount → DeployBar 在有节点轮换时禁用
      // Deploy）是 fail-closed —— 重载后陈旧缓存至多「禁用」Deploy，绝不会「放行」实时状态本应
      // 拦下的部署；refresh() 拉到实时状态后即收敛。控制器后端在 stage/promote 仍是最终权威。
      partialize: (state) => ({
        baseURL: state.baseURL,
        pathPrefix: state.pathPrefix,
        agentBaseURL: state.agentBaseURL,
        operatorCredentialId: state.operatorCredentialId,
        operatorCredentialAlg: state.operatorCredentialAlg,
        operatorRpId: state.operatorRpId,
        operatorPublicKeyPEM: state.operatorPublicKeyPEM,
        mode: state.mode,
        nodes: state.nodes,
        settings: state.settings,
        lastSyncedAt: state.lastSyncedAt,
      }),
    }
  )
);
