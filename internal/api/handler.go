package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// Handler serves the controller liveness probe (GET /api/health). framework-refactor plan-9
// deleted the four anonymous air-gap compute routes (validate/compile/export/deploy-script), so
// this is the only route Handler serves; the operator-gated compile path is on ControllerHandler.
// Handler holds no per-request state (the compile pipeline lives behind the localcompile façade).
type Handler struct{}

// NewHandler constructs a Handler. It is stateless: each request builds its own
// localcompile.CompileRequest, so there is nothing to wire up here.
func NewHandler() *Handler {
	return &Handler{}
}

// apiError is the wire envelope for every error response: a single nested object
// carrying a stable machine code, the server-rendered English message (for CLI/curl
// and as the i18n English fallback), and string params the panel interpolates into the
// localized template. See internal/apierr.
type apiError struct {
	Error errorBody `json:"error"`
}

// errorBody is the nested error payload.
type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Params  map[string]string `json:"params,omitempty"`
}

// HealthResponse is the JSON body returned by the /api/health endpoint.
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// CompileResponse is the JSON body carrying a full compile result: the compiled topology, the
// rendered per-node configs and scripts, any non-fatal warnings, and the compile manifest. It is
// reused by the controller compile-preview wire shape (see compilePreviewResponseJSON).
type CompileResponse struct {
	Topology         *model.Topology   `json:"topology"`
	WireGuardConfigs map[string]string `json:"wireguard_configs"`
	BabelConfigs     map[string]string `json:"babel_configs"`
	SysctlConfigs    map[string]string `json:"sysctl_configs"`
	InstallScripts   map[string]string `json:"install_scripts"`
	DeployScripts    map[string]string `json:"deploy_scripts"`
	// Non-fatal warnings that must still be surfaced to the user after a successful compile
	// (unreachable NAT, edges with no endpoint, isolated nodes, etc.). These warnings are
	// produced by semantic validation during compilation and must be returned with the success
	// response; otherwise an operator would deploy a doomed tunnel on top of a green compile
	// (audit blocker UX-1).
	Warnings []validator.ValidationError `json:"warnings,omitempty"`
	Manifest compiler.CompileManifest    `json:"manifest"`
}

// HandleHealth serves GET /api/health, returning an "ok" status with the current timestamp.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}

	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().Format(time.RFC3339),
	})
}

// maxRequestBodyBytes caps the maximum length of each POST request body (4 MiB). A body that
// exceeds this limit is not buffered into memory; instead http.MaxBytesReader truncates it and
// returns an error, which the caller maps to 413 Payload Too Large, preventing an unbounded
// io.ReadAll from causing an OOM DoS (D34).
const maxRequestBodyBytes int64 = 4 << 20 // 4 MiB

// errBodyTooLarge is the body-too-large sentinel returned by readTopology and the controller's
// raw-body reader on overflow. It is a coded *apierr.Error (CodeReqBodyTooLarge, 413) so writeCodedOr
// surfaces it via errors.As with the right status and the nested envelope. It is constructed once at
// init and only ever read afterwards (never mutated), so sharing the pointer across requests is safe.
var errBodyTooLarge = apierr.New(apierr.CodeReqBodyTooLarge).With("limit", strconv.FormatInt(maxRequestBodyBytes, 10))

// readTopology reads and parses the request body into a Topology. The body is capped at
// maxRequestBodyBytes by http.MaxBytesReader; overflow returns errBodyTooLarge (413), other read/
// parse failures return CodeReqInvalidBody / CodeReqBodyEmpty (400).
func readTopology(w http.ResponseWriter, r *http.Request) (*model.Topology, error) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errBodyTooLarge
		}
		return nil, apierr.New(apierr.CodeReqInvalidBody).Wrap(fmt.Errorf("read request body: %w", err))
	}

	if len(body) == 0 {
		return nil, apierr.New(apierr.CodeReqBodyEmpty)
	}

	var topo model.Topology
	if err := json.Unmarshal(body, &topo); err != nil {
		return nil, apierr.New(apierr.CodeReqInvalidBody).Wrap(fmt.Errorf("parse JSON: %w", err))
	}

	return &topo, nil
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeAPIError serializes a coded error as the nested envelope, using the error's own
// HTTP status. This is the single error-response path; new code calls it with a real
// apierr code.
func writeAPIError(w http.ResponseWriter, e *apierr.Error) {
	writeJSON(w, e.Status(), apiError{Error: errorBody{
		Code:    string(e.Code()),
		Message: e.Message(),
		Params:  e.Params(),
	}})
}

// writeCodedOr surfaces err as its coded envelope (with the error's own status) when err
// is, or wraps, an *apierr.Error; otherwise it emits the given fallback bucket code, wrapping
// err as the (log-only, never-serialized) cause. Used where a handler relays a deep error: a
// source-coded failure (e.g. render.GenerateKeys) flows through with its own code+status+params,
// while an un-coded one is bucketed under `fallback` so it still emits the nested envelope —
// never the legacy shim. A relay seam should pass the most precise bucket that fits
// (e.g. apierr.CodeRenderFailed); apierr.CodeInternal is the generic safety net.
func writeCodedOr(w http.ResponseWriter, fallback apierr.Code, err error) {
	writeAPIError(w, codedErr(fallback, err))
}
