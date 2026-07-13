//go:build airgap

package api

// handler_airgap.go — the -tags airgap build's anonymous compute handlers + ZIP helpers.
//
// plan-7 / 1.7 (LOCKED build-tag mechanism, NOT a delete): the four anonymous air-gap compute
// handlers (HandleValidate/HandleCompile/HandleExport/HandleDeployScript), the airGapRequest
// helper, the three handler-local ZIP helpers (createExportZip/tarGzDirectory/
// makeSelfExtractingInstaller), and ValidateResponse live HERE, behind //go:build airgap, so the
// DEFAULT (controller-only) build neither registers nor links them — no unauthenticated path
// reaches the keygen/allocator/compiler pipeline in the shipped controller. The -tags airgap build
// RETAINS them unchanged as the local-design oracle + the boot target for plan-13's --mode airgap
// E2E and plan-21's -tags airgap DAST.
//
// The un-tagged handler.go keeps what BOTH builds need: HandleHealth, readTopology +
// maxRequestBodyBytes + errBodyTooLarge (also used by the operator-only HandleCompilePreview),
// the writeJSON/writeAPIError/writeCodedOr helpers, and the response structs read by controller
// handlers (apiError/errorBody/HealthResponse/CompileResponse).

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// ValidateResponse is the JSON body returned by /api/validate: overall validity plus the
// schema and semantic errors and warnings. Air-gap-only (its sole producer is HandleValidate);
// the controller compile-preview path does not surface a validate-only response.
type ValidateResponse struct {
	Valid    bool                        `json:"valid"`
	Errors   []validator.ValidationError `json:"errors,omitempty"`
	Warnings []validator.ValidationError `json:"warnings,omitempty"`
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

// --- air-gap helpers ---

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
	// Pre-heal any cross-link pin collision BEFORE compiling — mirroring the controller's
	// CompileAndStage pre-heal (internal/controller/compile.go) — so the anonymous air-gap routes
	// (/api/compile, /api/export, /api/deploy-script) converge on the SAME healed compile the
	// controller produces, closing the divergence where the air-gap path skipped heal (a
	// colliding-pin design compiled differently on the anonymous vs controller path). Heal is
	// applied at the KNOWN entry points (controller stage + update-topology save + canvas load +
	// here); localcompile's semantic validator DELIBERATELY stays the LOUD safety net for any
	// un-healed corruption that reaches the compiler by another path (the C2 design —
	// docs/spec/rc1/3.4-findings.md; internal/edgecase/c2_reenable_test.go). The air-gap topology
	// is parsed fresh from the request body, so healing it in place is confined to this request.
	normalize.HealCollidingPins(topo)

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
