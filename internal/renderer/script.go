package renderer

import (
	"sort"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// InstallScriptConfig holds the data for rendering the per-peer install script.
type InstallScriptConfig struct {
	NodeName string // original node name, used only in the script header comment (comments are not evaluated in bash)
	// NodeNameQuoted is the node name escaped by bashSingleQuote, used as a single-quoted shell token
	// spliced into echo lines executed under the root identity. Every place in the template that puts
	// the node name into an echo MUST use this field rather than NodeName; otherwise a node name like
	// x$(touch /tmp/pwned) would trigger command substitution under root (audit T4 / D15). Escaping is
	// done at the data-fill stage to keep the template readable.
	NodeNameQuoted string
	NodeRole       string
	Platform       string
	OverlayIP      string
	DomainCIDR     string // domain CIDR for source routing rule
	// TransitCIDRs is the resolved transit address pool of the domain this node belongs to
	// (domain.TransitCIDR, falling back to the default 10.10.0.0/24 when empty). The SNAT source-address
	// fix must emit rules per these pools: rewrite any source address that falls within a transit pool to
	// the overlay IP. Hard-coding 10.10.0.0/24 would silently break any node with a custom transit_cidr —
	// the source address after the packet arrives would still be the un-rewritten transit address, leaving
	// the route unreachable (audit D38/D39). The template emits one rule per CIDR; when empty,
	// RenderInstallScript falls back to the default pool so existing callers' behavior is unchanged.
	TransitCIDRs []string
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
	// MimicXDPMode is the xdp_mode written into the mimic config ("skb" or "native", already normalized, never empty).
	// Defaults to "skb" (generic XDP, compatible with VPS NICs that lack native support); "native" when the node explicitly sets it.
	MimicXDPMode string
	// per-peer interface list
	WgInterfaces   []WgIfaceInfo
	BabelConfName  string
	SysctlConfName string
	// SigningPubkeyPEM is the Ed25519 verifying public key (PKIX/PKCS8 PEM) pinned into the
	// install script when bundle signing is enabled. The export path sets it (via
	// RenderInstallScriptSigned) only when the operator configured a signing key; otherwise it
	// is empty and the template emits no signature-verification block, so an unsigned bundle's
	// install.sh is byte-identical to the pre-signing output (opt-in back-compat). When non-empty
	// the template, before the existing sha256sum -c, verifies bundle.sig (raw Ed25519, base64)
	// over checksums.sha256 against this pinned key using openssl, failing clearly if bundle.sig
	// is present but openssl/Ed25519 is unavailable. See docs/spec/controller/signing.md.
	SigningPubkeyPEM string
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
	// Only meaningful when SplicePlaceholder is true.
	SplicePlaceholderToken string
	// Fetch carries the GitHub-.deb mimic-fallback pins (plan-3). The zero value means no
	// catalog is configured, so the template emits no fetch branch and the install.sh stays
	// byte-identical to the pre-FetchSettings output (air-gap byte-identity). Set by the
	// signed renderer after buildInstallScriptConfig, mirroring SigningPubkeyPEM.
	Fetch model.InstallFetch
}

// WgIfaceInfo describes a single WireGuard interface.
type WgIfaceInfo struct {
	Name     string // interface name, e.g. wg-beta
	ConfName string // config file name, e.g. wg-beta.conf
}

const installScriptTemplate = `#!/usr/bin/env bash
# Install script for node: {{ .NodeName }}
# Generated by Overlay Network Config Orchestrator
# Platform: {{ .Platform }}
# Role: {{ .NodeRole }}
# Architecture: per-peer WireGuard interfaces
#
# Usage:
#   sudo bash install.sh              # Install / upgrade overlay
#   sudo bash install.sh --uninstall  # Completely remove overlay

set -euo pipefail

UNINSTALL=0
for arg in "$@"; do
    case "$arg" in
        --uninstall|-u) UNINSTALL=1 ;;
    esac
done

# ============================================================
# Uninstall All
# ============================================================

if [ "$UNINSTALL" -eq 1 ]; then
    # Check root
    if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root" >&2
        exit 1
    fi

    echo "=== Uninstalling overlay from node: "{{ .NodeNameQuoted }}" ==="

{{ if .HasMimic -}}
    # Tear down mimic TCP-shaping transport (docs/spec/artifacts/mimic.md): stop/disable the
    # mimic@<egress> unit and remove its config. Re-detect the egress NIC the same way the
    # installer did; tolerate absence (mimic may already be gone / no default route).
    _mimic_egress_if="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
    if [ -n "$_mimic_egress_if" ]; then
        echo "  Stopping mimic@$_mimic_egress_if..."
        systemctl disable --now "mimic@$_mimic_egress_if" 2>/dev/null || true
        rm -f "/etc/mimic/$_mimic_egress_if.conf"
    fi
{{ end -}}

    # Stop and disable all managed WireGuard interfaces
    {{ range .WgInterfaces -}}
    if command -v wg >/dev/null 2>&1 && wg show "{{ .Name }}" > /dev/null 2>&1; then
        echo "  Stopping WireGuard interface: {{ .Name }}..."
        wg-quick down "{{ .Name }}" 2>/dev/null || true
    fi
    if systemctl is-enabled "wg-quick@{{ .Name }}" >/dev/null 2>&1; then
        systemctl disable "wg-quick@{{ .Name }}" 2>/dev/null || true
    fi
    rm -f "/etc/wireguard/{{ .ConfName }}"
    {{ end -}}

    # Stop and disable ALL remaining WireGuard interfaces
    if command -v wg >/dev/null 2>&1; then
        for _iface in $(wg show interfaces 2>/dev/null); do
            echo "  Stopping WireGuard interface: $_iface..."
            wg-quick down "$_iface" 2>/dev/null || true
            systemctl disable "wg-quick@$_iface" 2>/dev/null || true
        done
    fi
    rm -f /etc/wireguard/*.conf

{{ if .HasBabel -}}
    # Stop and disable Babel
    if systemctl is-active babeld >/dev/null 2>&1; then
        echo "  Stopping Babel daemon..."
        systemctl stop babeld 2>/dev/null || true
    fi
    if systemctl is-enabled babeld >/dev/null 2>&1; then
        systemctl disable babeld 2>/dev/null || true
    fi
    rm -f "/etc/babel/{{ .BabelConfName }}" "/etc/{{ .BabelConfName }}"
    rm -rf /etc/systemd/system/babeld.service.d
{{ end -}}

    # Remove sysctl config
    rm -f "/etc/sysctl.d/{{ .SysctlConfName }}"
    sysctl --system > /dev/null 2>&1

    # Remove overlay SNAT rule and service
    #
    # D52: same as _overlay_snat_cleanup - delete each rule whole, matched by wg interface + transit
    # source pool and ignoring --to-source, so uninstall fully clears even stale rules left by a prior overlay IP change.
    if command -v nft >/dev/null 2>&1; then
        nft delete table inet overlay-snat 2>/dev/null || true
    fi
    if command -v iptables >/dev/null 2>&1 && command -v iptables-save >/dev/null 2>&1; then
        {{ range .TransitCIDRs -}}
        # Chained grep -F deletes each matching rule whole, order-independent; see Phase 1's _overlay_snat_cleanup.
        # grep returns 1 on no match; with set -o pipefail that makes the pipe non-zero and aborts under set -e, so || true guards it.
        iptables-save -t nat 2>/dev/null \
            | grep -E '^-A POSTROUTING ' \
            | grep -F -- '-j SNAT' \
            | grep -F -- '-o wg-+' \
            | grep -F -- '-s {{ . }}' \
            | while IFS= read -r _snat_rule; do
                _snat_del="${_snat_rule/#-A/-D}"
                # shellcheck disable=SC2086
                iptables -t nat $_snat_del 2>/dev/null || true
            done || true
        {{ end -}}
    fi
    if systemctl is-enabled overlay-snat.service >/dev/null 2>&1; then
        systemctl disable overlay-snat.service 2>/dev/null || true
    fi
    rm -f /etc/systemd/system/overlay-snat.service

    # Remove dummy0 overlay interface and its systemd service
    if ip link show dummy0 >/dev/null 2>&1; then
        echo "  Removing dummy0 interface..."
        ip link del dummy0 2>/dev/null || true
    fi
    if systemctl is-enabled overlay-dummy.service >/dev/null 2>&1; then
        systemctl disable overlay-dummy.service 2>/dev/null || true
    fi
    rm -f /etc/systemd/system/overlay-dummy.service
    systemctl daemon-reload

    echo ""
    echo "============================================================"
    echo "  Overlay completely removed from node: "{{ .NodeNameQuoted }}
    echo "============================================================"
    exit 0
fi

# ============================================================
# Phase 0: Cleanup Previous Installation
# ============================================================

echo "=== Phase 0: Cleanup Previous Installation ==="

# Stop all WireGuard interfaces managed by this overlay
{{ range .WgInterfaces -}}
if command -v wg >/dev/null 2>&1 && wg show "{{ .Name }}" > /dev/null 2>&1; then
    echo "  Stopping WireGuard interface: {{ .Name }}..."
    wg-quick down "{{ .Name }}" 2>/dev/null || true
fi
if systemctl is-enabled "wg-quick@{{ .Name }}" >/dev/null 2>&1; then
    systemctl disable "wg-quick@{{ .Name }}" 2>/dev/null || true
fi
if [ -f "/etc/wireguard/{{ .ConfName }}" ]; then
    rm -f "/etc/wireguard/{{ .ConfName }}"
    echo "  Removed old config: /etc/wireguard/{{ .ConfName }}"
fi
{{ end -}}

# Clean up ALL legacy/stale WireGuard interfaces and configs
# This catches wg0, wg1, wg-overlay, or any other leftover profiles
if command -v wg >/dev/null 2>&1; then
    for _legacy_iface in $(wg show interfaces 2>/dev/null); do
        # Skip interfaces managed by this overlay (already handled above)
        _is_managed=false
        {{ range .WgInterfaces -}}
        [ "$_legacy_iface" = "{{ .Name }}" ] && _is_managed=true
        {{ end -}}
        if [ "$_is_managed" = "false" ]; then
            echo "  Stopping legacy WireGuard interface: $_legacy_iface..."
            wg-quick down "$_legacy_iface" 2>/dev/null || true
            if systemctl is-enabled "wg-quick@$_legacy_iface" >/dev/null 2>&1; then
                systemctl disable "wg-quick@$_legacy_iface" 2>/dev/null || true
            fi
        fi
    done
fi
# Remove any leftover WireGuard config files not managed by this overlay
for _legacy_conf in /etc/wireguard/*.conf; do
    [ -f "$_legacy_conf" ] || continue
    _legacy_name="$(basename "$_legacy_conf")"
    _is_managed=false
    {{ range .WgInterfaces -}}
    [ "$_legacy_name" = "{{ .ConfName }}" ] && _is_managed=true
    {{ end -}}
    if [ "$_is_managed" = "false" ]; then
        rm -f "$_legacy_conf"
        echo "  Removed legacy config: $_legacy_conf"
    fi
done

{{ if .HasBabel -}}
# Stop Babel if running
if systemctl is-active babeld >/dev/null 2>&1; then
    echo "  Stopping Babel daemon..."
    systemctl stop babeld 2>/dev/null || true
fi
if systemctl is-enabled babeld >/dev/null 2>&1; then
    systemctl disable babeld 2>/dev/null || true
fi
for _bcf in "/etc/babel/{{ .BabelConfName }}" "/etc/{{ .BabelConfName }}"; do
    if [ -f "$_bcf" ]; then
        rm -f "$_bcf"
        echo "  Removed old Babel config: $_bcf"
    fi
done
{{ end -}}

if [ -f "/etc/sysctl.d/{{ .SysctlConfName }}" ]; then
    rm -f "/etc/sysctl.d/{{ .SysctlConfName }}"
    echo "  Removed old sysctl config"
fi

echo "Phase 0 complete."

# ============================================================
# Phase 1: Environment Preparation
# ============================================================

echo "=== Phase 1: Environment Preparation ==="

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

{{ if .SigningPubkeyPEM -}}
# Verify the bundle's Ed25519 signature BEFORE the checksum check (docs/spec/controller/signing.md).
# bundle.sig is base64(raw 64-byte Ed25519 signature) over the exact bytes of checksums.sha256
# (the canonical bundle digest). The verifying public key is pinned below at generation time, so a
# tampered checksums.sha256 (even with matching file hashes) is rejected before any root action.
if [ -f "$SCRIPT_DIR/bundle.sig" ]; then
    echo "Verifying bundle signature..."
    if ! command -v openssl >/dev/null 2>&1; then
        # bundle.sig is present but we cannot verify it: fail clearly, never silently skip.
        echo "ERROR: bundle.sig present but openssl is not installed; cannot verify signature" >&2
        exit 1
    fi
    # Write the pinned verifying public key to a temp file for openssl pkeyutl -pubin.
    _sig_pubkey="$(mktemp)"
    _sig_raw="$(mktemp)"
    cleanup_sig() {
        rm -f "$_sig_pubkey" "$_sig_raw"
    }
    trap cleanup_sig EXIT
    cat > "$_sig_pubkey" << 'YAOG_SIGNING_PUBKEY_PEM'
{{ .SigningPubkeyPEM }}
YAOG_SIGNING_PUBKEY_PEM
    # Decode base64 signature to raw bytes for openssl -rawin verification.
    if ! base64 -d "$SCRIPT_DIR/bundle.sig" > "$_sig_raw" 2>/dev/null; then
        echo "ERROR: failed to decode bundle.sig (not valid base64)" >&2
        exit 1
    fi
    # Ed25519 is a one-shot (raw) signature: -rawin feeds the message directly, no pre-hash.
    # openssl without Ed25519 support exits nonzero here, satisfying the fail-clear requirement.
    if ! openssl pkeyutl -verify -pubin -inkey "$_sig_pubkey" -rawin -sigfile "$_sig_raw" -in "$SCRIPT_DIR/checksums.sha256" >/dev/null 2>&1; then
        echo "ERROR: bundle signature verification failed (openssl missing Ed25519 support or signature invalid)" >&2
        exit 1
    fi
    echo "Bundle signature verification passed."
    cleanup_sig
    trap - EXIT
else
    # This install.sh was rendered with signing enabled, so bundle.sig is MANDATORY: a missing
    # signature is signature-stripping tamper, not an unsigned bundle. We KNOW the bundle was
    # signed at generation time (the verifying key is pinned above), so refuse to proceed rather
    # than fall through to the bare checksum check an attacker could satisfy with rewritten files.
    echo "ERROR: bundle was signed at generation but bundle.sig is missing; refusing to proceed (possible signature-stripping tamper)" >&2
    exit 1
fi

{{ end -}}
# Verify checksums if available
if [ -f "$SCRIPT_DIR/checksums.sha256" ]; then
    echo "Verifying file integrity..."
    cd "$SCRIPT_DIR"
    if ! sha256sum --status -c checksums.sha256; then
        echo "ERROR: Checksum validation failed!" >&2
        exit 1
    fi
    echo "Checksum validation passed."
    cd - >/dev/null
fi

# Check root
if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: This script must be run as root" >&2
    exit 1
fi

# Detect OS
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS_ID="$ID"
    OS_VERSION="$VERSION_ID"
    echo "Detected OS: $OS_ID $OS_VERSION"
else
    echo "ERROR: Cannot detect OS" >&2
    exit 1
fi

# Install dependencies (cross-distro, idempotent)
#
# This runs on EVERY controller apply (the daemon re-runs install.sh per generation) and on a
# manual air-gap run, so it is a fast no-op once the tools exist: each MISSING command is mapped
# to a package and installed via the detected manager. Supported: apt / dnf / yum / zypper /
# pacman / apk. Package names match across managers except iproute2 -> iproute on dnf/yum/zypper.
# An unknown manager with tools still missing fails with an explicit list (no confusing mid-run error).
echo "Installing dependencies..."

YAOG_PM=""
for _c in apt-get dnf yum zypper pacman apk; do
    if command -v "$_c" >/dev/null 2>&1; then YAOG_PM="$_c"; break; fi
done

_pm_pkg() { # map a generic package name to this manager's concrete name
    case "$1:$YAOG_PM" in
        iproute2:dnf|iproute2:yum|iproute2:zypper) echo iproute ;;
        *) echo "$1" ;;
    esac
}

_PM_REFRESHED=0
_pm_install() {
    local _pkg; _pkg="$(_pm_pkg "$1")"
    if [ "$_PM_REFRESHED" -eq 0 ]; then
        case "$YAOG_PM" in
            apt-get) apt-get update -qq || true ;;
            apk)     apk update >/dev/null 2>&1 || true ;;
        esac
        _PM_REFRESHED=1
    fi
    case "$YAOG_PM" in
        apt-get) DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$_pkg" ;;
        dnf)     dnf install -y "$_pkg" ;;
        yum)     yum install -y "$_pkg" ;;
        zypper)  zypper --non-interactive install "$_pkg" ;;
        pacman)  pacman -Sy --noconfirm "$_pkg" ;;
        apk)     apk add --no-progress "$_pkg" ;;
        *)       return 1 ;;
    esac
}

YAOG_MISSING=""
ensure_cmd() { # ensure_cmd <command> <generic-package>: install only if the command is absent
    command -v "$1" >/dev/null 2>&1 && return 0
    if [ -n "$YAOG_PM" ]; then
        echo "  - installing $2 (provides '$1')"
        _pm_install "$2" || true
    fi
    command -v "$1" >/dev/null 2>&1 || YAOG_MISSING="$YAOG_MISSING $1"
}

ensure_cmd wg       wireguard-tools
ensure_cmd wg-quick wireguard-tools
ensure_cmd ip       iproute2
ensure_cmd openssl  openssl
# SNAT uses nft when present, else iptables (+iptables-save, same package) — ensure one exists.
if ! command -v nft >/dev/null 2>&1 && ! command -v iptables >/dev/null 2>&1; then
    ensure_cmd iptables iptables
fi
{{ if .HasBabel -}}
ensure_cmd babeld babeld
{{ end -}}
{{ if .HasMimic -}}
# mimic TCP-shaping transport (docs/spec/artifacts/mimic.md): a link uses transport="tcp".
# YAOG ships no mimic binary. Prefer the distro package (Debian 13+, AUR, ...); on Debian 12 /
# Ubuntu 24.04, where mimic is not yet packaged, fall back to a SHA-256-PINNED .deb from GitHub.
# The pin lives in artifacts.json (a controller-signed bundle member, already integrity-verified
# above), so reading it here is not a trust boundary; the download is verified against that pin
# and FAILS CLOSED under set -e. GH_PROXY is shell-escaped (shq) at generation time.
GH_PROXY={{ shq .Fetch.GithubProxy }}
if ! command -v mimic >/dev/null 2>&1 && [ -n "$YAOG_PM" ]; then
    _pm_install mimic || true
fi
if ! command -v mimic >/dev/null 2>&1; then
    # Distro package unavailable -> pinned GitHub .deb (apt/dpkg systems only).
    if [ "$YAOG_PM" != "apt-get" ] || ! command -v dpkg >/dev/null 2>&1; then
        echo "ERROR: mimic is not in this distro's repositories and the GitHub .deb fallback requires apt/dpkg" >&2
        exit 1
    fi
    if [ ! -f "$SCRIPT_DIR/artifacts.json" ]; then
        echo "ERROR: mimic GitHub fallback needs artifacts.json (no mimic catalog was configured for this deploy)" >&2
        exit 1
    fi
    _mimic_codename="$(. /etc/os-release 2>/dev/null; echo "${VERSION_CODENAME:-}")"
    _mimic_arch="$(dpkg --print-architecture 2>/dev/null)"
    _mimic_key="${_mimic_codename}-${_mimic_arch}"
    # Read the pin with jq (auto-installed on this apt path if absent); fail closed if jq is
    # unavailable rather than hand-parse nested JSON in bash.
    if ! command -v jq >/dev/null 2>&1; then
        _pm_install jq || true
    fi
    command -v jq >/dev/null 2>&1 || { echo "ERROR: mimic GitHub fallback needs jq to read artifacts.json" >&2; exit 1; }
    _mimic_rel="$(jq -r '.mimic.release_url // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_asset="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].asset // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_sha="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].sha256 // ""' "$SCRIPT_DIR/artifacts.json")"
    if [ -z "$_mimic_rel" ] || [ -z "$_mimic_asset" ] || [ -z "$_mimic_sha" ]; then
        echo "ERROR: no pinned mimic .deb for '$_mimic_key' in artifacts.json" >&2
        exit 1
    fi
    echo "Installing mimic from a SHA-256-pinned GitHub .deb ($_mimic_key)..."
    _mimic_deb="$(mktemp --suffix=.deb)"
    curl -fL --retry 3 --proto '=https,http' "${GH_PROXY}${_mimic_rel}/${_mimic_asset}" -o "$_mimic_deb"
    echo "${_mimic_sha}  ${_mimic_deb}" | sha256sum -c -
    # mimic's .deb builds its eBPF module via DKMS -> kernel headers + toolchain.
    _pm_install "linux-headers-$(uname -r)" || _pm_install linux-headers-generic || true
    _pm_install dkms || true
    _pm_install gcc || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y "$_mimic_deb"
    rm -f "$_mimic_deb"
fi
command -v mimic >/dev/null 2>&1 || { echo "ERROR: mimic still missing after distro + GitHub .deb fallback" >&2; exit 1; }
# Kernel/eBPF sanity: mimic is an eBPF (TC/XDP) program; warn early if BPF looks absent.
if [ ! -d /sys/fs/bpf ] && ! grep -qw bpf /proc/filesystems 2>/dev/null; then
    echo "WARNING: eBPF/BPF filesystem not detected; mimic requires kernel eBPF support" >&2
fi
{{ end -}}

if [ -n "$YAOG_MISSING" ]; then
    echo "ERROR: missing required tools:$YAOG_MISSING" >&2
    echo "  No supported package manager installed them automatically. Install the equivalents of" >&2
    echo "  wireguard-tools (wg, wg-quick), iproute2 (ip), openssl, iptables or nftables{{ if .HasBabel }}, babeld{{ end }}, then re-run." >&2
    exit 1
fi

# WireGuard kernel module: built into Linux >= 5.6; load it (best-effort) on older kernels.
# If it is genuinely unavailable, wg-quick below surfaces the real error per interface.
modprobe wireguard 2>/dev/null || true

mkdir -p /etc/wireguard
{{ if .HasBabel -}}
mkdir -p /etc/babel
{{ end -}}

# Create dummy0 interface for stable overlay address
if ! ip link show dummy0 >/dev/null 2>&1; then
    echo "Creating dummy0 interface for overlay address..."
    ip link add dummy0 type dummy
fi
ip addr flush dev dummy0 2>/dev/null || true
ip addr add {{ .OverlayIP }}/32 dev dummy0
ip link set dummy0 up
echo "  Overlay address {{ .OverlayIP }}/32 assigned to dummy0"

# Make dummy0 persistent across reboots
cat > /etc/systemd/system/overlay-dummy.service << 'DUMMY_SVC'
[Unit]
Description=Overlay dummy interface
Before=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/sbin/ip link add dummy0 type dummy
ExecStart=/sbin/ip addr add {{ .OverlayIP }}/32 dev dummy0
ExecStart=/sbin/ip link set dummy0 up
ExecStop=/sbin/ip link del dummy0

[Install]
WantedBy=multi-user.target
DUMMY_SVC
systemctl daemon-reload
systemctl enable overlay-dummy.service 2>/dev/null || true

# Fix source address selection for overlay traffic
# Without this, packets to overlay IPs use the transit IP (10.10.0.x) as source
# instead of the overlay IP, causing silent failures for plain "ping <overlay_ip>"
echo "Configuring overlay source address fix..."

# Remove any previous overlay SNAT rules
#
# D52: we cannot delete by an exact rule (with --to-source <current overlay IP>): after an overlay IP change a
# reinstall would leave the old --to-source rule in POSTROUTING, wrongly source-rewriting packets to the old address.
# Instead: parse iptables-save and delete EVERY POSTROUTING SNAT rule matching the wg interface + transit source
# pool, whole, regardless of its --to-source. The nft path drops the whole table and has no such problem, so it is left as-is.
_overlay_snat_cleanup() {
    if command -v nft >/dev/null 2>&1; then
        nft delete table inet overlay-snat 2>/dev/null || true
    fi
    if command -v iptables >/dev/null 2>&1 && command -v iptables-save >/dev/null 2>&1; then
        {{ range .TransitCIDRs -}}
        # Delete rule by rule: in the iptables-save output, every POSTROUTING rule whose out interface is wg-+, source is {{ . }},
        # and action is SNAT is turned into a -D delete using its full parameters (ignoring the specific --to-source value).
        # Use chained grep -F rather than a single order-assuming regex: iptables-save normalizes parameter order
        # (usually -s before -o), so fixed-string matching is order-independent and needs no escaping of the . and / in the CIDR.
        # grep returns 1 on no match; with set -o pipefail the whole pipe returns non-zero, and under set -e
        # that would abort the script. The || true here swallows the normal "no old rule to delete" case.
        iptables-save -t nat 2>/dev/null \
            | grep -E '^-A POSTROUTING ' \
            | grep -F -- '-j SNAT' \
            | grep -F -- '-o wg-+' \
            | grep -F -- '-s {{ . }}' \
            | while IFS= read -r _snat_rule; do
                _snat_del="${_snat_rule/#-A/-D}"
                # shellcheck disable=SC2086
                iptables -t nat $_snat_del 2>/dev/null || true
            done || true
        {{ end -}}
    fi
}
_overlay_snat_cleanup

# Add SNAT rule: rewrite transit source IPs to overlay IP on WG interfaces
# Use nftables if available, fall back to iptables
if command -v nft >/dev/null 2>&1; then
    nft -f - <<'NFT_EOF'
table inet overlay-snat {
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        {{ range .TransitCIDRs -}}
        oifname "wg-*" ip saddr {{ . }} snat to {{ $.OverlayIP }}
        {{ end -}}
    }
}
NFT_EOF
    {{ range .TransitCIDRs -}}
    echo "  SNAT (nftables): transit {{ . }} → {{ $.OverlayIP }} on wg-* interfaces"
    {{ end -}}
elif command -v iptables >/dev/null 2>&1; then
    {{ range .TransitCIDRs -}}
    iptables -t nat -C POSTROUTING -o "wg-+" -s {{ . }} -j SNAT --to-source {{ $.OverlayIP }} 2>/dev/null || \
        iptables -t nat -A POSTROUTING -o "wg-+" -s {{ . }} -j SNAT --to-source {{ $.OverlayIP }}
    echo "  SNAT (iptables): transit {{ . }} → {{ $.OverlayIP }} on wg-* interfaces"
    {{ end -}}
fi

# Persist SNAT rule via systemd
cat > /etc/systemd/system/overlay-snat.service << 'SNAT_SVC'
[Unit]
Description=Overlay SNAT rule for source address fix
After=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash -c 'if command -v nft >/dev/null 2>&1; then nft delete table inet overlay-snat 2>/dev/null || true; nft add table inet overlay-snat; nft add chain inet overlay-snat postrouting "{ type nat hook postrouting priority srcnat; policy accept; }"; {{ range .TransitCIDRs }}nft add rule inet overlay-snat postrouting oifname "wg-*" ip saddr {{ . }} snat to {{ $.OverlayIP }}; {{ end }}else {{ range .TransitCIDRs }}iptables -t nat -D POSTROUTING -o wg-+ -s {{ . }} -j SNAT --to-source {{ $.OverlayIP }} 2>/dev/null || true; iptables -t nat -A POSTROUTING -o wg-+ -s {{ . }} -j SNAT --to-source {{ $.OverlayIP }}; {{ end }}fi'
ExecStop=/bin/bash -c 'if command -v nft >/dev/null 2>&1; then nft delete table inet overlay-snat 2>/dev/null || true; else {{ range .TransitCIDRs }}iptables -t nat -D POSTROUTING -o wg-+ -s {{ . }} -j SNAT --to-source {{ $.OverlayIP }} 2>/dev/null || true; {{ end }}fi'

[Install]
WantedBy=multi-user.target
SNAT_SVC
systemctl daemon-reload
systemctl enable overlay-snat.service 2>/dev/null || true

echo "Phase 1 complete."

# ============================================================
# Phase 2: Deploy Configuration
# ============================================================

echo "=== Phase 2: Deploy Configuration ==="

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Deploy per-peer WireGuard configurations
echo "Deploying WireGuard per-peer configurations..."
{{ range .WgInterfaces -}}
cp "$SCRIPT_DIR/wireguard/{{ .ConfName }}" /etc/wireguard/{{ .ConfName }}
chmod 600 /etc/wireguard/{{ .ConfName }}
echo "  Deployed: /etc/wireguard/{{ .ConfName }}"
{{ if $.SplicePlaceholder -}}
# AgentHeld custody: splice the node's locally-held private key into the COPIED conf (never the
# bundle conf — the signed bundle stays pristine so re-runs keep passing sha256sum -c). This runs
# in Phase 2, AFTER the Phase-1 signature/checksum verify (over the pristine placeholder bundle) and
# BEFORE wg-quick up. Injection-safe: no sed/regex; the key is read once and the file rewritten line
# by line. Re-run safe: the preceding cp restores the placeholder conf, so each run re-splices the
# same stable key deterministically; the grep guard skips only a conf that carries no placeholder
# (e.g. an air-gap bundle, which never renders this block).
if grep -qxF 'PrivateKey = {{ $.SplicePlaceholderToken }}' "/etc/wireguard/{{ .ConfName }}"; then
    if [ ! -s /etc/wireguard/agent.key ]; then
        echo "ERROR: /etc/wireguard/{{ .ConfName }} expects an agent-held private key but /etc/wireguard/agent.key is missing or empty" >&2
        exit 1
    fi
    # Command substitution strips the trailing newline, yielding the bare base64 key.
    _agent_key="$(cat /etc/wireguard/agent.key)"
    _spliced="$(mktemp)"
    # The scratch file transiently holds the real private key; remove it on any exit.
    trap 'rm -f "$_spliced"' EXIT
    while IFS= read -r line || [ -n "$line" ]; do
        if [ "$line" = 'PrivateKey = {{ $.SplicePlaceholderToken }}' ]; then
            printf 'PrivateKey = %s\n' "$_agent_key" >> "$_spliced"
        else
            printf '%s\n' "$line" >> "$_spliced"
        fi
    done < "/etc/wireguard/{{ .ConfName }}"
    cat "$_spliced" > "/etc/wireguard/{{ .ConfName }}"
    rm -f "$_spliced"
    trap - EXIT
    chmod 600 /etc/wireguard/{{ .ConfName }}
    echo "  Spliced agent-held private key into /etc/wireguard/{{ .ConfName }}"
fi
{{ end -}}
{{ end -}}

{{ if .HasBabel -}}
# Deploy Babel configuration
echo "Deploying Babel configuration..."
cp "$SCRIPT_DIR/babel/{{ .BabelConfName }}" /etc/babel/{{ .BabelConfName }}
echo "  Deployed: /etc/babel/{{ .BabelConfName }}"
{{ end -}}

# Deploy sysctl configuration
cp "$SCRIPT_DIR/sysctl/{{ .SysctlConfName }}" /etc/sysctl.d/{{ .SysctlConfName }}
echo "  Deployed: /etc/sysctl.d/{{ .SysctlConfName }}"

echo "Phase 2 complete."

# ============================================================
# Phase 3: Activate and Verify
# ============================================================

echo "=== Phase 3: Activate and Verify ==="

# Apply sysctl
echo "Applying sysctl settings..."
sysctl --system > /dev/null 2>&1
{{ if .HasForward -}}
echo "  IPv4 forwarding: $(cat /proc/sys/net/ipv4/ip_forward)"
{{ end -}}

{{ if .HasMimic -}}
# Provision mimic TCP-shaping transport BEFORE bringing WireGuard up, so the shaping is in
# place when the tunnel handshakes (docs/spec/artifacts/mimic.md «Ordering»).
#
# mimic attaches to the EGRESS NIC (the default-route interface), NOT the wg interface; the
# egress if/ip are not known at compile time, so detect them here at runtime. YAOG only supplies
# the mimic listen-port set via the template.
echo "Provisioning mimic TCP-shaping transport..."
MIMIC_EGRESS_IF="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
MIMIC_EGRESS_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
if [ -z "$MIMIC_EGRESS_IF" ] || [ -z "$MIMIC_EGRESS_IP" ]; then
    echo "ERROR: could not detect egress interface/IP for mimic (no default route?)" >&2
    exit 1
fi
echo "  mimic egress: $MIMIC_EGRESS_IF ($MIMIC_EGRESS_IP)"
mkdir -p /etc/mimic
# One filter per mimic listen port on this node; all OR'ed by mimic. xdp_mode is operator-selectable
# per node (default skb for portability; native opt-in via Node.xdp_mode) — see mimic.md.
{
    {{ range .MimicPorts -}}
    echo "filter = local=${MIMIC_EGRESS_IP}:{{ . }}"
    {{ end -}}
    echo "xdp_mode = {{ .MimicXDPMode }}"
} > "/etc/mimic/${MIMIC_EGRESS_IF}.conf"
echo "  Wrote /etc/mimic/${MIMIC_EGRESS_IF}.conf"
# The distro mimic package ships mimic@<iface>.service (Requires=modprobe@mimic.service, so the
# kernel module auto-loads). Enable+start it on the egress NIC before WireGuard comes up.
systemctl enable --now "mimic@${MIMIC_EGRESS_IF}"
echo "  Started mimic@${MIMIC_EGRESS_IF}"
{{ end -}}

# Start all WireGuard per-peer interfaces
#
# D53: the script runs under set -euo pipefail. If one interface's wg-quick up failed without being tolerated,
# the whole script would abort before configuring babeld, leaving a "half-started" node (some tunnels up but the
# routing daemon unconfigured). So collect each interface's start failure (appended to FAILED_INTERFACES),
# warn on stderr and continue so babeld and later steps still run; print a failure summary at the end and
# exit non-zero if any failed (so the deploy tool still sees it), but only after the remaining steps have run.
# Note the set -e interaction: use the 'if ! wg-quick up ...; then' form, or the non-zero return would abort outright.
echo "Starting WireGuard interfaces..."
FAILED_INTERFACES=""
{{ range .WgInterfaces -}}
echo "  Starting {{ .Name }}..."
if ! wg-quick up "{{ .Name }}"; then
    echo "WARNING: failed to bring up WireGuard interface {{ .Name }}; continuing with remaining setup" >&2
    FAILED_INTERFACES="$FAILED_INTERFACES {{ .Name }}"
fi
systemctl enable wg-quick@"{{ .Name }}" 2>/dev/null || true
{{ end -}}

{{ if .HasBabel -}}
# Configure babeld systemd service
echo "Configuring babeld systemd service..."
mkdir -p /etc/systemd/system/babeld.service.d
cat > /etc/systemd/system/babeld.service.d/override.conf << 'BABEL_OVERRIDE'
[Unit]
Description=Babel routing daemon (overlay)
After=network.target{{ range .WgInterfaces }} wg-quick@{{ .Name }}.service{{ end }}
Wants={{ range $i, $iface := .WgInterfaces }}{{ if $i }} {{ end }}wg-quick@{{ $iface.Name }}.service{{ end }}

[Service]
Type=simple
ExecStart=
ExecStart=/usr/sbin/babeld -c /etc/babel/{{ .BabelConfName }}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
BABEL_OVERRIDE
systemctl daemon-reload
systemctl restart babeld
systemctl enable babeld
echo "  Babel daemon started"
{{ end -}}

# Show status
echo ""
echo "============================================================"
echo "  Node: "{{ .NodeNameQuoted }}
echo "  Overlay IP: {{ .OverlayIP }} (on dummy0)"
echo "  Role: {{ .NodeRole }}"
echo "  WireGuard interfaces: {{ range .WgInterfaces }}{{ .Name }} {{ end }}"
{{- if gt .MTU 0 }}
echo "  MTU: {{ .MTU }}"
{{- end }}
echo "============================================================"
echo ""
echo "WireGuard status:"
{{ range .WgInterfaces -}}
echo "--- {{ .Name }} ---"
wg show "{{ .Name }}" 2>/dev/null || echo "  (not yet connected)"
{{ end -}}
echo ""
{{ if .HasBabel -}}
echo "Babel status:"
echo "  Check with: nc ::1 33123 (then type 'dump')"
echo ""
{{ end -}}
echo "Installation complete!"
echo "Note: If peers are not yet online, connections will establish once they come up."

# D53: summarize the WireGuard interface start results. If any failed to start, print the failure list and exit
# non-zero so the upstream deploy tool can detect it - but this exit happens after babeld config, status display,
# and the other remaining steps have all completed, so the node is never left in a "half-started" state.
if [ -n "$FAILED_INTERFACES" ]; then
    echo ""
    echo "WARNING: the following WireGuard interface(s) failed to start:$FAILED_INTERFACES" >&2
    echo "         the rest of the installation completed; re-run 'wg-quick up <iface>' to retry." >&2
    exit 1
fi
`

// defaultTransitCIDR is the default value of the transit address pool, matching allocateTransitPair's fallback.
const defaultTransitCIDR = "10.10.0.0/24"

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
	config.SigningPubkeyPEM = signingPubkeyPEM
	config.SplicePlaceholder = splice.Enabled
	config.SplicePlaceholderToken = splice.Token
	config.Fetch = fetch
	return renderTemplate("install.sh", installScriptTemplate, config)
}

