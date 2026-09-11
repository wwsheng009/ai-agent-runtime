package toolctx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type contextKey string

const (
	sessionIDKey               contextKey = "tool_session_id"
	goalIDKey                  contextKey = "tool_goal_id"
	agentDepthKey              contextKey = "tool_agent_depth"
	generatedImageOutputDirKey contextKey = "generated_image_output_dir"
	shellOutputArtifactDirKey  contextKey = "shell_output_artifact_dir"
	workspaceRootKey           contextKey = "tool_workspace_root"
)

// WithSessionID stores the active session ID in ctx.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionIDKey, strings.TrimSpace(sessionID))
}

// SessionID retrieves the active session ID from ctx.
func SessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(sessionIDKey).(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// WithGoalID stores the goal that owns state created by a tool invocation.
func WithGoalID(ctx context.Context, goalID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, goalIDKey, strings.TrimSpace(goalID))
}

// GoalID retrieves the active goal ID from ctx.
func GoalID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(goalIDKey).(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// WithAgentDepth stores the depth of the agent invoking the tool. Root agents
// use depth zero; spawned child agents use positive depths.
func WithAgentDepth(ctx context.Context, depth int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if depth < 0 {
		depth = 0
	}
	return context.WithValue(ctx, agentDepthKey, depth)
}

// AgentDepth retrieves the invoking agent depth from ctx.
func AgentDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if value, ok := ctx.Value(agentDepthKey).(int); ok && value > 0 {
		return value
	}
	return 0
}

// WithGeneratedImageOutputDir stores the generated image output directory in ctx.
func WithGeneratedImageOutputDir(ctx context.Context, outputDir string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, generatedImageOutputDirKey, strings.TrimSpace(outputDir))
}

// GeneratedImageOutputDir retrieves the generated image output directory from ctx.
// If no explicit directory was stored but a session ID exists, a deterministic
// temp-directory fallback is returned.
func GeneratedImageOutputDir(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(generatedImageOutputDirKey).(string); ok {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	if sessionID := SessionID(ctx); sessionID != "" {
		return filepath.Join(defaultGeneratedImageRoot(), sanitizePathSegment(sessionID))
	}
	return ""
}

// WithShellOutputArtifactDir stores the session-local root for raw shell output.
func WithShellOutputArtifactDir(ctx context.Context, outputDir string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, shellOutputArtifactDirKey, strings.TrimSpace(outputDir))
}

// ShellOutputArtifactDir retrieves the session-local root for raw shell output.
func ShellOutputArtifactDir(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(shellOutputArtifactDirKey).(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// WithWorkspaceRoot stores the session-bound filesystem root in ctx. Tools use
// it to anchor process execution (shell CWD) and relative path resolution so a
// directory-bound session operates inside its bound project directory instead
// of the server process working directory.
func WithWorkspaceRoot(ctx context.Context, root string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, workspaceRootKey, strings.TrimSpace(root))
}

// WorkspaceRoot retrieves the session-bound filesystem root from ctx. Empty
// means the caller did not bind a workspace and tools fall back to the
// process working directory.
func WorkspaceRoot(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(workspaceRootKey).(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func defaultGeneratedImageRoot() string {
	return filepath.Join(os.TempDir(), "ai-agent-runtime", "generated-images")
}

func sanitizePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '-' || ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('_')
		}
	}
	result := builder.String()
	if result == "" {
		return "session"
	}
	return result
}
