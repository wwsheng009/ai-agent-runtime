package team

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
