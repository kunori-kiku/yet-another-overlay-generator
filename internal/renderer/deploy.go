package renderer

import (
	"fmt"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// DeployNodeInfo holds per-node SSH and artifact info for deploy script generation
type DeployNodeInfo struct {
	NodeID       string // every exporter keys bundle directories by this stable identity
	NodeName     string // display-only human name
	SSHTarget    string // ssh_alias or user@host
	SSHPort      int
	SSHKeyPath   string
	HasSSH       bool
	WgInterfaces []string // WireGuard interface names (e.g. "wg-alpha", "wg-beta")
	HasBabel     bool     // whether this node runs Babel
	IsClient     bool     // client nodes use wg0 instead of per-peer interfaces
	// HasMimic marks a node that has at least one transport=="tcp" link and therefore had mimic
	// (eBPF UDP->fake-TCP shaping) provisioned by install.sh. The uninstall path must tear it down —
	// otherwise an orphaned root eBPF program + a boot-persistent mimic@<egress> unit survive after
	// the operator believes the overlay is gone. Gated on this (NOT on !IsClient): a client whose sole
	// wg0 link is tcp also has mimic. See docs/spec/artifacts/mimic.md and install.sh.tmpl's teardown.
	HasMimic bool
	// MimicEgressInterface is the operator's egress-NIC override for mimic ("" = auto-detect from the
	// default route). MimicEgressOverride is (MimicEgressInterface != "") — a precomputed gate so the
	// teardown re-detects the egress the same way the installer did unless the operator pinned one.
	// The value is operator-supplied free text spliced into a root shell, so the emitters quote it with
	// bashSingleQuote (the mimic teardown runs on the REMOTE node inside a bash heredoc / here-string).
	MimicEgressInterface string
	MimicEgressOverride  bool
}

// DeployScriptConfig holds all nodes for one combined deploy script
type DeployScriptConfig struct {
	ProjectName string
	Nodes       []DeployNodeInfo
}

// RenderDeployScripts generates a bash deploy script and a PowerShell deploy script.
// Both scripts iterate all nodes that have SSH details configured, locate the node's
// complete bundle directory in the extracted archive, SCP that directory to a fresh
// remote staging path, and execute its integrity-gated install.sh via sudo.
// In uninstall mode, the scripts SSH in and run teardown commands directly
// without uploading any installer.
// Returns (bashScript, ps1Script, error).
func RenderDeployScripts(topo *model.Topology, peerMap map[string][]compiler.PeerInfo, babelConfigs map[string]string) (string, string, error) {
	return RenderDeployScriptsForCustody(topo, peerMap, babelConfigs, false)
}

// RenderDeployScriptsForCustody renders deploy helpers with an explicit key-custody boundary.
// AgentHeld bundles must pass through yaog-agent so off-host membership and durable rollback state
// are verified; a generic SSH helper cannot safely carry the operator credential/RP binding. Those
// helpers therefore fail closed with actionable guidance instead of directly invoking install.sh.
func RenderDeployScriptsForCustody(topo *model.Topology, peerMap map[string][]compiler.PeerInfo, babelConfigs map[string]string, agentHeld bool) (string, string, error) {
	if agentHeld {
		bash, ps1 := renderAgentHeldDeployGuidance(topo.Project.Name)
		return bash, ps1, nil
	}
	config := DeployScriptConfig{
		ProjectName: topo.Project.Name,
	}

	// Domain index: the HasBabel fallback decision must be made from the routing_mode
	// of the node's own domain, consistent with shouldRunBabel (see the D61 fix below).
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	for i := range topo.Nodes {
		node := topo.Nodes[i]
		if err := validateDeployBundleDirSegment(node.ID); err != nil {
			return "", "", fmt.Errorf("unsafe deploy bundle node id %q: %w", node.ID, err)
		}
		info := DeployNodeInfo{
			NodeID:   node.ID,
			NodeName: node.Name,
			SSHPort:  22,
			IsClient: node.Role == "client",
		}

		if node.SSHAlias != "" {
			info.SSHTarget = node.SSHAlias
			info.SSHPort = 0 // alias → let ssh_config decide
			info.HasSSH = true
		} else if node.SSHHost != "" {
			user := node.SSHUser
			if user == "" {
				user = "root"
			}
			info.SSHTarget = fmt.Sprintf("%s@%s", user, node.SSHHost)
			info.HasSSH = true
		}

		if node.SSHPort > 0 {
			info.SSHPort = node.SSHPort
		}
		info.SSHKeyPath = node.SSHKeyPath

		// Collect WireGuard interface names from peer map. HasMimic is derived the same way the
		// install-script renderer derives it (collectMimicPorts / len(mimicPorts) > 0): a node has mimic
		// iff any of its peers rides transport=="tcp" (PeerInfo.Mimic). A client node carries no PeerInfo
		// in peerMap (its wg0 lives in ClientConfigs, not passed here), so for clients HasMimic is instead
		// derived from the topology edges below.
		if peers, ok := peerMap[node.ID]; ok {
			for _, p := range peers {
				info.WgInterfaces = append(info.WgInterfaces, p.InterfaceName)
				if p.Mimic {
					info.HasMimic = true
				}
			}
		}
		if info.IsClient {
			info.WgInterfaces = []string{"wg0"}
			// A client's wg0 mimic can't be seen from peerMap (empty for clients), so derive HasMimic from
			// the topology: a client whose enabled wg0 link is transport=="tcp" has mimic provisioned, and
			// deploy-all --uninstall must tear it down. The teardown is idempotent (disable --now ... ||
			// true), so deriving from the raw edge (pre-mimic_fallback) is safe — an over-derive runs a
			// no-op teardown; a MISS orphans the boot-persistent mimic@<egress> unit (the plan-3 review gap).
			for i := range topo.Edges {
				if e := &topo.Edges[i]; e.IsEnabled && e.Transport == "tcp" && (e.FromNodeID == node.ID || e.ToNodeID == node.ID) {
					info.HasMimic = true
					break
				}
			}
		}

		// mimic egress-NIC override (operator-supplied): empty means auto-detect from the default route
		// in the teardown, exactly as install.sh.tmpl re-detects it. The raw value is carried through and
		// quoted at the emit site (bashSingleQuote) — never spliced raw.
		info.MimicEgressInterface = node.MimicEgressInterface
		info.MimicEgressOverride = node.MimicEgressInterface != ""

		// Check if this node runs Babel: use compiled configs if available,
		// otherwise fall back to a domain-aware decision that mirrors
		// shouldRunBabel (D61). The previous role-only fallback marked every
		// non-client node as Babel-bearing, which is wrong when the node's
		// domain uses a non-babel routing_mode — uninstall would then try to
		// tear down a Babel daemon that was never deployed.
		if babelConfigs != nil {
			if _, ok := babelConfigs[node.ID]; ok {
				info.HasBabel = true
			}
		} else {
			info.HasBabel = shouldRunBabel(&topo.Nodes[i], domainMap[node.DomainID])
		}

		config.Nodes = append(config.Nodes, info)
	}

	bash, err := renderBashDeploy(config)
	if err != nil {
		return "", "", err
	}

	ps1, err := renderPS1Deploy(config)
	if err != nil {
		return "", "", err
	}

	return bash, ps1, nil
}

func renderAgentHeldDeployGuidance(projectName string) (string, string) {
	project := deployCommentText(projectName)
	bash := `#!/usr/bin/env bash
# Deployment helper disabled for AgentHeld project: ` + project + `
echo "ERROR: deploy-all is disabled for AgentHeld/controller bundles." >&2
echo "Managed nodes must deploy through controller stage/promote and their enrolled agents." >&2
echo "Manual nodes must use: sudo yaog-agent kit apply --bundle <ZIP-or-dir> --node-id <id> --operator-cred <trusted.pem> --operator-cred-alg <alg> [--operator-rpid <rp-id> --operator-origin <origin>]" >&2
echo "Use the same verified kit apply path with --uninstall for removal." >&2
exit 2
`
	ps1 := `# Deployment helper disabled for AgentHeld project: ` + project + `
Write-Error "deploy-all is disabled for AgentHeld/controller bundles. Managed nodes must use controller stage/promote and their enrolled agents. Manual nodes must use yaog-agent kit apply with the separately provisioned operator credential (and RP ID/origin for WebAuthn); use that same verified path with --uninstall for removal."
exit 2
`
	return bash, ps1
}

// validateDeployBundleDirSegment protects both local archive lookups and remote staging-name
// construction. Topology validation already constrains IDs/names, but this renderer is also called
// directly in tests and is a security boundary in its own right. In particular, "." and ".." are
// syntactically simple yet escape the per-node directory contract.
func validateDeployBundleDirSegment(value string) error {
	if !naming.ValidPortableNodeID(value) {
		return fmt.Errorf("must be a portable node-ID directory segment")
	}
	return nil
}

// deployCommentText keeps display-only topology text on one comment line in both generated shell
// dialects. Project names are intentionally human-friendly and are not otherwise shell tokens; a
// newline must not be able to terminate the comment and inject an operator-side command.
func deployCommentText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\x00' {
			return ' '
		}
		return r
	}, value)
}

