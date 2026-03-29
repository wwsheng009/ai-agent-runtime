package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-gateway/ai-agent-runtime/internal/hooks"
	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
)

// DecisionType represents a permission decision.
type DecisionType string

const (
	DecisionAllow DecisionType = "allow"
	DecisionDeny  DecisionType = "deny"
	DecisionAsk   DecisionType = "ask"
)

// Decision captures the outcome of policy evaluation.
type Decision struct {
	Type        DecisionType
	Reason      string
	PatchedArgs json.RawMessage
	HookMessage string
	HookContext map[string]string
}

// EvalRequest captures information required to evaluate tool permissions.
type EvalRequest struct {
	SessionID    string
	TraceID      string
	ToolCallID   string
	ToolName     string
	ToolInfo     *skill.ToolInfo
	Args         map[string]interface{}
	Capabilities []Capability
	Mode         Mode
	Metadata     map[string]interface{}
}

// HookDispatcher dispatches hook events for permission checks.
type HookDispatcher interface {
	Dispatch(ctx context.Context, event hooks.Event, payload map[string]interface{}) (hooks.Decision, error)
}

// Engine evaluates tool permissions.
type Engine struct {
	Hooks              HookDispatcher
	Rules              []Rule
	Mode               Mode
	Callback           CanUseToolCallback
	AskHandler         ApprovalHandler
	Policy             *ToolExecutionPolicy
	CapabilityResolver CapabilityResolver
}

// Evaluate performs a permission evaluation for the given request.
func (e *Engine) Evaluate(ctx context.Context, req EvalRequest) (Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	req.ToolName = strings.TrimSpace(req.ToolName)
	if req.ToolName == "" {
		return Decision{Type: DecisionDeny, Reason: "tool_name_required"}, nil
	}
	if len(req.Capabilities) == 0 {
		resolver := e.CapabilityResolver
		if resolver == nil {
			resolver = DefaultCapabilityResolver{}
		}
		req.Capabilities = resolver.Resolve(req)
	}

	if e.Hooks != nil {
		payload := map[string]interface{}{
			"tool_name": req.ToolName,
			"args":      req.Args,
		}
		if req.SessionID != "" {
			payload["session_id"] = req.SessionID
		}
		if req.TraceID != "" {
			payload["trace_id"] = req.TraceID
		}
		if req.ToolInfo != nil {
			payload["mcp_name"] = req.ToolInfo.MCPName
			payload["trust_level"] = req.ToolInfo.MCPTrustLevel
			payload["execution_mode"] = req.ToolInfo.ExecutionMode
		}
		hookDecision, hookErr := e.Hooks.Dispatch(ctx, hooks.EventPermissionRequest, payload)
		if hookErr != nil {
			return Decision{Type: DecisionDeny, Reason: hookErr.Error()}, hookErr
		}
		if hookDecision.Action == hooks.DecisionBlock {
			return Decision{Type: DecisionDeny, Reason: hookDecision.Message}, nil
		}
		if hookDecision.Action == hooks.DecisionModify && len(hookDecision.PatchedPayload) > 0 {
			return Decision{
				Type:        DecisionAllow,
				PatchedArgs: hookDecision.PatchedPayload,
				HookMessage: strings.TrimSpace(hookDecision.Message),
				HookContext: cloneStringMap(hookDecision.ExtraContext),
			}, nil
		}
		req.Metadata = mergeHookMetadata(req.Metadata, hookDecision)
	}

	if e.Policy != nil {
		if err := e.Policy.AllowTool(req.ToolName); err != nil {
			return Decision{Type: DecisionDeny, Reason: err.Error()}, nil
		}
		if req.ToolInfo != nil {
			if err := e.Policy.AllowToolInfo(*req.ToolInfo); err != nil {
				return Decision{Type: DecisionDeny, Reason: err.Error()}, nil
			}
			if err := e.Policy.AllowToolCall(*req.ToolInfo, req.Args); err != nil {
				return Decision{Type: DecisionDeny, Reason: err.Error()}, nil
			}
		}
	}

	decision := Decision{}
	for _, rule := range e.Rules {
		if !rule.Matches(req) {
			continue
		}
		decision = Decision{Type: rule.Decision, Reason: rule.Reason}
		break
	}

	if decision.Type == "" {
		mode := req.Mode
		if mode == "" {
			mode = e.Mode
		}
		decision = Decision{Type: modeDecision(mode, req.Capabilities)}
		if decision.Type == DecisionAsk {
			decision.Reason = "permission_mode_requires_approval"
		}
	}

	if e.Callback != nil {
		callbackDecision, reason, err := e.Callback(ctx, req)
		if err != nil {
			return Decision{Type: DecisionDeny, Reason: err.Error()}, err
		}
		if callbackDecision.Type != "" {
			decision.Type = callbackDecision.Type
		}
		if strings.TrimSpace(reason) != "" {
			decision.Reason = strings.TrimSpace(reason)
		} else if strings.TrimSpace(callbackDecision.Reason) != "" {
			decision.Reason = strings.TrimSpace(callbackDecision.Reason)
		}
		if len(callbackDecision.PatchedArgs) > 0 {
			decision.PatchedArgs = callbackDecision.PatchedArgs
		}
	}

	decision = applyRequestHookMetadata(decision, req.Metadata)
	return e.resolveAsk(ctx, decision, req)
}

