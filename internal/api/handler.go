package api

import (
	"archive/zip"
	"bytes"
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
)

// Handler HTTP API 处理器
type Handler struct {
	compiler *compiler.Compiler
}

// NewHandler 创建新的 API 处理器
func NewHandler() *Handler {
	return &Handler{
		compiler: compiler.NewCompiler(),
	}
}

// apiError 统一错误响应
type apiError struct {
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
}

// HealthResponse 健康检查响应
type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// ValidateResponse 校验响应
type ValidateResponse struct {
	Valid    bool                       `json:"valid"`
	Errors   []validator.ValidationError `json:"errors,omitempty"`
	Warnings []validator.ValidationError `json:"warnings,omitempty"`
}

// CompileResponse 编译响应
type CompileResponse struct {
	Topology         *model.Topology            `json:"topology"`
	WireGuardConfigs map[string]string           `json:"wireguard_configs"`
	BabelConfigs     map[string]string           `json:"babel_configs"`
	SysctlConfigs    map[string]string           `json:"sysctl_configs"`
	InstallScripts   map[string]string           `json:"install_scripts"`
	Manifest         compiler.CompileManifest    `json:"manifest"`
}

// HandleHealth 健康检查
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET 方法")
		return
	}

	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().Format(time.RFC3339),
	})
}

// HandleValidate 校验拓扑
func (h *Handler) HandleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST 方法")
		return
	}

	topo, err := readTopology(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Schema 校验
	schemaResult := validator.ValidateSchema(topo)
	// 语义校验
	semanticResult := validator.ValidateSemantic(topo)

	// 合并结果
	allErrors := append(schemaResult.Errors, semanticResult.Errors...)
	allWarnings := append(schemaResult.Warnings, semanticResult.Warnings...)

	writeJSON(w, http.StatusOK, ValidateResponse{
		Valid:    len(allErrors) == 0,
		Errors:   allErrors,
		Warnings: allWarnings,
	})
}

// HandleCompile 编译拓扑
func (h *Handler) HandleCompile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST 方法")
		return
	}

	topo, err := readTopology(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 生成假密钥（Phase 1 阶段，后续替换为真密钥）
	keys := generateKeys(topo)

	// 编译
	result, err := h.compiler.Compile(topo, keys)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// 渲染所有配置
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
		Manifest:         result.Manifest,
	})
}

// HandleExport 导出产物压缩包
func (h *Handler) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST 方法")
		return
	}

	topo, err := readTopology(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	keys := generateKeys(topo)

	result, err := h.compiler.Compile(topo, keys)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if err := renderAll(result, keys); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 导出到临时目录
	tmpDir, err := os.MkdirTemp("", "overlay-export-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建临时目录失败")
		return
	}
	defer os.RemoveAll(tmpDir)

	if _, err := artifacts.Export(result, tmpDir); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("导出失败: %v", err))
		return
	}

	// 打包为 zip
	zipBuf, err := createZip(tmpDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("打包失败: %v", err))
		return
	}

	filename := fmt.Sprintf("%s-artifacts.zip", topo.Project.ID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	w.Write(zipBuf.Bytes())
}

// --- 辅助函数 ---

func readTopology(r *http.Request) (*model.Topology, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	defer r.Body.Close()

	if len(body) == 0 {
		return nil, fmt.Errorf("请求体为空")
	}

	var topo model.Topology
	if err := json.Unmarshal(body, &topo); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}

	return &topo, nil
}

func generateKeys(topo *model.Topology) map[string]compiler.KeyPair {
	keys := make(map[string]compiler.KeyPair)
	for _, node := range topo.Nodes {
		keys[node.ID] = compiler.KeyPair{
			PrivateKey: fmt.Sprintf("FAKE_PRIVKEY_%s", node.ID),
			PublicKey:  fmt.Sprintf("FAKE_PUBKEY_%s", node.ID),
		}
	}
	return keys
}

func renderAll(result *compiler.CompileResult, keys map[string]compiler.KeyPair) error {
	// WireGuard
	wgConfigs, err := renderer.RenderAllWireGuardConfigs(result.Topology, result.PeerMap, keys)
	if err != nil {
		return fmt.Errorf("渲染 WireGuard 配置失败: %w", err)
	}
	result.WireGuardConfigs = wgConfigs

	// Babel
	babelConfigs, err := renderer.RenderAllBabelConfigs(result.Topology, result.PeerMap)
	if err != nil {
		return fmt.Errorf("渲染 Babel 配置失败: %w", err)
	}
	result.BabelConfigs = babelConfigs

	// Sysctl
	sysctlConfigs, err := renderer.RenderAllSysctlConfigs(result.Topology)
	if err != nil {
		return fmt.Errorf("渲染 sysctl 配置失败: %w", err)
	}
	result.SysctlConfigs = sysctlConfigs

	// 安装脚本
	for _, node := range result.Topology.Nodes {
		_, hasBabel := result.BabelConfigs[node.ID]
		script, err := renderer.RenderInstallScript(&node, hasBabel)
		if err != nil {
			return fmt.Errorf("渲染节点 %s 安装脚本失败: %w", node.Name, err)
		}
		result.InstallScripts[node.ID] = script
	}

	return nil
}

func createZip(dir string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

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

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.Method = zip.Deflate

		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})
	if err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf, nil
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}
