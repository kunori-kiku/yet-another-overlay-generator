package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
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

	// Mimic release base (plan-9): the default carries the upstream mimic latest-release base, and
	// WithDefaults fills an empty one (legacy load) but never overrides an operator-set base.
	if d.MimicReleaseBase != DefaultMimicReleaseBase {
		t.Errorf("DefaultSettings mimic release base = %q, want %q", d.MimicReleaseBase, DefaultMimicReleaseBase)
	}
	if legacyBase := (ControllerSettings{}.WithDefaults()).MimicReleaseBase; legacyBase != DefaultMimicReleaseBase {
		t.Errorf("WithDefaults did not fill empty mimic release base: %q", legacyBase)
	}
	if setBase := (ControllerSettings{MimicReleaseBase: "https://mirror/m"}.WithDefaults()).MimicReleaseBase; setBase != "https://mirror/m" {
		t.Errorf("WithDefaults overrode a set mimic release base: %q", setBase)
	}
	// The default MUST use the "releases/latest/download" alias so a version-bearing assist can
	// tag-pin it (resolveReleaseBase only rewrites a base ending in that alias). A regression to a
	// pinned "download/v0.1.0" base would silently break mimic version-pinning.
	if !strings.HasSuffix(DefaultMimicReleaseBase, "releases/latest/download") {
		t.Errorf("DefaultMimicReleaseBase must end with the latest-download alias for tag-pinning, got %q", DefaultMimicReleaseBase)
	}

	// Translucency: default ON; WithDefaults fills a nil (legacy-load) value with true but
	// preserves an explicit false (the absent-vs-false migration).
	if d.Translucency == nil || !*d.Translucency {
		t.Errorf("DefaultSettings translucency should be true, got %v", d.Translucency)
	}
	legacy := ControllerSettings{}.WithDefaults()
	if legacy.Translucency == nil || !*legacy.Translucency {
		t.Errorf("WithDefaults(nil translucency) should default to true, got %v", legacy.Translucency)
	}
	off := false
	explicit := ControllerSettings{Translucency: &off}.WithDefaults()
	if explicit.Translucency == nil || *explicit.Translucency {
		t.Errorf("WithDefaults must keep an explicit false, got %v", explicit.Translucency)
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
				MimicVersion:        "0.1.0",
				MimicReleaseBase:    "https://github.com/hack3ric/mimic/releases/download/v0.1.0",
				MimicDebs: map[string]model.MimicDebPin{
					"bookworm-amd64": {Asset: "mimic_0.1.0_amd64.deb", SHA256: strings.Repeat("a", 64)},
				},
			}
			if err := s.PutSettings(ctx, tn, cs); err != nil {
				t.Fatalf("PutSettings: %v", err)
			}
			got, err := s.GetSettings(ctx, tn)
			if err != nil {
				t.Fatalf("GetSettings: %v", err)
			}
			if !reflect.DeepEqual(got, cs) {
				t.Fatalf("round-trip mismatch: got %+v want %+v", got, cs)
			}
			// Isolation: mutating the caller's MimicDebs map after Put must NOT change the stored
			// value (the store deep-copies via Clone; the map is a shared reference otherwise).
			cs.MimicDebs["bookworm-amd64"] = model.MimicDebPin{Asset: "evil.deb", SHA256: "x"}
			got2, _ := s.GetSettings(ctx, tn)
			if got2.MimicDebs["bookworm-amd64"].Asset != "mimic_0.1.0_amd64.deb" {
				t.Fatalf("stored MimicDebs aliased the caller's map: got %+v", got2.MimicDebs)
			}
			// Replace.
			cs2 := ControllerSettings{PublicAgentURL: "https://b", AgentReleaseBaseURL: "https://b/dl"}
			if err := s.PutSettings(ctx, tn, cs2); err != nil {
				t.Fatalf("PutSettings(2): %v", err)
			}
			got, _ = s.GetSettings(ctx, tn)
			if !reflect.DeepEqual(got, cs2) {
				t.Fatalf("replace mismatch: got %+v want %+v", got, cs2)
			}
		})
	}
}
