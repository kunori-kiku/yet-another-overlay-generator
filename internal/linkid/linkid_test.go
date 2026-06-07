package linkid

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestPinKeySymmetry 验证 PinKey 方向无关：PinKey(A, B) == PinKey(B, A)。
func TestPinKeySymmetry(t *testing.T) {
	pairs := [][2]string{
		{"alpha", "beta"},
		{"beta", "alpha"},
		{"node-1", "node-2"},
		{"zzz", "aaa"},
	}
	for _, p := range pairs {
		fwd := PinKey(p[0], p[1])
		rev := PinKey(p[1], p[0])
		if fwd != rev {
			t.Errorf("PinKey 应方向无关：PinKey(%q,%q)=%q != PinKey(%q,%q)=%q",
				p[0], p[1], fwd, p[1], p[0], rev)
		}
	}
}

// TestPinKeyOrder 验证 PinKey 的拼接顺序：较小的节点 ID 在前，用竖线分隔。
func TestPinKeyOrder(t *testing.T) {
	tests := []struct {
		a, b, expected string
	}{
		{"alpha", "beta", "alpha|beta"},
		{"beta", "alpha", "alpha|beta"},
		{"a", "b", "a|b"},
		{"b", "a", "a|b"},
		// 相等输入：min == max，仍以竖线拼接自身。
		{"x", "x", "x|x"},
	}
	for _, tt := range tests {
		got := PinKey(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("PinKey(%q,%q) = %q, 期望 %q", tt.a, tt.b, got, tt.expected)
		}
	}
}

// TestLinkKeyPrimaryReduction 验证 primary class 的 edge（role 为空或 "primary"）
// 的 LinkKey 退化为 PinKey——这是单 edge 节点对相对 parallel-links 之前编译器的
// no-drift 保证。同一对节点的两条 primary-class edge（含反向）共享同一个 LinkKey。
func TestLinkKeyPrimaryReduction(t *testing.T) {
	want := PinKey("alpha", "beta")

	roleless := &model.Edge{ID: "e1", FromNodeID: "alpha", ToNodeID: "beta"}
	if got := LinkKey(roleless); got != want {
		t.Errorf("空 role 的 LinkKey 应退化为 PinKey：得到 %q, 期望 %q", got, want)
	}

	explicitPrimary := &model.Edge{ID: "e2", FromNodeID: "alpha", ToNodeID: "beta", Role: model.EdgeRolePrimary}
	if got := LinkKey(explicitPrimary); got != want {
		t.Errorf("显式 primary 的 LinkKey 应退化为 PinKey：得到 %q, 期望 %q", got, want)
	}

	// 反向 primary-class edge 应与正向共享同一 LinkKey（方向无关）。
	reverse := &model.Edge{ID: "e3", FromNodeID: "beta", ToNodeID: "alpha"}
	if got := LinkKey(reverse); got != want {
		t.Errorf("反向 primary-class edge 的 LinkKey 应与正向一致：得到 %q, 期望 %q", got, want)
	}
}

// TestLinkKeyBackupDiscrimination 验证每条 backup edge 的 LinkKey 携带自己的 edge ID，
// 因此同一对节点下：两条 backup 彼此不同、backup 与 primary 也不同。
func TestLinkKeyBackupDiscrimination(t *testing.T) {
	pair := PinKey("alpha", "beta")

	backup1 := &model.Edge{ID: "b1", FromNodeID: "alpha", ToNodeID: "beta", Role: model.EdgeRoleBackup}
	backup2 := &model.Edge{ID: "b2", FromNodeID: "alpha", ToNodeID: "beta", Role: model.EdgeRoleBackup}

	want1 := pair + "#b1"
	if got := LinkKey(backup1); got != want1 {
		t.Errorf("backup edge 的 LinkKey 应为 PinKey#ID：得到 %q, 期望 %q", got, want1)
	}

	if LinkKey(backup1) == LinkKey(backup2) {
		t.Errorf("同一对节点的两条 backup 应有不同 LinkKey：均为 %q", LinkKey(backup1))
	}

	primary := &model.Edge{ID: "p1", FromNodeID: "alpha", ToNodeID: "beta"}
	if LinkKey(primary) == LinkKey(backup1) {
		t.Errorf("backup 与 primary 不应共享 LinkKey：均为 %q", LinkKey(primary))
	}

	// 即便 backup 是该对节点的唯一 edge，仍以 edge ID 区分（身份不迁移保证）。
	soleBackup := &model.Edge{ID: "only", FromNodeID: "gamma", ToNodeID: "delta", Role: model.EdgeRoleBackup}
	if got := LinkKey(soleBackup); got == PinKey("gamma", "delta") {
		t.Errorf("唯一的 backup edge 仍应被 edge ID 区分，不得退化为裸 PinKey：得到 %q", got)
	}
}

// TestIsBackup 验证角色判定：仅 "backup" 为 true，空值与 "primary" 为 false。
func TestIsBackup(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{"", false},
		{model.EdgeRolePrimary, false},
		{model.EdgeRoleBackup, true},
	}
	for _, tt := range tests {
		e := &model.Edge{ID: "e", FromNodeID: "a", ToNodeID: "b", Role: tt.role}
		if got := IsBackup(e); got != tt.want {
			t.Errorf("IsBackup(role=%q) = %v, 期望 %v", tt.role, got, tt.want)
		}
	}
}
