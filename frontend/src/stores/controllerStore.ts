import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../types/controller';
import type { ControllerConfig, WebAuthnAlg, ControllerSettings } from '../api/controllerClient';
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
} from '../api/controllerClient';
import { enrollOperatorCredential, signManifest } from '../lib/webauthn';
import { useTopologyStore } from './topologyStore';

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

  // 登录会话（plan-5.2）：密码登录后服务端签发的 bearer session token，仅在内存中保存
  // （刷新页面后需重新登录），绝不落 localStorage。operatorName/sessionExpiresAt 仅用于
  // 回显「已登录为 X，到期时间」。
  sessionToken: string;
  operatorName: string | null;
  sessionExpiresAt: string | null;

  // fleet 视图
  nodes: ControllerNode[];
  audit: ControllerAuditEntry[];
  auditVerified: boolean;
  lastDeploy: StageResult | null;

  // bootstrap 设置（plan-5.2，服务端持久化）：public agent URL / GitHub 代理 / agent 发布
  // 基址。null 表示尚未从服务端加载（refresh 时拉取）。
  settings: ControllerSettings | null;

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
  signing: boolean;
  enrolling: boolean;

  // actions
  setConfig: (partial: Partial<ControllerConfig & { agentBaseURL: string }>) => void;
  loadSettings: () => Promise<void>;
  saveSettings: (s: ControllerSettings) => Promise<void>;
  refresh: () => Promise<void>;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  mintToken: (nodeId: string, ttl: number) => Promise<string>;
  enrollOperator: () => Promise<void>;
  deploy: () => Promise<void>;
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
  };
}

// 派生选择器：是否已通过密码登录（持有有效 session）。DeployPanel 用它在登录区切换
// 「登录表单 / 已登录为 X」。break-glass operatorToken 不算「已登录」（它是恢复路径）。
export function selectLoggedIn(state: ControllerState): boolean {
  return state.sessionToken !== '';
}

// 派生选择器：是否持有任一可用的 operator 凭据（登录 session 或 break-glass token）。
// configOf 的 EFFECTIVE bearer 正是 sessionToken || operatorToken，所以任一非空即可发起
// operator 请求。DeployBar 用它决定是否禁用 Deploy/Roll-keys（不能再只看 operatorToken，
// 否则登录后的操作员会被错误地拦住）。
export function selectHasAuth(state: ControllerState): boolean {
  return state.sessionToken !== '' || state.operatorToken.trim() !== '';
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

      sessionToken: '',
      operatorName: null,
      sessionExpiresAt: null,

      nodes: [],
      audit: [],
      auditVerified: false,
      lastDeploy: null,
      settings: null,

      operatorCredentialId: null,
      operatorCredentialAlg: null,
      operatorRpId: null,
      operatorPublicKeyPEM: null,

      loading: false,
      error: null,
      lastSyncedAt: null,
      signing: false,
      enrolling: false,

      setConfig: (partial) => set(partial),

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
          // 顺带刷新 bootstrap 设置（不阻塞 fleet 视图；失败保留旧值）。
          try {
            set({ settings: await getSettings(cfg) });
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
          set({ settings: await getSettings(configOf(get())) });
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
      login: async (username, password) => {
        set({ loading: true, error: null });
        try {
          const result = await ctlLogin(configOf(get()), username, password);
          set({
            sessionToken: result.sessionToken,
            operatorName: result.operator,
            sessionExpiresAt: result.expiresAt,
            loading: false,
          });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Login failed',
            loading: false,
          });
        }
      },

      // 登出：best-effort 调 POST /logout 撤销服务端 session，然后无论成败都清空本地
      // session + fleet 视图（本地登出必须生效，即使网络/服务端撤销失败）。
      logout: async () => {
        try {
          if (get().sessionToken) {
            await ctlLogout(configOf(get()));
          }
        } catch {
          // 撤销失败不阻塞本地登出（session 仍会在服务端按 TTL 过期）。
        }
        set({
          sessionToken: '',
          operatorName: null,
          sessionExpiresAt: null,
          nodes: [],
          audit: [],
          auditVerified: false,
          error: null,
        });
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
      deploy: async () => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());
          const topo = useTopologyStore.getState().getTopology();
          const topoJSON = JSON.stringify(topo);
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
          set({ lastDeploy: result, loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Deploy failed',
            loading: false,
            signing: false,
          });
        }
      },

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
      // 公钥 PEM 都不是密钥材料——私钥从不离开 authenticator）。绝不持久化 operatorToken
      // （密钥不落 localStorage），也不持久化易失的 fleet 视图 / loading / error / signing。
      partialize: (state) => ({
        baseURL: state.baseURL,
        pathPrefix: state.pathPrefix,
        agentBaseURL: state.agentBaseURL,
        operatorCredentialId: state.operatorCredentialId,
        operatorCredentialAlg: state.operatorCredentialAlg,
        operatorRpId: state.operatorRpId,
        operatorPublicKeyPEM: state.operatorPublicKeyPEM,
      }),
    }
  )
);
