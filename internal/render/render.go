// Package render 是 API 与 CLI 两个入口共享的「密钥准备 + 全量渲染」层。
//
// 在此包出现之前，密钥生成与渲染逻辑只存在于 internal/api/handler.go 内（generateKeys /
// renderAll），CLI（cmd/compiler）则各自维护一份退化实现——它向每份配置塞入字面量
// FAKE_PRIVKEY_*，从不渲染 client 的 wg0.conf，也不生成 deploy-all 脚本（审计主题 T6：
// D6 / D27–29 / D59）。把这两个函数抽到本共享包后，两个入口走完全相同的渲染路径，CLI
// 自动获得真实密钥（遵守密钥持久化规则）、client wg0 配置与安装脚本、以及 deploy-all 脚本，
// 整个 T6 主题被一次性消除。
//
// 依赖方向：本包仅依赖 compiler / renderer / model / wgtypes，绝不反向依赖 api，
// 以免形成 api → render → api 的导入环（render 必须可被 api 与 cmd/compiler 同时引用）。
package render

import (
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// KeyCustody selects how GenerateKeys treats a node's WireGuard key material.
//
// It is the code half of the zero-knowledge custody decision (see
// docs/spec/controller/key-custody.md). The air-gap path (compiler CLI, the
// existing HTTP API) uses AirGap; only the controller renders in AgentHeld.
type KeyCustody int

const (
	// AirGap is the historical behavior: private keys round-trip through the
	// topology JSON so a stateless recompile reproduces them (invariant I5). A
	// node with a public key but no private key is a hard error. This is the
	// default for every existing caller and is byte-for-byte unchanged.
	AirGap KeyCustody = iota
	// AgentHeld is zero-knowledge custody: each node keeps its own private key
	// agent-side and registers only a public key. GenerateKeys emits
	// PrivateKeyPlaceholder for every node and NEVER returns a real private key,
	// so the controller can render a whole fleet from public keys alone; the
	// agent splices its locally-held key into the placeholder at install time.
	AgentHeld
)

// PrivateKeyPlaceholder is the sentinel emitted on a node's own
// [Interface] PrivateKey line under AgentHeld custody. It is intentionally NOT
// valid base64, so no WireGuard key parser can mistake it for a real key, and it
// is spliced with the agent's locally-held private key before the config is used.
const PrivateKeyPlaceholder = "PRIVATEKEY_PLACEHOLDER"

// GenerateKeys 为每个节点解析或生成 WireGuard 密钥对，并把结果写回节点以便随拓扑 JSON
// 持久化、在下次编译时被原样复用（不变式 I5：密钥稳定）。
//
// custody selects the custody model:
//
//   - AirGap (default for the air-gap CLI/API): private keys round-trip through
//     the topology JSON. Key handling branches on the node's two key fields:
//     (a) wireguard_private_key 非空：解析该私钥、由它派生公钥并复用；把派生出的公钥写回，
//     修复缺失或陈旧的公钥。
//     (b) wireguard_private_key 为空但 wireguard_public_key 非空：硬错误。无状态编译器无法
//     重建其私钥。提示操作员从主机 /etc/wireguard 粘贴在用私钥，或同时清空两个密钥字段以
//     显式轮换。
//     (c) 两者皆空：生成全新密钥对并写回，使其持久化、可往返，此后复用同一对密钥。
//   - AgentHeld (controller, zero-knowledge custody): never emit a real private
//     key. Use the node's registered public key (deriving it from a stray private
//     key and discarding that private key if one is present; hard error if neither
//     is present — the agent must register a public key first), emit
//     PrivateKeyPlaceholder for the private half, and clear any private key on the
//     node so the controller's topology never carries one.
func GenerateKeys(topo *model.Topology, custody KeyCustody) (map[string]compiler.KeyPair, error) {
	keys := make(map[string]compiler.KeyPair)
	for i := range topo.Nodes {
		node := &topo.Nodes[i]

		if custody == AgentHeld {
			// The registered public key is authoritative: when present it is trusted
			// verbatim (the agent holds the matching private key), and a stray private
			// key on the node is never preferred over it — only used to derive the
			// public half when no public key was registered, then discarded.
			pub := node.WireGuardPublicKey
			if pub == "" {
				// Defensive: an air-gap topology carrying a private key may be
				// imported into the controller. Derive the public half and DISCARD
				// the private one — it must never reach a controller-rendered bundle.
				if node.WireGuardPrivateKey == "" {
					return nil, apierr.New(apierr.CodeKeygenMissingPubkey).With("node", node.ID)
				}
				privateKey, err := wgtypes.ParseKey(node.WireGuardPrivateKey)
				if err != nil {
					return nil, apierr.New(apierr.CodeKeygenPrivkeyParse).With("node", node.ID).With("detail", err.Error()).Wrap(err)
				}
				pub = privateKey.PublicKey().String()
			}
			// Persist only the public key; guarantee no private key lingers.
			node.WireGuardPublicKey = pub
			node.WireGuardPrivateKey = ""
			keys[node.ID] = compiler.KeyPair{
				PrivateKey: PrivateKeyPlaceholder,
				PublicKey:  pub,
			}
			continue
		}

		switch {
		case node.WireGuardPrivateKey != "":
			// 情形 (a)：私钥在场。解析并由它派生公钥，复用整对密钥；把派生出的公钥写回，
			// 借此修复节点上缺失或与私钥不一致（陈旧）的公钥。
			privateKey, err := wgtypes.ParseKey(node.WireGuardPrivateKey)
			if err != nil {
				return nil, apierr.New(apierr.CodeKeygenPrivkeyParse).With("node", node.ID).With("detail", err.Error()).Wrap(err)
			}

			node.WireGuardPrivateKey = privateKey.String()
			node.WireGuardPublicKey = privateKey.PublicKey().String()

		case node.WireGuardPublicKey != "":
			// Case (b): the public key is present but the private key is missing. The stateless
			// compiler cannot reconstruct the private key, so it cannot render this node's own
			// Interface PrivateKey — a hard error, not a silent rotate-or-blank.
			return nil, apierr.New(apierr.CodeKeygenPinnedNoPrivkey).With("node", node.ID)

		default:
			// 情形 (c)：两枚密钥字段皆空，是全新节点。生成新密钥对，并把私钥与公钥都写回
			// 节点，使其随拓扑持久化、可往返，下次编译复用同一对密钥。
			privateKey, err := wgtypes.GeneratePrivateKey()
			if err != nil {
				return nil, apierr.New(apierr.CodeKeygenGenerateFailed).With("node", node.ID).With("detail", err.Error()).Wrap(err)
			}

			node.WireGuardPrivateKey = privateKey.String()
			node.WireGuardPublicKey = privateKey.PublicKey().String()
		}

		keys[node.ID] = compiler.KeyPair{
			PrivateKey: node.WireGuardPrivateKey,
			PublicKey:  node.WireGuardPublicKey,
		}
	}
	return keys, nil
}

// Artifact is one fetchable, integrity-pinned file (release asset + SHA-256). It is an alias for
// renderer.Artifact: callers and plans 3/4/9 write render.Artifact, while the single underlying type
// lives in renderer (the install.sh template consumes it), avoiding a render<->renderer import cycle.
type Artifact = renderer.Artifact

// FetchSettings is the typed channel of install-time fetch pins threaded through the single shared
// render path (All). It is populated from ControllerSettings (controller mode; plan-3/4/9) or from
// env/flags (air-gap; plan-7). The ZERO value means "no catalog configured", which MUST leave
// install.sh and the signed bundle byte-identical to today — the air-gap byte-identity HIGH principle.
// Every field is defined now so plans 3/4/9 fill them without re-opening All's signature.
type FetchSettings struct {
	// GithubProxy is an optional prefix applied to GitHub downloads (e.g. "https://gh-proxy.com/").
	GithubProxy string
	// Mimic GitHub-.deb fallback (plan-3): the pinned release version, its release base URL, and the
	// per-"<codename>-<arch>" .deb asset + SHA-256 install.sh verifies before dpkg. Only this subset is
	// threaded into install.sh (via renderer.InstallFetch).
	MimicVersion     string
	MimicReleaseBase string
	MimicDebs        map[string]Artifact
	// Agent self-update (plan-9): the desired/floor agent versions, the agent release base URL, and the
	// per-"linux-<arch>" binary asset + SHA-256 the agent verifies against the signed artifacts.json
	// pin. NOT consumed by install.sh (the agent self-updates at runtime); carried here for the signed
	// artifacts.json emitted on the export path (plan-3/9).
	AgentVersion     string
	AgentMinVersion  string
	AgentReleaseBase string
	AgentBins        map[string]Artifact
	// AgentRolloutNodeIDs is the set of node IDs that receive the artifacts.json AGENT block
	// (plan-9 canary-then-fleet): the agent block is PER-NODE, so a canary subset self-updates
	// while the rest of the fleet does not. A node's artifacts.json carries the agent block iff
	// AgentVersion != "" AND AgentRolloutNodeIDs[nodeID]. Nil/empty ⇒ no node self-updates
	// (the air-gap and pre-rollout default). The mimic block stays fleet-wide.
	AgentRolloutNodeIDs map[string]bool
}

// All 把一份编译结果渲染成全部部署产物，并把结果写回 result 的各 map 字段：
// per-peer WireGuard 配置、client 的单一 wg0 配置、Babel 配置、sysctl 配置、
// 每节点安装脚本（含 client 角色分支与 transit-CIDR 解析），以及 deploy-all 脚本（bash + ps1）。
//
// 这是 API 与 CLI 共享的唯一渲染入口——两个入口走完全相同的路径，
// 从而保证产物一致性（入口等价性，见 equivalence_test.go）。
func All(result *compiler.CompileResult, keys map[string]compiler.KeyPair, fs FetchSettings) error {
	// WireGuard (per-peer configs for non-client nodes)
	wgConfigs, err := renderer.RenderAllWireGuardConfigs(result.Topology, result.PeerMap, keys)
	if err != nil {
		return fmt.Errorf("rendering WireGuard configs failed: %w", err)
	}
	result.WireGuardConfigs = wgConfigs

	// WireGuard client configs (single wg0 for client nodes)
	for nodeID, clientInfo := range result.ClientConfigs {
		config, err := renderer.RenderClientWireGuardConfig(clientInfo)
		if err != nil {
			return fmt.Errorf("rendering WireGuard config for client %s failed: %w", clientInfo.NodeName, err)
		}
		result.WireGuardConfigs[nodeID+":wg0"] = config
	}

	// Babel
	babelConfigs, err := renderer.RenderAllBabelConfigs(result.Topology, result.PeerMap)
	if err != nil {
		return fmt.Errorf("rendering Babel configs failed: %w", err)
	}
	result.BabelConfigs = babelConfigs

	// Sysctl
	sysctlConfigs, err := renderer.RenderAllSysctlConfigs(result.Topology)
	if err != nil {
		return fmt.Errorf("rendering sysctl configs failed: %w", err)
	}
	result.SysctlConfigs = sysctlConfigs

	// Optional bundle signing (opt-in via bundlesig.EnvSigningKey). When a signing
	// key is configured, the install scripts embed the verifying public key and a
	// signature-verify step that runs before the existing sha256sum -c; the export
	// path signs the canonical checksums alongside (internal/artifacts/export.go).
	// When signing is off, signingPubPEM stays empty and the *Signed renderers emit
	// byte-identical output to the plain renderers (see script_signature_test.go), so
	// the air-gap path is unchanged. A misconfigured key fails closed here.
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return fmt.Errorf("loading the bundle signing key failed: %w", err)
	}
	var signingPubPEM string
	if signer != nil {
		signingPubPEM = string(signer.PublicKeyPEM())
	}

	// Install-time fetch settings threaded into the signed install-script renderers. Only the
	// GitHub proxy is baked into install.sh; the mimic pins are read at install time from the
	// integrity-verified artifacts.json. A zero FetchSettings yields a zero InstallFetch, so the
	// template emits no fetch branch and install.sh stays byte-identical (air-gap byte-identity).
	installFetch := renderer.InstallFetch{GithubProxy: fs.GithubProxy}

	//
	for _, node := range result.Topology.Nodes {
		// artifacts.json is PER-NODE (plan-9): the mimic block is fleet-wide, but the agent
		// self-update block is emitted only for nodes in the rollout set. Empty ⇒ export omits
		// the file, keeping a non-catalog / non-rollout bundle byte-identical (D4).
		artifactsContent, err := buildArtifactsJSON(fs, node.ID)
		if err != nil {
			return fmt.Errorf("building artifacts.json failed: %w", err)
		}
		if artifactsContent != "" {
			result.ArtifactsJSON[node.ID] = artifactsContent
		}
		// AgentHeld custody is detected per-node from the rendered private key: when the node's key
		// is the placeholder, the install.sh must splice the agent-held key at install time. Air-gap
		// nodes carry a real private key here, so custody=false and no splice block is emitted
		// (keeping the air-gap install.sh byte-identical). See docs/spec/controller/key-custody.md.
		custody := keys[node.ID].PrivateKey == PrivateKeyPlaceholder
		splice := renderer.CustodySplice{Enabled: custody, Token: PrivateKeyPlaceholder}
		if node.Role == "client" {
			// 传入该 client 的 ClientPeerInfo，使其单一 wg0 链路在 transport=="tcp" 时
			// 也装配 mimic（决策 #5：client 也支持）。键缺失时为 nil，renderer 已做空值保护。
			script, err := renderer.RenderClientInstallScriptSigned(&node, signingPubPEM, splice, installFetch, result.ClientConfigs[node.ID])
			if err != nil {
				return fmt.Errorf("rendering install script for client %s failed: %w", node.Name, err)
			}
			result.InstallScripts[node.ID] = script
		} else {
			peers := result.PeerMap[node.ID]
			_, hasBabel := result.BabelConfigs[node.ID]
			transitCIDRs := renderer.NodeTransitCIDRs(result.Topology, &node)
			script, err := renderer.RenderInstallScriptSigned(&node, peers, hasBabel, signingPubPEM, splice, installFetch, transitCIDRs...)
			if err != nil {
				return fmt.Errorf("rendering install script for node %s failed: %w", node.Name, err)
			}
			result.InstallScripts[node.ID] = script
		}
	}

	// Deploy scripts (bash + PowerShell)
	bashDeploy, ps1Deploy, err := renderer.RenderDeployScripts(result.Topology, result.PeerMap, result.BabelConfigs)
	if err != nil {
		return fmt.Errorf("deploy script render: %w", err)
	}
	result.DeployScripts["deploy-all.sh"] = bashDeploy
	result.DeployScripts["deploy-all.ps1"] = ps1Deploy

	return nil
}
