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
