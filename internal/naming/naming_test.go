package naming

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestSafeInstallerFileName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"简单小写", "alpha", "alpha.install.sh"},
		{"大写转小写", "Alpha", "alpha.install.sh"},
		{"空格转连字符", "Web 1", "web-1.install.sh"},
		{"已是连字符形式", "web-1", "web-1.install.sh"},
		{"特殊字符与折叠", "Edge Router", "edge-router.install.sh"},
		{"全特殊字符回退为 node", "  ***  ", "node.install.sh"},
		{"下划线保留", "my_server", "my_server.install.sh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeInstallerFileName(tt.input)
			if got != tt.expected {
				t.Errorf("SafeInstallerFileName(%q) = %q, 期望 %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestSafeInstallerFileNameCollision 验证 Spec D 中举例的两个不同原始名称
// （"Web 1" 与 "web-1"）会归一化到同一个安装脚本文件名——这正是 N2 唯一性
// 不变量需要语义校验拦截的碰撞情形。
func TestSafeInstallerFileNameCollision(t *testing.T) {
	a := SafeInstallerFileName("Web 1")
	b := SafeInstallerFileName("web-1")
	if a != b {
		t.Fatalf("期望 %q 与 %q 归一化到同一文件名，实际 %q != %q", "Web 1", "web-1", a, b)
	}
	if a != "web-1.install.sh" {
		t.Fatalf("碰撞结果应为 %q，实际 %q", "web-1.install.sh", a)
	}
}

func TestWgInterfaceName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"短名称", "alpha", "wg-alpha"},
		{"另一个短名称", "beta", "wg-beta"},
		{"大写转小写", "Alpha", "wg-alpha"},
		{"下划线转连字符", "my_server", "wg-my-server"},
		{"点转连字符", "db.east", "wg-db-east"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WgInterfaceName(tt.input)
			if got != tt.expected {
				t.Errorf("WgInterfaceName(%q) = %q, 期望 %q", tt.input, got, tt.expected)
			}
			if len(got) > 15 {
				t.Errorf("WgInterfaceName(%q) = %q 超过 15 字符", tt.input, got)
			}
		})
	}
}

// TestWgInterfaceNameLongHashBranch 验证超过 15 字符（清理后 >12 字符）的名称
// 走哈希后缀分支，且输出与算法定义逐字节一致：wg- 前缀 + clean[:8] + sha256(name)[:4]。
// 期望的哈希片段在测试中独立计算，以钉死实现行为。
func TestWgInterfaceNameLongHashBranch(t *testing.T) {
	const input = "my-long-server-name"

	// 独立复算期望值：清理后该名称全部为合法字符，长度 19，"wg-"+clean = 22 > 15，
	// 因此走长路径：wg- + clean[:8] + sha256(input)[:4]。
	clean := "my-long-server-name"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(input)))
	expected := "wg-" + clean[:8] + hash[:4]

	got := WgInterfaceName(input)
	if got != expected {
		t.Fatalf("WgInterfaceName(%q) = %q, 期望哈希分支输出 %q", input, got, expected)
	}
	if len(got) != 15 {
		t.Fatalf("哈希分支输出应恰为 15 字符，实际 %q (len=%d)", got, len(got))
	}
}

// TestWgInterfaceNamePinnedLongName 钉死历史实现中已固定的长名称用例，
// 确保从 internal/compiler 迁移后行为完全不变。
func TestWgInterfaceNamePinnedLongName(t *testing.T) {
	got := WgInterfaceName("abcdefghijklmnop")
	const expected = "wg-abcdefghf39d"
	if got != expected {
		t.Fatalf("WgInterfaceName(%q) = %q, 期望 %q", "abcdefghijklmnop", got, expected)
	}
}

