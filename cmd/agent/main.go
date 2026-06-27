// Command agent is the YAOG node agent CLI. It pulls a per-node install bundle
// from a configured source, verifies it (Ed25519 signature + per-file SHA-256),
// enforces anti-rollback, then runs the bundle's own install.sh (which performs
// the custody-gated private-key splice from /etc/wireguard/agent.key). Identity
// is configured via --node-id; controller mode adds a one-time token enrollment.
//
// Subcommands:
//
//	agent keygen --key PATH
//	    Idempotently ensure the local WireGuard private key exists (mode 0600) and
//	    print the corresponding public key. Re-running keeps the same key.
//
//	agent kit --node-id ID [--endpoint host:port] [--key PATH]
//	    One-shot provisioning for a MANUAL (hand-deployed, agent-less) node: ensure the
//	    WG key (the same file install.sh later splices) and print a {node_id,
//	    wireguard_public_key, endpoint} descriptor to paste into the controller design.
//	    Never contacts the controller; the private key never leaves the box.
//
//	agent run --node-id ID --source dir:PATH|http(s)://... [--pubkey PEM] [flags]
//	    pull -> verify -> anti-rollback -> apply -> report (configured-source mode).
//
//	agent enroll --controller URL --node-id ID --token T [--token-out PATH] [flags]
//	    One-time enrollment against the networked controller (plan-4.5): ensure the
//	    WG key, then POST /enroll (unauthenticated, gated by the single-use token)
//	    with the node's WG PUBLIC key and persist the returned per-node bearer API
//	    token (0600). TLS, if any, is terminated by a reverse proxy; the agent speaks
//	    plain HTTP to the URL it is given.
//
//	agent reprovision-keystone --operator-cred FILE|- --operator-cred-alg ALG [--cred-out PATH] [--restart]
//	    Adopt a ROTATED off-host operator credential supplied OUT OF BAND: validate the
//	    NEW public key parses for the given alg, atomically rewrite the pinned PEM (0600),
//	    then (by default) restart yaog-agent so the daemon re-reads it. Never fetches or
//	    auto-trusts a controller-supplied key; same-alg rotation only (see the flag help).
//
//	agent run --controller URL --token PATH --node-id ID [flags]
//	    Controller mode: load the per-node bearer token, long-poll /poll for a new
//	    generation, then run the same pull -> verify -> apply -> report loop against
//	    the controller's /config + /report. (v1 applies one pending generation per
//	    invocation; a production daemon loops — see runControllerMode.)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

