package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
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
	return newLSPBridgeWith(config, workspaceRoot, nil, nil)
}

// newLSPBridgeWith is the injectable seam (dial, observer). The bridge logs
// lifecycle/diagnostics events through the runtime logger, and
// `lsp.prewarm` starts the pool here instead of on the first edit.
func newLSPBridgeWith(config *runtimecfg.RuntimeConfig, workspaceRoot string, dial lsp.DialFunc, observer lsp.Observer) *lsp.Bridge {
	if config == nil || !config.LSP.Enabled {
		return nil
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		root = "."
	}
	bridge := lsp.NewBridgeWithOptions(config.LSP, root, lsp.BridgeOptions{
		Logger:   lspRuntimeLogger{},
		Dial:     dial,
		Observer: observer,
	})
	if config.LSP.Prewarm && bridge.Enabled() {
		bridge.StartAll(context.Background())
	}
	return bridge
}

// currentLSPBridge returns the manager's pool under the bridge lock, so
// callers on turn goroutines never race with a late AttachLSP.
func (m *Manager) currentLSPBridge() *lsp.Bridge {
	if m == nil {
		return nil
	}
	m.lspMu.RLock()
	defer m.lspMu.RUnlock()
	return m.lspBridge
}

// LSPEnabled reports whether this manager currently owns an enabled language
// server pool.
func (m *Manager) LSPEnabled() bool {
	bridge := m.currentLSPBridge()
	return bridge != nil && bridge.Enabled()
}

// EnableLSPFromConfig attaches the language-server pool described by config and
// registers the LSP tool surface on the existing registry. It is the
// late-binding counterpart of the construction-time wiring: a host may run a
// project scan asynchronously and call this once the effective config enables
// LSP, instead of blocking startup on the scan.
//
// The call is idempotent and safe off the turn goroutine: the bridge pointer is
// guarded by lspMu and the toolkit registry owns its own lock. It returns true
// only when this call attached the pool (already enabled, config still
// disabled, or no usable server spec all return false). A pool attached this
// way is released by the same Close as a construction-time pool.
func (m *Manager) EnableLSPFromConfig(config *runtimecfg.RuntimeConfig) bool {
	if m == nil || config == nil || !config.LSP.Enabled {
		return false
	}
	m.lspMu.Lock()
	defer m.lspMu.Unlock()
	if m.lspBridge != nil && m.lspBridge.Enabled() {
		return false
	}
	bridge := newLSPBridge(config, strings.TrimSpace(config.Workspace.Root))
	if bridge == nil || !bridge.Enabled() {
		return false
	}
	// Register the tools before publishing the bridge: once lspBridge is set,
	// ListTools advertises the inline-diagnostics hint, so the registry must
	// already contain lsp_servers/lsp_diagnostics at that point.
	if m.toolkit != nil {
		registerLSPTooling(m.toolkit, bridge, config)
	}
	m.lspBridge = bridge
	return true
}

// lspRuntimeLogger adapts the runtime's global logger to the minimal
// lsp.Logger surface, so server stderr and lifecycle events land in the
// session log (L2 observability).
type lspRuntimeLogger struct{}

func (lspRuntimeLogger) Debugf(format string, args ...interface{}) {
	logpkg.S().Debugf(format, args...)
}
func (lspRuntimeLogger) Infof(format string, args ...interface{}) { logpkg.S().Infof(format, args...) }
func (lspRuntimeLogger) Warnf(format string, args ...interface{}) { logpkg.S().Warnf(format, args...) }
func (lspRuntimeLogger) Errorf(format string, args ...interface{}) {
	logpkg.S().Errorf(format, args...)
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
	bridge := m.currentLSPBridge()
	if bridge == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bridge.Stop(ctx)
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
	if !status.LastActive.IsZero() {
		builder.WriteString(" last_active=")
		builder.WriteString(status.LastActive.UTC().Format(time.RFC3339))
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
