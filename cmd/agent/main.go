// Command agent is the YAOG node agent CLI. It pulls a per-node install bundle
// from a configured source, verifies it (Ed25519 signature + per-file SHA-256),
// enforces anti-rollback, then runs the bundle's own install.sh (which performs
// the custody-gated private-key splice from /etc/wireguard/agent.key). Identity
// is configured via --node-id; there is no enrollment in this phase.
//
// Subcommands:
//
//	agent keygen --key PATH
//	    Idempotently ensure the local WireGuard private key exists (mode 0600) and
//	    print the corresponding public key. Re-running keeps the same key.
//
//	agent run --node-id ID --source dir:PATH|http(s)://... [--pubkey PEM] [flags]
//	    pull -> verify -> anti-rollback -> apply -> report.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "keygen":
		os.Exit(runKeygen(os.Args[2:]))
	case "run":
		os.Exit(runRun(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "agent: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agent <keygen|run> [flags]")
	fmt.Fprintln(os.Stderr, "  keygen  ensure the local WireGuard private key exists and print its public key")
	fmt.Fprintln(os.Stderr, "  run     pull -> verify -> anti-rollback -> apply -> report")
}

// runKeygen implements `agent keygen`.
func runKeygen(args []string) int {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	keyPath := fs.String("key", agent.DefaultKeyPath, "path to the local WireGuard private-key file (mode 0600)")
	_ = fs.Parse(args)

	pub, created, err := agent.EnsureKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: keygen: %v\n", err)
		return 1
	}
	if created {
		fmt.Fprintf(os.Stderr, "agent: generated new key at %s\n", *keyPath)
	} else {
		fmt.Fprintf(os.Stderr, "agent: reusing existing key at %s\n", *keyPath)
	}
	// The public key is the only thing printed to stdout so it can be piped into a
	// registration step. The private key is never printed.
	fmt.Println(pub)
	return 0
}

// runRun implements `agent run`.
func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "configured node identity (bundle subdir / state key)")
	sourceSpec := fs.String("source", "", "bundle source: dir:PATH or http(s)://...")
	pubkeyPath := fs.String("pubkey", "", "path to the pinned signing public-key PEM (optional; when set, a signature is required)")
	keyPath := fs.String("key", agent.DefaultKeyPath, "path to the local WireGuard private-key file")
	stateDir := fs.String("state-dir", agent.DefaultStateDir, "directory for the agent's persisted state")
	stagingDir := fs.String("staging-dir", "", "directory to materialize the verified bundle (default: a fresh temp dir)")
	_ = fs.Parse(args)

	if *nodeID == "" {
		fmt.Fprintln(os.Stderr, "agent: run: --node-id is required")
		return 2
	}
	if *sourceSpec == "" {
		fmt.Fprintln(os.Stderr, "agent: run: --source is required")
		return 2
	}

	src, err := agent.NewSourceFromSpec(*sourceSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 2
	}

	var pinned []byte
	if *pubkeyPath != "" {
		pinned, err = os.ReadFile(*pubkeyPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: read pinned pubkey %s: %v\n", *pubkeyPath, err)
			return 1
		}
	}

	cfg := &agent.Config{
		NodeID:       *nodeID,
		Source:       src,
		PinnedPubPEM: pinned,
		KeyPath:      *keyPath,
		StateDir:     *stateDir,
		StagingDir:   *stagingDir,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	}

	res, err := agent.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: run: %v\n", err)
		return 1
	}
	signed := false
	if res.Verify != nil {
		signed = res.Verify.Signed
	}
	fmt.Fprintf(os.Stderr, "agent: applied generation compiled_at=%s checksum=%s signed=%t files=%d\n",
		res.CompiledAt, res.Checksum, signed, verifyFileCount(res))
	return 0
}

// verifyFileCount returns the number of files verified (0 when unavailable).
func verifyFileCount(res *agent.RunResult) int {
	if res.Verify == nil {
		return 0
	}
	return res.Verify.FileCount
}
