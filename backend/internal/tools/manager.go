package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolschema"
)

// ToolDescriptor describes a tool available to the runtime.
type ToolDescriptor struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
	Metadata    map[string]interface{}
}

// Manager unifies toolkit tools and MCP tools.
type Manager struct {
	toolkit       *toolkit.Registry
	mcp           manager.Manager
	sandbox       *runtimeexecutor.Sandbox
	runtimeConfig *runtimecfg.RuntimeConfig
	// codeGate 是 ADR-0004 的 code.* 分级注册闸门（nil = code_tools off 或
	// mode=off，工具面与改动前逐字节一致）。
	codeGate *codeToolGate
	// lspMu guards lspBridge: hosts may attach the pool after construction
	// (async project scan → late enable), while turns keep listing tools.
	lspMu     sync.RWMutex
	lspBridge *lsp.Bridge
	// lspObserver 是池事件的 host 转发槽：可后置注入（会话 runtime host 建立
	// 晚于工具管理器构造），桥在构造期即持有 hub，注入后立即生效。
	lspObserver *lspObserverHub
}

const toolkitMCPName = "toolkit"

var localToolkitPriorityTools = map[string]struct{}{
	"ls":   {},
	"glob": {},
	"grep": {},
	"rg":   {},
	"view": {},
}

// NewDefaultManager registers the built-in toolkit tools and merges MCP tools.
func NewDefaultManager(mcp manager.Manager) *Manager {
	return NewDefaultManagerWithRuntimeConfig(mcp, nil)
}

// NewDefaultManagerWithRuntimeConfig registers built-in toolkit tools with optional sandbox policy.
func NewDefaultManagerWithRuntimeConfig(mcp manager.Manager, config *runtimecfg.RuntimeConfig) *Manager {
	registry := toolkit.NewRegistry()
	var sandbox *runtimeexecutor.Sandbox
	workspaceRoot := ""
	if config != nil {
		sandbox = runtimeexecutor.NewSandbox(&config.Sandbox)
		workspaceRoot = strings.TrimSpace(config.Workspace.Root)
	}
	codeGate := registerBuiltinToolkitTools(registry, sandbox, workspaceRoot, config)
	manager := &Manager{
		toolkit:       registry,
		mcp:           mcp,
		sandbox:       sandbox,
		runtimeConfig: config,
		codeGate:      codeGate,
		lspObserver:   &lspObserverHub{},
	}
	manager.lspBridge = newLSPBridgeWith(config, workspaceRoot, nil, manager.lspObserver.emit)
	if manager.lspBridge != nil && manager.lspBridge.Enabled() {
		registerLSPTooling(registry, manager.lspBridge, config)
	}
	return manager
}

// ListTools returns the unified tool list, preferring MCP tools on name conflict.
func (m *Manager) ListTools() []ToolDescriptor {
	// ADR-0004 §4.1：每次列出工具前按最新陈旧度重新评估并切换分组；
	// 会话内动态生效（无需重启会话）。
	if m != nil && m.codeGate != nil {
		m.codeGate.sync(context.Background())
	}
	seen := make(map[string]struct{})
	toolsList := make([]ToolDescriptor, 0)

	if m.mcp != nil {
		mcpTools := m.mcp.ListTools()
		callableNames := mcpregistry.CallableToolNames(mcpTools)
		for index, info := range mcpTools {
			if info == nil || !info.Enabled || info.Tool == nil {
				continue
			}
			if m.shouldPreferLocalToolkit(info.MCPName, info.Tool.Name) {
				continue
			}
			callableName := callableNames[index]
			if _, exists := seen[callableName]; exists {
				continue
			}
			seen[callableName] = struct{}{}
			metadata := withMCPIdentity(cloneMetadataMap(info.Metadata), info, callableName)
			toolsList = append(toolsList, ToolDescriptor{
				Name:        callableName,
				Description: info.Tool.Description,
				Parameters:  normalizeParameters(info.Tool.InputSchema),
				Metadata:    metadata,
			})
		}
	}

	if m.toolkit != nil {
		for _, tool := range m.toolkit.List() {
			name := tool.Name()
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			metadata := map[string]interface{}(nil)
			if provider, ok := tool.(toolkit.ToolDefinitionMetadataProvider); ok {
				metadata = provider.DefinitionMetadata()
			}
			parameters, exists := m.toolkit.ParameterSchema(name)
			if !exists {
				parameters = normalizeParameters(nil)
			}
			description := tool.Description()
			if m.lspInlineHintEnabled(name) {
				description = strings.TrimRight(description, " ") + " " + lspInlineDiagnosticsHint
			}
			if m.codeGate != nil {
				description += m.codeGate.descriptionSuffix(name)
			}
			toolsList = append(toolsList, ToolDescriptor{
				Name:        name,
				Description: description,
				Parameters:  parameters,
				Metadata:    cloneMetadataMap(metadata),
			})
		}
	}

	if meta := m.metaToolDescriptor(); meta != nil {
		if _, exists := seen[meta.Name]; !exists {
			toolsList = append(toolsList, *meta)
		}
	}

	sort.Slice(toolsList, func(i, j int) bool {
		return toolsList[i].Name < toolsList[j].Name
	})

	return toolsList
}

