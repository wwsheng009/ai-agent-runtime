package compactruntime

import (
	"context"
	"math"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	ModeLocal  = "local"
	ModeRemote = "remote"
	ModeAuto   = "auto"

	PhasePreTurn = "pre_turn"

	defaultAutoCompactRatio   = 0.9
	defaultKeepRecentMessages = 8
)

// TokenCounter estimates message token usage for trigger decisions.
type TokenCounter func([]types.Message) int

// Request describes one auto compact evaluation.
type Request struct {
	SessionID          string
	TaskID             string
	Provider           string
	Model              string
	Mode               string
	Force              bool
	History            []types.Message
	KeepRecentMessages int
	Phase              string
	CountTokens        TokenCounter
}

// Result captures a successful history replacement.
type Result struct {
	Mode               string
	Phase              string
	ResolvedProvider   string
	ResolvedModel      string
	TriggerTokenLimit  int
	MaxContextTokens   int
	TokenBefore        int
	TokenAfter         int
	CompactedMessages  int
	CheckpointIDs      []string
	ReplacementHistory []types.Message
}

// Status captures skip/failure context for observability.
type Status struct {
	Mode              string
	Phase             string
	Reason            string
	ResolvedProvider  string
	ResolvedModel     string
	TriggerTokenLimit int
	MaxContextTokens  int
	TokenBefore       int
}

// Adapter performs one concrete compaction mode.
type Adapter interface {
	Compact(ctx context.Context, req Request, threshold threshold, counter TokenCounter) (*Result, string, error)
}

// Runtime routes compaction requests to the configured adapter.
type Runtime struct {
	llmRuntime     *llm.LLMRuntime
	contextManager *contextmgr.Manager
	local          Adapter
	remote         Adapter
}

type threshold struct {
	ResolvedProvider  string
	ResolvedModel     string
	Mode              string
	MaxContextTokens  int
	TriggerTokenLimit int
}

// New creates a compaction runtime with local and remote adapter slots.
func New(llmRuntime *llm.LLMRuntime, contextManager *contextmgr.Manager) *Runtime {
	return &Runtime{
		llmRuntime:     llmRuntime,
		contextManager: contextManager,
		local: &LocalAdapter{
			llmRuntime:     llmRuntime,
			contextManager: contextManager,
		},
		remote: &RemoteAdapter{
			llmRuntime: llmRuntime,
		},
	}
}

// MaybeCompact evaluates the current history and runs the selected auto compact mode when needed.
func (r *Runtime) MaybeCompact(ctx context.Context, req Request) (*Result, Status, error) {
	status := Status{
		Mode:  normalizeMode(req.Mode),
		Phase: normalizedPhase(req.Phase),
	}
	if status.Mode == "" {
		status.Mode = ModeLocal
	}
	if len(req.History) == 0 {
		status.Reason = "history_empty"
		return nil, status, nil
	}

	counter := req.CountTokens
	if counter == nil && r != nil && r.llmRuntime != nil {
		counter = r.llmRuntime.CountMessagesTokens
	}
	if counter == nil {
		status.Reason = "token_counter_unavailable"
		return nil, status, nil
	}

	limit, ok := resolveAutoCompactThreshold(r.llmRuntime, req.Provider, req.Model, req.Mode)
	if !ok {
		if req.Force && normalizeMode(req.Mode) == ModeLocal {
			limit = resolveForcedLocalThreshold(r.llmRuntime, req.Provider, req.Model)
			ok = true
		}
	}
	if !ok {
		status.Reason = "missing_model_capability"
		status.ResolvedProvider, status.ResolvedModel = resolveRuntimeProviderModel(r.llmRuntime, req.Provider, req.Model)
		status.TokenBefore = counter(req.History)
		return nil, status, nil
	}
	status.Mode = limit.Mode
	status.ResolvedProvider = limit.ResolvedProvider
	status.ResolvedModel = limit.ResolvedModel
	status.TriggerTokenLimit = limit.TriggerTokenLimit
	status.MaxContextTokens = limit.MaxContextTokens
	status.TokenBefore = counter(req.History)
	if !req.Force && status.TokenBefore <= limit.TriggerTokenLimit {
		status.Reason = "below_limit"
		return nil, status, nil
	}

	if req.KeepRecentMessages <= 0 {
		req.KeepRecentMessages = resolveKeepRecentMessages(r.contextManager)
	}
	if req.KeepRecentMessages <= 0 {
		req.KeepRecentMessages = defaultKeepRecentMessages
	}
	req.Provider = limit.ResolvedProvider
	req.Model = limit.ResolvedModel
	req.Mode = limit.Mode
	req.Phase = status.Phase

	adapter, reason := r.adapterForMode(limit.Mode)
	if adapter == nil {
		status.Reason = reason
		return nil, status, nil
	}

	result, reason, err := adapter.Compact(ctx, req, limit, counter)
	if reason != "" {
		status.Reason = reason
	}
	if result == nil {
		return nil, status, err
	}
	if result.Mode == "" {
		result.Mode = limit.Mode
	}
	return result, status, err
}

