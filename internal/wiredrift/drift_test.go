package wiredrift

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// drift_test.go — the wire-DTO / omitempty drift gate (framework-refactor plan-10).
//
// It re-adds and STRENGTHENS the FE-field-list half of the drift manifest that plan-5 deleted along
// with internal/conformance/. The old manifest merely SNAPSHOTTED the FE lists (a change reddened
// only until you re-ran -update); this gate instead cross-checks the hand-mirrored contracts against
// their Go source of truth DIRECTLY, so a Go field rename or a missing omitempty entry is a RED BUILD
// with no snapshot to refresh. It pins five ungated hand-mirror classes:
//
//  1. The six frontend *_OMITEMPTY lists (stores/controller/helpers.ts) each equal, EXACTLY, the
//     set of json-tagged omitempty fields on their model struct (Project/Domain/Node/Edge/
//     PublicEndpoint/TelemetryProbe). These lists tell the store which zero-valued fields the SERVER
//     drops on marshal, so canonicalDesign's save/hydrate round-trip compares equal; a Go omitempty field
//     absent from its list (the recorded "EDGE_OMITEMPTY mimic_fallback gap") phantoms a save
//     conflict, and a list entry the model does NOT mark omitempty would wrongly drop a required
//     field. Exact equality catches both directions.
//
//  2. The frontend persisted-pin list — PERSISTED_ALLOCATION_PIN_FIELDS
//     (lib/allocationFields.ts) — is a SUBSET of model.Edge's json tags. A Go rename of any pinned
//     field (e.g. pinned_from_port) orphans the FE literal, which the subset check reds. The
//     seven-field server list uses a TypeScript spread and is checked by the frontend unit suite.
//
//  3. The controller wire DTOs, hand-duplicated across the agent client (`...Wire`,
//     internal/agent/controller_client.go — "they must match the server's JSON exactly") and the
//     server wire contract (`...JSON`, internal/api/wire_controller.go). Every agent `<base>Wire`
//     must have a server `<base>JSON` counterpart with an IDENTICAL json-tag signature set
//     (field name + options, so an omitempty drift reds too). A field renamed on one side but not
//     the other silently breaks agent↔controller round-trip today; here it is a red build.
//
//  4. The OPERATOR-panel controller wire DTOs (post-refactor-debt-paydown plan-10). The server
//     response/request structs the panel consumes (settingsJSON, nodeJSON, the stage / deploy-preview
//     / audit / fleet / keystone DTOs across the whole internal/api package) are hand-mirrored by
//     snake_case `*JSON` / `*Wire` interfaces in frontend/src/api/controller/*.ts (+ lib/nodeConditions.ts).
//     TestControllerWireDTOsMirrorFE cross-checks the Go json-tag NAME set against the FE interface
//     field-NAME set (snake_case ↔ snake_case — the wire is snake_case, so no lossy camel↔snake
//     transform is involved): bidirectional for the full mirrors, FE⊆Go for a documented projection
//     (the audit view drops the server-only `seq`). This closes the gap ASSESSMENT.md flagged — a Go
//     field rename that silently degrades the panel to blank/0/undefined through its defensive `??`
//     mappers is now a RED build. The internal/api package is walked by SYMBOL (structsOfDir parses
//     every non-test file) so it is robust to which file a DTO lives in. See controllerWireCases for
//     the covered set and the DTOs left as documented follow-ups (auth/session, the operator-credential
//     inline response types, the nested release-pin result, telemetry-history — each a structural,
//     non-flat-mirror shape whose FE side is not a named snake_case interface).
//
//     (The camelCase RUNTIME mirror — frontend/src/types/controller.ts + the api/controller/* boundary
//     mappers — is NOT a json-tag copy; gating it directly needs the lossy camel↔snake transform the
//     assessment warned against, so it stays covered by the FE unit suite under src/api/ + src/lib/.)
//
//  5. The telemetry-history chart-family catalog. The executable Go catalog drives controller
//     projection/API encoding; the frontend literal drives exhaustive parser and renderer registries.
//     Exact equality makes a new chart family fail CI until the panel can both parse and render it.
//
//  6. The automatic-device numeric catalog. The Go leaf contract and frontend leaf literal must have
//     the same exact key/kind/unit triples, so a metric cannot be collected or accepted live while
//     silently lacking the correct chart semantics.
//
// FAIL-CLOSED (invariant [5]): this gate only EXTENDS the guarantee. There is deliberately no
// allowlist / escape hatch — a legitimately new omitempty field or wire field MUST be reflected in
// the mirror (the RED BUILD forces that edit), because a weakened gate is worse than none. The gate
// reads wire authorities as SOURCE (go/ast + regexp). The chart gates import only the leaf
// telemetrymetric/devicemetric catalogs so they compare executable definitions rather than a second
// Go manifest.
//
// Non-vacuity: authored against the live tree, this gate first reddened on the pre-existing
// EDGE_OMITEMPTY mimic_fallback gap (model.Edge tags mimic_fallback omitempty; the list omitted it);
// framework-refactor plan-10 fixed that gap so the gate is green, and TestGateIsNonVacuous proves a
// mutated Go tag / list entry reds it. The controller Go↔FE half [4] is proven the same way: the
// non-vacuity test runs the full pipeline (structsOfDir → feInterfaceFields → comparator) against a
// real pair and asserts a seeded FE-side rename reds (both a server-only miss and an FE-only extra).

