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
//  1. Permission hooks → deny | modify(args) then continue (never allow-and-stop)
//  2. ToolExecutionPolicy (capability/tool allow-deny) against final args
//  3. Rule engine → deny > ask > allow
//  4. Remembered grants (never for dangerous tools)
//  5. Taxonomy / shell read-only auto-allow
//  6. permission_mode policy
//  7. Callback override (patched args re-validated against hard constraints)
//  8. Ask handler / headless deny (patched args re-validated against hard constraints)
//
// bypass_permissions may skip ask/grants flow for mode decisions, but MUST NOT
// skip hook denials or hard deny rules / policy denials. Any argument patch
// produced by a hook, callback, or approval must pass hard constraints before
// execution; patches never grant execution on their own.
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
	// autoCapabilities tracks whether capabilities were derived here (vs supplied
	// by the caller). Only auto-derived capabilities are re-resolved after an
	// argument patch so explicit caller capabilities are never silently changed.
	autoCapabilities := len(req.Capabilities) == 0
	if autoCapabilities {
		req.Capabilities = e.resolveCapabilities(req)
	}

	// finalPatchedArgs carries the fully-resolved replacement args
	// (hook → callback → approval) back to the caller for execution.
	var finalPatchedArgs json.RawMessage
	patchApplied := false

	mode := req.Mode
	if mode == "" && e != nil {
		mode = e.Mode
	}
	mode = normalizeMode(mode)
	req.Mode = mode

	// 1) Permission hook — hard deny always wins (including under bypass). An
	// arg modification updates req.Args and continues through static policy,
	// rules, and mode below; it never short-circuits to allow.
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
			patchedArgs, patchErr := ApplyPatchedArgs(req.Args, hookDecision.PatchedPayload)
			if patchErr != nil {
				return withStage(Decision{Type: DecisionDeny, Reason: patchErr.Error()}, StageHooks, "patched_args_invalid"), nil
			}
			req.Args = patchedArgs
			finalPatchedArgs = hookDecision.PatchedPayload
			patchApplied = true
			if autoCapabilities {
				req.Capabilities = e.resolveCapabilities(req)
			}
		}
		req.Metadata = mergeHookMetadata(req.Metadata, hookDecision)
	}

	// 2) Static tool/capability policy — hard deny (evaluated against the
	// possibly hook-modified args).
	if deny := e.validateStaticPolicy(req); deny != nil {
		return *deny, nil
	}

	// 3) Rules — first match wins; deny is hard (not skipped by bypass).
	var decision Decision
	if ruleDecision, matched := e.firstMatchingRule(req); matched {
		decision = ruleDecision
		if decision.Type == DecisionDeny {
			return decision, nil
		}
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
			// A callback patch must not bypass hard constraints; re-validate
			// the replacement args before accepting them.
			updated, deny := e.applyPatchAndRevalidate(req, callbackDecision.PatchedArgs, autoCapabilities)
			if deny != nil {
				return *deny, nil
			}
			req = updated
			finalPatchedArgs = callbackDecision.PatchedArgs
			patchApplied = true
		}
		if callbackDecision.Stage != "" {
			decision.Stage = callbackDecision.Stage
		}
	}

	decision.PatchedArgs = finalPatchedArgs
	if patchApplied {
		// Always return the effective full args, rather than a later patch
		// relative to an earlier patch. The execution caller starts from the
		// original model args and therefore needs one complete replacement.
		if encoded, marshalErr := json.Marshal(req.Args); marshalErr != nil {
			return withStage(Decision{Type: DecisionDeny, Reason: marshalErr.Error()}, StagePolicy, "patched_args_invalid"), nil
		} else {
			decision.PatchedArgs = encoded
		}
	}
	decision = applyRequestHookMetadata(decision, req.Metadata)
	return e.resolveAsk(ctx, decision, req)
}

// resolveCapabilities returns the capabilities for a request using the engine's
// resolver (or the default resolver when none is configured).
func (e *Engine) resolveCapabilities(req EvalRequest) []Capability {
	var resolver CapabilityResolver
	if e != nil {
		resolver = e.CapabilityResolver
	}
	if resolver == nil {
		resolver = DefaultCapabilityResolver{}
	}
	return resolver.Resolve(req)
}

// validateStaticPolicy runs the non-negotiable static tool/capability policy
// (capability scope, tool allow/deny, tool-info governance, and sandbox
// path/URL/command checks in AllowToolCall) against the current request args.
// It returns a deny decision when blocked, or nil when the request passes.
func (e *Engine) validateStaticPolicy(req EvalRequest) *Decision {
	if e == nil || e.Policy == nil {
		return nil
	}
	if err := e.Policy.AllowCapabilities(req.Capabilities); err != nil {
		d := withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error())
		return &d
	}
	if err := e.Policy.AllowTool(req.ToolName); err != nil {
		d := withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error())
		return &d
	}
	if req.ToolInfo != nil {
		if err := e.Policy.AllowToolInfo(*req.ToolInfo); err != nil {
			d := withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error())
			return &d
		}
		if err := e.Policy.AllowToolCall(*req.ToolInfo, req.Args); err != nil {
			d := withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error())
			return &d
		}
	}
	return nil
}

