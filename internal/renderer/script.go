package renderer

import (
	"net"
	"sort"
	"strconv"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// InstallScriptConfig holds the data for rendering the per-peer install script. Every field the
// template splices into the root-executed script is a ShellToken (never a bare string): the
// machine-checked root-shell escape seam. field_safety_test enforces the typing structurally, and each
// value is classified at construction via ShellQuoted (an inert single-quoted argument) or ShellRaw (a
// compiler constant / allocator output / comment-or-heredoc body spliced verbatim by design). See
// shelltoken.go for the seam's honest guarantee.
type InstallScriptConfig struct {
	// NodeName is the original node name, used ONLY in the script header comment (bash never evaluates
	// comments), so it is ShellRaw. The echo positions use NodeNameQuoted instead.
	NodeName ShellToken
	// NodeNameQuoted is the node name as an inert single-quoted shell token (ShellQuoted), spliced into
	// echo lines executed under the root identity. Every place in the template that puts the node name
	// into an echo uses this field rather than NodeName; otherwise a node name like x$(touch /tmp/pwned)
	// would trigger command substitution under root (audit T4 / D15). Typing both as ShellToken makes
	// the raw-vs-quoted split explicit and stops a bare string from ever reaching an echo position.
	NodeNameQuoted ShellToken
	NodeRole       ShellToken // role enum, spliced into echo/comment lines (ShellRaw)
	Platform       ShellToken // platform label, header comment only (ShellRaw)
	OverlayIP      ShellToken // allocator-produced overlay IP, spliced raw into ip/nft commands (ShellRaw)
	// TransitCIDRs is the resolved transit address pool of the domain this node belongs to
	// (domain.TransitCIDR, falling back to the default 10.10.0.0/24 when empty). The SNAT source-address
	// fix must emit rules per these pools: rewrite any source address that falls within a transit pool to
	// the overlay IP. Hard-coding 10.10.0.0/24 would silently break any node with a custom transit_cidr —
	// the source address after the packet arrives would still be the un-rewritten transit address, leaving
	// the route unreachable (audit D38/D39). The template emits one rule per CIDR; when empty,
	// RenderInstallScript falls back to the default pool so existing callers' behavior is unchanged. Each
	// entry is a ShellRaw token (a validated CIDR spliced raw into the SNAT rules).
	TransitCIDRs []ShellToken
	MTU          int
	HasBabel     bool
	HasForward   bool
	// HasMimic indicates this node has at least one transport=="tcp" link and therefore needs mimic
	// (eBPF UDP->fake-TCP shaping) wired into the install/uninstall scripts. See docs/spec/artifacts/mimic.md.
	HasMimic bool
	// MimicPorts is the listen-port set of all mimic interfaces on this node (sorted, de-duplicated). mimic
	// attaches to the egress NIC (probed at runtime); each listen port emits one filter line
	// (local=<egress_ip>:<port>). YAOG supplies only the port set; the egress if/ip are probed by bash at
	// install time — see docs/spec/artifacts/mimic.md "Attaches to the egress NIC".
	MimicPorts []int
	// MimicRemotes is the distinct set of mimic peer ENDPOINTS this node dials (host+port from
	// PeerInfo.Endpoint). Each emits a `remote=<resolved-ip>:<port>` filter line in addition to the
	// per-listen-port `local=` lines. The remote endpoint is KNOWN and route-independent, so it matches
	// the obfuscated flow regardless of which local source IP the kernel picks for a multi-homed node —
	// the root fix for "mimic local= filter used the wrong source IP and did not work". Inbound-only
	// peers (Endpoint=="") contribute no remote line; the local= lines still catch their flow. Hosts
	// are resolved to IPs at install time (getent) and IPv6 is bracketed. See docs/spec/artifacts/mimic.md.
	MimicRemotes []MimicEndpoint
	// MimicXDPMode is the xdp_mode written into the mimic config ("skb" or "native", already normalized, never empty).
	// Defaults to "skb" (generic XDP, compatible with VPS NICs that lack native support); "native" when the node explicitly sets it.
	// Spliced raw into the mimic config (ShellRaw).
	MimicXDPMode ShellToken
	// MimicNative is (MimicXDPMode == "native"): gates the install.sh native→skb auto-downgrade elif.
	// A precomputed bool because every conditional in these templates is a plain bool field (the shell
	// template engine has no `eq` comparison pipeline).
	MimicNative bool
	// MimicEgressInterface overrides the auto-detected mimic egress NIC ("" = auto-detect from the
	// default route). It is operator-supplied, so it is a single-quoted token (ShellQuoted).
	// MimicEgressOverride is (MimicEgressInterface != "") — a precomputed bool gate, computed from the
	// raw node field so an empty override is never rendered.
	MimicEgressInterface ShellToken
	MimicEgressOverride  bool
	// per-peer interface list
	WgInterfaces   []WgIfaceInfo
	BabelConfName  ShellToken // "babeld.conf", a fixed filename spliced into paths (ShellRaw)
	SysctlConfName ShellToken // "99-overlay.conf", a fixed filename spliced into paths (ShellRaw)
	// SigningPubkeyPEM is the Ed25519 verifying public key (PKIX/PKCS8 PEM) pinned into the
	// install script when bundle signing is enabled. The export path sets it (via
	// RenderInstallScriptSigned) only when the operator configured a signing key; otherwise it
	// is empty and the template emits no signature-verification block, so an unsigned bundle's
	// install.sh is byte-identical to the pre-signing output (opt-in back-compat). When non-empty
	// the template, before the existing sha256sum -c, verifies bundle.sig (raw Ed25519, base64)
	// over checksums.sha256 against this pinned key using openssl, failing clearly if bundle.sig
	// is present but openssl/Ed25519 is unavailable. See docs/spec/controller/signing.md. It is
	// spliced as the body of a single-quoted-delimiter heredoc (not shell-evaluated), so it is ShellRaw.
	SigningPubkeyPEM ShellToken
	// HasSigning gates the signature-verification block: true iff SigningPubkeyPEM is non-empty. It is a
	// precomputed bool because the template's {{ if }} tests it, and a ShellToken (a struct) is always
	// truthy to text/template — so the emptiness test must live in a bool, matching the MimicNative /
	// MimicEgressOverride idiom (every conditional in these templates is a plain bool field).
	HasSigning bool
	// SplicePlaceholder enables the AgentHeld custody splice block in Phase 2: after each
	// per-peer conf is copied to /etc/wireguard, the copied conf's placeholder PrivateKey line is
	// replaced in place with the node's locally-held private key read from /etc/wireguard/agent.key.
	// False (the default) emits no splice block, so the air-gap install.sh stays byte-identical to
	// the pre-splice output. The bundled confs are never touched, so the signed bundle stays pristine
	// and re-runs remain idempotent. See docs/spec/controller/key-custody.md.
	SplicePlaceholder bool
	// SplicePlaceholderToken is the exact sentinel that appears as the value of the [Interface]
	// PrivateKey line under AgentHeld custody (PrivateKeyPlaceholder, e.g. "PRIVATEKEY_PLACEHOLDER").
	// The splice block matches the literal line 'PrivateKey = <token>' and replaces only that line.
	// Only meaningful when SplicePlaceholder is true. It is a fixed sentinel spliced inside a
	// single-quoted literal in the template, so it is ShellRaw.
	SplicePlaceholderToken ShellToken
	// RequiresTelemetryPolicyV1 makes a probe-bearing AgentHeld installer require the matching
	// yaog-agent execution capability before any host mutation. It is false for air-gap bundles and
	// AgentHeld nodes without probes, preserving their historical installer bytes and direct usage.
	RequiresTelemetryPolicyV1 bool
	// GithubProxy is the shell-escaped GitHub-.deb mimic-fallback proxy prefix (was Fetch.GithubProxy;
	// model.InstallFetch's only install.sh-relevant field). It is baked as GH_PROXY=<token> and may be
	// operator-supplied, so it is a single-quoted token (ShellQuoted); the empty default renders GH_PROXY=''
	// only inside the HasMimic branch. Set by the signed renderer after buildInstallScriptConfig,
	// mirroring SigningPubkeyPEM.
	GithubProxy ShellToken
	// MimicFallbackUDP is the per-node resolved mimic fallback policy (plan-5): true when EVERY mimic
	// link on this node resolves to "udp" (plan-4), so a mimic-provisioning failure (kernel lacks
	// eBPF / unit start fails / binary missing) SKIPS mimic and brings WireGuard up as plain UDP
	// instead of aborting. False (the default, incl. policy "none"/inherit-off, AND any mixed node —
	// fail-closed wins) preserves the fail-closed abort. Only meaningful when HasMimic is true.
	MimicFallbackUDP bool
	// MimicBreadcrumb carries the breadcrumb contract constants (path + the closed MimicOutcome*
	// tokens) install.sh writes, sourced from model so the script and the agent reader share one
	// source of truth. Only referenced inside the {{ if .HasMimic }} block, so a non-mimic node's
	// install.sh is byte-identical to pre-plan-5.
	MimicBreadcrumb MimicBreadcrumbData
}

// MimicBreadcrumbData carries the mimic-provisioning breadcrumb contract for the install template:
// the path install.sh writes the marker to, and the closed-enum outcome tokens (model.MimicOutcome*).
// Sourced from package model so the script writer and the agent reader (plan-5) cannot drift. Each
// token is spliced into a shell-argument position (the _mimic_breadcrumb argument, the redirect
// target), so all fields are ShellQuoted — the constants carry no shell metacharacters, but quoting
// keeps every value that reaches the root shell inside the typed seam.
type MimicBreadcrumbData struct {
	Path              ShellToken
	Active            ShellToken
	KernelTooOld      ShellToken
	EbpfLoad          ShellToken
	InstallFailed     ShellToken
	FellBackToUDP     ShellToken
	EgressUnresolved  ShellToken
	NativeDowngraded  ShellToken
	ModuleUnavailable ShellToken
}

// newMimicBreadcrumbData returns the breadcrumb constants from package model (single source of truth),
// each wrapped as a single-quoted shell token (ShellQuoted) — byte-identical to the template's former
// {{ shq . }} of the same constants.
func newMimicBreadcrumbData() MimicBreadcrumbData {
	return MimicBreadcrumbData{
		Path:              ShellQuoted(model.MimicBreadcrumbPath),
		Active:            ShellQuoted(model.MimicOutcomeActive),
		KernelTooOld:      ShellQuoted(model.MimicOutcomeKernelTooOld),
		EbpfLoad:          ShellQuoted(model.MimicOutcomeEbpfLoad),
		InstallFailed:     ShellQuoted(model.MimicOutcomeInstallFailed),
		FellBackToUDP:     ShellQuoted(model.MimicOutcomeFellBackToUDP),
		EgressUnresolved:  ShellQuoted(model.MimicOutcomeEgressUnresolved),
		NativeDowngraded:  ShellQuoted(model.MimicOutcomeNativeDowngraded),
		ModuleUnavailable: ShellQuoted(model.MimicOutcomeModuleUnavailable),
	}
}

// resolveMimicFallbackUDP reports whether this node's mimic provisioning may fall back to plain UDP.
// True only when EVERY mimic link (p.Mimic) resolves to the "udp" policy (plan-4 PeerInfo.MimicFallback);
// a single "none" mimic link forces fail-closed for the whole node, since one shared mimic@<egress>
// unit serves all this node's mimic ports and partial fallback is not representable — fail-closed must
// win so a "none" link is never silently de-cloaked by a sibling "udp" link. A node with no mimic link
// returns false (no fallback branch rendered).
func resolveMimicFallbackUDP(peers []compiler.PeerInfo) bool {
	any := false
	for _, p := range peers {
		if !p.Mimic {
			continue
		}
		any = true
		if p.MimicFallback != "udp" {
			return false
		}
	}
	return any
}

// WgIfaceInfo describes a single WireGuard interface. Name and ConfName are derived from the
// (validated, sanitized) interface name and are spliced raw into the install template (inside command
// arguments and paths), so both are ShellRaw tokens.
type WgIfaceInfo struct {
	Name     ShellToken // interface name, e.g. wg-beta
	ConfName ShellToken // config file name, e.g. wg-beta.conf
}

// RenderInstallScript renders the install script.
//
// transitCIDRs is the resolved list of transit address pools for the domain this node belongs to,
// used to parameterize the SNAT source-address fix rule (audit D38/D39). Callers should pass the
// transit_cidr of the node's domain (falling back to the default 10.10.0.0/24 when empty). The
// parameter is variadic to preserve compatibility with existing three-argument callers: when omitted
// it falls back to the default pool, matching historical behavior. Empty-string entries are dropped
// and back-filled with the default pool, and duplicates are de-duplicated, so that there is one SNAT
// rule per distinct CIDR.
func RenderInstallScript(node *model.Node, peers []compiler.PeerInfo, hasBabel bool, transitCIDRs ...string) (string, error) {
	config := buildInstallScriptConfig(node, peers, hasBabel, transitCIDRs)
	return renderTemplate("install.sh", installScriptTemplate, config)
}

// CustodySplice carries the AgentHeld custody-splice parameters into the *Signed renderers.
//
// When Enabled is true, the rendered install.sh gains a Phase-2 block that, after copying each conf
// to /etc/wireguard, replaces the placeholder PrivateKey line (value == Token) in the COPIED conf
// with the node's locally-held key from /etc/wireguard/agent.key. The zero value (Enabled:false)
// emits no splice block, so the air-gap install.sh stays byte-identical to the pre-splice output.
// See docs/spec/controller/key-custody.md.
type CustodySplice struct {
	// Enabled turns the custody-splice block on. False = no splice = byte-identical to today.
	Enabled bool
	// Token is the exact PrivateKey value to match for replacement (PrivateKeyPlaceholder).
	Token string
}

// RenderInstallScriptSigned renders the per-peer install script with bundle-signature verification
// enabled: the rendered install.sh, before its existing sha256sum -c, verifies bundle.sig over
// checksums.sha256 against the pinned signingPubkeyPEM (PKIX/PKCS8 PEM) using openssl.
//
// splice gates the AgentHeld custody-splice block (CustodySplice{} disables it, keeping output
// byte-identical to the pre-splice path). This is the entry point the export path calls only when an
// operator signing key is configured (YAOG_BUNDLE_SIGNING_KEY). When signingPubkeyPEM is empty, the
// output is byte-identical to RenderInstallScript (opt-in back-compat). fetch carries the optional
// GitHub-.deb mimic-fallback pins (plan-3); its zero value adds nothing, keeping output identical.
// See docs/spec/controller/signing.md and docs/spec/controller/key-custody.md.
func RenderInstallScriptSigned(node *model.Node, peers []compiler.PeerInfo, hasBabel bool, signingPubkeyPEM string, splice CustodySplice, fetch model.InstallFetch, transitCIDRs ...string) (string, error) {
	config := buildInstallScriptConfig(node, peers, hasBabel, transitCIDRs)
	config.SigningPubkeyPEM = ShellRaw(signingPubkeyPEM)
	config.HasSigning = signingPubkeyPEM != ""
	config.SplicePlaceholder = splice.Enabled
	config.SplicePlaceholderToken = ShellRaw(splice.Token)
	config.RequiresTelemetryPolicyV1 = splice.Enabled && len(node.TelemetryProbes) > 0
	config.GithubProxy = ShellQuoted(fetch.GithubProxy)
	return renderTemplate("install.sh", installScriptTemplate, config)
}

// buildInstallScriptConfig assembles the per-peer InstallScriptConfig shared by the plain and
// signed renderers. SigningPubkeyPEM is left empty here; signed callers set it after.
func buildInstallScriptConfig(node *model.Node, peers []compiler.PeerInfo, hasBabel bool, transitCIDRs []string) InstallScriptConfig {
	// build the WireGuard interface list (interface names are validated, sanitized slugs → ShellRaw)
	var wgIfaces []WgIfaceInfo
	for _, p := range peers {
		wgIfaces = append(wgIfaces, WgIfaceInfo{
			Name:     ShellRaw(p.InterfaceName),
			ConfName: ShellRaw(p.InterfaceName + ".conf"),
		})
	}

	resolvedTransitCIDRs := resolveTransitCIDRs(transitCIDRs)

	// mimic port set: scan peers to collect the listen ports of all mimic interfaces (p.Mimic),
	// de-duplicated and sorted. The renderer uses this to emit one filter line per port on the node's
	// egress NIC (docs/spec/artifacts/mimic.md).
	mimicPorts := collectMimicPorts(peers)

	// Platform defaults to debian; resolve it before typing so the ShellToken carries the final value.
	platform := node.Platform
	if platform == "" {
		platform = "debian"
	}

	return InstallScriptConfig{
		NodeName:             ShellRaw(node.Name),
		NodeNameQuoted:       ShellQuoted(node.Name),
		NodeRole:             ShellRaw(node.Role),
		Platform:             ShellRaw(platform),
		OverlayIP:            ShellRaw(node.OverlayIP),
		TransitCIDRs:         shellRawSlice(resolvedTransitCIDRs),
		MTU:                  node.MTU,
		HasBabel:             hasBabel,
		HasForward:           node.Capabilities.CanForward,
		HasMimic:             len(mimicPorts) > 0,
		MimicPorts:           mimicPorts,
		MimicRemotes:         collectMimicRemotes(peers),
		MimicXDPMode:         ShellRaw(resolveMimicXDPMode(node.XDPMode)),
		MimicNative:          resolveMimicXDPMode(node.XDPMode) == "native",
		MimicEgressInterface: ShellQuoted(node.MimicEgressInterface),
		MimicEgressOverride:  node.MimicEgressInterface != "",
		MimicFallbackUDP:     resolveMimicFallbackUDP(peers),
		MimicBreadcrumb:      newMimicBreadcrumbData(),
		WgInterfaces:         wgIfaces,
		BabelConfName:        ShellRaw("babeld.conf"),
		SysctlConfName:       ShellRaw("99-overlay.conf"),
	}
}

// resolveMimicXDPMode normalizes a node's XDPMode into the value written to the mimic config.
// Only "native" passes through; empty, "skb", and any value other than those already rejected by
// validation fall back to "skb" (generic XDP, compatible with NICs that lack native support) — this
// is the default and the safest mode for VPS virtio NICs. The validity of the value is guaranteed by
// the validator's schema stage (""/"skb"/"native"); here we only perform a safe normalization.
func resolveMimicXDPMode(mode string) string {
	if mode == "native" {
		return "native"
	}
	return "skb"
}

// collectMimicPorts scans a set of peers and collects the listen ports of all mimic interfaces
// (p.Mimic==true), de-duplicated and sorted ascending. mimic attaches to the node's egress NIC, and
// each mimic listen port corresponds to one filter line (local=<egress_ip>:<port>) in the egress
// config; see docs/spec/artifacts/mimic.md. Only ports with ListenPort>0 are collected: 0 means the
// interface has no bound listen port and cannot become a mimic filter.
func collectMimicPorts(peers []compiler.PeerInfo) []int {
	seen := make(map[int]bool)
	var ports []int
	for _, p := range peers {
		if !p.Mimic || p.ListenPort <= 0 || seen[p.ListenPort] {
			continue
		}
		seen[p.ListenPort] = true
		ports = append(ports, p.ListenPort)
	}
	sort.Ints(ports)
	return ports
}

// MimicEndpoint is one mimic peer's dial target (host + port), used to emit a route-independent
// `remote=<ip>:<port>` filter line. The dial host (a hostname resolved at install time, or a literal
// IPv4/IPv6 address) is spliced into two shell-argument positions (the getent resolve, the fallback
// literal), so Host is a single-quoted token (ShellQuoted); Port is an int.
type MimicEndpoint struct {
	Host ShellToken
	Port int
}

// collectMimicRemotes returns the distinct set of mimic peer endpoints this node dials, parsed from
// PeerInfo.Endpoint (a "host:port" / "[v6]:port" string). A peer we do not dial (Endpoint=="") or an
// unparseable/zero-port endpoint contributes nothing — those flows are still covered by the per-port
// local= lines. Deterministically ordered (host, then port) so the rendered conf is stable. Dedup and
// sort run on the raw host string; each surviving host is then wrapped with ShellQuoted for the template.
func collectMimicRemotes(peers []compiler.PeerInfo) []MimicEndpoint {
	type rawEndpoint struct {
		host string
		port int
	}
	seen := make(map[string]bool)
	var raw []rawEndpoint
	for _, p := range peers {
		if !p.Mimic || p.Endpoint == "" {
			continue
		}
		host, portStr, err := net.SplitHostPort(p.Endpoint)
		if err != nil || host == "" {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		key := net.JoinHostPort(host, portStr)
		if seen[key] {
			continue
		}
		seen[key] = true
		raw = append(raw, rawEndpoint{host: host, port: port})
	}
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].host != raw[j].host {
			return raw[i].host < raw[j].host
		}
		return raw[i].port < raw[j].port
	})
	out := make([]MimicEndpoint, len(raw))
	for i, r := range raw {
		out[i] = MimicEndpoint{Host: ShellQuoted(r.host), Port: r.port}
	}
	return out
}