// Source paths, relative to this package directory (internal/wiredrift/).
const (
	modelSrc           = "../model/topology.go"
	agentWireSrc       = "../agent/controller_client.go"
	apiWireSrc         = "../api/wire_controller.go"
	probeMetricSrc     = "../probemetric/result.go"
	feAllocationFields = "../../frontend/src/lib/allocationFields.ts"
	feControllerHelps  = "../../frontend/src/stores/controller/helpers.ts"
	feProbeResults     = "../../frontend/src/lib/probeResults.ts"
	feTelemetryHist    = "../../frontend/src/lib/telemetryHistory.ts"
	feDeviceTelemetry  = "../../frontend/src/types/deviceTelemetry.ts"
)

// jsonField is one struct field's on-the-wire identity: its json name, whether it carries
// `,omitempty`, and the full sig (name + options) used for exact wire-mirror comparison.
type jsonField struct {
	name      string
	omitempty bool
	sig       string // the raw json tag value, e.g. "agent_version,omitempty"
}

// structsOf parses a Go source file and returns each struct type's json fields keyed by struct name.
// Embedded / untagged / json:"-" fields are skipped (they carry no by-name wire contract). Reading
// SOURCE (not importing) keeps this package free of any edge to the tree it guards.
func structsOf(t *testing.T, path string) map[string][]jsonField {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string][]jsonField{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			var fields []jsonField
			for _, f := range st.Fields.List {
				if f.Tag == nil {
					continue // embedded / untagged
				}
				lit, err := strconv.Unquote(f.Tag.Value)
				if err != nil {
					t.Fatalf("%s: unquote struct tag %q: %v", path, f.Tag.Value, err)
				}
				jsonTag := reflect.StructTag(lit).Get("json")
				if jsonTag == "" {
					continue
				}
				parts := strings.Split(jsonTag, ",")
				name := parts[0]
				if name == "" || name == "-" {
					continue
				}
				omit := false
				for _, o := range parts[1:] {
					if o == "omitempty" {
						omit = true
					}
				}
				fields = append(fields, jsonField{name: name, omitempty: omit, sig: jsonTag})
			}
			out[ts.Name.Name] = fields
		}
	}
	return out
}

// omitemptyNames returns the sorted json names of the omitempty-tagged fields.
func omitemptyNames(fields []jsonField) []string {
	var out []string
	for _, f := range fields {
		if f.omitempty {
			out = append(out, f.name)
		}
	}
	sort.Strings(out)
	return out
}

// allNames returns the set of every json field name on a struct.
func allNames(fields []jsonField) map[string]bool {
	out := map[string]bool{}
	for _, f := range fields {
		out[f.name] = true
	}
	return out
}

// sigSet returns the sorted set of full json-tag signatures (name + options) — the wire identity.
func sigSet(fields []jsonField) []string {
	var out []string
	for _, f := range fields {
		out = append(out, f.sig)
	}
	sort.Strings(out)
	return out
}

