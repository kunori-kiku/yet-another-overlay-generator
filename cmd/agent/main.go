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
//	agent kit verify --bundle DIR|ZIP --node-id ID [--pubkey PEM] [--operator-cred FILE --operator-cred-alg ALG ...]
//	    Verify an already-downloaded manual-node bundle (Ed25519 signature + per-file
//	    SHA-256, then keystone membership). Reads public material only; no controller contact.
//	    Legacy bundles without a keystone require --dangerously-allow-no-keystone.
//	    Exit 0 verified / 1 verification failed / 2 usage or IO.
//
//	agent kit apply --bundle DIR|ZIP --node-id ID [--uninstall] [--state-dir DIR] [--pubkey PEM] [--operator-cred FILE --operator-cred-alg ALG ...]
//	    Trusted manual-node apply: require an out-of-band operator credential whenever
//	    trust-list files are present (and by default even if they were stripped), verify
//	    the loaded snapshot, copy it into an owned temporary directory, and execute only
//	    that copied install.sh. Legacy no-keystone bundles require the explicit dangerous
//	    acknowledgement flag. Run with sudo; never invoke the downloaded script directly.
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
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
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
		// Manual bundles are never executed from their downloaded path. `kit apply` loads and
		// verifies a snapshot, stages that map in an owned temp dir, then invokes the copy.
		if len(os.Args) > 2 {
			switch os.Args[2] {
			case "verify":
				os.Exit(runKitVerify(os.Args[3:]))
			case "apply":
				os.Exit(runKitApply(os.Args[3:]))
			}
		}
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
	fmt.Fprintln(os.Stderr, "  kit verify           verify a downloaded manual-node bundle; never run the download directly")
	fmt.Fprintln(os.Stderr, "  kit apply            verify a manual bundle, stage a trusted temp snapshot, then install/uninstall via its copied install.sh (use sudo)")
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
// bundle (operator GET /manual-node-bundle?node=<id>), then runs `sudo yaog-agent kit apply` over
// that download. The trusted apply path verifies, copies, and only then runs install.sh, whose
// custody splice reads the on-box key automatically. No separate splice step is needed.
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
	fmt.Fprintln(os.Stderr, "agent: kit: then stage + promote and download this node's bundle; do NOT run its install.sh directly.")
	fmt.Fprintln(os.Stderr, "agent: kit: run `sudo yaog-agent kit apply --bundle <DIR|ZIP> --node-id <id> ...`; it verifies a temp snapshot before the local-key splice.")
	out, err := json.MarshalIndent(manualNodeDescriptor{NodeID: *nodeID, PublicKey: wgPub, Endpoint: *endpoint}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit: %v\n", err)
		return 1
	}
	fmt.Println(string(out))
	return 0
}

// kitVerifyResult is the machine-parseable stdout of `kit verify` (human summary goes to stderr).
type kitVerifyResult struct {
	OK           bool  `json:"ok"`
	Signed       bool  `json:"signed"`
	FileCount    int   `json:"file_count"`
	Epoch        int64 `json:"epoch"`
	NodeIsMember bool  `json:"node_is_member"`
}

