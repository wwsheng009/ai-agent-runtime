package team

import (
	"context"
	"time"
)

// PreflightSeverity is the impact of a finding on whether a team can run.
type PreflightSeverity string

const (
	PreflightSeverityBlocker PreflightSeverity = "blocker"
	PreflightSeverityWarning PreflightSeverity = "warning"
	PreflightSeverityInfo    PreflightSeverity = "info"
)

// PreflightFinding is a deterministic, actionable result of a team preflight.
// The identity fields make it possible for API consumers to link a finding to
// the affected legacy Team, Teammate, Task, dependency, or path without
// parsing Message.
type PreflightFinding struct {
	Severity        PreflightSeverity `json:"severity"`
	Code            string            `json:"code"`
	Message         string            `json:"message"`
	Remediation     string            `json:"remediation"`
	TeamID          string            `json:"team_id,omitempty"`
	TeammateID      string            `json:"teammate_id,omitempty"`
	RelatedTeammate string            `json:"related_teammate_id,omitempty"`
	TaskID          string            `json:"task_id,omitempty"`
	RelatedTaskID   string            `json:"related_task_id,omitempty"`
	DependsOnID     string            `json:"depends_on_id,omitempty"`
	Path            string            `json:"path,omitempty"`
	RelatedPath     string            `json:"related_path,omitempty"`
	ExistingOwner   string            `json:"existing_owner,omitempty"`
	ExistingMode    PathClaimMode     `json:"existing_mode,omitempty"`
}

// PreflightRoutePreview is the optional route capability result for one task.
// Route is copied from the injected existing TaskRouteResolver. The preflight
// service deliberately never fills Provider or Model itself.
type PreflightRoutePreview struct {
	TaskID     string              `json:"task_id,omitempty"`
	TeammateID string              `json:"teammate_id,omitempty"`
	SessionID  string              `json:"session_id,omitempty"`
	Capability string              `json:"capability"`
	Route      *TaskExecutionRoute `json:"route,omitempty"`
	Disabled   bool                `json:"disabled,omitempty"`
	Strict     bool                `json:"strict,omitempty"`
	Error      string              `json:"error,omitempty"`
}

const (
	PreflightRouteCapabilityNotRequested = "not_requested"
	PreflightRouteCapabilityResolved     = "resolved"
	PreflightRouteCapabilityDisabled     = "disabled"
	PreflightRouteCapabilityUnavailable  = "unavailable"
	PreflightRouteCapabilityFailed       = "failed"
	PreflightRouteCapabilityPartial      = "partial"
)

// PreflightOptions configures optional, read-only preflight integrations.
type PreflightOptions struct {
	WorkspaceRoot string
	RouteResolver TaskRouteResolver
	Now           func() time.Time
}

// PreflightService builds reports from an existing legacy Team and Store.
// It does not persist data and does not introduce a Team Definition or Run
// schema.
type PreflightService struct {
	Store         Store
	WorkspaceRoot string
	RouteResolver TaskRouteResolver
	Now           func() time.Time
}

// TeamPreflight is the narrow contract suitable for a handler, command, or
// other API adapter. The adapter is intentionally not part of this slice.
type TeamPreflight interface {
	Build(ctx context.Context, teamID string) (*PreflightReport, error)
}

// PreflightReport is the stable result of validating one existing Team.
type PreflightReport struct {
	TeamID                 string                  `json:"team_id"`
	Team                   *Team                   `json:"team,omitempty"`
	TeamExists             bool                    `json:"team_exists"`
	Ready                  bool                    `json:"ready"`
	BlockerCount           int                     `json:"blocker_count"`
	WarningCount           int                     `json:"warning_count"`
	InfoCount              int                     `json:"info_count"`
	Findings               []PreflightFinding      `json:"findings,omitempty"`
	Blockers               []PreflightFinding      `json:"blockers,omitempty"`
	Warnings               []PreflightFinding      `json:"warnings,omitempty"`
	Infos                  []PreflightFinding      `json:"infos,omitempty"`
	RoutePreviewCapability string                  `json:"route_preview_capability"`
	RoutePreviews          []PreflightRoutePreview `json:"route_previews,omitempty"`
}

const (
	PreflightCodeTeamNotFound                 = "team_not_found"
	PreflightCodeTeamEmpty                    = "team_empty"
	PreflightCodeTeamNotRunnable              = "team_not_runnable"
	PreflightCodeTeamActiveTeammatesAtLimit   = "team_active_teammates_at_limit"
	PreflightCodeTeamActiveTeammatesExceeded  = "team_active_teammates_limit_exceeded"
	PreflightCodeTeamActiveWritersExceeded    = "team_active_writers_limit_exceeded"
	PreflightCodeTeamWriterLimitWillSerialize = "team_writer_limit_will_serialize"
	PreflightCodeTeamTeammatesEmpty           = "team_teammates_empty"
	PreflightCodeTeamTasksEmpty               = "team_tasks_empty"
	PreflightCodeTeammateIDMissing            = "teammate_id_missing"
	PreflightCodeTeammateDuplicateID          = "teammate_duplicate_id"
	PreflightCodeTeammateTeamMismatch         = "teammate_team_mismatch"
	PreflightCodeTeammateSessionMissing       = "teammate_session_missing"
	PreflightCodeTeammateDuplicateSession     = "teammate_duplicate_session"
	PreflightCodeTeammateOffline              = "teammate_offline"
	PreflightCodeTeammateBlocked              = "teammate_blocked"
	PreflightCodeTeammateBusy                 = "teammate_busy"
	PreflightCodeTeammateStateUnknown         = "teammate_state_unknown"
	PreflightCodeTaskIDMissing                = "task_id_missing"
	PreflightCodeTaskDuplicateID              = "task_duplicate_id"
	PreflightCodeTaskTeamMismatch             = "task_team_mismatch"
	PreflightCodeTaskUnassigned               = "task_unassigned"
	PreflightCodeTaskAssigneeUnknown          = "task_assignee_unknown"
	PreflightCodeTaskAssigneeSessionMissing   = "task_assignee_session_missing"
	PreflightCodeTaskAssigneeOffline          = "task_assignee_offline"
	PreflightCodeTaskAssigneeBlocked          = "task_assignee_blocked"
	PreflightCodeTaskAssigneeBusy             = "task_assignee_busy"
	PreflightCodeDependencySelf               = "dependency_self"
	PreflightCodeDependencyTaskMissing        = "dependency_task_missing"
	PreflightCodeDependencyCrossTeam          = "dependency_cross_team"
	PreflightCodeDependencyCycle              = "dependency_cycle"
	PreflightCodePathWriteConflict            = "path_write_conflict"
	PreflightCodePathClaimConflict            = "path_claim_conflict"
	PreflightCodeRoutePreviewUnavailable      = "route_preview_unavailable"
	PreflightCodeRoutePreviewDisabled         = "route_preview_disabled"
	PreflightCodeRoutePreviewError            = "route_preview_error"
)