// feArrayElements extracts the sorted string elements of a `const <name> = [ ... ]` TS array literal.
// The FE field lists are flat string arrays (single/double-quoted, possibly multi-line, optionally
// `] as const`); this scopes to the slice between the declaration and its first closing bracket and
// pulls every quoted element. Sorted, so element reordering (not a contract change) never reds while
// an add/remove/rename does. (Lifted verbatim from the retired conformance drift test.)
func feArrayElements(t *testing.T, path, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(raw)

	declRE := regexp.MustCompile(`(?:export\s+)?const\s+` + regexp.QuoteMeta(name) + `\s*(?::[^=]+)?=\s*\[`)
	loc := declRE.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("%s: array const %s not found", path, name)
	}
	rest := src[loc[1]:]
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		t.Fatalf("%s: array const %s has no closing bracket", path, name)
	}
	body := rest[:end]

	elemRE := regexp.MustCompile(`'([^']*)'|"([^"]*)"`)
	var elems []string
	for _, m := range elemRE.FindAllStringSubmatch(body, -1) {
		if m[1] != "" {
			elems = append(elems, m[1])
		} else {
			elems = append(elems, m[2])
		}
	}
	if len(elems) == 0 {
		t.Fatalf("%s: array const %s parsed zero elements", path, name)
	}
	sort.Strings(elems)
	return elems
}

func feDeviceNumericDefinitions(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(feDeviceTelemetry)
	if err != nil {
		t.Fatalf("read %s: %v", feDeviceTelemetry, err)
	}
	src := string(raw)
	declRE := regexp.MustCompile(`(?:export\s+)?const\s+DEVICE_NUMERIC_DEFINITIONS\s*=\s*\[`)
	loc := declRE.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("%s: DEVICE_NUMERIC_DEFINITIONS array not found", feDeviceTelemetry)
	}
	rest := src[loc[1]:]
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		t.Fatalf("%s: DEVICE_NUMERIC_DEFINITIONS has no closing bracket", feDeviceTelemetry)
	}
	entryRE := regexp.MustCompile(`\{\s*key:\s*'([^']+)'\s*,\s*kind:\s*'([^']+)'\s*,\s*unit:\s*'([^']+)'\s*\}`)
	matches := entryRE.FindAllStringSubmatch(rest[:end], -1)
	if len(matches) == 0 {
		t.Fatalf("%s: DEVICE_NUMERIC_DEFINITIONS has no parseable entries", feDeviceTelemetry)
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1]+"|"+match[2]+"|"+match[3])
	}
	sort.Strings(out)
	return out
}

