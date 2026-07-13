// Package wiredrift hosts the wire-DTO / omitempty drift gate (framework-refactor plan-10).
//
// It is a test-only package: the sole production file is this doc (so `go build ./...` sees a
// non-test Go file and treats the directory as a normal package); the gate itself lives in
// drift_test.go. The package imports nothing from the tree it guards — it reads the Go model, the
// two controller wire-DTO definitions, and the frontend field lists as SOURCE via go/ast + regexp,
// exactly as the retired internal/conformance drift test did, so it introduces no import edge and is
// immune to the very drift it polices.
//
// See drift_test.go for what it pins and why the guarantee is fail-closed (invariant [5]).
package wiredrift
