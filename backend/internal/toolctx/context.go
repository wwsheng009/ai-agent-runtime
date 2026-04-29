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
	generatedImageOutputDirKey contextKey = "generated_image_output_dir"
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
