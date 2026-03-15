package renderer

// BabelRolePreset Babel 角色预设参数
type BabelRolePreset struct {
	// 默认接口 cost（0 = 使用 Babel 默认值 96）
	DefaultCost int

	// hello interval（秒，0 = 使用默认值）
	HelloInterval int

	// update interval（秒，0 = 使用默认值）
	UpdateInterval int
}

// GetBabelRolePreset 获取角色的 Babel 预设参数
func GetBabelRolePreset(role string) BabelRolePreset {
	switch role {
	case "router":
		// router: 标准参数
		return BabelRolePreset{
			DefaultCost:    0, // 使用 Babel 默认
			HelloInterval:  0,
			UpdateInterval: 0,
		}

	case "relay":
		// relay: 低 cost，高优先级路径
		return BabelRolePreset{
			DefaultCost:    96, // 标准 wired cost
			HelloInterval:  0,
			UpdateInterval: 0,
		}

	case "gateway":
		// gateway: 标准参数，作为出口点
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  0,
			UpdateInterval: 0,
		}

	default: // "peer"
		// peer: 较高 cost，不作为中转首选
		return BabelRolePreset{
			DefaultCost:    0, // peer 不调整 cost（不中转）
			HelloInterval:  0,
			UpdateInterval: 0,
		}
	}
}
