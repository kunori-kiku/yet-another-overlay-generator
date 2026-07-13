// Package arch holds architecture-enforcement tests — a dependency ratchet. It has no
// runtime code; layers_test.go asserts the pure compile core imports nothing stateful,
// converting the pure/stateful quarantine (PRINCIPLES.md, docs/spec/controller/persistence.md
// §The quarantine boundary) from reviewer convention into a red build. The allow-list of
// current violations may only SHRINK — a CI check fails if it grows.
package arch
