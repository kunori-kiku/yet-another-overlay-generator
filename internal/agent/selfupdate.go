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
	"context"
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
	"sync/atomic"
	"syscall"
	"time"
)

// maxSelfUpdateAttempts bounds how many boots may try to resolve a single PendingUpdate before
// the reconcile abandons it (rolls back to the prior binary). It is the crash-loop ceiling.
const maxSelfUpdateAttempts = 3

// maxSelfUpdateArtifactBytes caps the UNTRUSTED self-update download (the agent binary or a mimic .deb)
// as defense-in-depth against a malicious/buggy mirror streaming an unbounded body onto the node's disk.
// It is sized generously for a real Go agent binary + margin — far above the 32<<20 config-response cap
// (controller_client.go) since this is a BINARY, not small JSON — while still bounding exhaustion. The
// signed-pin verification AFTER the download is the actual integrity gate.
const maxSelfUpdateArtifactBytes = 256 << 20 // 256 MiB

// sameVersion reports whether two version strings denote the SAME release, using the SemVer comparator
// (version.Compare via compareVersions) rather than a raw string ==. BuildVersion is the released git
// tag (e.g. "v2.0.0-beta.2") while an operator's TargetAgentVersion may omit the leading "v" — an
// exact-equality reconcile check would then treat a successfully-swapped binary as "not applied" and
// WEDGE the update channel (the floor never advances, the in-flight guard blocks every future update).
// The pre-swap self-test already compares with compareVersions; reconcile/finalize/rollback must too.
func sameVersion(a, b string) bool { return compareVersions(a, b) == 0 }