// Execute runs a tool by name, preferring MCP tools over toolkit tools.
func (m *Manager) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	output, _, err := m.ExecuteWithMeta(ctx, name, args)
	return output, err
}

// ExecuteWithMeta runs a tool and preserves structured metadata for runtime callers.
// 统一耗时兜底：工具自报 duration_ms 优先（bash 等），未上报的进程内工具
// （ls/view/grep/edit…）按墙钟补一个 >0 的毫秒值，使 tool.completed 与
// usage analytics 拿到同一口径；0ms（亚毫秒或失败前置返回）不伪造样本。
func (m *Manager) ExecuteWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error) {
	start := time.Now()
	output, metadata, err := m.executeWithMeta(ctx, name, args)
	if err == nil {
		var appended int
		output, appended = m.appendLSPDiagnostics(ctx, metadata, output)
		metadata = stampReservedTailBytes(metadata, appended)
	}
	return output, withToolDurationFallback(metadata, time.Since(start)), err
}

// appendLSPDiagnostics is W6: after a successful mutation it appends the
// current diagnostics of every written file to the untouched tool output
// (docs/lsp 03 invariants I1/I2). It is a no-op unless the LSP pool is
// enabled and the tool reported `mutated_paths`.
//
// The second return value is how many bytes the appended block added, which the
// caller declares as the reserved tail (toolresult.MetadataReservedTailBytesKey)
// so the render-layer fold keeps the diagnostics instead of dropping them. That
// declaration is a generic contract, not an LSP one - see the key's doc - so a
// second producer appending its own block reuses this path rather than adding
// one.
func (m *Manager) appendLSPDiagnostics(ctx context.Context, metadata map[string]interface{}, output string) (string, int) {
	bridge := m.currentLSPBridge()
	if bridge == nil || !bridge.Enabled() {
		return output, 0
	}
	paths := toolresult.MutatedPaths(metadata)
	if len(paths) == 0 {
		return output, 0
	}
	appended := bridge.AppendToResult(ctx, output, paths)
	// Measured, not estimated: AppendToResult returns the original text plus
	// the joined blocks plus the separating newline, so the delta is exactly
	// the block the model will see and cannot drift from it.
	return appended, len(appended) - len(output)
}

// stampReservedTailBytes records the appended block's size on the result
// metadata. A nil map is only materialized when there is something to record,
// so a call that appended nothing keeps the previous behavior byte for byte.
func stampReservedTailBytes(metadata map[string]interface{}, appended int) map[string]interface{} {
	if appended <= 0 {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]interface{}, 1)
	}
	metadata[toolresult.MetadataReservedTailBytesKey] = appended
	return metadata
}

// lspInlineHintEnabled reports whether the tool's model-facing description
// should carry the inline-diagnostics expectation (docs/lsp 02 §4.2).
func (m *Manager) lspInlineHintEnabled(toolName string) bool {
	bridge := m.currentLSPBridge()
	if bridge == nil || !bridge.Enabled() {
		return false
	}
	switch toolName {
	case "edit", "write", "append_write", "apply_patch", "multiedit":
		return true
	default:
		return false
	}
}

// withToolDurationFallback 只在工具未上报有效耗时时补墙钟值；0ms（亚毫秒）保持
// 缺省，由 analytics 的事件时间差回退兜底，避免把未知伪造成 0。
func withToolDurationFallback(metadata map[string]interface{}, elapsed time.Duration) map[string]interface{} {
	if ms := elapsed.Milliseconds(); ms > 0 && toolMetadataDurationMS(metadata) <= 0 {
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["duration_ms"] = ms
	}
	return metadata
}