// buildInstallScriptConfig assembles the per-peer InstallScriptConfig shared by the plain and
// signed renderers. SigningPubkeyPEM is left empty here; signed callers set it after.
func buildInstallScriptConfig(node *model.Node, peers []compiler.PeerInfo, hasBabel bool, transitCIDRs []string) InstallScriptConfig {
	// build the WireGuard interface list
	var wgIfaces []WgIfaceInfo
	for _, p := range peers {
		wgIfaces = append(wgIfaces, WgIfaceInfo{
			Name:     p.InterfaceName,
			ConfName: p.InterfaceName + ".conf",
		})
	}

	resolvedTransitCIDRs := resolveTransitCIDRs(transitCIDRs)

	// mimic port set: scan peers to collect the listen ports of all mimic interfaces (p.Mimic),
	// de-duplicated and sorted. The renderer uses this to emit one filter line per port on the node's
	// egress NIC (docs/spec/artifacts/mimic.md).
	mimicPorts := collectMimicPorts(peers)

	config := InstallScriptConfig{
		NodeName:       node.Name,
		NodeNameQuoted: bashSingleQuote(node.Name),
		NodeRole:       node.Role,
		Platform:       node.Platform,
		OverlayIP:      node.OverlayIP,
		TransitCIDRs:   resolvedTransitCIDRs,
		MTU:            node.MTU,
		HasBabel:       hasBabel,
		HasForward:     node.Capabilities.CanForward,
		HasMimic:       len(mimicPorts) > 0,
		MimicPorts:     mimicPorts,
		MimicXDPMode:   resolveMimicXDPMode(node.XDPMode),
		WgInterfaces:   wgIfaces,
		BabelConfName:  "babeld.conf",
		SysctlConfName: "99-overlay.conf",
	}

	if config.Platform == "" {
		config.Platform = "debian"
	}

	return config
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
		return []string{defaultTransitCIDR}
	}
	for i := range topo.Domains {
		if topo.Domains[i].ID != node.DomainID {
			continue
		}
		if cidr := topo.Domains[i].TransitCIDR; cidr != "" {
			return []string{cidr}
		}
		return []string{defaultTransitCIDR}
	}
	return []string{defaultTransitCIDR}
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
		return []string{defaultTransitCIDR}
	}
	return resolved
}

