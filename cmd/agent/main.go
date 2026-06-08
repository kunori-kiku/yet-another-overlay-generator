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
//	    pull -> verify -> anti-rollback -> apply -> report (configured-source mode).
//
//	agent enroll --controller URL --controller-ca PEM --node-id ID --token T [flags]
//	    One-time enrollment against the networked controller: ensure the WG key,
//	    generate an Ed25519 mTLS keypair + CSR (CN "<tenant>:<node>"), POST /enroll
//	    over a certless TLS connection trusting the pinned CA, verify the returned CA
//	    equals the pinned CA, then persist the issued client cert + mTLS private key.
//
//	agent run --controller URL --controller-ca PEM --node-id ID [flags]
//	    Controller mode: load the mTLS cert, long-poll /poll for a new generation,
//	    then run the same pull -> verify -> apply -> report loop against the
//	    controller's /config + /report. (v1 applies one pending generation per
//	    invocation; a production daemon loops — see runControllerMode.)
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
	case "enroll":
		os.Exit(runEnroll(os.Args[2:]))
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
	fmt.Fprintln(os.Stderr, "usage: agent <keygen|enroll|run> [flags]")
	fmt.Fprintln(os.Stderr, "  keygen  ensure the local WireGuard private key exists and print its public key")
	fmt.Fprintln(os.Stderr, "  enroll  enroll against the networked controller and persist the issued mTLS cert")
	fmt.Fprintln(os.Stderr, "  run     pull -> verify -> anti-rollback -> apply -> report (configured-source or --controller mode)")
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

// defaultMTLSCertPath / defaultMTLSKeyPath are where enrollment writes (and run
// reads) the issued mTLS client cert and its private key. They sit alongside the WG
// key under /etc/wireguard so the agent's secrets share one custody-gated directory.
const (
	defaultMTLSCertPath = "/etc/wireguard/agent-mtls.crt"
	defaultMTLSKeyPath  = "/etc/wireguard/agent-mtls.key"
)

// runEnroll implements `agent enroll`: the one-time ceremony that turns a single-use
// enrollment token into a persisted mTLS client cert. It (1) ensures the WG key and
// reads its public key, (2) generates a fresh Ed25519 mTLS keypair + a self-signed
// CSR with CN "<tenant>:<node>" (the PoP the controller checks), (3) POSTs /enroll
// over a certless TLS connection that trusts ONLY the pinned --controller-ca,
// verifying the returned CA equals the pinned one, and (4) writes the issued cert +
// the mTLS private key (PKCS#8 PEM) to disk at 0600. It prints the fingerprint.
func runEnroll(args []string) int {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	controller := fs.String("controller", "", "controller base URL: https://host:port")
	controllerCA := fs.String("controller-ca", "", "path to the pinned controller CA cert PEM")
	nodeID := fs.String("node-id", "", "node identity to enroll as (must match the token's scope)")
	token := fs.String("token", "", "single-use enrollment token (delivered out-of-band)")
	tenant := fs.String("tenant", "", "tenant id; the mTLS CSR CN is \"<tenant>:<node-id>\"")
	keyPath := fs.String("key", agent.DefaultKeyPath, "path to the local WireGuard private-key file (mode 0600)")
	mtlsCert := fs.String("mtls-cert", defaultMTLSCertPath, "where to write the issued mTLS client cert PEM (mode 0600)")
	mtlsKey := fs.String("mtls-key", defaultMTLSKeyPath, "where to write the mTLS private key PEM (mode 0600)")
	_ = fs.Parse(args)

	switch {
	case *controller == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --controller is required")
		return 2
	case *controllerCA == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --controller-ca is required")
		return 2
	case *nodeID == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --node-id is required")
		return 2
	case *token == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --token is required")
		return 2
	case *tenant == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --tenant is required (the CSR CN is \"<tenant>:<node-id>\")")
		return 2
	}

	caPEM, err := os.ReadFile(*controllerCA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: read controller CA %s: %v\n", *controllerCA, err)
		return 1
	}

	// (1) Ensure the WG key and read its public key (registered as-is on enroll).
	wgPub, _, err := agent.EnsureKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}

	// (2) Generate the mTLS keypair + CSR (CN "<tenant>:<node>").
	csrDER, mtlsPriv, err := generateMTLSCSR(*tenant + ":" + *nodeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}

	// (3) Enroll over a certless client that trusts only the pinned CA; the client
	// verifies the response CA equals the pinned CA before returning.
	client, err := agent.NewControllerClient(*controller, caPEM, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}
	result, err := client.Enroll(*token, *nodeID, csrDER, wgPub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}

	// (4) Persist the issued cert + mTLS private key (PKCS#8 PEM), both 0600.
	if err := writeMTLSMaterial(*mtlsCert, *mtlsKey, result.ClientCertPEM, mtlsPriv); err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "agent: enrolled node %q; mTLS cert written to %s\n", *nodeID, *mtlsCert)
	// The fingerprint is the only thing on stdout so it can be captured/verified.
	fmt.Println(result.Fingerprint)
	return 0
}

