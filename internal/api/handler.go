package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// Handler serves the air-gap HTTP API routes (health, validate, compile, export,
// deploy-script). The compile path (generate keys → compile → render) lives behind the
// localcompile façade, so the handler holds no per-request compile state of its own — it
// builds a localcompile.CompileRequest per request and lets the façade own the pipeline.
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

// ValidateResponse is the JSON body returned by /api/validate: overall validity plus the
// schema and semantic errors and warnings.
type ValidateResponse struct {
	Valid    bool                        `json:"valid"`
	Errors   []validator.ValidationError `json:"errors,omitempty"`
	Warnings []validator.ValidationError `json:"warnings,omitempty"`
}

// CompileResponse is the JSON body returned by /api/compile: the compiled topology, the
// rendered per-node configs and scripts, any non-fatal warnings, and the compile manifest.
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

// HandleValidate serves POST /api/validate, running schema and semantic validation on the
// posted topology and returning the combined errors and warnings.
func (h *Handler) HandleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		// readTopology returns a coded *apierr.Error (CodeReqBodyTooLarge 413 / CodeReqBodyEmpty 400 /
		// CodeReqInvalidBody 400); writeCodedOr surfaces it with its own status via errors.As.
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	// Schema
	schemaResult := validator.ValidateSchema(topo)
	//
	semanticResult := validator.ValidateSemantic(topo)

	//
	allErrors := append(schemaResult.Errors, semanticResult.Errors...)
	allWarnings := append(schemaResult.Warnings, semanticResult.Warnings...)

	writeJSON(w, http.StatusOK, ValidateResponse{
		Valid:    len(allErrors) == 0,
		Errors:   allErrors,
		Warnings: allWarnings,
	})
}