// BuildVersion is the agent's build version, overwritten at release link time via
// -ldflags "-X main.BuildVersion=<tag>" (see RELEASING.md). A non-release build reports "dev".
// It is printed by `agent version` and reported to the controller on every check-in.
var BuildVersion = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "keygen":
		os.Exit(runKeygen(os.Args[2:]))
	case "kit":
		os.Exit(runKit(os.Args[2:]))
	case "enroll":
		os.Exit(runEnroll(os.Args[2:]))
	case "reprovision-keystone":
		os.Exit(runReprovisionKeystone(os.Args[2:]))
	case "run":
		os.Exit(runRun(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Println(BuildVersion)
		os.Exit(0)
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
	fmt.Fprintln(os.Stderr, "usage: agent <keygen|kit|enroll|reprovision-keystone|run> [flags]")
	fmt.Fprintln(os.Stderr, "  keygen               ensure the local WireGuard private key exists and print its public key")
	fmt.Fprintln(os.Stderr, "  kit                  provision a MANUAL (agent-less) node: ensure the key + print a descriptor to paste into the design")
	fmt.Fprintln(os.Stderr, "  enroll               enroll against the networked controller and persist the per-node bearer token")
	fmt.Fprintln(os.Stderr, "  reprovision-keystone adopt a ROTATED off-host operator credential supplied out of band (rewrites the pinned PEM + restarts)")
	fmt.Fprintln(os.Stderr, "  run                  pull -> verify -> anti-rollback -> apply -> report (configured-source or --controller mode)")
}

// defaultTokenPath is where enrollment writes (and run reads) the per-node bearer
// API token. It sits alongside the WG key under /etc/wireguard so the agent's
// secrets share one custody-gated directory.
const defaultTokenPath = "/etc/wireguard/agent-controller.token"

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

// manualNodeDescriptor is the one-shot kit's output: the identity an operator pastes into the
// controller design for a MANUAL (deployment_mode=manual, hand-deployed) node. It carries the PUBLIC
// half only — the private key never leaves the box. node_id and wireguard_public_key mirror the
// model.Node field names; endpoint is a flat host:port PASTE HINT (the operator enters it as the
// node's public_endpoints[0] — model.Node has no flat endpoint field), omitted when not supplied.
type manualNodeDescriptor struct {
	NodeID    string `json:"node_id"`
	PublicKey string `json:"wireguard_public_key"`
	Endpoint  string `json:"endpoint,omitempty"`
}

// runKit implements `agent kit`: the one-shot on-box provisioning helper for a MANUAL node in a
// controller topology (mixed-controller-local-mode plan-4). A manual node has no agent and never
// enrolls; the operator hand-deploys it. The kit (1) ensures the on-box WireGuard key at --key — the
// SAME file the node's controller-rendered install.sh later splices over PRIVATEKEY_PLACEHOLDER at
// install time (AgentHeld custody) — and (2) prints a DESCRIPTOR {node_id, wireguard_public_key,
// endpoint} the operator pastes into the node's manual identity in the design. The private key NEVER
// leaves the box and the kit does NOT contact the controller (it is not an enroll, mints no bearer
// token, pulls no config). After pasting, the operator stages+promotes and downloads this node's
// bundle (operator GET /manual-node-bundle?node=<id>); `sudo bash install.sh` then splices the on-box
// key automatically. No splice step is needed here — install.sh already does it.
func runKit(args []string) int {
	fs := flag.NewFlagSet("kit", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "the node id this host will have in the controller design (required)")
	endpoint := fs.String("endpoint", "", "this node's reachable WireGuard endpoint host:port (optional; set it for a manual node that accepts inbound)")
	keyPath := fs.String("key", agent.DefaultKeyPath, "path to the local WireGuard private-key file (mode 0600) — the same file install.sh splices")
	_ = fs.Parse(args)

	if *nodeID == "" {
		fmt.Fprintln(os.Stderr, "agent: kit: --node-id is required")
		return 2
	}
	// A malformed --endpoint is a warn-not-fail (it is a paste hint, not a wire field): surface it now
	// rather than letting it become an opaque design error after the operator pastes it.
	if *endpoint != "" {
		if _, _, err := net.SplitHostPort(*endpoint); err != nil {
			fmt.Fprintf(os.Stderr, "agent: kit: warning: --endpoint %q is not host:port (%v); passing it through as a paste hint\n", *endpoint, err)
		}
	}

	wgPub, created, err := agent.EnsureKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit: %v\n", err)
		return 1
	}
	if created {
		fmt.Fprintf(os.Stderr, "agent: kit: generated new key at %s\n", *keyPath)
	} else {
		fmt.Fprintf(os.Stderr, "agent: kit: reusing existing key at %s\n", *keyPath)
	}

	// Guidance to stderr; the machine-parseable descriptor (public half only) to stdout.
	fmt.Fprintln(os.Stderr, "agent: kit: paste this descriptor into the manual node's identity in the controller design,")
	fmt.Fprintln(os.Stderr, "agent: kit: then stage + promote and download this node's bundle; `sudo bash install.sh` splices the local key.")
	out, err := json.MarshalIndent(manualNodeDescriptor{NodeID: *nodeID, PublicKey: wgPub, Endpoint: *endpoint}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}

// runEnroll implements `agent enroll`: the one-time ceremony that turns a single-use
// enrollment token into a persisted per-node bearer API token. It (1) ensures the WG
// key and reads its public key, (2) POSTs /enroll (unauthenticated; the single-use
// token is the credential) with the node id + WG PUBLIC key, and (3) writes the
// returned bearer token to disk at 0600 (under a 0700 dir). Nothing secret is printed.
func runEnroll(args []string) int {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	controller := fs.String("controller", "", "controller agent base URL: http://host:port (TLS, if any, is at a reverse proxy)")
	nodeID := fs.String("node-id", "", "node identity to enroll as (must match the token's scope)")
	token := fs.String("token", "", "single-use enrollment token (delivered out-of-band)")
	keyPath := fs.String("key", agent.DefaultKeyPath, "path to the local WireGuard private-key file (mode 0600)")
	tokenOut := fs.String("token-out", defaultTokenPath, "where to write the issued per-node bearer token (mode 0600)")
	_ = fs.Parse(args)

	switch {
	case *controller == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --controller is required")
		return 2
	case *nodeID == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --node-id is required")
		return 2
	case *token == "":
		fmt.Fprintln(os.Stderr, "agent: enroll: --token is required")
		return 2
	}

	// (1) Ensure the WG key and read its public key (registered as-is on enroll).
	wgPub, _, err := agent.EnsureKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}

	// (2) Enroll over a token-less client (the /enroll shape): the single-use token
	// is the authentication; no bearer credential exists yet.
	client, err := agent.NewControllerClient(*controller, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}
	result, err := client.Enroll(*token, *nodeID, wgPub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}

	// (3) Persist the per-node bearer token, 0600 under a 0700 dir. The token is a
	// secret: it is never printed or logged.
	if err := writeTokenFile(*tokenOut, result.APIToken); err != nil {
		fmt.Fprintf(os.Stderr, "agent: enroll: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "agent: enrolled node %q; bearer token written to %s\n", *nodeID, *tokenOut)
	return 0
}

// defaultOperatorCredPath is where the bootstrap writes (and run reads) the pinned off-host
// operator credential, and where reprovision-keystone rewrites it on a rotation.
const defaultOperatorCredPath = "/etc/wireguard/operator-cred.pem"

// runReprovisionKeystone implements `agent reprovision-keystone`: the guided, single-action
// adoption of a ROTATED keystone. The operator delivers the NEW credential PUBLIC key OUT OF BAND
// (a local file or stdin) — exactly as the original bootstrap delivered it — and this rewrites the
// pinned PEM (validate-before-atomic-write, fail-closed) then restarts the daemon so it re-reads
// the new credential. It NEVER fetches a credential from the controller, so the off-host trust
// anchor is never bridged automatically.
func runReprovisionKeystone(args []string) int {
	fs := flag.NewFlagSet("reprovision-keystone", flag.ExitOnError)
	credPath := fs.String("operator-cred", "", "path to the NEW operator credential public-key PEM, supplied out of band ('-' reads stdin) [required]")
	alg := fs.String("operator-cred-alg", "", "operator credential algorithm: ed25519 | webauthn-es256 | webauthn-eddsa [required]. MUST match the alg the running daemon was started with (the ExecStart --operator-cred-alg); reprovision rewrites only the PEM, not the unit, so adopting a DIFFERENT-alg (or different rpid/origin) keystone needs a fresh bootstrap / unit edit instead.")
	credOut := fs.String("cred-out", defaultOperatorCredPath, "where to write the pinned credential the daemon reads")
	restart := fs.Bool("restart", true, "restart the yaog-agent systemd service so the running daemon re-reads the new credential")
	_ = fs.Parse(args)

	switch {
	case *credPath == "":
		fmt.Fprintln(os.Stderr, "agent: reprovision-keystone: --operator-cred is required (the NEW public key, supplied out of band)")
		return 2
	case *alg == "":
		fmt.Fprintln(os.Stderr, "agent: reprovision-keystone: --operator-cred-alg is required")
		return 2
	}

	var newPEM []byte
	var err error
	if *credPath == "-" {
		newPEM, err = io.ReadAll(os.Stdin)
	} else {
		newPEM, err = os.ReadFile(*credPath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: reprovision-keystone: read credential: %v\n", err)
		return 1
	}

	if err := agent.ReprovisionKeystone(*credOut, *alg, newPEM); err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	// The credential public key is non-secret, but print only its fingerprint (not the body) to keep
	// the operator's terminal scrollback clean and comparable to the controller's GET-status fingerprint.
	fmt.Fprintf(os.Stderr, "agent: pinned operator credential rewritten at %s (fingerprint %s)\n", *credOut, agent.CredFingerprintShort(newPEM))

	if !*restart {
		fmt.Fprintln(os.Stderr, "agent: --restart=false: the RUNNING daemon still holds the previous credential in memory; restart yaog-agent for the new pin to take effect")
		return 0
	}
	// The daemon reads the pinned credential once at process start, so it must be (re)started to
	// re-read it. Use `restart` (NOT `try-restart`): try-restart is a benign no-op (exit 0) for a
	// loaded-but-STOPPED unit, which would print success while no daemon is actually running the new
	// pin (a silent split-brain). `restart` STARTS a stopped unit and restarts a running one, so the
	// daemon always ends up running the new pin; it exits non-zero only when the unit is not loaded
	// at all (a --once / non-systemd host, or systemctl absent) — which we surface loudly so the
	// operator restarts the agent themselves.
	cmd := exec.Command("systemctl", "restart", "yaog-agent.service")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "agent: WARNING: the pin was rewritten but yaog-agent could not be restarted (%v); if a daemon is running it still holds the OLD key in memory — start/restart yaog-agent yourself (e.g. `systemctl restart yaog-agent`)\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "agent: yaog-agent restarted; it now verifies membership against the new credential")
	return 0
}

// writeTokenFile persists the per-node bearer token to disk at mode 0600, creating
// the parent dir 0700. The token is a secret; the file is world-unreadable.
func writeTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	return nil
}

// runRun implements `agent run`. With --controller it runs controller mode (bearer
// poll/config/report); otherwise it runs the configured-source mode (--source).
func runRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	nodeID := fs.String("node-id", "", "configured node identity (bundle subdir / state key)")
	sourceSpec := fs.String("source", "", "bundle source: dir:PATH or http(s)://... (configured-source mode)")
	pubkeyPath := fs.String("pubkey", "", "path to the pinned signing public-key PEM (optional; when set, a signature is required)")
	operatorCredPath := fs.String("operator-cred", "", "path to the off-host operator credential public-key PEM (optional; when set, the keystone trust-list gate is enforced)")
	operatorCredAlg := fs.String("operator-cred-alg", "", "operator credential algorithm: ed25519 | webauthn-es256 | webauthn-eddsa (required with --operator-cred)")
	operatorRPID := fs.String("operator-rpid", "", "operator credential WebAuthn relying-party ID (WebAuthn algs only)")
	operatorOrigin := fs.String("operator-origin", "", "operator credential WebAuthn origin (WebAuthn algs only; advisory on a node)")
	stateDir := fs.String("state-dir", agent.DefaultStateDir, "directory for the agent's persisted state")
	stagingDir := fs.String("staging-dir", "", "directory to materialize the verified bundle (default: a fresh temp dir)")
	controller := fs.String("controller", "", "controller agent base URL (controller mode): http://host:port")
	tokenPath := fs.String("token", defaultTokenPath, "path to the per-node bearer token file (controller mode)")
	after := fs.Int64("after", 0, "controller mode: poll for a generation strictly greater than this (the last applied generation; a daemon advances it each cycle)")
	daemon := fs.Bool("daemon", false, "controller mode: keep running, continuously long-polling and applying new generations (default: a single poll->apply->report cycle)")
	ghProxy := fs.String("gh-proxy", "", "controller mode: optional GitHub download proxy prefix for signed agent self-update (e.g. https://gh-proxy.com/)")
	telemetryInterval := fs.Duration("telemetry-interval", 30*time.Second, "controller daemon mode: how often to send a live health heartbeat (POST /telemetry: node conditions + last-seen, never deploy state). 0 or less disables the heartbeat.")
	selfUpdateRetryInterval := fs.Duration("selfupdate-retry-interval", 10*time.Minute, "controller daemon mode: how often to re-attempt a DEFERRED agent self-update (a download that failed on a stable generation) without waiting for a new generation or a restart. 0 or less disables the retry.")
	_ = fs.Parse(args)

	if *nodeID == "" {
		fmt.Fprintln(os.Stderr, "agent: run: --node-id is required")
		return 2
	}

	// Controller mode takes precedence when --controller is set.
	if *controller != "" {
		return runControllerMode(controllerModeOpts{
			nodeID:                  *nodeID,
			baseURL:                 *controller,
			tokenPath:               *tokenPath,
			pubkeyPath:              *pubkeyPath,
			operatorCredPath:        *operatorCredPath,
			operatorCredAlg:         *operatorCredAlg,
			operatorRPID:            *operatorRPID,
			operatorOrigin:          *operatorOrigin,
			stateDir:                *stateDir,
			stagingDir:              *stagingDir,
			after:                   *after,
			daemon:                  *daemon,
			ghProxy:                 *ghProxy,
			telemetryInterval:       *telemetryInterval,
			selfUpdateRetryInterval: *selfUpdateRetryInterval,
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

	operatorCred, err := readOperatorCred(*operatorCredPath, *operatorCredAlg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}

	// NOTE: the private-key splice path is fixed at agent.DefaultKeyPath
	// (/etc/wireguard/agent.key) inside the rendered install.sh, so `run` has no --key
	// flag; `agent keygen` writes the key there. Config.KeyPath is left empty (unused by Run).
	cfg := &agent.Config{
		NodeID:          *nodeID,
		Source:          src,
		PinnedPubPEM:    pinned,
		OperatorCredPEM: operatorCred,
		OperatorCredAlg: *operatorCredAlg,
		OperatorRPID:    *operatorRPID,
		OperatorOrigin:  *operatorOrigin,
		StateDir:        *stateDir,
		StagingDir:      *stagingDir,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
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
	nodeID           string
	baseURL          string
	tokenPath        string
	pubkeyPath       string
	operatorCredPath string
	operatorCredAlg  string
	operatorRPID     string
	operatorOrigin   string
	stateDir         string
	stagingDir       string
	// after is the resume cursor: poll for a generation strictly greater than this.
	// A production daemon advances it from each applied generation; the single-shot
	// CLI takes it as a flag (default 0) since the agent State has no numeric
	// generation field to persist it in.
	after int64
	// daemon keeps the cycle looping (continuous long-poll); false runs one cycle.
	daemon bool
	// ghProxy is the optional GitHub download proxy prefix for signed agent self-update
	// (plan-9), baked into the systemd unit by the bootstrap when configured. Empty = direct.
	ghProxy string
	// telemetryInterval is how often the DAEMON sends a live health heartbeat (POST /telemetry).
	// 0 or less disables it. Single-shot runs never heartbeat (their one /report carries apply-time
	// conditions). Default 30s (set in the run flag). beta9-smoke-hardening plan-1.
	telemetryInterval time.Duration
	// selfUpdateRetryInterval is how often the DAEMON re-attempts a DEFERRED self-update (a download
	// that failed on a stable generation) without waiting for a new generation or a restart. 0 or less
	// disables it. Default 10m (set in the run flag). plan-8.
	selfUpdateRetryInterval time.Duration
}

// runControllerMode drives controller-pull deploys: load the per-node bearer token,
// long-poll /poll for a generation newer than the last applied one, then (on a change)
// drive agent.Run against the controller's /config + /report. Run does verify +
// apply(install.sh) + report; this function only sequences poll->run and tracks the
// applied generation.
//
// With --daemon it loops continuously (near-real-time: long-poll returns within a
// round-trip of a promote; keep-last-good with backoff on error). Without it, it runs a
// single poll->apply->report cycle — the deterministic unit the daemon loops over, and
// what the e2e test exercises.
func runControllerMode(o controllerModeOpts) int {
	// PHASE A of the self-update reconcile (plan-9), the VERY FIRST thing: bump the pending-update
	// attempt counter crash-durably and abandon (roll back to .bak) at the cap, BEFORE any
	// crash-prone setup (token/client/pubkey reads) below. This is what bounds the systemd
	// Restart=always loop even for a swapped binary that crashes during early init. No-op without a
	// breadcrumb; re-execs the rolled-back binary on abandon (never returns in that case).
	agent.ReconcileSelfUpdateEarly(o.stateDir, BuildVersion, os.Stderr)

	tokenBytes, err := os.ReadFile(o.tokenPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: read controller token %s: %v\n", o.tokenPath, err)
		fmt.Fprintln(os.Stderr, "agent: run enroll first to obtain a per-node bearer token")
		return 1
	}
	token := string(trimToken(tokenBytes))
	if token == "" {
		fmt.Fprintf(os.Stderr, "agent: controller token %s is empty; run enroll first\n", o.tokenPath)
		return 1
	}

	client, err := agent.NewControllerClient(o.baseURL, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	// Report this binary's build version on every check-in so the controller + panel show
	// each node's running version (plan-4 observability; the self-update floor is plan-9).
	client.AgentVersion = BuildVersion

	pinned, err := readPinnedPubkey(o.pubkeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}

	operatorCred, err := readOperatorCred(o.operatorCredPath, o.operatorCredAlg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}

	// Self-update (plan-9): the running build version is the comparison baseline; the optional
	// GitHub proxy is the download prefix. Nil-safe — an empty BuildVersion ("dev") just means a
	// pinned target always compares as newer (a dev binary updates to any real target).
	selfUpdate := &agent.SelfUpdateParams{RunningVersion: BuildVersion, GithubProxy: o.ghProxy}

	// PHASE B of the self-update reconcile: now that the client + pinned key exist, health-gate a
	// swapped binary. The gate is one clean Fetch + VerifyBundle — it proves THIS (possibly
	// just-swapped) binary can reach the controller and cryptographically verify a bundle. A pass
	// marks the update PROBATIONARY (FinalizeSelfUpdate promotes it after the first clean cycle
	// below); a failure (or a reboot during probation) rolls back to the prior binary. No-op
	// without a breadcrumb.
	// verifiedFetch returns the cryptographically VERIFIED served bundle (Fetch + VerifyBundle). It is
	// the shared primitive for BOTH the self-update reconcile health-gate AND the deferred-self-update
	// retry (plan-8), so every self-update decision re-fetches + re-verifies — a swap never acts on
	// stale or unverified pins.
	verifiedFetch := func() (map[string][]byte, error) {
		files, ferr := client.Fetch(o.nodeID)
		if ferr != nil {
			return nil, fmt.Errorf("fetch: %w", ferr)
		}
		if _, verr := agent.VerifyBundle(files, pinned); verr != nil {
			return nil, fmt.Errorf("verify: %w", verr)
		}
		return files, nil
	}
	healthCheck := func() error {
		_, err := verifiedFetch()
		return err
	}
	agent.ReconcileSelfUpdatePromote(o.stateDir, BuildVersion, healthCheck, os.Stderr)

	// Resume from the supplied cursor so a re-run does not re-fetch an already-applied
	// generation: long-poll for anything strictly newer than --after. (The agent State
	// keys anti-rollback on the manifest compiled_at string, not the controller's int64
	// generation, so the cursor is a flag here; a daemon advances it per cycle.)
	lastAppliedGen := o.after

	// cycle runs ONE poll->apply->report iteration from the current watermark via the
	// testable agent.RunControllerCycle (the deterministic unit the daemon loops over).
	// It returns the generation to resume from (the applied/fetched generation on
	// success, the polled wake generation on a rekey wake (so the stale pre-rekey bundle is
	// never re-applied, or the unchanged watermark on a timed-out long-poll) and whether
	// a new generation was applied. On error it returns the unchanged watermark
	// (keep-last-good: the running overlay is untouched, so the caller never advances
	// past a failed apply).
	cycle := func() (resumeGen int64, applied bool, err error) {
		return agent.RunControllerCycle(client, agent.CycleConfig{
			NodeID:          o.nodeID,
			After:           lastAppliedGen,
			PinnedPubPEM:    pinned,
			OperatorCredPEM: operatorCred,
			OperatorCredAlg: o.operatorCredAlg,
			OperatorRPID:    o.operatorRPID,
			OperatorOrigin:  o.operatorOrigin,
			StateDir:        o.stateDir,
			StagingDir:      o.stagingDir,
			KeyPath:         agent.DefaultKeyPath,
			Stdout:          os.Stdout,
			Stderr:          os.Stderr,
			SelfUpdate:      selfUpdate,
		})
	}

	if !o.daemon {
		// Single-shot: one cycle (deterministic — the unit the daemon loops over).
		resumeGen, applied, err := cycle()
		// FINALIZE a probationary self-update: the cycle returned, proving this (possibly
		// just-swapped) binary can actually RUN a full cycle, not merely pass `version`+verify.
		// No-op unless a Confirmed breadcrumb for this build exists.
		agent.FinalizeSelfUpdate(o.stateDir, BuildVersion, os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			return 1
		}
		if !applied && resumeGen == lastAppliedGen {
			// A timed-out long-poll (no advance). A rekey wake advances resumeGen and is
			// logged by RunControllerCycle, so do not print "nothing to do" over a rotation.
			fmt.Fprintf(os.Stderr, "agent: no new generation (still at %d); nothing to do\n", resumeGen)
		}
		return 0
	}

	// Daemon: loop the cycle for continuous, near-real-time updates. The long-poll
	// returns within a round-trip of a promote (so this is push-like without a new
	// transport); a timed-out poll simply re-polls with no busy-wait. On a transport or
	// apply error we keep last-good and retry after a short backoff — never tearing down
	// the running overlay. On a rekey wake the watermark advances to the polled wake
	// generation (so the stale pre-rekey bundle is never re-applied); the next applied
	// generation is the operator's post-rekey Deploy.
	//
	// A cycle that advanced the watermark WITHOUT applying (a rekey wake, or the
	// plan-3 idle skip when the served bundle is already applied — the orphaned-node
	// shape) also sleeps the backoff: both await an operator action, and the pause
	// bounds the wake-fetch rate even if the tenant generation is advancing rapidly
	// for OTHER nodes. install.sh never runs in those cycles.
	const errBackoff = 5 * time.Second
	fmt.Fprintf(os.Stderr, "agent: controller daemon started (node %s, resume @%d)\n", o.nodeID, lastAppliedGen)

	// LIVE health heartbeat (beta9-smoke-hardening plan-1): a DEDICATED goroutine re-samples the node's
	// conditions on an interval and POSTs them to /telemetry, so the panel reflects CURRENT health
	// instead of the frozen apply-time snapshot (a pre-handshake wireguard:LinkDown, a mid-probation
	// selfupdate). It is decoupled from the poll loop — it reads only the agent's persisted State + a
	// read-only `wg show`, and the client's immutable fields, carrying no generation — so it needs no
	// lock. It is daemon-only (a single-shot run's one /report still carries apply-time conditions). No
	// context/cancel is needed: the daemon loop below never returns, and a self-update swap is
	// syscall.Exec (which replaces the whole process image and destroys this goroutine with it).
	if o.telemetryInterval > 0 {
		go runHeartbeat(client, agent.BuildTelemetry(o.stateDir), o.telemetryInterval, os.Stderr)
	}

	// lastSelfUpdateRetry paces the deferred-self-update retry (plan-8): a download that failed on a
	// stable generation is re-attempted on idle cycles every selfUpdateRetryInterval, so a stalled
	// rollout recovers without a new generation or a manual restart. Initialized to now so the first
	// retry waits one interval after start (the apply path already made the initial attempt).
	lastSelfUpdateRetry := time.Now()
	finalizedSelfUpdate := false
	for {
		resumeGen, applied, err := cycle()
		// FINALIZE a probationary self-update after the FIRST completed cycle (once): the cycle
		// returning proves this (possibly just-swapped) binary RUNS its daemon loop, not merely
		// that `version`+verify pass — so a daemon-only crash AFTER the health gate cannot brick
		// (it reboots Confirmed-but-unfinalized and rolls back). No-op without a Confirmed
		// breadcrumb for this build.
		if !finalizedSelfUpdate {
			agent.FinalizeSelfUpdate(o.stateDir, BuildVersion, os.Stderr)
			finalizedSelfUpdate = true
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v (keeping last-good; retrying in %s)\n", err, errBackoff)
			time.Sleep(errBackoff)
			continue
		}
		if !applied && resumeGen > lastAppliedGen {
			time.Sleep(errBackoff) // idle/rekey wake: pace before re-polling
		}
		lastAppliedGen = resumeGen // advance on success, idle skip, or rekey wake; unchanged on a timed-out poll

		// Deferred self-update retry (plan-8): re-attempt a self-update that a prior cycle deferred
		// (e.g. a gh-proxy download timeout) WITHOUT waiting for a new generation. Idle cycles only —
		// an apply already ran the post-apply attempt (agent.go step 7) — paced by the interval, on
		// the MAIN thread so a swap (syscall.Exec) never interrupts a mid-flight apply. A no-op unless
		// State.SelfUpdateBlocked is armed, so calling it past the backoff is cheap when nothing is due.
		// (selfUpdate is always non-nil in controller daemon mode; RetryDeferredSelfUpdate is nil-safe
		// regardless, so the interval gate is the only guard needed here.)
		if o.selfUpdateRetryInterval > 0 && !applied &&
			time.Since(lastSelfUpdateRetry) >= o.selfUpdateRetryInterval {
			lastSelfUpdateRetry = time.Now()
			if _, suErr := agent.RetryDeferredSelfUpdate(selfUpdate, o.nodeID, o.stateDir, verifiedFetch, os.Stderr); suErr != nil {
				fmt.Fprintf(os.Stderr, "agent: deferred self-update retry: %v (will retry)\n", suErr)
			}
		}
	}
}

// runHeartbeat is the daemon's LIVE health heartbeat loop (beta9-smoke-hardening plan-1). It samples
// the registered telemetry probes and POSTs the result to /telemetry every `interval`, plus once
// immediately so a node's current health shows within a round-trip rather than after a full interval.
// Best-effort: a transport error is logged and swallowed (never disturbs the running overlay / poll
// loop). It skips a post when there is nothing to report — a never-applied node, or a transient
// State-read failure — so a momentary empty sample never WIPES the node's last-known conditions
// (the controller replaces the set wholesale; an applied node always yields at least the configapply
// condition, so this only ever skips genuinely-empty samples). Runs until the process exits/exec's.
func runHeartbeat(client *agent.ControllerClient, tel *agent.Telemetry, interval time.Duration, stderr io.Writer) {
	beat := func() {
		// A panic anywhere in a beat (a sampler outside its own guard, the merge, or the POST) must
		// NOT kill this goroutine — it is the ONLY thing that refreshes Last Seen after apply time, so
		// a silent death would freeze the controller's view of a live node. Recover, log, and let the
		// next tick try again (beta.16 heartbeat-resilience hardening).
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(stderr, "agent: telemetry heartbeat: recovered from panic: %v\n", r)
			}
		}()
		conds, metrics := tel.Collect(time.Now().UTC())
		if len(conds) == 0 && len(metrics) == 0 {
			return
		}
		if err := client.Telemetry(conds, metrics); err != nil {
			fmt.Fprintf(stderr, "agent: telemetry heartbeat: %v\n", err)
		}
	}
	beat()
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		beat()
	}
}

// trimToken strips surrounding whitespace/newlines from a token file's contents so
// an editor-added trailing newline does not corrupt the bearer header.
func trimToken(b []byte) []byte {
	start := 0
	for start < len(b) && isSpaceByte(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

// isSpaceByte reports whether b is an ASCII whitespace byte (space, tab, CR, LF).
func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
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

// readOperatorCred reads the optional off-host operator credential PEM that turns the
// keystone trust-list gate ON. An empty path means keystone OFF (opt-in: the agent
// applies exactly as before, no trust-list). A non-empty path REQUIRES a matching
// --operator-cred-alg, mirroring the readPinnedPubkey discipline; a read error or a
// missing alg is fatal so a misconfigured agent fails loudly rather than silently
// skipping the membership check.
func readOperatorCred(path, alg string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	if alg == "" {
		return nil, fmt.Errorf("--operator-cred-alg is required when --operator-cred is set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read operator credential %s: %w", path, err)
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
