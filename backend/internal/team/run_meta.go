package team

import "strings"

// TeamRunMeta contains team-specific execution context.
type TeamRunMeta struct {
	TeamID               string   `json:"team_id,omitempty"`
	AgentID              string   `json:"agent_id,omitempty"`
	CurrentTaskID        string   `json:"current_task_id,omitempty"`
	Difficulty           string   `json:"difficulty,omitempty"`
	DifficultySource     string   `json:"difficulty_source,omitempty"`
	DifficultyRationale  string   `json:"difficulty_rationale,omitempty"`
	RouteProvider        string   `json:"route_provider,omitempty"`
	RouteModel           string   `json:"route_model,omitempty"`
	RouteReasoningEffort string   `json:"route_reasoning_effort,omitempty"`
	RouteSource          string   `json:"route_source,omitempty"`
	RouteWarnings        []string `json:"route_warnings,omitempty"`
	RouteFallbackUsed    bool     `json:"fallback_used,omitempty"`
	RouteFallbackReason  string   `json:"fallback_reason,omitempty"`
}

// RunMeta captures the execution context for a session run.
type RunMeta struct {
	PermissionMode string `json:"permission_mode,omitempty"`
	// CompletionRequirement is none|complete_task for worker harness loops.
	// Team task runs default to complete_task when unset at the runner boundary.
	CompletionRequirement string       `json:"completion_requirement,omitempty"`
	Team                  *TeamRunMeta `json:"team,omitempty"`
}

// Clone returns a defensive copy of TeamRunMeta.
func (m *TeamRunMeta) Clone() *TeamRunMeta {
	if m == nil {
		return nil
	}
	clone := *m
	clone.RouteWarnings = append([]string(nil), m.RouteWarnings...)
	return &clone
}

// Clone returns a defensive copy of RunMeta.
func (m *RunMeta) Clone() *RunMeta {
	if m == nil {
		return nil
	}
	return &RunMeta{
		PermissionMode:        m.PermissionMode,
		CompletionRequirement: m.CompletionRequirement,
		Team:                  m.Team.Clone(),
	}
}

// EffectiveCompletionRequirement returns the harness completion mode for a run.
// Only an explicit CompletionRequirement is authoritative. Team worker runs
// must set complete_task at the runner boundary (see TeammateRunner); we do not
// infer complete_task from TeamID/AgentID alone so permission/route team metas
// without an outcome contract stay on none.
func EffectiveCompletionRequirement(runMeta *RunMeta) string {
	if runMeta != nil {
		if value := strings.TrimSpace(runMeta.CompletionRequirement); value != "" {
			return strings.ToLower(value)
		}
	}
	return "none"
}

// TaskExecutionRouteFromRunMeta reconstructs the observable route summary for
// the current task run. It is intended for audit/event propagation only; callers
// must not use it to mutate session defaults or execution permissions.
func TaskExecutionRouteFromRunMeta(runMeta *RunMeta) *TaskExecutionRoute {
	if runMeta == nil || runMeta.Team == nil {
		return nil
	}
	meta := runMeta.Team
	route := &TaskExecutionRoute{
		Difficulty:          strings.TrimSpace(meta.Difficulty),
		DifficultySource:    strings.TrimSpace(meta.DifficultySource),
		DifficultyRationale: strings.TrimSpace(meta.DifficultyRationale),
		Provider:            strings.TrimSpace(meta.RouteProvider),
		Model:               strings.TrimSpace(meta.RouteModel),
		ReasoningEffort:     strings.TrimSpace(meta.RouteReasoningEffort),
		Source:              strings.TrimSpace(meta.RouteSource),
		Warnings:            append([]string(nil), meta.RouteWarnings...),
		FallbackUsed:        meta.RouteFallbackUsed,
		FallbackReason:      strings.TrimSpace(meta.RouteFallbackReason),
	}
	if route.Difficulty == "" &&
		route.DifficultySource == "" &&
		route.DifficultyRationale == "" &&
		route.Provider == "" &&
		route.Model == "" &&
		route.ReasoningEffort == "" &&
		route.Source == "" &&
		len(route.Warnings) == 0 &&
		!route.FallbackUsed &&
		route.FallbackReason == "" {
		return nil
	}
	return route
}
