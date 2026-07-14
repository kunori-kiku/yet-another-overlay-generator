package api

// handler_bootstrap.go is the one-shot agent bootstrap surface (plan-5.2):
//   - agent route GET /bootstrap — serve the bash install+enroll+apply script
//     (unauthenticated; the script is generic, the enrollment token is a flag).
//
// The bootstrap script downloads the per-arch yaog-agent binary (GitHub proxy applied
// when configured), installs it, enrolls with the operator-supplied single-use token,
// and either installs a systemd daemon (default — so every future Deploy auto-applies)
// or applies once (--once). When the keystone is ON, the controller bakes the pinned
// off-host operator credential into the script so the node enforces membership.
//
// It also holds the operator-set-field format validators the /settings POST runs
// (validateAbsoluteHTTPURL / validateMimicCatalog / validateAgentRollout /
// validateOperatorCredentialBinding): they gate values baked into this root-executed
// script, so they live next to the renderer. The /settings read/write API itself moved
// out to handler_settings.go (plan-9).

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/version"
)

// HandleBootstrap serves the bash one-shot bootstrap script (agent port, NO auth).
// It bakes the server-side settings (controller URL incl. the secret path prefix, the
// GitHub proxy, the agent release URL) and — when the keystone is ON — the pinned
// off-host operator credential into the script as DEFAULTS; the node operator supplies
// the single-use enrollment token via a flag.
func (h *ControllerHandler) HandleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	cs, err := h.loadSettings(r)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// The agent's --controller is scheme://host[:port] + the AGENT secret path prefix
	// (YAOG_AGENT_PATH_PREFIX; the agent appends /api/v1/agent/ itself). The
	// server knows its own prefix, so it composes the full base here. The operator
	// prefix is deliberately NOT used: agents only ever talk to the agent port.
	// With no PublicAgentURL configured the base stays "" — appending the prefix to an
	// empty base would yield a schemeless "/<seg>" that defeats the script's
	// "--controller is required" guard and fails with a confusing connection error.
	controllerBase := ""
	if cs.PublicAgentURL != "" {
		controllerBase = strings.TrimRight(cs.PublicAgentURL, "/") + h.agentPrefix
	}

	// Keystone: bake the pinned off-host operator credential (public only) so the node
	// enforces membership. ONLY a genuine ErrNotFound means keystone OFF; any other
	// store error must fail loud — silently emitting a keystone-OFF script when the
	// keystone is actually ON would ship a node that does not verify membership.
	var cred *controller.OperatorCredential
	if oc, err := h.store.GetOperatorCredential(r.Context(), h.tenant); err == nil {
		cred = &oc
	} else if !errors.Is(err, controller.ErrNotFound) {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	script := renderBootstrapScript(controllerBase, cs.GithubProxy, cs.AgentReleaseBaseURL, cs.AgentBins, cred)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}

// validateAbsoluteHTTPURL ensures s is an absolute http(s) URL with a host, so a
// misconfigured setting fails at save time (a 400) rather than producing a broken
// bootstrap script later.
func validateAbsoluteHTTPURL(s string) error {
	// Reject whitespace/control characters: these URLs are baked into the bootstrap
	// bash script, and a space would word-split an (unquoted) systemd ExecStart token
	// even though the assignment itself is shellSingleQuote-safe. A real http(s) URL has none.
	if strings.ContainsAny(s, " \t\r\n\v\f") {
		return errors.New("must not contain whitespace")
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("must be a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must be an absolute http(s) URL")
	}
	if u.Host == "" {
		return errors.New("must include a host")
	}
	return nil
}

