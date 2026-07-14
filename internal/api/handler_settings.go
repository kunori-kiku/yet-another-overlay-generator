package api

// handler_settings.go holds the operator-facing controller SETTINGS surface (plan-5.2),
// split out of handler_bootstrap.go (plan-9) so the read/write settings API is separately
// legible from the bootstrap-script rendering it feeds:
//   - operator routes GET/POST /settings — read/update the server-persisted controller
//     settings (public agent URL, GitHub proxy, agent release URL, mimic GitHub-.deb catalog,
//     signed agent self-update rollout pins, panel translucency, resource-history cap).
//
// GET reads the stored settings with defaults applied and needs NO identity; POST requires the
// operator identity, validates every field, persists, and audits. BOTH branches respond through
// the single settingsResponse constructor so a field added for GET cannot be forgotten for POST.
// The format validators POST runs (validateAbsoluteHTTPURL / validateMimicCatalog /
// validateAgentRollout) and the bootstrap script that consumes these settings still live in
// handler_bootstrap.go — same package, so this file calls them directly.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// settingsJSON is the wire form of the operator-editable controller settings.
// maxTelemetryHistoryCap bounds the operator-settable per-node resource-history sample cap (plan-2) so a
// typo cannot request an effectively unbounded history file. 1e6 samples ≈ 347 days at a 30s heartbeat.
const maxTelemetryHistoryCap = 1_000_000

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
	MimicDebs        map[string]model.MimicDebPin `json:"mimic_debs,omitempty"`
	// MimicFallbackDefault is the fleet-wide mimic→UDP fallback policy ("" / "udp" / "none"). plan-4.
	MimicFallbackDefault string `json:"mimic_fallback_default,omitempty"`
	// Signed agent self-update (plan-9, canary-then-fleet). All NON-SECRET pins; the agent
	// release base is the existing AgentReleaseBaseURL above. Empty target ⇒ no self-update.
	TargetAgentVersion    string                    `json:"target_agent_version,omitempty"`
	MinAgentVersion       string                    `json:"min_agent_version,omitempty"`
	AgentBins             map[string]model.Artifact `json:"agent_bins,omitempty"`
	AgentCanaryNodeIDs    []string                  `json:"agent_canary_node_ids,omitempty"`
	AgentRolloutFleetWide bool                      `json:"agent_rollout_fleet_wide,omitempty"`
	// TelemetryHistoryCap is the per-node resource-history sample cap (plan-2). A POINTER: nil ⇒ use the
	// default, an explicit value (incl. 0 = disable history) is honored. Validated >= 0 and <= a sanity
	// bound on POST.
	TelemetryHistoryCap *int `json:"telemetry_history_cap,omitempty"`
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
		MimicFallbackDefault:  cs.MimicFallbackDefault,
		TargetAgentVersion:    cs.TargetAgentVersion,
		MinAgentVersion:       cs.MinAgentVersion,
		AgentBins:             cs.AgentBins,
		AgentCanaryNodeIDs:    cs.AgentCanaryNodeIDs,
		AgentRolloutFleetWide: cs.AgentRolloutFleetWide,
		TelemetryHistoryCap:   cs.TelemetryHistoryCap,
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
			MimicFallbackDefault:  strings.TrimSpace(req.MimicFallbackDefault),
			TargetAgentVersion:    strings.TrimSpace(req.TargetAgentVersion),
			MinAgentVersion:       strings.TrimSpace(req.MinAgentVersion),
			AgentBins:             req.AgentBins,
			AgentCanaryNodeIDs:    req.AgentCanaryNodeIDs,
			AgentRolloutFleetWide: req.AgentRolloutFleetWide,
			TelemetryHistoryCap:   req.TelemetryHistoryCap,
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
		// Fleet-wide mimic-fallback default enum (plan-4): the raw submitted value must be ""
		// (inherit→WithDefaults fills "none") / "udp" / "none". A typo is rejected, not silently floored.
		switch strings.TrimSpace(req.MimicFallbackDefault) {
		case "", "udp", "none":
		default:
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "mimic_fallback_default"))
			return
		}
		// plan-2: the resource-history cap must be >= 0 (0 = disable) and within a sanity bound, so a
		// typo cannot request an effectively unbounded per-node history file.
		if req.TelemetryHistoryCap != nil {
			if c := *req.TelemetryHistoryCap; c < 0 || c > maxTelemetryHistoryCap {
				writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "telemetry_history_cap"))
				return
			}
		}
		if err := validateMimicCatalog(cs); err != nil {
			writeAPIError(w, err)
			return
		}
		if err := validateAgentRollout(cs, h.version); err != nil {
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