// HandleCompile serves POST /api/compile, running the full pipeline (generate keys ->
// compile -> render) on the posted topology and returning the compiled configs, scripts,
// warnings, and manifest.
func (h *Handler) HandleCompile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		// readTopology returns a coded *apierr.Error (CodeReqBodyTooLarge 413 / CodeReqBodyEmpty 400 /
		// CodeReqInvalidBody 400); writeCodedOr surfaces it with its own status via errors.As.
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	req, err := h.airGapRequest(topo)
	if err != nil {
		// airGapRequest returns coded errors for the env-resolved fetch catalog / signing key;
		// writeCodedOr surfaces each at its own status via errors.As (CodeRenderFailed fallback).
		writeCodedOr(w, apierr.CodeRenderFailed, err)
		return
	}

	// Run the whole air-gap pipeline (generate keys → compile → render) through the shared
	// façade, the single compile authority. CompileResult yields the raw result so the
	// response keeps its per-map shape.
	result, err := localcompile.CompileResultCtx(r.Context(), req)
	if err != nil {
		writeCodedOr(w, apierr.CodeCompileFailed, err)
		return
	}

	writeJSON(w, http.StatusOK, CompileResponse{
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

// HandleExport serves POST /api/export, compiling and rendering the posted topology and
// returning a ZIP archive of per-node self-extracting installer bundles.
func (h *Handler) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		// readTopology returns a coded *apierr.Error (CodeReqBodyTooLarge 413 / CodeReqBodyEmpty 400 /
		// CodeReqInvalidBody 400); writeCodedOr surfaces it with its own status via errors.As.
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	req, err := h.airGapRequest(topo)
	if err != nil {
		// airGapRequest returns coded errors for the env-resolved fetch catalog / signing key;
		// writeCodedOr surfaces each at its own status via errors.As (CodeRenderFailed fallback).
		writeCodedOr(w, apierr.CodeRenderFailed, err)
		return
	}

	// Compile + render through the shared façade (the single compile authority), then hand
	// the resulting *compiler.CompileResult to the unchanged artifacts.Export → tmpDir →
	// createExportZip(tmpDir) flow below — the export path keeps its dir-based shape.
	result, err := localcompile.CompileResultCtx(r.Context(), req)
	if err != nil {
		writeCodedOr(w, apierr.CodeCompileFailed, err)
		return
	}

	// Create a temporary directory to write the export artifacts into.
	tmpDir, err := os.MkdirTemp("", "overlay-export-*")
	if err != nil {
		writeCodedOr(w, apierr.CodeExportIOFailed, err)
		return
	}
	defer os.RemoveAll(tmpDir)

	if _, err := artifacts.Export(result, tmpDir); err != nil {
		writeCodedOr(w, apierr.CodeExportIOFailed, err)
		return
	}

	archiveBuf, err := createExportZip(tmpDir)
	if err != nil {
		writeCodedOr(w, apierr.CodeExportIOFailed, err)
		return
	}

	filename := fmt.Sprintf("%s-artifacts.zip", topo.Project.ID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	w.Write(archiveBuf.Bytes())
}

// HandleDeployScript returns the deploy script (bash or PowerShell) as a downloadable file.
// Query parameter ?format=ps1 returns PowerShell; default is bash.
func (h *Handler) HandleDeployScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		// readTopology returns a coded *apierr.Error (CodeReqBodyTooLarge 413 / CodeReqBodyEmpty 400 /
		// CodeReqInvalidBody 400); writeCodedOr surfaces it with its own status via errors.As.
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	// The deploy script's uninstall branch needs the interface name of every per-peer tunnel
	// to tear them down one by one (wg-quick down / delete config), and those interface names
	// only exist in the PeerMap after a full compile. This endpoint must therefore run the same
	// pipeline as /api/compile; otherwise the generated uninstall block is missing all per-peer
	// teardown steps (audit blocker D36). Routing through the façade — the single compile
	// authority — renders the deploy scripts into result.DeployScripts (render.All calls the
	// same renderer.RenderDeployScripts internally), so the two endpoints can never drift.
	req, err := h.airGapRequest(topo)
	if err != nil {
		// airGapRequest returns coded errors for the env-resolved fetch catalog / signing key;
		// writeCodedOr surfaces each at its own status via errors.As (CodeRenderFailed fallback).
		writeCodedOr(w, apierr.CodeRenderFailed, err)
		return
	}

	result, err := localcompile.CompileResultCtx(r.Context(), req)
	if err != nil {
		writeCodedOr(w, apierr.CodeCompileFailed, err)
		return
	}

	format := r.URL.Query().Get("format")
	var script, filename, contentType string
	if format == "ps1" {
		script = result.DeployScripts["deploy-all.ps1"]
		filename = "deploy-all.ps1"
		contentType = "text/plain; charset=utf-8"
	} else {
		script = result.DeployScripts["deploy-all.sh"]
		filename = "deploy-all.sh"
		contentType = "text/x-shellscript; charset=utf-8"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
}

// --- helpers ---

// airGapRequest builds the localcompile.CompileRequest the three air-gap compute routes
// (/api/compile, /api/export, /api/deploy-script) share. It resolves the two environment-
// coupled inputs the façade no longer reads itself — the mimic/agent fetch catalog and the
// optional tier-1 bundle signing key — into explicit request fields, then stamps Custody
// AirGap and time.Now() as the compile clock.
//
//   - Fetch: FetchSettingsFromEnv (plan-7). Unset ⇒ the zero FetchSettings ⇒ a byte-
//     identical bundle (D4); a misconfigured catalog fails the request loud, not silently.
//   - SigningKey: LoadConfigSignerFromEnv. Unset ⇒ a nil signer ⇒ hash-only bundles
//     (byte-identical); a malformed key fails closed here.
//
// Both resolvers return coded *apierr.Error values so writeCodedOr surfaces each at its own
// status via errors.As. The façade compiles under context.Background() (the local compile
// path carries no HTTP deadline); the allocator's per-node scan budget remains the DoS
// bound for an over-large CIDR.
func (h *Handler) airGapRequest(topo *model.Topology) (localcompile.CompileRequest, error) {
	fetch, err := render.FetchSettingsFromEnv()
	if err != nil {
		return localcompile.CompileRequest{}, err
	}
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return localcompile.CompileRequest{}, err
	}
	return localcompile.CompileRequest{
		Topology:   *topo,
		Custody:    render.AirGap,
		Fetch:      fetch,
		SigningKey: signer,
		CompiledAt: time.Now(),
	}, nil
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

func createExportZip(dir string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	// Resolve the optional ConfigSigner once for the whole archive. nil means signing is off
	// (no YAOG_BUNDLE_SIGNING_KEY) and every wrapper stays byte-identical to today (opt-in).
	// The shared seam keeps the env-var name + PEM handling identical to the export path and the
	// install-script renderer, and lets a future KMS/HSM backend swap in without touching this loop.
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		nodeName := entry.Name()
		nodeDir := filepath.Join(dir, nodeName)

		tgz, err := tarGzDirectory(nodeDir)
		if err != nil {
			return nil, err
		}

		installer, err := makeSelfExtractingInstaller(nodeName, tgz.Bytes(), signer)
		if err != nil {
			return nil, err
		}

		// The installer ZIP entry name must use the same canonicalized filename as the deploy
		// script (naming.SafeInstallerFileName), not the raw directory name. If the two sides used
		// different name-derivation rules, any node containing uppercase letters, spaces, or special
		// characters would be written under one name and looked up by the deploy script under
		// another, and thus silently skipped (audit blocker D3/D32).
		installHeader := &zip.FileHeader{Name: naming.SafeInstallerFileName(nodeName), Method: zip.Deflate}
		installHeader.SetMode(0755)
		installWriter, err := zw.CreateHeader(installHeader)
		if err != nil {
			return nil, err
		}
		if _, err := installWriter.Write(installer); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf, nil
}

func tarGzDirectory(dir string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	gzw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gzw)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tw, file)
		return err
	})
	if err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}

	if err := gzw.Close(); err != nil {
		return nil, err
	}

	return buf, nil
}