// toolMetadataDurationMS 读取工具元数据里的 duration_ms（工具可能写 int/int64/float64）。
func toolMetadataDurationMS(metadata map[string]interface{}) int64 {
	if metadata == nil {
		return 0
	}
	switch value := metadata["duration_ms"].(type) {
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case float32:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

// executeLocalToolkitTool runs a built-in tool after argument normalization and
// surfaces applied input repairs to the model as a <repair_note> prefix (rule +
// key only, never values), so a rewritten call never looks like the model's own.
func executeLocalToolkitTool(ctx context.Context, tool toolkit.Tool, toolName string, args map[string]interface{}) (string, map[string]interface{}, error) {
	normalized := normalizeToolkitToolArgs(toolName, toolargs.Normalize(args))
	notes := toolkitArgRepairNotes(toolName, args, normalized)
	result, err := tool.Execute(ctx, normalized)
	if err != nil {
		return "", nil, err
	}
	output, metadata, err := formatToolkitResultWithSource(result, toolresult.SourceToolkit)
	if len(notes) > 0 {
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["repair_notes"] = notes
		output = "<repair_note>" + strings.Join(notes, "; ") + "</repair_note>\n" + output
	}
	return output, metadata, err
}

func (m *Manager) executeWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error) {
	if name == "list_mcp_resources" {
		metadata := toolresult.WithSource(toolresult.WithKind(nil, toolresult.KindText), toolresult.SourceMeta)
		if toolprotocol.HasReporter(ctx) {
			toolprotocol.ReportPhase(ctx, toolprotocol.PhaseStart, "mcp call started", map[string]interface{}{
				"tool_name": name,
				"source":    toolresult.SourceMeta,
			})
		}
		output, err := m.listMCPResources(ctx, args)
		if strings.TrimSpace(output) != "" {
			metadata["output_size"] = len(output)
			toolprotocol.ReportTextStream(ctx, output, toolprotocol.StreamChannelCombined)
		}
		if toolprotocol.HasReporter(ctx) {
			finishMsg := "mcp call finished"
			if err != nil {
				finishMsg = "mcp call failed"
			}
			toolprotocol.ReportPhase(ctx, toolprotocol.PhaseFinish, finishMsg, map[string]interface{}{
				"tool_name": name,
				"source":    toolresult.SourceMeta,
			})
		}
		return output, metadata, err
	}

	lookupName := canonicalManagedToolName(name)
	// ADR-0004 §4.1：不在当前档位（未注册）的 code.* 直接拒绝；工具自身的
	// 运行时守卫是第二道防线（注册决策与执行之间陈旧度可能漂移）。
	if m.codeGate != nil && m.codeGate.isCodeTool(lookupName) && !m.codeGate.toolVisible(lookupName) {
		return "", nil, fmt.Errorf("tool '%s' is not available: code index snapshot is out of policy (ADR-0004)", name)
	}
	if m.toolkit != nil {
		if tool, ok := m.toolkit.Get(lookupName); ok {
			if m.shouldPreferLocalToolkitTool(lookupName) {
				return executeLocalToolkitTool(ctx, tool, lookupName, args)
			}
		}
	}

	if m.mcp != nil {
		info, findErr := m.mcp.FindTool(name)
		if findErr != nil && mcpregistry.IsAmbiguousToolError(findErr) {
			return "", nil, findErr
		}
		if findErr == nil && info != nil && info.Tool != nil {
			if m.shouldPreferLocalToolkit(info.MCPName, lookupName) && m.toolkit != nil {
				if tool, ok := m.toolkit.Get(lookupName); ok {
					return executeLocalToolkitTool(ctx, tool, lookupName, args)
				}
			}
			if toolprotocol.HasReporter(ctx) {
				toolprotocol.ReportPhase(ctx, toolprotocol.PhaseStart, "mcp call started", map[string]interface{}{
					"tool_name":     name,
					"raw_tool_name": info.Tool.Name,
					"mcp_name":      info.MCPName,
					"source":        toolresult.SourceMCP,
				})
			}
			executionName := mcpregistry.ExecutionLookupName(info, m.mcp.ListTools())
			result, callErr := m.mcp.CallTool(ctx, info.MCPName, executionName, args)
			if callErr != nil {
				if toolprotocol.HasReporter(ctx) {
					toolprotocol.ReportPhase(ctx, toolprotocol.PhaseFinish, "mcp call failed", map[string]interface{}{
						"tool_name": name,
						"mcp_name":  info.MCPName,
						"source":    toolresult.SourceMCP,
					})
				}
				return "", withMCPIdentity(nil, info, name), callErr
			}
			output, metadata, formatErr := formatMCPResultWithSource(result, toolresult.SourceMCP)
			metadata = withMCPIdentity(metadata, info, name)
			if strings.TrimSpace(output) != "" {
				toolprotocol.ReportTextStream(ctx, output, toolprotocol.StreamChannelCombined)
			}
			if toolprotocol.HasReporter(ctx) {
				finishMsg := "mcp call finished"
				if formatErr != nil {
					finishMsg = "mcp call failed"
				}
				toolprotocol.ReportPhase(ctx, toolprotocol.PhaseFinish, finishMsg, map[string]interface{}{
					"tool_name":     name,
					"raw_tool_name": info.Tool.Name,
					"mcp_name":      info.MCPName,
					"source":        toolresult.SourceMCP,
				})
			}
			return output, metadata, formatErr
		}
	}

	if m.toolkit != nil {
		if tool, ok := m.toolkit.Get(lookupName); ok {
			result, err := tool.Execute(ctx, normalizeToolkitToolArgs(lookupName, args))
			if err != nil {
				return "", nil, err
			}
			return formatToolkitResultWithSource(result, toolresult.SourceToolkit)
		}
	}

	return "", nil, fmt.Errorf("tool '%s' not found", name)
}

func (m *Manager) resolveToolSource(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if name == "list_mcp_resources" {
		return toolresult.SourceMeta
	}
	lookupName := canonicalManagedToolName(name)
	if m.toolkit != nil {
		if _, ok := m.toolkit.Get(lookupName); ok && m.shouldPreferLocalToolkitTool(lookupName) {
			return toolresult.SourceToolkit
		}
	}
	if m.mcp != nil {
		info, err := m.mcp.FindTool(name)
		if err == nil && info != nil {
			if m.shouldPreferLocalToolkit(info.MCPName, lookupName) {
				return toolresult.SourceToolkit
			}
			return toolresult.SourceMCP
		}
		if mcpregistry.IsAmbiguousToolError(err) {
			return toolresult.SourceMCP
		}
	}
	if m.toolkit != nil {
		if _, ok := m.toolkit.Get(lookupName); ok {
			return toolresult.SourceToolkit
		}
	}
	return ""
}

func (m *Manager) shouldPreferLocalToolkit(mcpName, toolName string) bool {
	if m == nil || m.toolkit == nil {
		return false
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	_, ok := m.toolkit.Get(toolName)
	if !ok {
		return false
	}
	if m.shouldForceLocalToolkitTool(toolName) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(mcpName), toolkitMCPName) {
		return false
	}
	return true
}

func (m *Manager) shouldForceLocalToolkitTool(toolName string) bool {
	if m == nil {
		return false
	}
	_, ok := localToolkitPriorityTools[strings.ToLower(strings.TrimSpace(toolName))]
	return ok
}

func (m *Manager) shouldPreferLocalToolkitTool(toolName string) bool {
	return m.shouldPreferLocalToolkit(toolkitMCPName, toolName)
}

func canonicalManagedToolName(name string) string {
	canonical := toolnames.CanonicalOpenAIImageGenerateToolName(name)
	if canonical != "" {
		return canonical
	}
	return strings.TrimSpace(name)
}

func registerBuiltinToolkitTools(registry *toolkit.Registry, sandbox *runtimeexecutor.Sandbox, workspaceRoot string, runtimeConfig *runtimecfg.RuntimeConfig) *codeToolGate {
	// Phase 3（06 §4 Phase 3）：code.* 工具面（默认 off；knowledge.code_tools=on
	// 才注册，回滚即关闭）。索引不可用时工具自身按降级协议 fallback 到 grep/view。
	// ADR-0004 §4.4 全局硬闸：mode=off 时无论 code_tools 如何都不得注册 code.*
	// （工具面必须回到纯 grep/view 基线）。
	var codeResolver tools.CodeIndexResolver
	if runtimeConfig != nil && runtimeConfig.Knowledge.CodeToolsEnabled() && runtimeConfig.Knowledge.Enabled() {
		codeResolver = newCodeIndexResolver(runtimeConfig.Knowledge, workspaceRoot)
	}
	configure := func(tool toolkit.Tool) {
		if configurable, ok := tool.(interface {
			SetSandbox(*runtimeexecutor.Sandbox)
		}); ok {
			configurable.SetSandbox(sandbox)
		}
		if configurable, ok := tool.(interface {
			SetBasePath(string)
		}); ok {
			configurable.SetBasePath(workspaceRoot)
		}
		if codeResolver != nil {
			if configurable, ok := tool.(interface {
				SetCodeIndexResolver(tools.CodeIndexResolver)
			}); ok {
				configurable.SetCodeIndexResolver(codeResolver)
			}
		}
	}
	register := func(tool toolkit.Tool) {
		configure(tool)
		_ = registry.Register(tool)
	}

	// Ignore duplicates; registry will reject with error.
	// shell is the preferred Codex-aligned surface; bash and
	// execute_shell_command remain compatibility aliases with identical
	// execution semantics (including content-success for non-zero exits).
	register(tools.NewShellTool())
	register(tools.NewBashTool())
	register(tools.NewExecuteShellCommandTool())
	register(tools.NewAICLIExecTool())
	register(tools.NewApplyPatchTool())
	register(tools.NewViewTool())
	register(tools.NewEditTool())
	register(tools.NewWriteTool())
	register(tools.NewAppendWriteTool())
	register(tools.NewGlobTool())
	register(tools.NewGrepTool())
	// rg 是 grep 的兼容别名（同一 schema/执行实现）：注册它让以 shell 习惯名
	// `rg` 发起的工具调用落到 grep，而不是 "tool not found"。模型面仍只列出
	// grep（见 agent.optimizeModelToolSurface 的搜索面折叠）。
	register(tools.NewRGTool())
	register(tools.NewLsTool())
	register(tools.NewDownloadTool())
	register(tools.NewFetchTool())
	register(tools.NewMultieditTool())
	register(tools.NewTodosTool())
	register(tools.NewArtifactReadTool())
	register(tools.NewSourcegraphTool())
	register(tools.NewWebSearchTool())
	if shouldRegisterOpenAIImageGenerateTool(runtimeConfig) {
		register(tools.NewOpenAIImageGenerateTool(runtimeConfig))
	}
	if codeResolver == nil {
		return nil
	}
	// ADR-0004 §4.1/§4.4：构造期先按初始档位注册（复用本文件既有的条件
	// 注册机制）；之后每次 ListTools 由 gate.sync 重新评估并切换分组，
	// 会话内动态生效、无需重启。工具自身还有执行期陈旧度守卫（第二道防线）。
	gate := newCodeToolGate(codeResolver, runtimeConfig.Knowledge, registry)
	for _, spec := range []codeToolSpec{
		{name: "code_search", class: codeToolClassDefinition, tool: tools.NewCodeSearchTool()},
		{name: "code_inspect", class: codeToolClassDefinition, tool: tools.NewCodeInspectTool()},
		{name: "code_navigate", class: codeToolClassDefinition, tool: tools.NewCodeNavigateTool()},
		{name: "code_references", class: codeToolClassRelation, tool: tools.NewCodeReferencesTool()},
		{name: "code_callers", class: codeToolClassRelation, tool: tools.NewCodeCallersTool()},
	} {
		configure(spec.tool)
		gate.add(spec.name, spec.class, spec.tool)
	}
	gate.sync(context.Background())
	return gate
}

func shouldRegisterOpenAIImageGenerateTool(runtimeConfig *runtimecfg.RuntimeConfig) bool {
	globalCfg := agentconfig.GetGlobalConfig()
	if globalCfg == nil {
		return false
	}
	resolvedRuntime := runtimeConfig
	if resolvedRuntime == nil {
		resolvedRuntime = runtimecfg.DefaultRuntimeConfig()
	}
	defaultModel := strings.TrimSpace(resolvedRuntime.Images.Generations.DefaultModel)
	if defaultModel != "" {
		if _, err := agentconfig.SelectImagesGenerationsProvider(globalCfg, agentconfig.ImagesGenerationsHint{Model: defaultModel}); err == nil {
			return true
		}
	}
	_, err := agentconfig.SelectImagesGenerationsProvider(globalCfg, agentconfig.ImagesGenerationsHint{})
	return err == nil
}

func formatToolkitResult(result *toolkit.ToolResult) (string, map[string]interface{}, error) {
	if result == nil {
		return "", nil, nil
	}
	metadata := result.MetadataWithOutputKind()
	if result.Error != nil {
		return result.Content, metadata, result.Error
	}
	if result.Success {
		mutationSummary := func() string {
			if strings.TrimSpace(result.Content) != "" {
				return ""
			}
			return toolresult.MutationSummary(metadata)
		}
		switch result.NormalizedOutputKind() {
		case toolresult.KindText:
			if result.Content != "" {
				return result.Content, metadata, nil
			}
			if summary := mutationSummary(); summary != "" {
				return summary, metadata, nil
			}
			return "", metadata, nil
		case toolresult.KindEmpty:
			if summary := mutationSummary(); summary != "" {
				return summary, metadata, nil
			}
			return "", metadata, nil
		case toolresult.KindStructured:
			if strings.TrimSpace(result.Content) != "" {
				return result.Content, metadata, nil
			}
			if data, err := result.ToJSON(); err == nil && len(data) > 0 {
				return string(data), metadata, nil
			}
			return "", metadata, nil
		case toolresult.KindBinary:
			if data, err := result.ToJSON(); err == nil && len(data) > 0 {
				return string(data), metadata, nil
			}
			return "", metadata, nil
		}
		if data, err := result.ToJSON(); err == nil && len(data) > 0 {
			return string(data), metadata, nil
		}
		if summary := mutationSummary(); summary != "" {
			return summary, metadata, nil
		}
		return "", metadata, nil
	}
	return "", metadata, fmt.Errorf("tool execution failed")
}

func formatToolkitResultWithSource(result *toolkit.ToolResult, source string) (string, map[string]interface{}, error) {
	output, metadata, err := formatToolkitResult(result)
	return output, toolresult.WithSource(metadata, source), err
}

func formatMCPResult(result *protocol.CallToolResult) (string, map[string]interface{}, error) {
	if result == nil {
		return "", nil, nil
	}
	metadata := cloneMetadataMap(result.Meta)
	if kind := toolresult.KindFromMetadata(metadata); kind != "" {
		metadata = toolresult.WithKind(metadata, kind)
	}
	if len(result.Content) == 1 && result.Content[0].Type == "text" {
		text := result.Content[0].Text
		if result.IsError {
			if strings.TrimSpace(text) == "" {
				return "", metadata, errors.New("tool execution failed")
			}
			return text, metadata, errors.New(text)
		}
		return text, metadata, nil
	}

	var output strings.Builder
	for _, content := range result.Content {
		switch content.Type {
		case "text":
			output.WriteString(content.Text)
			output.WriteString("\n")
		case "image":
			output.WriteString(fmt.Sprintf("image: %s\n", content.MIMEType))
		case "resource":
			output.WriteString(fmt.Sprintf("resource: %s\n", content.URI))
		default:
			output.WriteString(fmt.Sprintf("content: %s\n", content.Type))
		}
	}
	rendered := strings.TrimSpace(output.String())

	if result.IsError {
		if rendered == "" {
			return "", metadata, errors.New("tool execution failed")
		}
		return rendered, metadata, errors.New(rendered)
	}
	return rendered, metadata, nil
}

func formatMCPResultWithSource(result *protocol.CallToolResult, source string) (string, map[string]interface{}, error) {
	output, metadata, err := formatMCPResult(result)
	return output, toolresult.WithSource(metadata, source), err
}

func cloneMetadataMap(input map[string]any) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func withMCPIdentity(metadata map[string]interface{}, info *mcpregistry.ToolInfo, callableName string) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if info == nil || info.Tool == nil {
		return metadata
	}
	metadata["mcp_name"] = info.MCPName
	metadata["mcp_raw_tool_name"] = info.Tool.Name
	metadata["mcp_canonical_name"] = mcpregistry.CanonicalToolName(info.MCPName, info.Tool.Name)
	metadata["tool_callable_name"] = strings.TrimSpace(callableName)
	return metadata
}