// Download bounds. A binary self-update over a slow GitHub proxy is a large, slow-but-progressing
// transfer, so a single TOTAL deadline (the historic http.Client.Timeout) wrongly trips on the body
// read of a ~10–15 MB binary (the live "context deadline exceeded while reading body" failure). The
// download is bounded instead by THREE independent guards: a response-header timeout (a mirror that
// never answers fails fast), a STALL watchdog (no bytes for selfUpdateStallTimeout ⇒ abort — catches
// a hung mid-transfer), and a generous absolute ceiling (a final backstop).
// Download bounds are vars (not consts) so tests can shrink them, the same indirection pattern as
// execFn/osExecutable. Production values are unchanged.
var (
	selfUpdateHeaderTimeout = 45 * time.Second // time-to-first-byte (response headers)
	selfUpdateStallTimeout  = 90 * time.Second // max idle gap between body chunks
	selfUpdateAbsoluteCap   = 15 * time.Minute // hard backstop for the WHOLE download (all sources)
)

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
// It returns (swapped, err): swapped=true means the binary on disk WAS replaced (a re-exec failure
// after that point must NOT be recorded as a routine failure — the breadcrumb on disk has to
// survive for the next-boot reconcile, else the bad swap bricks). The custody check (SHA-256 vs the
// signed pin) gates everything that follows.
func performSelfUpdate(cfg *Config, cat *agentCatalog, running, ghProxy string, stderr io.Writer) (swapped bool, err error) {
	arch := runtime.GOARCH
	// D9: self-update is certified for amd64/arm64 only; fail closed on other arches (the
	// bootstrap may install 386/armv7 binaries, but self-update is not enabled for them yet).
	if arch != "amd64" && arch != "arm64" {
		return false, fmt.Errorf("self-update unsupported on arch %q", arch)
	}
	// In-flight guard: if a breadcrumb is already pending, a swap is in progress (e.g. a prior
	// re-exec failed and this daemon retried the cycle). Do NOT re-swap — that would overwrite the
	// .bak rollback target with the already-installed new binary AND reset Attempts, defeating the
	// crash-loop cap. Leave it for the next-boot reconcile to resolve.
	if st, _ := LoadState(cfg.StateDir); st != nil && st.PendingUpdate != nil {
		return false, fmt.Errorf("self-update to %s already in flight; awaiting restart to reconcile", st.PendingUpdate.To)
	}
	key := "linux-" + arch
	pin, ok := cat.Bins[key]
	if !ok || pin.Asset == "" || pin.SHA256 == "" {
		return false, fmt.Errorf("no signed self-update pin for %q", key)
	}

	self, err := osExecutable()
	if err != nil {
		return false, fmt.Errorf("locate self: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved // resolve symlinks so the rename targets the real binary
	}
	dir := filepath.Dir(self)
	// Download to a partial in the SAME directory as the target so the install rename is atomic.
	partial := filepath.Join(dir, ".yaog-agent."+cat.Version+".partial")
	defer os.Remove(partial) // removed on every error path; consumed (renamed away) on success

	// Source order: the operator-configured proxy FIRST (it exists for nodes that cannot reach GitHub
	// directly), then a DIRECT GitHub fetch as the fallback when the proxy is slow/down (the live
	// failure — a gh-proxy body-read timeout). The SHA-256-vs-signed-pin check below gates the swap
	// regardless of WHICH source served the bytes, so trying multiple sources is custody-safe: a
	// tampered mirror fails the pin and is refused. Keep the "download " error prefix so
	// classifySelfUpdateBlock still recognizes a download failure.
	direct := cat.ReleaseURL + "/" + pin.Asset
	urls := make([]string, 0, 2)
	if strings.TrimSpace(ghProxy) != "" {
		urls = append(urls, ghProxy+direct)
	}
	urls = append(urls, direct)
	// ONE absolute deadline across BOTH source attempts (not per-source): the whole download — proxy
	// fallback included — is bounded by selfUpdateAbsoluteCap, so a slow-but-progressing trickle on one
	// source then the other cannot block the (main-thread) caller for a multiple of the cap.
	dlCtx, dlCancel := context.WithTimeout(context.Background(), selfUpdateAbsoluteCap)
	defer dlCancel()
	var dlErr error
	for i, u := range urls {
		if dlErr = downloadTo(dlCtx, u, partial); dlErr == nil {
			if i > 0 {
				fmt.Fprintf(stderr, "agent: self-update downloaded via fallback source (proxy failed)\n")
			}
			break
		}
		fmt.Fprintf(stderr, "agent: self-update download from %s failed: %v\n", u, dlErr)
	}
	if dlErr != nil {
		return false, fmt.Errorf("download %s: %w", strings.Join(urls, ", "), dlErr)
	}

	// CUSTODY: verify the downloaded bytes against the SIGNED artifacts.json pin BEFORE the
	// binary is made executable or run. A mismatch (a tampered mirror) is refused, keep-last-good.
	gotSHA, err := fileSHA256(partial)
	if err != nil {
		return false, fmt.Errorf("hash downloaded binary: %w", err)
	}
	if !strings.EqualFold(gotSHA, pin.SHA256) {
		return false, fmt.Errorf("self-update hash mismatch for %s: got %s, want %s (refusing)", key, gotSHA, pin.SHA256)
	}
	if err := os.Chmod(partial, 0o755); err != nil {
		return false, fmt.Errorf("chmod new binary: %w", err)
	}
	// Self-test: the new binary must run and report the desired version, or we refuse to swap it
	// in (catches a corrupt-but-hash-matching or wrong-arch binary before it can brick).
	out, err := exec.Command(partial, "version").Output()
	if err != nil {
		return false, fmt.Errorf("self-test of new binary failed: %w", err)
	}
	// Compare with the SemVer comparator (not a raw byte compare): BuildVersion is the released
	// git tag (e.g. "v2.0.0-beta.2") while a hand-set TargetAgentVersion may omit the "v" — an
	// exact-equality check would silently refuse every such update.
	if got := strings.TrimSpace(string(out)); compareVersions(got, cat.Version) != 0 {
		return false, fmt.Errorf("self-test version %q != desired %q; refusing", got, cat.Version)
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
		return false, fmt.Errorf("persist self-update breadcrumb: %w", err)
	}

	// Install-then-flip: COPY the current binary to .bak (leaving the live binary in place), then
	// atomically rename the new binary over it. A same-directory rename atomically REPLACES the
	// target, so the binary is never absent at the systemd ExecStart path across ANY crash point.
	// The .bak copy is the rollback artifact.
	bak := self + ".bak"
	if err := copyFile(self, bak); err != nil {
		return false, fmt.Errorf("back up current binary: %w", err)
	}
	if err := renameOrCopy(partial, self); err != nil {
		_ = os.Remove(bak)
		return false, fmt.Errorf("install new binary: %w", err)
	}

	// From HERE the binary on disk IS the new one (swapped=true): a re-exec failure below must NOT
	// be recorded as a routine failure, or the on-disk breadcrumb would be erased and the swapped
	// (possibly bad) binary would boot with nothing for the reconcile to roll back — a brick.
	fmt.Fprintf(stderr, "agent: self-updated %s -> %s; re-exec\n", running, cat.Version)
	return true, execFn(self, os.Args, os.Environ())
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
	if err := SaveState(stateDir, st); err != nil {
		// If the attempt bump can't be persisted (e.g. the state dir went read-only), the crash-loop
		// ceiling can't advance across boots — surface it loudly rather than swallowing the error, so an
		// unbounded restart loop on an unwritable node is diagnosable instead of silent.
		fmt.Fprintf(stderr, "agent: WARNING: could not persist self-update attempt %d/%d (%v); the crash-loop brick-bound will not advance while the state dir is unwritable\n", pu.Attempts, maxSelfUpdateAttempts, err)
	}
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
	if !sameVersion(buildVersion, pu.To) {
		// Swap/exec never took effect (or we are mid-rollback): Phase A bounds it via Attempts.
		fmt.Fprintf(stderr, "agent: pending self-update to %s not applied (running %s, attempt %d/%d)\n",
			pu.To, buildVersion, pu.Attempts, maxSelfUpdateAttempts)
		return
	}
	if pu.Confirmed {
		// Health-confirmed on a PRIOR boot but rebooted before finalizing. Do NOT roll back here:
		// a benign host reboot during the short probation window would otherwise FALSELY abandon a
		// perfectly healthy binary and silently drop the node from the rollout. Instead RESUME
		// probation — the daemon retries finalize on its next cycle. A genuinely-crashing binary
		// never finalizes and is bounded by Phase A's Attempts cap (which rolls back + abandons).
		fmt.Fprintf(stderr, "agent: self-update to %s resuming probation (attempt %d/%d)\n", pu.To, pu.Attempts, maxSelfUpdateAttempts)
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
	if !pu.Confirmed || !sameVersion(buildVersion, pu.To) {
		return
	}
	self, _ := osExecutable()
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	st.PendingUpdate = nil
	st.AgentVersionFloor = pu.To
	st.AbandonedAgentVersion = "" // a successful promote supersedes any prior abandonment
	st.AbandonedReason = ""       // cleared with the version it described
	// A successful self-update means the node is no longer blocked: clear any leftover Blocked latch
	// from the earlier deferred/failed attempts so the selfupdate condition stops reporting "Blocked"
	// once the node IS on the target. Otherwise it would persist until the next generation apply
	// (recordSuccess) or a reachable idle retry — the beta.16 "sticky Blocked" smoke finding.
	st.SelfUpdateBlocked = ""
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
	if sameVersion(buildVersion, pu.To) {
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
	st.AbandonedReason = curateAbandonReason(reason) // durable, curated (no raw stderr) — feeds the Abandoned condition
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

// downloadTo fetches url into path (truncating) under the caller's `parent` context. It is bounded by
// a response-header timeout + a STALL watchdog + the parent's absolute ceiling rather than a single
// total deadline — so a large binary on a slow link (which kept tripping the old 2-minute total) is
// tolerated as long as bytes keep flowing, while a hung mirror still fails fast. The parent carries
// the WHOLE-download absolute cap (shared across source-fallback attempts); this call derives a child
// for its own stall cancellation, so a stall on one source does not poison the next. The bytes are
// UNTRUSTED until the caller verifies them against the signed pin.
func downloadTo(parent context.Context, url, path string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// No total Client.Timeout: the body is bounded by the stall watchdog + the parent's absolute cap,
	// so a slow-but-progressing transfer is never killed mid-body. ResponseHeaderTimeout bounds the
	// time-to-first-byte. Proxy:FromEnvironment preserves the default transport's proxy posture.
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: selfUpdateHeaderTimeout,
	}}
	resp, err := client.Do(req)
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
	sr := newStallReader(resp.Body, selfUpdateStallTimeout, cancel)
	// Cap the UNTRUSTED body (defense-in-depth vs a mirror streaming an unbounded download → disk
	// exhaustion; the signed-pin verify AFTER this is the integrity gate). The +1 lets an over-cap body
	// be DISTINGUISHED as a hard error rather than silently truncated into a pin mismatch.
	n, copyErr := io.Copy(f, io.LimitReader(sr, maxSelfUpdateArtifactBytes+1))
	sr.stop()
	closeErr := f.Close()
	if copyErr != nil {
		// A stall surfaces as a copy error (the watchdog cancelled the context). Distinguish it from a
		// generic transport error / the parent absolute-cap deadline so the log is actionable. Only
		// consult stalled() here — checking it on a CLEAN copy would risk a benign false positive if the
		// watchdog fired in the tiny window between the final Read and stop().
		if sr.stalled() {
			return fmt.Errorf("download stalled: no data for %s", selfUpdateStallTimeout)
		}
		return copyErr
	}
	if n > maxSelfUpdateArtifactBytes {
		return fmt.Errorf("download exceeds the %d-byte cap; refusing the untrusted oversized body", maxSelfUpdateArtifactBytes)
	}
	return closeErr
}

// stallReader wraps a download body with an IDLE (stall) watchdog: if no bytes are read for `timeout`,
// it fires `cancel` (cancelling the request context, which aborts the in-flight transfer). It imposes
// NO total-time cap — a slow-but-progressing transfer keeps resetting the timer and is tolerated.
// stalled() reports whether the watchdog fired, so the caller can surface a clear stall error instead
// of the opaque context-cancelled error. AfterFunc timers carry no channel, so Reset/Stop here are
// race-free with the firing callback (the t.C-drain caveat does not apply).
type stallReader struct {
	r       io.Reader
	timeout time.Duration
	cancel  context.CancelFunc
	timer   *time.Timer
	fired   atomic.Bool
}

func newStallReader(r io.Reader, timeout time.Duration, cancel context.CancelFunc) *stallReader {
	s := &stallReader{r: r, timeout: timeout, cancel: cancel}
	s.timer = time.AfterFunc(timeout, func() {
		s.fired.Store(true)
		s.cancel()
	})
	return s
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(s.timeout)
	}
	return n, err
}

func (s *stallReader) stop()         { s.timer.Stop() }
func (s *stallReader) stalled() bool { return s.fired.Load() }

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
