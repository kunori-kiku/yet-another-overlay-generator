// Package probepolicy owns the signed active-telemetry policy wire format and validation.
// Keeping this contract below artifacts, agent, and validator gives bundle construction and
// runtime activation one strict parser instead of parallel interpretations.
package probepolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrycap"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
)

const (
	FileName                   = "telemetry.json"
	CurrentVersion             = 1
	SuccessorFileName          = "telemetry-policy.json"
	SuccessorVersion           = 2
	MaxProbes                  = 16
	DefaultIntervalSeconds     = 60
	MinIntervalSeconds         = 30
	MaxIntervalSeconds         = 3600
	DefaultTimeoutMilliseconds = 2000
	MinTimeoutMilliseconds     = 100
	MaxTimeoutMilliseconds     = 5000
	MaxNameRunes               = 128
	DefaultExpectedStatus      = 200
	MaxURLBytes                = 2048
	// MaxEncodedURLPolicyBytes reserves half of the authenticated telemetry metrics envelope for
	// the exact URL strings repeated in mandatory latest-result rows. The remaining half covers the
	// bounded row metadata and other core metrics; recent attempts and peer detail are independently
	// shed by adaptive admission. Count Go's actual JSON encoding so HTML-sensitive query bytes such
	// as '&' cannot expand a valid signed policy into an unreportable heartbeat.
	MaxEncodedURLPolicyBytes = telemetryprotocol.MaxMetricsBytes / 2
	maxPolicyBytes           = 64 << 10
)

var probeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,63}$`)

var errPolicyRuntimeOnly = errors.New("probepolicy: Policy is a parsed runtime view; use Marshal for telemetry.json")
var errSuccessorPolicyRuntimeOnly = errors.New("probepolicy: SuccessorPolicy is a runtime view; use MarshalSuccessor for telemetry-policy.json")

// Policy is the parsed runtime view of the canonical, versioned bundle member. It deliberately
// cannot be marshaled directly; telemetry.json is emitted only through Marshal's private wire DTO.
type Policy struct {
	Version int
	Probes  []model.TelemetryProbe
}

type DeviceMode string

const DeviceModeAllEligibleV1 DeviceMode = "all-eligible-v1"

type DevicePolicy struct {
	Mode DeviceMode `json:"mode"`
}

// SuccessorPolicy is the unified runtime view used for the separately named version-2 member and
// for reading the one durable last-known-good policy field. It is serialized only through the
// private strict wire DTO in MarshalSuccessor so controller-only probe metadata cannot leak on-node.
type SuccessorPolicy struct {
	Version int                    `json:"version"`
	Probes  []model.TelemetryProbe `json:"probes,omitempty"`
	Devices *DevicePolicy          `json:"devices,omitempty"`
}

func (SuccessorPolicy) MarshalJSON() ([]byte, error) {
	return nil, errSuccessorPolicyRuntimeOnly
}

// MarshalJSON blocks accidental serialization of the runtime view, including controller-only
// fields carried by model.TelemetryProbe. Both Policy values and pointers use this value receiver.
func (Policy) MarshalJSON() ([]byte, error) {
	return nil, errPolicyRuntimeOnly
}

// executableProbeWire is the strict, version-1 on-node policy shape. Do not marshal or decode
// model.TelemetryProbe directly: that topology model also carries optional controller-only display
// metadata such as Name. Existing rc.9-rc.11 agents reject unknown fields, and new agents must keep
// rejecting a handcrafted display field inside the executable policy rather than silently ignoring
// a future security-relevant extension.
type executableProbeWire struct {
	ID                  string `json:"id"`
	Type                string `json:"type"`
	Host                string `json:"host"`
	Port                int    `json:"port,omitempty"`
	IntervalSeconds     int    `json:"interval_seconds,omitempty"`
	TimeoutMilliseconds int    `json:"timeout_milliseconds,omitempty"`
}

type policyWire struct {
	Version int                   `json:"version"`
	Probes  []executableProbeWire `json:"probes"`
}

type devicePolicyWire struct {
	Mode DeviceMode `json:"mode"`
}

type successorPolicyWire struct {
	Version int                  `json:"version"`
	Probes  []successorProbeWire `json:"probes,omitempty"`
	Devices *devicePolicyWire    `json:"devices,omitempty"`
}

// successorProbeWire is deliberately separate from executableProbeWire. URL and expected-status
// fields are successor-only and must never touch the frozen telemetry.json v1 DTO.
type successorProbeWire struct {
	ID                  string `json:"id"`
	Type                string `json:"type"`
	Host                string `json:"host,omitempty"`
	Port                int    `json:"port,omitempty"`
	URL                 string `json:"url,omitempty"`
	ExpectedStatus      int    `json:"expected_status,omitempty"`
	IntervalSeconds     int    `json:"interval_seconds,omitempty"`
	TimeoutMilliseconds int    `json:"timeout_milliseconds,omitempty"`
}

func successorProbe(probe model.TelemetryProbe) successorProbeWire {
	return successorProbeWire{
		ID:                  probe.ID,
		Type:                probe.Type,
		Host:                probe.Host,
		Port:                probe.Port,
		URL:                 probe.URL,
		ExpectedStatus:      canonicalExpectedStatus(probe),
		IntervalSeconds:     probe.IntervalSeconds,
		TimeoutMilliseconds: probe.TimeoutMilliseconds,
	}
}

func successorTopologyProbe(probe successorProbeWire) model.TelemetryProbe {
	return model.TelemetryProbe{
		ID:                  probe.ID,
		Type:                probe.Type,
		Host:                probe.Host,
		Port:                probe.Port,
		URL:                 probe.URL,
		ExpectedStatus:      probe.ExpectedStatus,
		IntervalSeconds:     probe.IntervalSeconds,
		TimeoutMilliseconds: probe.TimeoutMilliseconds,
	}
}

func executableProbe(probe model.TelemetryProbe) executableProbeWire {
	return executableProbeWire{
		ID:                  probe.ID,
		Type:                probe.Type,
		Host:                probe.Host,
		Port:                probe.Port,
		IntervalSeconds:     probe.IntervalSeconds,
		TimeoutMilliseconds: probe.TimeoutMilliseconds,
	}
}

func topologyProbe(probe executableProbeWire) model.TelemetryProbe {
	return model.TelemetryProbe{
		ID:                  probe.ID,
		Type:                probe.Type,
		Host:                probe.Host,
		Port:                probe.Port,
		IntervalSeconds:     probe.IntervalSeconds,
		TimeoutMilliseconds: probe.TimeoutMilliseconds,
	}
}

// Marshal emits canonical compact JSON for a non-empty probe set. Callers omit telemetry.json
// entirely when the node has no probes, preserving historical bundle bytes.
func Marshal(probes []model.TelemetryProbe) ([]byte, error) {
	if len(probes) == 0 {
		return nil, nil
	}
	if err := validateLegacyProbes(probes); err != nil {
		return nil, err
	}
	wireProbes := make([]executableProbeWire, len(probes))
	for i := range probes {
		wireProbes[i] = executableProbe(probes[i])
	}
	return json.Marshal(policyWire{Version: CurrentVersion, Probes: wireProbes})
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
	var wire policyWire
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("parse telemetry policy: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse telemetry policy: trailing JSON value")
		}
		return nil, fmt.Errorf("parse telemetry policy trailing data: %w", err)
	}
	if wire.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported telemetry policy version %d", wire.Version)
	}
	if len(wire.Probes) == 0 {
		return nil, fmt.Errorf("telemetry policy has no probes")
	}
	policy := &Policy{Version: wire.Version, Probes: make([]model.TelemetryProbe, len(wire.Probes))}
	for i := range wire.Probes {
		policy.Probes[i] = topologyProbe(wire.Probes[i])
	}
	if err := validateLegacyProbes(policy.Probes); err != nil {
		return nil, err
	}
	return policy, nil
}

// MarshalSuccessor emits the strict, compact telemetry-policy.json version-2 member. Version zero
// means "use the current successor" for callers constructing a fresh runtime value; any other
// non-current version is rejected rather than silently rewritten.
func MarshalSuccessor(policy SuccessorPolicy) ([]byte, error) {
	if policy.Version != 0 && policy.Version != SuccessorVersion {
		return nil, fmt.Errorf("unsupported successor telemetry policy version %d", policy.Version)
	}
	if len(policy.Probes) == 0 && policy.Devices == nil {
		return nil, fmt.Errorf("successor telemetry policy has no executable features")
	}
	if err := Validate(policy.Probes); err != nil {
		return nil, err
	}
	if err := ValidateDevicePolicy(policy.Devices); err != nil {
		return nil, err
	}
	wire := successorPolicyWire{Version: SuccessorVersion}
	if len(policy.Probes) > 0 {
		wire.Probes = make([]successorProbeWire, len(policy.Probes))
		for i := range policy.Probes {
			wire.Probes[i] = successorProbe(policy.Probes[i])
		}
	}
	if policy.Devices != nil {
		wire.Devices = &devicePolicyWire{Mode: policy.Devices.Mode}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal successor telemetry policy: %w", err)
	}
	if len(raw) > maxPolicyBytes {
		return nil, fmt.Errorf("successor telemetry policy exceeds %d bytes", maxPolicyBytes)
	}
	return raw, nil
}

// ParseSuccessor strictly decodes telemetry-policy.json. Unknown fields and trailing JSON remain
// fail-closed, independently of the frozen telemetry.json v1 parser above.
func ParseSuccessor(data []byte) (*SuccessorPolicy, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("successor telemetry policy is empty")
	}
	if len(data) > maxPolicyBytes {
		return nil, fmt.Errorf("successor telemetry policy exceeds %d bytes", maxPolicyBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var wire successorPolicyWire
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("parse successor telemetry policy: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse successor telemetry policy: trailing JSON value")
		}
		return nil, fmt.Errorf("parse successor telemetry policy trailing data: %w", err)
	}
	if wire.Version != SuccessorVersion {
		return nil, fmt.Errorf("unsupported successor telemetry policy version %d", wire.Version)
	}
	if len(wire.Probes) == 0 && wire.Devices == nil {
		return nil, fmt.Errorf("successor telemetry policy has no executable features")
	}
	policy := &SuccessorPolicy{Version: wire.Version}
	if len(wire.Probes) > 0 {
		policy.Probes = make([]model.TelemetryProbe, len(wire.Probes))
		for i := range wire.Probes {
			policy.Probes[i] = successorTopologyProbe(wire.Probes[i])
		}
	}
	if wire.Devices != nil {
		policy.Devices = &DevicePolicy{Mode: wire.Devices.Mode}
	}
	if err := Validate(policy.Probes); err != nil {
		return nil, err
	}
	if err := ValidateDevicePolicy(policy.Devices); err != nil {
		return nil, err
	}
	return policy, nil
}

// ParseActive dispatches a bounded durable policy by its root version, then delegates to the exact
// strict parser for that version. It does not weaken either file contract or accept a filename.
func ParseActive(data []byte) (*SuccessorPolicy, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("telemetry policy is empty")
	}
	if len(data) > maxPolicyBytes {
		return nil, fmt.Errorf("telemetry policy exceeds %d bytes", maxPolicyBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var root struct {
		Version int `json:"version"`
	}
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse telemetry policy version: %w", err)
	}
	switch root.Version {
	case CurrentVersion:
		policy, err := Parse(data)
		if err != nil {
			return nil, err
		}
		return &SuccessorPolicy{Version: policy.Version, Probes: policy.Probes}, nil
	case SuccessorVersion:
		return ParseSuccessor(data)
	default:
		return nil, fmt.Errorf("unsupported telemetry policy version %d", root.Version)
	}
}

// ValidateDevicePolicy is the canonical topology/runtime validator for the successor device
// selector. Schema validation calls the same definition so malformed drafts fail as structured
// validation errors instead of reaching render as an internal error.
func ValidateDevicePolicy(policy *DevicePolicy) error {
	if policy == nil {
		return nil
	}
	if policy.Mode != DeviceModeAllEligibleV1 {
		return fmt.Errorf("unsupported telemetry device mode %q", policy.Mode)
	}
	return nil
}

// RequiresSuccessor reports whether a node needs the separately named version-2 policy member.
func RequiresSuccessor(node model.Node) bool {
	if node.TelemetryDevices != nil {
		return true
	}
	for _, probe := range node.TelemetryProbes {
		if probe.Type == model.TelemetryProbeURL {
			return true
		}
	}
	return false
}

// RequiredCapabilities returns the exact authenticated agent capabilities needed to deploy a node's
// successor policy. The list is sorted so installer requirements and readiness diagnostics are stable.
func RequiredCapabilities(node model.Node) []string {
	if !RequiresSuccessor(node) {
		return nil
	}
	capabilities := []string{telemetrycap.PolicyV2}
	for _, probe := range node.TelemetryProbes {
		if probe.Type == model.TelemetryProbeURL {
			capabilities = append(capabilities, telemetrycap.URLV1)
			break
		}
	}
	if node.TelemetryDevices != nil {
		capabilities = append(capabilities, telemetrycap.DeviceV1)
	}
	sort.Strings(capabilities)
	return capabilities
}

// ProjectLegacy removes only successor-only fields from an already-copied topology node. ICMP/TCP
// probes remain available to the frozen telemetry.json v1 path during the upgrade-first deployment.
func ProjectLegacy(node *model.Node) {
	if node == nil {
		return
	}
	node.TelemetryDevices = nil
	legacy := make([]model.TelemetryProbe, 0, len(node.TelemetryProbes))
	for _, probe := range node.TelemetryProbes {
		if probe.Type != model.TelemetryProbeURL {
			legacy = append(legacy, probe)
		}
	}
	node.TelemetryProbes = legacy
}

func validateLegacyProbes(probes []model.TelemetryProbe) error {
	if err := Validate(probes); err != nil {
		return err
	}
	for _, probe := range probes {
		if probe.Type == model.TelemetryProbeURL || probe.URL != "" || probe.ExpectedStatus != 0 {
			return fmt.Errorf("probe %q requires successor telemetry policy", probe.ID)
		}
	}
	return nil
}

// Validate checks the topology and runtime form of an active probe set.
func Validate(probes []model.TelemetryProbe) error {
	if len(probes) > MaxProbes {
		return fmt.Errorf("too many telemetry probes: got %d, max %d", len(probes), MaxProbes)
	}
	seen := make(map[string]struct{}, len(probes))
	encodedURLPolicyBytes := 0
	for i, probe := range probes {
		if !probeIDPattern.MatchString(probe.ID) {
			return fmt.Errorf("probe %d has invalid id %q", i, probe.ID)
		}
		if err := ValidateName(probe.Name); err != nil {
			return fmt.Errorf("probe %q has invalid name: %w", probe.ID, err)
		}
		if _, exists := seen[probe.ID]; exists {
			return fmt.Errorf("probe %d duplicates id %q", i, probe.ID)
		}
		seen[probe.ID] = struct{}{}
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
			if !ValidHost(probe.Host) {
				return fmt.Errorf("probe %q has invalid host %q", probe.ID, probe.Host)
			}
			if probe.Port != 0 {
				return fmt.Errorf("ICMP probe %q must not set a port", probe.ID)
			}
			if probe.URL != "" || probe.ExpectedStatus != 0 {
				return fmt.Errorf("ICMP probe %q must not set URL fields", probe.ID)
			}
		case model.TelemetryProbeTCP:
			if !ValidHost(probe.Host) {
				return fmt.Errorf("probe %q has invalid host %q", probe.ID, probe.Host)
			}
			if probe.Port < 1 || probe.Port > 65535 {
				return fmt.Errorf("TCP probe %q requires one port in 1..65535", probe.ID)
			}
			if probe.URL != "" || probe.ExpectedStatus != 0 {
				return fmt.Errorf("TCP probe %q must not set URL fields", probe.ID)
			}
		case model.TelemetryProbeURL:
			if probe.Host != "" || probe.Port != 0 {
				return fmt.Errorf("URL probe %q must not set host or port fields", probe.ID)
			}
			if err := ValidateURL(probe.URL); err != nil {
				return fmt.Errorf("URL probe %q has invalid URL: %w", probe.ID, err)
			}
			encodedURL, err := json.Marshal(probe.URL)
			if err != nil {
				return fmt.Errorf("URL probe %q encode URL: %w", probe.ID, err)
			}
			encodedURLPolicyBytes += len(encodedURL)
			if encodedURLPolicyBytes > MaxEncodedURLPolicyBytes {
				return fmt.Errorf("URL probes require %d encoded destination bytes, max %d",
					encodedURLPolicyBytes, MaxEncodedURLPolicyBytes)
			}
			status := EffectiveExpectedStatus(probe)
			if status < 100 || status > 599 {
				return fmt.Errorf("URL probe %q expected status %d is outside 100..599", probe.ID, status)
			}
		default:
			return fmt.Errorf("probe %q has unsupported type %q", probe.ID, probe.Type)
		}
	}
	return nil
}

// EffectiveExpectedStatus returns the exact URL response code considered successful. Zero is the
// topology shorthand for the default, while successor executable policy writes 200 explicitly.
func EffectiveExpectedStatus(probe model.TelemetryProbe) int {
	if probe.ExpectedStatus == 0 {
		return DefaultExpectedStatus
	}
	return probe.ExpectedStatus
}

func canonicalExpectedStatus(probe model.TelemetryProbe) int {
	if probe.Type != model.TelemetryProbeURL {
		return 0
	}
	return EffectiveExpectedStatus(probe)
}

// ValidateURL accepts an exact, unnormalized absolute HTTP(S) target for the fixed GET runner.
// Internal, private, loopback, and overlay destinations remain valid when explicitly authorized by
// the signed policy; safety is provided by the closed request shape rather than address filtering.
func ValidateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("must not be empty")
	}
	if !utf8.ValidString(raw) {
		return fmt.Errorf("must be valid UTF-8")
	}
	if len(raw) > MaxURLBytes {
		return fmt.Errorf("must be at most %d bytes", MaxURLBytes)
	}
	if raw != strings.TrimSpace(raw) {
		return fmt.Errorf("must not have leading or trailing whitespace")
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return fmt.Errorf("must not contain control characters")
		}
	}
	if strings.Contains(raw, " ") {
		return fmt.Errorf("must not contain literal spaces")
	}
	authorityStart := strings.Index(raw, "://")
	if authorityStart < 0 {
		return fmt.Errorf("must be an absolute http or https URL")
	}
	authority := raw[authorityStart+3:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	// Keep the signed destination portable across Go's net/url parser and the browser's WHATWG URL
	// parser. Authority escapes may be normalized into a different hostname, and IPv6 zones use the
	// same syntax, so neither belongs in this deliberately narrow ASCII-DNS/IP contract.
	if strings.Contains(authority, "%") {
		return fmt.Errorf("must not escape the URL authority or include an IPv6 zone identifier")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if parsed.Opaque != "" || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("must be an absolute http or https URL")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return fmt.Errorf("must include a host")
	}
	hostname := parsed.Hostname()
	if !ValidHost(hostname) {
		return fmt.Errorf("host must be an IPv4 address, IPv6 address, or ASCII DNS hostname")
	}
	if strings.HasPrefix(authority, "[") && (net.ParseIP(hostname) == nil || !strings.Contains(hostname, ":")) {
		return fmt.Errorf("bracketed host must be an IPv6 address")
	}
	if parsed.User != nil {
		return fmt.Errorf("must not include user information")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("must not include a fragment")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return fmt.Errorf("must not include an empty port")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("port must be in 1..65535")
		}
	}
	return nil
}

// ValidateName checks optional controller-side display metadata. Names are deliberately concise,
// single-line labels; they need not be unique because stable ID plus exact executable fields remain
// the policy/history identity.
func ValidateName(name string) error {
	if name == "" {
		return nil
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("must be valid UTF-8")
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("must not have leading or trailing whitespace")
	}
	if utf8.RuneCountInString(name) > MaxNameRunes {
		return fmt.Errorf("must be at most %d characters", MaxNameRunes)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("must contain only printable single-line characters")
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
