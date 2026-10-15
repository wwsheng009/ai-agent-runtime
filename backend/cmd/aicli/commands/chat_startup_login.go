package commands

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// runStartupProviderLogin runs the interactive provider login from the chat
// startup path (the "＋ 新建 provider" / pending-login picker rows) before any
// chat session exists. It shares the startup tracked stdin reader so the
// watchdog keeps treating prompts as "waiting for user input", persists via
// the same runProviderLogin chain as `aicli login`, and applies the result to
// the in-memory config. The caller re-runs provider resolution afterwards.
//
// presetProviderName optionally names an existing (often preset-contributed)
// provider to log into; when empty the login flow's own name/template pickers
// run (template choice → name → protocol/base URL confirm → API key → models).
func runStartupProviderLogin(cfg *config.Config, reader *bufio.Reader, presetProviderName string) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("provider login requires a loaded config")
	}
	if reader == nil {
		reader = newTrackedStdinReader()
	}
	authStorePath := config.DefaultAuthStorePath()
	prompter := newCLILoginPrompterWithReader(cfg, authStorePath, false, reader)

	// First login in a fresh workspace should become the default provider so
	// the user does not have to run `aicli provider set-default` afterwards.
	setDefault := len(cfg.Providers.Items) == 0 || strings.TrimSpace(cfg.Providers.DefaultProvider) == ""

	req := providerLoginRequest{
		Context:      context.Background(),
		Config:       cfg,
		ConfigPath:   cfg.ConfigFilePath,
		ProviderName: presetProviderName,
		SetDefault:   setDefault,
		Interactive:  true,
		Timeout:      60 * time.Second,
		Prompter:     prompter,
	}
	result, err := runProviderLogin(req)
	if err != nil {
		return "", err
	}
	return result.ProviderName, nil
}

// providerNeedsLogin reports whether an enabled provider lacks usable
// credentials (no API keys resolvable through the auth store and no OAuth
// auth record). Such providers are the "pending login" rows in the startup
// picker: selecting one routes into the login flow instead of failing on the
// first upstream request.
func providerNeedsLogin(provider config.Provider) bool {
	if !provider.Enabled {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(provider.AuthMode), config.AuthKeyTypeOAuth) {
		if strings.TrimSpace(provider.AuthRef) != "" {
			return false
		}
	} else {
		if strings.TrimSpace(provider.APIKeyRef) != "" {
			if secret, err := config.LoadProviderAuthSecret(strings.TrimSpace(provider.APIKeyRef), config.AuthKeyTypeAPIKey); err == nil && strings.TrimSpace(secret) != "" {
				return false
			}
		}
	}
	return len(provider.GetAllAPIKeys()) == 0
}

// providerLoginHintLine builds the picker detail suffix for a provider that
// still needs credentials.
func providerLoginHintLine(provider config.Provider) string {
	if providerNeedsLogin(provider) {
		return "待登录：选择后将进入登录流程补全凭据"
	}
	return ""
}
