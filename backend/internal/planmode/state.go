// Package planmode implements session plan-mode lifecycle helpers.
//
// MVP scope (Iteration B3):
//   - enter plan mode → active state + plan.md write allow paths
//   - exit plan mode → approve | request_changes | quit
//
// Permission enforcement still lives in policy.Engine (PlanWriteAllowPaths + mode=plan).
// This package owns durable session context keys and transition helpers.
package planmode

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

const (
	// ContextKey is the session context key storing plan-mode state.
	ContextKey = "plan_mode"

	// DefaultPlanPath is the conventional plan artifact path.
	DefaultPlanPath = runtimepolicy.DefaultPlanFileName
)

// Status describes the plan-mode lifecycle phase.
type Status string

const (
	StatusInactive Status = "inactive"
	StatusActive   Status = "active"
	StatusExited   Status = "exited"
)

// ExitDecision is the user/CLI decision when leaving plan mode.
type ExitDecision string

const (
	ExitApprove        ExitDecision = "approve"
	ExitRequestChanges ExitDecision = "request_changes"
	ExitQuit           ExitDecision = "quit"
	ExitNone           ExitDecision = ""
)

// ExitSource identifies who decided (or requested) a plan-mode exit.
type ExitSource string

const (
	ExitSourceUser  ExitSource = "user"
	ExitSourceModel ExitSource = "model"
	ExitSourceNone  ExitSource = ""
)

// State is the durable plan-mode session state.
type State struct {
	Status          Status       `json:"status"`
	PlanPath        string       `json:"plan_path,omitempty"`
	EnteredAt       string       `json:"entered_at,omitempty"`
	ExitedAt        string       `json:"exited_at,omitempty"`
	ExitDecision    ExitDecision `json:"exit_decision,omitempty"`
	PreviousMode    string       `json:"previous_mode,omitempty"`
	Notes           string       `json:"notes,omitempty"`
	WriteAllowPaths []string     `json:"write_allow_paths,omitempty"`
	// WriteAllowPathsResolved is the workspace-anchored absolute form of
	// WriteAllowPaths, filled at enter time when the host knows the session
	// workspace. Permission matching prefers it (cleaned absolute equality or a
	// separator-boundary directory prefix); when it is empty the raw
	// WriteAllowPaths relative semantics apply. Best-effort: resolution never
	// fails an enter.
	WriteAllowPathsResolved []string `json:"write_allow_paths_resolved,omitempty"`
	PendingExitRequest      bool     `json:"pending_exit_request,omitempty"`
	// LastExitSource records who last decided or requested the exit
	// (user|model). It is diagnostic: the decision itself lives in
	// ExitDecision/PendingExitRequest.
	LastExitSource ExitSource `json:"last_exit_source,omitempty"`
	// PendingReviewNotes carries user review feedback (request_changes) that
	// must reach the model on its next turn. It is cleared once delivered.
	PendingReviewNotes string `json:"pending_review_notes,omitempty"`
	// ReviewRound counts completed review rounds for the current plan session.
	ReviewRound int `json:"review_round,omitempty"`
}

// ContextGetter reads a session context value.
type ContextGetter interface {
	GetContext(key string) (interface{}, bool)
}

// ContextSetter writes a session context value.
type ContextSetter interface {
	SetContext(key string, value interface{})
}

// NormalizeStatus returns a canonical status.
func NormalizeStatus(raw string) Status {
	switch Status(strings.ToLower(strings.TrimSpace(raw))) {
	case StatusActive:
		return StatusActive
	case StatusExited:
		return StatusExited
	default:
		return StatusInactive
	}
}

// NormalizeExitDecision parses approve|request_changes|quit.
func NormalizeExitDecision(raw string) (ExitDecision, error) {
	switch ExitDecision(strings.ToLower(strings.TrimSpace(raw))) {
	case ExitApprove, "approved", "yes", "y":
		return ExitApprove, nil
	case ExitRequestChanges, "request-changes", "changes", "revise":
		return ExitRequestChanges, nil
	case ExitQuit, "cancel", "abort", "no", "n":
		return ExitQuit, nil
	case "":
		return ExitNone, fmt.Errorf("planmode: exit decision required (approve|request_changes|quit)")
	default:
		return ExitNone, fmt.Errorf("planmode: invalid exit decision %q (approve|request_changes|quit)", raw)
	}
}

// DefaultWriteAllowPaths returns the default plan write allowlist.
func DefaultWriteAllowPaths() []string {
	return runtimepolicy.DefaultPlanWriteAllowPaths()
}

// NormalizePlanPath cleans a plan path or returns the default.
func NormalizePlanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return DefaultPlanPath
	}
	return path
}