func (m *Manager) metaToolDescriptor() *ToolDescriptor {
	meta := adapter.BuildMCPMetaTools()
	if len(meta) == 0 {
		return nil
	}
	tool := meta[0]
	name, _ := tool["name"].(string)
	if name == "" {
		return nil
	}
	desc, _ := tool["description"].(string)
	params, _ := tool["parameters"].(map[string]interface{})
	return &ToolDescriptor{
		Name:        name,
		Description: desc,
		Parameters:  normalizeParameters(params),
	}
}

func (m *Manager) listMCPResources(ctx context.Context, args map[string]interface{}) (string, error) {
	// A mistyped server/cursor kind used to be dropped silently, which turned a
	// request for one server's resources into "list everything" and a paging
	// request into "start from page one". Reject the call instead.
	if raw, present := args["server"]; present && raw != nil {
		if _, ok := raw.(string); !ok {
			return "", fmt.Errorf("list_mcp_resources server must be a string, got %T (%v); omit it to list every enabled server", raw, raw)
		}
	}
	if raw, present := args["cursor"]; present && raw != nil {
		if _, ok := raw.(string); !ok {
			return "", fmt.Errorf("list_mcp_resources cursor must be a string, got %T (%v); omit it to start from the first page", raw, raw)
		}
	}

	if m.mcp == nil {
		payload := map[string]interface{}{
			"servers": map[string]interface{}{},
		}
		data, _ := json.Marshal(payload)
		return string(data), nil
	}

	var server string
	if raw, ok := args["server"].(string); ok {
		server = strings.TrimSpace(raw)
	}

	var cursor *string
	if raw, ok := args["cursor"].(string); ok {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			cursor = &trimmed
		}
	}

	if server != "" {
		result, err := m.mcp.ListResources(ctx, server, cursor)
		if err != nil {
			return "", err
		}
		payload := map[string]interface{}{
			"server":    server,
			"resources": formatResources(result.Resources),
		}
		if result.NextCursor != nil {
			payload["next_cursor"] = *result.NextCursor
		}
		data, _ := json.Marshal(payload)
		return string(data), nil
	}

	servers := make(map[string]interface{})
	for _, status := range m.mcp.ListMCPs() {
		if status == nil || !status.Enabled || status.Name == "" {
			continue
		}
		result, err := m.mcp.ListResources(ctx, status.Name, nil)
		entry := map[string]interface{}{}
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["resources"] = formatResources(result.Resources)
			if result.NextCursor != nil {
				entry["next_cursor"] = *result.NextCursor
			}
		}
		servers[status.Name] = entry
	}

	payload := map[string]interface{}{
		"servers": servers,
	}
	if cursor != nil {
		payload["warning"] = "cursor is only supported when server is specified"
	}

	data, _ := json.Marshal(payload)
	return string(data), nil
}

