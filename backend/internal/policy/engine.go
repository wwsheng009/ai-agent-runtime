package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
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
	Stage       string
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
//
// Pipeline order (Iteration A productization):
//
//  1. PreToolUse / permission hooks → deny | allow(+patch)
//  2. ToolExecutionPolicy (capability/tool allow-deny)
//  3. Rule engine → deny > ask > allow
//  4. Remembered grants (never for dangerous tools)
//  5. Taxonomy / shell read-only auto-allow
//  6. permission_mode policy
//  7. Callback override
//  8. Ask handler / headless deny
//
// bypass_permissions may skip ask/grants flow for mode decisions, but MUST NOT
// skip hook denials or hard deny rules / policy denials.
type Engine struct {
	Hooks              HookDispatcher
	Rules              []Rule
	Mode               Mode
	Callback           CanUseToolCallback
	AskHandler         ApprovalHandler
	Policy             *ToolExecutionPolicy
	CapabilityResolver CapabilityResolver
	ApprovalTimeout    time.Duration
	// Grants stores remembered allow decisions (optional).
	Grants GrantStore
	// ReadOnlyAuto enables taxonomy/shell read-only auto-allow (default true when unset via nil pointer semantics: use EnableReadOnlyAuto).
	DisableReadOnlyAuto bool
	// PlanWriteAllowPaths restricts write tools under mode=plan to these path prefixes/names (optional).
	PlanWriteAllowPaths []string
}

const DefaultApprovalTimeout = 30 * time.Minute

// DefaultPlanFileName is the conventional plan artifact path for plan mode.
const DefaultPlanFileName = "plan.md"

// DefaultPlanWriteAllowPaths returns the default write allowlist used when
// permission_mode=plan and no custom plan paths were configured.
func DefaultPlanWriteAllowPaths() []string {
	return []string{DefaultPlanFileName}
}

// EnsurePlanWriteAllowPaths sets PlanWriteAllowPaths to the default plan file
// when the engine has none configured. Safe to call repeatedly.
func EnsurePlanWriteAllowPaths(engine *Engine) {
	if engine == nil {
		return
	}
	if len(engine.PlanWriteAllowPaths) > 0 {
		return
	}
	engine.PlanWriteAllowPaths = DefaultPlanWriteAllowPaths()
}

// SetPlanWriteAllowPaths replaces the plan-mode write allowlist.
// Empty input restores the default plan.md allowlist.
func SetPlanWriteAllowPaths(engine *Engine, paths ...string) {
	if engine == nil {
		return
	}
	cleaned := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(path))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, path)
	}
	if len(cleaned) == 0 {
		engine.PlanWriteAllowPaths = DefaultPlanWriteAllowPaths()
		return
	}
	engine.PlanWriteAllowPaths = cleaned
}

