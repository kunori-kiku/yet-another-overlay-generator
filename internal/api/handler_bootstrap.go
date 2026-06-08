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
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// settingsJSON is the wire form of the operator-editable controller settings.
type settingsJSON struct {
	PublicAgentURL      string `json:"public_agent_url"`
	GithubProxy         string `json:"github_proxy"`
	AgentReleaseBaseURL string `json:"agent_release_base_url"`
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
			writeError(w, http.StatusInternalServerError, "failed to read settings")
			return
		}
		writeJSON(w, http.StatusOK, settingsJSON{cs.PublicAgentURL, cs.GithubProxy, cs.AgentReleaseBaseURL})

	case http.MethodPost:
		tenant, actor, ok := identity(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "missing authenticated identity")
			return
		}
		var req settingsJSON
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		cs := controller.ControllerSettings{
			PublicAgentURL:      strings.TrimSpace(req.PublicAgentURL),
			GithubProxy:         strings.TrimSpace(req.GithubProxy),
			AgentReleaseBaseURL: strings.TrimSpace(req.AgentReleaseBaseURL),
		}.WithDefaults()
		if cs.PublicAgentURL != "" {
			if err := validateAbsoluteHTTPURL(cs.PublicAgentURL); err != nil {
				writeError(w, http.StatusBadRequest, "public_agent_url: "+err.Error())
				return
			}
		}
		if cs.GithubProxy != "" {
			if err := validateAbsoluteHTTPURL(cs.GithubProxy); err != nil {
				writeError(w, http.StatusBadRequest, "github_proxy: "+err.Error())
				return
			}
		}
		if err := validateAbsoluteHTTPURL(cs.AgentReleaseBaseURL); err != nil {
			writeError(w, http.StatusBadRequest, "agent_release_base_url: "+err.Error())
			return
		}
		if err := h.store.PutSettings(r.Context(), tenant, cs); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save settings")
			return
		}
		_, _ = h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
			Timestamp: time.Now().UTC(),
			Actor:     "operator:" + actor,
			Action:    "settings-update",
		})
		writeJSON(w, http.StatusOK, settingsJSON{cs.PublicAgentURL, cs.GithubProxy, cs.AgentReleaseBaseURL})

	default:
		writeError(w, http.StatusMethodNotAllowed, "only GET and POST are supported")
	}
}

// HandleBootstrap serves the bash one-shot bootstrap script (agent port, NO auth).
// It bakes the server-side settings (controller URL incl. the secret path prefix, the
// GitHub proxy, the agent release URL) and — when the keystone is ON — the pinned
// off-host operator credential into the script as DEFAULTS; the node operator supplies
// the single-use enrollment token via a flag.
func (h *ControllerHandler) HandleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET is supported")
		return
	}
	cs, err := h.loadSettings(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read settings")
		return
	}
	// The agent's --controller is scheme://host[:port] + the controller's secret path
	// prefix (the agent appends /api/v1/controller/ itself). The server knows its own
	// prefix, so it composes the full base here.
	controllerBase := strings.TrimRight(cs.PublicAgentURL, "/") + h.pathPrefix

	// Keystone: bake the pinned off-host operator credential (public only) so the node
	// enforces membership. ONLY a genuine ErrNotFound means keystone OFF; any other
	// store error must fail loud — silently emitting a keystone-OFF script when the
	// keystone is actually ON would ship a node that does not verify membership.
	var cred *controller.OperatorCredential
	if oc, err := h.store.GetOperatorCredential(r.Context(), h.tenant); err == nil {
		cred = &oc
	} else if !errors.Is(err, controller.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to read operator credential")
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
	b.WriteString("#   bash <(curl -fsSL <public-agent-url>/api/v1/controller/bootstrap) --token <T> --node-id <ID>\n")
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
# Pin the allowed protocols (http/https only) so a redirect cannot pivot to file://,
# scp://, etc. --proto has been in curl since 7.20; no scheme-widening fallback.
curl -fL --retry 3 --proto '=https' --proto '=http' "$URL" -o "$tmp_bin"
install -m 0755 "$tmp_bin" /usr/local/bin/yaog-agent

# Build the keystone (off-host operator credential) flags when the controller baked a
# credential into this script (keystone ON). The PEM is public; written 0600 anyway.
OP_FLAGS=""
if [ -n "$OPERATOR_CRED_PEM" ]; then
  install -d -m 0700 /etc/wireguard
  printf '%s\n' "$OPERATOR_CRED_PEM" > /etc/wireguard/operator-cred.pem
  chmod 0600 /etc/wireguard/operator-cred.pem
  OP_FLAGS="--operator-cred /etc/wireguard/operator-cred.pem --operator-cred-alg ${OPERATOR_CRED_ALG}"
  [ -n "$OPERATOR_RPID" ]   && OP_FLAGS="$OP_FLAGS --operator-rpid ${OPERATOR_RPID}"
  [ -n "$OPERATOR_ORIGIN" ] && OP_FLAGS="$OP_FLAGS --operator-origin ${OPERATOR_ORIGIN}"
fi

echo ">> enrolling node: $NODE_ID"
/usr/local/bin/yaog-agent enroll --controller "$CONTROLLER" --node-id "$NODE_ID" --token "$TOKEN"

if [ "$DAEMON" -eq 1 ]; then
  echo ">> installing systemd daemon (continuous; future deploys auto-apply)"
  cat > /etc/systemd/system/yaog-agent.service <<UNIT
[Unit]
Description=YAOG overlay agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/yaog-agent run --controller "${CONTROLLER}" --node-id "${NODE_ID}" --daemon ${OP_FLAGS}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now yaog-agent.service
  echo ">> agent installed as a daemon; the current generation applies on the next poll"
else
  echo ">> applying once"
  # shellcheck disable=SC2086
  /usr/local/bin/yaog-agent run --controller "$CONTROLLER" --node-id "$NODE_ID" $OP_FLAGS
fi
echo ">> bootstrap complete"
`
