package controller

// settings.go holds the defaults for the operator-editable controller settings that
// drive the one-shot agent bootstrap (plan-5.2). The ControllerSettings record type
// and its Store methods live in store.go beside the other persisted records.

// DefaultAgentReleaseBaseURL is where the bootstrap downloads the per-arch yaog-agent
// binary by default: the project's "latest release" assets (the /latest/download alias
// always resolves to the newest published release). A deployment can override it (an
// internal mirror, a pinned tag) via the operator settings.
const DefaultAgentReleaseBaseURL = "https://github.com/kunori-kiku/yet-another-overlay-generator/releases/latest/download"

// DefaultSettings returns the controller settings applied when none have been saved:
// no public agent URL yet (the operator must set it), GitHub proxy OFF, and the
// project's latest-release asset URL as the agent binary source.
func DefaultSettings() ControllerSettings {
	return ControllerSettings{
		PublicAgentURL:      "",
		GithubProxy:         "",
		AgentReleaseBaseURL: DefaultAgentReleaseBaseURL,
	}
}

// WithDefaults returns s with any empty AgentReleaseBaseURL filled from the default,
// so a partially-saved record still yields a usable agent binary source.
func (s ControllerSettings) WithDefaults() ControllerSettings {
	if s.AgentReleaseBaseURL == "" {
		s.AgentReleaseBaseURL = DefaultAgentReleaseBaseURL
	}
	return s
}