// makeSelfExtractingInstaller builds the self-extracting installer wrapper for one node's tar.gz
// payload. When signer is non-nil, the wrapper additionally carries a base64 Ed25519 signature
// over the payload bytes plus the pinned verifying public key, and verifies the signature (openssl)
// BEFORE the existing SHA-256 integrity check — with the same fail-clear discipline (a present
// signature that cannot be verified aborts; openssl/Ed25519 missing aborts). When signer is nil
// the emitted wrapper is byte-identical to the pre-signing output. signer is the shared
// bundlesig.ConfigSigner seam (today an in-process Ed25519 key from YAOG_BUNDLE_SIGNING_KEY; a
// future KMS/HSM backend swaps in transparently). See docs/spec/controller/signing.md.
func makeSelfExtractingInstaller(nodeName string, payload []byte, signer bundlesig.ConfigSigner) ([]byte, error) {
	encoded := base64.StdEncoding.EncodeToString(payload)

	// The self-extracting wrapper script previously base64-decoded and executed the payload as
	// root directly, with no integrity anchoring of the payload at all (audit item D25). Here we
	// compute the SHA-256 of the tar.gz payload on the Go side and embed it as a literal in the
	// script; after base64-decoding and before unpacking/executing, the script uses a sha256sum -c
	// style comparison to verify the decoded archive matches this expected hash, aborting with an
	// error on mismatch. The expected hash corresponds exactly to the bytes written to
	// ARCHIVE_PATH (i.e. decode(encoded) == payload), so hashing the payload is sufficient.
	expectedPayloadSHA256 := fmt.Sprintf("%x", sha256.Sum256(payload))

	// Build the optional signature-verification block. When signing is off it is empty, so the
	// wrapper renders byte-identical to today (opt-in). When on, we sign the SAME payload bytes
	// whose SHA-256 is pinned above, base64-encode the raw signature, and emit a block that runs
	// BEFORE the SHA-256 check.
	sigBlock := ""
	if signer != nil {
		sig, err := signer.Sign(payload)
		if err != nil {
			return nil, fmt.Errorf("sign installer payload: %w", err)
		}
		sigB64 := base64.StdEncoding.EncodeToString(sig)
		// Carry both the signature and the PEM as base64 to avoid any shell quoting/newline issues:
		// %q would Go-escape the PEM's newlines as literal backslash-n, which bash double quotes do
		// NOT re-interpret, corrupting the key. base64 round-trips the exact bytes safely.
		pubkeyB64 := base64.StdEncoding.EncodeToString(signer.PublicKeyPEM())
		// All shell vars quoted; pinned pubkey written to a temp file for openssl pkeyutl -pubin.
		// openssl missing, or lacking Ed25519 support, exits nonzero and aborts (never silently skip).
		sigBlock = fmt.Sprintf(`PAYLOAD_SIG_B64=%q
SIGNING_PUBKEY_PEM_B64=%q

# Verify the payload's Ed25519 signature BEFORE the SHA-256 check (docs/spec/controller/signing.md).
# Signs the raw tar.gz payload bytes against the public key pinned at generation time, so a tampered
# payload is rejected before any root action.
if ! command -v openssl >/dev/null 2>&1; then
	echo "ERROR: installer is signed but openssl is not installed; cannot verify signature" >&2
	exit 1
fi
SIG_PUBKEY_FILE="$(mktemp)"
SIG_RAW_FILE="$(mktemp)"
cleanup_sig() {
	rm -f "${SIG_PUBKEY_FILE}" "${SIG_RAW_FILE}"
}
trap 'cleanup_sig; cleanup' EXIT
printf '%%s' "${SIGNING_PUBKEY_PEM_B64}" | base64 -d > "${SIG_PUBKEY_FILE}" 2>/dev/null || {
	echo "ERROR: failed to decode embedded signing public key" >&2
	exit 1
}
printf '%%s' "${PAYLOAD_SIG_B64}" | base64 -d > "${SIG_RAW_FILE}" 2>/dev/null || {
	echo "ERROR: failed to decode embedded payload signature" >&2
	exit 1
}
# Ed25519 is a one-shot (raw) signature: -rawin feeds the message directly, no pre-hash.
if ! openssl pkeyutl -verify -pubin -inkey "${SIG_PUBKEY_FILE}" -rawin -sigfile "${SIG_RAW_FILE}" -in "${ARCHIVE_PATH}" >/dev/null 2>&1; then
	echo "ERROR: installer signature verification failed (Ed25519 - openssl lacks Ed25519 support or the signature is invalid); aborting." >&2
	exit 1
fi
cleanup_sig
trap cleanup EXIT

`, sigB64, pubkeyB64)
	}

	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

NODE_NAME=%q
EXPECTED_PAYLOAD_SHA256=%q
WORKDIR="$(mktemp -d -t "${NODE_NAME}-install-XXXXXX")"
ARCHIVE_PATH="${WORKDIR}/${NODE_NAME}.tar.gz"

cleanup() {
	rm -rf "${WORKDIR}"
}
trap cleanup EXIT

PAYLOAD_LINE="$(awk '/^__PAYLOAD_BELOW__$/ {print NR + 1; exit 0; }' "$0")"
if [[ -z "${PAYLOAD_LINE}" ]]; then
	echo "ERROR: installer payload marker not found" >&2
	exit 1
fi

tail -n +"${PAYLOAD_LINE}" "$0" | base64 -d > "${ARCHIVE_PATH}"

%s# Integrity check: before unpacking/executing, verify the decoded archive's SHA-256 against the value embedded at build time.
# A mismatch means the payload was tampered with or corrupted; abort immediately, before running as root (audit item D25).
echo "${EXPECTED_PAYLOAD_SHA256}  ${ARCHIVE_PATH}" | sha256sum -c - >/dev/null 2>&1 || {
	echo "ERROR: installer integrity check failed (SHA-256 mismatch); aborting. The payload may have been tampered with or corrupted in transit." >&2
	exit 1
}

tar -xzf "${ARCHIVE_PATH}" -C "${WORKDIR}"

if [[ ! -f "${WORKDIR}/install.sh" ]]; then
	echo "ERROR: install.sh not found in extracted payload" >&2
	exit 1
fi

echo "Running node installer for ${NODE_NAME}..."
if [[ "$(id -u)" -eq 0 ]]; then
	bash "${WORKDIR}/install.sh" "$@"
elif command -v sudo >/dev/null 2>&1; then
	sudo bash "${WORKDIR}/install.sh" "$@"
else
	echo "ERROR: root privileges required (run as root or install sudo)" >&2
	exit 1
fi

exit 0
__PAYLOAD_BELOW__
%s
`, nodeName, expectedPayloadSHA256, sigBlock, encoded)

	return []byte(script), nil
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
	var ae *apierr.Error
	if errors.As(err, &ae) {
		writeAPIError(w, ae)
		return
	}
	writeAPIError(w, apierr.New(fallback).Wrap(err))
}