func (e *Engine) resolveAsk(ctx context.Context, decision Decision, req EvalRequest) (Decision, error) {
	if decision.Type != DecisionAsk {
		return decision, nil
	}
	if e.AskHandler == nil {
		return Decision{
			Type:        DecisionDeny,
			Reason:      "approval_required",
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, nil
	}
	approvalReq := ApprovalRequest{
		ID:         firstNonEmpty(req.ToolCallID, req.TraceID),
		SessionID:  req.SessionID,
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Reason:     decision.Reason,
		RiskLevel:  riskLevel(req.Capabilities),
	}
	if len(req.Args) > 0 {
		if payload, err := json.Marshal(req.Args); err == nil {
			approvalReq.ArgsJSON = payload
		}
	}
	resp, err := e.AskHandler.RequestApproval(ctx, approvalReq)
	if err != nil {
		return Decision{
			Type:        DecisionDeny,
			Reason:      err.Error(),
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, err
	}
	if !resp.Allowed {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "approval_denied"
		}
		return Decision{
			Type:        DecisionDeny,
			Reason:      reason,
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, nil
	}
	return Decision{
		Type:        DecisionAllow,
		PatchedArgs: resp.PatchedArgs,
		HookMessage: decision.HookMessage,
		HookContext: cloneStringMap(decision.HookContext),
	}, nil
}

func riskLevel(caps []Capability) string {
	high := map[Capability]bool{
		CapWriteFS:            true,
		CapExecShell:          true,
		CapExternalSideEffect: true,
	}
	for _, cap := range caps {
		if high[cap] {
			return "high"
		}
	}
	if hasCapability(caps, CapNetwork) || hasCapability(caps, CapBackgroundTask) {
		return "medium"
	}
	return "low"
}

// ApplyPatchedArgs replaces args if patched payload is provided.
func ApplyPatchedArgs(args map[string]interface{}, patched json.RawMessage) (map[string]interface{}, error) {
	if len(patched) == 0 {
		return args, nil
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(patched, &decoded); err != nil {
		return args, fmt.Errorf("decode patched args: %w", err)
	}
	return decoded, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mergeHookMetadata(metadata map[string]interface{}, hookDecision hooks.Decision) map[string]interface{} {
	if len(hookDecision.ExtraContext) == 0 && strings.TrimSpace(hookDecision.Message) == "" {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]interface{}, 2)
	}
	if strings.TrimSpace(hookDecision.Message) != "" {
		metadata["hook_message"] = strings.TrimSpace(hookDecision.Message)
	}
	if len(hookDecision.ExtraContext) > 0 {
		contextMap := make(map[string]string, len(hookDecision.ExtraContext))
		for key, value := range hookDecision.ExtraContext {
			contextMap[key] = value
		}
		metadata["hook_context"] = contextMap
	}
	return metadata
}

func applyRequestHookMetadata(decision Decision, metadata map[string]interface{}) Decision {
	if len(metadata) == 0 {
		return decision
	}
	if message, ok := metadata["hook_message"].(string); ok && strings.TrimSpace(message) != "" {
		decision.HookMessage = strings.TrimSpace(message)
	}
	if raw, ok := metadata["hook_context"].(map[string]string); ok && len(raw) > 0 {
		decision.HookContext = cloneStringMap(raw)
		return decision
	}
	if raw, ok := metadata["hook_context"].(map[string]interface{}); ok && len(raw) > 0 {
		contextMap := make(map[string]string, len(raw))
		for key, value := range raw {
			text, ok := value.(string)
			if !ok {
				continue
			}
			contextMap[key] = text
		}
		if len(contextMap) > 0 {
			decision.HookContext = contextMap
		}
	}
	return decision
}
