package controller

// settings.go holds the defaults for the operator-editable controller settings that
// drive the one-shot agent bootstrap (plan-5.2). The ControllerSettings record type
// and its Store methods live in store.go beside the other persisted records.

// DefaultAgentReleaseBaseURL is where the bootstrap downloads the per-arch yaog-agent
// binary by default: the project's "latest release" assets (the /latest/download alias
// always resolves to the newest published release). A deployment can override it (an
// internal mirror, a pinned tag) via the operator settings.
const DefaultAgentReleaseBaseURL = "https://github.com/kunori-kiku/yet-another-overlay-generator/releases/latest/download"

// DefaultMimicReleaseBase is the upstream mimic project's "latest release" asset base. mimic is a
// THIRD-PARTY project (github.com/hack3ric/mimic), so this points at its releases, NOT YAOG's. It is
// the default the mimic GitHub-.deb catalog assist fetches .deb .sha256 sidecars from when the
// operator has not set their own MimicReleaseBase; like DefaultAgentReleaseBaseURL it uses the
// "releases/latest/download" alias so a version-bearing assist can pin it to a tag (resolveReleaseBase).
// NON-SECRET (a public URL). Upstream does not currently publish per-distro .deb assets, so the assist
// may legitimately miss rows (handled best-effort, per-row) — the value is still a working, helpful
// pre-fill that removes the assistNeedsBase hard error.
const DefaultMimicReleaseBase = "https://github.com/hack3ric/mimic/releases/latest/download"

// DefaultMimicFallbackPolicy is the shipped fleet-wide mimic-fallback default: "none" (fail-closed).
// A fallback to plain UDP DE-CLOAKS the link, so the conservative default preserves mimic's
// censorship-evasion guarantee; the operator opts in fleet-wide ("udp") or per-link (D1). FLAG FOR
// OWNER CONFIRM at review (outline D1 marks this an inferred default).
const DefaultMimicFallbackPolicy = "none"

// boolPtr returns a pointer to v (for the optional Translucency field).
func boolPtr(v bool) *bool { return &v }

// DefaultSettings returns the controller settings applied when none have been saved:
// no public agent URL yet (the operator must set it), GitHub proxy OFF, the project's
// latest-release asset URL as the agent binary source, the upstream mimic latest-release
// base as the mimic .deb assist source, and translucency ON (matching the panel's default
// appearance).
func DefaultSettings() ControllerSettings {
	return ControllerSettings{
		PublicAgentURL:       "",
		GithubProxy:          "",
		AgentReleaseBaseURL:  DefaultAgentReleaseBaseURL,
		MimicReleaseBase:     DefaultMimicReleaseBase,
		MimicFallbackDefault: DefaultMimicFallbackPolicy, // "none" — fail-closed (D1)
		Translucency:         boolPtr(true),
	}
}

// WithDefaults returns s with any empty AgentReleaseBaseURL / MimicReleaseBase filled from their
// defaults and a nil Translucency (a legacy record predating the field) defaulted to true — so a
// partially-saved record still yields a usable agent source, a usable mimic assist source, and the
// default-on appearance.
func (s ControllerSettings) WithDefaults() ControllerSettings {
	if s.AgentReleaseBaseURL == "" {
		s.AgentReleaseBaseURL = DefaultAgentReleaseBaseURL
	}
	// A legacy record predating the mimic default (empty base) gets the upstream latest-release base
	// so the .deb catalog assist has a working pre-fill (back-compat: a base with no MimicDebs is
	// inert — no GitHub fallback is emitted, mirroring the agent base with no AgentBins).
	if s.MimicReleaseBase == "" {
		s.MimicReleaseBase = DefaultMimicReleaseBase
	}
	// A legacy record (empty) gets the fail-closed default explicitly on save (resolveMimicFallback
	// already floors "" to "none", but make the stored value definite, D1).
	if s.MimicFallbackDefault == "" {
		s.MimicFallbackDefault = DefaultMimicFallbackPolicy
	}
	if s.Translucency == nil {
		s.Translucency = boolPtr(true)
	}
	return s
}