// NodeTransitCIDRs resolves the transit address pools that a node's SNAT fix should cover.
//
// A node's per-peer transit addresses come from the transit pool of its domain (domain.TransitCIDR,
// falling back to the default 10.10.0.0/24 when empty, matching the allocator/compiler resolution
// rules). A node belongs to only one domain, so it usually returns a single CIDR; a slice is returned
// to stay consistent with the InstallScriptConfig.TransitCIDRs contract and to avoid a signature
// change should cross-domain links appear in the future. Callers should pass the result to
// RenderInstallScript.
func NodeTransitCIDRs(topo *model.Topology, node *model.Node) []string {
	if topo == nil || node == nil {
		return []string{allocconst.DefaultTransitCIDR}
	}
	for i := range topo.Domains {
		if topo.Domains[i].ID != node.DomainID {
			continue
		}
		if cidr := topo.Domains[i].TransitCIDR; cidr != "" {
			return []string{cidr}
		}
		return []string{allocconst.DefaultTransitCIDR}
	}
	return []string{allocconst.DefaultTransitCIDR}
}

// resolveTransitCIDRs normalizes the caller-supplied transit address pools into a de-duplicated,
// non-empty, stable-order list. Empty-string entries are dropped; when the whole list is empty it
// falls back to the default pool [10.10.0.0/24], guaranteeing the SNAT rule always has a source pool
// to write, while keeping existing three-argument callers (which pass no transitCIDRs) behaving
// exactly as before.
func resolveTransitCIDRs(transitCIDRs []string) []string {
	seen := make(map[string]bool)
	resolved := make([]string, 0, len(transitCIDRs))
	for _, cidr := range transitCIDRs {
		if cidr == "" || seen[cidr] {
			continue
		}
		seen[cidr] = true
		resolved = append(resolved, cidr)
	}
	if len(resolved) == 0 {
		return []string{allocconst.DefaultTransitCIDR}
	}
	return resolved
}