// Evaluate performs a permission evaluation for the given request.
func (e *Engine) Evaluate(ctx context.Context, req EvalRequest) (Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	req.ToolName = strings.TrimSpace(req.ToolName)
	if req.ToolName == "" {
		return Decision{Type: DecisionDeny, Reason: "tool_name_required", Stage: StagePolicy}, nil
	}
	if len(req.Capabilities) == 0 {
		resolver := e.CapabilityResolver
		if resolver == nil {
			resolver = DefaultCapabilityResolver{}
		}
		req.Capabilities = resolver.Resolve(req)
	}

	mode := req.Mode
	if mode == "" && e != nil {
		mode = e.Mode
	}
	mode = normalizeMode(mode)
	req.Mode = mode

	// 1) Hooks — hard deny always wins, including under bypass.
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
			return withStage(Decision{Type: DecisionDeny, Reason: hookErr.Error()}, StageHooks, hookErr.Error()), hookErr
		}
		if hookDecision.Action == hooks.DecisionBlock {
			return withStage(Decision{Type: DecisionDeny, Reason: hookDecision.Message}, StageHooks, firstNonEmpty(hookDecision.Message, "hook_denied")), nil
		}
		if hookDecision.Action == hooks.DecisionModify && len(hookDecision.PatchedPayload) > 0 {
			return withStage(Decision{
				Type:        DecisionAllow,
				PatchedArgs: hookDecision.PatchedPayload,
				HookMessage: strings.TrimSpace(hookDecision.Message),
				HookContext: cloneStringMap(hookDecision.ExtraContext),
			}, StageHooks, firstNonEmpty(hookDecision.Message, "hook_modified")), nil
		}
		req.Metadata = mergeHookMetadata(req.Metadata, hookDecision)
	}

	// 2) Static tool/capability policy — hard deny.
	if e.Policy != nil {
		if err := e.Policy.AllowCapabilities(req.Capabilities); err != nil {
			return withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error()), nil
		}
		if err := e.Policy.AllowTool(req.ToolName); err != nil {
			return withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error()), nil
		}
		if req.ToolInfo != nil {
			if err := e.Policy.AllowToolInfo(*req.ToolInfo); err != nil {
				return withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error()), nil
			}
			if err := e.Policy.AllowToolCall(*req.ToolInfo, req.Args); err != nil {
				return withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error()), nil
			}
		}
	}

	// 3) Rules — first match wins; deny is hard (not skipped by bypass).
	var decision Decision
	for _, rule := range e.Rules {
		if !rule.Matches(req) {
			continue
		}
		decision = withStage(Decision{Type: rule.Decision, Reason: rule.Reason}, StageRules, firstNonEmpty(rule.Reason, string(rule.Decision)))
		break
	}
	if decision.Type == DecisionDeny {
		return decision, nil
	}

	// 4) Remembered grants (skipped under bypass — bypass already allows without ask).
	if decision.Type == "" && mode != ModeBypassPermissions && e.Grants != nil {
		if grant, ok := e.Grants.Find(req.ToolName, req.Args); ok && !IsDangerousTool(req.ToolName) {
			decision = withStage(Decision{Type: DecisionAllow, Reason: "remembered_grant"}, StageGrants, firstNonEmpty(grant.Pattern, grant.Tool, "remembered_grant"))
		}
	}

	// 5) Taxonomy / shell read-only auto-allow.
	if decision.Type == "" && e != nil && !e.DisableReadOnlyAuto {
		if auto, reason := e.readOnlyAutoDecision(req); auto {
			decision = withStage(Decision{Type: DecisionAllow, Reason: reason}, StageReadonlyAuto, reason)
		}
	}

	// 6) permission_mode policy when still undecided.
	if decision.Type == "" {
		// Plan-mode write path pre-check: when PlanWriteAllowPaths is set, CapWriteFS
		// is allowed only for matching paths; otherwise keep legacy mode deny.
		if mode == ModePlan && hasCapability(req.Capabilities, CapWriteFS) {
			if e != nil && len(e.PlanWriteAllowPaths) > 0 {
				if e.planWriteAllowed(req) {
					decision = withStage(Decision{
						Type:   DecisionAllow,
						Reason: "plan_mode_write_path_allowed",
					}, StageMode, "plan_mode_write_path_allowed")
				} else {
					decision = withStage(Decision{Type: DecisionDeny, Reason: "plan_mode_write_path_not_allowed"}, StageMode, "plan_mode_write_path_not_allowed")
				}
			}
		}
		if decision.Type == "" {
			decision = withStage(Decision{Type: modeDecision(mode, req.Capabilities)}, StageMode, string(mode))
			if decision.Type == DecisionAsk {
				decision.Reason = "mode:permission_mode_requires_approval"
			} else if decision.Type == DecisionAllow && mode == ModeBypassPermissions {
				decision.Reason = "mode:bypass_permissions"
			} else if decision.Type == DecisionDeny && mode == ModePlan {
				decision.Reason = "mode:plan_denies_non_readonly"
			}
		}
	}

	// 7) Callback override.
	if e.Callback != nil {
		callbackDecision, reason, err := e.Callback(ctx, req)
		if err != nil {
			return withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StageCallback, err.Error()), err
		}
		if callbackDecision.Type != "" {
			decision.Type = callbackDecision.Type
			decision.Stage = StageCallback
		}
		if strings.TrimSpace(reason) != "" {
			decision.Reason = strings.TrimSpace(reason)
			if decision.Stage == "" {
				decision.Stage = StageCallback
			}
		} else if strings.TrimSpace(callbackDecision.Reason) != "" {
			decision.Reason = strings.TrimSpace(callbackDecision.Reason)
			if decision.Stage == "" {
				decision.Stage = StageCallback
			}
		}
		if len(callbackDecision.PatchedArgs) > 0 {
			decision.PatchedArgs = callbackDecision.PatchedArgs
		}
		if callbackDecision.Stage != "" {
			decision.Stage = callbackDecision.Stage
		}
	}

	decision = applyRequestHookMetadata(decision, req.Metadata)
	return e.resolveAsk(ctx, decision, req)
}

func (e *Engine) readOnlyAutoDecision(req EvalRequest) (bool, string) {
	// Pure read-only capability tools auto-allow.
	if len(req.Capabilities) > 0 {
		onlyRead := true
		for _, cap := range req.Capabilities {
			if cap != CapReadOnly && cap != CapAskUser {
				onlyRead = false
				break
			}
		}
		if onlyRead {
			return true, "taxonomy_readonly"
		}
	}

	if tax, ok := ResolveToolTaxonomy(req.ToolName, req.Metadata); ok {
		if tax.ReadOnly && !tax.MutatesFS && tax.Kind != "exec" {
			return true, "taxonomy_readonly:" + tax.Name
		}
	}

	// Shell/bash: allow only when command matches read-only table.
	if IsShellLikeToolName(req.ToolName) {
		cmd := ExtractShellCommand(req.Args)
		if cmd != "" && IsShellReadOnlyCommand(cmd) {
			return true, "shell_readonly"
		}
		// Batch: if commands present and all readonly.
		if raw, ok := req.Args["commands"]; ok {
			if allShellCommandsReadOnly(raw) {
				return true, "shell_readonly_batch"
			}
		}
	}
	return false, ""
}

