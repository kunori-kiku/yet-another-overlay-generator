package wiredrift

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// drift_test.go — the wire-DTO / omitempty drift gate (framework-refactor plan-10).
//
// It re-adds and STRENGTHENS the FE-field-list half of the drift manifest that plan-5 deleted along
// with internal/conformance/. The old manifest merely SNAPSHOTTED the FE lists (a change reddened
// only until you re-ran -update); this gate instead cross-checks the hand-mirrored contracts against
// their Go source of truth DIRECTLY, so a Go field rename or a missing omitempty entry is a RED BUILD
// with no snapshot to refresh. It pins three ungated hand-mirror classes:
//
//  1. The five frontend *_OMITEMPTY lists (stores/controller/helpers.ts) each equal, EXACTLY, the
//     set of json-tagged omitempty fields on their model struct (Project/Domain/Node/Edge/
//     PublicEndpoint). These lists tell the store which zero-valued fields the SERVER drops on
//     marshal, so canonicalDesign's save/hydrate round-trip compares equal; a Go omitempty field
//     absent from its list (the recorded "EDGE_OMITEMPTY mimic_fallback gap") phantoms a save
//     conflict, and a list entry the model does NOT mark omitempty would wrongly drop a required
//     field. Exact equality catches both directions.
//
//  2. The two frontend pin lists — PIN_FIELDS (lib/normalizeEdges.ts) and ALLOCATION_PIN_FIELDS
//     (stores/topologyStore.ts) — are each a SUBSET of model.Edge's json tags. A Go rename of any
//     pinned field (e.g. pinned_from_port) orphans the FE literal, which the subset check reds.
//
//  3. The controller wire DTOs, hand-duplicated across the agent client (`...Wire`,
//     internal/agent/controller_client.go — "they must match the server's JSON exactly") and the
//     server wire contract (`...JSON`, internal/api/wire_controller.go). Every agent `<base>Wire`
//     must have a server `<base>JSON` counterpart with an IDENTICAL json-tag signature set
//     (field name + options, so an omitempty drift reds too). A field renamed on one side but not
//     the other silently breaks agent↔controller round-trip today; here it is a red build.
//     (The third, camelCase-mapped mirror — frontend/src/types/controller.ts + the api/controller/*
//     mappers — is NOT a json-tag copy and is gated separately by the FE *.conformance.test.ts
//     suite; see the report note.)
//
// FAIL-CLOSED (invariant [5]): this gate only EXTENDS the guarantee. There is deliberately no
// allowlist / escape hatch — a legitimately new omitempty field or wire field MUST be reflected in
// the mirror (the RED BUILD forces that edit), because a weakened gate is worse than none. The gate
// reads every authority as SOURCE (go/ast + regexp), imports none of them, and so cannot itself
// drift out of sync with what ships.
//
// Non-vacuity: authored against the live tree, this gate first reddened on the pre-existing
// EDGE_OMITEMPTY mimic_fallback gap (model.Edge tags mimic_fallback omitempty; the list omitted it);
// plan-10 fixes that gap so the gate is green, and TestGateIsNonVacuous proves a mutated Go tag /
// list entry reds it.

// Source paths, relative to this package directory (internal/wiredrift/).
const (
	modelSrc          = "../model/topology.go"
	agentWireSrc      = "../agent/controller_client.go"
	apiWireSrc        = "../api/wire_controller.go"
	feNormalizeEdges  = "../../frontend/src/lib/normalizeEdges.ts"
	feTopologyStore   = "../../frontend/src/stores/topologyStore.ts"
	feControllerHelps = "../../frontend/src/stores/controller/helpers.ts"
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

// TestFEPinListsAreEdgeFields asserts every element of the FE pin lists is a real model.Edge json
// tag (subset). A Go rename of a pinned field orphans the FE literal and reds here.
func TestFEPinListsAreEdgeFields(t *testing.T) {
	edge, ok := structsOf(t, modelSrc)["Edge"]
	if !ok {
		t.Fatalf("model struct Edge not found in %s", modelSrc)
	}
	edgeNames := allNames(edge)
	cases := []struct {
		list string
		path string
	}{
		{"PIN_FIELDS", feNormalizeEdges},
		{"ALLOCATION_PIN_FIELDS", feTopologyStore},
	}
	for _, c := range cases {
		t.Run(c.list, func(t *testing.T) {
			for _, f := range feArrayElements(t, c.path, c.list) {
				if !edgeNames[f] {
					t.Errorf("%s contains %q, which is NOT a model.Edge json tag — a Go rename orphaned it.\n"+
						"  Reconcile %s (%s) with model.Edge's json tags.", c.list, f, c.list, c.path)
				}
			}
		})
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
}
