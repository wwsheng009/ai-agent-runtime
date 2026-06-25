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
	PermissionMode string       `json:"permission_mode,omitempty"`
	Team           *TeamRunMeta `json:"team,omitempty"`
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
		PermissionMode: m.PermissionMode,
		Team:           m.Team.Clone(),
	}
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
