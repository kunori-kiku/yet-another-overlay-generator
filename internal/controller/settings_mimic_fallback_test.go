package controller

import (
	"encoding/json"
	"testing"
)

// TestDefaultSettings_MimicFallbackNone pins D1: the shipped fleet-wide default is "none" (fail-closed).
func TestDefaultSettings_MimicFallbackNone(t *testing.T) {
	if DefaultMimicFallbackPolicy != "none" {
		t.Fatalf("DefaultMimicFallbackPolicy = %q, want none (D1 fail-closed)", DefaultMimicFallbackPolicy)
	}
	if got := DefaultSettings().MimicFallbackDefault; got != DefaultMimicFallbackPolicy {
		t.Fatalf("DefaultSettings().MimicFallbackDefault = %q, want %q", got, DefaultMimicFallbackPolicy)
	}
}

// TestWithDefaults_FillsLegacyEmpty: a legacy record with an empty policy gets the fail-closed
// default on WithDefaults (the stored value is made explicit), while a set value is preserved.
func TestWithDefaults_FillsLegacyEmpty(t *testing.T) {
	if got := (ControllerSettings{}).WithDefaults().MimicFallbackDefault; got != "none" {
		t.Fatalf("WithDefaults() on empty = %q, want none", got)
	}
	if got := (ControllerSettings{MimicFallbackDefault: "udp"}).WithDefaults().MimicFallbackDefault; got != "udp" {
		t.Fatalf("WithDefaults() must preserve a set policy, got %q", got)
	}
}

// TestSettings_BackCompatLoadNoField: a legacy settings.json with no mimic_fallback_default field
// loads as "" (then WithDefaults floors it to "none"); a saved record round-trips the value.
func TestSettings_BackCompatLoadNoField(t *testing.T) {
	legacy := `{"public_agent_url":"","github_proxy":"","agent_release_base_url":"https://example/dl"}`
	var cs ControllerSettings
	if err := json.Unmarshal([]byte(legacy), &cs); err != nil {
		t.Fatalf("unmarshal legacy settings: %v", err)
	}
	if cs.MimicFallbackDefault != "" {
		t.Fatalf("legacy settings MimicFallbackDefault = %q, want \"\"", cs.MimicFallbackDefault)
	}
	if cs.WithDefaults().MimicFallbackDefault != "none" {
		t.Fatalf("WithDefaults() must floor a legacy empty to none")
	}
}
