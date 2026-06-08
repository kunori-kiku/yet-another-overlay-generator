package controller

import (
	"context"
	"errors"
	"testing"
)

// TestDefaultSettingsAndWithDefaults: defaults carry the release URL; WithDefaults
// fills only an empty release URL and leaves set fields alone.
func TestDefaultSettingsAndWithDefaults(t *testing.T) {
	d := DefaultSettings()
	if d.AgentReleaseBaseURL != DefaultAgentReleaseBaseURL {
		t.Errorf("DefaultSettings release URL = %q", d.AgentReleaseBaseURL)
	}
	if d.PublicAgentURL != "" || d.GithubProxy != "" {
		t.Errorf("DefaultSettings should have empty PublicAgentURL/GithubProxy, got %+v", d)
	}
	// WithDefaults fills an empty release URL.
	got := ControllerSettings{PublicAgentURL: "https://x", GithubProxy: "https://p/"}.WithDefaults()
	if got.AgentReleaseBaseURL != DefaultAgentReleaseBaseURL {
		t.Errorf("WithDefaults did not fill release URL: %q", got.AgentReleaseBaseURL)
	}
	if got.PublicAgentURL != "https://x" || got.GithubProxy != "https://p/" {
		t.Errorf("WithDefaults clobbered set fields: %+v", got)
	}
	// WithDefaults leaves a set release URL alone.
	got = ControllerSettings{AgentReleaseBaseURL: "https://mirror/dl"}.WithDefaults()
	if got.AgentReleaseBaseURL != "https://mirror/dl" {
		t.Errorf("WithDefaults overrode a set release URL: %q", got.AgentReleaseBaseURL)
	}
}

// TestStoreSettingsRoundTrip: Get(absent)->ErrNotFound, Put then Get round-trips, on
// both Store impls.
func TestStoreSettingsRoundTrip(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tn = TenantID("t1")

			if _, err := s.GetSettings(ctx, tn); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetSettings(absent) = %v, want ErrNotFound", err)
			}
			cs := ControllerSettings{
				PublicAgentURL:      "https://overlay.example.com",
				GithubProxy:         "https://gh-proxy.com/",
				AgentReleaseBaseURL: "https://github.com/o/r/releases/latest/download",
			}
			if err := s.PutSettings(ctx, tn, cs); err != nil {
				t.Fatalf("PutSettings: %v", err)
			}
			got, err := s.GetSettings(ctx, tn)
			if err != nil {
				t.Fatalf("GetSettings: %v", err)
			}
			if got != cs {
				t.Fatalf("round-trip mismatch: got %+v want %+v", got, cs)
			}
			// Replace.
			cs2 := ControllerSettings{PublicAgentURL: "https://b", AgentReleaseBaseURL: "https://b/dl"}
			if err := s.PutSettings(ctx, tn, cs2); err != nil {
				t.Fatalf("PutSettings(2): %v", err)
			}
			got, _ = s.GetSettings(ctx, tn)
			if got != cs2 {
				t.Fatalf("replace mismatch: got %+v want %+v", got, cs2)
			}
		})
	}
}
