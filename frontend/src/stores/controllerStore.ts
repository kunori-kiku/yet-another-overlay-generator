import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../types/controller';
import type { ControllerConfig, WebAuthnAlg } from '../api/controllerClient';
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
  operatorToken: string;

  // fleet 视图
  nodes: ControllerNode[];
  audit: ControllerAuditEntry[];
  auditVerified: boolean;
  lastDeploy: StageResult | null;

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
  refresh: () => Promise<void>;
  mintToken: (nodeId: string, ttl: number) => Promise<string>;
  enrollOperator: () => Promise<void>;
  deploy: () => Promise<void>;
  revoke: (nodeId: string) => Promise<void>;
  rollKeys: () => Promise<void>;
}

// 从连接字段切出 controllerClient 需要的 ControllerConfig（不含 agentBaseURL）。
function configOf(state: ControllerState): ControllerConfig {
  return {
    baseURL: state.baseURL,
    pathPrefix: state.pathPrefix,
    operatorToken: state.operatorToken,
  };
}

// 把标准 base64（带 padding，GET /trustlist 的 trustlist_json 编码）解码回原始字节。
// 这些字节就是 canonical manifest 字节，其 SHA-256 即 WebAuthn challenge。
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
export function selectOperatorEnrolled(state: ControllerState): boolean {
  return state.operatorCredentialId !== null && state.operatorCredentialAlg !== null;
}

export const useControllerStore = create<ControllerState>()(
  persist(
    (set, get) => ({
      // 默认连接配置（见 DESIGN：operator 默认 :8080，agent 默认 :9090）。
      baseURL: 'http://localhost:8080',
      pathPrefix: '',
      agentBaseURL: 'http://localhost:9090',
      operatorToken: '',

      nodes: [],
      audit: [],
      auditVerified: false,
      lastDeploy: null,

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

      // 刷新 fleet 视图：并行拉取 nodes + audit。任一失败则记录 error，并保持已有视图不变。
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
        } catch (err) {
          set({
            error: err instanceof Error ? err.message : 'Failed to refresh controller state',
            loading: false,
          });
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
              if (credentialId === null || alg === null) {
                // keystone 已开启（节点需要签名）但本地没有 pin 的签名凭据：
                // 这是可执行的前置条件失败，而非内部错误。
                throw new Error(
                  'This deploy requires an off-host signature, but no operator signing key is enrolled — enroll your signing key first.',
                );
              }
              // 把标准 base64 的 canonical bytes 解码回原始字节：它们的 SHA-256 就是
              // WebAuthn challenge（节点比对 base64url(SHA256(Canonical(manifest)))）。
              const manifestBytes = base64StdToBytes(toSign.trustlistJson);
              const pem = get().operatorPublicKeyPEM ?? '';
              set({ signing: true });
              let signed;
              try {
                signed = await signManifest(manifestBytes, credentialId, alg, pem);
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