// runKitVerify implements `agent kit verify`: it runs the SAME fail-closed verification a managed
// agent applies before install (materialization preflight, VerifyBundle, then VerifyMembership) over
// an already-DOWNLOADED manual-node bundle, so an operator can audit a tampered install.sh / rotated
// keystone before applying it with `kit apply`. It never contacts the controller and reads only
// public material. Exit 0 = verified, 1 = verification failed, 2 = usage/IO error.
func runKitVerify(args []string) int {
	fs := flag.NewFlagSet("kit verify", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to the downloaded bundle: a directory of extracted files OR a .zip (required)")
	nodeID := fs.String("node-id", "", "this node's id (for the keystone membership + bundle-digest binding; required)")
	pubkeyPath := fs.String("pubkey", "", "pinned bundle-signing public-key PEM (optional; absent = trust-on-first-supply, like the agent)")
	credPath := fs.String("operator-cred", "", "out-of-band operator credential public-key PEM (required by default; only legacy bundles may use --dangerously-allow-no-keystone)")
	credAlg := fs.String("operator-cred-alg", "", "operator credential algorithm (ed25519 | webauthn-es256 | webauthn-eddsa) — required with --operator-cred")
	credRPID := fs.String("operator-rpid", "", "operator credential WebAuthn relying-party ID (WebAuthn algs only)")
	credOrigin := fs.String("operator-origin", "", "operator credential WebAuthn origin (WebAuthn algs only)")
	allowNoKeystone := fs.Bool("dangerously-allow-no-keystone", false, "explicitly accept a legacy bundle with no off-host-signed membership (unsafe; rejected if trust-list files are present)")
	_ = fs.Parse(args)

	if *bundlePath == "" || *nodeID == "" {
		fmt.Fprintln(os.Stderr, "agent: kit verify: --bundle and --node-id are required")
		return 2
	}
	files, err := loadBundleFiles(*bundlePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit verify: %v\n", err)
		return 2
	}
	if err := agent.PreflightBundleMaterialization(files); err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit verify: bundle materialization preflight FAILED: %v\n", err)
		return 1
	}
	pinned, err := readPinnedPubkey(*pubkeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit verify: %v\n", err)
		return 2
	}
	operatorCred, err := readOperatorCred(*credPath, *credAlg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit verify: %v\n", err)
		return 2
	}
	if bundleCarriesTrustList(files) && len(operatorCred) == 0 {
		fmt.Fprintln(os.Stderr, "agent: kit verify: bundle carries a keystone trust list; --operator-cred and --operator-cred-alg are required so membership is verified against an out-of-band credential")
		return 2
	}
	if len(operatorCred) == 0 && !*allowNoKeystone {
		fmt.Fprintln(os.Stderr, "agent: kit verify: an out-of-band --operator-cred and --operator-cred-alg are required by default; for a legacy bundle that has never used the keystone, explicitly acknowledge the downgrade with --dangerously-allow-no-keystone")
		return 2
	}
	vr, err := agent.VerifyBundle(files, pinned)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit verify: bundle verification FAILED: %v\n", err)
		return 1
	}
	// prevEpoch = 0: a pre-install human check is stateless (it tracks no persisted anti-rollback floor,
	// so 0 accepts any epoch >= 0). The operator controls what they downloaded.
	epoch, err := agent.VerifyMembership(files, agent.MembershipConfig{
		NodeID:          *nodeID,
		OperatorCredPEM: operatorCred,
		OperatorCredAlg: *credAlg,
		OperatorRPID:    *credRPID,
		OperatorOrigin:  *credOrigin,
	}, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit verify: keystone membership verification FAILED: %v\n", err)
		return 1
	}

	member := len(operatorCred) > 0 // the keystone gate actually ran (an operator credential was pinned)
	// Report vr.FileCount (the number of files whose SHA-256 was actually verified against
	// checksums.sha256), NOT len(files) — the latter also counts the unchecked meta-files (bundle.sig,
	// signing-pubkey.pem, trustlist.*) and would overstate coverage + disagree with `agent run`.
	if member {
		fmt.Fprintf(os.Stderr, "agent: kit verify: OK — %d files verified, signed=%v, membership verified (epoch %d, operator-cred %s)\n",
			vr.FileCount, vr.Signed, epoch, agent.CredFingerprintShort(operatorCred))
	} else {
		fmt.Fprintf(os.Stderr, "agent: kit verify: OK — %d files verified, signed=%v; keystone OFF (no --operator-cred, membership NOT checked)\n",
			vr.FileCount, vr.Signed)
	}
	out, _ := json.Marshal(kitVerifyResult{OK: true, Signed: vr.Signed, FileCount: vr.FileCount, Epoch: epoch, NodeIsMember: member})
	fmt.Println(string(out))
	return 0
}