// diffSets returns the elements missing from got and the extras present in got but not want.
func diffSets(want, got []string) (missing, extra []string) {
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
	for _, s := range want {
		if !g[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range got {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// TestFEOmitemptyListsMatchModel asserts each FE *_OMITEMPTY list equals, exactly, its model struct's
// omitempty json-tag set. A missing omitempty entry OR a stale extra entry reds. This is the pin the
// team has been bitten by repeatedly ("a new omitempty node field MUST go in NODE_OMITEMPTY").
func TestFEOmitemptyListsMatchModel(t *testing.T) {
	structs := structsOf(t, modelSrc)
	cases := []struct {
		list      string
		path      string
		modelType string
	}{
		{"PROJECT_OMITEMPTY", feControllerHelps, "Project"},
		{"DOMAIN_OMITEMPTY", feControllerHelps, "Domain"},
		{"NODE_OMITEMPTY", feControllerHelps, "Node"},
		{"EDGE_OMITEMPTY", feControllerHelps, "Edge"},
		{"PUBLIC_ENDPOINT_OMITEMPTY", feControllerHelps, "PublicEndpoint"},
		{"TELEMETRY_PROBE_OMITEMPTY", feControllerHelps, "TelemetryProbe"},
	}
	for _, c := range cases {
		t.Run(c.list, func(t *testing.T) {
			fields, ok := structs[c.modelType]
			if !ok {
				t.Fatalf("model struct %s not found in %s", c.modelType, modelSrc)
			}
			want := omitemptyNames(fields)
			got := feArrayElements(t, c.path, c.list)
			missing, extra := diffSets(want, got)
			if len(missing) > 0 || len(extra) > 0 {
				t.Errorf("%s drifted from model.%s omitempty json tags.\n"+
					"  missing from the FE list (model marks omitempty, list omits): %v\n"+
					"  extra in the FE list (list drops it, model does NOT mark omitempty): %v\n"+
					"  Reconcile %s (%s) with model.%s's `,omitempty` json tags.",
					c.list, c.modelType, missing, extra, c.list, c.path, c.modelType)
			}
		})
	}
}

// TestFEPinListsAreEdgeFields asserts every persisted frontend pin is a real model.Edge json tag.
// A Go rename of a pinned field orphans the FE literal and reds here.
func TestFEPinListsAreEdgeFields(t *testing.T) {
	edge, ok := structsOf(t, modelSrc)["Edge"]
	if !ok {
		t.Fatalf("model struct Edge not found in %s", modelSrc)
	}
	edgeNames := allNames(edge)
	const list = "PERSISTED_ALLOCATION_PIN_FIELDS"
	for _, f := range feArrayElements(t, feAllocationFields, list) {
		if !edgeNames[f] {
			t.Errorf("%s contains %q, which is NOT a model.Edge json tag — a Go rename orphaned it.\n"+
				"  Reconcile %s (%s) with model.Edge's json tags.", list, f, list, feAllocationFields)
		}
	}
}

// TestWireDTOsMirror asserts every agent `<base>Wire` DTO has a server `<base>JSON` counterpart with
// an identical json-tag signature set (name + options). A field renamed / an omitempty flipped on one
// side but not the other silently breaks the agent↔controller wire round-trip; here it reds.
func TestWireDTOsMirror(t *testing.T) {
	agent := structsOf(t, agentWireSrc)
	api := structsOf(t, apiWireSrc)

	var bases []string
	for name := range agent {
		if strings.HasSuffix(name, "Wire") {
			bases = append(bases, strings.TrimSuffix(name, "Wire"))
		}
	}
	sort.Strings(bases)
	if len(bases) == 0 {
		t.Fatalf("no `...Wire` DTOs found in %s — the parser or the file layout changed", agentWireSrc)
	}

	for _, base := range bases {
		t.Run(base, func(t *testing.T) {
			apiFields, ok := api[base+"JSON"]
			if !ok {
				t.Fatalf("agent DTO %sWire has NO server counterpart %sJSON in %s "+
					"(every agent wire DTO must mirror a controller JSON DTO)", base, base, apiWireSrc)
			}
			wantSigs := sigSet(api[base+"JSON"])
			gotSigs := sigSet(agent[base+"Wire"])
			missing, extra := diffSets(wantSigs, gotSigs)
			if len(missing) > 0 || len(extra) > 0 {
				t.Errorf("wire DTO %sWire (agent) drifted from %sJSON (controller).\n"+
					"  in %sJSON but not %sWire: %v\n"+
					"  in %sWire but not %sJSON: %v\n"+
					"  These two json contracts MUST be identical (%s ↔ %s).",
					base, base, base, base, missing, base, base, extra, agentWireSrc, apiWireSrc)
			}
			_ = apiFields
		})
	}
}

// TestProbeResultWireMirror pins the shared Go active-probe result against the panel's defensive
// snake_case boundary. Probe results used to be a private agent struct plus a hand-written TS type;
// adding history made that drift capable of silently dropping a chart dimension, so the source-read
// gate is deliberately bidirectional here.
func TestProbeResultWireMirror(t *testing.T) {
	goFields, ok := structsOf(t, probeMetricSrc)["Result"]
	if !ok {
		t.Fatalf("probemetric.Result not found in %s", probeMetricSrc)
	}
	want := fieldNames(goFields)
	got := feInterfaceFields(t, feProbeResults, "ProbeResultWire")
	missing, extra := diffSets(want, got)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("probemetric.Result drifted from ProbeResultWire.\n"+
			"  Go fields missing from the frontend boundary: %v\n"+
			"  frontend fields missing from the Go contract: %v", missing, extra)
	}
}

// TestTelemetryHistoryChartFamiliesMirrorFrontend closes the non-flat telemetry-history gap left by
// the DTO mirror below. Go families are executable controller/API registration identities; the TS
// literal is consumed by exhaustive parser and renderer registries. A family added on only one side
// is therefore a release-blocking drift rather than a silently uncharted metric.
func TestTelemetryHistoryChartFamiliesMirrorFrontend(t *testing.T) {
	goFamilies := telemetrymetric.ChartFamilies()
	want := make([]string, len(goFamilies))
	for i, family := range goFamilies {
		want[i] = string(family)
	}
	sort.Strings(want)
	got := feArrayElements(t, feTelemetryHist, "HISTORY_CHART_FAMILIES")
	missing, extra := diffSets(want, got)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("telemetry history chart families drifted between Go and the frontend.\n"+
			"  Go families missing from parser/render registries: %v\n"+
			"  frontend families missing from the Go catalog: %v", missing, extra)
	}
}

// TestDeviceNumericDefinitionsMirrorFrontend pins the full key/kind/unit contract, not only the
// top-level device chart family. The frontend literal is shared by live parsing, exact-history
// parsing, and rendering, so any missing, extra, wrong-kind, or wrong-unit definition blocks release.
func TestDeviceNumericDefinitionsMirrorFrontend(t *testing.T) {
	want := make([]string, 0, len(devicemetric.NumericDefinitions()))
	for _, definition := range devicemetric.NumericDefinitions() {
		want = append(want, string(definition.Key)+"|"+string(definition.Kind)+"|"+definition.Unit)
	}
	sort.Strings(want)
	got := feDeviceNumericDefinitions(t)
	missing, extra := diffSets(want, got)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("device numeric definitions drifted between Go and the frontend.\n"+
			"  Go definitions missing from the frontend authority: %v\n"+
			"  frontend definitions missing from Go: %v", missing, extra)
	}
}

// --- [4] the operator-panel controller wire DTO ↔ FE snake_case mirror gate (plan-10) ---

// FE snake_case controller wire-mirror sources (the *JSON / *Wire interfaces the operator panel maps
// at its transport boundary). apiDir is walked as a PACKAGE (every non-test .go file) so a DTO is
// found by SYMBOL regardless of which file defines it — settingsJSON has moved between
// handler_bootstrap.go / handler_settings.go across merges, so hardcoding a file would be brittle.
const (
	apiDir           = "../api"
	feCtlSettings    = "../../frontend/src/api/controller/settings.ts"
	feCtlFleet       = "../../frontend/src/api/controller/fleet.ts"
	feCtlDeploy      = "../../frontend/src/api/controller/deploy.ts"
	feCtlKeystone    = "../../frontend/src/api/controller/keystone.ts"
	feNodeConditions = "../../frontend/src/lib/nodeConditions.ts"
)

// wireMirrorMode selects how strict the Go↔FE field-name comparison is for one DTO.
type wireMirrorMode int

const (
	// wireExact: the FE interface mirrors the FULL server DTO — bidirectional name-set equality, so
	// a field added/removed/renamed on EITHER side reds.
	wireExact wireMirrorMode = iota
	// wireFEProjection: the FE view is a documented PROJECTION consuming a SUBSET of the server
	// fields. Every FE field MUST still be a real server json tag (the drift that silently blanks
	// the panel via a `??` mapper), but the server MAY carry fields the panel never renders. This is
	// NOT an escape hatch — it is the structural fact that the panel's audit row is a projection; it
	// keeps the load-bearing direction (FE⊆Go) fail-closed, only relaxing the reverse.
	wireFEProjection
)

// controllerWireCases is the covered set: each controller wire DTO (internal/api, found by symbol)
// and its FE snake_case mirror interface. Every pair here matches on the live tree.
//
// DOCUMENTED FOLLOW-UPS (deliberately NOT covered — each is a non-flat-mirror whose FE side is not a
// named snake_case interface this parser can pin, so a robust gate over the core beats a brittle one
// over everything; see the assessment's "cover the CORE robustly + list the rest"):
//   - auth/session: LoginResponseJSON, SessionResponseJSON, passkeyChallengeJSON, TOTPStatusJSON,
//     TOTPEnrollJSON (frontend/src/api/controller/auth.ts) ↔ handler_login.go / cookie_session.go /
//     handler_passkey.go / handler_totp.go — some carry nested inline object types.
//   - keystone operator-credential: operatorCredentialPinResultJSON / operatorCredentialStatusJSON —
//     the panel consumes these via INLINE anonymous response types (keystone.ts), no named interface.
//   - release pins: releasePinRequestJSON / releasePinResponseJSON (release_pins.go) — release.ts maps
//     them to a camelCase result with a NESTED `data` object, not a flat snake_case interface.
//   - telemetry-history field shapes: historyResponse / historyBucket / metricAgg
//     (telemetry_history.go) — parsed defensively rather than mirrored as flat snake_case interfaces.
//     Its chart-family coverage is separately fail-closed in
//     TestTelemetryHistoryChartFamiliesMirrorFrontend above.
//   - the agent↔controller `...Wire` / `...JSON` pairs are already covered by TestWireDTOsMirror above.
//   - the map-value types AgentPin (↔ renderer.Artifact) and MimicDebPinJSON (↔ model.MimicDebPin) are
//     nested inside agent_bins / mimic_debs; their enclosing map fields ARE gated here.
var controllerWireCases = []struct {
	goStruct string // the internal/api DTO struct name (located by symbol via the package walk)
	feName   string // the FE mirror interface name
	fePath   string // the FE file it lives in
	mode     wireMirrorMode
	note     string // for a projection: the server-only fields the panel legitimately drops
}{
	{"settingsJSON", "SettingsJSON", feCtlSettings, wireExact, ""},
	{"nodeJSON", "NodeJSON", feCtlFleet, wireExact, ""},
	{"conditionJSON", "ConditionWire", feNodeConditions, wireExact, ""},
	{"auditEntryJSON", "AuditEntryJSON", feCtlFleet, wireFEProjection, "seq (audit-chain bookkeeping the panel never renders)"},
	{"auditResponseJSON", "AuditResponseJSON", feCtlFleet, wireExact, ""},
	{"enrollmentTokenResponseJSON", "EnrollmentTokenResponseJSON", feCtlFleet, wireExact, ""},
	{"revokeResponseJSON", "RevokeResponseJSON", feCtlFleet, wireExact, ""},
	{"rekeyAllResponseJSON", "RekeyAllResponseJSON", feCtlFleet, wireExact, ""},
	{"clearRekeyResponseJSON", "ClearRekeyResponseJSON", feCtlFleet, wireExact, ""},
	{"stageResponseJSON", "StageResponseJSON", feCtlDeploy, wireExact, ""},
	{"deployPreviewNodeJSON", "deployPreviewNodeJSON", feCtlDeploy, wireExact, ""},
	{"deployPreviewResponseJSON", "deployPreviewResponseJSON", feCtlDeploy, wireExact, ""},
	{"generationResponseJSON", "GenerationResponseJSON", feCtlDeploy, wireExact, ""},
	{"trustListResponseJSON", "TrustListResponseJSON", feCtlKeystone, wireExact, ""},
}

// TestControllerWireDTOsMirrorFE cross-checks each covered controller wire DTO (Go json tags) against
// its hand-mirrored FE snake_case interface (field names). It closes the ASSESSMENT.md gap: the panel
// data layer is a hand-mirror held only by discipline, and a Go field rename would silently degrade
// the panel to blank/0/undefined via its defensive `??` mappers with no red build. Here it reds.
func TestControllerWireDTOsMirrorFE(t *testing.T) {
	api := structsOfDir(t, apiDir)
	for _, c := range controllerWireCases {
		t.Run(c.goStruct, func(t *testing.T) {
			fields, ok := api[c.goStruct]
			if !ok {
				t.Fatalf("controller DTO %s not found in the %s package walk — it was renamed/removed, or the "+
					"package layout changed (this gate finds it by SYMBOL, not by file)", c.goStruct, apiDir)
			}
			goNames := fieldNames(fields)
			feNames := feInterfaceFields(t, c.fePath, c.feName)
			missing, extra := diffSets(goNames, feNames) // missing = server-only; extra = FE-only
			switch c.mode {
			case wireExact:
				if len(missing) > 0 || len(extra) > 0 {
					t.Errorf("controller wire DTO %s (Go) drifted from %s (%s).\n"+
						"  in %s json tags but NOT the FE interface (add it to the FE mirror): %v\n"+
						"  in the FE interface but NOT %s json tags (a Go rename orphaned it — the panel now "+
						"reads undefined via its ?? mapper): %v\n"+
						"  These snake_case contracts MUST match field-for-field.",
						c.goStruct, c.feName, c.fePath, c.goStruct, missing, c.goStruct, extra)
				}
			case wireFEProjection:
				if len(extra) > 0 {
					t.Errorf("FE interface %s (%s) references %v, which is NOT a %s json tag — a Go rename "+
						"orphaned an FE-consumed field (the panel reads undefined via its ?? mapper).\n"+
						"  (%s is a documented projection; server-only fields it legitimately drops: %s.)",
						c.feName, c.fePath, extra, c.goStruct, c.feName, c.note)
				}
			}
		})
	}
}

// fieldNames returns the sorted set of every json field name on a struct.
func fieldNames(fields []jsonField) []string {
	var out []string
	for _, f := range fields {
		out = append(out, f.name)
	}
	sort.Strings(out)
	return out
}

// structsOfDir parses EVERY non-test .go file in dir and merges their struct maps, so a DTO can be
// located by SYMBOL regardless of which file in the package defines it. Go forbids duplicate type
// names within one package, so the merge is unambiguous. Reads SOURCE like structsOf — no import edge
// to the package it guards, immune to the very drift it polices.
func structsOfDir(t *testing.T, dir string) map[string][]jsonField {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	out := map[string][]jsonField{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for k, v := range structsOf(t, filepath.Join(dir, name)) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		t.Fatalf("no structs parsed from %s — the package walk found nothing (layout changed?)", dir)
	}
	return out
}

// feInterfaceFields extracts the top-level field names of a TS `interface <name> { ... }`. It
// locates the declaration, then single-pass scans from the opening brace tracking brace depth while
// blanking // and /* */ comments and string / template literals; only characters at depth 1 enter a
// "skeleton", so an inline object-type field (e.g. `telemetry?: { wireguard_peers?: ... }`) never
// leaks its nested members. In the skeleton, every top-level `ident` / `ident?` that precedes a `:`
// is a field name. Sorted, so a reordering (not a contract change) never reds. Source-read like
// feArrayElements — no import edge; robust to the multi-field-per-line and inline-object shapes the
// live FE interfaces use.
func feInterfaceFields(t *testing.T, path, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(raw)

	declRE := regexp.MustCompile(`(?:export\s+)?interface\s+` + regexp.QuoteMeta(name) + `\b[^{]*\{`)
	loc := declRE.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("%s: interface %s not found", path, name)
	}
	rest := src[loc[1]-1:] // start AT the opening brace so the depth counter opens on it

	var sk strings.Builder // depth-1 skeleton: comments, strings, and depth>=2 content removed
	depth := 0
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == '/' && i+1 < len(rest) && rest[i+1] == '/':
			for i < len(rest) && rest[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(rest) && rest[i+1] == '*':
			i += 2
			for i+1 < len(rest) && !(rest[i] == '*' && rest[i+1] == '/') {
				i++
			}
			i++ // consume the closing '/'
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			for i < len(rest) && rest[i] != q {
				if rest[i] == '\\' {
					i++ // skip an escaped char
				}
				i++
			}
		case c == '{':
			depth++ // the outer brace opens depth 1; an inline object opens depth 2 (not written)
		case c == '}':
			depth--
			if depth == 0 {
				i = len(rest) // matched the interface's closing brace — stop
			}
		default:
			if depth == 1 {
				sk.WriteByte(c)
			}
		}
	}

	// At depth 1 the only `ident:` occurrences are member names (type-internal identifiers are
	// followed by `[`, `<`, `,`, `|`, `>` or whitespace — never `:` — and inline objects are blanked).
	fieldRE := regexp.MustCompile(`(?:^|[;,\n])\s*([A-Za-z_$][\w$]*)\s*\??\s*:`)
	var fields []string
	for _, m := range fieldRE.FindAllStringSubmatch(sk.String(), -1) {
		fields = append(fields, m[1])
	}
	if len(fields) == 0 {
		t.Fatalf("%s: interface %s parsed zero fields — the extractor or the interface shape changed", path, name)
	}
	sort.Strings(fields)
	return fields
}

