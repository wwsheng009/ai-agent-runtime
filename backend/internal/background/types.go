package background

import "time"

// RestartPolicy describes how a job should be handled after a process restart.
type RestartPolicy string

const (
	RestartPolicyFail  RestartPolicy = "fail"
	RestartPolicyRerun RestartPolicy = "rerun"
)

// JobStatus describes the lifecycle state of a background job.
type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
	StatusTimedOut  JobStatus = "timed_out"
	StatusCancelled JobStatus = "cancelled"
	StatusOrphaned  JobStatus = "orphaned"
)

// StartupProbeType identifies the generic probe used to accept a started process.
type StartupProbeType string

const (
	StartupProbeNone    StartupProbeType = "none"
	StartupProbeProcess StartupProbeType = "process"
	StartupProbeTCP     StartupProbeType = "tcp"
	StartupProbeHTTP    StartupProbeType = "http"
)

// StartupAcceptance configures the process-start acceptance gate. TCP and HTTP
// probes are caller-supplied infrastructure checks; the runtime does not infer
// application ports, endpoints, or provider-specific semantics.
type StartupAcceptance struct {
	Probe         StartupProbeType `json:"probe,omitempty"`
	GracePeriodMs int              `json:"grace_period_ms,omitempty"`
	TimeoutMs     int              `json:"timeout_ms,omitempty"`
	Address       string           `json:"address,omitempty"`
	URL           string           `json:"url,omitempty"`
}

// BackgroundTaskArgs describes background task submission.
type BackgroundTaskArgs struct {
	Command       string             `json:"command"`
	Cwd           string             `json:"cwd,omitempty"`
	TimeoutSec    int                `json:"timeout_sec,omitempty"`
	Priority      int                `json:"priority,omitempty"`
	RestartPolicy RestartPolicy      `json:"restart_policy,omitempty"`
	Startup       *StartupAcceptance `json:"startup_acceptance,omitempty"`
}

// BackgroundTaskResult reports a submitted job.
type BackgroundTaskResult struct {
	JobID            string        `json:"job_id"`
	Status           string        `json:"status"`
	Message          string        `json:"message,omitempty"`
	RestartPolicy    RestartPolicy `json:"restart_policy,omitempty"`
	LaunchState      string        `json:"launch_state,omitempty"`
	HealthcheckState string        `json:"healthcheck_state,omitempty"`
	StartupProbe     string        `json:"startup_probe,omitempty"`
	StartupGraceMs   int64         `json:"startup_grace_ms,omitempty"`
}

// TaskOutputArgs reads task output from an offset.
type TaskOutputArgs struct {
	JobID  string `json:"job_id"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// TaskOutputResult returns output chunk information.
type TaskOutputResult struct {
	JobID               string `json:"job_id"`
	Status              string `json:"status"`
	Output              string `json:"output,omitempty"`
	NextOffset          int64  `json:"next_offset"`
	ExitCode            *int   `json:"exit_code,omitempty"`
	Message             string `json:"message,omitempty"`
	ErrorCode           string `json:"error_code,omitempty"`
	TimeoutRequestedMs  int64  `json:"timeout_requested_ms,omitempty"`
	TimeoutEffectiveMs  int64  `json:"timeout_effective_ms,omitempty"`
	TimeoutSource       string `json:"timeout_source,omitempty"`
	CancelSource        string `json:"cancel_source,omitempty"`
	WatchdogState       string `json:"watchdog_state,omitempty"`
	WatchdogErrorCode   string `json:"watchdog_error_code,omitempty"`
	LaunchAttempt       int    `json:"launch_attempt,omitempty"`
	LaunchMaxAttempts   int    `json:"launch_max_attempts,omitempty"`
	ProcessState        string `json:"process_state,omitempty"`
	HeartbeatAgeMs      int64  `json:"heartbeat_age_ms,omitempty"`
	LastOutputAt        string `json:"last_output_at,omitempty"`
	QuietForMs          int64  `json:"quiet_for_ms,omitempty"`
	RecoveryAttempt     int    `json:"recovery_attempt,omitempty"`
	RecoveryMaxAttempts int    `json:"recovery_max_attempts,omitempty"`
	NextRecoveryAt      string `json:"next_recovery_at,omitempty"`
	LaunchState         string `json:"launch_state,omitempty"`
	ProcessStarted      bool   `json:"process_started,omitempty"`
	ProcessAlive        *bool  `json:"process_alive,omitempty"`
	StartupProbe        string `json:"startup_probe,omitempty"`
	StartupGraceMs      int64  `json:"startup_grace_ms,omitempty"`
	StartupAcceptedAt   string `json:"startup_accepted_at,omitempty"`
	HealthcheckState    string `json:"healthcheck_state,omitempty"`
	HealthcheckError    string `json:"healthcheck_error,omitempty"`
}

// JobFilter filters background job queries.
type JobFilter struct {
	SessionID string
	Status    []JobStatus
	Limit     int
	Offset    int
}

// Job captures a background command execution.
type Job struct {
	ID            string
	SessionID     string
	Kind          string
	Command       string
	Cwd           string
	Priority      int
	RestartPolicy RestartPolicy
	Status        JobStatus
	Message       string
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	ExitCode      *int
	LogPath       string
	Metadata      map[string]interface{}
}
