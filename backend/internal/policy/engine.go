package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
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
	// RuleDetail carries the matching specifier detail of a rules-stage
	// decision (e.g. the matched command segment or path) for diagnostics.
	RuleDetail string
	// ExternalDirs carries the directories of an external_dir:admit decision
	// (§4.5) so an approving host can widen the session root set.
	ExternalDirs []string
	// HardAsk marks an ask that bypass_permissions must not resolve: the
	// root/home removal circuit breaker still requires an explicit user
	// decision (or the usual headless deny when no AskHandler exists). Host
	// callbacks cannot downgrade it to an allow.
	HardAsk bool
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
//     5b. Root/home removal circuit breaker (bypass-proof ask; plan/dont-ask deny)
//     5c. Sensitive-write gate (ask in default/accept_edits; plan/dont-ask deny)
//  6. Model plan-entry gate: enter_plan_mode asks for one user confirmation
//     unless Engine.PlanAutoEnterWithoutApproval or the process policy opts in
//     (see PlanEnterToolName / PlanAutoEnterApprovalReason)
//  7. permission_mode policy
//  8. Callback override (patched args re-validated against hard constraints)
//  9. Ask handler / headless deny (patched args re-validated against hard constraints)
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
	// PlanWriteAllowPathsResolved carries the workspace-resolved absolute form of
	// PlanWriteAllowPaths (see planmode.State.WriteAllowPathsResolved). When set
	// it is the authoritative allowlist: the engine matches cleaned absolute
	// paths or separator-boundary directory prefixes only, and a relative target
	// is anchored to the session workspace root carried in the evaluation
	// context. Base-name matches are never accepted in either mode.
	PlanWriteAllowPathsResolved []string
	// PlanAutoEnterWithoutApproval disables the default user-confirmation gate
	// for a model-driven enter_plan_mode call (PlanEnterToolName). The zero
	// value (false) keeps the product default: the request is routed through the
	// existing approval channel (DecisionAsk with Reason =
	// PlanAutoEnterApprovalReason), and a host without an AskHandler denies it
	// with the pipeline's ordinary headless semantics (StageHeadlessDeny,
	// reason=approval_required) — such hosts opt back into autonomous entry with
	// AICLI_PLAN_MODE_MODEL_AUTONOMY=1 (PlanModelAutonomyEnabled).
	//
	// Set it to true only for trusted/autonomous deployments that intentionally
	// restore the legacy behavior. Callers are runtime hosts that build the
	// permission engine from configuration; the field never applies to any other
	// tool, and never to explicit user actions such as `/plan enter` or
	// `--permission-mode plan`.
	PlanAutoEnterWithoutApproval bool
	// DisableShellBreaker turns off the root/home removal circuit breaker
	// (docs/analysis/commandcode-permissions-design-borrowing-20260926.md §4.3).
	// Zero value keeps it enabled; disabling it is only for tests and for hosts
	// that implement an equivalent guard themselves.
	DisableShellBreaker bool
	// DisableSensitiveWriteGate turns off the sensitive-write gate (§4.4),
	// which requires an approval before writing secret material, persistence
	// vectors, VCS metadata, or the runtime's own configuration. Zero value
	// keeps it enabled.
	DisableSensitiveWriteGate bool
	// DisableSafeFileFastPath turns off the accept-edits safe-file-command fast
	// lane (§4.11), which auto-accepts mkdir/touch/cp/mv/rmdir and
	// non-recursive rm when every target stays inside the workspace and is not
	// sensitive. Zero value keeps it enabled.
	DisableSafeFileFastPath bool
	// DisableBypass is the process-level disable_bypass policy (§4.9): requests
	// evaluated under bypass_permissions are downgraded to default mode, so
	// they ask (or headless-deny) instead of running unattended. It is set from
	// the merged permission layers and must also be enforced at mode-switch
	// entry points by the host.
	DisableBypass bool
	// DisableExternalDirGate turns off the external-directory gate (§4.5), which
	// admits path arguments outside the workspace / admitted roots. Zero value
	// keeps it enabled.
	DisableExternalDirGate bool
	// ExternalAllowedRoots statically widens the admitted root set when the host
	// cannot carry them per run; sessions normally pass roots via
	// toolctx.WithAllowedRoots.
	ExternalAllowedRoots []string
	// ExternalReadOnlyRoots lists externally admitted roots whose *reads* are
	// exempt (registered skill/plugin directories). Writes under them still go
	// through the gate.
	ExternalReadOnlyRoots []string
	// ExternalDirTempRoots overrides the silent temp-directory exemption roots
	// of the external-directory gate (§4.5). Nil means the OS temp directory;
	// hosts may append artifact/output roots too.
	ExternalDirTempRoots []string
	// ApproveExternalDir is called with the directories of an approved
	// external_dir:admit decision so the host can add them to the session roots.
	ApproveExternalDir func(dirs []string)
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