// TestWgInterfaceNameForEdgePrimaryByteIdentical 验证 backup == false 时，
// 边感知接口名与 WgInterfaceName(remoteName) 逐字节一致——包括短名称、需要清理的
// 名称，以及走哈希长路径（清理后 >12 字符）的名称。部署中的集群因此零接口重命名。
func TestWgInterfaceNameForEdgePrimaryByteIdentical(t *testing.T) {
	names := []string{
		"alpha",               // 短路径
		"my_server",           // 需清理（下划线 → 连字符）
		"db.east",             // 需清理（点 → 连字符）
		"my-long-server-name", // 长路径，走哈希后缀
		"abcdefghijklmnop",    // 历史钉死长名称
	}
	for _, n := range names {
		// edgeID 在 primary 路径上必须被忽略，故传入一个非空值以确认它不影响结果。
		got := WgInterfaceNameForEdge(n, "some-edge-id", false)
		want := WgInterfaceName(n)
		if got != want {
			t.Errorf("primary 路径应与 WgInterfaceName 逐字节一致：WgInterfaceNameForEdge(%q,_,false) = %q, 期望 %q", n, got, want)
		}
		if len(got) > 15 {
			t.Errorf("WgInterfaceNameForEdge(%q,_,false) = %q 超过 15 字符", n, got)
		}
	}
}

// TestWgInterfaceNameForEdgeBackupShape 验证 backup == true 时无条件走长路径形状：
// wg- + clean[:8] + sha256(remoteName+"|"+edgeID)[:4]，且恰为 15 字符。
// 即便 remoteName 很短（primary 路径本会走短路径），backup 路径也仍取哈希后缀。
func TestWgInterfaceNameForEdgeBackupShape(t *testing.T) {
	const remote = "alpha"
	const edgeID = "edge-1"

	clean := "alpha"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(remote+"|"+edgeID)))
	// clean 长度 5 < 8，故 clean[:8] 受实际长度约束，取全部 "alpha"。
	expected := "wg-" + clean + hash[:4]

	got := WgInterfaceNameForEdge(remote, edgeID, true)
	if got != expected {
		t.Fatalf("WgInterfaceNameForEdge(%q,%q,true) = %q, 期望 %q", remote, edgeID, got, expected)
	}
}

// TestWgInterfaceNameForEdgeBackupDistinct 验证朝向同一远端的两条 backup edge
// 因 edge ID 不同而产生不同的接口名（4 位十六进制后缀分叉）。
func TestWgInterfaceNameForEdgeBackupDistinct(t *testing.T) {
	const remote = "gateway-node"
	a := WgInterfaceNameForEdge(remote, "edge-a", true)
	b := WgInterfaceNameForEdge(remote, "edge-b", true)
	if a == b {
		t.Fatalf("同一远端、不同 edge ID 的两条 backup 接口名应不同，实际均为 %q", a)
	}
	// backup 接口名也必须区别于 primary 接口名。
	if a == WgInterfaceName(remote) {
		t.Fatalf("backup 接口名不应与 primary 接口名相同：均为 %q", a)
	}
}

// TestWgInterfaceNameForEdgeLengthBound 验证 backup 路径对短名称与长名称都不超过
// 15 字符（哈希形状的预算上界恰为 3 + 8 + 4 = 15）。
func TestWgInterfaceNameForEdgeLengthBound(t *testing.T) {
	cases := []struct {
		remote string
		edgeID string
	}{
		// 极短远端名、短远端名、长远端名各一例。
		{"a", "e1"},
		{"alpha", "edge-1"},
		{"my-long-server-name-that-is-very-long", "edge-xyz-123"},
	}
	for _, c := range cases {
		got := WgInterfaceNameForEdge(c.remote, c.edgeID, true)
		if len(got) > 15 {
			t.Errorf("WgInterfaceNameForEdge(%q,%q,true) = %q 超过 15 字符 (len=%d)", c.remote, c.edgeID, got, len(got))
		}
	}
}

// TestWgInterfaceNameForEdgeDeterminism 验证相同输入恒产出相同输出（编译应可重现）。
func TestWgInterfaceNameForEdgeDeterminism(t *testing.T) {
	const remote = "edge-router"
	const edgeID = "edge-42"
	first := WgInterfaceNameForEdge(remote, edgeID, true)
	for i := 0; i < 5; i++ {
		if got := WgInterfaceNameForEdge(remote, edgeID, true); got != first {
			t.Fatalf("相同输入应确定性产出相同接口名：第 %d 次得到 %q, 首次为 %q", i, got, first)
		}
	}
}