func formatResources(resources []*protocol.Resource) []map[string]interface{} {
	if len(resources) == 0 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, len(resources))
	for _, res := range resources {
		if res == nil {
			continue
		}
		item := map[string]interface{}{
			"uri":  res.URI,
			"name": res.Name,
		}
		if res.Description != "" {
			item["description"] = res.Description
		}
		if res.MIMEType != "" {
			item["mimeType"] = res.MIMEType
		}
		if res.Annotations != nil {
			item["annotations"] = res.Annotations
		}
		out = append(out, item)
	}
	return out
}

func normalizeParameters(schema map[string]interface{}) map[string]interface{} {
	normalized, _, err := toolschema.Canonicalize(schema)
	if err == nil {
		return normalized
	}
	// Definitions from trusted legacy adapters may still be malformed. Preserve
	// their shape for diagnostics; external MCP definitions are rejected by the
	// registry before reaching this projection.
	fallback := cloneMetadataMap(schema)
	if fallback == nil {
		fallback = map[string]interface{}{}
	}
	return fallback
}

// ---- ADR-0004：code.* 工具面的陈旧度分级注册（§4.1）与描述变体（§4.3）----
//
// 机制说明（ADR 证据 1 勘误）：本仓库不存在 RegisterGroup(name, active)；
// 既有机制是 registerBuiltinToolkitTools 里的构造期条件注册（manager.go:442）。
// 本闸门复用它做初始档位，并在每次 ListTools 前用 Registry.Register/Unregister
// 做会话内动态切换（heartbeat/写事务推进后无需重启会话即可换档）。