func allShellCommandsReadOnly(raw interface{}) bool {
	switch typed := raw.(type) {
	case []string:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			if !IsShellReadOnlyCommand(item) {
				return false
			}
		}
		return true
	case []interface{}:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			switch v := item.(type) {
			case string:
				if !IsShellReadOnlyCommand(v) {
					return false
				}
			case map[string]interface{}:
				cmd, _ := firstStringArg(v, "command", "cmd")
				if cmd == "" || !IsShellReadOnlyCommand(cmd) {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (e *Engine) planWriteAllowed(req EvalRequest) bool {
	if e == nil {
		return false
	}
	// If no allow paths configured, keep legacy modeDecision behavior (deny writes in plan via mode).
	// Returning true here means "do not extra-deny"; modeDecision still applies when decision empty.
	if len(e.PlanWriteAllowPaths) == 0 {
		return true
	}
	path, ok := firstStringArg(req.Args, "file_path", "path")
	if !ok {
		// apply_patch may use freeform patch; allow if plan paths include plan.md heuristic later.
		if raw, ok := req.Args["patch"].(string); ok && strings.TrimSpace(raw) != "" {
			lower := strings.ToLower(raw)
			for _, allow := range e.PlanWriteAllowPaths {
				allow = strings.ToLower(strings.TrimSpace(allow))
				if allow != "" && strings.Contains(lower, allow) {
					return true
				}
			}
		}
		return false
	}
	clean := filepath.Clean(path)
	base := strings.ToLower(filepath.Base(clean))
	for _, allow := range e.PlanWriteAllowPaths {
		allow = strings.TrimSpace(allow)
		if allow == "" {
			continue
		}
		if strings.EqualFold(filepath.Base(allow), base) {
			return true
		}
		if strings.HasPrefix(strings.ToLower(clean), strings.ToLower(filepath.Clean(allow))) {
			return true
		}
	}
	return false
}

func (e *Engine) resolveAsk(ctx context.Context, decision Decision, req EvalRequest) (Decision, error) {
	if decision.Type != DecisionAsk {
		return decision, nil
	}
	// bypass should not reach ask (modeDecision returns allow), but be defensive.
	if normalizeMode(req.Mode) == ModeBypassPermissions || normalizeMode(e.Mode) == ModeBypassPermissions {
		return withStage(Decision{
			Type:        DecisionAllow,
			Reason:      "bypass_permissions",
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, StageMode, "bypass_permissions"), nil
	}
	if e.AskHandler == nil {
		return withStage(Decision{
			Type:        DecisionDeny,
			Reason:      "approval_required",
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, StageHeadlessDeny, "approval_required"), nil
	}
	approvalReq := ApprovalRequest{
		ID:         firstNonEmpty(req.ToolCallID, req.TraceID),
		SessionID:  req.SessionID,
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Reason:     decision.Reason,
		RiskLevel:  riskLevel(req.Capabilities),
	}
	approvalTimeout := e.ApprovalTimeout
	if approvalTimeout == 0 {
		approvalTimeout = DefaultApprovalTimeout
	}
	if approvalTimeout > 0 {
		approvalReq.ExpiresAt = time.Now().UTC().Add(approvalTimeout)
	}
	if len(req.Args) > 0 {
		if payload, err := json.Marshal(req.Args); err == nil {
			approvalReq.ArgsJSON = payload
		}
	}
	resp, err := e.AskHandler.RequestApproval(ctx, approvalReq)
	if err != nil {
		return withStage(Decision{
			Type:        DecisionDeny,
			Reason:      err.Error(),
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, StageAsk, err.Error()), err
	}
	if !resp.Allowed {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "approval_denied"
		}
		return withStage(Decision{
			Type:        DecisionDeny,
			Reason:      reason,
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, StageAsk, reason), nil
	}
	// Optionally remember grant when requested and not dangerous.
	if resp.Remember && e.Grants != nil && !IsDangerousTool(req.ToolName) {
		_ = e.Grants.Remember(Grant{Tool: req.ToolName, Scope: "session"})
	}
	return withStage(Decision{
		Type:        DecisionAllow,
		PatchedArgs: resp.PatchedArgs,
		HookMessage: decision.HookMessage,
		HookContext: cloneStringMap(decision.HookContext),
	}, StageAsk, "approved"), nil
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
	if hasCapability(caps, CapNetwork) || hasCapability(caps, CapBackgroundTask) || hasCapability(caps, CapAgentManagement) {
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
