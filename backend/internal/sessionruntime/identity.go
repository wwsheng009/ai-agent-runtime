package sessionruntime

import (
	"os"
	osuser "os/user"
	"strings"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

const (
	envSessionUser = "AICLI_SESSION_USER"
	serverFallback = "anonymous"
	cliFallback    = "default"
)

// IdentitySource describes all known identity sources for session history.
// The resolver intentionally keeps server and CLI fallback behavior different:
// server requests fall back to anonymous, while local CLI keeps an OS-user
// fallback for offline compatibility.
type IdentitySource struct {
	ExplicitUserID string
	AuthUserID     string
	CLIUserID      string
	Config         *runtimecfg.RuntimeConfig
	CLILocal       bool
	ServerFallback bool
}

func ResolveSessionUserID(source IdentitySource) string {
	for _, value := range []string{
		source.ExplicitUserID,
		source.AuthUserID,
		source.CLIUserID,
		os.Getenv(envSessionUser),
		defaultConfigUserID(source.Config),
	} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	if source.CLILocal {
		if username := currentOSUsername(); username != "" {
			return username
		}
		for _, key := range []string{"USERNAME", "USER"} {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				return value
			}
		}
		return cliFallback
	}
	if source.ServerFallback {
		return serverFallback
	}
	return ""
}

func defaultConfigUserID(config *runtimecfg.RuntimeConfig) string {
	if config == nil {
		return ""
	}
	return strings.TrimSpace(config.Sessions.DefaultUserID)
}

func currentOSUsername() string {
	current, err := osuser.Current()
	if err != nil || current == nil {
		return ""
	}
	return strings.TrimSpace(current.Username)
}