// code.* 的两类工具面（ADR-0004 §1.2 的风险划分）：
//
//	definition = 定义类（陈旧只导致显式漏检，模型可回退 grep）
//	relation   = 关系类（陈旧会产生"没有调用者"式静默错误，必须硬约束）
const (
	codeToolClassDefinition = "definition"
	codeToolClassRelation   = "relation"
)

// codeToolSpec 是一个受分级管辖的 code.* 工具。
type codeToolSpec struct {
	name  string
	class string
	tool  toolkit.Tool
}

// codeToolGate 是 code.* 的分级注册闸门。
//
// 线程安全：sync/descriptionSuffix/toolVisible 都持锁；registry 自身有锁，
// 两者嵌套顺序固定为 gate.mu → registry.mu，不会反向。
type codeToolGate struct {
	resolver tools.CodeIndexResolver
	config   knowledge.Config
	registry *toolkit.Registry

	mu         sync.Mutex
	specs      []codeToolSpec
	classes    map[string]string
	registered map[string]bool
	tier       string
	staleness  int64
}

func newCodeToolGate(resolver tools.CodeIndexResolver, cfg knowledge.Config, registry *toolkit.Registry) *codeToolGate {
	return &codeToolGate{
		resolver:   resolver,
		config:     cfg,
		registry:   registry,
		classes:    map[string]string{},
		registered: map[string]bool{},
	}
}

