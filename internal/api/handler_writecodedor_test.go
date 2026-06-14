package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
)

// TestWriteCodedOr pins the relay semantics that the plan-3.5b-5 review flagged at HandleStage:
// a source-coded error (e.g. wrapped through CompileAndStage) surfaces at its OWN status, while an
// un-coded error falls back to the caller's bucket code. This is why /stage no longer flattens every
// failure to 422: a keygen 400 or an export 500 now surfaces precisely.
func TestWriteCodedOr(t *testing.T) {
	decode := func(rec *httptest.ResponseRecorder) (string, int) {
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
			t.Fatalf("decode envelope: %v; body=%s", err, rec.Body.String())
		}
		return env.Error.Code, rec.Code
	}

	t.Run("surfaces a wrapped source code at its native status", func(t *testing.T) {
		// CodeKeygenMissingPubkey is a 400 (not the 422 fallback), wrapped behind a plain prefix
		// exactly like CompileAndStage does with %w.
		src := apierr.New(apierr.CodeKeygenMissingPubkey).With("node", "edge-1")
		wrapped := fmt.Errorf("stage: %w", src)
		rec := httptest.NewRecorder()
		writeCodedOr(rec, apierr.CodeStageFailed, wrapped)
		code, status := decode(rec)
		if status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (source code wins, not the 422 fallback)", status)
		}
		if code != "keygen_missing_pubkey" {
			t.Errorf("code = %q, want keygen_missing_pubkey", code)
		}
	})

	t.Run("falls back to the bucket code for an un-coded error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeCodedOr(rec, apierr.CodeStageFailed, errors.New("disk gone"))
		code, status := decode(rec)
		if status != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422 (CodeStageFailed fallback)", status)
		}
		if code != "stage_failed" {
			t.Errorf("code = %q, want stage_failed", code)
		}
	})
}