// ClientInstallScriptConfig holds the data for rendering a client node's install script. Like
// InstallScriptConfig, every template-consumed string is a ShellToken (ShellQuoted / ShellRaw) — the
// same machine-checked root-shell escape seam, enforced by field_safety_test.
type ClientInstallScriptConfig struct {
	// NodeName is the original node name, header comment only (ShellRaw); echo positions use NodeNameQuoted.
	NodeName ShellToken
	// NodeNameQuoted is the node name as an inert single-quoted token (ShellQuoted), used in echo lines
	// executed under the root identity, to prevent command-substitution injection (D15). Same seam as
	// InstallScriptConfig.NodeNameQuoted.
	NodeNameQuoted ShellToken
	NodeRole       ShellToken // role enum, echo/comment (ShellRaw)
	Platform       ShellToken // platform label, header comment (ShellRaw)
	OverlayIP      ShellToken // allocator-produced overlay IP (ShellRaw)
	MTU            int
	SysctlConfName ShellToken // "99-overlay.conf" (ShellRaw)
	// HasMimic / MimicPorts are the same as in InstallScriptConfig: true when the client's sole wg0 link
	// has transport=="tcp", and MimicPorts is the client wg0's listen port (a single port).
	// See docs/spec/artifacts/mimic.md.
	HasMimic   bool
	MimicPorts []int
	// MimicRemotes is the client wg0's dial target (the router endpoint), emitting a route-independent
	// `remote=<ip>:<port>` filter line; same semantics as InstallScriptConfig.MimicRemotes. A single
	// entry (the client has one outbound link) or empty when the endpoint is unknown.
	MimicRemotes []MimicEndpoint
	MimicXDPMode ShellToken // normalized xdp_mode ("skb"/"native") spliced raw (ShellRaw), see InstallScriptConfig
	MimicNative  bool       // MimicXDPMode == "native"; gates the native→skb downgrade elif (precomputed bool)
	// MimicEgressInterface / MimicEgressOverride: same semantics as InstallScriptConfig (egress override).
	// The interface is operator-supplied → ShellQuoted; the override bool is computed from the raw field.
	MimicEgressInterface ShellToken
	MimicEgressOverride  bool
	// MimicFallbackUDP / MimicBreadcrumb: same semantics as InstallScriptConfig (plan-5). The client
	// has a single wg0 link, so the per-node resolution collapses to that link's policy.
	MimicFallbackUDP bool
	MimicBreadcrumb  MimicBreadcrumbData
	// SigningPubkeyPEM is the pinned Ed25519 verifying public key (PEM) for bundle-signature
	// verification; same semantics as InstallScriptConfig.SigningPubkeyPEM. Empty when signing is
	// off (opt-in), keeping the client install.sh byte-identical to the pre-signing output. Spliced as a
	// quoted-heredoc body (ShellRaw).
	SigningPubkeyPEM ShellToken
	// HasSigning gates the signature-verification block: true iff SigningPubkeyPEM is non-empty (a
	// precomputed bool because a ShellToken is always truthy to text/template). Same idiom as
	// InstallScriptConfig.HasSigning.
	HasSigning bool
	// SplicePlaceholder enables the AgentHeld custody splice block on the copied wg0.conf in Phase 2;
	// same semantics as InstallScriptConfig.SplicePlaceholder. False keeps the client install.sh
	// byte-identical to the pre-splice output. See docs/spec/controller/key-custody.md.
	SplicePlaceholder bool
	// SplicePlaceholderToken is the exact PrivateKey value to match for replacement
	// (PrivateKeyPlaceholder); same semantics as InstallScriptConfig.SplicePlaceholderToken. Spliced
	// inside a single-quoted literal in the template (ShellRaw). Only meaningful when SplicePlaceholder is true.
	SplicePlaceholderToken ShellToken
	// RequiresTelemetryPolicyV1 has the same fail-closed compatibility semantics as the peer/router
	// installer field above.
	RequiresTelemetryPolicyV1 bool
	// GithubProxy is the shell-escaped GitHub-.deb mimic-fallback proxy prefix (was Fetch.GithubProxy);
	// same semantics as InstallScriptConfig.GithubProxy (ShellQuoted). Empty default → no catalog → the
	// HasMimic branch renders GH_PROXY=''. Set by the signed renderer after buildClientInstallScriptConfig.
	GithubProxy ShellToken
}