// Strict format patterns for the mimic GitHub-.deb catalog (D8). These pins are operator-set and
// flow into the controller-signed artifacts.json + a root-executed install.sh, so they are
// validated at save time, not trusted at deploy time.
var (
	semverPattern    = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)*$`)
	sha256HexPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	debKeyPattern    = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`) // "<codename>-<arch>"
	debAssetPattern  = regexp.MustCompile(`^[A-Za-z0-9._+-]+\.deb$`)
	// Agent self-update pins (plan-9 D8): key "linux-<arch>" (e.g. "linux-amd64"); a safe
	// asset filename (the published per-arch agent binary, no shell/path metacharacters).
	agentBinKeyPattern   = regexp.MustCompile(`^linux-[a-z0-9]+$`)
	agentBinAssetPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// validateMimicCatalog enforces D8 on the mimic GitHub-.deb catalog: a semver version, an http(s)
// release base, a "<codename>-<arch>" key, a safe ".deb" asset name (no path/shell metacharacters),
// and a 64-hex SHA-256 per pin. Every field is optional (empty = no catalog), but a partially
// specified catalog that could not actually fetch (debs without a release base) is rejected.
// Returns a coded *apierr.Error (CodeReqFieldInvalid) naming the offending field, or nil.
func validateMimicCatalog(cs controller.ControllerSettings) *apierr.Error {
	if cs.MimicVersion != "" && !semverPattern.MatchString(cs.MimicVersion) {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_version")
	}
	if cs.MimicReleaseBase != "" {
		if err := validateAbsoluteHTTPURL(cs.MimicReleaseBase); err != nil {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_release_base").Wrap(err)
		}
		// Defense-in-depth: the base is emitted into artifacts.json and read into a root install.sh
		// (used quoted, so this is not a live injection), but reject shell-dangerous bytes anyway so
		// it is as strict as the sibling asset/key pins. A real release URL contains none of these.
		if strings.ContainsAny(cs.MimicReleaseBase, shellDangerousBytes) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_release_base")
		}
	}
	if len(cs.MimicDebs) > 0 && cs.MimicReleaseBase == "" {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_release_base")
	}
	for key, art := range cs.MimicDebs {
		if !debKeyPattern.MatchString(key) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_debs.key")
		}
		if !debAssetPattern.MatchString(art.Asset) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_debs["+key+"].asset")
		}
		if !sha256HexPattern.MatchString(art.SHA256) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_debs["+key+"].sha256")
		}
		// Companion mimic-dkms .deb (the two-package install): validated when EITHER dkms field is
		// set — both must then be present + valid. A row with NEITHER is a mimic-only pin, allowed
		// for back-compat but it fails on split-package distros (Debian 12 / Ubuntu 24.04); the
		// panel surfaces that as a warning and the install degrades under mimic_fallback=udp.
		if art.DKMSAsset != "" || art.DKMSSHA256 != "" {
			if !debAssetPattern.MatchString(art.DKMSAsset) {
				return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_debs["+key+"].dkms_asset")
			}
			if !sha256HexPattern.MatchString(art.DKMSSHA256) {
				return apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_debs["+key+"].dkms_sha256")
			}
		}
	}
	return nil
}

// shellDangerousBytes is the shell-metacharacter class rejected on operator-set fields that
// are baked into the root-executed bootstrap script (see validateMimicCatalog at the release
// base). It already includes a space; validateOperatorCredentialBinding adds the remaining
// whitespace (tab/CR/LF/VT/FF) because the OP_FLAGS injection vector is word-splitting.
const shellDangerousBytes = "$`;|&<>(){}[]'\"\\*? "

