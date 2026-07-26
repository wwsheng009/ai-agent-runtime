package agent

import runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"

// PermissionEngine evaluates runtime tool permissions.
type PermissionEngine = runtimepolicy.Engine

// NewPermissionEngine creates a new permission engine.
func NewPermissionEngine() *PermissionEngine {
	engine := &runtimepolicy.Engine{}
	runtimepolicy.EnsurePlanWriteAllowPaths(engine)
	return engine
}
