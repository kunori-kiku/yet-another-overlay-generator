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
	MimicOutcomeActive        = "active"           // mimic provisioned + mimic@<egress> started
	MimicOutcomeKernelTooOld  = "kernel_too_old"   // eBPF/bpffs absent → mimic cannot load
	MimicOutcomeEbpfLoad      = "ebpf_load_failed" // mimic@<egress> failed to start (eBPF attach or unit-start error)
	MimicOutcomeInstallFailed = "install_failed"   // distro pkg + pinned .deb both failed
	MimicOutcomeFellBackToUDP = "fell_back_to_udp" // policy=udp: skipped mimic, link up as plain UDP
)