// Load reads plan-mode state from session context.
func Load(getter ContextGetter) State {
	if getter == nil {
		return State{Status: StatusInactive}
	}
	raw, ok := getter.GetContext(ContextKey)
	if !ok || raw == nil {
		return State{Status: StatusInactive}
	}
	switch typed := raw.(type) {
	case State:
		return normalizeState(typed)
	case *State:
		if typed == nil {
			return State{Status: StatusInactive}
		}
		return normalizeState(*typed)
	case map[string]interface{}:
		return normalizeState(stateFromMap(typed))
	default:
		return State{Status: StatusInactive}
	}
}

// Save writes plan-mode state into session context.
func Save(setter ContextSetter, state State) {
	if setter == nil {
		return
	}
	setter.SetContext(ContextKey, normalizeState(state).ToMap())
}

// Clear removes plan-mode state (sets inactive).
func Clear(setter ContextSetter) {
	if setter == nil {
		return
	}
	setter.SetContext(ContextKey, State{Status: StatusInactive}.ToMap())
}

// EnterOptions configures a plan-mode entry.
type EnterOptions struct {
	// PreviousMode is the permission mode to restore when plan mode exits.
	PreviousMode string
	// PlanPath is the primary plan artifact path.
	PlanPath string
	// WriteAllowPaths extends the plan write allowlist. The primary plan path is
	// always unioned in (see Enter).
	WriteAllowPaths []string
	// Workspace is the session workspace root used to resolve WriteAllowPaths
	// into State.WriteAllowPathsResolved. Empty (or unresolvable) input keeps the
	// legacy relative-path semantics: resolved stays empty and the raw allowlist
	// is compared as-is. planmode deliberately does not read the workspace from
	// session metadata; the host passes it in, so this package stays free of a
	// sessionmeta dependency.
	Workspace string
}

// EnterPlan activates plan mode from options, resolving the write allowlist
// against the supplied workspace.
//
// With EnterOptions.Workspace empty this is exactly Enter: no resolved paths are
// recorded and matching falls back to the raw relative allowlist.
func EnterPlan(opts EnterOptions) State {
	state := Enter(opts.PreviousMode, opts.PlanPath, opts.WriteAllowPaths...)
	state.WriteAllowPathsResolved = resolveWriteAllowPaths(state.WriteAllowPaths, opts.Workspace)
	return state
}

// Enter activates plan mode, recording previous permission mode and plan path.
//
// It is the workspace-less entry point; hosts that know the session workspace
// should call EnterPlan so the allowlist is also recorded in its resolved form.
func Enter(previousMode, planPath string, writeAllowPaths ...string) State {
	path := NormalizePlanPath(planPath)
	allows := cleanPaths(writeAllowPaths...)
	if len(allows) == 0 {
		allows = []string{path}
	} else if !pathInList(path, allows) {
		allows = append([]string{path}, allows...)
	}
	prev := strings.TrimSpace(previousMode)
	if prev == "" {
		prev = string(runtimepolicy.ModeDefault)
	}
	return State{
		Status:          StatusActive,
		PlanPath:        path,
		EnteredAt:       time.Now().UTC().Format(time.RFC3339Nano),
		PreviousMode:    prev,
		WriteAllowPaths: allows,
	}
}