// loadedKitBundleSource is an immutable in-memory Source for `kit apply`. loadBundleFiles has
// already copied the untrusted DIR/ZIP into this map; Fetch returns another deep copy so agent.Run
// can only stage and execute the captured snapshot, never reopen the attacker-controlled source
// path after verification.
type loadedKitBundleSource struct {
	files map[string][]byte
}

func (s loadedKitBundleSource) Fetch(string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(s.files))
	for name, content := range s.files {
		out[name] = append([]byte(nil), content...)
	}
	return out, nil
}

// runKitApply implements the trusted manual-node install path. Exit codes deliberately mirror
// `kit verify`: 2 is a usage/input-IO problem, 1 means the loaded candidate failed verification or
// its verified install.sh failed, and 0 means the verified temporary snapshot applied successfully.
//
// The command never passes the downloaded path to bash. It reads the complete candidate into
// memory and requires an out-of-band operator credential by default, rather than trusting the
// attacker-controlled presence of trust-list files (a never-keystoned legacy bundle needs the loud
// opt-out). It preflights the captured path set, then delegates to agent.Run, which re-runs the
// preflight before VerifyBundle -> VerifyMembership -> rollback check -> fresh-temp stage -> apply.
// Only bytes from the verified map reach the root shell, and durable state is checked before and
// after. The normal AgentHeld install.sh still performs the /etc/wireguard/agent.key placeholder
// splice.
func runKitApply(args []string) int {
	return runKitApplyWithStateSaver(args, nil)
}

// runKitApplyWithStateSaver is runKitApply with the final state-persistence seam
// exposed for focused failure testing. Production passes nil and uses agent.SaveState.
func runKitApplyWithStateSaver(args []string, stateSaver func(string, *agent.State) error) int {
	fs := flag.NewFlagSet("kit apply", flag.ExitOnError)
	bundlePath := fs.String("bundle", "", "path to the downloaded bundle: a directory of extracted files OR a .zip (required; never executed in place)")
	nodeID := fs.String("node-id", "", "this manual node's id (required; must match manifest and signed membership)")
	pubkeyPath := fs.String("pubkey", "", "out-of-band pinned bundle-signing public-key PEM (recommended; when set, a signature is required)")
	credPath := fs.String("operator-cred", "", "out-of-band operator credential public-key PEM (required by default; only never-keystoned legacy state may use --dangerously-allow-no-keystone)")
	credAlg := fs.String("operator-cred-alg", "", "operator credential algorithm (ed25519 | webauthn-es256 | webauthn-eddsa) — required with --operator-cred")
	credRPID := fs.String("operator-rpid", "", "operator credential WebAuthn relying-party ID (WebAuthn algs only)")
	credOrigin := fs.String("operator-origin", "", "operator credential WebAuthn origin (WebAuthn algs only)")
	stateDir := fs.String("state-dir", agent.DefaultStateDir, "durable directory for manual-apply anti-rollback state (must remain stable across applies)")
	uninstall := fs.Bool("uninstall", false, "verify the bundle and signed membership, then invoke the verified copy as install.sh --uninstall")
	allowNoKeystone := fs.Bool("dangerously-allow-no-keystone", false, "explicitly apply a legacy bundle with no off-host-signed membership (unsafe; rejected if trust-list files are present or this state has ever used the keystone)")
	_ = fs.Parse(args)

	if *bundlePath == "" || *nodeID == "" || strings.TrimSpace(*stateDir) == "" {
		fmt.Fprintln(os.Stderr, "agent: kit apply: --bundle, --node-id, and a non-empty --state-dir are required")
		return 2
	}
	files, err := loadBundleFiles(*bundlePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit apply: %v\n", err)
		return 2
	}
	if err := agent.PreflightBundleMaterialization(files); err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit apply: bundle materialization preflight FAILED: %v\n", err)
		return 1
	}
	pinned, err := readPinnedPubkey(*pubkeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit apply: %v\n", err)
		return 2
	}
	operatorCred, err := readOperatorCred(*credPath, *credAlg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit apply: %v\n", err)
		return 2
	}
	if bundleCarriesTrustList(files) && len(operatorCred) == 0 {
		fmt.Fprintln(os.Stderr, "agent: kit apply: bundle carries a keystone trust list; --operator-cred and --operator-cred-alg are required so membership is verified against an out-of-band credential")
		return 2
	}
	if len(operatorCred) == 0 && !*allowNoKeystone {
		fmt.Fprintln(os.Stderr, "agent: kit apply: an out-of-band --operator-cred and --operator-cred-alg are required by default; for a legacy bundle that has never used the keystone, explicitly acknowledge the downgrade with --dangerously-allow-no-keystone")
		return 2
	}
	if err := ensureKitStateWritable(*stateDir); err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit apply: anti-rollback state is not writable: %v\n", err)
		return 2
	}

	// State is intentionally durable: agent.Run persists both compiled-at and signed-membership epoch
	// floors here, so a later `kit apply` cannot replay an older authorized bundle. Leave StagingDir
	// empty so Run still creates and removes its own trusted staging directory rather than reusing the
	// download location. Run also refuses a state record belonging to another node identity.
	var installArgs []string
	if *uninstall {
		installArgs = []string{"--uninstall"}
	}

	res, err := agent.Run(&agent.Config{
		NodeID:          *nodeID,
		Source:          loadedKitBundleSource{files: files},
		PinnedPubPEM:    pinned,
		OperatorCredPEM: operatorCred,
		OperatorCredAlg: *credAlg,
		OperatorRPID:    *credRPID,
		OperatorOrigin:  *credOrigin,
		StateDir:        *stateDir,
		StateSaver:      stateSaver,
		StagingDir:      "",
		InstallArgs:     installArgs,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit apply: refused or failed: %v\n", err)
		return 1
	}
	if err := verifyKitAppliedState(*stateDir, *nodeID, operatorCred, res); err != nil {
		fmt.Fprintf(os.Stderr, "agent: kit apply: install.sh completed, but durable anti-rollback state verification FAILED: %v; do not apply another bundle until the state directory is repaired\n", err)
		return 1
	}
	agent.PrintAppliedTo(os.Stderr, res)
	return 0
}

