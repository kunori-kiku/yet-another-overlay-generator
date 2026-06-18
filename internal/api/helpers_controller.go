package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// identity pulls the tenant+node the auth middleware pinned onto the context. The
// boolean is false if either is missing (a handler reached without the middleware,
// which is an internal error).
func identity(ctx context.Context) (controller.TenantID, string, bool) {
	tenant, okT := tenantFromCtx(ctx)
	node, okN := nodeFromCtx(ctx)
	if !okT || !okN {
		return "", "", false
	}
	return tenant, node, true
}

// decodeJSON reads a size-capped JSON body into v. It rejects unknown fields so a
// typo'd key is a 400 rather than a silently-ignored field.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, controllerMaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// readControllerBody reads a size-capped raw body (for endpoints that store the
// bytes verbatim, e.g. update-topology). It returns errBodyTooLarge on overflow so
// the caller can map it to 413.
func readControllerBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, controllerMaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	if len(body) == 0 {
		return nil, errBodyEmpty
	}
	return body, nil
}

// parseAfter parses the /poll ?after= cursor. An empty value means 0 (poll for any
// generation). A non-numeric, negative, or out-of-range value is a 400 — strconv
// rejects overflow, so a huge all-digit value cannot silently wrap to a negative
// generation (which would make WaitForGeneration return immediately).
func parseAfter(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("after must be a non-negative integer")
	}
	return n, nil
}