// buildSSHOpts returns (sshOpts, scpOpts) strings.
// sshOpts has a leading space if non-empty (for "ssh%s target" formatting).
// scpOpts has NO leading space (for "scp %s ..." formatting).
func buildSSHOpts(node DeployNodeInfo, quoteStyle string) (string, string) {
	var sshParts, scpParts []string

	if node.SSHPort > 0 {
		sshParts = append(sshParts, fmt.Sprintf("-p %d", node.SSHPort))
		scpParts = append(scpParts, fmt.Sprintf("-P %d", node.SSHPort))
	}
	if node.SSHKeyPath != "" {
		// SSHKeyPath is operator-supplied free text spliced into a root/operator
		// shell command (`ssh -i <path>` / `scp -i <path>`), so it MUST go through
		// the same escaping idiom as SSHTarget / NodeName — otherwise a path like
		// `/k$(touch x).pem` (bash) or `k".pem` (PowerShell) is a command-injection
		// path. Go's %q is NOT bash-safe: it emits a double-quoted token, and bash
		// still expands `$`/`$(...)`/backticks inside double quotes — so bash uses
		// bashSingleQuote (single quotes are fully inert), and PowerShell uses
		// powerShellArgQuote, exactly as the target/name interpolations do.
		switch quoteStyle {
		case "bash":
			keyArg := "-i " + bashSingleQuote(node.SSHKeyPath)
			sshParts = append(sshParts, keyArg)
			scpParts = append(scpParts, keyArg)
		case "powershell":
			keyArg := "-i " + powerShellArgQuote(node.SSHKeyPath)
			sshParts = append(sshParts, keyArg)
			scpParts = append(scpParts, keyArg)
		}
	}

	sshOpts := ""
	if len(sshParts) > 0 {
		sshOpts = " " + strings.Join(sshParts, " ")
	}
	scpOpts := strings.Join(scpParts, " ")
	return sshOpts, scpOpts
}

// mimicUninstallLines returns the per-node mimic teardown block for the uninstall path, mirroring
// install.sh.tmpl's "Tear down mimic" block VERBATIM (docs/spec/artifacts/mimic.md): stop+disable the
// boot-persistent mimic@<egress> unit and remove its config. Without it, deploy-all --uninstall leaves
// an orphaned root eBPF program + a mimic@<egress> service after the operator believes the overlay is
// gone. Emitted only when node.HasMimic (a client+tcp wg0 node has mimic too, hence the gate is
// HasMimic, not !IsClient); returns "" otherwise so a non-mimic node's uninstall is byte-unchanged.
//
// The egress NIC is re-detected the same way the installer did (default-route interface) unless the
// operator pinned an override. The block runs on the REMOTE node (inside the bash uninstall heredoc /
// here-string that ssh pipes to `sudo bash -s`), so the operator-supplied override is quoted with
// bashSingleQuote — the same remote-bash escaping the node-name echo lines use — and is NEVER spliced
// raw. Only the two commands install.sh uses (disable --now + rm the conf); no bpftool/XDP-detach or
// /run/mimic removal (that would be a third divergent teardown, the drift this mirrors away).
func mimicUninstallLines(node DeployNodeInfo) string {
	if !node.HasMimic {
		return ""
	}
	egress := `"$(ip route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"`
	if node.MimicEgressOverride {
		egress = bashSingleQuote(node.MimicEgressInterface)
	}
	var b strings.Builder
	b.WriteString("_mimic_egress_if=" + egress + "\n")
	b.WriteString("if [ -n \"$_mimic_egress_if\" ]; then\n")
	b.WriteString("    echo \"  Stopping mimic@$_mimic_egress_if...\"\n")
	b.WriteString("    systemctl disable --now \"mimic@$_mimic_egress_if\" 2>/dev/null || true\n")
	b.WriteString("    rm -f \"/etc/mimic/$_mimic_egress_if.conf\"\n")
	b.WriteString("fi\n")
	return b.String()
}

