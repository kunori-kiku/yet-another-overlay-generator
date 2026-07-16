package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

type TelemetryPolicyDeployMode string

const (
	TelemetryPolicyDeployNormal             TelemetryPolicyDeployMode = "normal"
	TelemetryPolicyDeployUpgradeAgentsFirst TelemetryPolicyDeployMode = "upgrade-agents-first"
)

type TelemetryPolicyReadinessError struct {
	NodeIDs []string
}

func (e *TelemetryPolicyReadinessError) Error() string {
	if e == nil || len(e.NodeIDs) == 0 {
		return "controller: successor telemetry policy requires confirmed agent capabilities"
	}
	return "controller: successor telemetry policy requires agent upgrades for nodes: " + strings.Join(e.NodeIDs, ", ")
}

// PrepareTelemetryPolicyDeployment deep-copies the operator topology and applies the requested
// rollout decision only to that copy. Normal deployment requires exact authenticated latest-heartbeat
// capabilities on ready managed nodes. Upgrade-first strips successor-only fields from the compile
// copy while returning every affected node ID; it never rewrites the saved draft.
func PrepareTelemetryPolicyDeployment(topo *model.Topology, nodes []Node, mode TelemetryPolicyDeployMode) (*model.Topology, []string, error) {
	if topo == nil {
		return nil, nil, fmt.Errorf("controller: telemetry policy deployment topology is nil")
	}
	if mode == "" {
		mode = TelemetryPolicyDeployNormal
	}
	raw, err := json.Marshal(topo)
	if err != nil {
		return nil, nil, fmt.Errorf("controller: copying telemetry policy topology: %w", err)
	}
	var projected model.Topology
	if err := json.Unmarshal(raw, &projected); err != nil {
		return nil, nil, fmt.Errorf("controller: copying telemetry policy topology: %w", err)
	}
	// Validate the copied draft before capability readiness or legacy projection. In particular, an
	// unknown/manual-only device selector must remain a structured topology error; it must not hide
	// behind a temporary 412 readiness result or be stripped by phase one and fail later as a 500.
	if schema := validator.ValidateSchema(&projected); len(schema.Errors) > 0 {
		return nil, nil, &compiler.TopologyValidationError{Phase: "schema", Findings: schema.Errors}
	}

	switch mode {
	case TelemetryPolicyDeployUpgradeAgentsFirst:
		var omitted []string
		for i := range projected.Nodes {
			if !probepolicy.RequiresSuccessor(projected.Nodes[i]) {
				continue
			}
			omitted = append(omitted, projected.Nodes[i].ID)
			probepolicy.ProjectLegacy(&projected.Nodes[i])
		}
		sort.Strings(omitted)
		return &projected, omitted, nil
	case TelemetryPolicyDeployNormal:
		ready, _, err := projectEnrolledSubgraph(&projected, nodes)
		if err != nil {
			return nil, nil, err
		}
		registry := make(map[string]Node, len(nodes))
		for _, node := range nodes {
			registry[node.NodeID] = node
		}
		var blocked []string
		for _, node := range ready.Nodes {
			if node.IsManual() || !probepolicy.RequiresSuccessor(node) {
				continue
			}
			available, ok := latestAgentCapabilities(registry[node.ID])
			if !ok || !containsAllCapabilities(available, probepolicy.RequiredCapabilities(node)) {
				blocked = append(blocked, node.ID)
			}
		}
		if len(blocked) > 0 {
			sort.Strings(blocked)
			return nil, nil, &TelemetryPolicyReadinessError{NodeIDs: blocked}
		}
		return &projected, nil, nil
	default:
		return nil, nil, fmt.Errorf("controller: unsupported telemetry policy deploy mode %q", mode)
	}
}

func latestAgentCapabilities(node Node) (map[string]struct{}, bool) {
	raw, ok := node.Telemetry[telemetrymetric.AgentCapabilitiesKey]
	if !ok || len(raw) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var metric telemetrymetric.AgentCapabilitiesMetric
	if err := dec.Decode(&metric); err != nil {
		return nil, false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	if err := telemetrymetric.ValidateAgentCapabilities(metric.Capabilities); err != nil {
		return nil, false
	}
	set := make(map[string]struct{}, len(metric.Capabilities))
	for _, capability := range metric.Capabilities {
		set[capability] = struct{}{}
	}
	return set, true
}

func containsAllCapabilities(available map[string]struct{}, required []string) bool {
	for _, capability := range required {
		if _, ok := available[capability]; !ok {
			return false
		}
	}
	return true
}