// validateOperatorCredentialBinding rejects an operator credential whose RPID or Origin
// carries shell-dangerous bytes OR whitespace. These two fields are emitted UNQUOTED into the
// bootstrap script's OP_FLAGS accumulator (handler_bootstrap.go OP_FLAGS / ExecStart / --once:
// the unquoted ${OP_FLAGS} is intentional — it is a multi-flag string that REQUIRES word-
// splitting to expand into separate argv entries, hence the explicit `# shellcheck disable=SC2086`).
// Quoting OP_FLAGS would collapse every flag into one argument and break enrollment, so the
// load-bearing defense is validate-at-pin: once RPID/Origin contain no whitespace and no
// metacharacter, the unquoted expansion is safe BY CONSTRUCTION. Whitespace is the primary
// vector here (word-splitting), so it is rejected in addition to the shell metacharacter class
// reused from validateMimicCatalog. Empty RPID/Origin are valid (a keystone binding need not
// carry either). Returns a coded *apierr.Error (CodeReqFieldInvalid) naming the field, or nil.
func validateOperatorCredentialBinding(cred controller.OperatorCredential) *apierr.Error {
	const whitespaceBytes = " \t\r\n\v\f" // matches validateAbsoluteHTTPURL's reject class
	check := func(field, val string) *apierr.Error {
		if strings.ContainsAny(val, shellDangerousBytes) || strings.ContainsAny(val, whitespaceBytes) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", field)
		}
		return nil
	}
	if err := check("rpid", cred.RPID); err != nil {
		return err
	}
	if err := check("origin", cred.Origin); err != nil {
		return err
	}
	return nil
}

// UnsafeOperatorCredentialBindingField reports the offending field ("rpid"/"origin") of a
// stored operator credential whose binding would NOT pass the forward-only validate-at-pin
// gate (validateOperatorCredentialBinding) — i.e. a credential pinned BEFORE that gate existed
// whose RPID/Origin still carries whitespace or a shell metacharacter and would word-split via
// the unquoted ${OP_FLAGS} in the rendered bootstrap script. Returns "" when the binding is safe.
//
// It is the EXPORTED, advisory-only counterpart of the pin-time gate: the byte-class logic is
// single-sourced through validateOperatorCredentialBinding so the startup warning and the pin
// rejection can never diverge. Callers (the controller startup path in cmd/server) use this to
// log a WARNING — never to lock an already-authenticated operator out mid-upgrade.
func UnsafeOperatorCredentialBindingField(cred controller.OperatorCredential) string {
	if err := validateOperatorCredentialBinding(cred); err != nil {
		return err.Params()["field"]
	}
	return ""
}

// validateAgentRollout enforces D8 on the signed agent self-update pins: semver target/min
// versions, a "linux-<arch>" bin key, a safe asset filename, and a 64-hex SHA-256 per pin. These
// flow into the controller-signed artifacts.json the agent verifies a fetched binary against
// before exec, so they are validated at save time. A non-empty TargetAgentVersion REQUIRES at
// least one AgentBins entry — a target with no fetchable asset can only ever no-op, so reject it
// as a misconfiguration rather than silently freeze the rollout. The agent release base is the
// already-validated AgentReleaseBaseURL (http(s)). Canary node-ids need no format check: an id
// matching no enrolled node is simply absent from the rollout set (AgentRolloutNodeIDs intersects).
// validateAgentRollout also enforces the plan-8 refuse-newer floor: a TargetAgentVersion strictly
// NEWER than controllerVersion is rejected, because the controller can only certify a self-update
// target its own release pipeline shipped (the release the agent fetches is published by the same
// pipeline that stamps BuildVersion), so a newer target can only ever fail-closed at verify time —
// reject it at save with a clear coded error rather than arming a rollout that can never converge.
// controllerVersion is the in-process main.BuildVersion (plan-7). The floor is a PRODUCTION safety net,
// not a dev blocker: an unstamped/non-semver controllerVersion (a "dev" `go run` build) DISABLES the
// floor (version.Compare treats a non-semver core as 0.0.0, which would otherwise reject every real
// target and freeze a dev controller), so the floor only applies when controllerVersion is real semver.
func validateAgentRollout(cs controller.ControllerSettings, controllerVersion string) *apierr.Error {
	if cs.TargetAgentVersion != "" && !semverPattern.MatchString(cs.TargetAgentVersion) {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "target_agent_version")
	}
	if cs.MinAgentVersion != "" && !semverPattern.MatchString(cs.MinAgentVersion) {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "min_agent_version")
	}
	for key, art := range cs.AgentBins {
		if !agentBinKeyPattern.MatchString(key) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "agent_bins.key")
		}
		if !agentBinAssetPattern.MatchString(art.Asset) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "agent_bins["+key+"].asset")
		}
		if !sha256HexPattern.MatchString(art.SHA256) {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "agent_bins["+key+"].sha256")
		}
	}
	if cs.TargetAgentVersion != "" && len(cs.AgentBins) == 0 {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "agent_bins")
	}
	// Refuse-newer floor (plan-8): reject a target strictly newer than the controller's own version,
	// using the SAME comparator as the agent's anti-downgrade floor (internal/version, single-sourced).
	// Only applies when controllerVersion is real semver — a dev/unstamped build's non-semver version
	// would otherwise reject every target, so it disables the floor instead of freezing the controller.
	if cs.TargetAgentVersion != "" && semverPattern.MatchString(controllerVersion) &&
		version.Compare(cs.TargetAgentVersion, controllerVersion) > 0 {
		return apierr.New(apierr.CodeAgentTargetNewerThanController).
			With("target", cs.TargetAgentVersion).
			With("controller", controllerVersion)
	}
	return nil
}

