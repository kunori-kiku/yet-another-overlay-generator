//go:build linux && integration

package realtunnel

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// nspawn.go — the systemd-nspawn container lifecycle (Option B: each node is a real booted-systemd
// container so the UNMODIFIED install.sh runs against real systemd). A container boots the shared
// base rootfs with --volatile=overlay (read-only base + per-container tmpfs upper — fast, isolated,
// no full rootfs copy) and --private-network (its own netns; the harness wires veths into it). All
// objects are named with objectPrefix so the orphan sweep can reclaim them.

// container is a booted node container.
type container struct {
	name string
	cmd  *exec.Cmd
	log  *syncBuffer
}

// syncBuffer is a goroutine-safe sink for the nspawn boot log. os/exec runs a copier goroutine that
// writes to cmd.Stdout/Stderr for the whole life of the container, while bootLog() may read the log
// on a failure path BEFORE the process exits — so Write and String must share one lock (a plain
// strings.Builder would be a data race the race detector flags).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// bootOpts configures a node container's networking + mounts.
type bootOpts struct {
	// bridge, when set, attaches the container's veth to this host bridge (--network-bridge, which
	// implies a private netns). When empty the container gets an isolated --private-network netns.
	bridge string
	// binds are "hostPath:containerPath" read-only bind mounts (the node's deployment bundle).
	binds []string
}

// bootContainer boots name from rootfs with the given network/bind options and blocks until systemd
// inside reports State=running, then registers teardown (terminate + wait) on t.Cleanup so it is
// reclaimed even on failure.
func bootContainer(t *testing.T, rootfs, name string, opts bootOpts) *container {
	t.Helper()
	c := &container{name: name, log: &syncBuffer{}}
	// --boot runs systemd as PID 1; --volatile=overlay isolates per-container writes over the shared
	// read-only base (fast — no full rootfs copy). Networking: a host bridge (the topology underlay)
	// or an isolated private netns. --machine lets machinectl drive it.
	args := []string{
		"--quiet",
		"--directory=" + rootfs,
		"--volatile=overlay",
		"--machine=" + name,
		"--boot",
	}
	if opts.bridge != "" {
		args = append(args, "--network-bridge="+opts.bridge)
	} else {
		args = append(args, "--private-network")
	}
	for _, b := range opts.binds {
		args = append(args, "--bind-ro="+b)
	}
	c.cmd = exec.Command("systemd-nspawn", args...)
	c.cmd.Stdout = c.log
	c.cmd.Stderr = c.log
	if err := c.cmd.Start(); err != nil {
		t.Fatalf("start nspawn %s: %v", name, err)
	}
	t.Cleanup(func() { c.terminate() })

	// Wait for the container's systemd to finish booting (machinectl State=running).
	waitFor(t, 45*time.Second, fmt.Sprintf("container %s to reach running", name), func() bool {
		out, _ := tryRun("machinectl", "show", name, "--property=State", "--value")
		return strings.TrimSpace(out) == "running"
	})
	return c
}

// exec runs argv inside the container as root and returns combined output; fails the test on a
// non-zero exit (dumping the container's boot log for diagnosis).
func (c *container) exec(t *testing.T, argv ...string) string {
	t.Helper()
	args := append([]string{"--machine=" + c.name, "--quiet", "--pipe", "--wait", "--collect"}, argv...)
	out, err := exec.Command("systemd-run", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("in-container command failed in %s: %s\n  err: %v\n  output:\n%s\n  boot log:\n%s",
			c.name, strings.Join(argv, " "), err, out, c.bootLog())
	}
	return string(out)
}

// tryExec runs argv inside the container best-effort, returning output + error (for polling / data
// plane probes where a non-zero exit is an expected, non-fatal "not converged yet").
func (c *container) tryExec(argv ...string) (string, error) {
	args := append([]string{"--machine=" + c.name, "--quiet", "--pipe", "--wait", "--collect"}, argv...)
	out, err := exec.Command("systemd-run", args...).CombinedOutput()
	return string(out), err
}

// bootLog returns the container's captured nspawn stdout/stderr (goroutine-safe via syncBuffer).
func (c *container) bootLog() string {
	return c.log.String()
}

// terminate powers the container off and waits for the nspawn process to exit (best-effort, runs on
// cleanup even after a failure).
func (c *container) terminate() {
	_, _ = tryRun("machinectl", "terminate", c.name)
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}

// waitFor polls cond every 500ms until it returns true or timeout elapses, failing the test with
// `what` on timeout. The bounded-poll replacement for any fixed sleep (DoD #2: no fixed sleep).
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}