// ClientInstallScriptConfig holds the data for rendering a client node's install script.
type ClientInstallScriptConfig struct {
	NodeName string // original node name, used only in the script header comment (comments are not evaluated in bash)
	// NodeNameQuoted is the same as InstallScriptConfig.NodeNameQuoted: the node name escaped by
	// bashSingleQuote, used in echo lines executed under the root identity, to prevent command-substitution injection (D15).
	NodeNameQuoted string
	NodeRole       string
	Platform       string
	OverlayIP      string
	MTU            int
	SysctlConfName string
	// HasMimic / MimicPorts are the same as in InstallScriptConfig: true when the client's sole wg0 link
	// has transport=="tcp", and MimicPorts is the client wg0's listen port (a single port).
	// See docs/spec/artifacts/mimic.md.
	HasMimic     bool
	MimicPorts   []int
	MimicXDPMode string // normalized xdp_mode ("skb"/"native"), see InstallScriptConfig
	// SigningPubkeyPEM is the pinned Ed25519 verifying public key (PEM) for bundle-signature
	// verification; same semantics as InstallScriptConfig.SigningPubkeyPEM. Empty when signing is
	// off (opt-in), keeping the client install.sh byte-identical to the pre-signing output.
	SigningPubkeyPEM string
	// SplicePlaceholder enables the AgentHeld custody splice block on the copied wg0.conf in Phase 2;
	// same semantics as InstallScriptConfig.SplicePlaceholder. False keeps the client install.sh
	// byte-identical to the pre-splice output. See docs/spec/controller/key-custody.md.
	SplicePlaceholder bool
	// SplicePlaceholderToken is the exact PrivateKey value to match for replacement
	// (PrivateKeyPlaceholder); same semantics as InstallScriptConfig.SplicePlaceholderToken. Only
	// meaningful when SplicePlaceholder is true.
	SplicePlaceholderToken string
	// Fetch carries the GitHub-.deb mimic-fallback pins (plan-3); same semantics as
	// InstallScriptConfig.Fetch. Zero value = no catalog → no fetch branch → client install.sh
	// byte-identical. Set by the signed renderer after buildClientInstallScriptConfig.
	Fetch model.InstallFetch
}