// shellSingleQuote POSIX single-quote-escapes s so it can be spliced into the bootstrap
// bash script as one inert shell word: the whole value is wrapped in single quotes '…'
// (which preserve newlines and disable ALL shell expansion) and every embedded single
// quote is rewritten as the '\” idiom (close the quote, emit an escaped literal ', reopen).
// It is a self-contained api-local primitive — internal/api does NOT import internal/renderer,
// so the renderer's ShellToken seam is deliberately not reused here. The injected values are
// operator-configured settings + the public operator credential (never request input), but
// quoting keeps a stray metacharacter from breaking the emitted assignment.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderBootstrapScript composes the bootstrap script: the server-injected config
// header (single-quoted) followed by the static body (bootstrapScriptBody).
func renderBootstrapScript(controllerBase, ghProxy, releaseBase string, agentBins map[string]model.Artifact, cred *controller.OperatorCredential) string {
	credAlg, credRPID, credOrigin, credPEM := "", "", "", ""
	if cred != nil {
		credAlg, credRPID, credOrigin, credPEM = cred.Alg, cred.RPID, cred.Origin, cred.PublicKeyPEM
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# YAOG one-shot agent bootstrap (plan-5.2). Run as root on the target node, e.g.:\n")
	b.WriteString("#   bash <(curl -fsSL <public-agent-url>/api/v1/agent/bootstrap) --token <T> --node-id <ID>\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("# --- server-injected defaults (operator settings; flags below override) ---\n")
	fmt.Fprintf(&b, "CONTROLLER=%s\n", shellSingleQuote(controllerBase))
	fmt.Fprintf(&b, "GH_PROXY=%s\n", shellSingleQuote(ghProxy))
	fmt.Fprintf(&b, "RELEASE_BASE=%s\n", shellSingleQuote(releaseBase))
	fmt.Fprintf(&b, "OPERATOR_CRED_ALG=%s\n", shellSingleQuote(credAlg))
	// OP_FLAGS unquoted-by-design: OPERATOR_RPID / OPERATOR_ORIGIN are single-quoted at THIS
	// assignment (assignment integrity — an embedded quote can't break the line), but UNLIKE the
	// other fields they are re-expanded UNQUOTED downstream: bootstrapScriptBody splices them into
	// the OP_FLAGS accumulator ("--operator-rpid ${OPERATOR_RPID} …") which is then word-split at the
	// unquoted ${OP_FLAGS} expansion (ExecStart / --once) into separate argv flags. That splice is a
	// THIRD shell context where single-quoting the VALUE would collapse the two flags into one
	// argument and break enrollment — so it is NOT single-quoted there, by design. Their injection-
	// safety is enforced at PIN time by validateOperatorCredentialBinding (rejecting whitespace + shell
	// metacharacters), NOT by quoting. Do NOT try to further quote them (here or at the splice).
	fmt.Fprintf(&b, "OPERATOR_RPID=%s\n", shellSingleQuote(credRPID))
	fmt.Fprintf(&b, "OPERATOR_ORIGIN=%s\n", shellSingleQuote(credOrigin))
	fmt.Fprintf(&b, "OPERATOR_CRED_PEM=%s\n\n", shellSingleQuote(credPEM))
	// Bake the per-arch agent-binary pins (SHA-256 + asset) the operator configured, as shell-safe vars
	// the body verifies before install (plan-6). Sorted for determinism. The keys are already
	// charset-validated at settings-save (agentBinKeyPattern = linux-<arch>) and re-checked here
	// defensively before becoming a bash identifier (hyphen -> underscore: linux-amd64 -> linux_amd64).
	if len(agentBins) > 0 {
		keys := make([]string, 0, len(agentBins))
		for k := range agentBins {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("# per-arch agent-binary pins (from the operator's agent rollout settings)\n")
		for _, k := range keys {
			art := agentBins[k]
			if !agentBinKeyPattern.MatchString(k) || !sha256HexPattern.MatchString(art.SHA256) || !agentBinAssetPattern.MatchString(art.Asset) {
				continue
			}
			varSuffix := strings.ReplaceAll(k, "-", "_")
			fmt.Fprintf(&b, "AGENT_SHA_%s=%s\n", varSuffix, shellSingleQuote(art.SHA256))
			fmt.Fprintf(&b, "AGENT_ASSET_%s=%s\n", varSuffix, shellSingleQuote(art.Asset))
		}
		b.WriteString("\n")
	}
	b.WriteString(bootstrapScriptBody)
	return b.String()
}

// bootstrapScriptBody is the static bash logic appended after the injected config. It
// parses flags, downloads the per-arch agent (GitHub proxy applied), installs it,
// enrolls, then installs a systemd daemon (default) or applies once (--once). It must
// not contain backticks (Go raw string); all command substitution uses $(...).
const bootstrapScriptBody = `TOKEN=""
NODE_ID=""
DAEMON=1   # default: install a continuous systemd daemon. --once = single apply.

usage() {
  cat >&2 <<USAGE
usage: bootstrap --token <enrollment-token> --node-id <id> [flags]
  --token T         single-use enrollment token (minted in the panel) [required]
  --node-id ID      node identity to enroll as [required]
  --controller URL  controller agent base URL (default: server-configured)
  --gh-proxy URL    GitHub proxy prefix, e.g. https://gh-proxy.com/ (default: server-configured)
  --release-base U  agent binary release base URL (default: server-configured)
  --once            apply once and exit instead of installing the systemd daemon
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --token)        TOKEN="${2:-}"; shift; [ $# -gt 0 ] && shift ;;
    --node-id)      NODE_ID="${2:-}"; shift; [ $# -gt 0 ] && shift ;;
    --controller)   CONTROLLER="${2:-}"; shift; [ $# -gt 0 ] && shift ;;
    --gh-proxy)     GH_PROXY="${2:-}"; shift; [ $# -gt 0 ] && shift ;;
    --release-base) RELEASE_BASE="${2:-}"; shift; [ $# -gt 0 ] && shift ;;
    --once)         DAEMON=0; shift ;;
    -h|--help)      usage; exit 0 ;;
    *) echo "bootstrap: unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

fail() { echo "bootstrap: $1" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "must run as root (the agent installs to /usr/local/bin and configures WireGuard)"
[ -n "$TOKEN" ]      || fail "--token is required (mint one in the panel)"
[ -n "$NODE_ID" ]    || fail "--node-id is required"
[ -n "$CONTROLLER" ] || fail "--controller is required (set the public agent URL in the panel, or pass --controller)"
command -v curl >/dev/null 2>&1 || fail "curl is required"

# Map the machine architecture to the published agent asset name.
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)   ASSET="yaog-agent-linux-amd64"; agent_arch="amd64" ;;
  aarch64|arm64)  ASSET="yaog-agent-linux-arm64"; agent_arch="arm64" ;;
  i386|i686)      ASSET="yaog-agent-linux-386"; agent_arch="386" ;;
  armv7l|armv7)   ASSET="yaog-agent-linux-armv7"; agent_arch="armv7" ;;
  *) fail "unsupported architecture: $arch" ;;
esac

# Resolve the per-arch agent-binary pin the operator configured (baked above; empty = not configured).
# An operator-set asset overrides the default; the SHA-256 gates the install below.
sha_var="AGENT_SHA_linux_${agent_arch}"
asset_var="AGENT_ASSET_linux_${agent_arch}"
pin_sha="${!sha_var:-}"
pin_asset="${!asset_var:-}"
[ -n "$pin_asset" ] && ASSET="$pin_asset"

URL="${GH_PROXY}${RELEASE_BASE}/${ASSET}"
echo ">> downloading agent: $URL"
install -d -m 0755 /usr/local/bin
tmp_bin="$(mktemp)"
trap 'rm -f "$tmp_bin"' EXIT
# Pin the allowed protocols to https/http ONLY (one comma list — a SECOND '--proto
# =...' would REPLACE the allow-list rather than extend it, leaving http-only), so a
# redirect cannot pivot to file://, scp://, etc. --proto has been in curl since 7.20.
curl -fL --retry 3 --proto '=https,http' "$URL" -o "$tmp_bin"
# Verify the agent binary against the operator's SHA-256 pin (fail-closed): this closes the
# first-contact binary-TOFU — a MITM or compromised mirror cannot ship a tampered root binary. The pin
# makes integrity independent of the transport. When NO pin is configured for this arch, warn loudly
# and proceed (preserves the "just set a release URL" bring-up; the binary-TOFU is then the operator's
# explicit, visible choice — not a silent gap).
if [ -n "$pin_sha" ]; then
  command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required to verify the pinned agent binary"
  printf '%s  %s\n' "$pin_sha" "$tmp_bin" | sha256sum -c - >/dev/null 2>&1 || fail "agent binary SHA-256 does not match the configured pin for linux-${agent_arch} (expected ${pin_sha}); refusing to install"
  echo ">> agent binary verified against the configured SHA-256 pin"
else
  echo ">> WARNING: no SHA-256 pin configured for linux-${agent_arch}; agent binary integrity is NOT verified (set one in the agent rollout settings)" >&2
fi
install -m 0755 "$tmp_bin" /usr/local/bin/yaog-agent

# write_operator_cred "$cred_file" "$pem": write the baked operator-credential PEM to $cred_file at
# 0600, RE-PINNING (overwriting) any existing file by default. The bootstrap runs as root and is
# fetched fresh from the controller, so its baked credential IS the current pinned keystone — so a
# re-bootstrap SHOULD (re)pin it, and refusing would buy no security (root can rewrite this file
# directly) while blocking a legitimate re-provision. We do NOT do it silently: when we replace a
# DIFFERING credential we log a loud NOTICE, so the one real downside (re-running a STALE script that
# downgrades the pin to an old key) is visible rather than hidden. To re-pin WITHOUT a full
# bootstrap, use yaog-agent reprovision-keystone. The function is unit-tested in isolation.
write_operator_cred() {
  woc_file="$1"; woc_pem="$2"
  if [ -f "$woc_file" ] && [ "$(cat "$woc_file" 2>/dev/null)" != "$(printf '%s' "$woc_pem")" ]; then
    echo "bootstrap: NOTICE: re-pinning $woc_file — the existing operator credential DIFFERS (byte comparison; may be the same key re-encoded) from this script's baked credential. Overwriting with the script's credential." >&2
    echo "bootstrap: if that was NOT intended (e.g. a STALE script baked an OLD key), re-pin the correct one with:" >&2
    echo "bootstrap:   yaog-agent reprovision-keystone --operator-cred <cred.pem> --operator-cred-alg ${OPERATOR_CRED_ALG}" >&2
  fi
  printf '%s\n' "$woc_pem" > "$woc_file"
  chmod 0600 "$woc_file"
}

# Build the keystone (off-host operator credential) flags when the controller baked a
# credential into this script (keystone ON). The PEM is public; written 0600 anyway.
#
# OP_FLAGS is a multi-flag ACCUMULATOR: each [ -n ... ] && append builds one space-
# separated string ("--operator-cred FILE --operator-cred-alg ALG --operator-rpid RPID ...").
# It is expanded UNQUOTED at the ExecStart and --once sites below precisely so word-splitting
# turns it back into separate argv entries; quoting it would pass the whole string as one
# argument and break enrollment. That unquoted expansion is safe BY CONSTRUCTION because the
# controller rejects whitespace + shell metacharacters in RPID/Origin at pin time
# (validateOperatorCredentialBinding) and shQuotes the ALG, so no field can inject a flag or a
# command. Do NOT "fix" the SC2086-disabled --once line: the unquoting is intentional.
OP_FLAGS=""
if [ -n "$OPERATOR_CRED_PEM" ]; then
  install -d -m 0700 /etc/wireguard
  cred_file=/etc/wireguard/operator-cred.pem
  write_operator_cred "$cred_file" "$OPERATOR_CRED_PEM"
  OP_FLAGS="--operator-cred $cred_file --operator-cred-alg ${OPERATOR_CRED_ALG}"
  [ -n "$OPERATOR_RPID" ]   && OP_FLAGS="$OP_FLAGS --operator-rpid ${OPERATOR_RPID}"
  [ -n "$OPERATOR_ORIGIN" ] && OP_FLAGS="$OP_FLAGS --operator-origin ${OPERATOR_ORIGIN}"
fi

echo ">> enrolling node: $NODE_ID"
/usr/local/bin/yaog-agent enroll --controller "$CONTROLLER" --node-id "$NODE_ID" --token "$TOKEN"

if [ "$DAEMON" -eq 1 ]; then
  echo ">> installing systemd daemon (continuous; future deploys auto-apply)"
  # Pass the GitHub proxy to the daemon (plan-9) so signed agent self-update fetches the new
  # binary through the same mirror the bootstrap used; empty = direct (flag omitted).
  GH_PROXY_FLAG=""
  [ -n "$GH_PROXY" ] && GH_PROXY_FLAG="--gh-proxy ${GH_PROXY}"
  cat > /etc/systemd/system/yaog-agent.service <<UNIT
[Unit]
Description=YAOG overlay agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/yaog-agent run --controller "${CONTROLLER}" --node-id "${NODE_ID}" --daemon ${GH_PROXY_FLAG} ${OP_FLAGS}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable yaog-agent.service
  # RESTART, not "enable --now": a re-bootstrap of an ALREADY-RUNNING daemon must pick up the freshly
  # enrolled bearer token and the (re-pinned) operator credential, both of which the daemon reads
  # ONLY at startup. "enable --now" is start-if-stopped only, so on an active unit it leaves the OLD
  # in-memory token (401 loop) and OLD pinned credential in place; "restart" re-execs a running unit
  # AND starts a stopped one, so a re-bootstrap always takes effect. Cost: the re-exec'd daemon
  # re-applies the current bundle once on startup (a brief keep-last-good per-interface WG/Babel
  # flap), identical to the Restart=always crash/reboot re-apply — the deliberate price of reloading
  # the at-startup-only token + credential.
  systemctl restart yaog-agent.service
  echo ">> agent installed + (re)started as a daemon; the current generation applies on the next poll"
else
  echo ">> applying once"
  # $OP_FLAGS MUST stay unquoted here: it is a word-split multi-flag accumulator (see the
  # OP_FLAGS comment above). Safe by construction — RPID/Origin are whitespace/metachar-free
  # by validate-at-pin (validateOperatorCredentialBinding) and the alg/cred are shQuoted.
  # shellcheck disable=SC2086
  /usr/local/bin/yaog-agent run --controller "$CONTROLLER" --node-id "$NODE_ID" $OP_FLAGS
fi
echo ">> bootstrap complete"
`