func ensureKitStateWritable(stateDir string) error {
	if err := agent.EnsureSecureOwnedDir(stateDir); err != nil {
		return err
	}
	probe, err := os.CreateTemp(stateDir, ".yaog-kit-state-probe-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	return nil
}

func verifyKitAppliedState(stateDir, nodeID string, operatorCred []byte, res *agent.RunResult) error {
	if res == nil || !res.Applied {
		return fmt.Errorf("agent returned no successful apply result")
	}
	st, err := agent.LoadState(stateDir)
	if err != nil {
		return err
	}
	if st.NodeID != nodeID || st.LastResult != agent.LastResultOK || st.LastCompiledAt != res.CompiledAt || st.LastChecksum != res.Checksum {
		return fmt.Errorf("state does not record the applied node/bundle")
	}
	if res.Action != "" && st.LastAction != res.Action {
		return fmt.Errorf("state records action %q, want completed action %q", st.LastAction, res.Action)
	}
	if len(operatorCred) > 0 && (!st.MembershipVerified || st.MembershipEpoch < res.MembershipEpoch) {
		return fmt.Errorf("state does not record verified membership epoch %d", res.MembershipEpoch)
	}
	return nil
}

func bundleCarriesTrustList(files map[string][]byte) bool {
	_, hasManifest := files["trustlist.json"]
	_, hasSignature := files["trustlist.sig"]
	return hasManifest || hasSignature
}

// Manual bundles are small configuration artifacts, not software-distribution archives. Bound
// every input dimension before materializing the map so a local/untrusted DIR or a compressed ZIP
// cannot make a root-run `kit verify/apply` allocate unbounded memory. These ceilings leave orders
// of magnitude of headroom over normal bundles while keeping worst-case capture below 16 MiB.
const (
	maxKitBundleEntries     = 512
	maxKitBundleFileBytes   = 4 << 20
	maxKitBundleTotal       = 16 << 20
	maxKitBundleArchiveSize = 32 << 20
)

// loadBundleFiles reads a downloaded bundle into the filename->bytes map VerifyBundle/VerifyMembership
// expect, from either a DIRECTORY of extracted files or a .zip archive. Most bundle files are top-level,
// but some live in a subdir (wireguard/*.conf); each relative path / zip entry name is preserved as a
// SLASH-separated key (filepath.ToSlash) so those subpaths survive — VerifyMembership matches the
// "wireguard/" prefix and VerifyBundle's per-file checksums are keyed by these exact paths (so do NOT
// collapse to filepath.Base). Entries are mapped in memory only; nothing is written to disk (no zip-slip).
// Non-regular directory entries, entry-local unsafe/non-canonical names, and duplicate zip names are
// rejected here. Cross-entry and cross-platform aliases are rejected by the shared agent
// materialization preflight immediately after this capture returns.
func loadBundleFiles(path string) (map[string][]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read bundle %s: %w", path, err)
	}
	files := map[string][]byte{}
	if info.IsDir() {
		return loadKitBundleDirectory(path)
	}
	if info.Size() > maxKitBundleArchiveSize {
		return nil, fmt.Errorf("bundle archive is %d bytes; compressed archive limit is %d", info.Size(), maxKitBundleArchiveSize)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open bundle zip %s: %w", path, err)
	}
	defer zr.Close()
	var totalBytes int64
	if len(zr.File) > maxKitBundleEntries {
		return nil, fmt.Errorf("bundle zip has %d entries; limit is %d", len(zr.File), maxKitBundleEntries)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("zip entry %q is not a regular file", f.Name)
		}
		name, nErr := safeBundleEntryName(f.Name)
		if nErr != nil {
			return nil, nErr
		}
		if _, duplicate := files[name]; duplicate {
			return nil, fmt.Errorf("zip contains duplicate bundle entry %q", name)
		}
		rc, oErr := f.Open()
		if oErr != nil {
			return nil, fmt.Errorf("open %s in zip: %w", f.Name, oErr)
		}
		if f.UncompressedSize64 > uint64(maxKitBundleFileBytes) {
			rc.Close()
			return nil, fmt.Errorf("bundle entry %q is %d bytes; per-file limit is %d", name, f.UncompressedSize64, maxKitBundleFileBytes)
		}
		data, rErr := readKitBundleEntry(rc, name, int64(f.UncompressedSize64), &totalBytes)
		closeErr := rc.Close()
		if rErr != nil {
			return nil, fmt.Errorf("read %s in zip: %w", f.Name, rErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close %s in zip: %w", f.Name, closeErr)
		}
		files[name] = data
	}
	return files, nil
}

