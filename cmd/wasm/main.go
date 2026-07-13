//go:build js && wasm

// Command wasm is the browser/Node WebAssembly shim over the pure Go compile pipeline
// (framework-refactor plan-3, "WASM add-alongside"). Built with GOOS=js GOARCH=wasm into
// web/yaog.wasm, it registers a small JSON-string API on the JS global `yaog` and then
// blocks forever, so the SAME pure pipeline the controller runs in Go executes IN the
// browser (the opt-in local design engine, VITE_YAOG_LOCAL_ENGINE='wasm') and IN the
// permanent headless three-way conformance gate.
//
// ADDITIVE phase: the TypeScript compiler stays the DEFAULT local engine; this wasm engine
// is opt-in behind a flag. Nothing is deleted. The `buildManifest` entry is the
// load-bearing one — invariant [1] "parity by execution + a permanent gate": the
// WASM-vs-golden gate (scripts/wasm-conformance-gate.mjs) executes it over the success
// corpus and asserts byte-equality against the frozen Go golden, proving WASM == Go. The
// existing vitest conformance proves TS == Go, so the two together prove WASM == Go == TS.
//
// Every registered function takes and returns JSON STRINGS: JS passes a string argument,
// the shim json.Unmarshals it, runs the pure pipeline, and json.Marshals the result back to
// a string. No Go struct ever crosses the syscall/js boundary, so the JS<->Go contract is
// trivial and identical in the browser and in Node. Each function returns a single JSON
// object `{"error":"<code-or-message>"}` on failure — a shape wasmEngine.ts detects.
//
// The shim adds ZERO Go dependencies (invariant [4]/[6]): it imports only the pure-core
// packages already in the module (localcompile/compiler/render/validator/model/bundlesig)
// plus the test-support conformance oracle, which BuildManifest/Marshal live in as ordinary
// (non-test-tagged) code. It performs NO file I/O — the gate feeds the fixture JSON and the
// signing PEM as string arguments — so the js/wasm os shim is never exercised.
package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"syscall/js"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/conformance"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

func main() {
	// Register the API object on the JS global. syscall/js callbacks run synchronously on
	// the JS->Go call, so each function computes and returns a string in one turn.
	yaog := js.Global().Get("Object").New()
	yaog.Set("compile", js.FuncOf(wasmCompile))
	yaog.Set("validate", js.FuncOf(wasmValidate))
	yaog.Set("deployScript", js.FuncOf(wasmDeployScript))
	yaog.Set("exportFiles", js.FuncOf(wasmExportFiles))
	yaog.Set("buildManifest", js.FuncOf(wasmBuildManifest))
	js.Global().Set("yaog", yaog)

	// Block forever so the registered js.Func callbacks stay alive for the page/process
	// lifetime. The pending JS callbacks keep the Go scheduler from reporting a deadlock;
	// this is the canonical Go-wasm "keep the exports callable" idiom.
	select {}
}

// compileResponse mirrors internal/api.CompileResponse's wire shape (the direct field-map off
// *compiler.CompileResult that handler_airgap.go:121 emits) so wasmEngine.ts and the FE
// CompileResponse type consume the wasm output with no translation. It is redeclared here
// (not imported) because api.CompileResponse's sibling ValidateResponse lives behind
// //go:build airgap and the shim links the DEFAULT profile; keeping both response shapes
// local also keeps the wasm binary lean.
type compileResponse struct {
	Topology         *model.Topology             `json:"topology"`
	WireGuardConfigs map[string]string           `json:"wireguard_configs"`
	BabelConfigs     map[string]string           `json:"babel_configs"`
	SysctlConfigs    map[string]string           `json:"sysctl_configs"`
	InstallScripts   map[string]string           `json:"install_scripts"`
	DeployScripts    map[string]string           `json:"deploy_scripts"`
	Warnings         []validator.ValidationError `json:"warnings,omitempty"`
	Manifest         compiler.CompileManifest    `json:"manifest"`
}

// validateResponse mirrors the air-gap ValidateResponse ({valid, errors, warnings}) so the
// wasm validate() matches the FE ValidateResponse type; wasmEngine.ts normalizes the omitted
// (empty) error/warning slices back to arrays.
type validateResponse struct {
	Valid    bool                        `json:"valid"`
	Errors   []validator.ValidationError `json:"errors,omitempty"`
	Warnings []validator.ValidationError `json:"warnings,omitempty"`
}

// onDiskFixture is the JSON shape of a plan-3 contract fixture — kept byte-identical to the
// loader in internal/conformance/golden_test.go (onDiskFixture) so the SAME corpus file
// resolves the SAME Fixture on the wasm side. buildManifest consumes it.
type onDiskFixture struct {
	Name     string          `json:"name"`
	Doc      string          `json:"doc"`
	Custody  string          `json:"custody"`
	Signing  bool            `json:"signing"`
	Topology json.RawMessage `json:"topology"`
}

// fixedPreviewClock is the compile clock the browser-preview compile/deploy/export paths
// inject so the display-only manifest.compiled_at is deterministic. It reuses the oracle's
// pinned instant (compiled_at is OUT of the conformance byte set, so this coupling changes
// no gated bytes) — invariant [2].
var fixedPreviewClock = conformance.FixedCompiledAt

// previewRequest builds the CompileRequest the browser-preview paths (compile / deployScript /
// exportFiles) share: AirGap custody (local mode reconstructs private keys into the result
// topology, as the air-gap server does), no fetch catalog (the zero FetchSettings keeps the
// bundle byte-identical), the default keygen (nil), and the fixed preview clock.
func previewRequest(topo model.Topology) localcompile.CompileRequest {
	return localcompile.CompileRequest{
		Topology:   topo,
		Custody:    render.AirGap,
		Fetch:      render.FetchSettings{},
		CompiledAt: fixedPreviewClock,
	}
}

