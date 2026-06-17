package api

// handler_bootstrap.go is the one-shot agent bootstrap surface (plan-5.2):
//   - operator routes GET/POST /settings  — read/update the server-persisted
//     controller settings (public agent URL, GitHub proxy, agent release URL).
//   - agent route    GET  /bootstrap      — serve the bash install+enroll+apply
//     script (unauthenticated; the script is generic, the enrollment token is a flag).
//
// The bootstrap script downloads the per-arch yaog-agent binary (GitHub proxy applied
// when configured), installs it, enrolls with the operator-supplied single-use token,
// and either installs a systemd daemon (default — so every future Deploy auto-applies)
// or applies once (--once). When the keystone is ON, the controller bakes the pinned
// off-host operator credential into the script so the node enforces membership.

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
)

// settingsJSON is the wire form of the operator-editable controller settings.
type settingsJSON struct {
	PublicAgentURL      string `json:"public_agent_url"`
	GithubProxy         string `json:"github_proxy"`
	AgentReleaseBaseURL string `json:"agent_release_base_url"`
	// Translucency is the panel's appearance preference (P5). It round-trips through
	// GET/POST /settings but is NOT injected into the bootstrap script.
	Translucency bool `json:"translucency"`
	// AgentPathPrefix is READ-ONLY: the server's normalized agent secret path prefix
	// (YAOG_AGENT_PATH_PREFIX, "" or "/<seg>"), reported so the panel composes
	// agent-facing URLs (the bootstrap one-liner, the manual enroll command)
	// server-authoritatively instead of mirroring a second env by hand. It is
	// env-derived, not a stored setting — POST ignores any submitted value.
	AgentPathPrefix string `json:"agent_path_prefix"`
	// Mimic GitHub-.deb catalog (plan-3). All NON-SECRET pins. Empty = distro-only mimic.
	MimicVersion     string                       `json:"mimic_version,omitempty"`
	MimicReleaseBase string                       `json:"mimic_release_base,omitempty"`
	MimicDebs        map[string]renderer.Artifact `json:"mimic_debs,omitempty"`
	// Signed agent self-update (plan-9, canary-then-fleet). All NON-SECRET pins; the agent
	// release base is the existing AgentReleaseBaseURL above. Empty target ⇒ no self-update.
	TargetAgentVersion    string                       `json:"target_agent_version,omitempty"`
	MinAgentVersion       string                       `json:"min_agent_version,omitempty"`
	AgentBins             map[string]renderer.Artifact `json:"agent_bins,omitempty"`
	AgentCanaryNodeIDs    []string                     `json:"agent_canary_node_ids,omitempty"`
	AgentRolloutFleetWide bool                         `json:"agent_rollout_fleet_wide,omitempty"`
}

// settingsResponse builds the wire view of cs: the stored settings plus the
// server-derived read-only fields (agent path prefix). Both HandleSettings branches
// MUST respond through this single constructor so a field added for GET cannot be
// forgotten for POST (which would make it flicker empty right after every save).
func (h *ControllerHandler) settingsResponse(cs controller.ControllerSettings) settingsJSON {
	return settingsJSON{
		PublicAgentURL:        cs.PublicAgentURL,
		GithubProxy:           cs.GithubProxy,
		AgentReleaseBaseURL:   cs.AgentReleaseBaseURL,
		Translucency:          cs.Translucency != nil && *cs.Translucency,
		AgentPathPrefix:       h.agentPrefix,
		MimicVersion:          cs.MimicVersion,
		MimicReleaseBase:      cs.MimicReleaseBase,
		MimicDebs:             cs.MimicDebs,
		TargetAgentVersion:    cs.TargetAgentVersion,
		MinAgentVersion:       cs.MinAgentVersion,
		AgentBins:             cs.AgentBins,
		AgentCanaryNodeIDs:    cs.AgentCanaryNodeIDs,
		AgentRolloutFleetWide: cs.AgentRolloutFleetWide,
	}
}

// loadSettings returns the tenant's settings with defaults applied (so an absent or
// partially-saved record still yields a usable agent release URL).
func (h *ControllerHandler) loadSettings(r *http.Request) (controller.ControllerSettings, error) {
	cs, err := h.store.GetSettings(r.Context(), h.tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return controller.DefaultSettings(), nil
		}
		return controller.ControllerSettings{}, err
	}
	return cs.WithDefaults(), nil
}

