package renderer

// BabelRolePreset describes the default Babel link-tuning values for a role.
// These fields are the source of "per-role tunability"; the renderer must read them from
// the preset rather than hard-coding literals in the template, otherwise the ability to tune
// per role would be unreachable (dossier D78).
type BabelRolePreset struct {
	// Default rxcost for the interface (0 = omit the rxcost token and use babeld's built-in
	// default; relay uses a bias of 96). An edge's LinkCost > 0 overrides this default.
	DefaultCost int

	// hello-interval (seconds; 0 = omit the token and use babeld's built-in default).
	HelloInterval int

	// update-interval (seconds; 0 = omit the token and use babeld's built-in default).
	UpdateInterval int
}

// Timer defaults for the role presets. Historically the template hard-coded
// hello-interval 4 / update-interval 16; the spec
// (docs/spec/compiler/routing-modes.md "Role-preset timers and control port") mandates
// that these two current defaults now be carried by the preset, to allow per-role tuning later.
const (
	// defaultHelloInterval is the current default hello-interval for all roles (seconds).
	defaultHelloInterval = 4
	// defaultUpdateInterval is the current default update-interval for all roles (seconds).
	defaultUpdateInterval = 16
)

// GetBabelRolePreset returns the Babel preset for the given role.
func GetBabelRolePreset(role string) BabelRolePreset {
	switch role {
	case "router":
		// router: default cost uses babeld's built-in default (omit rxcost).
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}

	case "relay":
		// relay: a higher wired-like cost bias so path selection prefers to avoid the relay.
		return BabelRolePreset{
			DefaultCost:    96,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}

	case "gateway":
		// gateway: default cost uses babeld's built-in default.
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}

	default: // "peer"
		// peer: default cost uses babeld's built-in default.
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}
	}
}
