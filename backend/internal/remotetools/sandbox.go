package remotetools

import (
	"fmt"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// Sandbox wraps the shared executor.Sandbox for one remote target.
//
// The remote side reuses executor.Sandbox instead of implementing a second path
// checker (review D7): it already provides lexical allow/deny/read-only roots and
// symlink-resolved re-checks. Command authorization and env filtering live in
// CommandPolicy so the envd bridge can apply the exact same rules.
type Sandbox struct {
	inner    *runtimeexecutor.Sandbox
	commands *CommandPolicy
}

// NewSandbox materializes the remote sandbox from configuration.
//
// The allowed path set is exactly the workspace jail, so a path that passes the
// jail can never fail the sandbox for a different reason. ReadOnlyPaths is empty
// because the profile already decides whether write tools exist at all; making
// every path read-only would instead break the `standard` profile.
func NewSandbox(cfg *Config, workspace *Workspace) (*Sandbox, error) {
	if cfg == nil || workspace == nil {
		return nil, fmt.Errorf("config and workspace are required")
	}
	commands, err := NewCommandPolicy(cfg.Shell, cfg.Limits.CommandTimeout.Duration)
	if err != nil {
		return nil, err
	}
	sandboxConfig := runtimeexecutor.SandboxConfig{
		Enabled:       true,
		AllowedPaths:  workspace.AllowedRoots(),
		DeniedPaths:   nil,
		ReadOnlyPaths: nil,
	}
	return &Sandbox{
		inner:    runtimeexecutor.NewSandbox(&sandboxConfig),
		commands: commands,
	}, nil
}

// CheckPath validates one already-resolved absolute path for an operation.
func (s *Sandbox) CheckPath(op runtimeexecutor.PermissionOp, absPath string) error {
	if s == nil || s.inner == nil {
		return newToolError(CodeInternal, "check the server configuration", "sandbox is not initialized")
	}
	if err := s.inner.CheckPermission(op, absPath); err != nil {
		return newToolError(CodePathDenied, "use a path inside the workspace", "%v", err)
	}
	return nil
}

// CheckCommand delegates to the shared command policy.
func (s *Sandbox) CheckCommand(command string) (string, error) {
	var policy *CommandPolicy
	if s != nil {
		policy = s.commands
	}
	return policy.CheckCommand(command)
}

// FilterEnv returns the environment for one command.
//
// An empty whitelist yields an empty environment (not the inherited one): a remote
// command should not silently inherit credentials from the server process.
func (s *Sandbox) FilterEnv(env []string) []string {
	var policy *CommandPolicy
	if s != nil {
		policy = s.commands
	}
	return policy.FilterEnv(env)
}

// Timeout returns the effective command timeout.
func (s *Sandbox) Timeout() time.Duration {
	var policy *CommandPolicy
	if s != nil {
		policy = s.commands
	}
	return policy.Timeout()
}
