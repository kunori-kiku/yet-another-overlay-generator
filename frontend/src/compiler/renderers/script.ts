// Install-script renderer — the TypeScript mirror of internal/renderer/script.go.
//
// Two templates: installScriptTemplate (per-peer interfaces; router/relay/gateway/peer roles) and
// clientInstallScriptTemplate (single wg0; client role). Both are COPIED VERBATIM from script.go
// (installScriptTemplate at :84-805, clientInstallScriptTemplate at :1009-1436) — every {{- -}} chomp
// marker preserved — so the rendered install.sh bytes are identical to the Go oracle. The template
// engine (template.ts) is the only thing that must be correct; the conformance harness arbitrates
// byte-equality.
//
// VERBATIM-COPY note: the templates are bash, so they contain backslashes and `${...}` shell
// expansions. In a JS template literal those would be interpreted (\\ -> \, ${ -> interpolation), so
// they are JS-escaped (\\ for a literal backslash, \${ for a literal "${") — the RUNTIME string value
// is byte-identical to the Go raw-string const. The escaping is mechanical and was generated directly
// from the Go source, never hand-transcribed.
//
// Local-mode scope (the conformance gate): signing is a NO-OP (no signer in local mode, so
// SigningPubkeyPEM is empty and the signature-verify block never renders); custody is AirGap (no
// splice block) unless the caller selects AgentHeld (then SplicePlaceholderToken =
// PrivateKeyPlaceholder and the splice block renders, mirroring render.AllWith's per-node custody
// detection keys[node.ID].PrivateKey == PrivateKeyPlaceholder). The SNAT transit literal is
// DefaultTransitCIDR ("10.10.0.0/24", allocconst) — the script.go:805 pinned site.

import { DefaultTransitCIDR } from '../allocconst';
import { bashSingleQuote } from '../escape';
import type {
  ClientPeerInfo,
  KeyPair,
  Node,
  PeerInfo,
  Topology,
} from '../model';
import { renderTemplate } from './template';

// PrivateKeyPlaceholder is the AgentHeld custody sentinel emitted on a node's [Interface] PrivateKey
// line. Mirrors render.PrivateKeyPlaceholder (render.go:56). When a node's KeyPair.privateKey equals
// this, the install.sh splice block is rendered (SplicePlaceholder=true, Token=this), matching
// render.AllWith (render.go:371-372). Intentionally NOT valid base64.
const PrivateKeyPlaceholder = 'PRIVATEKEY_PLACEHOLDER';

// WgIfaceInfo describes a single WireGuard interface for the install template. Mirrors
// renderer.WgIfaceInfo (script.go:79-82).
interface WgIfaceInfo {
  Name: string;
  ConfName: string;
}

// InstallScriptConfig is the template data for the per-peer install script. Field names are the Go
// struct field names (PascalCase) the template references. Mirrors renderer.InstallScriptConfig
// (script.go:12-76); the fields not reachable in local mode (SigningPubkeyPEM stays '', Fetch carries
// only the empty GithubProxy) are present so the template's branches resolve identically to Go.
interface InstallScriptConfig {
  NodeName: string;
  NodeNameQuoted: string;
  NodeRole: string;
  Platform: string;
  OverlayIP: string;
  TransitCIDRs: string[];
  MTU: number;
  HasBabel: boolean;
  HasForward: boolean;
  HasMimic: boolean;
  MimicPorts: number[];
  MimicRemotes: MimicEndpoint[];
  MimicXDPMode: string;
  MimicNative: boolean;
  MimicEgressInterface: string;
  MimicEgressOverride: boolean;
  MimicFallbackUDP: boolean;
  MimicBreadcrumb: MimicBreadcrumbData;
  WgInterfaces: WgIfaceInfo[];
  BabelConfName: string;
  SysctlConfName: string;
  SigningPubkeyPEM: string;
  SplicePlaceholder: boolean;
  SplicePlaceholderToken: string;
  Fetch: { GithubProxy: string };
}

// MimicBreadcrumbData carries the mimic-provisioning breadcrumb contract for the install template:
// the path install.sh writes the marker to, and the closed-enum outcome tokens. Mirrors
// renderer.MimicBreadcrumbData (script.go), sourced from the same Go model constants (model.MimicOutcome*).
interface MimicBreadcrumbData {
  Path: string;
  Active: string;
  KernelTooOld: string;
  EbpfLoad: string;
  InstallFailed: string;
  FellBackToUDP: string;
  EgressUnresolved: string;
  NativeDowngraded: string;
  ModuleUnavailable: string;
}

// MimicEndpoint is one mimic peer's dial target (host + port) for a route-independent remote= filter.
// PascalCase fields so the template's {{ .Host }} / {{ .Port }} resolve. Mirrors renderer.MimicEndpoint.
interface MimicEndpoint {
  Host: string;
  Port: number;
}

// ClientInstallScriptConfig is the template data for the client install script. Mirrors
// renderer.ClientInstallScriptConfig (script.go:975-1007).
interface ClientInstallScriptConfig {
  NodeName: string;
  NodeNameQuoted: string;
  NodeRole: string;
  Platform: string;
  OverlayIP: string;
  MTU: number;
  SysctlConfName: string;
  HasMimic: boolean;
  MimicPorts: number[];
  MimicRemotes: MimicEndpoint[];
  MimicXDPMode: string;
  MimicNative: boolean;
  MimicEgressInterface: string;
  MimicEgressOverride: boolean;
  MimicFallbackUDP: boolean;
  MimicBreadcrumb: MimicBreadcrumbData;
  SigningPubkeyPEM: string;
  SplicePlaceholder: boolean;
  SplicePlaceholderToken: string;
  Fetch: { GithubProxy: string };
}

// installScriptTemplate is copied VERBATIM from script.go:84-805 (see the VERBATIM-COPY note).
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
    _mimic_egress_if={{ if .MimicEgressOverride }}{{ shq .MimicEgressInterface }}{{ else }}"$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"{{ end }}
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
        iptables-save -t nat 2>/dev/null \\
            | grep -E '^-A POSTROUTING ' \\
            | grep -F -- '-j SNAT' \\
            | grep -F -- '-o wg-+' \\
            | grep -F -- '-s {{ . }}' \\
            | while IFS= read -r _snat_rule; do
                _snat_del="\${_snat_rule/#-A/-D}"
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

# rc.4: stop any stale mimic@ unit + config, unconditionally. The mimic teardown otherwise lives only
# in the --uninstall path (HasMimic-gated), so flipping a node's last tcp link to udp (HasMimic
# true->false) would never stop the old mimic@ (it would keep shaping traffic WG now sends as plain
# UDP). A still-mimic node re-provisions in Phase 3; no-op when mimic was never installed.
for _stale_mimic in $(systemctl list-units --plain --no-legend 'mimic@*.service' 2>/dev/null | awk '{print $1}'); do
    echo "  Stopping stale mimic unit: $_stale_mimic..."
    systemctl disable --now "$_stale_mimic" 2>/dev/null || true
