package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// withTestIdentity pins the tenant+operator identity onto r's context the SAME way
// operatorAuth/requireNode do (ctxKeyTenant + ctxKeyNode), so a handler routed through
// op/opRaw sees a valid identity() without the full auth middleware.
func withTestIdentity(r *http.Request, tenant controller.TenantID, node string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxKeyTenant, tenant)
	ctx = context.WithValue(ctx, ctxKeyNode, node)
	return r.WithContext(ctx)
}

func decodeErrEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (string, map[string]string) {
	t.Helper()
	var env struct {
		Error struct {
			Code   string            `json:"code"`
			Params map[string]string `json:"params"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, rec.Body.String())
	}
	return env.Error.Code, env.Error.Params
}

func newAdapterTestHandler() *ControllerHandler {
	return NewControllerHandler(controller.NewMemStore(), testTenant, controller.HashToken(testOperatorToken), DefaultOperatorName, "dev")
}

// TestOpAdapter_EnforcesIdentity is the perpetual guard on invariant 4 (backend sole authority
// for identity): a handler routed through op() CANNOT run its body unless identity() succeeded.
// A request that reaches the adapter WITHOUT an identity pinned (i.e. never passed operatorAuth)
// is rejected with internal_identity_missing (500) and the fn is NEVER invoked — the identity
// check is structural, not a per-handler convention a body could forget.
func TestOpAdapter_EnforcesIdentity(t *testing.T) {
	h := newAdapterTestHandler()
	called := false
	handler := h.op(http.MethodPost, func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
		called = true
		return map[string]string{"ok": "yes"}, nil
	})

	rec := httptest.NewRecorder()
	// No identity on the context (simulates a route that skipped operatorAuth).
	handler(rec, httptest.NewRequest(http.MethodPost, "/x", nil))

	if called {
		t.Fatal("fn was invoked despite a missing identity — the adapter did NOT enforce identity()")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if code, _ := decodeErrEnvelope(t, rec); code != string(apierr.CodeInternalIdentityMissing) {
		t.Errorf("code = %q, want internal_identity_missing", code)
	}
}

// TestOpAdapter_MethodGuard: a disallowed method is a 405 whose {method} param echoes the exact
// allow-list string, and the fn is not called — byte-identical to the hand-rolled guards.
func TestOpAdapter_MethodGuard(t *testing.T) {
	h := newAdapterTestHandler()
	called := false
	handler := h.op(http.MethodPost, func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
		called = true
		return nil, nil
	})
	rec := httptest.NewRecorder()
	// GET on a POST-only handler; identity IS present so only the method guard can reject.
	handler(rec, withTestIdentity(httptest.NewRequest(http.MethodGet, "/x", nil), testTenant, "op"))
	if called {
		t.Fatal("fn was invoked despite a disallowed method")
	}
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	code, params := decodeErrEnvelope(t, rec)
	if code != string(apierr.CodeMethodNotAllowed) {
		t.Errorf("code = %q, want method_not_allowed", code)
	}
	if params["method"] != http.MethodPost {
		t.Errorf("params[method] = %q, want POST", params["method"])
	}
}

// TestOpAdapter_SuccessAndError: with a valid method + identity, the adapter passes the pinned
// tenant/actor to fn and writes its result as 200 JSON; a coded error from fn is written at that
// error's own status.
func TestOpAdapter_SuccessAndError(t *testing.T) {
	h := newAdapterTestHandler()

	t.Run("success writes 200 JSON with the pinned identity", func(t *testing.T) {
		handler := h.op(http.MethodGet, func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
			return map[string]string{"tenant": string(tenant), "actor": actor}, nil
		})
		rec := httptest.NewRecorder()
		handler(rec, withTestIdentity(httptest.NewRequest(http.MethodGet, "/x", nil), testTenant, "op-alice"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["tenant"] != string(testTenant) || body["actor"] != "op-alice" {
			t.Errorf("fn saw tenant=%q actor=%q, want %q/op-alice", body["tenant"], body["actor"], testTenant)
		}
	})

	t.Run("a coded error is written at its own status", func(t *testing.T) {
		handler := h.op(http.MethodGet, func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
			return nil, apierr.New(apierr.CodeNodeNotFound)
		})
		rec := httptest.NewRecorder()
		handler(rec, withTestIdentity(httptest.NewRequest(http.MethodGet, "/x", nil), testTenant, "op"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
		if code, _ := decodeErrEnvelope(t, rec); code != string(apierr.CodeNodeNotFound) {
			t.Errorf("code = %q, want node_not_found", code)
		}
	})
}

// TestOpAdapter_MultiMethod: methodAllowed accepts every verb in a comma-space list, and the 405
// for a verb NOT in the list echoes the whole list (matches the multi-method guards the
// hand-rolled dispatchers used).
func TestOpAdapter_MultiMethod(t *testing.T) {
	h := newAdapterTestHandler()
	handler := h.op("GET, POST", func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
		return map[string]string{"m": r.Method}, nil
	})
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		handler(rec, withTestIdentity(httptest.NewRequest(m, "/x", nil), testTenant, "op"))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", m, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler(rec, withTestIdentity(httptest.NewRequest(http.MethodDelete, "/x", nil), testTenant, "op"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: status = %d, want 405", rec.Code)
	}
	if _, params := decodeErrEnvelope(t, rec); params["method"] != "GET, POST" {
		t.Errorf("params[method] = %q, want \"GET, POST\"", params["method"])
	}
}

// TestOpRawAdapter_EnforcesIdentity: opRaw enforces the SAME structural identity() guard — a
// missing identity is a 500 and the fn (which would write the raw body) never runs.
func TestOpRawAdapter_EnforcesIdentity(t *testing.T) {
	h := newAdapterTestHandler()
	called := false
	handler := h.opRaw(http.MethodGet, func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) *apierr.Error {
		called = true
		_, _ = w.Write([]byte("raw"))
		return nil
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if called {
		t.Fatal("opRaw fn ran despite a missing identity")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if code, _ := decodeErrEnvelope(t, rec); code != string(apierr.CodeInternalIdentityMissing) {
		t.Errorf("code = %q, want internal_identity_missing", code)
	}
}

// TestOpRawAdapter_SuccessWritesOwnBody: with method+identity ok, opRaw runs the fn, which writes
// its own body/headers; the adapter adds nothing on success.
func TestOpRawAdapter_SuccessWritesOwnBody(t *testing.T) {
	h := newAdapterTestHandler()
	handler := h.opRaw(http.MethodGet, func(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) *apierr.Error {
		w.Header().Set("Content-Type", "application/zip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("PK\x03\x04"))
		return nil
	})
	rec := httptest.NewRecorder()
	handler(rec, withTestIdentity(httptest.NewRequest(http.MethodGet, "/x", nil), testTenant, "op"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("content-type = %q, want application/zip", ct)
	}
	if rec.Body.String() != "PK\x03\x04" {
		t.Errorf("body = %q, want the raw bytes fn wrote", rec.Body.String())
	}
}