// RenderClientInstallScript renders the install script for a client node.
//
// clientInfo is an optional variadic argument, backward-compatible with the existing single-argument
// calls (when omitted the mimic fields stay at their zero values and the output is byte-identical to
// the old implementation). When the client's sole wg0 link has transport=="tcp"
// (clientInfo.Mimic==true), its ListenPort is taken as the mimic filter port and mimic is wired in
// (see docs/spec/artifacts/mimic.md). The caller (internal/render) should pass the client's
// ClientPeerInfo to enable mimic support.
func RenderClientInstallScript(node *model.Node, clientInfo ...*compiler.ClientPeerInfo) (string, error) {
	config := buildClientInstallScriptConfig(node, clientInfo)
	return renderTemplate("client-install.sh", clientInstallScriptTemplate, config)
}

// RenderClientInstallScriptSigned renders the client install script with bundle-signature
// verification enabled (openssl Ed25519 verify of bundle.sig over checksums.sha256 against the
// pinned signingPubkeyPEM, before the existing sha256sum -c). splice gates the AgentHeld
// custody-splice block on the copied wg0.conf (CustodySplice{} disables it, keeping output
// byte-identical to the pre-splice path). Empty signingPubkeyPEM yields output byte-identical to
// RenderClientInstallScript (opt-in). The export path calls this only when an operator signing key
// is configured. fetch carries the optional GitHub-.deb mimic-fallback pins (plan-3); its zero value
// adds nothing, keeping output identical. See docs/spec/controller/signing.md and key-custody.md.
func RenderClientInstallScriptSigned(node *model.Node, signingPubkeyPEM string, splice CustodySplice, fetch model.InstallFetch, clientInfo ...*compiler.ClientPeerInfo) (string, error) {
	config := buildClientInstallScriptConfig(node, clientInfo)
	config.SigningPubkeyPEM = ShellRaw(signingPubkeyPEM)
	config.HasSigning = signingPubkeyPEM != ""
	config.SplicePlaceholder = splice.Enabled
	config.SplicePlaceholderToken = ShellRaw(splice.Token)
	config.RequiresTelemetryPolicyV1 = splice.Enabled && len(node.TelemetryProbes) > 0
	config.GithubProxy = ShellQuoted(fetch.GithubProxy)
	return renderTemplate("client-install.sh", clientInstallScriptTemplate, config)
}

