package agent

// selfupdate.go is the signed agent self-update (plan-9, the RISKY CORE). An agent may replace
// its OWN binary with the version pinned in the bundle's controller-signed, keystone-bound
// artifacts.json — but ONLY after verifying the downloaded bytes against that signed pin, never
// the upstream .sha256 sidecar (same untrusted transport as the binary). It never downgrades
// below a health-confirmed floor, and a crashing new binary cannot infinitely loop a node: a
// crash-durable PendingUpdate breadcrumb is reconciled on every boot (promote on health, roll
// back on failure, abandon at the attempt cap) BEFORE the daemon loop — bounding the systemd
// Restart=always loop without a unit-file change.
//
// HIGH custody invariant (PRINCIPLES.md / outline): the download is verified against the pin in
// the signed artifacts.json (which VerifyBundle has already confirmed is covered by checksums),
// the binary is self-tested (`<bin> version` must equal the desired version) before exec, and the
// floor advances ONLY after a swapped binary survives one clean cycle.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// maxSelfUpdateAttempts bounds how many boots may try to resolve a single PendingUpdate before
// the reconcile abandons it (rolls back to the prior binary). It is the crash-loop ceiling.
const maxSelfUpdateAttempts = 3

// selfUpdateDownloadTimeout bounds the binary download so a hung mirror cannot wedge a cycle.
const selfUpdateDownloadTimeout = 2 * time.Minute

// execFn is syscall.Exec, indirected so tests can observe the re-exec without replacing the test
// process. The real syscall.Exec NEVER returns on success.
var execFn = syscall.Exec

// osExecutable locates the running binary (the swap target), indirected so tests can point it at a
// temp file instead of the go-test binary. Production uses os.Executable.
var osExecutable = os.Executable

// SelfUpdateParams enables signed self-update for a Run (controller mode). Nil ⇒ no self-update
// (air-gap / DirSource): Run behaves exactly as before.
type SelfUpdateParams struct {
	// RunningVersion is this binary's BuildVersion (the comparison baseline + rollback target).
	RunningVersion string
	// GithubProxy is the optional download prefix (e.g. "https://gh-proxy.com/") prepended to the
	// signed agent.release_url, exactly as the mimic install.sh prepends it. Empty = direct.
	GithubProxy string
}

// agentCatalog is the agent self-update block parsed from a VERIFIED bundle's artifacts.json.
type agentCatalog struct {
	Version    string
	MinVersion string
	ReleaseURL string
	Bins       map[string]binPin
}

// binPin is one per-arch agent binary pin: the release asset name and the SHA-256 the download
// MUST match before the binary is made executable or exec'd.
type binPin struct {
	Asset  string `json:"asset"`
	SHA256 string `json:"sha256"`
}

// artifactsAgentEnvelope parses just the .agent block of artifacts.json (the mimic block and the
// rest are irrelevant to self-update). The JSON keys mirror render's artifactsAgent.
type artifactsAgentEnvelope struct {
	Agent struct {
		Version    string            `json:"version"`
		MinVersion string            `json:"min_version"`
		ReleaseURL string            `json:"release_url"`
		Bins       map[string]binPin `json:"bins"`
	} `json:"agent"`
}

// parseAgentCatalog extracts the agent self-update block from a bundle's artifacts.json. It is
// called ONLY on the VERIFIED file map (VerifyBundle's coverage guard has confirmed artifacts.json
// is part of the signed set), so the pins it returns are trusted. Returns nil when artifacts.json
// is absent, malformed, or carries no agent version (⇒ no self-update — fail-safe).
func parseAgentCatalog(files map[string][]byte) *agentCatalog {
	raw, ok := files["artifacts.json"]
	if !ok {
		return nil
	}
	var env artifactsAgentEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	if strings.TrimSpace(env.Agent.Version) == "" {
		return nil
	}
	return &agentCatalog{
		Version:    env.Agent.Version,
		MinVersion: env.Agent.MinVersion,
		ReleaseURL: env.Agent.ReleaseURL,
		Bins:       env.Agent.Bins,
	}
}

// updateDecision is the outcome of the PURE self-update decision.
type updateDecision int

const (
	updateSkip       updateDecision = iota // nothing to do (no catalog, or already at desired)
	updateRefuse                           // a downgrade / missing-pin / misconfig — do NOT swap
	updateForced                           // running < min_version: MUST update before applying
	updateAfterApply                       // desired > running, ≥ floor: update AFTER a good apply
)

// isForced reports whether the bundle requires a newer agent than is running (running <
// agent.min_version) — the agent must self-update BEFORE applying such a bundle.
func isForced(cat *agentCatalog, running string) bool {
	return cat != nil && strings.TrimSpace(cat.MinVersion) != "" &&
		compareVersions(running, cat.MinVersion) < 0
}

