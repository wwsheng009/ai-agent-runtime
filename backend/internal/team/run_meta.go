package team

// TeamRunMeta contains team-specific execution context.
type TeamRunMeta struct {
	TeamID        string `json:"team_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	CurrentTaskID string `json:"current_task_id,omitempty"`
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