func renderBashDeploy(config DeployScriptConfig) (string, error) {
	var b strings.Builder

	b.WriteString(`#!/usr/bin/env bash
# Auto-deploy script for project: ` + deployCommentText(config.ProjectName) + `
# Generated by Overlay Network Config Orchestrator
#
# Usage:
#   bash deploy-all.sh [--clean] <path-to-artifacts.zip>    Deploy overlay
#   bash deploy-all.sh --uninstall <path-to-artifacts.zip>   Remove overlay from all nodes
#
# Options:
#   --clean      Remove ALL existing WireGuard interfaces (wg*) before deploying.
#                Use this when switching from a single-interface (wg0) layout to
#                per-peer interfaces, or vice versa.
#   --uninstall  Completely remove the overlay from all nodes via SSH.
#                Stops all WireGuard interfaces, removes configs, stops Babel,
#                removes dummy0, and reloads systemd. No installer upload needed.
#
# This script SSHs into each configured node. Nodes without SSH details are skipped.
# SSH failures are caught per-node and do not abort the entire run.

set -uo pipefail

CLEAN=0
UNINSTALL=0
ARTIFACTS_ZIP=""

for arg in "$@"; do
    case "$arg" in
        --clean) CLEAN=1 ;;
        --uninstall|-u) UNINSTALL=1 ;;
        *) ARTIFACTS_ZIP="$arg" ;;
    esac
done

# Destructive layout cleanup is delegated to the candidate install.sh as --clean. That script
# verifies its signature/checksums first; deploy-all must never mutate the remote host beforehand.
INSTALL_ARGS=""
if [ "$CLEAN" -eq 1 ]; then
    INSTALL_ARGS=" --clean"
fi

if [ "$UNINSTALL" -eq 0 ] && [ -z "$ARTIFACTS_ZIP" ]; then
    echo "Usage: $0 [--clean] <path-to-artifacts.zip>"
    echo "       $0 --uninstall"
    echo ""
    echo "Options:"
    echo "  --clean      Remove all existing WireGuard interfaces before deploying"
    echo "  --uninstall  Completely remove the overlay from all nodes"
    echo ""
    echo "Download the artifacts ZIP from the web UI (Export Artifacts button),"
    echo "then run this script pointing at the downloaded file."
    exit 1
fi

WORKDIR=""
if [ -n "$ARTIFACTS_ZIP" ]; then
    if [ ! -f "$ARTIFACTS_ZIP" ]; then
        echo "ERROR: File not found: $ARTIFACTS_ZIP" >&2
        exit 1
    fi

    # Bound both the transport and the central-directory expansion before extraction. These
    # limits match the manual kit's intentionally small configuration-bundle envelope.
    ARCHIVE_MAX_BYTES=33554432
    ARCHIVE_MAX_ENTRIES=512
    ARCHIVE_MAX_FILE_BYTES=4194304
    ARCHIVE_MAX_EXPANDED_BYTES=16777216
    ARCHIVE_BYTES="$(wc -c < "$ARTIFACTS_ZIP" | tr -d '[:space:]')"
    case "$ARCHIVE_BYTES" in
        ''|*[!0-9]*) echo "ERROR: Could not determine artifacts ZIP size" >&2; exit 1 ;;
    esac
    if [ "$ARCHIVE_BYTES" -gt "$ARCHIVE_MAX_BYTES" ]; then
        echo "ERROR: Artifacts ZIP exceeds the 32 MiB archive limit" >&2
        exit 1
    fi

    # Extract to a temp directory
    WORKDIR="$(mktemp -d -t overlay-deploy-XXXXXX)"
    cleanup() { rm -rf "$WORKDIR"; }
    trap cleanup EXIT

    # Reject path aliases, traversal, duplicate names, and Unix special/symlink entries before
    # extraction. SCP -r follows symlinks, so accepting one could copy arbitrary operator-host files.
    ZIP_LIST="$WORKDIR/.zip-entries"
    if ! unzip -Z1 "$ARTIFACTS_ZIP" > "$ZIP_LIST"; then
        echo "ERROR: Failed to inspect artifacts ZIP: $ARTIFACTS_ZIP" >&2
        exit 1
    fi
    ZIP_ENTRY_COUNT="$(wc -l < "$ZIP_LIST" | tr -d '[:space:]')"
    case "$ZIP_ENTRY_COUNT" in
        ''|*[!0-9]*) echo "ERROR: Could not determine artifacts ZIP entry count" >&2; exit 1 ;;
    esac
    if [ "$ZIP_ENTRY_COUNT" -gt "$ARCHIVE_MAX_ENTRIES" ]; then
        echo "ERROR: Artifacts ZIP exceeds the 512-entry limit" >&2
        exit 1
    fi
    # Info-ZIP's long listing reports each accepted regular/directory entry with its uncompressed
    # byte length in column 4. Require the stats count to match -Z1 so an unknown entry type cannot
    # evade the size accounting, then bound every file and the aggregate expanded payload.
    if ! unzip -Z -l "$ARTIFACTS_ZIP" | LC_ALL=C awk \
        -v expected="$ZIP_ENTRY_COUNT" \
        -v max_file="$ARCHIVE_MAX_FILE_BYTES" \
        -v max_total="$ARCHIVE_MAX_EXPANDED_BYTES" '
            $1 ~ /^[-d]/ {
                if ($4 !~ /^[0-9]+$/) bad=1
                count++
                size=$4+0
                if (size > max_file) bad=1
                total += size
                if (total > max_total) bad=1
            }
            END { exit (bad || count != expected) ? 1 : 0 }
        '; then
        echo "ERROR: Artifacts ZIP has an unknown entry type or exceeds the 4 MiB per-file / 16 MiB expanded limits" >&2
        exit 1
    fi
    if LC_ALL=C awk '{ key=tolower($0); if (seen[key]++) duplicate=1 } END { exit duplicate ? 0 : 1 }' "$ZIP_LIST"; then
        echo "ERROR: Artifacts ZIP contains duplicate or case-colliding entry names" >&2
        exit 1
    fi
    while IFS= read -r ZIP_ENTRY || [ -n "$ZIP_ENTRY" ]; do
        ZIP_ENTRY_CHECK="${ZIP_ENTRY%/}"
        case "$ZIP_ENTRY_CHECK" in
            ""|/*|*\\*|*:* )
                echo "ERROR: Unsafe artifacts ZIP entry: $ZIP_ENTRY" >&2
                exit 1
                ;;
        esac
        case "/$ZIP_ENTRY_CHECK/" in
            *"/../"*|*"/./"*|*"//"*)
                echo "ERROR: Non-canonical artifacts ZIP entry: $ZIP_ENTRY" >&2
                exit 1
                ;;
        esac
        if printf '%s' "$ZIP_ENTRY_CHECK" | LC_ALL=C grep -q '[[:cntrl:]]'; then
            echo "ERROR: Artifacts ZIP entry contains a control character" >&2
            exit 1
        fi
    done < "$ZIP_LIST"
    if unzip -Z -l "$ARTIFACTS_ZIP" | grep -Eq '^[lbcps]'; then
        echo "ERROR: Artifacts ZIP contains a symlink or special-file entry" >&2
        exit 1
    fi
    rm -f "$ZIP_LIST"

    echo "Extracting artifacts..."
    if ! unzip -q "$ARTIFACTS_ZIP" -d "$WORKDIR"; then
        echo "ERROR: Failed to extract artifacts ZIP: $ARTIFACTS_ZIP" >&2
        exit 1
    fi
    if find "$WORKDIR" \( -type l -o \( ! -type f ! -type d \) \) -print -quit | grep -q .; then
        echo "ERROR: Extracted artifacts contain a symlink or special file" >&2
        exit 1
    fi
fi

if [ "$UNINSTALL" -eq 1 ]; then
    echo ""
    echo "*** UNINSTALL MODE — overlay will be removed from all nodes ***"
    echo ""
fi

FAILED=0
SKIPPED=0
SUCCESS=0

`)

	for _, node := range config.Nodes {
		b.WriteString(fmt.Sprintf("# --- Node: %s ---\n", node.NodeName))

		// Shell-escaped forms used at every interpolation site below. SSHTarget
		// (ssh_alias or user@ssh_host) and NodeName are user-supplied and are
		// spliced into a script that runs on the OPERATOR's machine; without
		// quoting an ssh_host of "x; rm -rf $HOME #" injects commands locally
		// (D7) and a single quote in a node name breaks the echo / heredoc
		// (D16). targetQuoted is a single-quoted bash token suitable wherever
		// the target was a bare argument (ssh / scp), and nameQuoted is the same
		// for echo arguments. This mirrors the existing %q treatment of
		// SSHKeyPath in buildSSHOpts.
		targetQuoted := bashSingleQuote(node.SSHTarget)
		nameQuoted := bashSingleQuote(node.NodeName)

		// Every exporter keys the complete bundle directory by node ID. A single
		// namespace prevents node A's ID from selecting node B's human name directory.
		bundleIDQuoted := bashSingleQuote(node.NodeID)
		remoteTemplate := bashSingleQuote("/tmp/yaog-" + node.NodeID + "-XXXXXXXX")
		remotePrefix := bashSingleQuote("/tmp/yaog-" + node.NodeID + "-")
		if !node.HasSSH {
			b.WriteString(fmt.Sprintf(`echo ""
echo "=== "%s": SKIPPED (no SSH details configured) ==="
SKIPPED=$((SKIPPED + 1))
`, nameQuoted))
			b.WriteString("\n")
			continue
		}

		sshOpts, scpOpts := buildSSHOpts(node, "bash")

		// scpCmd: "scp -P 22 -i key" or just "scp" when no opts
		scpCmd := "scp"
		if scpOpts != "" {
			scpCmd = "scp " + scpOpts
		}

		// Build the inline uninstall script for this node. These echo lines run
		// on the REMOTE node inside a quoted heredoc, but NodeName is still
		// interpolated into single-quoted echo strings here — a single quote in
		// the name would terminate the echo string early (D16). Splice the
		// bashSingleQuote token rather than the raw name so the name is always a
		// single inert shell token.
		var uninstallCmds strings.Builder
		uninstallCmds.WriteString("set -uo pipefail\n")
		// Tear down mimic FIRST (mirrors install.sh.tmpl), before WireGuard: stop the boot-persistent
		// mimic@<egress> unit + remove its config so no orphaned root eBPF shaping survives an uninstall.
		uninstallCmds.WriteString(mimicUninstallLines(node))
		uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 1/4] Stopping WireGuard interfaces on '%s'...'\n", nameQuoted))

		// Stop and disable WireGuard interfaces
		for _, iface := range node.WgInterfaces {
			uninstallCmds.WriteString(fmt.Sprintf("wg-quick down %s 2>/dev/null || true\n", iface))
			uninstallCmds.WriteString(fmt.Sprintf("systemctl disable wg-quick@%s 2>/dev/null || true\n", iface))
			uninstallCmds.WriteString(fmt.Sprintf("rm -f /etc/wireguard/%s.conf\n", iface))
		}

		// Stop any remaining WireGuard interfaces
		uninstallCmds.WriteString("for _i in $(wg show interfaces 2>/dev/null); do wg-quick down \"$_i\" 2>/dev/null || true; systemctl disable \"wg-quick@$_i\" 2>/dev/null || true; done\n")
		uninstallCmds.WriteString("rm -f /etc/wireguard/*.conf\n")

		if node.HasBabel {
			uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 2/4] Stopping Babel routing daemon on '%s'...'\n", nameQuoted))
			uninstallCmds.WriteString("systemctl stop babeld 2>/dev/null || true\n")
			uninstallCmds.WriteString("systemctl disable babeld 2>/dev/null || true\n")
			uninstallCmds.WriteString("rm -f /etc/babel/babeld.conf /etc/babeld.conf\n")
			uninstallCmds.WriteString("rm -rf /etc/systemd/system/babeld.service.d\n")
		}

		// Remove sysctl overlay config
		uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 3/4] Removing overlay network configuration on '%s'...'\n", nameQuoted))
		uninstallCmds.WriteString("rm -f /etc/sysctl.d/99-overlay.conf\n")
		uninstallCmds.WriteString("sysctl --system > /dev/null 2>&1 || true\n")

		if !node.IsClient {
			// Remove overlay SNAT rule and service.
			//
			// D52: mirror install.sh.tmpl's robust teardown — delete each matching POSTROUTING SNAT rule
			// WHOLE (order-independent, ignoring --to-source), so it also clears a stale rule left by an
			// overlay-IP change. Match by wg interface + SNAT target only (no -s <CIDR>): CIDR-agnostic, so
			// a node on a custom transit_cidr is cleared too (the prior hard-coded -s 10.10.0.0/24 missed it).
			uninstallCmds.WriteString("nft delete table inet overlay-snat 2>/dev/null || true\n")
			uninstallCmds.WriteString("iptables-save -t nat 2>/dev/null | grep -E '^-A POSTROUTING ' | grep -F -- '-j SNAT' | grep -F -- '-o wg-+' | while IFS= read -r _snat_rule; do _snat_del=\"${_snat_rule/#-A/-D}\"; iptables -t nat $_snat_del 2>/dev/null || true; done || true\n")
			uninstallCmds.WriteString("systemctl disable overlay-snat.service 2>/dev/null || true\n")
			uninstallCmds.WriteString("rm -f /etc/systemd/system/overlay-snat.service\n")
			// Remove dummy0 overlay interface
			uninstallCmds.WriteString("ip link del dummy0 2>/dev/null || true\n")
			uninstallCmds.WriteString("systemctl disable overlay-dummy.service 2>/dev/null || true\n")
			uninstallCmds.WriteString("rm -f /etc/systemd/system/overlay-dummy.service\n")
		}

		uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 4/4] Reloading systemd on '%s'...'\n", nameQuoted))
		uninstallCmds.WriteString("systemctl daemon-reload || { echo 'ERROR: systemctl daemon-reload failed' >&2; exit 1; }\n")
		uninstallCmds.WriteString(fmt.Sprintf("echo 'Overlay removed from '%s'.'\n", nameQuoted))

		// Every topology-derived shell value below is already a single-quoted inert
		// token. Deploy mode uploads the complete candidate directory to a fresh
		// mktemp path. install.sh then verifies its signature/checksum set before it
		// observes --clean or performs any other root mutation.
		b.WriteString(fmt.Sprintf(`echo ""
if [ "$UNINSTALL" -eq 1 ]; then
    echo "=== Uninstalling from "%s" ("%s") ==="
    if ! ssh%s %s "echo ok" >/dev/null 2>&1; then
        echo "  ERROR: SSH connection to "%s" failed." >&2
        FAILED=$((FAILED + 1))
    else
        if ssh%s %s sudo bash -s <<'UNINSTALL_EOF'; then
%s
UNINSTALL_EOF
            echo "  SUCCESS: "%s" uninstalled."
            SUCCESS=$((SUCCESS + 1))
        else
            echo "  ERROR: Uninstall failed on "%s" (exit code: $?)." >&2
            FAILED=$((FAILED + 1))
        fi
    fi
else
    echo "=== Deploying to "%s" ("%s") ==="
    BUNDLE_DIR=""
    if [ -d "$WORKDIR"/%s ]; then
        BUNDLE_DIR="$WORKDIR"/%s
    fi
    if [ -z "$BUNDLE_DIR" ] || [ ! -f "$BUNDLE_DIR/install.sh" ] || [ ! -f "$BUNDLE_DIR/checksums.sha256" ]; then
        echo "  WARNING: Complete bundle directory for "%s" not found in archive, skipping."
        SKIPPED=$((SKIPPED + 1))
    else
        if ! ssh%s %s "echo ok" >/dev/null 2>&1; then
            echo "  ERROR: SSH connection to "%s" failed." >&2
            FAILED=$((FAILED + 1))
        else
            REMOTE_DIR="$(ssh%s %s "mktemp -d -- %s")"
            REMOTE_PREFIX=%s
            REMOTE_SUFFIX="${REMOTE_DIR#"$REMOTE_PREFIX"}"
            if [ "$REMOTE_DIR" != "$REMOTE_PREFIX$REMOTE_SUFFIX" ] || ! [[ "$REMOTE_SUFFIX" =~ ^[A-Za-z0-9]{8}$ ]]; then
                echo "  ERROR: Remote staging directory for "%s" was not created safely." >&2
                FAILED=$((FAILED + 1))
                REMOTE_DIR=""
            fi
            if [ -n "$REMOTE_DIR" ] && %s -r "$BUNDLE_DIR" %s:"$REMOTE_DIR/bundle"; then
                if ssh%s %s "sudo bash '$REMOTE_DIR/bundle/install.sh'$INSTALL_ARGS; _yaog_rc=\$?; rm -rf -- '$REMOTE_DIR' || { [ \$_yaog_rc -ne 0 ] || _yaog_rc=1; }; exit \$_yaog_rc"; then
                    echo "  SUCCESS: "%s" deployed."
                    SUCCESS=$((SUCCESS + 1))
                else
                    echo "  ERROR: Installation script failed on "%s" (exit code: $?)." >&2
                    FAILED=$((FAILED + 1))
                fi
            elif [ -n "$REMOTE_DIR" ]; then
                echo "  ERROR: SCP upload to "%s" failed." >&2
                ssh%s %s "rm -rf -- '$REMOTE_DIR'" >/dev/null 2>&1 || true
                FAILED=$((FAILED + 1))
            fi
        fi
    fi
fi
`,
			// Uninstall branch
			nameQuoted, targetQuoted,
			sshOpts, targetQuoted,
			nameQuoted,
			sshOpts, targetQuoted,
			uninstallCmds.String(),
			nameQuoted,
			nameQuoted,
			// Deploy branch
			nameQuoted, targetQuoted,
			bundleIDQuoted, bundleIDQuoted,
			nameQuoted,
			sshOpts, targetQuoted,
			nameQuoted,
			sshOpts, targetQuoted, remoteTemplate,
			remotePrefix,
			nameQuoted,
			// SCP + verified install from the fresh directory.
			scpCmd, targetQuoted,
			sshOpts, targetQuoted,
			nameQuoted,
			nameQuoted,
			nameQuoted,
			sshOpts, targetQuoted,
		))
		b.WriteString("\n")
	}

	b.WriteString(`echo ""
echo "============================================================"
if [ "$UNINSTALL" -eq 1 ]; then
    echo "  Uninstall Summary"
else
    echo "  Deploy Summary"
fi
echo "  Success: $SUCCESS  Skipped: $SKIPPED  Failed: $FAILED"
echo "============================================================"

if [ "$FAILED" -gt 0 ]; then
    exit 1
fi
`)

	return b.String(), nil
}