// generateMTLSCSR generates a fresh Ed25519 keypair and a self-signed PKCS#10 CSR
// with Subject CN cn. The CSR's self-signature is the proof-of-possession the
// controller's IssueClientCert verifies; the private key never leaves the node.
func generateMTLSCSR(cn string) (csrDER []byte, priv ed25519.PrivateKey, err error) {
	_, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate mTLS key: %w", err)
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn},
	}, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("create mTLS CSR: %w", err)
	}
	return csrDER, priv, nil
}

// writeMTLSMaterial persists the issued client cert PEM and the mTLS private key
// (marshaled PKCS#8, PEM-wrapped) to disk, both mode 0600, creating parent dirs
// 0700. The cert is not secret but is written 0600 for hygiene alongside the key.
func writeMTLSMaterial(certPath, keyPath string, certPEM []byte, priv ed25519.PrivateKey) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal mTLS private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(filepath.Dir(certPath), 0700); err != nil {
		return fmt.Errorf("create mTLS cert dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return fmt.Errorf("create mTLS key dir: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return fmt.Errorf("write mTLS cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("write mTLS key: %w", err)
	}
	return nil
}

// runRun implements `agent run`. With --controller it runs controller mode (mTLS
// poll/config/report); otherwise it runs the configured-source mode (--source).
func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "configured node identity (bundle subdir / state key)")
	sourceSpec := fs.String("source", "", "bundle source: dir:PATH or http(s)://... (configured-source mode)")
	pubkeyPath := fs.String("pubkey", "", "path to the pinned signing public-key PEM (optional; when set, a signature is required)")
	stateDir := fs.String("state-dir", agent.DefaultStateDir, "directory for the agent's persisted state")
	stagingDir := fs.String("staging-dir", "", "directory to materialize the verified bundle (default: a fresh temp dir)")
	controller := fs.String("controller", "", "controller base URL (controller mode): https://host:port")
	controllerCA := fs.String("controller-ca", "", "path to the pinned controller CA cert PEM (controller mode)")
	mtlsCert := fs.String("mtls-cert", defaultMTLSCertPath, "path to the issued mTLS client cert PEM (controller mode)")
	mtlsKey := fs.String("mtls-key", defaultMTLSKeyPath, "path to the mTLS private key PEM (controller mode)")
	after := fs.Int64("after", 0, "controller mode: poll for a generation strictly greater than this (the last applied generation; a daemon advances it each cycle)")
	_ = fs.Parse(args)

	if *nodeID == "" {
		fmt.Fprintln(os.Stderr, "agent: run: --node-id is required")
		return 2
	}

	// Controller mode takes precedence when --controller is set.
	if *controller != "" {
		return runControllerMode(controllerModeOpts{
			nodeID:       *nodeID,
			baseURL:      *controller,
			controllerCA: *controllerCA,
			mtlsCert:     *mtlsCert,
			mtlsKey:      *mtlsKey,
			pubkeyPath:   *pubkeyPath,
			stateDir:     *stateDir,
			stagingDir:   *stagingDir,
			after:        *after,
		})
	}

	if *sourceSpec == "" {
		fmt.Fprintln(os.Stderr, "agent: run: --source is required (or use --controller for controller mode)")
		return 2
	}

	src, err := agent.NewSourceFromSpec(*sourceSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 2
	}

	pinned, err := readPinnedPubkey(*pubkeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}

	// NOTE: the private-key splice path is fixed at agent.DefaultKeyPath
	// (/etc/wireguard/agent.key) inside the rendered install.sh, so `run` has no --key
	// flag; `agent keygen` writes the key there. Config.KeyPath is left empty (unused by Run).
	cfg := &agent.Config{
		NodeID:       *nodeID,
		Source:       src,
		PinnedPubPEM: pinned,
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
	printApplied(res)
	return 0
}

