package errors

// ErrorCode 错误码
type ErrorCode string

const (
	// 网络错误
	ErrNetworkTimeout     ErrorCode = "NETWORK_TIMEOUT"
	ErrNetworkUnavailable ErrorCode = "NETWORK_UNAVAILABLE"

	// API 错误
	ErrAPIRateLimit    ErrorCode = "API_RATE_LIMIT"
	ErrAPIUnauthorized ErrorCode = "API_UNAUTHORIZED"
	ErrAPINotFound     ErrorCode = "API_NOT_FOUND"
	ErrAPIBadRequest   ErrorCode = "API_BAD_REQUEST"
	ErrAPIServerError  ErrorCode = "API_SERVER_ERROR"

	// 工具错误
	ErrToolNotFound         ErrorCode = "TOOL_NOT_FOUND"
	ErrToolExecution        ErrorCode = "TOOL_EXECUTION"
	ErrToolTimeout          ErrorCode = "TOOL_TIMEOUT"
	ErrWritePrecondition    ErrorCode = "WRITE_PRECONDITION_FAILED"
	ErrJobNotFound          ErrorCode = "JOB_NOT_FOUND"
	ErrTurnDeadlineExceeded ErrorCode = "TURN_DEADLINE_EXCEEDED"
	ErrAgentRunCanceled     ErrorCode = "AGENT_RUN_CANCELED"
	ErrApprovalExpired      ErrorCode = "APPROVAL_EXPIRED"
	ErrSessionLeaseConflict ErrorCode = "SESSION_LEASE_CONFLICT"
	ErrToolInvalidArgs      ErrorCode = "TOOL_INVALID_ARGS"
	ErrToolPathNotFound     ErrorCode = "TOOL_PATH_NOT_FOUND"
	// ErrToolStaleContext marks edit/apply_patch failures where the provided
	// old_string / @@ context no longer matches the workspace. Models must
	// re-view and rebuild rather than retry the same payload unchanged.
	ErrToolStaleContext ErrorCode = "STALE_CONTEXT"
	// ErrToolShellCompat marks shell/environment mismatch failures that are
	// recoverable by changing the command shape (missing utility, wrong shell
	// dialect, Unix-only pipeline on Windows, etc.). Generic — not tool-name bound.
	ErrToolShellCompat ErrorCode = "TOOL_SHELL_COMPAT"
	// ErrAgentSpawnDepthLimit marks spawn_agent failures caused by MaxDepth.
	// Not retryable: continue locally or use spawn_team / an existing agent.
	ErrAgentSpawnDepthLimit ErrorCode = "SPAWN_DEPTH_LIMIT"
	// ErrAgentNestedDelegation marks a child attempting to create another
	// execution node without an explicit inherited delegation opt-in.
	ErrAgentNestedDelegation ErrorCode = "NESTED_DELEGATION_DISABLED"
	// ErrAgentSubagentCircuitOpen marks fail-fast rejection after consecutive
	// failed subagent batches. It is intentionally distinct from depth/policy.
	ErrAgentSubagentCircuitOpen ErrorCode = "SUBAGENT_CIRCUIT_OPEN"
	ErrToolBrokerFailure        ErrorCode = "TOOL_BROKER_FAILURE"
	ErrProcessStartFailed       ErrorCode = "PROCESS_START_FAILED"
	ErrProcessHealthcheck       ErrorCode = "PROCESS_HEALTHCHECK_FAILED"

	// Agent 错误
	ErrAgentMaxSteps   ErrorCode = "AGENT_MAX_STEPS"
	ErrAgentPermission ErrorCode = "AGENT_PERMISSION"
	// ErrAgentReadOnly is a non-overridable child/tool execution boundary.
	// Approval and bypass_permissions cannot widen this policy.
	ErrAgentReadOnly ErrorCode = "AGENT_READ_ONLY"
	// ErrAgentAlreadyExists marks a deterministic spawn conflict. Retrying the
	// same explicit child id cannot succeed; callers must reuse/close it or pick
	// another id.
	ErrAgentAlreadyExists ErrorCode = "AGENT_ALREADY_EXISTS"
	// ErrAgentBusy marks send_input attempts that require the caller to wait for
	// the active run or explicitly request interruption before resubmitting.
	ErrAgentBusy ErrorCode = "AGENT_BUSY"
	// ErrAgentSessionNotFound marks an opaque session_ref_* that is not present
	// in the current parent session's durable handle registry.
	ErrAgentSessionNotFound ErrorCode = "AGENT_SESSION_NOT_FOUND"
	ErrContextBudget        ErrorCode = "CONTEXT_BUDGET_EXCEEDED"
	ErrStreamInterrupted    ErrorCode = "STREAM_INTERRUPTED"
	ErrUpstreamUnavailable  ErrorCode = "UPSTREAM_UNAVAILABLE"

	// 内存错误
	ErrMemoryFull ErrorCode = "MEMORY_FULL"

	// Workflow 错误
	ErrWorkflowCycle ErrorCode = "WORKFLOW_CYCLE"
	ErrWorkflowStep  ErrorCode = "WORKFLOW_STEP"

	// Skill 错误
	ErrSkillNotFound     ErrorCode = "SKILL_NOT_FOUND"
	ErrSkillLoadFailed   ErrorCode = "SKILL_LOAD_FAILED"
	ErrInvalidManifest   ErrorCode = "INVALID_MANIFEST"
	ErrToolNotRegistered ErrorCode = "TOOL_NOT_REGISTERED"

	// 验证错误
	ErrValidationFailed ErrorCode = "VALIDATION_FAILED"

	// 配置错误
	ErrConfigNotFound ErrorCode = "CONFIG_NOT_FOUND"
	ErrConfigInvalid  ErrorCode = "CONFIG_INVALID"
)
