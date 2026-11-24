package runtimehost

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimecontext "github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
)

// BuildContextOptions projects runtime context/memory/knowledge/workspace/
// artifact settings into the agent option map. Both hosts must use this one
// projection so a field that is present in runtime.yaml has the same effect on
// runtime-server and aicli.
func BuildContextOptions(config *runtimecfg.RuntimeConfig, activation *knowledge.Activation) map[string]interface{} {
	if config == nil {
		options := make(map[string]interface{})
		ApplyKnowledgeContextOptions(options, activation)
		if len(options) == 0 {
			return nil
		}
		return options
	}
	options := make(map[string]interface{})
	ApplyMemoryOptions(options, config.Memory)

	ctxCfg := config.Context
	if strings.TrimSpace(ctxCfg.Profile) != "" {
		options["context_profile"] = strings.TrimSpace(ctxCfg.Profile)
	}
	if strings.TrimSpace(ctxCfg.CompactionMode) != "" {
		options["context_compaction_mode"] = strings.TrimSpace(ctxCfg.CompactionMode)
	}
	if strings.TrimSpace(ctxCfg.RecallMode) != "" {
		options["context_recall_mode"] = strings.TrimSpace(ctxCfg.RecallMode)
	}
	if strings.TrimSpace(ctxCfg.ObservationMode) != "" {
		options["context_observation_mode"] = strings.TrimSpace(ctxCfg.ObservationMode)
	}
	if strings.TrimSpace(ctxCfg.WorkspaceMode) != "" {
		options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(ctxCfg.WorkspaceMode))
	}
	if ctxCfg.MinCompactionMessages > 0 {
		options["context_min_compaction_messages"] = ctxCfg.MinCompactionMessages
	}
	if ctxCfg.MinRecallQueryLength > 0 {
		options["context_min_recall_query_length"] = ctxCfg.MinRecallQueryLength
	}
	if ctxCfg.LedgerLoadLimit > 0 {
		options["context_ledger_load_limit"] = ctxCfg.LedgerLoadLimit
	}
	if ctxCfg.MaxPromptTokens > 0 {
		options["context_max_prompt_tokens"] = ctxCfg.MaxPromptTokens
	}
	if ctxCfg.FallbackMaxPromptTokens > 0 {
		options["context_fallback_max_prompt_tokens"] = ctxCfg.FallbackMaxPromptTokens
	}
	if ctxCfg.MaxMessages > 0 {
		options["context_max_messages"] = ctxCfg.MaxMessages
	}
	if ctxCfg.KeepRecentMessages > 0 {
		options["context_keep_recent_messages"] = ctxCfg.KeepRecentMessages
	}
	if ctxCfg.MaxRecallResults > 0 {
		options["context_max_recall_results"] = ctxCfg.MaxRecallResults
	}
	if ctxCfg.MaxObservationItems > 0 {
		options["context_max_observation_items"] = ctxCfg.MaxObservationItems
	}
	if ctxCfg.MaxImageAttachmentTokens != 0 {
		options["context_max_image_attachment_tokens"] = ctxCfg.MaxImageAttachmentTokens
	}

	wsCfg := config.Workspace
	if strings.TrimSpace(ctxCfg.WorkspaceMode) == "" && strings.TrimSpace(wsCfg.Mode) != "" {
		options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(wsCfg.Mode))
	}
	if wsCfg.MaxFileSize > 0 {
		options["workspace_max_file_size"] = wsCfg.MaxFileSize
	}
	if wsCfg.MaxChunkSize > 0 {
		options["workspace_max_chunk_size"] = wsCfg.MaxChunkSize
	}
	if wsCfg.ChunkOverlap > 0 {
		options["workspace_chunk_overlap"] = wsCfg.ChunkOverlap
	}
	if len(wsCfg.Include) > 0 {
		options["workspace_include"] = append([]string(nil), wsCfg.Include...)
	}
	if len(wsCfg.Exclude) > 0 {
		options["workspace_exclude"] = append([]string(nil), wsCfg.Exclude...)
	}
	if path := strings.TrimSpace(config.Artifact.StorePath); path != "" {
		options["artifact_store_path"] = path
	}
	if dsn := strings.TrimSpace(config.Artifact.StoreDSN); dsn != "" {
		options["artifact_store_dsn"] = dsn
	}

	ApplyKnowledgeContextOptions(options, activation)
	if len(options) == 0 {
		return nil
	}
	return options
}

