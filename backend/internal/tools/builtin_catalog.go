package tools

import (
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// builtinToolkitRegistration is one row of the single builtin construction
// catalog. "Which tools exist" is declared by toolregistry.Entry.ToolkitBuiltin;
// "how to construct them" is answered here, once, for every surface (builtin
// agent, toolctl, compatibility MCP wrappers).
type builtinToolkitRegistration struct {
	// Canonical is the registry entry this row constructs (T6 reconciliation key).
	Canonical string
	// Condition is nil for unconditional registration. A non-nil predicate is a
	// host capability gate (for example an image provider being configured).
	Condition func(*runtimecfg.RuntimeConfig) bool
	// New constructs the tool from host configuration.
	New func(*runtimecfg.RuntimeConfig) toolkit.Tool
}

// BuiltinToolkitRegistration is the exported read-only view of one catalog row.
// Surfaces outside this package use it instead of keeping a second table.
type BuiltinToolkitRegistration struct {
	Canonical string
	Condition func(*runtimecfg.RuntimeConfig) bool
	New       func(*runtimecfg.RuntimeConfig) toolkit.Tool
}

// BuiltinToolkitCatalog returns the single builtin construction catalog.
//
// The returned slice is a copy: callers cannot mutate the process-wide table.
// Rows are ordered only for deterministic debugging; registration order carries
// no observable semantics because toolkit.Registry.List sorts by name.
func BuiltinToolkitCatalog() []BuiltinToolkitRegistration {
	out := make([]BuiltinToolkitRegistration, 0, len(builtinToolkitRegistrations))
	for _, spec := range builtinToolkitRegistrations {
		out = append(out, BuiltinToolkitRegistration{
			Canonical: spec.Canonical,
			Condition: spec.Condition,
			New:       spec.New,
		})
	}
	return out
}

// defaultImageAttachmentTokenDivisor converts the configured prompt budget into
// a per-image attachment ceiling: one image may consume at most a quarter of
// the budget. Without a bound, a single screenshot can dominate the prompt and
// make every compaction replacement exceed the send gate (2026-10-19 incident:
// one 1200x900 PNG priced as ~139k text tokens).
const defaultImageAttachmentTokenDivisor = 4

// NewConfiguredViewTool applies the runtime image-attachment budget to a view
// (or read) tool. A negative context.maxImageAttachmentTokens disables the gate;
// zero derives a quarter of the configured prompt budget.
func NewConfiguredViewTool(view *tools.ViewTool, cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
	if view == nil {
		return nil
	}
	limit := 0
	if cfg != nil {
		limit = cfg.Context.MaxImageAttachmentTokens
		if limit == 0 && cfg.Context.MaxPromptTokens > 0 {
			limit = cfg.Context.MaxPromptTokens / defaultImageAttachmentTokenDivisor
		}
	}
	if limit < 0 {
		limit = 0
	}
	if limit > 0 {
		view.SetImageAttachmentTokenLimit(limit)
	}
	return view
}

// ShouldRegisterOpenAIImageGenerateTool is the host capability gate for the
// image generation tool.
func ShouldRegisterOpenAIImageGenerateTool(runtimeConfig *runtimecfg.RuntimeConfig) bool {
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

// builtinToolkitRegistrations is the process-wide construction table. It is
// defined here so every surface imports the same rows; manager.go and toolctl
// must never duplicate them.
var builtinToolkitRegistrations = []builtinToolkitRegistration{
	// shell is the preferred Codex-aligned surface; bash and
	// execute_shell_command are compatibility aliases with identical execution
	// semantics (including non-zero exits as content results).
	{Canonical: "shell", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewShellTool() }},
	{Canonical: "shell", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewBashTool() }},
	{Canonical: "shell", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewExecuteShellCommandTool() }},
	{Canonical: "aicli_exec", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewAICLIExecTool() }},
	{Canonical: "apply_patch", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewApplyPatchTool() }},
	// read is a compatibility alias of view (same schema/implementation).
	{Canonical: "view", New: func(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
		return NewConfiguredViewTool(tools.NewViewTool(), cfg)
	}},
	{Canonical: "view", New: func(cfg *runtimecfg.RuntimeConfig) toolkit.Tool {
		return NewConfiguredViewTool(tools.NewReadTool(), cfg)
	}},
	{Canonical: "edit", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewEditTool() }},
	{Canonical: "write", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewWriteTool() }},
	{Canonical: "append_write", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewAppendWriteTool() }},
	{Canonical: "glob", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewGlobTool() }},
	// rg is a compatibility alias of grep (same schema/implementation).
	{Canonical: "grep", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewGrepTool() }},
	{Canonical: "grep", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewRGTool() }},
	{Canonical: "list_dir", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewLsTool() }},
	{Canonical: "download", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewDownloadTool() }},
	{Canonical: "fetch", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewFetchTool() }},
	{Canonical: "multiedit", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewMultieditTool() }},
	{Canonical: "todos", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewTodosTool() }},
	{Canonical: "artifact_read", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewArtifactReadTool() }},
	{Canonical: "sourcegraph", New: func(*runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewSourcegraphTool() }},
	{Canonical: "web_search", New: func(cfg *runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewWebSearchToolWithConfig(cfg) }},
	// Image generation is registered only when a provider is configured.
	{
		Canonical: "openai_image_generate",
		Condition: ShouldRegisterOpenAIImageGenerateTool,
		New:       func(cfg *runtimecfg.RuntimeConfig) toolkit.Tool { return tools.NewOpenAIImageGenerateTool(cfg) },
	},
}
