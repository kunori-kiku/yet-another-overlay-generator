package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/renderer"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
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

// apiError
type apiError struct {
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
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
	Topology         *model.Topology          `json:"topology"`
	WireGuardConfigs map[string]string        `json:"wireguard_configs"`
	BabelConfigs     map[string]string        `json:"babel_configs"`
	SysctlConfigs    map[string]string        `json:"sysctl_configs"`
	InstallScripts   map[string]string        `json:"install_scripts"`
	DeployScripts    map[string]string        `json:"deploy_scripts"`
	Manifest         compiler.CompileManifest `json:"manifest"`
}

// HandleHealth
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, " GET ")
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
		writeError(w, http.StatusMethodNotAllowed, " POST ")
		return
	}

	topo, err := readTopology(r)
	if err != nil {
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
		writeError(w, http.StatusMethodNotAllowed, " POST ")
		return
	}

	topo, err := readTopology(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	keys, err := generateKeys(topo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf(" WireGuard : %v", err))
		return
	}

	//
	result, err := h.compiler.Compile(topo, keys)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	//
	if err := renderAll(result, keys); err != nil {
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
		Manifest:         result.Manifest,
	})
}

// HandleExport
func (h *Handler) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, " POST ")
		return
	}

	topo, err := readTopology(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	keys, err := generateKeys(topo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf(" WireGuard : %v", err))
		return
	}

	result, err := h.compiler.Compile(topo, keys)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if err := renderAll(result, keys); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	//
	tmpDir, err := os.MkdirTemp("", "overlay-export-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "")
		return
	}
	defer os.RemoveAll(tmpDir)

	if _, err := artifacts.Export(result, tmpDir); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf(": %v", err))
		return
	}

	archiveBuf, err := createExportZip(tmpDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf(": %v", err))
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
		writeError(w, http.StatusMethodNotAllowed, " POST ")
		return
	}

	topo, err := readTopology(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	bashScript, ps1Script, err := renderer.RenderDeployScripts(topo)
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

func readTopology(r *http.Request) (*model.Topology, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf(": %w", err)
	}
	defer r.Body.Close()

	if len(body) == 0 {
		return nil, fmt.Errorf("")
	}

	var topo model.Topology
	if err := json.Unmarshal(body, &topo); err != nil {
		return nil, fmt.Errorf("JSON : %w", err)
	}

	return &topo, nil
}

func generateKeys(topo *model.Topology) (map[string]compiler.KeyPair, error) {
	keys := make(map[string]compiler.KeyPair)
	for i := range topo.Nodes {
		node := &topo.Nodes[i]

		if node.FixedPrivateKey {
			if node.WireGuardPrivateKey != "" {
				privateKey, err := wgtypes.ParseKey(node.WireGuardPrivateKey)
				if err != nil {
					return nil, fmt.Errorf(" %s : %w", node.ID, err)
				}

				node.WireGuardPrivateKey = privateKey.String()
				node.WireGuardPublicKey = privateKey.PublicKey().String()
				keys[node.ID] = compiler.KeyPair{
					PrivateKey: node.WireGuardPrivateKey,
					PublicKey:  node.WireGuardPublicKey,
				}
				continue
			}

			privateKey, err := wgtypes.GeneratePrivateKey()
			if err != nil {
				return nil, fmt.Errorf(" %s : %w", node.ID, err)
			}

			node.WireGuardPrivateKey = privateKey.String()
			node.WireGuardPublicKey = privateKey.PublicKey().String()
			keys[node.ID] = compiler.KeyPair{
				PrivateKey: node.WireGuardPrivateKey,
				PublicKey:  node.WireGuardPublicKey,
			}
			continue
		}

		privateKey, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf(" %s : %w", node.ID, err)
		}

		// ：，
		node.WireGuardPrivateKey = ""
		node.WireGuardPublicKey = ""

		keys[node.ID] = compiler.KeyPair{
			PrivateKey: privateKey.String(),
			PublicKey:  privateKey.PublicKey().String(),
		}
	}
	return keys, nil
}

func renderAll(result *compiler.CompileResult, keys map[string]compiler.KeyPair) error {
	// WireGuard (per-peer configs for non-client nodes)
	wgConfigs, err := renderer.RenderAllWireGuardConfigs(result.Topology, result.PeerMap, keys)
	if err != nil {
		return fmt.Errorf(" WireGuard : %w", err)
	}
	result.WireGuardConfigs = wgConfigs

	// WireGuard client configs (single wg0 for client nodes)
	for nodeID, clientInfo := range result.ClientConfigs {
		config, err := renderer.RenderClientWireGuardConfig(clientInfo)
		if err != nil {
			return fmt.Errorf(" client %s WireGuard : %w", clientInfo.NodeName, err)
		}
		result.WireGuardConfigs[nodeID+":wg0"] = config
	}

	// Babel
	babelConfigs, err := renderer.RenderAllBabelConfigs(result.Topology, result.PeerMap)
	if err != nil {
		return fmt.Errorf(" Babel : %w", err)
	}
	result.BabelConfigs = babelConfigs

	// Sysctl
	sysctlConfigs, err := renderer.RenderAllSysctlConfigs(result.Topology)
	if err != nil {
		return fmt.Errorf(" sysctl : %w", err)
	}
	result.SysctlConfigs = sysctlConfigs

	//
	for _, node := range result.Topology.Nodes {
		if node.Role == "client" {
			script, err := renderer.RenderClientInstallScript(&node)
			if err != nil {
				return fmt.Errorf(" client %s : %w", node.Name, err)
			}
			result.InstallScripts[node.ID] = script
		} else {
			peers := result.PeerMap[node.ID]
			_, hasBabel := result.BabelConfigs[node.ID]
			script, err := renderer.RenderInstallScript(&node, peers, hasBabel)
			if err != nil {
				return fmt.Errorf(" %s : %w", node.Name, err)
			}
			result.InstallScripts[node.ID] = script
		}
	}

	// Deploy scripts (bash + PowerShell)
	bashDeploy, ps1Deploy, err := renderer.RenderDeployScripts(result.Topology)
	if err != nil {
		return fmt.Errorf("deploy script render: %w", err)
	}
	result.DeployScripts["deploy-all.sh"] = bashDeploy
	result.DeployScripts["deploy-all.ps1"] = ps1Deploy

	return nil
}

func createExportZip(dir string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

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

		installer, err := makeSelfExtractingInstaller(nodeName, tgz.Bytes())
		if err != nil {
			return nil, err
		}

		installHeader := &zip.FileHeader{Name: nodeName + ".install.sh", Method: zip.Deflate}
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

func makeSelfExtractingInstaller(nodeName string, payload []byte) ([]byte, error) {
	encoded := base64.StdEncoding.EncodeToString(payload)

	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

NODE_NAME=%q
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
tar -xzf "${ARCHIVE_PATH}" -C "${WORKDIR}"

if [[ ! -f "${WORKDIR}/install.sh" ]]; then
	echo "ERROR: install.sh not found in extracted payload" >&2
	exit 1
fi

echo "Running node installer for ${NODE_NAME}..."
if [[ "$(id -u)" -eq 0 ]]; then
	bash "${WORKDIR}/install.sh"
elif command -v sudo >/dev/null 2>&1; then
	sudo bash "${WORKDIR}/install.sh"
else
	echo "ERROR: root privileges required (run as root or install sudo)" >&2
	exit 1
fi

exit 0
__PAYLOAD_BELOW__
%s
`, nodeName, encoded)

	return []byte(script), nil
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}