func (g *codeToolGate) add(name, class string, tool toolkit.Tool) {
	if g == nil || tool == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.specs = append(g.specs, codeToolSpec{name: name, class: class, tool: tool})
	g.classes[name] = class
}

// evaluate 解析当前档位：
//   - resolver 未装配 / tools.enabled=false → none（逃生舱：索引照跑，不注册工具）；
//   - 索引不可用（库不存在 / 打开失败）→ all：保留既有 Degrade-Not-Fail 工具面，
//     工具执行时各自 fallback 到 grep/view（与 ADR 前行为一致）；
//   - 索引可用 → handle.Tier（resolver 按 writer / reader+S 计算，含 stale_reader 逃生舱）。
func (g *codeToolGate) evaluate(ctx context.Context) (string, *tools.CodeIndexHandle) {
	if g == nil || g.resolver == nil {
		return tools.CodeIndexTierNone, nil
	}
	if !g.config.Tools.ToolsEnabled() {
		return tools.CodeIndexTierNone, nil
	}
	handle, ok := g.resolver(ctx)
	if !ok || handle == nil {
		return tools.CodeIndexTierAll, nil
	}
	tier := handle.Tier
	if tier == "" {
		tier = tools.CodeIndexTierAll
	}
	return tier, handle
}

// sync 重新评估档位并让注册表与档位一致（幂等：档位未变时不触碰注册表，
// 避免无谓的 SchemaRevision 抖动）。
func (g *codeToolGate) sync(ctx context.Context) {
	if g == nil || g.registry == nil {
		return
	}
	tier, handle := g.evaluate(ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tier = tier
	if handle != nil {
		g.staleness = handle.StalenessSeconds
	} else {
		g.staleness = 0
	}
	for _, spec := range g.specs {
		want := codeToolTierIncludes(tier, spec.class)
		if want == g.registered[spec.name] {
			continue
		}
		if want {
			if err := g.registry.Register(spec.tool); err == nil {
				g.registered[spec.name] = true
			}
			continue
		}
		if err := g.registry.Unregister(spec.name); err == nil {
			delete(g.registered, spec.name)
		}
	}
}

// codeToolTierIncludes 报告某类工具在给定档位下是否注册。
func codeToolTierIncludes(tier, class string) bool {
	switch tier {
	case tools.CodeIndexTierAll:
		return true
	case tools.CodeIndexTierDefinitions:
		return class == codeToolClassDefinition
	default:
		return false
	}
}

// descriptionSuffix 实现 ADR-0004 §4.3 的描述变体：仅中等陈旧档
// （definitions）的定义类工具追加陈旧提示，N 为实际陈旧秒数；
// 其他档位返回空串（schema 恒定，仅 description 文本可变）。
func (g *codeToolGate) descriptionSuffix(name string) string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tier != tools.CodeIndexTierDefinitions || g.staleness <= 0 {
		return ""
	}
	if g.classes[name] != codeToolClassDefinition {
		return ""
	}
	return fmt.Sprintf(" 结果来自本地索引快照，可能落后约 %d 秒。若需确认某个符号是否存在或已删除，请用 grep 复核。", g.staleness)
}

// isCodeTool 报告 name 是否受本闸门管辖（含当前未注册的档位）。
func (g *codeToolGate) isCodeTool(name string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.classes[name]
	return ok
}

// toolVisible 报告 name 在当前档位下是否已注册（模型可见）。
func (g *codeToolGate) toolVisible(name string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.registered[name]
}