// controllerModeOpts groups the controller-mode run inputs (kept as a struct so the
// flag plumbing in runRun stays readable).
type controllerModeOpts struct {
	nodeID       string
	baseURL      string
	controllerCA string
	mtlsCert     string
	mtlsKey      string
	pubkeyPath   string
	stateDir     string
	stagingDir   string
	// after is the resume cursor: poll for a generation strictly greater than this.
	// A production daemon advances it from each applied generation; the single-shot
	// CLI takes it as a flag (default 0) since the agent State has no numeric
	// generation field to persist it in.
	after int64
}

// runControllerMode runs ONE controller-driven apply cycle: load the mTLS cert + the
// pinned CA, long-poll /poll for a generation newer than the last applied one, then
// (on a change) drive agent.Run against the controller's /config + /report. Run does
// verify + apply(install.sh) + report; the loop only sequences poll->run and records
// the applied generation.
//
// This is intentionally a SINGLE iteration for v1 (and to keep the e2e test
// deterministic): a production daemon would wrap the poll->run body in a loop,
// updating lastAppliedGen from each RunResult and continuing on error (keep
// last-good). The single-shot form is the unit of that loop.
func runControllerMode(o controllerModeOpts) int {
	if o.controllerCA == "" {
		fmt.Fprintln(os.Stderr, "agent: run: --controller-ca is required in controller mode")
		return 2
	}
	caPEM, err := os.ReadFile(o.controllerCA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: read controller CA %s: %v\n", o.controllerCA, err)
		return 1
	}
	cert, err := tls.LoadX509KeyPair(o.mtlsCert, o.mtlsKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: load mTLS cert/key (%s / %s): %v\n", o.mtlsCert, o.mtlsKey, err)
		fmt.Fprintln(os.Stderr, "agent: run enroll first to obtain a client cert")
		return 1
	}

	client, err := agent.NewControllerClient(o.baseURL, caPEM, &cert)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}

	pinned, err := readPinnedPubkey(o.pubkeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}

	// Resume from the supplied cursor so a re-run does not re-fetch an already-applied
	// generation: long-poll for anything strictly newer than --after. (The agent State
	// keys anti-rollback on the manifest compiled_at string, not the controller's int64
	// generation, so the cursor is a flag here; a daemon advances it per cycle.)
	lastAppliedGen := o.after

	gen, changed, err := client.Poll(lastAppliedGen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: poll: %v\n", err)
		return 1
	}
	if !changed {
		fmt.Fprintf(os.Stderr, "agent: no new generation (still at %d); nothing to do\n", lastAppliedGen)
		return 0
	}

	// A new generation is available. Record the prior watermark so a FAILED apply
	// reports it unchanged (never falsely advancing); a successful apply reports the
	// generation actually fetched. agent.Run fetches the bundle (setting the fetched
	// generation) and fires the auto-Report itself, since this client is a Reporter.
	client.SetPriorGeneration(lastAppliedGen)
	cfg := &agent.Config{
		NodeID:       o.nodeID,
		Source:       client,
		PinnedPubPEM: pinned,
		StateDir:     o.stateDir,
		StagingDir:   o.stagingDir,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	}
	res, err := agent.Run(cfg)
	if err != nil {
		// Keep last-good: Run already recorded the failure and left the running tunnel
		// untouched. A production loop would continue; the single-shot form exits non-zero.
		fmt.Fprintf(os.Stderr, "agent: run: %v\n", err)
		return 1
	}
	printApplied(res)
	fmt.Fprintf(os.Stderr, "agent: applied controller generation %d\n", gen)
	return 0
}

// readPinnedPubkey reads an optional pinned signing-pubkey PEM. An empty path means
// no pin (unsigned bundles permitted); a read error on a non-empty path is fatal.
func readPinnedPubkey(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pinned pubkey %s: %w", path, err)
	}
	return data, nil
}

// printApplied logs a one-line apply summary to stderr.
func printApplied(res *agent.RunResult) {
	signed := false
	if res.Verify != nil {
		signed = res.Verify.Signed
	}
	fmt.Fprintf(os.Stderr, "agent: applied generation compiled_at=%s checksum=%s signed=%t files=%d\n",
		res.CompiledAt, res.Checksum, signed, verifyFileCount(res))
}

// verifyFileCount returns the number of files verified (0 when unavailable).
func verifyFileCount(res *agent.RunResult) int {
	if res.Verify == nil {
		return 0
	}
	return res.Verify.FileCount
}
