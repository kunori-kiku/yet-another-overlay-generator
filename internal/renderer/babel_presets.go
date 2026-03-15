package renderer

// BabelRolePreset Babel 
type BabelRolePreset struct {
	//  cost（0 =  Babel  96）
	DefaultCost int

	// hello interval（，0 = ）
	HelloInterval int

	// update interval（，0 = ）
	UpdateInterval int
}

// GetBabelRolePreset  Babel 
func GetBabelRolePreset(role string) BabelRolePreset {
	switch role {
	case "router":
		// router: 
		return BabelRolePreset{
			DefaultCost:    0, //  Babel 
			HelloInterval:  0,
			UpdateInterval: 0,
		}

	case "relay":
		// relay:  cost，
		return BabelRolePreset{
			DefaultCost:    96, //  wired cost
			HelloInterval:  0,
			UpdateInterval: 0,
		}

	case "gateway":
		// gateway: ，
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  0,
			UpdateInterval: 0,
		}

	default: // "peer"
		// peer:  cost，
		return BabelRolePreset{
			DefaultCost:    0, // peer  cost（）
			HelloInterval:  0,
			UpdateInterval: 0,
		}
	}
}
