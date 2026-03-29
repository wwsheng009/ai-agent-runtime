package errors

// ErrorCode 错误码
type ErrorCode string

const (
	// 网络错误
	ErrNetworkTimeout     ErrorCode = "NETWORK_TIMEOUT"
	ErrNetworkUnavailable ErrorCode = "NETWORK_UNAVAILABLE"

	// API 错误
	ErrAPIRateLimit   ErrorCode = "API_RATE_LIMIT"
	ErrAPIUnauthorized ErrorCode = "API_UNAUTHORIZED"
	ErrAPINotFound    ErrorCode = "API_NOT_FOUND"
	ErrAPIBadRequest  ErrorCode = "API_BAD_REQUEST"
	ErrAPIServerError ErrorCode = "API_SERVER_ERROR"

	// 工具错误
	ErrToolNotFound  ErrorCode = "TOOL_NOT_FOUND"
	ErrToolExecution ErrorCode = "TOOL_EXECUTION"
	ErrToolTimeout   ErrorCode = "TOOL_TIMEOUT"

	// Agent 错误
	ErrAgentMaxSteps   ErrorCode = "AGENT_MAX_STEPS"
	ErrAgentPermission ErrorCode = "AGENT_PERMISSION"

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