// resolveWriteAllowPaths anchors each allowlist entry to the workspace root.
//
// Best-effort by contract: an unknown or unresolvable workspace yields nil (so
// matching keeps the relative fallback), and absolute entries are only cleaned.
// It never returns partial garbage for an entry it cannot resolve: such an entry
// is skipped, and the raw allowlist still covers it.
func resolveWriteAllowPaths(allows []string, workspace string) []string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || len(allows) == 0 {
		return nil
	}
	root := workspace
	if !filepath.IsAbs(root) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil
		}
		root = absRoot
	}
	resolved := make([]string, 0, len(allows))
	seen := map[string]struct{}{}
	for _, allow := range allows {
		allow = strings.TrimSpace(allow)
		if allow == "" {
			continue
		}
		path := allow
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path = filepath.Clean(path)
		key := strings.ToLower(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		resolved = append(resolved, path)
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// RequestExit marks that the agent requested exit approval without deciding yet.
func RequestExit(state State) State {
	state = normalizeState(state)
	if state.Status != StatusActive {
		state.Status = StatusActive
	}
	state.PendingExitRequest = true
	return state
}

// NormalizeExitSource parses user|model. Unknown/empty input falls back to
// user, the historical default for host-driven exit decisions.
func NormalizeExitSource(raw string) ExitSource {
	switch ExitSource(strings.ToLower(strings.TrimSpace(raw))) {
	case ExitSourceModel, "agent", "assistant":
		return ExitSourceModel
	case ExitSourceUser, "host", "human":
		return ExitSourceUser
	default:
		return ExitSourceUser
	}
}

// RequestExitFrom marks an exit request that still needs a user decision while
// recording who asked (model|user).
func RequestExitFrom(state State, source ExitSource, notes string) State {
	state = RequestExit(state)
	state.LastExitSource = NormalizeExitSource(string(source))
	if text := strings.TrimSpace(notes); text != "" {
		state.Notes = text
	}
	return state
}

// ModelMayDecideExit reports whether a model-authored approve/quit may be
// applied directly. Interactive hosts require an explicit user decision;
// headless/autonomous hosts opt in with AICLI_PLAN_MODE_MODEL_AUTONOMY=1.
//
// The switch lives in internal/policy so plan entry (the engine's
// enter_plan_mode confirmation gate) and plan exit share one policy and cannot
// diverge.
func ModelMayDecideExit() bool {
	return runtimepolicy.PlanModelAutonomyEnabled()
}

// RecordReviewNotes stores user review feedback for delivery on the next turn
// and advances the review round.
func RecordReviewNotes(state State, notes string) State {
	state = normalizeState(state)
	if text := strings.TrimSpace(notes); text != "" {
		state.PendingReviewNotes = text
	}
	state.ReviewRound++
	return state
}

// ConsumeReviewNotes returns pending review feedback and clears it, so the
// feedback is delivered exactly once.
func ConsumeReviewNotes(state State) (State, string) {
	state = normalizeState(state)
	notes := strings.TrimSpace(state.PendingReviewNotes)
	state.PendingReviewNotes = ""
	return state, notes
}

// ExitRequested reports whether an exit request awaits a user decision.
func ExitRequested(state State) bool {
	return normalizeState(state).PendingExitRequest
}

// Exit finalizes plan mode with an approve/request_changes/quit decision.
func Exit(state State, decision ExitDecision, notes string) (State, error) {
	state = normalizeState(state)
	normalized, err := NormalizeExitDecision(string(decision))
	if err != nil {
		return state, err
	}
	state.Status = StatusExited
	state.ExitDecision = normalized
	state.ExitedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.PendingExitRequest = false
	if text := strings.TrimSpace(notes); text != "" {
		state.Notes = text
	}
	return state, nil
}

// IsActive reports whether plan mode is currently restricting writes.
func IsActive(state State) bool {
	return normalizeState(state).Status == StatusActive
}

// EffectivePermissionMode returns ModePlan while active; otherwise previous mode.
func EffectivePermissionMode(state State) string {
	state = normalizeState(state)
	if state.Status == StatusActive {
		return string(runtimepolicy.ModePlan)
	}
	if state.PreviousMode != "" {
		return state.PreviousMode
	}
	return string(runtimepolicy.ModeDefault)
}

// ApplyToEngine configures a permission engine for the current plan state.
// When active, forces PlanWriteAllowPaths (and, when the host resolved them, the
// workspace-anchored PlanWriteAllowPathsResolved); when inactive, leaves engine
// untouched except ensuring defaults if already in plan mode elsewhere.
func ApplyToEngine(engine *runtimepolicy.Engine, state State) {
	if engine == nil {
		return
	}
	state = normalizeState(state)
	if state.Status != StatusActive {
		return
	}
	runtimepolicy.SetPlanWriteAllowPaths(engine, state.WriteAllowPaths...)
	runtimepolicy.SetPlanWriteAllowPathsResolved(engine, state.WriteAllowPathsResolved...)
	engine.Mode = runtimepolicy.ModePlan
}

// ResumeModeAfterExit returns the permission mode to restore after exit.
// approve → previous mode (ready to execute)
// request_changes → stay in plan (caller may re-enter)
// quit → previous mode without executing
func ResumeModeAfterExit(state State) string {
	state = normalizeState(state)
	prev := strings.TrimSpace(state.PreviousMode)
	if prev == "" {
		prev = string(runtimepolicy.ModeDefault)
	}
	switch state.ExitDecision {
	case ExitRequestChanges:
		// Stay ready for another plan revision under plan mode.
		return string(runtimepolicy.ModePlan)
	case ExitApprove:
		// Approving a plan must not silently restore a permission-bypass mode:
		// implementation starts in accept_edits so the approval is not also a
		// blanket "no more prompts" switch for the rest of the session.
		if prev == string(runtimepolicy.ModeBypassPermissions) {
			return string(runtimepolicy.ModeAcceptEdits)
		}
		return prev
	default:
		return prev
	}
}

// ToMap serializes state for session context storage.
func (s State) ToMap() map[string]interface{} {
	s = normalizeState(s)
	out := map[string]interface{}{
		"status": string(s.Status),
	}
	if s.PlanPath != "" {
		out["plan_path"] = s.PlanPath
	}
	if s.EnteredAt != "" {
		out["entered_at"] = s.EnteredAt
	}
	if s.ExitedAt != "" {
		out["exited_at"] = s.ExitedAt
	}
	if s.ExitDecision != "" {
		out["exit_decision"] = string(s.ExitDecision)
	}
	if s.PreviousMode != "" {
		out["previous_mode"] = s.PreviousMode
	}
	if s.Notes != "" {
		out["notes"] = s.Notes
	}
	if len(s.WriteAllowPaths) > 0 {
		paths := make([]interface{}, 0, len(s.WriteAllowPaths))
		for _, p := range s.WriteAllowPaths {
			paths = append(paths, p)
		}
		out["write_allow_paths"] = paths
	}
	if len(s.WriteAllowPathsResolved) > 0 {
		paths := make([]interface{}, 0, len(s.WriteAllowPathsResolved))
		for _, p := range s.WriteAllowPathsResolved {
			paths = append(paths, p)
		}
		out["write_allow_paths_resolved"] = paths
	}
	if s.PendingExitRequest {
		out["pending_exit_request"] = true
	}
	if s.LastExitSource != "" {
		out["last_exit_source"] = string(s.LastExitSource)
	}
	if s.PendingReviewNotes != "" {
		out["pending_review_notes"] = s.PendingReviewNotes
	}
	if s.ReviewRound > 0 {
		out["review_round"] = s.ReviewRound
	}
	return out
}

func normalizeState(state State) State {
	state.Status = NormalizeStatus(string(state.Status))
	state.PlanPath = NormalizePlanPath(state.PlanPath)
	state.PreviousMode = strings.TrimSpace(state.PreviousMode)
	state.Notes = strings.TrimSpace(state.Notes)
	state.EnteredAt = strings.TrimSpace(state.EnteredAt)
	state.ExitedAt = strings.TrimSpace(state.ExitedAt)
	if state.ExitDecision != "" {
		if decision, err := NormalizeExitDecision(string(state.ExitDecision)); err == nil {
			state.ExitDecision = decision
		}
	}
	state.PendingReviewNotes = strings.TrimSpace(state.PendingReviewNotes)
	if state.LastExitSource != "" {
		state.LastExitSource = NormalizeExitSource(string(state.LastExitSource))
	}
	if state.ReviewRound < 0 {
		state.ReviewRound = 0
	}
	state.WriteAllowPaths = cleanPaths(state.WriteAllowPaths...)
	state.WriteAllowPathsResolved = cleanPaths(state.WriteAllowPathsResolved...)
	if len(state.WriteAllowPathsResolved) == 0 {
		state.WriteAllowPathsResolved = nil
	}
	if state.Status == StatusActive && len(state.WriteAllowPaths) == 0 {
		state.WriteAllowPaths = []string{state.PlanPath}
	}
	return state
}

func stateFromMap(raw map[string]interface{}) State {
	if raw == nil {
		return State{Status: StatusInactive}
	}
	state := State{
		Status:             NormalizeStatus(fmt.Sprint(raw["status"])),
		PlanPath:           strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "plan_path"))),
		EnteredAt:          strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "entered_at"))),
		ExitedAt:           strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "exited_at"))),
		PreviousMode:       strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "previous_mode"))),
		Notes:              strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "notes"))),
		PendingExitRequest: asBool(raw["pending_exit_request"]),
		PendingReviewNotes: strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "pending_review_notes"))),
		ReviewRound:        asInt(raw["review_round"]),
	}
	if source := strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "last_exit_source"))); source != "" && source != "<nil>" {
		state.LastExitSource = NormalizeExitSource(source)
	}
	if decision := strings.TrimSpace(fmt.Sprint(rawValueOrEmpty(raw, "exit_decision"))); decision != "" && decision != "<nil>" {
		if normalized, err := NormalizeExitDecision(decision); err == nil {
			state.ExitDecision = normalized
		}
	}
	if paths, ok := raw["write_allow_paths"]; ok {
		state.WriteAllowPaths = stringList(paths)
	}
	if paths, ok := raw["write_allow_paths_resolved"]; ok {
		state.WriteAllowPathsResolved = stringList(paths)
	}
	return state
}

func rawValueOrEmpty(raw map[string]interface{}, key string) interface{} {
	if raw == nil {
		return ""
	}
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	return value
}

func asBool(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "y":
			return true
		}
	case float64:
		return typed != 0
	case int:
		return typed != 0
	}
	return false
}

func asInt(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return 0
}

func stringList(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return cleanPaths(typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return cleanPaths(out...)
	case string:
		return cleanPaths(typed)
	default:
		return nil
	}
}

func cleanPaths(paths ...string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || path == "<nil>" {
			continue
		}
		key := strings.ToLower(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, path)
	}
	return out
}

func pathInList(path string, list []string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	for _, item := range list {
		if strings.ToLower(strings.TrimSpace(item)) == path {
			return true
		}
	}
	return false
}