// loadKitBundleDirectory walks in bounded batches rather than filepath.Walk/WalkDir: those helpers
// sort an entire directory by first reading all names, which lets one enormous directory allocate
// without giving the entry-count gate a chance to stop it. The explicit stack also avoids recursive
// call depth; ReadDir(64) bounds name allocation before the 512-entry ceiling is enforced.
func loadKitBundleDirectory(root string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	dirs := []string{root}
	entryCount := 0
	var totalBytes int64

	for len(dirs) > 0 {
		current := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		err := func() error {
			d, err := os.Open(current)
			if err != nil {
				return err
			}
			defer d.Close()

			for {
				entries, readErr := d.ReadDir(64)
				for _, entry := range entries {
					entryCount++
					if entryCount > maxKitBundleEntries {
						return fmt.Errorf("bundle has more than %d entries", maxKitBundleEntries)
					}
					p := filepath.Join(current, entry.Name())
					rel, err := filepath.Rel(root, p)
					if err != nil {
						return err
					}
					rel, err = safeBundleEntryName(filepath.ToSlash(rel))
					if err != nil {
						return err
					}
					fi, err := entry.Info()
					if err != nil {
						return err
					}
					if fi.IsDir() {
						dirs = append(dirs, p)
						continue
					}
					if !fi.Mode().IsRegular() {
						return fmt.Errorf("bundle entry %s is not a regular file", p)
					}
					f, err := os.Open(p)
					if err != nil {
						return err
					}
					data, readFileErr := readKitBundleEntry(f, rel, fi.Size(), &totalBytes)
					closeErr := f.Close()
					if readFileErr != nil {
						return readFileErr
					}
					if closeErr != nil {
						return closeErr
					}
					files[rel] = data
				}
				if readErr == io.EOF {
					return nil
				}
				if readErr != nil {
					return readErr
				}
			}
		}()
		if err != nil {
			return nil, fmt.Errorf("read bundle directory: %w", err)
		}
	}
	return files, nil
}

