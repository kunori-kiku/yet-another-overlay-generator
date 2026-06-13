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
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// Handler HTTP API
type Handler struct {
	compiler *compiler.Compiler
}

// NewHandler  API
func NewHandler() *Handler {
	return &Handler{
		compiler: compiler.NewCompiler(),
	}
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

// HealthResponse
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// ValidateResponse
type ValidateResponse struct {
	Valid    bool                        `json:"valid"`
	Errors   []validator.ValidationError `json:"errors,omitempty"`
	Warnings []validator.ValidationError `json:"warnings,omitempty"`
}

// CompileResponse
type CompileResponse struct {
	Topology         *model.Topology   `json:"topology"`
	WireGuardConfigs map[string]string `json:"wireguard_configs"`
	BabelConfigs     map[string]string `json:"babel_configs"`
	SysctlConfigs    map[string]string `json:"sysctl_configs"`
	InstallScripts   map[string]string `json:"install_scripts"`
	DeployScripts    map[string]string `json:"deploy_scripts"`
	// 编译成功后仍需向用户展示的非致命告警（NAT 不可达、无 endpoint 的边、孤立节点等）。
	// 这些告警在编译期由语义校验产生，必须随成功响应返回，否则操作员会在绿色编译上
	// 部署一条注定不通的隧道（审计阻断项 UX-1）。
	Warnings []validator.ValidationError `json:"warnings,omitempty"`
	Manifest compiler.CompileManifest    `json:"manifest"`
}

// HandleHealth
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 GET 请求")
		return
	}

	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().Format(time.RFC3339),
	})
}

// HandleValidate
func (h *Handler) HandleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST 请求")
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
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

// HandleCompile
func (h *Handler) HandleCompile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST 请求")
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	keys, err := render.GenerateKeys(topo, render.AirGap)
	if err != nil {
		writeCodedOr(w, "failed to generate WireGuard keys", err)
		return
	}

	//
	result, err := h.compiler.Compile(topo, keys)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	//
	if err := render.All(result, keys); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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

// HandleExport
func (h *Handler) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST 请求")
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	keys, err := render.GenerateKeys(topo, render.AirGap)
	if err != nil {
		writeCodedOr(w, "failed to generate WireGuard keys", err)
		return
	}

	result, err := h.compiler.Compile(topo, keys)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if err := render.All(result, keys); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 创建临时目录用于写出导出产物
	tmpDir, err := os.MkdirTemp("", "overlay-export-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建临时目录失败")
		return
	}
	defer os.RemoveAll(tmpDir)

	if _, err := artifacts.Export(result, tmpDir); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("导出产物失败: %v", err))
		return
	}

	archiveBuf, err := createExportZip(tmpDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("打包 ZIP 失败: %v", err))
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
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST 请求")
		return
	}

	topo, err := readTopology(w, r)
	if err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 部署脚本的卸载分支需要每条 per-peer 隧道的接口名才能逐一拆除（wg-quick down / 删除
	// 配置），而接口名只有在完整编译后才存在于 PeerMap 中。因此本端点必须运行与 /api/compile
	// 相同的流水线（生成密钥 → 编译 → 渲染 Babel 配置），否则生成的卸载块缺失全部 per-peer
	// 拆除步骤（审计阻断项 D36）。
	keys, err := render.GenerateKeys(topo, render.AirGap)
	if err != nil {
		writeCodedOr(w, "failed to generate WireGuard keys", err)
		return
	}

	result, err := h.compiler.Compile(topo, keys)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// 部署脚本的 HasBabel 判定按 node.ID 查 BabelConfigs（见 renderer.RenderDeployScripts），
	// 因此必须先按编译路径渲染出 Babel 配置，再渲染部署脚本。
	babelConfigs, err := renderer.RenderAllBabelConfigs(result.Topology, result.PeerMap)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("渲染 Babel 配置失败: %v", err))
		return
	}
	result.BabelConfigs = babelConfigs

	bashScript, ps1Script, err := renderer.RenderDeployScripts(result.Topology, result.PeerMap, result.BabelConfigs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("deploy script render: %v", err))
		return
	}

	format := r.URL.Query().Get("format")
	var script, filename, contentType string
	if format == "ps1" {
		script = ps1Script
		filename = "deploy-all.ps1"
		contentType = "text/plain; charset=utf-8"
	} else {
		script = bashScript
		filename = "deploy-all.sh"
		contentType = "text/x-shellscript; charset=utf-8"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(script))
}