func renderPS1Deploy(config DeployScriptConfig) (string, error) {
	var b strings.Builder

	b.WriteString(`# Auto-deploy script for project: ` + deployCommentText(config.ProjectName) + `
# Generated by Overlay Network Config Orchestrator
#
# Usage:
#   .\deploy-all.ps1 -ArtifactsZip <path-to-artifacts.zip> [-Clean]    Deploy overlay
#   .\deploy-all.ps1 -Uninstall                                         Remove overlay
#
# Options:
#   -Clean      Remove ALL existing WireGuard interfaces (wg*) before deploying.
#   -Uninstall  Completely remove the overlay from all nodes via SSH.
#               No artifacts ZIP needed.

param(
    [string]$ArtifactsZip,

    [switch]$Clean,

    [switch]$Uninstall
)

$ErrorActionPreference = "Continue"

if (-not $Uninstall -and [string]::IsNullOrEmpty($ArtifactsZip)) {
    Write-Host "Usage: .\deploy-all.ps1 -ArtifactsZip <path> [-Clean]"
    Write-Host "       .\deploy-all.ps1 -Uninstall"
    exit 1
}

$WorkDir = $null

if (-not [string]::IsNullOrEmpty($ArtifactsZip)) {
    if (-not (Test-Path $ArtifactsZip)) {
        Write-Error "File not found: $ArtifactsZip"
        exit 1
    }

    $ArchiveMaxBytes = [long]33554432
    $ArchiveMaxEntries = 512
    $ArchiveMaxFileBytes = [long]4194304
    $ArchiveMaxExpandedBytes = [long]16777216
    $ArchiveInfo = Get-Item -LiteralPath $ArtifactsZip -Force -ErrorAction Stop
    if ($ArchiveInfo.PSIsContainer -or $ArchiveInfo.Length -gt $ArchiveMaxBytes) {
        Write-Error "Artifacts ZIP is not a regular file or exceeds the 32 MiB archive limit"
        exit 1
    }

    # Extract to temp
    $WorkDir = Join-Path ([System.IO.Path]::GetTempPath()) ("overlay-deploy-" + [System.Guid]::NewGuid().ToString("N").Substring(0,8))
    New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null
}

# Destructive layout cleanup is delegated to the candidate install.sh as --clean. That script
# verifies its signature/checksums first; deploy-all must never mutate the remote host beforehand.
$InstallArgs = if ($Clean) { " --clean" } else { "" }

try {
    if ($WorkDir) {
        # Expand-Archive does not itself reject traversal aliases, duplicate/case-colliding names,
        # Unix symlinks, or Windows reparse points. Validate the central directory first because a
        # later recursive SCP must never follow an archive-provided link into the operator's host.
        $Archive = $null
        try {
            Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction Stop
            $Archive = [System.IO.Compression.ZipFile]::OpenRead((Resolve-Path -LiteralPath $ArtifactsZip -ErrorAction Stop))
            if ($Archive.Entries.Count -gt $ArchiveMaxEntries) {
                throw "Artifacts ZIP exceeds the 512-entry limit"
            }
            $SeenEntries = @{}
            [long]$ExpandedBytes = 0
            foreach ($Entry in $Archive.Entries) {
                $EntryName = $Entry.FullName
                $EntryCheck = $EntryName.TrimEnd([char]'/')
                if ([string]::IsNullOrEmpty($EntryCheck) -or
                    $EntryCheck -match '(^/|\\|:|(^|/)(\.|\.\.)(/|$)|//|[\x00-\x1f\x7f])') {
                    throw "Unsafe or non-canonical artifacts ZIP entry: $EntryName"
                }
                $EntryKey = $EntryName.ToUpperInvariant()
                if ($SeenEntries.ContainsKey($EntryKey)) {
                    throw "Duplicate or case-colliding artifacts ZIP entry: $EntryName"
                }
                $SeenEntries[$EntryKey] = $true

                if ($Entry.Length -gt $ArchiveMaxFileBytes) {
                    throw "Artifacts ZIP entry exceeds the 4 MiB per-file limit: $EntryName"
                }
                $ExpandedBytes += [long]$Entry.Length
                if ($ExpandedBytes -gt $ArchiveMaxExpandedBytes) {
                    throw "Artifacts ZIP exceeds the 16 MiB expanded limit"
                }

                $IsDirectory = $EntryName.EndsWith('/', [System.StringComparison]::Ordinal)
                $UnixType = ($Entry.ExternalAttributes -shr 16) -band 0xF000
                $HasReparsePoint = ($Entry.ExternalAttributes -band [int][System.IO.FileAttributes]::ReparsePoint) -ne 0
                if ($HasReparsePoint -or
                    ($UnixType -ne 0 -and
                        (($IsDirectory -and $UnixType -ne 0x4000) -or
                         (-not $IsDirectory -and $UnixType -ne 0x8000)))) {
                    throw "Artifacts ZIP contains a symlink, reparse point, or special-file entry: $EntryName"
                }
            }
        } catch {
            Write-Host ("ERROR: Failed artifacts ZIP safety check: " + $_.Exception.Message) -ForegroundColor Red
            exit 1
        } finally {
            if ($null -ne $Archive) { $Archive.Dispose() }
        }

        Write-Host "Extracting artifacts..."
        try {
            Expand-Archive -LiteralPath $ArtifactsZip -DestinationPath $WorkDir -Force -ErrorAction Stop
            $UnsafeExtractedEntry = Get-ChildItem -LiteralPath $WorkDir -Force -Recurse -ErrorAction Stop |
                Where-Object { ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 } |
                Select-Object -First 1
            if ($null -ne $UnsafeExtractedEntry) {
                throw "extracted archive contains a reparse point: $($UnsafeExtractedEntry.FullName)"
            }
        } catch {
            Write-Host ("ERROR: Failed to extract artifacts ZIP: " + $ArtifactsZip + " (" + $_.Exception.Message + ")") -ForegroundColor Red
            exit 1
        }
    }

    if ($Uninstall) {
        Write-Host ""
        Write-Host "*** UNINSTALL MODE — overlay will be removed from all nodes ***" -ForegroundColor Yellow
        Write-Host ""
    }

    $Failed = 0
    $Skipped = 0
    $Success = 0

`)

	for _, node := range config.Nodes {
		// Shell-escaped forms for the two distinct quoting contexts of the PS1
		// script. PowerShell contexts (the & ssh / & scp call-operator arguments
		// and the Write-Host strings) take powerShellArgQuote: an unquoted target
		// splits on spaces and an embedded double quote breaks the one quoted scp
		// site (D43). The bash here-string body (@'...'@ piped to ssh ... bash)
		// is interpreted by the REMOTE shell, so NodeName interpolated into its
		// single-quoted echo lines takes bashSingleQuote, same idiom as the bash
		// renderer. Every export presentation uses NodeID for the bundle directory.
		targetPSArg := powerShellArgQuote(node.SSHTarget)
		namePSStr := powerShellArgQuote(node.NodeName)
		nameBashQuoted := bashSingleQuote(node.NodeName)
		bundleIDPSArg := powerShellArgQuote(node.NodeID)
		remoteTemplatePSArg := powerShellArgQuote("mktemp -d -- " + bashSingleQuote("/tmp/yaog-"+node.NodeID+"-XXXXXXXX"))
		remotePrefixPSArg := powerShellArgQuote("/tmp/yaog-" + node.NodeID + "-")

		if !node.HasSSH {
			b.WriteString(fmt.Sprintf(`    Write-Host ""
    Write-Host ("=== " + %s + ": SKIPPED (no SSH details configured) ===")
    $Skipped++
`, namePSStr))
			b.WriteString("\n")
			continue
		}

		sshOpts, scpOpts := buildSSHOpts(node, "powershell")

		scpCmd := "scp"
		if scpOpts != "" {
			scpCmd = "scp " + scpOpts
		}

		// Build the inline uninstall script for this node (multi-line, for PS1
		// here-string). The @'...'@ here-string is literal in PowerShell, but the
		// body is piped to ssh ... sudo bash -s and run on the REMOTE node, so
		// NodeName interpolated into these single-quoted echo lines is escaped for
		// the bash single-quote context (D16/D43) via nameBashQuoted.
		var uninstallCmds strings.Builder
		uninstallCmds.WriteString("set -uo pipefail\n")
		// Tear down mimic FIRST (mirrors install.sh.tmpl), before WireGuard. This body runs on the REMOTE
		// node inside the @'...'@ here-string ssh pipes to `sudo bash -s`, so mimicUninstallLines quotes
		// the egress override for the bash context (bashSingleQuote), same as the here-string's echo lines.
		uninstallCmds.WriteString(mimicUninstallLines(node))
		uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 1/4] Stopping WireGuard interfaces on '%s'...'\n", nameBashQuoted))
		for _, iface := range node.WgInterfaces {
			uninstallCmds.WriteString(fmt.Sprintf("wg-quick down %s 2>/dev/null || true\n", iface))
			uninstallCmds.WriteString(fmt.Sprintf("systemctl disable wg-quick@%s 2>/dev/null || true\n", iface))
			uninstallCmds.WriteString(fmt.Sprintf("rm -f /etc/wireguard/%s.conf\n", iface))
		}
		uninstallCmds.WriteString("for _i in $(wg show interfaces 2>/dev/null); do wg-quick down \"$_i\" 2>/dev/null || true; systemctl disable \"wg-quick@$_i\" 2>/dev/null || true; done\n")
		uninstallCmds.WriteString("rm -f /etc/wireguard/*.conf\n")
		if node.HasBabel {
			uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 2/4] Stopping Babel routing daemon on '%s'...'\n", nameBashQuoted))
			uninstallCmds.WriteString("systemctl stop babeld 2>/dev/null || true\n")
			uninstallCmds.WriteString("systemctl disable babeld 2>/dev/null || true\n")
			uninstallCmds.WriteString("rm -f /etc/babel/babeld.conf /etc/babeld.conf\n")
			uninstallCmds.WriteString("rm -rf /etc/systemd/system/babeld.service.d\n")
		}
		uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 3/4] Removing overlay network configuration on '%s'...'\n", nameBashQuoted))
		uninstallCmds.WriteString("rm -f /etc/sysctl.d/99-overlay.conf\n")
		uninstallCmds.WriteString("sysctl --system > /dev/null 2>&1 || true\n")
		if !node.IsClient {
			// D52: CIDR-agnostic robust SNAT teardown, mirroring install.sh.tmpl — see the bash renderer's
			// site above. Deletes each matching POSTROUTING SNAT rule whole (ignoring --to-source), so a
			// stale rule from an overlay-IP change and a node on a custom transit_cidr are both cleared.
			uninstallCmds.WriteString("nft delete table inet overlay-snat 2>/dev/null || true\n")
			uninstallCmds.WriteString("iptables-save -t nat 2>/dev/null | grep -E '^-A POSTROUTING ' | grep -F -- '-j SNAT' | grep -F -- '-o wg-+' | while IFS= read -r _snat_rule; do _snat_del=\"${_snat_rule/#-A/-D}\"; iptables -t nat $_snat_del 2>/dev/null || true; done || true\n")
			uninstallCmds.WriteString("systemctl disable overlay-snat.service 2>/dev/null || true\n")
			uninstallCmds.WriteString("rm -f /etc/systemd/system/overlay-snat.service\n")
			uninstallCmds.WriteString("ip link del dummy0 2>/dev/null || true\n")
			uninstallCmds.WriteString("systemctl disable overlay-dummy.service 2>/dev/null || true\n")
			uninstallCmds.WriteString("rm -f /etc/systemd/system/overlay-dummy.service\n")
		}
		uninstallCmds.WriteString(fmt.Sprintf("echo '[Stage 4/4] Reloading systemd on '%s'...'\n", nameBashQuoted))
		uninstallCmds.WriteString("systemctl daemon-reload || { echo 'ERROR: systemctl daemon-reload failed' >&2; exit 1; }\n")
		uninstallCmds.WriteString(fmt.Sprintf("echo 'Overlay removed from '%s'.'\n", nameBashQuoted))

		// Deploy mode mirrors the bash renderer: choose the ID-keyed bundle directory,
		// upload the complete directory to a fresh remote mktemp path, then invoke the
		// candidate's own integrity-gated install.sh. The dynamic SCP destination is
		// assembled from an already-quoted constant target and a validated mktemp result.
		b.WriteString(fmt.Sprintf(`    Write-Host ""
    if ($Uninstall) {
        Write-Host ("=== Uninstalling from " + %s + " (" + %s + ") ===")
        $sshTest = & ssh%s %s "echo ok" 2>&1
        if ($LASTEXITCODE -ne 0) {
            Write-Host ("  ERROR: SSH connection to " + %s + " failed.") -ForegroundColor Red
            $Failed++
        } else {
            $uninstallScript = @'
%s'@
            # Windows PowerShell writes CRLF text to native-process stdin. Materialize an
            # explicitly LF-normalized UTF-8 file and SCP it instead. This avoids both stray
            # carriage returns and Windows' command-line-length limit for large topologies.
            $crlf = ([string][char]13) + [char]10
            $lf = [string][char]10
            $cr = [string][char]13
            $uninstallLF = $uninstallScript.Replace($crlf, $lf).Replace($cr, $lf)
            $uninstallBytes = [System.Text.UTF8Encoding]::new($false).GetBytes($uninstallLF)
            $UninstallTemp = [System.IO.Path]::GetTempFileName()
            try {
                [System.IO.File]::WriteAllBytes($UninstallTemp, $uninstallBytes)
                $RemoteOutput = & ssh%s %s %s 2>&1
                $RemoteCreateExit = $LASTEXITCODE
                $RemoteDir = [string]($RemoteOutput | Select-Object -Last 1)
                $RemoteDir = $RemoteDir.Trim()
                $RemotePrefix = %s
                $RemotePattern = '^' + [regex]::Escape($RemotePrefix) + '[A-Za-z0-9]{8}$'
                if ($RemoteCreateExit -ne 0 -or [string]::IsNullOrEmpty($RemoteDir) -or
                    $RemoteDir -notmatch $RemotePattern) {
                    Write-Host ("  ERROR: Remote uninstall staging directory for " + %s + " was not created safely.") -ForegroundColor Red
                    $Failed++
                } else {
                    $ScpDestination = %s + ":" + $RemoteDir + "/uninstall.sh"
                    & %s $UninstallTemp $ScpDestination
                    if ($LASTEXITCODE -ne 0) {
                        Write-Host ("  ERROR: Uninstall upload to " + %s + " failed.") -ForegroundColor Red
                        $CleanupCommand = "rm -rf -- '$RemoteDir'"
                        & ssh%s %s $CleanupCommand 2>$null | Out-Null
                        $Failed++
                    } else {
                        $UninstallCommand = "sudo bash '$RemoteDir/uninstall.sh'" + '; _yaog_rc=$?; rm -rf -- ' + "'$RemoteDir'" + ' || { [ $_yaog_rc -ne 0 ] || _yaog_rc=1; }; exit $_yaog_rc'
                        & ssh%s %s $UninstallCommand
                        if ($LASTEXITCODE -ne 0) {
                            Write-Host ("  ERROR: Uninstall failed on " + %s + ".") -ForegroundColor Red
                            $Failed++
                        } else {
                            Write-Host ("  SUCCESS: " + %s + " uninstalled.")
                            $Success++
                        }
                    }
                }
            } finally {
                Remove-Item -LiteralPath $UninstallTemp -Force -ErrorAction SilentlyContinue
            }
        }
    } else {
        Write-Host ("=== Deploying to " + %s + " (" + %s + ") ===")
        $BundleDir = Join-Path $WorkDir %s
        $InstallScript = Join-Path $BundleDir "install.sh"
        $Checksums = Join-Path $BundleDir "checksums.sha256"
        if (-not (Test-Path -LiteralPath $BundleDir -PathType Container) -or
            -not (Test-Path -LiteralPath $InstallScript -PathType Leaf) -or
            -not (Test-Path -LiteralPath $Checksums -PathType Leaf)) {
            Write-Warning ("Complete bundle directory for " + %s + " not found in archive, skipping.")
            $Skipped++
        } else {
            $sshTest = & ssh%s %s "echo ok" 2>&1
            if ($LASTEXITCODE -ne 0) {
                Write-Host ("  ERROR: SSH connection to " + %s + " failed.") -ForegroundColor Red
                $Failed++
            } else {
                $RemoteOutput = & ssh%s %s %s 2>&1
                $RemoteDir = [string]($RemoteOutput | Select-Object -Last 1)
                $RemoteDir = $RemoteDir.Trim()
                $RemotePrefix = %s
                $RemotePattern = '^' + [regex]::Escape($RemotePrefix) + '[A-Za-z0-9]{8}$'
                if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrEmpty($RemoteDir) -or
                    $RemoteDir -notmatch $RemotePattern) {
                    Write-Host ("  ERROR: Remote staging directory for " + %s + " was not created safely.") -ForegroundColor Red
                    $Failed++
                } else {
                    $ScpDestination = %s + ":" + $RemoteDir + "/bundle"
                    & %s -r $BundleDir $ScpDestination
                    if ($LASTEXITCODE -ne 0) {
                        Write-Host ("  ERROR: SCP upload to " + %s + " failed.") -ForegroundColor Red
                        $CleanupCommand = "rm -rf -- '$RemoteDir'"
                        & ssh%s %s $CleanupCommand 2>$null | Out-Null
                        $Failed++
                    } else {
                        $RemoteCommand = "sudo bash '$RemoteDir/bundle/install.sh'" + $InstallArgs + '; _yaog_rc=$?; rm -rf -- ' + "'$RemoteDir'" + ' || { [ $_yaog_rc -ne 0 ] || _yaog_rc=1; }; exit $_yaog_rc'
                        & ssh%s %s $RemoteCommand
                        if ($LASTEXITCODE -ne 0) {
                            Write-Host ("  ERROR: Installation script failed on " + %s + " (exit code: $LASTEXITCODE).") -ForegroundColor Red
                            $Failed++
                        } else {
                            Write-Host ("  SUCCESS: " + %s + " deployed.")
                            $Success++
                        }
                    }
                }
            }
        }
    }
`,
			// Uninstall branch
			namePSStr, targetPSArg,
			sshOpts, targetPSArg,
			namePSStr,
			uninstallCmds.String(),
			sshOpts, targetPSArg, remoteTemplatePSArg,
			remotePrefixPSArg,
			namePSStr,
			targetPSArg,
			scpCmd,
			namePSStr,
			sshOpts, targetPSArg,
			sshOpts, targetPSArg,
			namePSStr,
			namePSStr,
			// Deploy branch
			namePSStr, targetPSArg,
			bundleIDPSArg,
			namePSStr,
			sshOpts, targetPSArg,
			namePSStr,
			sshOpts, targetPSArg, remoteTemplatePSArg,
			remotePrefixPSArg,
			namePSStr,
			targetPSArg,
			scpCmd,
			namePSStr,
			sshOpts, targetPSArg,
			sshOpts, targetPSArg,
			namePSStr,
			namePSStr,
		))
		b.WriteString("\n")
	}

	b.WriteString(`    Write-Host ""
    Write-Host "============================================================"
    if ($Uninstall) {
        Write-Host "  Uninstall Summary"
    } else {
        Write-Host "  Deploy Summary"
    }
    Write-Host "  Success: $Success  Skipped: $Skipped  Failed: $Failed"
    Write-Host "============================================================"

    if ($Failed -gt 0) {
        exit 1
    }
} finally {
    if ($WorkDir) { Remove-Item -Recurse -Force $WorkDir -ErrorAction SilentlyContinue }
}
`)

	return b.String(), nil
}
