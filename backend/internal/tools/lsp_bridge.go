package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Tool names for the LSP tool surface (docs/lsp 03 W7).
const (
	// LSPDiagnosticsToolName is the optional on-demand diagnostics tool.
	// The inline loop (W6) is the primary feedback path; this tool is a
	// supplement for files the inline loop cannot cover.
	LSPDiagnosticsToolName = "lsp_diagnostics"
	// LSPServersToolName exposes pool observability (L2): which servers are
	// ready, unavailable, or crashed, and why.
	LSPServersToolName = "lsp_servers"
)

// lspInlineDiagnosticsHint is appended to the model-facing description of
// editing tools while the pool is enabled (docs/lsp 02 §4.2). It tells the
// model that diagnostics arrive in the same tool result, so it does not need
// a follow-up round trip.
const lspInlineDiagnosticsHint = "启用 LSP 时，本工具成功后会把同一文件的最新诊断追加在返回文本尾部（追加不改写原文）；无需再调用 lsp_diagnostics 复核本次改动。LSP 可能不存在、不可用或未覆盖该文件：此时本工具照常返回编辑结果，只是没有诊断追加，编辑本身不受影响。"

// newLSPBridge builds the tool-layer facade. It returns nil when the LSP
// section is disabled so every call site can treat LSP as absent (A11).
func newLSPBridge(config *runtimecfg.RuntimeConfig, workspaceRoot string) *lsp.Bridge {
	if config == nil || !config.LSP.Enabled {
		return nil
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		root = "."
	}
	return lsp.NewBridge(config.LSP, root, nil, nil)
}

// registerLSPTooling registers the LSP tools that are enabled by config.
// `lsp_servers` mirrors the pool state; `lsp_diagnostics` is opt-in through
// diagnostics.tool_enabled (W7).
func registerLSPTooling(registry *toolkit.Registry, bridge *lsp.Bridge, config *runtimecfg.RuntimeConfig) {
	if registry == nil || bridge == nil || !bridge.Enabled() {
		return
	}
	diagnosticsCfg := bridge.Config().Diagnostics
	_ = registry.Register(newLSPServersTool(bridge))
	if config != nil && config.LSP.Diagnostics.ToolEnabled {
		_ = registry.Register(newLSPDiagnosticsTool(bridge))
		return
	}
	if diagnosticsCfg.ToolEnabled {
		_ = registry.Register(newLSPDiagnosticsTool(bridge))
	}
}

// Close releases the language-server pool owned by this manager (L2). The
// host calls it on shutdown; a nil bridge is a no-op.
func (m *Manager) Close() error {
	if m == nil || m.lspBridge == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.lspBridge.Stop(ctx)
	return nil
}

// Close releases the pool through the adapter surface so hosts that only hold
// the agent-facing adapter can still shut LSP processes down.
func (a *AgentAdapter) Close() error {
	if a == nil || a.manager == nil {
		return nil
	}
	return a.manager.Close()
}

// ---- lsp_servers ----

type lspServersTool struct {
	*toolkit.BaseTool
	bridge *lsp.Bridge
}

func newLSPServersTool(bridge *lsp.Bridge) *lspServersTool {
	parameters := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   []string{},
	}
	return &lspServersTool{
		BaseTool: toolkit.NewBaseTool(
			LSPServersToolName,
			"列出当前工作区的语言服务器池状态（ready/starting/unavailable/crashed/stopped 及失败原因）。只读，不触发启动。",
			"1.0.0",
			parameters,
			true,
		),
		bridge: bridge,
	}
}

func (t *lspServersTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindRead,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

func (t *lspServersTool) Execute(_ context.Context, _ map[string]interface{}) (*toolkit.ToolResult, error) {
	if t == nil || t.bridge == nil || !t.bridge.Enabled() {
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content:    "LSP 未启用：没有配置任何可用的语言服务器。\n",
		}, nil
	}
	statuses := t.bridge.Statuses()
	lines := make([]string, 0, len(statuses)+1)
	lines = append(lines, fmt.Sprintf("LSP pool: %d server(s), root=%s", len(statuses), t.bridge.Root()))
	for _, status := range statuses {
		lines = append(lines, formatLSPServerStatus(status))
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    strings.Join(lines, "\n") + "\n",
		Metadata: map[string]interface{}{
			"lsp_servers": len(statuses),
		},
	}, nil
}

func formatLSPServerStatus(status lsp.ServerStatus) string {
	var builder strings.Builder
	builder.WriteString("- ")
	builder.WriteString(status.Name)
	builder.WriteString(": ")
	builder.WriteString(string(status.State))
	if status.Language != "" {
		builder.WriteString(" lang=")
		builder.WriteString(status.Language)
	}
	if status.PID > 0 {
		builder.WriteString(fmt.Sprintf(" pid=%d", status.PID))
	}
	if status.Restarts > 0 {
		builder.WriteString(fmt.Sprintf(" restarts=%d", status.Restarts))
	}
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		builder.WriteString(" reason=")
		builder.WriteString(reason)
	}
	if lastErr := strings.TrimSpace(status.LastError); lastErr != "" {
		builder.WriteString(" last_error=")
		builder.WriteString(lastErr)
	}
	return builder.String()
}

// ---- lsp_diagnostics (W7, opt-in) ----

type lspDiagnosticsTool struct {
	*toolkit.BaseTool
	bridge *lsp.Bridge
}

func newLSPDiagnosticsTool(bridge *lsp.Bridge) *lspDiagnosticsTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "要查询诊断的文件路径（相对当前工作区或绝对路径）。",
			},
		},
		"required": []string{"path"},
	}
	return &lspDiagnosticsTool{
		BaseTool: toolkit.NewBaseTool(
			LSPDiagnosticsToolName,
			"查询单个文件的语言服务器诊断（LSP）。编辑类工具已内联同一文件的诊断；仅在需要复核其他文件或内联未覆盖时使用。",
			"1.0.0",
			parameters,
			true,
		),
		bridge: bridge,
	}
}

func (t *lspDiagnosticsTool) DefinitionMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindRead,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

func (t *lspDiagnosticsTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	path := ""
	if raw, ok := params["path"].(string); ok {
		path = strings.TrimSpace(raw)
	}
	if path == "" {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("path 参数不能为空"),
		}, nil
	}
	if t == nil || t.bridge == nil || !t.bridge.Enabled() {
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content:    "LSP 未启用：没有可用的语言服务器。\n",
		}, nil
	}
	resolved := t.resolvePath(ctx, path)
	text, handled, err := t.bridge.Report(ctx, resolved)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	if !handled {
		return &toolkit.ToolResult{
			Success:    true,
			OutputKind: toolresult.KindText,
			Content:    fmt.Sprintf("没有已配置的语言服务器负责 %s（静默跳过，非错误）。\n", resolved),
		}, nil
	}
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    text,
	}, nil
}

func (t *lspDiagnosticsTool) resolvePath(ctx context.Context, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if root := strings.TrimSpace(toolctx.WorkspaceRoot(ctx)); root != "" {
		return filepath.Join(root, path)
	}
	return path
}
