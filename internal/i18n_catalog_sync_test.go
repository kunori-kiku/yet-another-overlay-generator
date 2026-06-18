// Package internal hosts cross-package, test-only invariants that no single inner package can
// own. i18n_catalog_sync_test.go is plan-5 step 5: the codes-catalog SSOT guard.
//
// The product has TWO Go-side code-name systems — validator.Code (internal/validator/code.go, a
// validation finding that rides a 200 ValidateResponse) and apierr.Code (internal/apierr/apierr.go,
// a transport/compile failure that rides the HTTP error envelope) — and ONE frontend localization
// catalog keyed error.<code> (frontend/src/i18n/messages/{en,zh}.ts) that localizes BOTH. This test
// asserts the SUBSET relation
//
//	validator.Code ∪ apierr.Code  ⊆  (en.ts error.* keys ∩ zh.ts error.* keys)
//
// so adding a Go code without localizing it in BOTH catalogs reds the build. It is a subset (⊆),
// not an equality, on purpose: the catalog legitimately carries extra keys (e.g. error.generic, the
// shape-agnostic fallback, and forward-declared codes), and the relation must keep holding as the
// catalog grows. This is the SINGLE FE-side code-name list the design keeps — plan-4's proposed
// frontend/src/compiler/codes.ts is SUPERSEDED here (a fourth mirror would defeat the SSOT bar), and
// no //go:generate is introduced.
//
// The two Go registries are unexported (var registry), so this package (internal, distinct from
// validator/apierr) reads the SSOT the structure-aware way: it parses the const declarations of the
// two source files with go/ast and collects every `<Name> Code = "<value>"` string literal. That is
// the exact same source of truth the in-package TestRegistryBijection guards keep honest, so a code
// declared in the const block is seen here whether or not a registry entry exists yet.
package internal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

const (
	// Paths are relative to this test's directory (internal/). The two Go code SSOTs and the two
	// FE catalogs the guard joins them against.
	validatorCodeSrc = "validator/code.go"
	apierrCodeSrc    = "apierr/apierr.go"
	enCatalogSrc     = "../frontend/src/i18n/messages/en.ts"
	zhCatalogSrc     = "../frontend/src/i18n/messages/zh.ts"
)

// codeTypeName is the Go type both code systems declare their consts as: `<Name> Code = "<value>"`.
// parseCodeConsts keys on it so an unrelated string const in the same file is never mistaken for a
// code.
const codeTypeName = "Code"

// parseCodeConsts reads a Go source file and returns the sorted set of string-literal values of
// every `<Name> Code = "<literal>"` const declaration in it. It uses go/ast (not a regex) so it is
// robust to formatting, line moves, and trailing comments, and so it pins the SAME declarations the
// in-package registry-bijection tests do — the authoritative code list, independent of the
// unexported registry map this external package cannot reach.
func parseCodeConsts(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var codes []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// Only `<Name> Code = "..."`: the spec must annotate the Code type explicitly (both
			// code.go and apierr.go do, on every line) and assign a single string literal.
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != codeTypeName {
				continue
			}
			if len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: unquote const value %q: %v", path, lit.Value, err)
			}
			codes = append(codes, val)
		}
	}
	if len(codes) == 0 {
		t.Fatalf("%s: parsed zero Code consts (the const block shape changed — fix parseCodeConsts)", path)
	}
	sort.Strings(codes)
	return codes
}

// catalogKeyRE matches an `'error.<code>':` map key in the i18n catalog (single-quoted, snake_case
// code). It deliberately requires the trailing colon so a bare reference to a key inside a comment
// or a fallback string (e.g. "'error.generic' is the fallback") is NOT counted as a declared key.
var catalogKeyRE = regexp.MustCompile(`'error\.([a-z0-9_]+)':`)

// parseCatalogKeys reads an i18n catalog file and returns the set of declared error.<code> keys
// (the <code> part, without the error. prefix). It is a focused regex rather than a TS parser: the
// catalog is a flat string→string object literal, and the key shape is fixed and machine-checked by
// the FE build, so a line-oriented match is sufficient and avoids pulling a TS toolchain into Go.
func parseCatalogKeys(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read catalog %s: %v", path, err)
	}
	keys := map[string]bool{}
	for _, m := range catalogKeyRE.FindAllStringSubmatch(string(raw), -1) {
		keys[m[1]] = true
	}
	if len(keys) == 0 {
		t.Fatalf("%s: parsed zero error.<code> keys (the catalog shape changed — fix catalogKeyRE)", path)
	}
	return keys
}

// TestI18nCatalogSync is the SSOT guard: every validator.Code and every apierr.Code must have a
// matching error.<code> key in BOTH the en.ts and the zh.ts catalog. A Go code added without a
// localization (in either language) reds here, naming the missing code and the catalog it is absent
// from. This is the mechanized form of code.go's "the frontend still localizes both via ONE
// error.<code> catalog" contract.
func TestI18nCatalogSync(t *testing.T) {
	validatorCodes := parseCodeConsts(t, validatorCodeSrc)
	apierrCodes := parseCodeConsts(t, apierrCodeSrc)
	enKeys := parseCatalogKeys(t, enCatalogSrc)
	zhKeys := parseCatalogKeys(t, zhCatalogSrc)

	check := func(system string, codes []string) {
		for _, code := range codes {
			if !enKeys[code] {
				t.Errorf("%s code %q has no 'error.%s' key in en.ts (%s) — localize it (and zh.ts) before merging",
					system, code, code, enCatalogSrc)
			}
			if !zhKeys[code] {
				t.Errorf("%s code %q has no 'error.%s' key in zh.ts (%s) — localize it (EN/ZH parity required)",
					system, code, code, zhCatalogSrc)
			}
		}
	}
	check("validator.Code", validatorCodes)
	check("apierr.Code", apierrCodes)
}

// TestI18nCatalogParity asserts the two catalogs declare the IDENTICAL error.<code> key set, so the
// subset check in TestI18nCatalogSync can never pass for one language while silently failing for the
// other. (A code present in en.ts but missing from zh.ts would otherwise red TestI18nCatalogSync for
// zh only; this test makes the language imbalance itself the failure, which is the clearer signal.)
func TestI18nCatalogParity(t *testing.T) {
	enKeys := parseCatalogKeys(t, enCatalogSrc)
	zhKeys := parseCatalogKeys(t, zhCatalogSrc)
	for k := range enKeys {
		if !zhKeys[k] {
			t.Errorf("error.%s is in en.ts but missing from zh.ts (EN/ZH catalog parity broken)", k)
		}
	}
	for k := range zhKeys {
		if !enKeys[k] {
			t.Errorf("error.%s is in zh.ts but missing from en.ts (EN/ZH catalog parity broken)", k)
		}
	}
}
