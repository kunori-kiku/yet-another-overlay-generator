package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
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

	// Bundle signing is opt-in via YAOG_BUNDLE_SIGNING_KEY (resolved through the shared
	// seam so the env-var name + PEM handling stay in one place). Unset ⇒ nil signer ⇒
	// hash-only bundles, byte-for-byte today's output. Resolved up front so a malformed
	// key fails the run before any artifact is rendered.
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load the bundle signing key: %v\n", err)
		os.Exit(1)
	}

	// Run the whole local compile path — resolve/generate WireGuard keys (round-trip and
	// reuse private keys, hard-error when only a public key is persisted, generate-and-
	// write-back for brand-new nodes; replacing the old generateFakeKeys, audit blocker
	// D6), compile, and render every deployment artifact — through the single shared
	// façade, so the CLI is byte-for-byte identical to the API/controller. The air-gap
	// CLI has no request context and no off-host clock, so it passes time.Now() as the
	// compile clock (it feeds only manifest.json's compiled_at, out of the byte set).
	result, err := localcompile.CompileResult(localcompile.CompileRequest{
		Topology:   topo,
		Custody:    render.AirGap,
		Fetch:      fetch,
		SigningKey: signer,
		CompiledAt: time.Now(),
	})
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