// decideSelfUpdate is the PURE decision (no network, no disk): given the verified catalog, the
// running version, the persisted floor, and the last abandoned target, what should the agent do?
// Custody is enforced here — a downgrade below the running version OR below the health-confirmed
// floor is REFUSED, and a target previously abandoned (rolled back at the attempt cap) is NOT
// re-armed until the operator moves to a different version (avoids a doomed target re-flapping).
func decideSelfUpdate(cat *agentCatalog, running, floor, abandoned string) (updateDecision, string) {
	if cat == nil || strings.TrimSpace(cat.Version) == "" {
		return updateSkip, "no agent catalog"
	}
	desired := cat.Version
	forced := isForced(cat, running)

	if compareVersions(desired, running) == 0 && !forced {
		return updateSkip, "already at desired version " + desired
	}
	if abandoned != "" && compareVersions(desired, abandoned) == 0 {
		return updateRefuse, fmt.Sprintf("target %s was previously abandoned (rolled back at the attempt cap); change the target to retry", desired)
	}
	// Anti-downgrade (custody): never below the running version or the health-confirmed floor.
	if compareVersions(desired, running) < 0 {
		return updateRefuse, fmt.Sprintf("desired %s is older than running %s (downgrade refused)", desired, running)
	}
	if floor != "" && compareVersions(desired, floor) < 0 {
		return updateRefuse, fmt.Sprintf("desired %s is below the health-confirmed floor %s (downgrade refused)", desired, floor)
	}
	if forced {
		// A forced update whose target does not even reach the required min is a misconfigured
		// rollout (target < min_version): refuse rather than swap to a still-incompatible binary.
		if compareVersions(desired, cat.MinVersion) < 0 {
			return updateRefuse, fmt.Sprintf("target %s is below required min_version %s (misconfigured rollout)", desired, cat.MinVersion)
		}
		return updateForced, fmt.Sprintf("running %s is below required min_version %s; updating to %s", running, cat.MinVersion, desired)
	}
	return updateAfterApply, fmt.Sprintf("update %s -> %s", running, desired)
}

// performSelfUpdate downloads, verifies-against-the-signed-pin, self-tests, breadcrumbs, swaps,
// and re-execs the desired agent binary. On success it does NOT return (the process is replaced).
// It returns an error if any step fails BEFORE the swap (the caller keeps the running binary).
// The custody check (SHA-256 vs the signed pin) gates everything that follows.
func performSelfUpdate(cfg *Config, cat *agentCatalog, running, ghProxy string, stderr io.Writer) error {
	arch := runtime.GOARCH
	// D9: self-update is certified for amd64/arm64 only; fail closed on other arches (the
	// bootstrap may install 386/armv7 binaries, but self-update is not enabled for them yet).
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("self-update unsupported on arch %q", arch)
	}
	key := "linux-" + arch
	pin, ok := cat.Bins[key]
	if !ok || pin.Asset == "" || pin.SHA256 == "" {
		return fmt.Errorf("no signed self-update pin for %q", key)
	}

	self, err := osExecutable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved // resolve symlinks so the rename targets the real binary
	}
	dir := filepath.Dir(self)
	// Download to a partial in the SAME directory as the target so the install rename is atomic.
	partial := filepath.Join(dir, ".yaog-agent."+cat.Version+".partial")
	defer os.Remove(partial) // removed on every error path; consumed (renamed away) on success

	url := ghProxy + cat.ReleaseURL + "/" + pin.Asset
	if err := downloadTo(url, partial); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	// CUSTODY: verify the downloaded bytes against the SIGNED artifacts.json pin BEFORE the
	// binary is made executable or run. A mismatch (a tampered mirror) is refused, keep-last-good.
	gotSHA, err := fileSHA256(partial)
	if err != nil {
		return fmt.Errorf("hash downloaded binary: %w", err)
	}
	if !strings.EqualFold(gotSHA, pin.SHA256) {
		return fmt.Errorf("self-update hash mismatch for %s: got %s, want %s (refusing)", key, gotSHA, pin.SHA256)
	}
	if err := os.Chmod(partial, 0o755); err != nil {
		return fmt.Errorf("chmod new binary: %w", err)
	}
	// Self-test: the new binary must run and report EXACTLY the desired version, or we refuse to
	// swap it in (catches a corrupt-but-hash-matching or wrong-arch binary before it can brick).
	out, err := exec.Command(partial, "version").Output()
	if err != nil {
		return fmt.Errorf("self-test of new binary failed: %w", err)
	}
	// Compare with the SemVer comparator (not a raw byte compare): BuildVersion is the released
	// git tag (e.g. "v2.0.0-beta.2") while a hand-set TargetAgentVersion may omit the "v" — an
	// exact-equality check would silently refuse every such update.
	if got := strings.TrimSpace(string(out)); compareVersions(got, cat.Version) != 0 {
		return fmt.Errorf("self-test version %q != desired %q; refusing", got, cat.Version)
	}

	// Crash-durable breadcrumb BEFORE the swap: the next boot reconciles it (promote/rollback/
	// abandon), which is what bounds the Restart=always loop. Confirmed=false — the swap is not
	// trusted until it passes the startup health gate AND survives a full daemon cycle.
	st, _ := LoadState(cfg.StateDir)
	if st == nil {
		st = &State{}
	}
	st.NodeID = cfg.NodeID
	st.PendingUpdate = &PendingUpdate{From: running, To: cat.Version, Attempts: 0}
	if err := SaveState(cfg.StateDir, st); err != nil {
		return fmt.Errorf("persist self-update breadcrumb: %w", err)
	}

	// Install-then-flip: COPY the current binary to .bak (leaving the live binary in place), then
	// atomically rename the new binary over it. A same-directory rename atomically REPLACES the
	// target, so `self` always names a valid executable across ANY crash point — there is never a
	// window with no binary at the systemd ExecStart path. The .bak copy is the rollback artifact.
	bak := self + ".bak"
	if err := copyFile(self, bak); err != nil {
		return fmt.Errorf("back up current binary: %w", err)
	}
	if err := renameOrCopy(partial, self); err != nil {
		_ = os.Remove(bak)
		return fmt.Errorf("install new binary: %w", err)
	}

	fmt.Fprintf(stderr, "agent: self-updated %s -> %s; re-exec\n", running, cat.Version)
	// Replace the process with the new binary, same argv/env, so it resumes as the daemon and the
	// startup reconcile resolves the breadcrumb. execFn returns ONLY on failure.
	return execFn(self, os.Args, os.Environ())
}

