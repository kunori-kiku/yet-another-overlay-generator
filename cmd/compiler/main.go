package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

func main() {
	inputFile := flag.String("input", "", " JSON ")
	outputDir := flag.String("output", "output", "")
	flag.Parse()

	if *inputFile == "" {
		fmt.Fprintln(os.Stderr, ":  (-input)")
		flag.Usage()
		os.Exit(1)
	}

	// 
	data, err := os.ReadFile(*inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, ": %v\n", err)
		os.Exit(1)
	}

	var topo model.Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		fmt.Fprintf(os.Stderr, " JSON : %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("项目: %s (%s)\n", topo.Project.Name, topo.Project.ID)
	fmt.Printf("节点数: %d, 边数: %d, 网络域数: %d\n",
		len(topo.Nodes), len(topo.Edges), len(topo.Domains))

	// 为每个节点解析或生成真实 WireGuard 密钥对（与 API 入口共用同一持久化规则：
	// 私钥往返复用、只持久化公钥时硬错误、全新节点生成并写回新密钥）。
	// 取代旧的 generateFakeKeys——后者向每份配置塞入字面量 FAKE_PRIVKEY_*，
	// 产物无法被 wg-quick 接受、不可部署（审计阻断项 D6）。
	keys, err := render.GenerateKeys(&topo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成 WireGuard 密钥失败: %v\n", err)
		os.Exit(1)
	}

	// 编译拓扑
	c := compiler.NewCompiler()
	result, err := c.Compile(&topo, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "编译失败: %v\n", err)
		os.Exit(1)
	}

	// 编译成功后仍需向用户展示的非致命告警（NAT 不可达、无 endpoint 的边、孤立节点等）。
	// 这些告警与 API /api/compile 返回的 warnings 同源，确保 CLI 与 API 行为一致。
	if len(result.Warnings) > 0 {
		fmt.Fprintf(os.Stderr, "\n编译告警 (%d 条):\n", len(result.Warnings))
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", w.Level, w.Field, w.Message)
		}
		fmt.Fprintln(os.Stderr)
	}

	fmt.Printf("IP 分配结果:\n")
	for _, node := range result.Topology.Nodes {
		fmt.Printf("  %s -> %s\n", node.Name, node.OverlayIP)
	}

	// 渲染全部部署产物，走与 API 入口完全相同的共享路径（render.All）：per-peer WireGuard
	// 配置、client 的单一 wg0 配置与 client 安装脚本（D27/D28/D29）、Babel 配置、sysctl 配置、
	// 每节点安装脚本，以及 deploy-all.sh/.ps1（D59）。CLI 由此与 API 产物逐字一致。
	if err := render.All(result, keys); err != nil {
		fmt.Fprintf(os.Stderr, "渲染部署产物失败: %v\n", err)
		os.Exit(1)
	}

	//
	exportResult, err := artifacts.Export(result, *outputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, ": %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n: %s\n", exportResult.OutputDir)
	for _, nodeName := range exportResult.Nodes {
		fmt.Printf("  📦 %s/\n", nodeName)
	}
	fmt.Printf("\n! checksum: %s\n", result.Manifest.Checksum)
}
