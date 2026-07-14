// Package wiredrift hosts the wire-DTO / omitempty drift gate (framework-refactor plan-10).
//
// It is a test-only package: the sole production file is this doc (so `go build ./...` sees a
// non-test Go file and treats the directory as a normal package); the gate itself lives in
// drift_test.go. The package imports nothing from the tree it guards — it reads the Go model, the
// agent/server controller wire-DTO definitions, the OPERATOR-panel controller wire DTOs (the whole
// internal/api package, walked by symbol), and the frontend field lists / snake_case wire interfaces
// as SOURCE via go/ast + regexp, exactly as the retired internal/conformance drift test did, so it
// introduces no import edge and is immune to the very drift it polices.
//
// post-refactor-debt-paydown plan-10 extended it to the operator-panel controller wire DTOs
// (settingsJSON / nodeJSON / the stage / deploy-preview / audit / fleet / keystone DTOs ↔ the
// snake_case *JSON / *Wire interfaces in frontend/src/api/controller/*.ts), closing the hand-mirror
// gap the assessment flagged. See TestControllerWireDTOsMirrorFE + controllerWireCases for the
// covered set and the documented follow-ups.
//
// See drift_test.go for what it pins and why the guarantee is fail-closed (invariant [5]).
package wiredrift