// firstMatchingRule returns the decision for the first matching static rule.
func (e *Engine) firstMatchingRule(req EvalRequest) (Decision, bool) {
	if e == nil {
		return Decision{}, false
	}
	for _, rule := range e.Rules {
		if !rule.Matches(req) {
			continue
		}
		return withStage(Decision{Type: rule.Decision, Reason: rule.Reason}, StageRules, firstNonEmpty(rule.Reason, string(rule.Decision))), true
	}
	return Decision{}, false
}

// validateHardConstraints re-runs only the non-negotiable checks (static
// tool/capability policy incl. sandbox, plus hard deny rules) against a patched
// candidate. It never runs the hook, grants, readonly-auto, permission mode,
// callback, or the ask handler, so it is safe to call after a callback or
// approval argument patch without re-triggering approval prompts.
func (e *Engine) validateHardConstraints(req EvalRequest) *Decision {
	if deny := e.validateStaticPolicy(req); deny != nil {
		return deny
	}
	if ruleDecision, matched := e.firstMatchingRule(req); matched && ruleDecision.Type == DecisionDeny {
		return &ruleDecision
	}
	return nil
}

// applyPatchAndRevalidate decodes a full-replacement arg patch, updates
// req.Args, re-resolves auto-derived capabilities, and enforces hard
// constraints. On failure it returns a deny decision; on success it returns the
// updated request.
func (e *Engine) applyPatchAndRevalidate(req EvalRequest, patched json.RawMessage, autoCapabilities bool) (EvalRequest, *Decision) {
	patchedArgs, err := ApplyPatchedArgs(req.Args, patched)
	if err != nil {
		d := withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, "patched_args_invalid")
		return req, &d
	}
	req.Args = patchedArgs
	if autoCapabilities {
		req.Capabilities = e.resolveCapabilities(req)
	}
	if deny := e.validateHardConstraints(req); deny != nil {
		return req, deny
	}
	return req, nil
}

// ValidateHardConstraints enforces only the non-negotiable checks for an
// already-approved tool call that is being executed or replayed outside the
// normal Evaluate flow (for example, ExecuteApprovedToolCall after a restart).
//
// It honors a permission-hook hard block and applies a single permission-hook
// arg modification, then runs static tool/capability policy (including sandbox
// path/URL/command checks) and hard deny rules. It never runs grants,
// readonly-auto, permission mode, callback, or the ask handler, so it cannot
// re-prompt for approval or loop. When a hook modifies args, the replacement is
// returned via Decision.PatchedArgs.
func (e *Engine) ValidateHardConstraints(ctx context.Context, req EvalRequest) (Decision, error) {
	if e == nil {
		return Decision{Type: DecisionAllow, Stage: StagePolicy, Reason: "no_engine"}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req.ToolName = strings.TrimSpace(req.ToolName)
	if req.ToolName == "" {
		return withStage(Decision{Type: DecisionDeny, Reason: "tool_name_required"}, StagePolicy, "tool_name_required"), nil
	}
	autoCapabilities := len(req.Capabilities) == 0
	if autoCapabilities {
		req.Capabilities = e.resolveCapabilities(req)
	}

	var patched json.RawMessage
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
			updatedArgs, patchErr := ApplyPatchedArgs(req.Args, hookDecision.PatchedPayload)
			if patchErr != nil {
				return withStage(Decision{Type: DecisionDeny, Reason: patchErr.Error()}, StageHooks, "patched_args_invalid"), nil
			}
			req.Args = updatedArgs
			patched = hookDecision.PatchedPayload
			if autoCapabilities {
				req.Capabilities = e.resolveCapabilities(req)
			}
		}
	}

	if deny := e.validateHardConstraints(req); deny != nil {
		return *deny, nil
	}
	return withStage(Decision{Type: DecisionAllow, PatchedArgs: patched, Reason: "hard_constraints_ok"}, StagePolicy, "hard_constraints_ok"), nil
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
			PatchedArgs: decision.PatchedArgs,
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
	// Preserve any hook/callback patch already carried on the decision; the
	// approver may replace it, but a missing approval patch must not silently
	// drop an earlier patch.
	patchedArgs := decision.PatchedArgs
	if len(resp.PatchedArgs) > 0 {
		// An approval-supplied patch must still pass hard constraints (sandbox,
		// capability, tool policy, hard rules) before it can execute. We do not
		// re-enter mode/callback/ask, so this cannot loop or re-prompt.
		revalReq := req
		updatedArgs, patchErr := ApplyPatchedArgs(req.Args, resp.PatchedArgs)
		if patchErr != nil {
			return withStage(Decision{Type: DecisionDeny, Reason: patchErr.Error()}, StageAsk, "patched_args_invalid"), nil
		}
		revalReq.Args = updatedArgs
		if deny := e.validateHardConstraints(revalReq); deny != nil {
			return *deny, nil
		}
		patchedArgs = resp.PatchedArgs
		if encoded, marshalErr := json.Marshal(revalReq.Args); marshalErr != nil {
			return withStage(Decision{Type: DecisionDeny, Reason: marshalErr.Error()}, StageAsk, "patched_args_invalid"), nil
		} else {
			patchedArgs = encoded
		}
	}
	return withStage(Decision{
		Type:        DecisionAllow,
		PatchedArgs: patchedArgs,
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
