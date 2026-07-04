package model

// Artifact is one fetchable, integrity-pinned file: its release asset name and the
// SHA-256 the downloader must verify the bytes against before use. It is the shared
// pin type carried by render.FetchSettings (mimic .debs, agent binaries) and, here,
// by InstallFetch for the install.sh GitHub-.deb mimic fallback. Defined in package
// renderer (the lowest consumer — the install-script template) so render can reference
// it without the render -> renderer -> render import cycle that would arise if it lived
// in render. Zero value (both fields empty) means "no pin", which callers treat as
// absent.
type Artifact struct {
	Asset  string `json:"asset"`
	SHA256 string `json:"sha256"`
}

// MimicDebPin is ONE mimic-catalog row's pinned package PAIR for a "<codename>-<arch>": the
// userspace `mimic` .deb (Asset/SHA256) AND its companion `mimic-dkms` .deb (DKMSAsset/DKMSSHA256).
// Upstream hack3ric/mimic ships both per distro/arch; the `mimic` package declares
// `Depends: mimic-modules`, a virtual package `Provides`d by `mimic-dkms`, so the install MUST fetch
// and dpkg BOTH or apt cannot satisfy the dependency (the rc.1 live-fleet `exit status 100`).
//
// It is deliberately NOT a reuse of Artifact: that type is shared by the mimic map, the agent
// self-update bins, AND the release-pin Assist response, so a mimic-only `dkms_*` field on Artifact
// would leak into the agent + Assist paths. The layout is flat + additive — the legacy Asset/SHA256
// keep their meaning (the `mimic` pkg), so encoding/json round-trips an old {asset,sha256}-only
// catalog into a pin with an empty (absent) dkms companion, and an old reader ignores the unknown
// dkms_* fields. A row with no DKMS companion installs only `mimic` and so fails on split-package
// distros (Debian 12 / Ubuntu 24.04) — surfaced by validation, and degradable under mimic_fallback=udp.
type MimicDebPin struct {
	Asset      string `json:"asset"`
	SHA256     string `json:"sha256"`
	DKMSAsset  string `json:"dkms_asset,omitempty"`
	DKMSSHA256 string `json:"dkms_sha256,omitempty"`
}

// InstallFetch is the install.sh-relevant subset of render.FetchSettings. The install.sh
// mimic-from-GitHub fallback reads the pin (release_url + per-"<codename>-<arch>" asset +
// sha256) from the integrity-verified artifacts.json bundle member at install time — the
// single signed source of truth — so the only value that must be BAKED into the script is
// the optional GitHub proxy prefix (a deploy-network preference, kept out of the signed
// catalog so changing it does not churn the bundle digest). It is set on
// InstallScriptConfig/ClientInstallScriptConfig by the signed renderers (after
// buildInstallScriptConfig, mirroring SigningPubkeyPEM).
//
// The zero value carries no proxy and, paired with HasMimic=false / no artifacts.json,
// leaves install.sh byte-identical to the pre-FetchSettings output (air-gap byte-identity).
type InstallFetch struct {
	// GithubProxy is an optional prefix applied to GitHub downloads (e.g.
	// "https://gh-proxy.com/"). Empty = direct github.com. It is shell-escaped (shq) at the
	// template boundary before being baked as GH_PROXY.
	GithubProxy string
}

// MimicBreadcrumbPath is the absolute path install.sh writes the mimic-provisioning outcome
// breadcrumb to (a small JSON marker keyed by the MimicOutcome* Go constants below — never raw
// stderr). The agent reads it each cycle to emit the `mimic` Node Condition (plan-5). It lives
// outside the bundle (host-local mutable state) so it survives re-applies, mirroring the agent state
// dir. install.sh creates the directory 0700 under root. See docs/spec/artifacts/mimic.md (UDP
// fallback). A node with no tcp (mimic) link never writes it ⇒ the agent reads ENOENT ⇒ no condition.
const MimicBreadcrumbPath = "/var/lib/yaog-agent/mimic-status.json"

// MimicOutcome* are the closed-enum outcome codes install.sh writes into the breadcrumb's "outcome"
// field. They are the ONLY values the script emits; the agent's classifyMimic maps each to a
// Condition reason. Adding a value here is a coordinated script+agent change (the contract is closed).
const (
	MimicOutcomeActive           = "active"            // mimic provisioned + mimic@<egress> started
	MimicOutcomeKernelTooOld     = "kernel_too_old"    // eBPF/bpffs absent → mimic cannot load
	MimicOutcomeEbpfLoad         = "ebpf_load_failed"  // mimic@<egress> failed to start (eBPF attach or unit-start error)
	MimicOutcomeInstallFailed    = "install_failed"    // distro pkg + pinned .deb both failed
	MimicOutcomeFellBackToUDP    = "fell_back_to_udp"  // policy=udp: skipped mimic, link up as plain UDP
	MimicOutcomeEgressUnresolved = "egress_unresolved" // egress IP empty or loopback (no routable default-route src) → the local= filter would never match the real WG source; treated per policy
)
