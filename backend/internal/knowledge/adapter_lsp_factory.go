package knowledge

import (
	"os"
	"path/filepath"
	"strings"

	knowledgelsp "github.com/wwsheng009/ai-agent-runtime/internal/knowledge/lsp"
	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// Phase 4 接线（04 §5 Phase 4 交付 2/3 → Phase 3 工具面消费）：语义通道的
// 生产构造入口。工具面按配置获得 SemanticAdapter；不可用时返回稳定 reason，
// 由调用方降级到索引/启发式路径（Degrade-Not-Fail）。

// 语义通道降级原因 token（稳定，供日志与工具 explanation 消费）。
const (
	SemanticReasonDisabled       = "lsp_disabled"
	SemanticReasonModeNotSelf    = "lsp_mode_not_self"
	SemanticReasonWorkspace      = "workspace_missing"
	SemanticReasonNoLanguage     = "no_supported_language"
	SemanticReasonServerSpecMiss = "server_spec_missing"
)

// SemanticWorkspaceLanguage 报告 workspace 的语义服务语言；v1 仅 Go（需要
// go.mod 作为模块根——gopls 的类型信息依赖可解析的模块）。空串表示不支持。
func SemanticWorkspaceLanguage(workspaceRoot string) string {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(root, "go.mod")); err == nil && !info.IsDir() {
		return "go"
	}
	return ""
}

// NewSemanticAdapterForWorkspace 按配置为 workspace 构造语义适配器。
//
// 返回 (adapter, reason)：adapter == nil 时 reason 为稳定降级 token；
// 构造本身**不启动进程**——首次查询时由适配器内部 Ensure（受 startup_timeout
// 约束），启动失败同样降级。门控与 ADR-0002 §4.1 一致：
// Enabled=false 是硬闸；Mode 仅接受 self（external 由宿主侧集成，不在自管通道）。
func NewSemanticAdapterForWorkspace(cfg Config, workspaceRoot string) (SemanticAdapter, string) {
	cfg = cfg.Normalize()
	if !cfg.LSP.Enabled {
		return nil, SemanticReasonDisabled
	}
	if mode := strings.ToLower(strings.TrimSpace(cfg.LSP.Mode)); mode != "self" {
		return nil, SemanticReasonModeNotSelf
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return nil, SemanticReasonWorkspace
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if SemanticWorkspaceLanguage(root) == "" {
		return nil, SemanticReasonNoLanguage
	}
	specs := baselsp.PresetServersNamed([]string{"gopls"})
	if len(specs) == 0 {
		return nil, SemanticReasonServerSpecMiss
	}
	manager := knowledgelsp.NewManager(knowledgelsp.Options{
		Root:           root,
		Spec:           specs[0],
		MaxProcesses:   cfg.LSP.MaxProcesses,
		MemoryLimitMB:  cfg.LSP.MemoryLimitMB,
		StartupTimeout: cfg.LSP.StartupTimeout,
		RequestTimeout: cfg.LSP.RequestTimeout,
	})
	return NewLSPSemanticAdapter(manager, root), ""
}