// SetPlanWriteAllowPathsResolved replaces the workspace-resolved plan-mode write
// allowlist (absolute paths, workspace-anchored). Empty input clears it, which
// returns matching to the raw PlanWriteAllowPaths relative semantics.
func SetPlanWriteAllowPathsResolved(engine *Engine, paths ...string) {
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
		cleaned = append(cleaned, filepath.Clean(path))
	}
	if len(cleaned) == 0 {
		engine.PlanWriteAllowPathsResolved = nil
		return
	}
	engine.PlanWriteAllowPathsResolved = cleaned
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

	requestedMode := req.Mode
	if strings.TrimSpace(string(requestedMode)) == "" && e != nil {
		requestedMode = e.Mode
	}
	// disable_bypass (§4.9): a bypass request never outranks a policy that
	// forbids bypass — the request is evaluated as default mode instead.
	mode := e.effectiveMode(req)
	if autoCapabilities && normalizeMode(requestedMode) != mode {
		// Capability resolution sees the request, and a bypass request may
		// resolve to no capabilities; re-derive them under the effective
		// (downgraded) mode.
		req.Mode = mode
		req.Capabilities = e.resolveCapabilities(req)
	}
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
	if deny := e.validateStaticPolicy(ctx, req); deny != nil {
		return *deny, nil
	}

	// ruleRoot anchors specifier path patterns (/ and relative) to the same
	// workspace root the executor uses.
	ruleRoot := planWorkspaceRoot(ctx)
	if ruleRoot == "" && e != nil && e.Policy != nil {
		ruleRoot = strings.TrimSpace(e.Policy.PathAnchorRoot)
	}

	// 3) Rules — first match wins; deny is hard (not skipped by bypass).
	var decision Decision
	if ruleDecision, matched := e.firstMatchingRule(ruleRoot, req); matched {
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

	// 4b) External-directory gate (§4.5): a path argument (or shell cwd) outside
	// the workspace and the session's admitted roots is admitted once before the
	// call runs. It sits before the read-only fast lane so external reads are
	// admitted too, and before the mode stage so bypass silently admits while
	// dont_ask stays fail-closed.
	if decision.Type == "" {
		if gate := e.externalDirGate(ctx, req, mode); gate != nil {
			if gate.Type == DecisionDeny {
				return *gate, nil
			}
			decision = *gate
		}
	}

	// 5) Taxonomy / shell read-only auto-allow.
	//
	// enter_plan_mode is skipped while the plan-entry gate below is armed: its
	// taxonomy capabilities (read_only + ask_user) would auto-allow it here and
	// the gate would never see an undecided request. The opt-out paths keep the
	// legacy behavior untouched, because the gate is not armed for them.
	if decision.Type == "" && e != nil && !e.DisableReadOnlyAuto && !e.planEnterGateArmed(req) {
		if auto, reason := e.readOnlyAutoDecision(req); auto {
			decision = withStage(Decision{Type: DecisionAllow, Reason: reason}, StageReadonlyAuto, reason)
		}
	}

	// 5b) Root/home removal circuit breaker. It runs before the mode decision
	// and before ask resolution: bypass_permissions cannot auto-allow a
	// recursive delete of / or the home directory, and plan/dont-ask deny it
	// outright. Rules and grants (steps 3/4) already decided win, which keeps
	// an explicit deny authoritative and lets a remembered read-only grant
	// short-circuit harmlessly.
	if decision.Type == "" && e != nil && !e.DisableShellBreaker {
		if commands := shellBreakerCommands(req); len(commands) > 0 {
			if finding, risky := shellrisk.AssessCommands(commands).First(); risky {
				reason := "shell_breaker:" + string(finding.Risk)
				if mode == ModePlan || mode == ModeDontAsk {
					decision = withStage(Decision{Type: DecisionDeny, Reason: reason}, StageShellBreaker, reason)
				} else {
					decision = withStage(Decision{Type: DecisionAsk, Reason: reason, HardAsk: true}, StageShellBreaker, reason)
				}
			}
		}
	}

	// 5c) Sensitive-write gate. Writing secret material, persistence vectors,
	// VCS metadata, or the runtime's own configuration needs an explicit
	// approval in default/accept_edits. plan mode keeps its own write
	// allowlist (step 7) and bypass skips the gate, mirroring CommandCode's
	// decision ladder. A rule or grant that already allowed this tool call
	// short-circuits the gate (content-level granularity arrives with the
	// specifier syntax, §4.1).
	if decision.Type == "" && e != nil && !e.DisableSensitiveWriteGate {
		if targets := e.sensitiveWriteTargets(ctx, req); len(targets) > 0 {
			reason := "sensitive_write:" + string(targets[0].Match.Kind)
			switch mode {
			case ModePlan, ModeBypassPermissions:
				// plan: plan write allowlist governs; bypass: allow via mode.
			case ModeDontAsk:
				decision = withStage(Decision{Type: DecisionDeny, Reason: reason}, StageSensitiveWrite, reason)
			default:
				decision = withStage(Decision{Type: DecisionAsk, Reason: reason}, StageSensitiveWrite, reason)
			}
		}
	}

	// 6) Model plan-entry gate: switching into plan mode is a change the user
	// confirms once. Rules (step 3) and grants (step 4) already decided win —
	// this only fires on a still-undecided request — and plan mode already being
	// in effect is a no-op re-entry that must not prompt again. bypass_permissions
	// adds no special case here: the ask flows into resolveAsk, which keeps its
	// existing bypass → allow semantics. Without an AskHandler, resolveAsk turns
	// the ask into the pipeline's headless deny (StageHeadlessDeny,
	// reason=approval_required), which is the intended behavior for
	// non-interactive hosts; AICLI_PLAN_MODE_MODEL_AUTONOMY=1 or
	// Engine.PlanAutoEnterWithoutApproval restores autonomous entry.
	if decision.Type == "" && e.planEnterNeedsApproval(req, mode) {
		// withStage composes "stage:reason"; PlanAutoEnterApprovalReason is a
		// public contract hosts match on, so pin it verbatim (same pattern as the
		// permission_mode branch below).
		decision = withStage(Decision{Type: DecisionAsk}, StageMode, "")
		decision.Reason = PlanAutoEnterApprovalReason
	}

	// 7) permission_mode policy when still undecided.
	if decision.Type == "" {
		// Plan-mode write path pre-check: when PlanWriteAllowPaths is set, CapWriteFS
		// is allowed only for matching paths; otherwise keep legacy mode deny.
		if mode == ModePlan && hasCapability(req.Capabilities, CapWriteFS) {
			if e != nil && (len(e.PlanWriteAllowPaths) > 0 || len(e.PlanWriteAllowPathsResolved) > 0) {
				if e.planWriteAllowed(ctx, req) {
					decision = withStage(Decision{
						Type:   DecisionAllow,
						Reason: "plan_mode_write_path_allowed",
					}, StageMode, "plan_mode_write_path_allowed")
				} else {
					decision = withStage(Decision{Type: DecisionDeny, Reason: "plan_mode_write_path_not_allowed"}, StageMode, "plan_mode_write_path_not_allowed")
				}
			}
		}
		// Accept-edits safe-file-command fast lane (§4.11): mkdir/touch/cp/mv/
		// rmdir and non-recursive rm are auto-accepted when every target stays
		// inside the workspace and is not sensitive. Recursive deletes, targets
		// outside the workspace, sensitive targets, and unresolvable commands
		// keep the normal ask behavior.
		if decision.Type == "" && mode == ModeAcceptEdits && e != nil && !e.DisableSafeFileFastPath &&
			hasCapability(req.Capabilities, CapExecShell) {
			if commands := shellBreakerCommands(req); len(commands) > 0 {
				if allowed, detail := acceptEditsSafeFileAllowed(commands, ruleRoot); allowed {
					decision = withStage(Decision{
						Type:       DecisionAllow,
						Reason:     "mode:accept_edits_safe_file",
						RuleDetail: detail,
					}, StageMode, "accept_edits_safe_file:"+detail)
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
			} else if decision.Type == DecisionDeny && mode == ModeDontAsk {
				decision.Reason = "mode:dont_ask_denies_unapproved"
			}
		}
	}

	// A hard ask (root/home removal breaker) survives callback overrides: a
	// host callback may deny or re-ask it, but cannot turn it into an allow.
	hardAsk := decision.HardAsk
	hardAskReason := decision.Reason

	// 8) Callback override.
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
			updated, deny := e.applyPatchAndRevalidate(ctx, req, callbackDecision.PatchedArgs, autoCapabilities)
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
		if hardAsk && decision.Type == DecisionAllow {
			decision = withStage(Decision{Type: DecisionAsk, Reason: hardAskReason, HardAsk: true}, StageShellBreaker, hardAskReason)
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

// effectiveMode returns the permission mode a request is actually evaluated
// under: the request mode when set, otherwise the engine mode, with the
// process-level disable_bypass policy (§4.9) downgrading bypass to default.
func (e *Engine) effectiveMode(req EvalRequest) Mode {
	mode := req.Mode
	if strings.TrimSpace(string(mode)) == "" && e != nil {
		mode = e.Mode
	}
	mode = normalizeMode(mode)
	if e != nil && e.DisableBypass && mode == ModeBypassPermissions {
		return ModeDefault
	}
	return mode
}

// validateStaticPolicy runs the non-negotiable static tool/capability policy
// (capability scope, tool allow/deny, tool-info governance, and sandbox
// path/URL/command checks in AllowToolCall) against the current request args.
// It returns a deny decision when blocked, or nil when the request passes.
func (e *Engine) validateStaticPolicy(ctx context.Context, req EvalRequest) *Decision {
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
		// The session-bound workspace root travels in ctx so path checks cover
		// the same file the executor will touch.
		if err := e.Policy.AllowToolCallWithContext(ctx, *req.ToolInfo, req.Args); err != nil {
			d := withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, err.Error())
			return &d
		}
	}
	return nil
}

// firstMatchingRule returns the decision for the first matching static rule.
// root anchors specifier path patterns to the session workspace.
func (e *Engine) firstMatchingRule(root string, req EvalRequest) (Decision, bool) {
	if e == nil {
		return Decision{}, false
	}
	for _, rule := range e.Rules {
		matched, detail := rule.MatchesRequest(root, req)
		if !matched {
			continue
		}
		decision := Decision{Type: rule.Decision, Reason: rule.Reason, RuleDetail: detail}
		return withStage(decision, StageRules, firstNonEmpty(rule.Reason, string(rule.Decision))), true
	}
	return Decision{}, false
}

// validateHardConstraints re-runs only the non-negotiable checks (static
// tool/capability policy incl. sandbox, plus hard deny rules) against a patched
// candidate. It never runs the hook, grants, readonly-auto, permission mode,
// callback, or the ask handler, so it is safe to call after a callback or
// approval argument patch without re-triggering approval prompts.
func (e *Engine) validateHardConstraints(ctx context.Context, req EvalRequest) *Decision {
	if deny := e.validateStaticPolicy(ctx, req); deny != nil {
		return deny
	}
	root := planWorkspaceRoot(ctx)
	if root == "" && e != nil && e.Policy != nil {
		root = strings.TrimSpace(e.Policy.PathAnchorRoot)
	}
	if ruleDecision, matched := e.firstMatchingRule(root, req); matched && ruleDecision.Type == DecisionDeny {
		return &ruleDecision
	}
	return nil
}

// applyPatchAndRevalidate decodes a full-replacement arg patch, updates
// req.Args, re-resolves auto-derived capabilities, and enforces hard
// constraints. On failure it returns a deny decision; on success it returns the
// updated request.
func (e *Engine) applyPatchAndRevalidate(ctx context.Context, req EvalRequest, patched json.RawMessage, autoCapabilities bool) (EvalRequest, *Decision) {
	patchedArgs, err := ApplyPatchedArgs(req.Args, patched)
	if err != nil {
		d := withStage(Decision{Type: DecisionDeny, Reason: err.Error()}, StagePolicy, "patched_args_invalid")
		return req, &d
	}
	req.Args = patchedArgs
	if autoCapabilities {
		req.Capabilities = e.resolveCapabilities(req)
	}
	if deny := e.validateHardConstraints(ctx, req); deny != nil {
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

	if deny := e.validateHardConstraints(ctx, req); deny != nil {
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

// shellBreakerCommands extracts every shell command a request would execute:
// the single command/args form and the structured commands batch. Non-shell
// tools yield nil.
func shellBreakerCommands(req EvalRequest) []string {
	if !IsShellLikeToolName(req.ToolName) {
		return nil
	}
	var commands []string
	if command := ExtractShellCommand(req.Args); strings.TrimSpace(command) != "" {
		commands = append(commands, command)
	}
	if raw, ok := req.Args["commands"]; ok {
		commands = append(commands, shellCommandList(raw)...)
	}
	return commands
}

// shellCommandList flattens the structured commands argument into command
// strings, accepting both []string and the provider-shaped []interface{}
// entries.
func shellCommandList(raw interface{}) []string {
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			switch value := item.(type) {
			case string:
				out = append(out, value)
			case map[string]interface{}:
				if command, ok := firstStringArg(value, "command", "cmd"); ok {
					out = append(out, command)
				}
			}
		}
		return out
	default:
		return nil
	}
}

// sensitiveWriteTarget is one write target that matched the sensitive-path
// classifier.
type sensitiveWriteTarget struct {
	Path  string
	Match SensitivePathMatch
}

// sensitiveWriteTargets returns the sensitive targets of a file-mutating call,
// resolved against the same workspace root the executor uses. A call whose
// targets cannot be extracted yields nil; the static policy and plan gate stay
// responsible for those.
func (e *Engine) sensitiveWriteTargets(ctx context.Context, req EvalRequest) []sensitiveWriteTarget {
	if !callMutatesFiles(req) {
		return nil
	}
	paths := collectPathArgs(req.Args, policyArgKeysForTool(req.ToolName))
	if len(paths) == 0 {
		return nil
	}
	root := ""
	if ctx != nil {
		root = strings.TrimSpace(toolctx.WorkspaceRoot(ctx))
	}
	if root == "" && e != nil && e.Policy != nil {
		root = strings.TrimSpace(e.Policy.PathAnchorRoot)
	}
	targets := make([]sensitiveWriteTarget, 0, len(paths))
	for _, path := range paths {
		resolved := resolveSensitiveTarget(root, path)
		if match, ok := ClassifySensitivePath(resolved); ok {
			targets = append(targets, sensitiveWriteTarget{Path: resolved, Match: match})
		}
	}
	return targets
}

// callMutatesFiles reports whether a request can write files: write-like tool
// names or the CapWriteFS capability (taxonomy-derived for file tools).
func callMutatesFiles(req EvalRequest) bool {
	if IsWriteLikeToolName(req.ToolName) {
		return true
	}
	return hasCapability(req.Capabilities, CapWriteFS)
}

// resolveSensitiveTarget anchors a relative target to the workspace root the
// same way the executor does; absolute paths are returned unchanged.
func resolveSensitiveTarget(root, target string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" || root == "" || filepath.IsAbs(trimmed) {
		return trimmed
	}
	if !filepath.IsAbs(root) {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return trimmed
		}
		root = absolute
	}
	return filepath.Clean(filepath.Join(root, trimmed))
}