// containsStr reports whether xs contains want.
func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestGateIsNonVacuous proves the gate's comparators actually red on drift (guards against a gate
// that passes because a parser returned nothing or a comparator is inert). It mutates in-memory
// copies of the parsed authorities — NOT the source — and asserts each seeded drift is detected.
func TestGateIsNonVacuous(t *testing.T) {
	edge := structsOf(t, modelSrc)["Edge"]
	modelOmit := omitemptyNames(edge)

	// (a) A missing omitempty entry must be caught: drop one model field from the "FE list".
	feLike := append([]string(nil), modelOmit...)
	if len(feLike) == 0 {
		t.Fatal("model.Edge has no omitempty fields — fixture assumption broke")
	}
	feMissingOne := feLike[:len(feLike)-1]
	if missing, _ := diffSets(modelOmit, feMissingOne); len(missing) == 0 {
		t.Error("non-vacuity FAIL: dropping an omitempty field was NOT detected as missing")
	}

	// (b) An extra/renamed entry must be caught.
	feExtra := append(append([]string(nil), modelOmit...), "renamed_field_that_is_not_in_model")
	if _, extra := diffSets(modelOmit, feExtra); len(extra) == 0 {
		t.Error("non-vacuity FAIL: an extra FE list entry was NOT detected")
	}

	// (c) A wire-signature drift must be caught: flip an omitempty on one copy.
	report := structsOf(t, apiWireSrc)["reportRequestJSON"]
	if len(report) == 0 {
		t.Fatal("reportRequestJSON not found — fixture assumption broke")
	}
	base := sigSet(report)
	mutated := append([]string(nil), base...)
	mutated[0] = mutated[0] + ",omitempty-DRIFT"
	if missing, extra := diffSets(base, mutated); len(missing) == 0 && len(extra) == 0 {
		t.Error("non-vacuity FAIL: a mutated wire tag signature was NOT detected")
	}

	// (d) The controller Go↔FE snake_case comparison [4] must red on a one-sided drift, exercising the
	// FULL pipeline (structsOfDir → feInterfaceFields → comparator), not just diffSets in isolation.
	// Parse a real pair, assert the parser is non-vacuous + the pair currently matches, then seed a
	// FE-side rename and assert BOTH a server-only miss AND an FE-only extra are flagged.
	feSettings := feInterfaceFields(t, feCtlSettings, "SettingsJSON")
	if len(feSettings) < 5 {
		t.Fatalf("non-vacuity FAIL: feInterfaceFields(SettingsJSON) parsed %d fields — the TS parser is vacuous", len(feSettings))
	}
	if !containsStr(feSettings, "public_agent_url") {
		t.Errorf("non-vacuity FAIL: feInterfaceFields did not extract the known wire field public_agent_url from %v", feSettings)
	}
	goSettings := fieldNames(structsOfDir(t, apiDir)["settingsJSON"])
	if m, e := diffSets(goSettings, feSettings); len(m) != 0 || len(e) != 0 {
		t.Fatalf("fixture assumption broke: settingsJSON ↔ SettingsJSON should match exactly (server-only %v, FE-only %v)", m, e)
	}
	renamedFE := make([]string, 0, len(feSettings))
	for _, f := range feSettings {
		if f == "public_agent_url" {
			renamedFE = append(renamedFE, "public_agent_uri") // seed a one-field rename on the FE side
			continue
		}
		renamedFE = append(renamedFE, f)
	}
	sort.Strings(renamedFE)
	if m, e := diffSets(goSettings, renamedFE); len(m) == 0 || len(e) == 0 {
		t.Errorf("non-vacuity FAIL: a renamed controller wire field was NOT detected by the comparator "+
			"(server-only=%v, FE-only=%v; both must be non-empty)", m, e)
	}
}