// ApplyContextOptions merges the shared projection into an existing agent
// config without dropping host-owned options (stream, workspace, profile
// context, etc.).
func ApplyContextOptions(config *agent.Config, runtimeConfig *runtimecfg.RuntimeConfig, activation *knowledge.Activation) {
	if config == nil {
		return
	}
	options := BuildContextOptions(runtimeConfig, activation)
	if len(options) == 0 {
		return
	}
	if config.Options == nil {
		config.Options = make(map[string]interface{})
	}
	for key, value := range options {
		config.Options[key] = value
	}
}

// ApplyMemoryOptions writes the cross-session memory v2 options. Callers own
// the destination map; a nil map is a no-op.
func ApplyMemoryOptions(options map[string]interface{}, cfg runtimecfg.MemoryConfig) {
	if options == nil {
		return
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" || mode == "off" {
		return
	}
	memorypolicy.ApplyReadOptions(cfg, options)
	options["memory_mode"] = mode
	options["memory_manual_enabled"] = cfg.Manual.Enabled
	if strings.TrimSpace(cfg.StorePath) != "" {
		options["memory_store_path"] = strings.TrimSpace(cfg.StorePath)
	}
	options["memory_injection_enabled"] = cfg.Injection.Enabled
	options["memory_injection_mode"] = strings.ToLower(strings.TrimSpace(cfg.Injection.Mode))
	options["memory_pinned_budget_ratio"] = cfg.Injection.PinnedBudgetRatio
	options["memory_stage_split"] = cfg.Injection.StageSplit
	options["memory_injection_extended_preset"] = cfg.Injection.ExtendedPreset
	options["memory_injection_budget_core"] = cfg.Injection.Budgets.Core
	options["memory_injection_budget_task"] = cfg.Injection.Budgets.Task
	options["memory_injection_budget_retrieved"] = cfg.Injection.Budgets.Retrieved
	options["memory_injection_budget_procedure"] = cfg.Injection.Budgets.Procedure
	options["memory_injection_budget_reference"] = cfg.Injection.Budgets.Reference
	if cfg.Injection.MaxItems > 0 {
		options["context_max_project_memory"] = cfg.Injection.MaxItems
	}
	if budget := cfg.Injection.EffectiveTokenBudget(); budget > 0 {
		options["context_project_memory_tokens"] = budget
	}
	options["memory_citation_enabled"] = cfg.Feedback.CitationEnabled
	options["memory_exposure_sampling"] = cfg.Feedback.ExposureSampling
	options["memory_recall_trace_enabled"] = cfg.Injection.RecallTrace.Enabled
	options["memory_recall_trace_sample_rate"] = cfg.Injection.RecallTrace.SampleRate
}

// ApplyKnowledgeContextOptions writes the knowledge-layer handles:
//   - mode=on: context_knowledge_mode + context_knowledge_layer;
//   - mode=shadow: only context_knowledge_layer (keep the index fresh without
//     injecting prompt content);
//   - off/nil: no new keys.
func ApplyKnowledgeContextOptions(options map[string]interface{}, activation *knowledge.Activation) {
	if options == nil || activation == nil {
		return
	}
	layer := activation.Layer()
	if layer == nil {
		return
	}
	mode := runtimecontext.KnowledgeModeForLayerMode(layer.Mode())
	if mode == runtimecontext.KnowledgeModeOff {
		if layer.Mode() == knowledge.ModeShadow {
			options["context_knowledge_layer"] = layer
		}
		return
	}
	options["context_knowledge_mode"] = mode
	options["context_knowledge_layer"] = layer
}