func readKitBundleEntry(r io.Reader, name string, declaredSize int64, totalBytes *int64) ([]byte, error) {
	if declaredSize < 0 || declaredSize > maxKitBundleFileBytes {
		return nil, fmt.Errorf("bundle entry %q is %d bytes; per-file limit is %d", name, declaredSize, maxKitBundleFileBytes)
	}
	if declaredSize > int64(maxKitBundleTotal)-*totalBytes {
		return nil, fmt.Errorf("bundle exceeds the %d-byte total decompressed limit at entry %q", maxKitBundleTotal, name)
	}
	remaining := int64(maxKitBundleTotal) - *totalBytes
	limit := int64(maxKitBundleFileBytes)
	if remaining < limit {
		limit = remaining
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > int64(maxKitBundleFileBytes) {
		return nil, fmt.Errorf("bundle entry %q exceeds the %d-byte per-file limit", name, maxKitBundleFileBytes)
	}
	if int64(len(data)) > remaining {
		return nil, fmt.Errorf("bundle exceeds the %d-byte total decompressed limit at entry %q", maxKitBundleTotal, name)
	}
	*totalBytes += int64(len(data))
	return data, nil
}

func safeBundleEntryName(name string) (string, error) {
	if strings.Contains(name, `\`) {
		return "", fmt.Errorf("unsafe bundle entry %q: backslash paths are not allowed", name)
	}
	clean := path.Clean(name)
	if name == "" || strings.HasPrefix(name, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != name {
		return "", fmt.Errorf("unsafe or non-canonical bundle entry %q", name)
	}
	return clean, nil
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
		newPEM, err = agent.ReadProtectedFile(*credPath)
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

// writeTokenFile atomically persists the per-node bearer token at mode 0600,
// creating the parent dir 0700. A private same-directory temp file prevents a
// replacement token from being exposed through an existing permissive target.
func writeTokenFile(path, token string) error {
	return agent.WritePrivateFileAtomic(path, []byte(token))
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
	stagingDir := fs.String("staging-dir", "", "secure parent for a fresh verified-bundle directory retained for inspection (default: a temporary directory removed after apply)")
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
	agent.PrintAppliedTo(os.Stderr, res)
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
	if err := agent.ReconcileSelfUpdateEarly(o.stateDir, BuildVersion, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "agent: early self-update reconciliation refused: %v\n", err)
		return 1
	}

	tokenBytes, err := agent.ReadPrivateFile(o.tokenPath)
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
	// membershipVerifiedFetch wraps verifiedFetch with the off-host keystone membership gate — the SAME
	// pre-swap verification the apply path runs (VerifyBundle then VerifyMembership). The deferred
	// self-update RETRY decides a binary swap from the fetched artifacts.json pin, so it MUST bind that
	// pin to the operator credential; verifying only the tier-1 bundle signature would let any party that
	// can serve a VerifyBundle-passing bundle swap the agent binary once a deferral is armed (keystone
	// bypass). Keystone OFF (empty operatorCred) → VerifyMembership is a no-op → identical to verifiedFetch.
	membershipVerifiedFetch := agent.WithMembershipGate(verifiedFetch, agent.MembershipConfig{
		NodeID:          o.nodeID,
		OperatorCredPEM: operatorCred,
		OperatorCredAlg: o.operatorCredAlg,
		OperatorRPID:    o.operatorRPID,
		OperatorOrigin:  o.operatorOrigin,
	}, o.stateDir)
	healthCheck := func() error {
		_, err := verifiedFetch()
		return err
	}
	if err := agent.WithStateLock(o.stateDir, func() error {
		agent.ReconcileSelfUpdatePromote(o.stateDir, BuildVersion, healthCheck, os.Stderr)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "agent: self-update reconciliation refused: %v\n", err)
		return 1
	}

	// cycle runs ONE poll->apply->report iteration from the given watermark via the testable
	// agent.RunControllerCycle (the deterministic unit the daemon loops over). It returns the generation
	// to resume from (the applied/fetched generation on success, the polled wake generation on a rekey
	// wake — so the stale pre-rekey bundle is never re-applied — or the unchanged watermark on a
	// timed-out long-poll) and whether a new generation was applied. On error it returns the unchanged
	// watermark (keep-last-good: the running overlay is untouched, so the caller never advances past a
	// failed apply). The resume cursor starts at --after (o.after); the loop advances it per cycle. (The
	// agent State keys anti-rollback on the manifest compiled_at string, not the controller's int64
	// generation, so the cursor is a flag here.)
	cycle := func(after int64) (resumeGen int64, applied bool, err error) {
		return agent.RunControllerCycle(client, agent.CycleConfig{
			NodeID:          o.nodeID,
			After:           after,
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

	// The daemon loop, the single-shot cycle, the post-apply heartbeat kick, the boot self-update
	// finalize, and the deferred-self-update retry live in agent.ControllerLoop (plan-7 decompose) so the
	// loop SEQUENCING is unit-tested. This wrapper only wires the seams from the flags/client/pinned key
	// built above; BuildVersion (the release-injected -ldflags var) rides into the Finalize /
	// RetryDeferred closures and the cycle's SelfUpdate, so the ldflags injection seam stays in cmd/agent.
	loop := &agent.ControllerLoop{
		Cycle: cycle,
		Finalize: func() {
			if err := agent.WithStateLock(o.stateDir, func() error {
				agent.FinalizeSelfUpdate(o.stateDir, BuildVersion, os.Stderr)
				return nil
			}); err != nil {
				fmt.Fprintf(os.Stderr, "agent: self-update finalize refused: %v\n", err)
			}
		},
		RetryDeferred: func() (bool, error) {
			var swapped bool
			err := agent.WithStateLock(o.stateDir, func() error {
				var retryErr error
				swapped, retryErr = agent.RetryDeferredSelfUpdate(selfUpdate, o.nodeID, o.stateDir, membershipVerifiedFetch, os.Stderr)
				return retryErr
			})
			return swapped, err
		},
		After:             o.after,
		ErrBackoff:        5 * time.Second,
		RetryInterval:     o.selfUpdateRetryInterval,
		Poster:            client,
		Telemetry:         agent.BuildTelemetry(o.stateDir),
		TelemetryInterval: o.telemetryInterval,
		NodeID:            o.nodeID,
		Stderr:            os.Stderr,
	}

	if !o.daemon {
		// Single-shot: one poll->apply->report cycle (deterministic — the unit the daemon loops over).
		return loop.RunOnce()
	}
	// Daemon: continuous, near-real-time updates. RunForever spawns the live health heartbeat and never
	// returns — a self-update swap is a syscall.Exec that destroys this process image, and any other exit
	// is a crash systemd Restart=always relaunches.
	loop.RunForever()
	return 0 // unreachable: RunForever never returns
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
	data, err := agent.ReadProtectedFile(path)
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
	data, err := agent.ReadProtectedFile(path)
	if err != nil {
		return nil, fmt.Errorf("read operator credential %s: %w", path, err)
	}
	return data, nil
}
