package renderer

// BabelRolePreset 描述某个角色的 Babel 链路调参默认值。
// 这些字段是「每角色可调」的来源，渲染器必须从预设读取而非在模板里写死字面量，
// 否则按角色调参的能力就不可达（dossier D78）。
type BabelRolePreset struct {
	// 接口的默认 rxcost（0 = 省略 rxcost token，使用 babeld 内置默认；relay 用 96 偏置）。
	// 边上的 LinkCost > 0 时覆盖本默认值。
	DefaultCost int

	// hello-interval（秒，0 = 省略 token，使用 babeld 内置默认）。
	HelloInterval int

	// update-interval（秒，0 = 省略 token，使用 babeld 内置默认）。
	UpdateInterval int
}

// 角色预设的计时器默认值。历史上模板里硬编码为 hello-interval 4 / update-interval 16，
// Spec（docs/spec/compiler/routing-modes.md「Role-preset timers and control port」）规定
// 这两个当前默认值现在必须由预设承载，以便后续按角色调参。
const (
	// defaultHelloInterval 是各角色 hello-interval 的当前默认值（秒）。
	defaultHelloInterval = 4
	// defaultUpdateInterval 是各角色 update-interval 的当前默认值（秒）。
	defaultUpdateInterval = 16
)

// GetBabelRolePreset 返回指定角色的 Babel 预设。
func GetBabelRolePreset(role string) BabelRolePreset {
	switch role {
	case "router":
		// router：默认 cost 走 babeld 内置默认（省略 rxcost）。
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}

	case "relay":
		// relay：偏置较高的 wired-like cost，使路径优选避开中继。
		return BabelRolePreset{
			DefaultCost:    96,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}

	case "gateway":
		// gateway：默认 cost 走 babeld 内置默认。
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}

	default: // "peer"
		// peer：默认 cost 走 babeld 内置默认。
		return BabelRolePreset{
			DefaultCost:    0,
			HelloInterval:  defaultHelloInterval,
			UpdateInterval: defaultUpdateInterval,
		}
	}
}