// ReconcileSelfUpdateEarly is PHASE A of the startup reconcile (plan-9): it MUST run as the very
// first thing in controller mode — BEFORE any crash-prone setup (token/client/pubkey reads) — so a
// freshly swapped binary that crashes during early init is still bounded. With no breadcrumb it is
// a no-op. Otherwise it bumps Attempts crash-durably FIRST (every boot counts toward the cap, even
// an early-init panic), then ABANDONS at the cap (roll back to .bak, remember the target). It needs
// only stateDir + buildVersion — no controller client.
func ReconcileSelfUpdateEarly(stateDir, buildVersion string, stderr io.Writer) {
	st, err := LoadState(stateDir)
	if err != nil || st == nil || st.PendingUpdate == nil {
		return
	}
	pu := st.PendingUpdate
	pu.Attempts++
	_ = SaveState(stateDir, st)
	if pu.Attempts > maxSelfUpdateAttempts {
		rollbackAndAbandon(stateDir, buildVersion, pu, fmt.Sprintf("attempt cap %d exceeded", maxSelfUpdateAttempts), stderr)
	}
}

// ReconcileSelfUpdatePromote is PHASE B: once the controller client + pinned key exist, it
// health-gates a swapped binary. It runs AFTER ReconcileSelfUpdateEarly (Attempts already bumped).
// No breadcrumb ⇒ no-op. When the running build IS the target:
//   - not yet Confirmed: run healthCheck; a pass marks the update PROBATIONARY (Confirmed, .bak
//     kept, floor NOT yet advanced) so the daemon proceeds; a failure rolls back.
//   - already Confirmed: it passed the gate on a PRIOR boot but rebooted before finalizing — it
//     crashed during probation — so roll back.
//
// The promote is FINALIZED (floor advanced, .bak dropped, breadcrumb cleared) by FinalizeSelfUpdate
// once the new binary completes a full daemon cycle — proving it can actually run, not merely pass
// `version` + a fetch/verify. healthCheck is injected (a Fetch + VerifyBundle in production) so
// this is unit-testable.
func ReconcileSelfUpdatePromote(stateDir, buildVersion string, healthCheck func() error, stderr io.Writer) {
	st, err := LoadState(stateDir)
	if err != nil || st == nil || st.PendingUpdate == nil {
		return
	}
	pu := st.PendingUpdate
	if buildVersion != pu.To {
		// Swap/exec never took effect (or we are mid-rollback): Phase A bounds it via Attempts.
		fmt.Fprintf(stderr, "agent: pending self-update to %s not applied (running %s, attempt %d/%d)\n",
			pu.To, buildVersion, pu.Attempts, maxSelfUpdateAttempts)
		return
	}
	if pu.Confirmed {
		rollbackAndAbandon(stateDir, buildVersion, pu, "did not survive probation (rebooted before finalizing)", stderr)
		return
	}
	if err := healthCheck(); err != nil {
		rollbackAndAbandon(stateDir, buildVersion, pu, "health gate failed: "+err.Error(), stderr)
		return
	}
	// Health-confirmed: begin PROBATION. Keep .bak + breadcrumb and do NOT advance the floor yet;
	// FinalizeSelfUpdate promotes once a full daemon cycle proves the binary runs.
	pu.Confirmed = true
	st.Health = "self-update to " + pu.To + " health-confirmed (probationary)"
	_ = SaveState(stateDir, st)
	fmt.Fprintf(stderr, "agent: self-update to %s health-confirmed; probationary until one clean cycle\n", pu.To)
}

