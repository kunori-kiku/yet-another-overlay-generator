package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
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

	fmt.Printf(": %s (%s)\n", topo.Project.Name, topo.Project.ID)
	fmt.Printf(": %d, : %d, : %d\n",
		len(topo.Nodes), len(topo.Edges), len(topo.Domains))

	// （Phase 0 ， wg genkey）
	keys := generateFakeKeys(&topo)

	// 
	c := compiler.NewCompiler()
	result, err := c.Compile(&topo, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, ": %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("IP :\n")
	for _, node := range result.Topology.Nodes {
		fmt.Printf("  %s -> %s\n", node.Name, node.OverlayIP)
	}

	//  WireGuard 
	wgConfigs, err := renderer.RenderAllWireGuardConfigs(result.Topology, result.PeerMap, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, " WireGuard : %v\n", err)
		os.Exit(1)
	}
	result.WireGuardConfigs = wgConfigs

	//  Babel 
	babelConfigs, err := renderer.RenderAllBabelConfigs(result.Topology, result.PeerMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, " Babel : %v\n", err)
		os.Exit(1)
	}
	result.BabelConfigs = babelConfigs

	//  sysctl 
	sysctlConfigs, err := renderer.RenderAllSysctlConfigs(result.Topology)
	if err != nil {
		fmt.Fprintf(os.Stderr, " sysctl : %v\n", err)
		os.Exit(1)
	}
	result.SysctlConfigs = sysctlConfigs

	// 
	for _, node := range result.Topology.Nodes {
		_, hasBabel := result.BabelConfigs[node.ID]
		script, err := renderer.RenderInstallScript(&node, result.PeerMap[node.ID], hasBabel)
		if err != nil {
			fmt.Fprintf(os.Stderr, ": %v\n", err)
			os.Exit(1)
		}
		result.InstallScripts[node.ID] = script
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

func generateFakeKeys(topo *model.Topology) map[string]compiler.KeyPair {
	keys := make(map[string]compiler.KeyPair)
	for _, node := range topo.Nodes {
		keys[node.ID] = compiler.KeyPair{
			PrivateKey: fmt.Sprintf("FAKE_PRIVKEY_%s", node.ID),
			PublicKey:  fmt.Sprintf("FAKE_PUBKEY_%s", node.ID),
		}
	}
	return keys
}