// buildClientInstallScriptConfig assembles the ClientInstallScriptConfig shared by the plain and
// signed client renderers. SigningPubkeyPEM is left empty here; signed callers set it after.
func buildClientInstallScriptConfig(node *model.Node, clientInfo []*compiler.ClientPeerInfo) ClientInstallScriptConfig {
	// Platform defaults to debian; resolve before typing so the ShellToken carries the final value.
	platform := node.Platform
	if platform == "" {
		platform = "debian"
	}

	config := ClientInstallScriptConfig{
		NodeName:             ShellRaw(node.Name),
		NodeNameQuoted:       ShellQuoted(node.Name),
		NodeRole:             ShellRaw(node.Role),
		Platform:             ShellRaw(platform),
		OverlayIP:            ShellRaw(node.OverlayIP),
		MTU:                  node.MTU,
		MimicXDPMode:         ShellRaw(resolveMimicXDPMode(node.XDPMode)),
		MimicNative:          resolveMimicXDPMode(node.XDPMode) == "native",
		MimicEgressInterface: ShellQuoted(node.MimicEgressInterface),
		MimicEgressOverride:  node.MimicEgressInterface != "",
		MimicBreadcrumb:      newMimicBreadcrumbData(),
		SysctlConfName:       ShellRaw("99-overlay.conf"),
	}

	// The client wg0 is a single link: if its transport=="tcp" (Mimic==true) and the listen port is
	// valid, wire in mimic, with the filter port being that wg0's listen port. The per-node fallback
	// policy collapses to this one link's resolved policy (plan-5).
	if len(clientInfo) > 0 && clientInfo[0] != nil {
		ci := clientInfo[0]
		if ci.Mimic && ci.ListenPort > 0 {
			config.HasMimic = true
			config.MimicPorts = []int{ci.ListenPort}
			config.MimicFallbackUDP = ci.MimicFallback == "udp"
			// The client dials the router at RouterEndpoint (host:port); emit a route-independent
			// remote= filter for it. Parsed best-effort — an empty/unparseable endpoint just omits the
			// remote line (the local= line still covers wg0's listen port). The host is spliced into two
			// shell-argument positions, so it is ShellQuoted.
			if host, portStr, err := net.SplitHostPort(ci.RouterEndpoint); err == nil && host != "" {
				if port, perr := strconv.Atoi(portStr); perr == nil && port > 0 {
					config.MimicRemotes = []MimicEndpoint{{Host: ShellQuoted(host), Port: port}}
				}
			}
		}
	}

	return config
}