// HandleSettings serves GET (read current settings, defaults applied) and POST (save
// settings). Operator-authenticated. POST validates a non-empty PublicAgentURL is an
// absolute http(s) URL and audits the update.
func (h *ControllerHandler) HandleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cs, err := h.loadSettings(r)
		if err != nil {
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
		writeJSON(w, http.StatusOK, h.settingsResponse(cs))

	case http.MethodPost:
		tenant, actor, ok := identity(r.Context())
		if !ok {
			writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
			return
		}
		var req settingsJSON
		if err := decodeJSON(w, r, &req); err != nil {
			writeCodedOr(w, apierr.CodeReqInvalidBody, err)
			return
		}
		// A POST always carries an explicit translucency bool (the panel sends it), so pin
		// it as a non-nil pointer; WithDefaults only fills a nil (legacy-load) value.
		translucency := req.Translucency
		cs := controller.ControllerSettings{
			PublicAgentURL:        strings.TrimSpace(req.PublicAgentURL),
			GithubProxy:           strings.TrimSpace(req.GithubProxy),
			AgentReleaseBaseURL:   strings.TrimSpace(req.AgentReleaseBaseURL),
			Translucency:          &translucency,
			MimicVersion:          strings.TrimSpace(req.MimicVersion),
			MimicReleaseBase:      strings.TrimSpace(req.MimicReleaseBase),
			MimicDebs:             req.MimicDebs,
			TargetAgentVersion:    strings.TrimSpace(req.TargetAgentVersion),
			MinAgentVersion:       strings.TrimSpace(req.MinAgentVersion),
			AgentBins:             req.AgentBins,
			AgentCanaryNodeIDs:    req.AgentCanaryNodeIDs,
			AgentRolloutFleetWide: req.AgentRolloutFleetWide,
		}.WithDefaults()
		if cs.PublicAgentURL != "" {
			if err := validateAbsoluteHTTPURL(cs.PublicAgentURL); err != nil {
				writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "public_agent_url").Wrap(err))
				return
			}
		}
		if cs.GithubProxy != "" {
			if err := validateAbsoluteHTTPURL(cs.GithubProxy); err != nil {
				writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "github_proxy").Wrap(err))
				return
			}
		}
		if err := validateAbsoluteHTTPURL(cs.AgentReleaseBaseURL); err != nil {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "agent_release_base_url").Wrap(err))
			return
		}
		if err := validateMimicCatalog(cs); err != nil {
			writeAPIError(w, err)
			return
		}
		if err := validateAgentRollout(cs); err != nil {
			writeAPIError(w, err)
			return
		}
		if err := h.store.PutSettings(r.Context(), tenant, cs); err != nil {
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
		_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
			Timestamp: time.Now().UTC(),
			Actor:     "operator:" + actor,
			Action:    "settings-update",
		})
		writeJSON(w, http.StatusOK, h.settingsResponse(cs))

	default:
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET, POST"))
	}
}

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

	script := renderBootstrapScript(controllerBase, cs.GithubProxy, cs.AgentReleaseBaseURL, cred)
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
	// even though the assignment itself is shQuote-safe. A real http(s) URL has none.
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
		if strings.ContainsAny(cs.MimicReleaseBase, "$`;|&<>(){}[]'\"\\*? ") {
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
	}
	return nil
}

// validateAgentRollout enforces D8 on the signed agent self-update pins: semver target/min
// versions, a "linux-<arch>" bin key, a safe asset filename, and a 64-hex SHA-256 per pin. These
// flow into the controller-signed artifacts.json the agent verifies a fetched binary against
// before exec, so they are validated at save time. A non-empty TargetAgentVersion REQUIRES at
// least one AgentBins entry — a target with no fetchable asset can only ever no-op, so reject it
// as a misconfiguration rather than silently freeze the rollout. The agent release base is the
// already-validated AgentReleaseBaseURL (http(s)). Canary node-ids need no format check: an id
// matching no enrolled node is simply absent from the rollout set (AgentRolloutNodeIDs intersects).
func validateAgentRollout(cs controller.ControllerSettings) *apierr.Error {
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
	return nil
}

// shQuote single-quotes s for safe inclusion in the bootstrap bash script: the value
// is wrapped in single quotes (which preserve newlines and disable all expansion) and
// any embedded single quote is escaped as '\”. The injected values are operator-
// configured settings + the public operator credential, never request input, but
// quoting keeps a stray metacharacter from breaking the script.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderBootstrapScript composes the bootstrap script: the server-injected config
// header (single-quoted) followed by the static body (bootstrapScriptBody).
func renderBootstrapScript(controllerBase, ghProxy, releaseBase string, cred *controller.OperatorCredential) string {
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
	fmt.Fprintf(&b, "CONTROLLER=%s\n", shQuote(controllerBase))
	fmt.Fprintf(&b, "GH_PROXY=%s\n", shQuote(ghProxy))
	fmt.Fprintf(&b, "RELEASE_BASE=%s\n", shQuote(releaseBase))
	fmt.Fprintf(&b, "OPERATOR_CRED_ALG=%s\n", shQuote(credAlg))
	fmt.Fprintf(&b, "OPERATOR_RPID=%s\n", shQuote(credRPID))
	fmt.Fprintf(&b, "OPERATOR_ORIGIN=%s\n", shQuote(credOrigin))
	fmt.Fprintf(&b, "OPERATOR_CRED_PEM=%s\n\n", shQuote(credPEM))
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
  x86_64|amd64)   ASSET="yaog-agent-linux-amd64" ;;
  aarch64|arm64)  ASSET="yaog-agent-linux-arm64" ;;
  i386|i686)      ASSET="yaog-agent-linux-386" ;;
  armv7l|armv7)   ASSET="yaog-agent-linux-armv7" ;;
  *) fail "unsupported architecture: $arch" ;;
esac

URL="${GH_PROXY}${RELEASE_BASE}/${ASSET}"
echo ">> downloading agent: $URL"
install -d -m 0755 /usr/local/bin
tmp_bin="$(mktemp)"
trap 'rm -f "$tmp_bin"' EXIT
# Pin the allowed protocols to https/http ONLY (one comma list — a SECOND '--proto
# =...' would REPLACE the allow-list rather than extend it, leaving http-only), so a
# redirect cannot pivot to file://, scp://, etc. --proto has been in curl since 7.20.
curl -fL --retry 3 --proto '=https,http' "$URL" -o "$tmp_bin"
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
  # shellcheck disable=SC2086
  /usr/local/bin/yaog-agent run --controller "$CONTROLLER" --node-id "$NODE_ID" $OP_FLAGS
fi
echo ">> bootstrap complete"
`
