// Package probepolicy owns the signed active-telemetry policy wire format and validation.
// Keeping this contract below artifacts, agent, and validator gives bundle construction and
// runtime activation one strict parser instead of parallel interpretations.
package probepolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

const (
	FileName                   = "telemetry.json"
	CurrentVersion             = 1
	MaxProbes                  = 16
	DefaultIntervalSeconds     = 60
	MinIntervalSeconds         = 30
	MaxIntervalSeconds         = 3600
	DefaultTimeoutMilliseconds = 2000
	MinTimeoutMilliseconds     = 100
	MaxTimeoutMilliseconds     = 5000
	maxPolicyBytes             = 64 << 10
)

var probeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,63}$`)

// Policy is the canonical, versioned bundle member. A version at the root lets a future URL
// probe introduce its own typed fields without making old agents guess at new semantics.
type Policy struct {
	Version int                    `json:"version"`
	Probes  []model.TelemetryProbe `json:"probes"`
}

// Marshal emits canonical compact JSON for a non-empty probe set. Callers omit telemetry.json
// entirely when the node has no probes, preserving historical bundle bytes.
func Marshal(probes []model.TelemetryProbe) ([]byte, error) {
	if len(probes) == 0 {
		return nil, nil
	}
	if err := Validate(probes); err != nil {
		return nil, err
	}
	return json.Marshal(Policy{Version: CurrentVersion, Probes: probes})
}

// Parse strictly decodes and validates a telemetry.json member. Unknown fields and trailing JSON
// are rejected so an old agent never silently ignores a security-relevant field added by a newer
// controller.
func Parse(data []byte) (*Policy, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("telemetry policy is empty")
	}
	if len(data) > maxPolicyBytes {
		return nil, fmt.Errorf("telemetry policy exceeds %d bytes", maxPolicyBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var policy Policy
	if err := dec.Decode(&policy); err != nil {
		return nil, fmt.Errorf("parse telemetry policy: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse telemetry policy: trailing JSON value")
		}
		return nil, fmt.Errorf("parse telemetry policy trailing data: %w", err)
	}
	if policy.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported telemetry policy version %d", policy.Version)
	}
	if len(policy.Probes) == 0 {
		return nil, fmt.Errorf("telemetry policy has no probes")
	}
	if err := Validate(policy.Probes); err != nil {
		return nil, err
	}
	return &policy, nil
}

// Validate checks the topology and runtime form of an active probe set.
func Validate(probes []model.TelemetryProbe) error {
	if len(probes) > MaxProbes {
		return fmt.Errorf("too many telemetry probes: got %d, max %d", len(probes), MaxProbes)
	}
	seen := make(map[string]struct{}, len(probes))
	for i, probe := range probes {
		if !probeIDPattern.MatchString(probe.ID) {
			return fmt.Errorf("probe %d has invalid id %q", i, probe.ID)
		}
		if _, exists := seen[probe.ID]; exists {
			return fmt.Errorf("probe %d duplicates id %q", i, probe.ID)
		}
		seen[probe.ID] = struct{}{}
		if !ValidHost(probe.Host) {
			return fmt.Errorf("probe %q has invalid host %q", probe.ID, probe.Host)
		}

		interval := EffectiveIntervalSeconds(probe)
		if interval < MinIntervalSeconds || interval > MaxIntervalSeconds {
			return fmt.Errorf("probe %q interval %d is outside %d..%d seconds", probe.ID, interval, MinIntervalSeconds, MaxIntervalSeconds)
		}
		timeout := EffectiveTimeoutMilliseconds(probe)
		if timeout < MinTimeoutMilliseconds || timeout > MaxTimeoutMilliseconds {
			return fmt.Errorf("probe %q timeout %d is outside %d..%d milliseconds", probe.ID, timeout, MinTimeoutMilliseconds, MaxTimeoutMilliseconds)
		}
		if timeout >= interval*1000 {
			return fmt.Errorf("probe %q timeout must be shorter than its interval", probe.ID)
		}

		switch probe.Type {
		case model.TelemetryProbeICMP:
			if probe.Port != 0 {
				return fmt.Errorf("ICMP probe %q must not set a port", probe.ID)
			}
		case model.TelemetryProbeTCP:
			if probe.Port < 1 || probe.Port > 65535 {
				return fmt.Errorf("TCP probe %q requires one port in 1..65535", probe.ID)
			}
		default:
			return fmt.Errorf("probe %q has unsupported type %q", probe.ID, probe.Type)
		}
	}
	return nil
}

// ValidHost accepts a bare IPv4/IPv6 literal or an ASCII DNS hostname (including a single-label
// hostname and an optional final root dot). It deliberately rejects URL syntax, bracketed URL
// hosts, paths, ports, query strings, whitespace, and shell metacharacters. International names
// can be supplied in their explicit ASCII/Punycode form.
func ValidHost(host string) bool {
	if host == "" || host != strings.TrimSpace(host) || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" || len(name) > 253 || strings.ContainsAny(name, "/:?#[\\]@") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func EffectiveIntervalSeconds(probe model.TelemetryProbe) int {
	if probe.IntervalSeconds == 0 {
		return DefaultIntervalSeconds
	}
	return probe.IntervalSeconds
}

func EffectiveTimeoutMilliseconds(probe model.TelemetryProbe) int {
	if probe.TimeoutMilliseconds == 0 {
		return DefaultTimeoutMilliseconds
	}
	return probe.TimeoutMilliseconds
}