// ---  ---

// maxRequestBodyBytes 限制每个 POST 请求体的最大长度（4 MiB）。
// 超过该上限的请求体不会被缓冲到内存，而是被 http.MaxBytesReader 截断并报错，
// 由调用方映射为 413 Payload Too Large，防止无上限的 io.ReadAll 造成 OOM DoS（D34）。
const maxRequestBodyBytes int64 = 4 << 20 // 4 MiB

// errBodyTooLarge 标识请求体超出 maxRequestBodyBytes 的哨兵错误。
// 调用方据此返回 413（http.StatusRequestEntityTooLarge），其余读取/解析错误返回 400。
var errBodyTooLarge = fmt.Errorf("请求体超出大小上限（最大 %d 字节）", maxRequestBodyBytes)

// isBodyTooLarge 判断 readTopology 返回的错误是否为请求体过大。
func isBodyTooLarge(err error) bool {
	return errors.Is(err, errBodyTooLarge)
}

// readTopology 读取并解析请求体中的 Topology。
// 请求体被 http.MaxBytesReader 限制在 maxRequestBodyBytes 以内；
// 超限时返回 errBodyTooLarge（调用方映射为 413），其余错误为可读性/格式问题（映射为 400）。
func readTopology(w http.ResponseWriter, r *http.Request) (*model.Topology, error) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errBodyTooLarge
		}
		return nil, fmt.Errorf("读取请求体失败: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("请求体为空")
	}

	var topo model.Topology
	if err := json.Unmarshal(body, &topo); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
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

		// 安装器 ZIP 条目名必须使用与部署脚本相同的规范化文件名（naming.SafeInstallerFileName），
		// 而非原始目录名。两侧若使用不同的名称推导规则，凡是含大写、空格或特殊字符的节点
		// 都会被写成一个名字、被部署脚本按另一个名字查找，从而被静默跳过（审计阻断项 D3/D32）。
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

	// 自解包装脚本此前直接 base64 解码并以 root 执行 payload，对 payload 没有任何完整性锚定
	// （审计项 D25）。这里在 Go 侧对 tar.gz payload 计算 SHA-256，并作为字面量嵌入脚本；
	// 脚本在 base64 解码之后、tar 解包/执行之前，用 sha256sum -c 风格的比对来验证解码出的
	// 归档与该期望哈希一致，不一致则带中文错误中止。期望哈希对应的正是写入 ARCHIVE_PATH 的
	// 那份字节（即 decode(encoded) == payload），因此对 payload 求哈希即可。
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
	echo "错误：安装包签名校验失败（Ed25519，openssl 缺少 Ed25519 支持或签名无效），已中止。" >&2
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

%s# 完整性校验：在解包/执行之前，核对解码出的归档 SHA-256 与构建时嵌入的期望值。
# 不一致说明 payload 被篡改或损坏，必须以 root 身份执行前立即中止（审计项 D25）。
echo "${EXPECTED_PAYLOAD_SHA256}  ${ARCHIVE_PATH}" | sha256sum -c - >/dev/null 2>&1 || {
	echo "错误：安装包完整性校验失败（SHA-256 不匹配），已中止。payload 可能被篡改或在传输中损坏。" >&2
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

// writeError is the TRANSITIONAL bridge: it wraps a bare, not-yet-coded message under
// CodeLegacyUncoded so the ~200 existing call sites emit the nested envelope unchanged.
// It is removed in the final plan-3 commit, once every site calls writeAPIError with a
// real code (grep-gated). Do not use it in new code.
//
// Deprecated: use writeAPIError with an apierr.Code.
func writeError(w http.ResponseWriter, status int, message string) {
	writeAPIError(w, apierr.New(apierr.CodeLegacyUncoded).WithStatus(status).WithMessage(message))
}

// writeCodedOr surfaces err as its coded envelope (with the error's own status) when err
// is, or wraps, an *apierr.Error; otherwise it falls back to a legacy 500 with the given
// context message. Used where a handler relays a deep error (e.g. render.GenerateKeys)
// that is coded at the source — the code + status flow through to the panel instead of
// being flattened to a generic 500.
func writeCodedOr(w http.ResponseWriter, fallbackMsg string, err error) {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		writeAPIError(w, ae)
		return
	}
	writeError(w, http.StatusInternalServerError, fmt.Sprintf("%s: %v", fallbackMsg, err))
}