done
rm -f /etc/mimic/*.conf 2>/dev/null || true

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
# mimic-provisioning outcome breadcrumb (plan-5): a small Go-constant-keyed JSON marker the agent
# reads to emit the mimic Node Condition. Only the OUTCOME token (a fixed shell literal from a Go
# constant) and the kernel-derived egress NIC (set in Phase 3; empty here) are interpolated — never
# captured stderr / node name / upstream text (PRINCIPLES root-script safety).
mkdir -p /var/lib/yaog-agent
_mimic_breadcrumb() {
    printf '{"outcome":"%s","egress":"%s","ts":"%s"}\\n' \\
        "$1" "\${MIMIC_EGRESS_IF:-}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \\
        > {{ shq .MimicBreadcrumb.Path }}
}
# _MIMIC_SKIP is set when mimic could not be provisioned AND policy=udp, so the Phase-3 provisioning
# block skips mimic and the link comes up as plain UDP. Empty = provision normally.
_MIMIC_SKIP=
# _mimic_provision installs mimic — the distro package, else a SHA-256-pinned GitHub .deb PAIR. It
# RETURNS non-zero on any failure instead of exiting, so the caller honors the link's mimic_fallback
# policy (udp: skip to plain UDP; none: fail closed) rather than aborting the whole apply under set -e.
# Upstream mimic ships TWO packages: mimic (userspace) and mimic-dkms (the eBPF module, which
# Provides the mimic-modules the mimic pkg Depends on), so BOTH .debs are fetched, verified against
# their pins, and dpkg'd together — installing only mimic cannot satisfy the dependency.
_mimic_provision() {
    command -v mimic >/dev/null 2>&1 && return 0
    if [ -n "$YAOG_PM" ]; then _pm_install mimic || true; fi
    command -v mimic >/dev/null 2>&1 && return 0
    # Distro package unavailable -> pinned GitHub .deb (apt/dpkg systems only).
    if [ "$YAOG_PM" != "apt-get" ] || ! command -v dpkg >/dev/null 2>&1; then
        echo "ERROR: mimic is not in this distro's repositories and the GitHub .deb fallback requires apt/dpkg" >&2
        return 1
    fi
    if [ ! -f "$SCRIPT_DIR/artifacts.json" ]; then
        echo "ERROR: mimic GitHub fallback needs artifacts.json (no mimic catalog was configured for this deploy)" >&2
        return 1
    fi
    _mimic_codename="$(. /etc/os-release 2>/dev/null; echo "\${VERSION_CODENAME:-}")"
    _mimic_arch="$(dpkg --print-architecture 2>/dev/null)"
    _mimic_key="\${_mimic_codename}-\${_mimic_arch}"
    # Read the pins with jq (auto-installed on this apt path if absent); fail if jq is unavailable
    # rather than hand-parse nested JSON in bash.
    if ! command -v jq >/dev/null 2>&1; then
        _pm_install jq || true
    fi
    command -v jq >/dev/null 2>&1 || { echo "ERROR: mimic GitHub fallback needs jq to read artifacts.json" >&2; return 1; }
    _mimic_rel="$(jq -r '.mimic.release_url // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_asset="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].asset // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_sha="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].sha256 // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_dkms_asset="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].dkms_asset // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_dkms_sha="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].dkms_sha256 // ""' "$SCRIPT_DIR/artifacts.json")"
    if [ -z "$_mimic_rel" ] || [ -z "$_mimic_asset" ] || [ -z "$_mimic_sha" ]; then
        echo "ERROR: no pinned mimic .deb for '$_mimic_key' in artifacts.json" >&2
        return 1
    fi
    echo "Installing mimic from a SHA-256-pinned GitHub .deb ($_mimic_key)..."
    # mimic-dkms builds the eBPF module via DKMS -> kernel headers + toolchain (best-effort; a stale
    # kernel whose exact headers left the repo cannot build until it reboots into the current kernel).
    _pm_install "linux-headers-$(uname -r)" || _pm_install linux-headers-generic || true
    _pm_install dkms || true
    _pm_install gcc || true
    _pm_install bubblewrap || true   # mimic-dkms's build sandbox (bwrap); the DKMS build is Error 127 without it
    _pm_install dwarves || true      # provides pahole for the module's BTF generation (else 'pahole: not found')
    # _mimic_get <asset> <sha256> <dest>: download via the proxy and verify the pin (0 ok / non-zero fail).
    _mimic_get() {
        curl -fL --retry 3 --proto '=https,http' "\${GH_PROXY}\${_mimic_rel}/$1" -o "$3" || return 1
        echo "$2  $3" | sha256sum -c -
    }
    _mimic_deb="$(mktemp --suffix=.deb)"
    _mimic_get "$_mimic_asset" "$_mimic_sha" "$_mimic_deb" || { rm -f "$_mimic_deb"; return 1; }
    _mimic_install="$_mimic_deb"
    if [ -n "$_mimic_dkms_asset" ] && [ -n "$_mimic_dkms_sha" ]; then
        _mimic_dkms_deb="$(mktemp --suffix=.deb)"
        _mimic_get "$_mimic_dkms_asset" "$_mimic_dkms_sha" "$_mimic_dkms_deb" || { rm -f "$_mimic_deb" "$_mimic_dkms_deb"; return 1; }
        _mimic_install="$_mimic_install $_mimic_dkms_deb"
    fi
    # Install both .debs together so mimic's Depends: mimic-modules resolves from the local dkms .deb.
    if ! DEBIAN_FRONTEND=noninteractive apt-get install -y $_mimic_install; then
        rm -f $_mimic_install
        return 1
    fi
    rm -f $_mimic_install
    command -v mimic >/dev/null 2>&1
}
# _mimic_module_ready reports 0 iff mimic's DKMS kernel module is built AND loadable for the running
# kernel. mimic's eBPF program calls a kfunc the module exports; without the module loaded 'mimic run'
# fails to load the BPF program (exit 22). Installing the .deb (userspace binary) does NOT guarantee
# the module built — a stale kernel whose linux-headers-$(uname -r) were pruned from the repo leaves
# DKMS at "added" (never built). Try to load it; else build it via DKMS for THIS kernel and re-check.
_mimic_module_ready() {
    lsmod 2>/dev/null | grep -qw mimic && return 0
    modprobe mimic 2>/dev/null && return 0
    if command -v dkms >/dev/null 2>&1; then
        _pm_install "linux-headers-$(uname -r)" >/dev/null 2>&1 || true
        _pm_install bubblewrap >/dev/null 2>&1 || true
        _pm_install dwarves >/dev/null 2>&1 || true
        dkms autoinstall -k "$(uname -r)" >/dev/null 2>&1 || true
        modprobe mimic 2>/dev/null && return 0
    fi
    return 1
}
if ! _mimic_provision; then
{{ if .MimicFallbackUDP -}}
    # policy=udp: mimic could not be provisioned — skip it, bring the link up as plain UDP.
    echo "WARNING: mimic could not be provisioned; falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.InstallFailed }}
    _MIMIC_SKIP=1
{{ else -}}
    echo "ERROR: mimic could not be provisioned and this link's mimic_fallback policy is fail-closed" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.InstallFailed }}
    exit 1
{{ end -}}
elif ! _mimic_module_ready; then
{{ if .MimicFallbackUDP -}}
    # policy=udp: the mimic binary installed but its kernel module isn't usable on this kernel — skip
    # mimic and bring the link up as plain UDP (this closes the false-success that used to defeat the
    # fallback when only the binary, not the module, was present).
    echo "WARNING: the mimic kernel module could not be built/loaded for kernel $(uname -r); falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.ModuleUnavailable }}
    _MIMIC_SKIP=1
{{ else -}}
    echo "ERROR: the mimic kernel module could not be built/loaded for kernel $(uname -r) — reboot into the current kernel so DKMS can build it, or set this link's mimic_fallback to udp" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.ModuleUnavailable }}
    exit 1
{{ end -}}
fi
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
        iptables-save -t nat 2>/dev/null \\
            | grep -E '^-A POSTROUTING ' \\
            | grep -F -- '-j SNAT' \\
            | grep -F -- '-o wg-+' \\
            | grep -F -- '-s {{ . }}' \\
            | while IFS= read -r _snat_rule; do
                _snat_del="\${_snat_rule/#-A/-D}"
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
    iptables -t nat -C POSTROUTING -o "wg-+" -s {{ . }} -j SNAT --to-source {{ $.OverlayIP }} 2>/dev/null || \\
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
            printf 'PrivateKey = %s\\n' "$_agent_key" >> "$_spliced"
        else
            printf '%s\\n' "$line" >> "$_spliced"
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
if [ -n "\${_MIMIC_SKIP:-}" ]; then
# The mimic binary could not be installed and this node's policy is udp — skip provisioning; the
# WireGuard interfaces come up as plain UDP below (install_failed was breadcrumbed in the deps phase).
echo "Skipping mimic provisioning; falling back to plain UDP" >&2
_mimic_breadcrumb {{ shq .MimicBreadcrumb.FellBackToUDP }}
else
echo "Provisioning mimic TCP-shaping transport..."
# mimic filter helpers. _mimic_ipport formats an IP:port, bracketing IPv6 (mimic's config form is
# [2001:db8::1]:port). _mimic_resolve maps a peer host to an IP at install time — getent handles
# both a hostname and a literal IP; the caller falls back to the literal so an IP entered directly
# still works even if getent is unavailable.
_mimic_ipport() { case "$1" in *:*) printf '[%s]:%s' "$1" "$2";; *) printf '%s:%s' "$1" "$2";; esac; }
_mimic_resolve() { getent ahosts "$1" 2>/dev/null | awk 'NR==1{print $1; exit}' || true; }
{{ if .MimicEgressOverride -}}
MIMIC_EGRESS_IF={{ shq .MimicEgressInterface }}
MIMIC_EGRESS_IP="$(ip -o -4 addr show dev "$MIMIC_EGRESS_IF" 2>/dev/null | awk 'NR==1{print $4}' | cut -d/ -f1 || true)"
{{ else -}}
MIMIC_EGRESS_IF="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}' || true)"
MIMIC_EGRESS_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}' || true)"
{{ end -}}
# A loopback src (e.g. 1.1.1.1 null-routed/blackholed) or an empty result would yield a loopback-only
# filter that can NEVER match a real WireGuard egress packet — drop it so we treat the egress as
# unresolved rather than writing a guaranteed-dead filter.
case "$MIMIC_EGRESS_IP" in 127.*|::1) MIMIC_EGRESS_IP="" ;; esac
if [ -z "$MIMIC_EGRESS_IF" ] || [ -z "$MIMIC_EGRESS_IP" ]; then
{{ if .MimicFallbackUDP -}}
    echo "WARNING: could not determine a routable egress IP for mimic; falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EgressUnresolved }}
{{ else -}}
    echo "ERROR: could not determine a routable egress IP for mimic; mimic required by this link's policy (no fallback)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EgressUnresolved }}
    exit 1
{{ end -}}
else
echo "  mimic egress: $MIMIC_EGRESS_IF ($MIMIC_EGRESS_IP)"
# eBPF gate: mimic is an eBPF (TC/XDP) program — a kernel without BPF cannot run it. This is the
# kernel-too-old case (the dominant mimic-failure mode the per-link fallback policy guards).
if [ ! -d /sys/fs/bpf ] && ! grep -qw bpf /proc/filesystems 2>/dev/null; then
{{ if .MimicFallbackUDP -}}
    echo "WARNING: kernel lacks eBPF/bpffs; mimic unavailable — falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.KernelTooOld }}
{{ else -}}
    echo "ERROR: kernel lacks eBPF/bpffs; mimic required by this link's policy (no fallback)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.KernelTooOld }}
    exit 1
{{ end -}}
else
# Two filter families, all OR'ed by mimic (the config is a whitelist), xdp_mode operator-selectable
# per node (default skb for portability; native opt-in via Node.xdp_mode) — see mimic.md:
#   local=<egress_ip>:<listenport>  catches the LISTEN direction (peers that dial in to us).
#   remote=<peer_ip>:<peer_port>    catches every flow to a peer we DIAL. The peer endpoint is known
#     and route-independent, so it matches even when the kernel picks a different local source IP than
#     the egress probe found (multi-homing / secondary or floating IPs / policy routing) — the root
#     fix for "the local= filter used the wrong source IP and mimic did nothing".
mkdir -p /etc/mimic
{
    {{ range .MimicPorts -}}
    echo "filter = local=$(_mimic_ipport "$MIMIC_EGRESS_IP" {{ . }})"
    {{ end -}}
    {{ range .MimicRemotes -}}
    _mimic_rip="$(_mimic_resolve {{ shq .Host }})"
    [ -z "$_mimic_rip" ] && _mimic_rip={{ shq .Host }}
    echo "filter = remote=$(_mimic_ipport "$_mimic_rip" {{ .Port }})"
    {{ end -}}
    echo "xdp_mode = {{ .MimicXDPMode }}"
} > "/etc/mimic/\${MIMIC_EGRESS_IF}.conf"
echo "  Wrote /etc/mimic/\${MIMIC_EGRESS_IF}.conf"
# The distro mimic package ships mimic@<iface>.service (Requires=modprobe@mimic.service, so the
# kernel module auto-loads). Enable it for boot, then RESTART (not a no-op start on an
# already-running unit) so a redeploy RE-APPLIES the freshly-written config — and, for a native node,
# RE-EVALUATES the native→skb downgrade rather than leaving a stale on-disk native config the next
# reboot would start mimic from and fail (a silent de-cloak: the on-disk config would revert to
# native while the running unit stayed skb). WG is down here (Phase 0), so the restart is not
# disruptive. Runs before WireGuard comes up.
systemctl enable "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
# Clear a wedged unit before (re)starting: a prior mimic instance can orphan its /run/mimic lock
# ("failed to lock ... File exists" -> mimic exit 17), after which systemd rate-limits restarts
# ("start request repeated too quickly"). A node has exactly one mimic egress, so removing all
# /run/mimic locks while the unit is stopped is safe. modprobe explicitly too — the shipped unit's
# Requires=modprobe@mimic only loads the module once it has actually been built.
systemctl stop "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
rm -f /run/mimic/*.lock 2>/dev/null || true
systemctl reset-failed "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
modprobe mimic 2>/dev/null || true
if systemctl restart "mimic@\${MIMIC_EGRESS_IF}"; then
    echo "  Started mimic@\${MIMIC_EGRESS_IF}"
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.Active }}
{{ if .MimicNative -}}
elif sed -i 's/^xdp_mode = native$/xdp_mode = skb/' "/etc/mimic/\${MIMIC_EGRESS_IF}.conf"; rm -f /run/mimic/*.lock 2>/dev/null || true; systemctl reset-failed "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true; systemctl restart "mimic@\${MIMIC_EGRESS_IF}"; then
    # native XDP attach failed on this NIC — auto-downgrade the config to skb (generic XDP) and retry.
    echo "  mimic@\${MIMIC_EGRESS_IF} native XDP attach failed; retried + started in skb mode" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.NativeDowngraded }}
{{ end -}}
else
{{ if .MimicFallbackUDP -}}
    echo "WARNING: mimic@\${MIMIC_EGRESS_IF} failed to start; falling back to plain UDP (policy=udp)" >&2
    # De-provision the half-applied filter so no orphaned mimic shaping survives on a UDP link.
    systemctl disable --now "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
    rm -f "/etc/mimic/\${MIMIC_EGRESS_IF}.conf"
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EbpfLoad }}
{{ else -}}
    echo "ERROR: mimic@\${MIMIC_EGRESS_IF} failed to start; mimic required by this link's policy (no fallback)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EbpfLoad }}
    exit 1
{{ end -}}
fi
fi
fi
fi
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
`;

// clientInstallScriptTemplate is copied VERBATIM from script.go:1009-1436.
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
    _mimic_egress_if={{ if .MimicEgressOverride }}{{ shq .MimicEgressInterface }}{{ else }}"$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"{{ end }}
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

# rc.4: stop any stale mimic@ unit + config, unconditionally. The mimic teardown otherwise lives only
# in the --uninstall path (HasMimic-gated), so flipping a node's last tcp link to udp (HasMimic
# true->false) would never stop the old mimic@ (it would keep shaping traffic WG now sends as plain
# UDP). A still-mimic node re-provisions in Phase 3; no-op when mimic was never installed.
for _stale_mimic in $(systemctl list-units --plain --no-legend 'mimic@*.service' 2>/dev/null | awk '{print $1}'); do
    echo "  Stopping stale mimic unit: $_stale_mimic..."
    systemctl disable --now "$_stale_mimic" 2>/dev/null || true
done
rm -f /etc/mimic/*.conf 2>/dev/null || true

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
# mimic-provisioning outcome breadcrumb (plan-5): a small Go-constant-keyed JSON marker the agent
# reads to emit the mimic Node Condition. Only the OUTCOME token (a fixed shell literal from a Go
# constant) and the kernel-derived egress NIC (set in Phase 3; empty here) are interpolated — never
# captured stderr / node name / upstream text (PRINCIPLES root-script safety).
mkdir -p /var/lib/yaog-agent
_mimic_breadcrumb() {
    printf '{"outcome":"%s","egress":"%s","ts":"%s"}\\n' \\
        "$1" "\${MIMIC_EGRESS_IF:-}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \\
        > {{ shq .MimicBreadcrumb.Path }}
}
# _MIMIC_SKIP is set when mimic could not be provisioned AND policy=udp, so the Phase-3 provisioning
# block skips mimic and the link comes up as plain UDP. Empty = provision normally.
_MIMIC_SKIP=
# _mimic_provision installs mimic — the distro package, else a SHA-256-pinned GitHub .deb PAIR. It
# RETURNS non-zero on any failure instead of exiting, so the caller honors the link's mimic_fallback
# policy (udp: skip to plain UDP; none: fail closed) rather than aborting the whole apply under set -e.
# Upstream mimic ships TWO packages: mimic (userspace) and mimic-dkms (the eBPF module, which
# Provides the mimic-modules the mimic pkg Depends on), so BOTH .debs are fetched, verified against
# their pins, and dpkg'd together — installing only mimic cannot satisfy the dependency.
_mimic_provision() {
    command -v mimic >/dev/null 2>&1 && return 0
    if [ -n "$YAOG_PM" ]; then _pm_install mimic || true; fi
    command -v mimic >/dev/null 2>&1 && return 0
    # Distro package unavailable -> pinned GitHub .deb (apt/dpkg systems only).
    if [ "$YAOG_PM" != "apt-get" ] || ! command -v dpkg >/dev/null 2>&1; then
        echo "ERROR: mimic is not in this distro's repositories and the GitHub .deb fallback requires apt/dpkg" >&2
        return 1
    fi
    if [ ! -f "$SCRIPT_DIR/artifacts.json" ]; then
        echo "ERROR: mimic GitHub fallback needs artifacts.json (no mimic catalog was configured for this deploy)" >&2
        return 1
    fi
    _mimic_codename="$(. /etc/os-release 2>/dev/null; echo "\${VERSION_CODENAME:-}")"
    _mimic_arch="$(dpkg --print-architecture 2>/dev/null)"
    _mimic_key="\${_mimic_codename}-\${_mimic_arch}"
    # Read the pins with jq (auto-installed on this apt path if absent); fail if jq is unavailable
    # rather than hand-parse nested JSON in bash.
    if ! command -v jq >/dev/null 2>&1; then
        _pm_install jq || true
    fi
    command -v jq >/dev/null 2>&1 || { echo "ERROR: mimic GitHub fallback needs jq to read artifacts.json" >&2; return 1; }
    _mimic_rel="$(jq -r '.mimic.release_url // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_asset="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].asset // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_sha="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].sha256 // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_dkms_asset="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].dkms_asset // ""' "$SCRIPT_DIR/artifacts.json")"
    _mimic_dkms_sha="$(jq -r --arg k "$_mimic_key" '.mimic.debs[$k].dkms_sha256 // ""' "$SCRIPT_DIR/artifacts.json")"
    if [ -z "$_mimic_rel" ] || [ -z "$_mimic_asset" ] || [ -z "$_mimic_sha" ]; then
        echo "ERROR: no pinned mimic .deb for '$_mimic_key' in artifacts.json" >&2
        return 1
    fi
    echo "Installing mimic from a SHA-256-pinned GitHub .deb ($_mimic_key)..."
    # mimic-dkms builds the eBPF module via DKMS -> kernel headers + toolchain (best-effort; a stale
    # kernel whose exact headers left the repo cannot build until it reboots into the current kernel).
    _pm_install "linux-headers-$(uname -r)" || _pm_install linux-headers-generic || true
    _pm_install dkms || true
    _pm_install gcc || true
    _pm_install bubblewrap || true   # mimic-dkms's build sandbox (bwrap); the DKMS build is Error 127 without it
    _pm_install dwarves || true      # provides pahole for the module's BTF generation (else 'pahole: not found')
    # _mimic_get <asset> <sha256> <dest>: download via the proxy and verify the pin (0 ok / non-zero fail).
    _mimic_get() {
        curl -fL --retry 3 --proto '=https,http' "\${GH_PROXY}\${_mimic_rel}/$1" -o "$3" || return 1
        echo "$2  $3" | sha256sum -c -
    }
    _mimic_deb="$(mktemp --suffix=.deb)"
    _mimic_get "$_mimic_asset" "$_mimic_sha" "$_mimic_deb" || { rm -f "$_mimic_deb"; return 1; }
    _mimic_install="$_mimic_deb"
    if [ -n "$_mimic_dkms_asset" ] && [ -n "$_mimic_dkms_sha" ]; then
        _mimic_dkms_deb="$(mktemp --suffix=.deb)"
        _mimic_get "$_mimic_dkms_asset" "$_mimic_dkms_sha" "$_mimic_dkms_deb" || { rm -f "$_mimic_deb" "$_mimic_dkms_deb"; return 1; }
        _mimic_install="$_mimic_install $_mimic_dkms_deb"
    fi
    # Install both .debs together so mimic's Depends: mimic-modules resolves from the local dkms .deb.
    if ! DEBIAN_FRONTEND=noninteractive apt-get install -y $_mimic_install; then
        rm -f $_mimic_install
        return 1
    fi
    rm -f $_mimic_install
    command -v mimic >/dev/null 2>&1
}
# _mimic_module_ready reports 0 iff mimic's DKMS kernel module is built AND loadable for the running
# kernel. mimic's eBPF program calls a kfunc the module exports; without the module loaded 'mimic run'
# fails to load the BPF program (exit 22). Installing the .deb (userspace binary) does NOT guarantee
# the module built — a stale kernel whose linux-headers-$(uname -r) were pruned from the repo leaves
# DKMS at "added" (never built). Try to load it; else build it via DKMS for THIS kernel and re-check.
_mimic_module_ready() {
    lsmod 2>/dev/null | grep -qw mimic && return 0
    modprobe mimic 2>/dev/null && return 0
    if command -v dkms >/dev/null 2>&1; then
        _pm_install "linux-headers-$(uname -r)" >/dev/null 2>&1 || true
        _pm_install bubblewrap >/dev/null 2>&1 || true
        _pm_install dwarves >/dev/null 2>&1 || true
        dkms autoinstall -k "$(uname -r)" >/dev/null 2>&1 || true
        modprobe mimic 2>/dev/null && return 0
    fi
    return 1
}
if ! _mimic_provision; then
{{ if .MimicFallbackUDP -}}
    # policy=udp: mimic could not be provisioned — skip it, bring the link up as plain UDP.
    echo "WARNING: mimic could not be provisioned; falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.InstallFailed }}
    _MIMIC_SKIP=1
{{ else -}}
    echo "ERROR: mimic could not be provisioned and this link's mimic_fallback policy is fail-closed" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.InstallFailed }}
    exit 1
{{ end -}}
elif ! _mimic_module_ready; then
{{ if .MimicFallbackUDP -}}
    # policy=udp: the mimic binary installed but its kernel module isn't usable on this kernel — skip
    # mimic and bring the link up as plain UDP (this closes the false-success that used to defeat the
    # fallback when only the binary, not the module, was present).
    echo "WARNING: the mimic kernel module could not be built/loaded for kernel $(uname -r); falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.ModuleUnavailable }}
    _MIMIC_SKIP=1
{{ else -}}
    echo "ERROR: the mimic kernel module could not be built/loaded for kernel $(uname -r) — reboot into the current kernel so DKMS can build it, or set this link's mimic_fallback to udp" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.ModuleUnavailable }}
    exit 1
{{ end -}}
fi
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
            printf 'PrivateKey = %s\\n' "$_agent_key" >> "$_spliced"
        else
            printf '%s\\n' "$line" >> "$_spliced"
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
if [ -n "\${_MIMIC_SKIP:-}" ]; then
# The mimic binary could not be installed and policy is udp — skip provisioning; wg0 comes up as
# plain UDP below (the install_failed breadcrumb was written in the deps phase).
echo "Skipping mimic provisioning; falling back to plain UDP" >&2
_mimic_breadcrumb {{ shq .MimicBreadcrumb.FellBackToUDP }}
else
echo "Provisioning mimic TCP-shaping transport..."
# mimic filter helpers (IPv6-bracketing + install-time host resolution) — see the per-peer install
# script for the rationale.
_mimic_ipport() { case "$1" in *:*) printf '[%s]:%s' "$1" "$2";; *) printf '%s:%s' "$1" "$2";; esac; }
_mimic_resolve() { getent ahosts "$1" 2>/dev/null | awk 'NR==1{print $1; exit}' || true; }
{{ if .MimicEgressOverride -}}
MIMIC_EGRESS_IF={{ shq .MimicEgressInterface }}
MIMIC_EGRESS_IP="$(ip -o -4 addr show dev "$MIMIC_EGRESS_IF" 2>/dev/null | awk 'NR==1{print $4}' | cut -d/ -f1 || true)"
{{ else -}}
MIMIC_EGRESS_IF="$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}' || true)"
MIMIC_EGRESS_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}' || true)"
{{ end -}}
# Drop a loopback/empty egress src (a dead loopback-only filter) — treat as unresolved.
case "$MIMIC_EGRESS_IP" in 127.*|::1) MIMIC_EGRESS_IP="" ;; esac
if [ -z "$MIMIC_EGRESS_IF" ] || [ -z "$MIMIC_EGRESS_IP" ]; then
{{ if .MimicFallbackUDP -}}
    echo "WARNING: could not determine a routable egress IP for mimic; falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EgressUnresolved }}
{{ else -}}
    echo "ERROR: could not determine a routable egress IP for mimic; mimic required by this link's policy (no fallback)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EgressUnresolved }}
    exit 1
{{ end -}}
else
echo "  mimic egress: $MIMIC_EGRESS_IF ($MIMIC_EGRESS_IP)"
# eBPF gate: mimic is an eBPF (TC/XDP) program — a kernel without BPF cannot run it (kernel-too-old).
if [ ! -d /sys/fs/bpf ] && ! grep -qw bpf /proc/filesystems 2>/dev/null; then
{{ if .MimicFallbackUDP -}}
    echo "WARNING: kernel lacks eBPF/bpffs; mimic unavailable — falling back to plain UDP (policy=udp)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.KernelTooOld }}
{{ else -}}
    echo "ERROR: kernel lacks eBPF/bpffs; mimic required by this link's policy (no fallback)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.KernelTooOld }}
    exit 1
{{ end -}}
else
# local=<egress_ip>:<listenport> (listen direction) + remote=<router_ip>:<port> (the dialed router,
# route-independent — the multi-homing fix). All OR'ed by mimic's whitelist.
mkdir -p /etc/mimic
{
    {{ range .MimicPorts -}}
    echo "filter = local=$(_mimic_ipport "$MIMIC_EGRESS_IP" {{ . }})"
    {{ end -}}
    {{ range .MimicRemotes -}}
    _mimic_rip="$(_mimic_resolve {{ shq .Host }})"
    [ -z "$_mimic_rip" ] && _mimic_rip={{ shq .Host }}
    echo "filter = remote=$(_mimic_ipport "$_mimic_rip" {{ .Port }})"
    {{ end -}}
    echo "xdp_mode = {{ .MimicXDPMode }}"
} > "/etc/mimic/\${MIMIC_EGRESS_IF}.conf"
echo "  Wrote /etc/mimic/\${MIMIC_EGRESS_IF}.conf"
# Enable mimic@<iface> for boot, then RESTART (not a no-op start on an already-running unit) so a
# redeploy re-applies the freshly-written config and, for a native node, re-evaluates the native→skb
# downgrade instead of leaving a stale on-disk native config a reboot would start from and fail.
systemctl enable "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
# Clear a wedged unit before (re)starting: a prior mimic instance can orphan its /run/mimic lock
# ("failed to lock ... File exists" -> mimic exit 17), after which systemd rate-limits restarts
# ("start request repeated too quickly"). A node has exactly one mimic egress, so removing all
# /run/mimic locks while the unit is stopped is safe. modprobe explicitly too — the shipped unit's
# Requires=modprobe@mimic only loads the module once it has actually been built.
systemctl stop "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
rm -f /run/mimic/*.lock 2>/dev/null || true
systemctl reset-failed "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
modprobe mimic 2>/dev/null || true
if systemctl restart "mimic@\${MIMIC_EGRESS_IF}"; then
    echo "  Started mimic@\${MIMIC_EGRESS_IF}"
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.Active }}
{{ if .MimicNative -}}
elif sed -i 's/^xdp_mode = native$/xdp_mode = skb/' "/etc/mimic/\${MIMIC_EGRESS_IF}.conf"; rm -f /run/mimic/*.lock 2>/dev/null || true; systemctl reset-failed "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true; systemctl restart "mimic@\${MIMIC_EGRESS_IF}"; then
    # native XDP attach failed on this NIC — auto-downgrade the config to skb (generic XDP) and retry.
    echo "  mimic@\${MIMIC_EGRESS_IF} native XDP attach failed; retried + started in skb mode" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.NativeDowngraded }}
{{ end -}}
else
{{ if .MimicFallbackUDP -}}
    echo "WARNING: mimic@\${MIMIC_EGRESS_IF} failed to start; falling back to plain UDP (policy=udp)" >&2
    systemctl disable --now "mimic@\${MIMIC_EGRESS_IF}" 2>/dev/null || true
    rm -f "/etc/mimic/\${MIMIC_EGRESS_IF}.conf"
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EbpfLoad }}
{{ else -}}
    echo "ERROR: mimic@\${MIMIC_EGRESS_IF} failed to start; mimic required by this link's policy (no fallback)" >&2
    _mimic_breadcrumb {{ shq .MimicBreadcrumb.EbpfLoad }}
    exit 1
{{ end -}}
fi
fi
fi
fi
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
`;

// resolveMimicXDPMode normalizes a node's xdp_mode into the value written to the mimic config. Mirrors
// renderer.resolveMimicXDPMode (script.go:903-908): only "native" passes through; everything else
// (incl. empty and "skb") falls back to "skb" (generic XDP, the safe VPS default).
function resolveMimicXDPMode(mode: string | undefined): string {
  return mode === 'native' ? 'native' : 'skb';
}

// collectMimicPorts scans a set of peers and collects the listen ports of all mimic interfaces
// (p.mimic === true), de-duplicated and sorted ascending. Mirrors renderer.collectMimicPorts
// (script.go:915-927): only ports with listenPort > 0 are collected (0 means no bound listen port).
// The sort is numeric ascending (Go sort.Ints).
function collectMimicPorts(peers: PeerInfo[]): number[] {
  const seen = new Set<number>();
  const ports: number[] = [];
  for (const p of peers) {
    if (!p.mimic || p.listenPort <= 0 || seen.has(p.listenPort)) {
      continue;
    }
    seen.add(p.listenPort);
    ports.push(p.listenPort);
  }
  ports.sort((a, b) => a - b);
  return ports;
}

// newMimicBreadcrumbData returns the breadcrumb contract constants (path + closed MimicOutcome*
// tokens) the install template references. Mirrors renderer.newMimicBreadcrumbData (script.go); the
// values MUST match the Go model constants (model.MimicBreadcrumbPath / model.MimicOutcome*) so the
// rendered install.sh is byte-identical to the Go oracle and the agent reader cannot drift.
function newMimicBreadcrumbData(): MimicBreadcrumbData {
  return {
    Path: '/var/lib/yaog-agent/mimic-status.json',
    Active: 'active',
    KernelTooOld: 'kernel_too_old',
    EbpfLoad: 'ebpf_load_failed',
    InstallFailed: 'install_failed',
    FellBackToUDP: 'fell_back_to_udp',
    EgressUnresolved: 'egress_unresolved',
    NativeDowngraded: 'native_downgraded_skb',
    ModuleUnavailable: 'module_unavailable',
  };
}

// splitHostPort mirrors Go net.SplitHostPort for the well-formed endpoints formatEndpoint produces
// ("host:port" or "[v6]:port"). Returns null when there is no parseable host:port (where Go errors).
function splitHostPort(s: string): { host: string; port: string } | null {
  if (s.startsWith('[')) {
    const end = s.indexOf(']');
    if (end < 0) return null;
    const rest = s.slice(end + 1);
    if (!rest.startsWith(':')) return null;
    return { host: s.slice(1, end), port: rest.slice(1) };
  }
  const i = s.lastIndexOf(':');
  if (i < 0) return null;
  const host = s.slice(0, i);
  if (host.includes(':')) return null; // a bare host with extra colons — Go errors (too many colons)
  return { host, port: s.slice(i + 1) };
}

// collectMimicRemotes returns the distinct, deterministically-ordered set of mimic peer endpoints this
// node dials (PeerInfo.endpoint), each emitting a route-independent remote= filter. Mirrors
// renderer.collectMimicRemotes (script.go): inbound-only ('' endpoint), unparseable, and zero/non-numeric
// /out-of-range port entries are skipped; deduped; sorted by host then port. Exported for the direct
// parity unit test (script.test.ts) so the Go↔TS equivalence is CI-locked, not only one-off-verified.
export function collectMimicRemotes(peers: PeerInfo[]): MimicEndpoint[] {
  const seen = new Set<string>();
  const out: MimicEndpoint[] = [];
  for (const p of peers) {
    if (!p.mimic || p.endpoint === '') continue;
    const hp = splitHostPort(p.endpoint);
    if (hp === null || hp.host === '' || !/^\d+$/.test(hp.port)) continue;
    const port = parseInt(hp.port, 10);
    // 0 < port <= 65535: the valid port range. The upper bound matches Go (strconv.Atoi overflows ->
    // skip) and prevents a huge digit string rendering as a corrupt exponential (e.g. "1e+22").
    if (port <= 0 || port > 65535) continue;
    const key = hp.host + ' ' + hp.port;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({ Host: hp.host, Port: port });
  }
  out.sort((a, b) => (a.Host < b.Host ? -1 : a.Host > b.Host ? 1 : a.Port - b.Port));
  return out;
}

// resolveMimicFallbackUDP reports whether this node's mimic provisioning may fall back to plain UDP.
// Mirrors renderer.resolveMimicFallbackUDP (script.go): true only when EVERY mimic link (p.mimic)
// resolves to the "udp" policy (plan-4 PeerInfo.mimicFallback); a single non-"udp" mimic link forces
// fail-closed for the whole node (one shared mimic@<egress> unit serves all this node's mimic ports,
// so partial fallback is not representable — fail-closed must win). A node with no mimic link returns
// false (no fallback branch rendered).
function resolveMimicFallbackUDP(peers: PeerInfo[]): boolean {
  let any = false;
  for (const p of peers) {
    if (!p.mimic) {
      continue;
    }
    any = true;
    if (p.mimicFallback !== 'udp') {
      return false;
    }
  }
  return any;
}

// resolveTransitCIDRs normalizes the caller-supplied transit address pools into a de-duplicated,
// non-empty, stable-order list. Mirrors renderer.resolveTransitCIDRs (script.go:958-972): empty-string
// entries are dropped; an empty result falls back to [DefaultTransitCIDR].
function resolveTransitCIDRs(transitCIDRs: string[]): string[] {
  const seen = new Set<string>();
  const resolved: string[] = [];
  for (const cidr of transitCIDRs) {
    if (cidr === '' || seen.has(cidr)) {
      continue;
    }
    seen.add(cidr);
    resolved.push(cidr);
  }
  if (resolved.length === 0) {
    return [DefaultTransitCIDR];
  }
  return resolved;
}

// nodeTransitCIDRs resolves the transit address pools a node's SNAT fix should cover. Mirrors
// renderer.NodeTransitCIDRs (script.go:937-951): the transit pool of the node's domain
// (domain.transit_cidr, falling back to DefaultTransitCIDR when empty/absent). A node belongs to one
// domain, so this is a single-element list.
function nodeTransitCIDRs(topo: Topology, node: Node): string[] {
  for (const d of topo.domains) {
    if (d.id !== node.domain_id) {
      continue;
    }
    if (d.transit_cidr !== undefined && d.transit_cidr !== '') {
      return [d.transit_cidr];
    }
    return [DefaultTransitCIDR];
  }
  return [DefaultTransitCIDR];
}

// resolvePlatform mirrors the `if config.Platform == "" { config.Platform = "debian" }` default in Go
// (script.go:891-893 / :1492-1494). The TS Node.platform type is a string union, but a raw wire/JSON
// topology may carry an empty string, so the empty check is done at runtime (String() coercion) to stay
// byte-faithful with Go's empty-string default.
function resolvePlatform(platform: string | undefined): string {
  const p = platform ?? '';
  return p === '' ? 'debian' : p;
}

// buildInstallScriptConfig assembles the per-peer InstallScriptConfig. Mirrors
// renderer.buildInstallScriptConfig (script.go:856-896): the WireGuard interface list comes from the
// node's peers (one conf per peer), the SNAT transit pools from the node's domain, and the mimic port
// set from the peers. Platform defaults to "debian" when empty (script.go:891-893). SigningPubkeyPEM /
// SplicePlaceholder* / Fetch are filled by the caller (signed renderer); the local path leaves
// SigningPubkeyPEM empty and Fetch's GithubProxy empty, and sets the splice fields per custody.
function buildInstallScriptConfig(
  node: Node,
  peers: PeerInfo[],
  hasBabel: boolean,
  transitCIDRs: string[],
): InstallScriptConfig {
  const wgIfaces: WgIfaceInfo[] = peers.map((p) => ({
    Name: p.interfaceName,
    ConfName: p.interfaceName + '.conf',
  }));

  const resolvedTransitCIDRs = resolveTransitCIDRs(transitCIDRs);
  const mimicPorts = collectMimicPorts(peers);

  return {
    NodeName: node.name,
    NodeNameQuoted: bashSingleQuote(node.name),
    NodeRole: node.role,
    Platform: resolvePlatform(node.platform),
    OverlayIP: node.overlay_ip ?? '',
    TransitCIDRs: resolvedTransitCIDRs,
    MTU: node.mtu ?? 0,
    HasBabel: hasBabel,
    HasForward: node.capabilities.can_forward,
    HasMimic: mimicPorts.length > 0,
    MimicPorts: mimicPorts,
    MimicRemotes: collectMimicRemotes(peers),
    MimicXDPMode: resolveMimicXDPMode(node.xdp_mode),
    MimicNative: resolveMimicXDPMode(node.xdp_mode) === 'native',
    MimicEgressInterface: node.mimic_egress_interface ?? '',
    MimicEgressOverride: (node.mimic_egress_interface ?? '') !== '',
    MimicFallbackUDP: resolveMimicFallbackUDP(peers),
    MimicBreadcrumb: newMimicBreadcrumbData(),
    WgInterfaces: wgIfaces,
    BabelConfName: 'babeld.conf',
    SysctlConfName: '99-overlay.conf',
    SigningPubkeyPEM: '',
    SplicePlaceholder: false,
    SplicePlaceholderToken: '',
    Fetch: { GithubProxy: '' },
  };
}

// buildClientInstallScriptConfig assembles the ClientInstallScriptConfig. Mirrors
// renderer.buildClientInstallScriptConfig (script.go:1470-1496): mimic is wired in only when the
// client's wg0 link has mimic === true and a positive listen port (then MimicPorts = [listenPort]).
// Platform defaults to "debian" when empty.
function buildClientInstallScriptConfig(
  node: Node,
  clientInfo: ClientPeerInfo | undefined,
): ClientInstallScriptConfig {
  let hasMimic = false;
  let mimicPorts: number[] = [];
  let mimicRemotes: MimicEndpoint[] = [];
  let mimicFallbackUDP = false;
  // Mirror Go buildClientInstallScriptConfig (script.go): MimicFallbackUDP is set INSIDE the
  // `mimic && listenPort > 0` block, so it stays false when there is no bound listen port — keeping
  // the dual-implementation bit-for-bit equivalent (the conformance byte-compare gates on HasMimic
  // and so cannot catch a divergence here).
  if (clientInfo !== undefined && clientInfo.mimic && clientInfo.listenPort > 0) {
    hasMimic = true;
    mimicPorts = [clientInfo.listenPort];
    mimicFallbackUDP = clientInfo.mimicFallback === 'udp';
    // The client dials the router at routerEndpoint (host:port); emit a route-independent remote=
    // filter for it. Parsed best-effort, mirroring the Go builder.
    const hp = splitHostPort(clientInfo.routerEndpoint);
    if (hp !== null && hp.host !== '' && /^\d+$/.test(hp.port)) {
      const port = parseInt(hp.port, 10);
      if (port > 0) {
        mimicRemotes = [{ Host: hp.host, Port: port }];
      }
    }
  }

  return {
    NodeName: node.name,
    NodeNameQuoted: bashSingleQuote(node.name),
    NodeRole: node.role,
    Platform: resolvePlatform(node.platform),
    OverlayIP: node.overlay_ip ?? '',
    MTU: node.mtu ?? 0,
    SysctlConfName: '99-overlay.conf',
    HasMimic: hasMimic,
    MimicPorts: mimicPorts,
    MimicRemotes: mimicRemotes,
    MimicXDPMode: resolveMimicXDPMode(node.xdp_mode),
    MimicNative: resolveMimicXDPMode(node.xdp_mode) === 'native',
    MimicEgressInterface: node.mimic_egress_interface ?? '',
    MimicEgressOverride: (node.mimic_egress_interface ?? '') !== '',
    MimicFallbackUDP: mimicFallbackUDP,
    MimicBreadcrumb: newMimicBreadcrumbData(),
    SigningPubkeyPEM: '',
    SplicePlaceholder: false,
    SplicePlaceholderToken: '',
    Fetch: { GithubProxy: '' },
  };
}

// renderInstallScript renders the per-peer install script for a node. Mirrors
// renderer.RenderInstallScript (script.go:816-819) on the AirGap (no-splice, no-signing) path. The
// caller resolves transitCIDRs via nodeTransitCIDRs.
export function renderInstallScript(
  node: Node,
  peers: PeerInfo[],
  hasBabel: boolean,
  transitCIDRs: string[],
): string {
  const config = buildInstallScriptConfig(node, peers, hasBabel, transitCIDRs);
  return renderTemplate('install.sh', installScriptTemplate, config);
}

// renderClientInstallScript renders the client (wg0) install script. Mirrors
// renderer.RenderClientInstallScript (script.go:1446-1449) on the AirGap path.
export function renderClientInstallScript(
  node: Node,
  clientInfo: ClientPeerInfo | undefined,
): string {
  const config = buildClientInstallScriptConfig(node, clientInfo);
  return renderTemplate('client-install.sh', clientInstallScriptTemplate, config);
}

// renderAllInstallScripts renders the install script for every node, keyed by node ID. Mirrors the
// per-node install-script loop in render.AllWith (render.go:356-392): a client node renders the client
// template; every other node renders the per-peer template. The AgentHeld custody splice is detected
// per-node exactly as Go does — keys[nodeID].privateKey === PrivateKeyPlaceholder — so an AgentHeld
// bundle gets the splice block and an AirGap bundle stays byte-identical to the pre-splice output.
// Signing is a NO-OP in local mode (no signer): SigningPubkeyPEM stays empty and the signature block
// never renders. hasBabel mirrors render.AllWith's `_, hasBabel := result.BabelConfigs[node.ID]`.
//
// clientConfigs is keyed by node ID (the client's ClientPeerInfo); a client with no entry passes
// undefined, matching render.AllWith passing result.ClientConfigs[node.ID] (a nil map value).
export function renderAllInstallScripts(
  topo: Topology,
  peerMap: Record<string, PeerInfo[]>,
  babelConfigs: Record<string, string>,
  clientConfigs: Record<string, ClientPeerInfo>,
  keys: Map<string, KeyPair>,
): Record<string, string> {
  const scripts: Record<string, string> = {};

  for (const node of topo.nodes) {
    // Per-node custody splice: AgentHeld when the node's emitted private key is the placeholder.
    const keyPair = keys.get(node.id);
    const custody =
      keyPair !== undefined && keyPair.privateKey === PrivateKeyPlaceholder;

    if (node.role === 'client') {
      const config = buildClientInstallScriptConfig(node, clientConfigs[node.id]);
      config.SplicePlaceholder = custody;
      config.SplicePlaceholderToken = PrivateKeyPlaceholder;
      scripts[node.id] = renderTemplate(
        'client-install.sh',
        clientInstallScriptTemplate,
        config,
      );
      continue;
    }

    const peers = peerMap[node.id] ?? [];
    const hasBabel = babelConfigs[node.id] !== undefined;
    const transitCIDRs = nodeTransitCIDRs(topo, node);
    const config = buildInstallScriptConfig(node, peers, hasBabel, transitCIDRs);
    config.SplicePlaceholder = custody;
    config.SplicePlaceholderToken = PrivateKeyPlaceholder;
    scripts[node.id] = renderTemplate(
      'install.sh',
      installScriptTemplate,
      config,
    );
  }

  return scripts;
}