func resolveAutoCompactThreshold(runtime *llm.LLMRuntime, providerName, model, requestedMode string) (threshold, bool) {
	resolvedProvider, resolvedModel, capability, ok := llm.ResolveRuntimeModelCapability(runtime, providerName, model)
	if !ok {
		return threshold{}, false
	}

	limit := threshold{
		ResolvedProvider: resolvedProvider,
		ResolvedModel:    resolvedModel,
		Mode:             resolveAutoCompactMode(requestedMode, capability.AutoCompactMode, capability.SupportsRemoteCompact),
		MaxContextTokens: capability.MaxContextTokens,
	}
	if capability.AutoCompactTokenLimit > 0 {
		limit.TriggerTokenLimit = capability.AutoCompactTokenLimit
	}
	if limit.TriggerTokenLimit <= 0 && capability.MaxContextTokens > 0 {
		ratio := capability.AutoCompactRatio
		if ratio <= 0 || ratio >= 1 {
			ratio = defaultAutoCompactRatio
		}
		limit.TriggerTokenLimit = int(math.Floor(float64(capability.MaxContextTokens) * ratio))
	}
	if capability.MaxContextTokens > 0 && limit.TriggerTokenLimit > capability.MaxContextTokens {
		limit.TriggerTokenLimit = capability.MaxContextTokens
	}
	if limit.TriggerTokenLimit <= 0 {
		return threshold{}, false
	}
	return limit, true
}

func resolveForcedLocalThreshold(runtime *llm.LLMRuntime, providerName, model string) threshold {
	resolvedProvider, resolvedModel := resolveRuntimeProviderModel(runtime, providerName, model)
	return threshold{
		ResolvedProvider: strings.TrimSpace(resolvedProvider),
		ResolvedModel:    strings.TrimSpace(resolvedModel),
		Mode:             ModeLocal,
	}
}

func resolveRuntimeProviderModel(runtime *llm.LLMRuntime, providerName, model string) (string, string) {
	resolvedProvider := strings.TrimSpace(providerName)
	if runtime != nil {
		if resolved := runtime.ResolveProviderName(resolvedProvider); resolved != "" {
			resolvedProvider = resolved
		}
		if resolvedProvider == "" {
			resolvedProvider = runtime.ResolveProviderName(model)
		}
		if resolvedProvider == "" {
			resolvedProvider = strings.TrimSpace(runtime.DefaultProvider())
		}
	}

	resolvedModel := strings.TrimSpace(model)
	if resolvedModel == "" && runtime != nil {
		resolvedModel = strings.TrimSpace(runtime.DefaultModel())
	}

	return strings.TrimSpace(resolvedProvider), strings.TrimSpace(resolvedModel)
}

func (r *Runtime) adapterForMode(mode string) (Adapter, string) {
	if r == nil {
		return nil, "compact_runtime_unavailable"
	}
	switch normalizeMode(mode) {
	case "", ModeLocal:
		if r.local == nil {
			return nil, "local_adapter_unavailable"
		}
		return r.local, ""
	case ModeRemote:
		if r.remote == nil {
			return nil, "remote_adapter_unavailable"
		}
		return r.remote, ""
	default:
		return nil, "unsupported_compact_mode"
	}
}

func resolveAutoCompactMode(requestedMode, capabilityMode string, supportsRemote bool) string {
	if mode := normalizeMode(requestedMode); mode != "" && mode != ModeAuto {
		return mode
	}
	if mode := normalizeMode(capabilityMode); mode != "" && mode != ModeAuto {
		return mode
	}
	if supportsRemote {
		return ModeRemote
	}
	return ModeLocal
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ModeLocal:
		return ModeLocal
	case ModeRemote:
		return ModeRemote
	case ModeAuto:
		return ModeAuto
	default:
		return ""
	}
}

func normalizedPhase(phase string) string {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return PhasePreTurn
	}
	return phase
}

func resolveKeepRecentMessages(manager *contextmgr.Manager) int {
	if manager == nil {
		return 0
	}
	if manager.Budget.KeepRecentMessages > 0 {
		return manager.Budget.KeepRecentMessages
	}
	return 0
}
