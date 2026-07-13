package renderer

import (
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// SysctlConfig holds the data used to render a node's sysctl configuration. NodeName appears only in
// the header comment (never shell-evaluated), so it is a ShellRaw token — part of the same
// root-shell-safety seam as the install templates (field_safety_test covers this config too).
type SysctlConfig struct {
	NodeName         ShellToken
	EnableForwarding bool
}

// RenderSysctlConfig renders the sysctl configuration for a single node.
func RenderSysctlConfig(node *model.Node) (string, error) {
	config := SysctlConfig{
		NodeName:         ShellRaw(node.Name),
		EnableForwarding: node.Capabilities.CanForward,
	}

	return renderTemplate("sysctl.conf", sysctlConfigTemplate, config)
}

// RenderAllSysctlConfigs renders the sysctl configuration for every node in the topology.
func RenderAllSysctlConfigs(topo *model.Topology) (map[string]string, error) {
	configs := make(map[string]string)

	for i := range topo.Nodes {
		node := &topo.Nodes[i]
		config, err := RenderSysctlConfig(node)
		if err != nil {
			return nil, err
		}
		configs[node.ID] = config
	}

	return configs, nil
}
