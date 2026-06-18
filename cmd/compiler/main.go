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

// BuildVersion is the compiler's build version, overwritten at release link time via
// -ldflags "-X main.BuildVersion=<tag>" (see RELEASING.md). A non-release build reports "dev".
var BuildVersion = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(BuildVersion)
		return
	}
	inputFile := flag.String("input", "", "path to the topology JSON file")
	outputDir := flag.String("output", "output", "output directory")
	// Air-gap artifact-catalog inputs (plan-7), layered OVER the env vars of the same purpose
	// (flag wins when set). All unset ⇒ distro-only mimic + no artifacts.json ⇒ byte-identical
	// bundle (D4).
	artifactCatalog := flag.String("artifact-catalog", "", "path to an artifacts.json-shaped mimic catalog (env "+render.EnvArtifactCatalog+")")
	ghProxy := flag.String("gh-proxy", "", "GitHub download proxy prefix baked into install.sh (env "+render.EnvGithubProxy+")")
	mimicVersion := flag.String("mimic-version", "", "pinned mimic version label override (env "+render.EnvMimicVersion+")")
	flag.Parse()

	if *inputFile == "" {
		fmt.Fprintln(os.Stderr, "error: -input is required")
		flag.Usage()
		os.Exit(1)
	}

	data, err := os.ReadFile(*inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read input file: %v\n", err)
		os.Exit(1)
	}

	var topo model.Topology
	if err := json.Unmarshal(data, &topo); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("project: %s (%s)\n", topo.Project.Name, topo.Project.ID)
	fmt.Printf("nodes: %d, edges: %d, domains: %d\n",
		len(topo.Nodes), len(topo.Edges), len(topo.Domains))

	// Resolve or generate a real WireGuard key pair for every node, sharing the
	// same persistence rules as the API entry point: round-trip and reuse private
	// keys, hard-error when only a public key is persisted, and generate-then-write-back
	// new keys for brand-new nodes. This replaces the old generateFakeKeys, which
	// stuffed literal FAKE_PRIVKEY_* into each config — artifacts that wg-quick
	// rejects and that cannot be deployed (audit blocker D6).
	keys, err := render.GenerateKeys(&topo, render.AirGap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate WireGuard keys: %v\n", err)
		os.Exit(1)
	}

	// Compile the topology.
	c := compiler.NewCompiler()
	result, err := c.Compile(&topo, keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compile failed: %v\n", err)
		os.Exit(1)
	}

	// Non-fatal warnings that still need to be surfaced to the user after a
	// successful compile (unreachable NAT, edges without an endpoint, orphaned
	// nodes, etc.). These warnings share the same source as the ones returned by
	// the API /api/compile, keeping CLI and API behavior consistent.
	if len(result.Warnings) > 0 {
		fmt.Fprintf(os.Stderr, "\ncompile warnings (%d):\n", len(result.Warnings))
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", w.Level, w.Field, w.Message)
		}
		fmt.Fprintln(os.Stderr)
	}

	fmt.Printf("IP allocation:\n")
	for _, node := range result.Topology.Nodes {
		fmt.Printf("  %s -> %s\n", node.Name, node.OverlayIP)
	}

	// Air-gap mimic catalog: flags override the env vars of the same purpose. All unset ⇒ zero
	// FetchSettings ⇒ distro-only mimic install + no artifacts.json ⇒ byte-identical bundle (D4).
	fetch, err := render.LoadFetchSettings(
		firstNonEmpty(*artifactCatalog, os.Getenv(render.EnvArtifactCatalog)),
		firstNonEmpty(*ghProxy, os.Getenv(render.EnvGithubProxy)),
		firstNonEmpty(*mimicVersion, os.Getenv(render.EnvMimicVersion)),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load artifact catalog: %v\n", err)
		os.Exit(1)
	}

	// Render all deployment artifacts through the exact same shared path as the API
	// entry point (render.All): per-peer WireGuard configs, the client's single wg0
	// config and client install script (D27/D28/D29), Babel configs, sysctl configs,
	// per-node install scripts, and deploy-all.sh/.ps1 (D59). This keeps the CLI
	// artifacts byte-for-byte identical to the API's.
	if err := render.All(result, keys, fetch); err != nil {
		fmt.Fprintf(os.Stderr, "failed to render deployment artifacts: %v\n", err)
		os.Exit(1)
	}

	exportResult, err := artifacts.Export(result, *outputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to export artifacts: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nexported to: %s\n", exportResult.OutputDir)
	for _, nodeName := range exportResult.Nodes {
		fmt.Printf("  📦 %s/\n", nodeName)
	}
	fmt.Printf("\ndone! checksum: %s\n", result.Manifest.Checksum)
}

// firstNonEmpty returns a if it is non-empty, else b — the flag-over-env precedence helper.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