// FinalizeSelfUpdate promotes a Confirmed (probationary) self-update once the new binary has
// completed a full daemon cycle — proving it actually RUNS, not just that `version` + a fetch/verify
// succeed (the gap that would otherwise leave a daemon-only crash after the health gate unrecoverable).
// It advances AgentVersionFloor (the ONLY place it advances), clears the breadcrumb + the
// abandoned-target memory, and drops .bak. No-op unless a Confirmed breadcrumb for the running
// version exists. Called from the daemon/single-shot loop after the first cycle returns.
func FinalizeSelfUpdate(stateDir, buildVersion string, stderr io.Writer) {
	st, err := LoadState(stateDir)
	if err != nil || st == nil || st.PendingUpdate == nil {
		return
	}
	pu := st.PendingUpdate
	if !pu.Confirmed || buildVersion != pu.To {
		return
	}
	self, _ := osExecutable()
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	st.PendingUpdate = nil
	st.AgentVersionFloor = pu.To
	st.AbandonedAgentVersion = "" // a successful promote supersedes any prior abandonment
	st.Health = "self-updated to " + pu.To
	_ = SaveState(stateDir, st)
	_ = os.Remove(self + ".bak")
	fmt.Fprintf(stderr, "agent: self-update to %s finalized after a clean cycle (floor=%s)\n", pu.To, pu.To)
}

// rollbackAndAbandon is the shared failure path for every doomed self-update (cap exceeded, health
// gate failed, crashed during probation): when running the failed target with a .bak available it
// restores the prior binary and re-execs it; otherwise it stays on the current (from) binary and
// reports unhealthy. It records AbandonedAgentVersion so decideSelfUpdate will not re-arm the SAME
// target (no perpetual flap) until the operator moves to a different version. Crash-safe ordering:
// the .bak→binary rename happens BEFORE the breadcrumb is cleared, so a crash mid-rollback re-tries
// on the next boot rather than stranding a broken binary unbreadcrumbed. A failed update NEVER
// advances the floor (the load below leaves it untouched).
func rollbackAndAbandon(stateDir, buildVersion string, pu *PendingUpdate, reason string, stderr io.Writer) {
	self, _ := osExecutable()
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	bak := self + ".bak"

	rolledBack := false
	if buildVersion == pu.To {
		if _, e := os.Stat(bak); e == nil {
			if e := os.Rename(bak, self); e == nil { // restore the good binary FIRST
				rolledBack = true
			}
		}
	}

	st, _ := LoadState(stateDir)
	if st == nil {
		st = &State{}
	}
	st.PendingUpdate = nil // cleared AFTER the rename above
	st.AbandonedAgentVersion = pu.To
	st.Health = "self-update to " + pu.To + " abandoned: " + reason
	_ = SaveState(stateDir, st)

	if rolledBack {
		fmt.Fprintf(stderr, "agent: self-update to %s failed (%s); rolled back to %s; re-exec\n", pu.To, reason, pu.From)
		_ = execFn(self, os.Args, os.Environ())
		return // execFn returns only on error — fall through to staying on the restored binary
	}
	_ = os.Remove(bak)
	fmt.Fprintf(stderr, "agent: self-update to %s abandoned (%s); staying on %s\n", pu.To, reason, buildVersion)
}

// copyFile copies src to dst (0755), used to snapshot the current binary to .bak WITHOUT removing
// it, so the live binary is never absent during an install-then-flip swap.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// downloadTo fetches url into path (truncating), with a bounded timeout and the same
// http(s)-only posture as the rest of the agent's transport. The bytes are UNTRUSTED until the
// caller verifies them against the signed pin.
func downloadTo(url, path string) error {
	client := &http.Client{Timeout: selfUpdateDownloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// fileSHA256 returns the lowercase hex SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// renameOrCopy renames src→dst, falling back to a copy+fsync+rename when the rename crosses a
// filesystem boundary (EXDEV). The partial lives beside the target, so the rename path is the
// norm; the fallback is robustness for an unusual layout (e.g. the binary on a read-only-ish or
// bind-mounted FS). The destination is left executable (0755).
func renameOrCopy(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".swap"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(src)
	return nil
}
