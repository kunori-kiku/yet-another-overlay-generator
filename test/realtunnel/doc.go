//go:build linux && integration

// Package realtunnel is the MANDATORY rc.1-gating real-tunnel integration tier (plan-18 / 3.6). It
// is TEST-ONLY and adds ZERO production code: it consumes the unmodified cmd/compiler /
// internal/artifacts output as the single oracle, brings the generated bundle up inside per-node
// systemd-nspawn containers (Option B — the UNMODIFIED install.sh under real systemd, the owner's
// choice 2026-06-19), and asserts the overlay actually works on a kernel: per-interface WireGuard
// handshake, babel-converged routes to every node's OverlayIP/32, end-to-end overlay ping, and the
// overlay-SNAT transit->overlay source rewrite.
//
// Every file carries `//go:build linux && integration` so it is invisible to the default build /
// `go test ./...` / vet / gofmt. Run it explicitly, as root, on Linux with the WireGuard module +
// systemd-nspawn + a base rootfs:
//
//	sudo go test -tags integration ./test/realtunnel/...
//
// The capability preflight (capabilities.go / TestMain) t.Skips cleanly when a prerequisite is
// absent, so a non-root or container-less checkout reports SKIP, never a false failure. See
// README.md for prerequisites, the base-rootfs recipe, the scenario flag, and the anti-drift
// (template-shape pin) contract.
package realtunnel
