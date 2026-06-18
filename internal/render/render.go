// Package render is the "key preparation + full rendering" layer shared by the
// API and CLI entry points.
//
// Before this package existed, the key-generation and rendering logic lived only
// inside internal/api/handler.go (generateKeys / renderAll), while the CLI
// (cmd/compiler) maintained its own degraded reimplementation — it stuffed the
// literal FAKE_PRIVKEY_* into every config, never rendered a client's wg0.conf,
// and never generated a deploy-all script (audit theme T6: D6 / D27–29 / D59).
// Hoisting those two functions into this shared package makes both entry points
// follow the exact same rendering path, so the CLI automatically gets real keys
// (obeying the key-persistence rules), client wg0 configs plus install scripts,
// and a deploy-all script — eliminating the whole T6 theme in one stroke.
//
// Dependency direction: this package depends only on compiler / renderer / model
// / wgtypes and never depends back on api, to avoid forming an
// api → render → api import cycle (render must be importable by both api and
// cmd/compiler).
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

// GenerateKeys parses or generates a WireGuard key pair for each node and writes
// the result back onto the node so it persists with the topology JSON and is
// reused verbatim on the next compile (invariant I5: key stability).
//
// custody selects the custody model:
//
//   - AirGap (default for the air-gap CLI/API): private keys round-trip through
//     the topology JSON. Key handling branches on the node's two key fields:
//     (a) wireguard_private_key non-empty: parse that private key, derive the
//     public key from it, and reuse both; write the derived public key back,
//     fixing a missing or stale public key.
//     (b) wireguard_private_key empty but wireguard_public_key non-empty: hard
//     error. The stateless compiler cannot reconstruct the private key. Prompt the
//     operator to paste the in-use private key from the host's /etc/wireguard, or
//     to clear both key fields to rotate explicitly.
//     (c) both empty: generate a brand-new key pair and write it back so it
//     persists and round-trips, reusing the same pair thereafter.
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
			// Case (a): the private key is present. Parse it, derive the public key from
			// it, and reuse the whole pair; write the derived public key back to fix a
			// public key on the node that was missing or inconsistent (stale) with it.
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
			// Case (c): both key fields are empty — a brand-new node. Generate a new key
			// pair and write both the private and public keys back to the node so they
			// persist with the topology and round-trip, reusing the same pair on the
			// next compile.
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

// All renders a single compile result into all deployment artifacts and writes
// the results back into result's map fields: per-peer WireGuard configs, a
// client's single wg0 config, Babel configs, sysctl configs, per-node install
// scripts (including the client-role branch and transit-CIDR resolution), and
// the deploy-all scripts (bash + ps1).
//
// This is the single rendering entry point shared by the API and the CLI — both
// entry points follow the exact same path, guaranteeing artifact consistency
// (entry-point equivalence, see equivalence_test.go).
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
			// Pass this client's ClientPeerInfo so its single wg0 link also installs
			// mimic when transport=="tcp" (decision #5: clients are supported too).
			// Nil when the key is absent — the renderer already guards against nil.
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
