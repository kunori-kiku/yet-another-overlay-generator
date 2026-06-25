package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestZipBundleFiles: the bundle ZIP packs every file deterministically and round-trips byte-exact.
func TestZipBundleFiles(t *testing.T) {
	files := map[string][]byte{
		"wireguard/wg-alpha.conf": []byte("[Interface]\n"),
		"install.sh":              []byte("#!/usr/bin/env bash\n"),
		"trustlist.json":          []byte("{}"),
	}
	buf, err := zipBundleFiles(files)
	if err != nil {
		t.Fatalf("zipBundleFiles: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if len(got) != len(files) {
		t.Fatalf("zip has %d files, want %d", len(got), len(files))
	}
	for name, want := range files {
		if got[name] != string(want) {
			t.Errorf("zip[%s] = %q, want %q", name, got[name], want)
		}
	}
}

// TestHandleManualNodeBundle_ValidationAndGating covers the download endpoint's guards (the served-bundle
// 200 path reuses GetServedConfig — exercised by the agent /config tests — + the unit-tested
// zipBundleFiles): a missing node param is 400; a managed/unknown node is 404 (managed nodes pull via
// their agent); a manual node with no promoted bundle yet is 404; and the route is operator-gated.
func TestHandleManualNodeBundle_ValidationAndGating(t *testing.T) {
	env := newCtlTestEnv(t)
	topo := model.Topology{
		Project: model.Project{ID: "p", Name: "p"},
		Nodes: []model.Node{
			{ID: "node-mike", Name: "mike", Role: "router", DomainID: "d1", DeploymentMode: model.DeploymentManual, WireGuardPublicKey: "manual-pub"},
			{ID: "node-alpha", Name: "alpha", Role: "router", DomainID: "d1"},
		},
	}
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatalf("marshal topo: %v", err)
	}
	if _, err := env.store.PutTopology(context.Background(), testTenant, raw); err != nil {
		t.Fatalf("PutTopology: %v", err)
	}

	getAuthed := func(query string) int {
		req, _ := http.NewRequest(http.MethodGet, env.opURL("manual-node-bundle")+query, nil)
		req.Header.Set("Authorization", "Bearer "+testOperatorToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if st := getAuthed(""); st != http.StatusBadRequest {
		t.Errorf("missing node param = %d, want 400", st)
	}
	if st := getAuthed("?node=node-alpha"); st != http.StatusNotFound {
		t.Errorf("managed node download = %d, want 404 (managed nodes pull via agent)", st)
	}
	if st := getAuthed("?node=does-not-exist"); st != http.StatusNotFound {
		t.Errorf("unknown node download = %d, want 404", st)
	}
	if st := getAuthed("?node=node-mike"); st != http.StatusNotFound {
		t.Errorf("manual node without a promoted bundle = %d, want 404", st)
	}

	// Operator-gated: an unauthenticated request must never return the bundle.
	resp, err := http.Get(env.opURL("manual-node-bundle") + "?node=node-mike")
	if err != nil {
		t.Fatalf("unauth request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("unauthenticated manual-node-bundle download returned 200; it must be operator-gated")
	}
}