// wasmCompile mirrors POST /api/compile (the air-gap shape): unmarshal the topology, run the
// pure pipeline under AirGap custody + the fixed clock, and marshal the direct field-map
// CompileResponse. On error returns {"error":"<code-or-message>"}.
func wasmCompile(_ js.Value, args []js.Value) any {
	var topo model.Topology
	if err := json.Unmarshal([]byte(args[0].String()), &topo); err != nil {
		return errEnvelope(err)
	}
	result, err := localcompile.CompileResult(previewRequest(topo))
	if err != nil {
		return errEnvelope(err)
	}
	return mustJSON(compileResponse{
		Topology:         result.Topology,
		WireGuardConfigs: result.WireGuardConfigs,
		BabelConfigs:     result.BabelConfigs,
		SysctlConfigs:    result.SysctlConfigs,
		InstallScripts:   result.InstallScripts,
		DeployScripts:    result.DeployScripts,
		Warnings:         result.Warnings,
		Manifest:         result.Manifest,
	})
}

// wasmValidate mirrors POST /api/validate: schema-then-semantic over the topology, returning
// {valid, errors, warnings}. Mirrors HandleValidate's error/warning collection exactly (fresh
// slices so the schema result's backing array is never aliased).
func wasmValidate(_ js.Value, args []js.Value) any {
	var topo model.Topology
	if err := json.Unmarshal([]byte(args[0].String()), &topo); err != nil {
		return errEnvelope(err)
	}
	schema := validator.ValidateSchema(&topo)
	semantic := validator.ValidateSemantic(&topo)
	allErrors := append(append([]validator.ValidationError{}, schema.Errors...), semantic.Errors...)
	allWarnings := append(append([]validator.ValidationError{}, schema.Warnings...), semantic.Warnings...)
	return mustJSON(validateResponse{
		Valid:    len(allErrors) == 0,
		Errors:   allErrors,
		Warnings: allWarnings,
	})
}

// wasmDeployScript mirrors POST /api/deploy-script?format=sh|ps1: compile, then return the
// selected project-level deploy script as a RAW string (not JSON — a bash/PowerShell script
// never begins with '{', so wasmEngine.ts distinguishes it from the {"error":...} envelope).
func wasmDeployScript(_ js.Value, args []js.Value) any {
	var topo model.Topology
	if err := json.Unmarshal([]byte(args[0].String()), &topo); err != nil {
		return errEnvelope(err)
	}
	result, err := localcompile.CompileResult(previewRequest(topo))
	if err != nil {
		return errEnvelope(err)
	}
	name := "deploy-all.sh"
	if args[1].String() == "ps1" {
		name = "deploy-all.ps1"
	}
	return result.DeployScripts[name]
}

// wasmExportFiles returns the per-node bundle file set {nodeID:{relpath:content}} from the
// canonical CompileArtifacts, so wasmEngine.ts can build the preview ZIP client-side.
func wasmExportFiles(_ js.Value, args []js.Value) any {
	var topo model.Topology
	if err := json.Unmarshal([]byte(args[0].String()), &topo); err != nil {
		return errEnvelope(err)
	}
	art, err := localcompile.Compile(previewRequest(topo))
	if err != nil {
		return errEnvelope(err)
	}
	return mustJSON(art.Files)
}

// wasmBuildManifest is THE GATE ENTRY (invariant [1]). It resolves an on-disk contract
// fixture into a conformance.Fixture EXACTLY as internal/conformance/golden_test.go's
// parseFixture/loadTestSigner do — custody string -> render.KeyCustody, and, for a signing
// fixture, the throwaway test signer built from the PEM the second argument carries — then
// runs conformance.BuildManifest and returns conformance.Marshal's canonical bytes. That
// output is byte-identical to the frozen golden iff WASM == Go.
func wasmBuildManifest(_ js.Value, args []js.Value) any {
	var od onDiskFixture
	if err := json.Unmarshal([]byte(args[0].String()), &od); err != nil {
		return errEnvelope(err)
	}
	fx := conformance.Fixture{Name: od.Name}
	if err := json.Unmarshal(od.Topology, &fx.Topology); err != nil {
		return errEnvelope(err)
	}
	switch od.Custody {
	case "airgap", "":
		fx.Custody = render.AirGap
	case "agentheld":
		fx.Custody = render.AgentHeld
	default:
		return errEnvelope(fmt.Errorf("unknown custody %q", od.Custody))
	}
	if od.Signing {
		priv, err := bundlesig.LoadPrivateKeyPEM([]byte(args[1].String()))
		if err != nil {
			return errEnvelope(err)
		}
		fx.Signer = &bundlesig.Signing{
			Priv:      priv,
			PubKeyPEM: bundlesig.MarshalPublicKeyPEM(priv.Public().(ed25519.PublicKey)),
		}
	}
	m, err := conformance.BuildManifest(fx)
	if err != nil {
		return errEnvelope(err)
	}
	out, err := conformance.Marshal(m)
	if err != nil {
		return errEnvelope(err)
	}
	return string(out)
}

// mustJSON marshals v to a JSON string, degrading to an error envelope on the (unexpected)
// marshal failure so the JS side always receives a parseable string.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return errEnvelope(err)
	}
	return string(b)
}

// errEnvelope renders {"error":"<code-or-message>"}. A coded *apierr.Error surfaces its stable
// machine code (the same code the HTTP envelope carries); any other error surfaces its message.
func errEnvelope(err error) string {
	msg := err.Error()
	var coded *apierr.Error
	if errors.As(err, &coded) {
		msg = string(coded.Code())
	}
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}