// planWriteAllowed applies the plan-mode write allowlist to one request.
//
// Every write target of the call must be covered (see planWriteTargetsAllowed):
// file tools contribute their path arguments and apply_patch contributes each
// parsed patch header, so one patch cannot smuggle a second file past the
// allowlist. A call whose targets cannot be extracted is denied. Matching is
// cleaned-path equality or a separator-boundary directory prefix; base-name
// equality is not accepted.
func (e *Engine) planWriteAllowed(ctx context.Context, req EvalRequest) bool {
	if e == nil {
		return false
	}
	// If no allow paths configured, keep legacy modeDecision behavior (deny writes in plan via mode).
	// Returning true here means "do not extra-deny"; modeDecision still applies when decision empty.
	if len(e.PlanWriteAllowPaths) == 0 && len(e.PlanWriteAllowPathsResolved) == 0 {
		return true
	}
	return planWriteTargetsAllowed(
		req.ToolName,
		req.Args,
		e.PlanWriteAllowPathsResolved,
		e.PlanWriteAllowPaths,
		planWorkspaceRoot(ctx),
	)
}

func (e *Engine) resolveAsk(ctx context.Context, decision Decision, req EvalRequest) (Decision, error) {
	if decision.Type != DecisionAsk {
		return decision, nil
	}
	// dont_ask is the fail-closed unattended mode: a request that would ask
	// (mode ask, ask rule, or the model plan-entry gate) is denied instead of
	// prompting. Reads, read-only shell, grants, and allow rules never reach
	// resolveAsk, so they keep running.
	effectiveMode := e.effectiveMode(req)
	if effectiveMode == ModeDontAsk {
		return withStage(Decision{
			Type:        DecisionDeny,
			Reason:      "mode:dont_ask_denies_unapproved",
			HookMessage: decision.HookMessage,
			HookContext: cloneStringMap(decision.HookContext),
		}, StageMode, "mode:dont_ask_denies_unapproved"), nil
	}
	// bypass should not reach ask (modeDecision returns allow), but be defensive.
	// A HardAsk (root/home removal breaker) is deliberately *not* resolvable by
	// bypass_permissions: it still requires an explicit user decision.
	if !decision.HardAsk && effectiveMode == ModeBypassPermissions {
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
	// An approved external-directory ask admits the directories into the session
	// root set (§4.5) before the call continues.
	if len(decision.ExternalDirs) > 0 {
		e.approveExternalDirs(decision.ExternalDirs)
	}
	// Optionally remember grant when requested and not dangerous. Hard asks are
	// one-shot by design: a breaker confirmation must never be remembered.
	if resp.Remember && !decision.HardAsk && e.Grants != nil && !IsDangerousTool(req.ToolName) {
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
		if deny := e.validateHardConstraints(ctx, revalReq); deny != nil {
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
		// Carry the admitted directories so the caller can audit an
		// external_dir:admit approval (§4.5).
		ExternalDirs: decision.ExternalDirs,
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
