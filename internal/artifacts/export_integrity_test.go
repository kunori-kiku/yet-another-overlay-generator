package artifacts

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExport_ChecksumsCoversInstallScript 验证 install.sh —— 以 root 执行的信任锚点 ——
// 被纳入 checksums.sha256（审计项 D24），且记录的哈希与落盘文件内容一致。
func TestExport_ChecksumsCoversInstallScript(t *testing.T) {
	result := minimalCompileResult()
	outputDir := t.TempDir()

	if _, err := Export(result, outputDir, nil); err != nil {
		t.Fatalf("Export 失败: %v", err)
	}

	nodeDir := filepath.Join(outputDir, "alpha")

	// 读取 checksums.sha256 并解析出 install.sh 那一行记录的哈希。
	checksumsPath := filepath.Join(nodeDir, "checksums.sha256")
	recordedHash, ok := readChecksumFor(t, checksumsPath, "install.sh")
	if !ok {
		t.Fatalf("checksums.sha256 中缺少 install.sh 行（D24：信任锚点未被覆盖）；文件内容:\n%s",
			mustReadFile(t, checksumsPath))
	}

	// 计算 install.sh 实际落盘内容的哈希，与记录值比对。
	installPath := filepath.Join(nodeDir, "install.sh")
	actualBytes := mustReadFileBytes(t, installPath)
	actualHash := fmt.Sprintf("%x", sha256.Sum256(actualBytes))

	if recordedHash != actualHash {
		t.Errorf("install.sh 记录的校验和与实际内容不符:\n  记录: %s\n  实际: %s", recordedHash, actualHash)
	}

	// manifest.json 携带 compiled_at 等编译期时间戳，按 spec 刻意排除于完整性校验之外。
	if _, present := readChecksumFor(t, checksumsPath, "manifest.json"); present {
		t.Errorf("manifest.json 不应出现在 checksums.sha256 中（spec 明确排除）")
	}
}

// readChecksumFor 解析 sha256sum 风格的校验文件，返回指定相对路径的哈希。
// 校验行格式为 "<hex>  <relpath>"（两个空格分隔）。
func readChecksumFor(t *testing.T, checksumsPath, relPath string) (string, bool) {
	t.Helper()

	f, err := os.Open(checksumsPath)
	if err != nil {
		t.Fatalf("读取 checksums.sha256 失败: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == relPath {
			return fields[0], true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("扫描 checksums.sha256 失败: %v", err)
	}
	return "", false
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	return string(mustReadFileBytes(t, path))
}

func mustReadFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}