const clientInstallScriptTemplate = `#!/usr/bin/env bash
# Install script for client node: {{ .NodeName }}
# Generated by Overlay Network Config Orchestrator
# Platform: {{ .Platform }}
# Role: {{ .NodeRole }}
# Architecture: single-interface (client)
#
# Usage:
#   sudo bash install.sh              # Install / upgrade overlay
#   sudo bash install.sh --uninstall  # Completely remove overlay

set -euo pipefail

UNINSTALL=0
for arg in "$@"; do
    case "$arg" in
        --uninstall|-u) UNINSTALL=1 ;;
    esac
done

# ============================================================
# Uninstall All
# ============================================================

if [ "$UNINSTALL" -eq 1 ]; then
    # Check root
    if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root" >&2
        exit 1
    fi

    echo "=== Uninstalling overlay from client node: "{{ .NodeNameQuoted }}" ==="

{{ if .HasMimic -}}
    # Tear down mimic TCP-shaping transport (docs/spec/artifacts/mimic.md): re-detect the egress
    # NIC, stop/disable mimic@<egress> and remove its config. Tolerate absence.
    _mimic_egress_if="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
    if [ -n "$_mimic_egress_if" ]; then
        echo "  Stopping mimic@$_mimic_egress_if..."
        systemctl disable --now "mimic@$_mimic_egress_if" 2>/dev/null || true
        rm -f "/etc/mimic/$_mimic_egress_if.conf"
    fi
{{ end -}}

    # Stop and disable wg0
    if command -v wg >/dev/null 2>&1 && wg show "wg0" > /dev/null 2>&1; then
        echo "  Stopping WireGuard interface: wg0..."
        wg-quick down "wg0" 2>/dev/null || true
    fi
    if systemctl is-enabled "wg-quick@wg0" >/dev/null 2>&1; then
        systemctl disable "wg-quick@wg0" 2>/dev/null || true
    fi
    rm -f "/etc/wireguard/wg0.conf"

    # Stop and disable ALL remaining WireGuard interfaces
    if command -v wg >/dev/null 2>&1; then
        for _iface in $(wg show interfaces 2>/dev/null); do
            echo "  Stopping WireGuard interface: $_iface..."
            wg-quick down "$_iface" 2>/dev/null || true
            systemctl disable "wg-quick@$_iface" 2>/dev/null || true
        done
    fi
    rm -f /etc/wireguard/*.conf

    # Remove sysctl config
    rm -f "/etc/sysctl.d/{{ .SysctlConfName }}"
    sysctl --system > /dev/null 2>&1

    echo ""
    echo "============================================================"
    echo "  Overlay completely removed from client node: "{{ .NodeNameQuoted }}
    echo "============================================================"
    exit 0
fi

# ============================================================
# Phase 0: Cleanup Previous Installation
# ============================================================

echo "=== Phase 0: Cleanup Previous Installation ==="

# Stop WireGuard wg0 if running
if command -v wg >/dev/null 2>&1 && wg show "wg0" > /dev/null 2>&1; then
    echo "  Stopping WireGuard interface: wg0..."
    wg-quick down "wg0" 2>/dev/null || true
fi
if systemctl is-enabled "wg-quick@wg0" >/dev/null 2>&1; then
    systemctl disable "wg-quick@wg0" 2>/dev/null || true
fi
if [ -f "/etc/wireguard/wg0.conf" ]; then
    rm -f "/etc/wireguard/wg0.conf"
    echo "  Removed old config: /etc/wireguard/wg0.conf"
fi

# Clean up legacy WireGuard interfaces
if command -v wg >/dev/null 2>&1; then
    for _legacy_iface in $(wg show interfaces 2>/dev/null); do
        [ "$_legacy_iface" = "wg0" ] && continue
        echo "  Stopping legacy WireGuard interface: $_legacy_iface..."
        wg-quick down "$_legacy_iface" 2>/dev/null || true
        if systemctl is-enabled "wg-quick@$_legacy_iface" >/dev/null 2>&1; then
            systemctl disable "wg-quick@$_legacy_iface" 2>/dev/null || true
        fi
    done
fi
for _legacy_conf in /etc/wireguard/*.conf; do
    [ -f "$_legacy_conf" ] || continue
    _legacy_name="$(basename "$_legacy_conf")"
    [ "$_legacy_name" = "wg0.conf" ] && continue
    rm -f "$_legacy_conf"
    echo "  Removed legacy config: $_legacy_conf"
done

if [ -f "/etc/sysctl.d/{{ .SysctlConfName }}" ]; then
    rm -f "/etc/sysctl.d/{{ .SysctlConfName }}"
    echo "  Removed old sysctl config"
fi

echo "Phase 0 complete."

# ============================================================
# Phase 1: Environment Preparation
# ============================================================

echo "=== Phase 1: Environment Preparation ==="

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

{{ if .SigningPubkeyPEM -}}
# Verify the bundle's Ed25519 signature BEFORE the checksum check (docs/spec/controller/signing.md).
# bundle.sig is base64(raw 64-byte Ed25519 signature) over the exact bytes of checksums.sha256
# (the canonical bundle digest). The verifying public key is pinned below at generation time, so a
# tampered checksums.sha256 (even with matching file hashes) is rejected before any root action.
if [ -f "$SCRIPT_DIR/bundle.sig" ]; then
    echo "Verifying bundle signature..."
    if ! command -v openssl >/dev/null 2>&1; then
        # bundle.sig is present but we cannot verify it: fail clearly, never silently skip.
        echo "ERROR: bundle.sig present but openssl is not installed; cannot verify signature" >&2
        exit 1
    fi
    # Write the pinned verifying public key to a temp file for openssl pkeyutl -pubin.
    _sig_pubkey="$(mktemp)"
    _sig_raw="$(mktemp)"
    cleanup_sig() {
        rm -f "$_sig_pubkey" "$_sig_raw"
    }
    trap cleanup_sig EXIT
    cat > "$_sig_pubkey" << 'YAOG_SIGNING_PUBKEY_PEM'
{{ .SigningPubkeyPEM }}
YAOG_SIGNING_PUBKEY_PEM
    # Decode base64 signature to raw bytes for openssl -rawin verification.
    if ! base64 -d "$SCRIPT_DIR/bundle.sig" > "$_sig_raw" 2>/dev/null; then
        echo "ERROR: failed to decode bundle.sig (not valid base64)" >&2
        exit 1
    fi
    # Ed25519 is a one-shot (raw) signature: -rawin feeds the message directly, no pre-hash.
    # openssl without Ed25519 support exits nonzero here, satisfying the fail-clear requirement.
    if ! openssl pkeyutl -verify -pubin -inkey "$_sig_pubkey" -rawin -sigfile "$_sig_raw" -in "$SCRIPT_DIR/checksums.sha256" >/dev/null 2>&1; then
        echo "ERROR: bundle signature verification failed (openssl missing Ed25519 support or signature invalid)" >&2
        exit 1
    fi
    echo "Bundle signature verification passed."
    cleanup_sig
    trap - EXIT
else
    # This install.sh was rendered with signing enabled, so bundle.sig is MANDATORY: a missing
    # signature is signature-stripping tamper, not an unsigned bundle. We KNOW the bundle was
    # signed at generation time (the verifying key is pinned above), so refuse to proceed rather
    # than fall through to the bare checksum check an attacker could satisfy with rewritten files.
    echo "ERROR: bundle was signed at generation but bundle.sig is missing; refusing to proceed (possible signature-stripping tamper)" >&2
    exit 1
fi

{{ end -}}
# Verify checksums if available
if [ -f "$SCRIPT_DIR/checksums.sha256" ]; then
    echo "Verifying file integrity..."
    cd "$SCRIPT_DIR"
    if ! sha256sum --status -c checksums.sha256; then
        echo "ERROR: Checksum validation failed!" >&2
        exit 1
    fi
    echo "Checksum validation passed."
    cd - >/dev/null
fi

# Check root
if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: This script must be run as root" >&2
    exit 1
fi

# Detect OS
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS_ID="$ID"
    OS_VERSION="$VERSION_ID"
    echo "Detected OS: $OS_ID $OS_VERSION"
else
    echo "ERROR: Cannot detect OS" >&2
    exit 1
fi

# Install dependencies (cross-distro, idempotent; no Babel/iptables for a client)
#
# A client needs only the WireGuard userspace + iproute2 + openssl (signed bundles). Same
# fast-no-op-once-present logic as the router script: map each MISSING command to a package
# and install via the detected manager (apt/dnf/yum/zypper/pacman/apk). iproute2 -> iproute
# on dnf/yum/zypper. Unknown manager with tools still missing fails with an explicit list.
echo "Installing dependencies..."

YAOG_PM=""
for _c in apt-get dnf yum zypper pacman apk; do
    if command -v "$_c" >/dev/null 2>&1; then YAOG_PM="$_c"; break; fi
done

_pm_pkg() {
    case "$1:$YAOG_PM" in
        iproute2:dnf|iproute2:yum|iproute2:zypper) echo iproute ;;
        *) echo "$1" ;;
    esac
}

_PM_REFRESHED=0
_pm_install() {
    local _pkg; _pkg="$(_pm_pkg "$1")"
    if [ "$_PM_REFRESHED" -eq 0 ]; then
        case "$YAOG_PM" in
            apt-get) apt-get update -qq || true ;;
            apk)     apk update >/dev/null 2>&1 || true ;;
        esac
        _PM_REFRESHED=1
    fi
    case "$YAOG_PM" in
        apt-get) DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$_pkg" ;;
        dnf)     dnf install -y "$_pkg" ;;
        yum)     yum install -y "$_pkg" ;;
        zypper)  zypper --non-interactive install "$_pkg" ;;
        pacman)  pacman -Sy --noconfirm "$_pkg" ;;
        apk)     apk add --no-progress "$_pkg" ;;
        *)       return 1 ;;
    esac
}

YAOG_MISSING=""
ensure_cmd() {
    command -v "$1" >/dev/null 2>&1 && return 0
    if [ -n "$YAOG_PM" ]; then
        echo "  - installing $2 (provides '$1')"
        _pm_install "$2" || true
    fi
    command -v "$1" >/dev/null 2>&1 || YAOG_MISSING="$YAOG_MISSING $1"
}

ensure_cmd wg       wireguard-tools
ensure_cmd wg-quick wireguard-tools
ensure_cmd ip       iproute2
ensure_cmd openssl  openssl
{{ if .HasMimic -}}
# mimic TCP-shaping transport (docs/spec/artifacts/mimic.md): the client wg0 link uses
# transport="tcp". YAOG ships no mimic binary. Distro-first, else a SHA-256-PINNED GitHub .deb
# whose pin lives in the integrity-verified artifacts.json (mirrors the per-peer install.sh).
GH_PROXY={{ shq .Fetch.GithubProxy }}
if ! command -v mimic >/dev/null 2>&1 && [ -n "$YAOG_PM" ]; then
    _pm_install mimic || true
fi
if ! command -v mimic >/dev/null 2>&1; then
    if [ "$YAOG_PM" != "apt-get" ] || ! command -v dpkg >/dev/null 2>&1; then
        echo "ERROR: mimic is not in this distro's repositories and the GitHub .deb fallback requires apt/dpkg" >&2
        exit 1
    fi
    if [ ! -f "$SCRIPT_DIR/artifacts.json" ]; then
        echo "ERROR: mimic GitHub fallback needs artifacts.json (no mimic catalog was configured for this deploy)" >&2
        exit 1
    fi
    _mimic_codename="$(. /etc/os-release 2>/dev/null; echo "${VERSION_CODENAME:-}")"
    _mimic_arch="$(dpkg --print-architecture 2>/dev/null)"
    _mimic_key="${_mimic_codename}-${_mimic_arch}"
    if ! command -v jq >/dev/null 2>&1; then
        _pm_install jq || true
    fi
    command -v jq >/dev/null 2>&1 || { echo "ERROR: mimic GitHub fallback needs jq to read artifacts.json" >&2; exit 1; }
    _mimic_rel="$(jq -r '.mimic.release_url // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_asset="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].asset // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_sha="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].sha256 // ""' "$SCRIPT_DIR/artifacts.json")"
    if [ -z "$_mimic_rel" ] || [ -z "$_mimic_asset" ] || [ -z "$_mimic_sha" ]; then
        echo "ERROR: no pinned mimic .deb for '$_mimic_key' in artifacts.json" >&2
        exit 1
    fi
    echo "Installing mimic from a SHA-256-pinned GitHub .deb ($_mimic_key)..."
    _mimic_deb="$(mktemp --suffix=.deb)"
    curl -fL --retry 3 --proto '=https,http' "${GH_PROXY}${_mimic_rel}/${_mimic_asset}" -o "$_mimic_deb"
    echo "${_mimic_sha}  ${_mimic_deb}" | sha256sum -c -
    _pm_install "linux-headers-$(uname -r)" || _pm_install linux-headers-generic || true
    _pm_install dkms || true
    _pm_install gcc || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y "$_mimic_deb"
    rm -f "$_mimic_deb"
fi
command -v mimic >/dev/null 2>&1 || { echo "ERROR: mimic still missing after distro + GitHub .deb fallback" >&2; exit 1; }
if [ ! -d /sys/fs/bpf ] && ! grep -qw bpf /proc/filesystems 2>/dev/null; then
    echo "WARNING: eBPF/BPF filesystem not detected; mimic requires kernel eBPF support" >&2
fi
{{ end -}}

if [ -n "$YAOG_MISSING" ]; then
    echo "ERROR: missing required tools:$YAOG_MISSING" >&2
    echo "  No supported package manager installed them automatically. Install the equivalents of" >&2
    echo "  wireguard-tools (wg, wg-quick), iproute2 (ip), openssl, then re-run." >&2
    exit 1
fi

# WireGuard kernel module: built into Linux >= 5.6; load it (best-effort) on older kernels.
modprobe wireguard 2>/dev/null || true

mkdir -p /etc/wireguard

echo "Phase 1 complete."

# ============================================================
# Phase 2: Deploy Configuration
# ============================================================

echo "=== Phase 2: Deploy Configuration ==="

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Deploy WireGuard wg0 configuration
echo "Deploying WireGuard client configuration..."
cp "$SCRIPT_DIR/wireguard/wg0.conf" /etc/wireguard/wg0.conf
chmod 600 /etc/wireguard/wg0.conf
echo "  Deployed: /etc/wireguard/wg0.conf"
{{ if .SplicePlaceholder -}}
# AgentHeld custody: splice the node's locally-held private key into the COPIED wg0.conf (never the
# bundle conf — the signed bundle stays pristine so re-runs keep passing sha256sum -c). This runs
# in Phase 2, AFTER the Phase-1 signature/checksum verify (over the pristine placeholder bundle) and
# BEFORE wg-quick up. Injection-safe: no sed/regex; the key is read once and the file rewritten line
# by line. Re-run safe: the preceding cp restores the placeholder conf, so each run re-splices the
# same stable key deterministically; the grep guard skips only a conf that carries no placeholder
# (e.g. an air-gap bundle, which never renders this block).
if grep -qxF 'PrivateKey = {{ .SplicePlaceholderToken }}' /etc/wireguard/wg0.conf; then
    if [ ! -s /etc/wireguard/agent.key ]; then
        echo "ERROR: /etc/wireguard/wg0.conf expects an agent-held private key but /etc/wireguard/agent.key is missing or empty" >&2
        exit 1
    fi
    # Command substitution strips the trailing newline, yielding the bare base64 key.
    _agent_key="$(cat /etc/wireguard/agent.key)"
    _spliced="$(mktemp)"
    # The scratch file transiently holds the real private key; remove it on any exit.
    trap 'rm -f "$_spliced"' EXIT
    while IFS= read -r line || [ -n "$line" ]; do
        if [ "$line" = 'PrivateKey = {{ .SplicePlaceholderToken }}' ]; then
            printf 'PrivateKey = %s\n' "$_agent_key" >> "$_spliced"
        else
            printf '%s\n' "$line" >> "$_spliced"
        fi
    done < /etc/wireguard/wg0.conf
    cat "$_spliced" > /etc/wireguard/wg0.conf
    rm -f "$_spliced"
    trap - EXIT
    chmod 600 /etc/wireguard/wg0.conf
    echo "  Spliced agent-held private key into /etc/wireguard/wg0.conf"
fi
{{ end -}}

# Deploy sysctl configuration
cp "$SCRIPT_DIR/sysctl/{{ .SysctlConfName }}" /etc/sysctl.d/{{ .SysctlConfName }}
echo "  Deployed: /etc/sysctl.d/{{ .SysctlConfName }}"

echo "Phase 2 complete."

# ============================================================
# Phase 3: Activate and Verify
# ============================================================

echo "=== Phase 3: Activate and Verify ==="

# Apply sysctl
echo "Applying sysctl settings..."
sysctl --system > /dev/null 2>&1

{{ if .HasMimic -}}
# Provision mimic TCP-shaping transport BEFORE bringing wg0 up (docs/spec/artifacts/mimic.md
# «Ordering»). mimic attaches to the EGRESS NIC, detected at runtime; YAOG supplies the port set.
echo "Provisioning mimic TCP-shaping transport..."
MIMIC_EGRESS_IF="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
MIMIC_EGRESS_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
if [ -z "$MIMIC_EGRESS_IF" ] || [ -z "$MIMIC_EGRESS_IP" ]; then
    echo "ERROR: could not detect egress interface/IP for mimic (no default route?)" >&2
    exit 1
fi
echo "  mimic egress: $MIMIC_EGRESS_IF ($MIMIC_EGRESS_IP)"
mkdir -p /etc/mimic
{
    {{ range .MimicPorts -}}
    echo "filter = local=${MIMIC_EGRESS_IP}:{{ . }}"
    {{ end -}}
    echo "xdp_mode = {{ .MimicXDPMode }}"
} > "/etc/mimic/${MIMIC_EGRESS_IF}.conf"
echo "  Wrote /etc/mimic/${MIMIC_EGRESS_IF}.conf"
systemctl enable --now "mimic@${MIMIC_EGRESS_IF}"
echo "  Started mimic@${MIMIC_EGRESS_IF}"
{{ end -}}

# Start WireGuard wg0
echo "Starting WireGuard wg0..."
wg-quick up "wg0"
systemctl enable wg-quick@"wg0" 2>/dev/null || true

# Show status
echo ""
echo "============================================================"
echo "  Node: "{{ .NodeNameQuoted }}
echo "  Overlay IP: {{ .OverlayIP }} (on wg0)"
echo "  Role: {{ .NodeRole }}"
echo "  WireGuard interface: wg0"
{{- if gt .MTU 0 }}
echo "  MTU: {{ .MTU }}"
{{- end }}
echo "============================================================"
echo ""
echo "WireGuard status:"
echo "--- wg0 ---"
wg show "wg0" 2>/dev/null || echo "  (not yet connected)"
echo ""
echo "Installation complete!"
echo "Note: If the router is not yet online, connection will establish once it comes up."
`

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
	config.SigningPubkeyPEM = signingPubkeyPEM
	config.SplicePlaceholder = splice.Enabled
	config.SplicePlaceholderToken = splice.Token
	config.Fetch = fetch
	return renderTemplate("client-install.sh", clientInstallScriptTemplate, config)
}

// buildClientInstallScriptConfig assembles the ClientInstallScriptConfig shared by the plain and
// signed client renderers. SigningPubkeyPEM is left empty here; signed callers set it after.
func buildClientInstallScriptConfig(node *model.Node, clientInfo []*compiler.ClientPeerInfo) ClientInstallScriptConfig {
	config := ClientInstallScriptConfig{
		NodeName:       node.Name,
		NodeNameQuoted: bashSingleQuote(node.Name),
		NodeRole:       node.Role,
		Platform:       node.Platform,
		OverlayIP:      node.OverlayIP,
		MTU:            node.MTU,
		MimicXDPMode:   resolveMimicXDPMode(node.XDPMode),
		SysctlConfName: "99-overlay.conf",
	}

	// The client wg0 is a single link: if its transport=="tcp" (Mimic==true) and the listen port is
	// valid, wire in mimic, with the filter port being that wg0's listen port.
	if len(clientInfo) > 0 && clientInfo[0] != nil {
		ci := clientInfo[0]
		if ci.Mimic && ci.ListenPort > 0 {
			config.HasMimic = true
			config.MimicPorts = []int{ci.ListenPort}
		}
	}

	if config.Platform == "" {
		config.Platform = "debian"
	}

	return config
}
