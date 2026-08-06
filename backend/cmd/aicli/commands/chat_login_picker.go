package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// executeStructuredLoginCommand is the unified interactive entry point for
// /login. It reuses the legacy parse + runProviderLogin chain (whose prompter
// already routes through the unified composer), but renders the result as one
// unified command cell instead of raw stdout, and collects the post-login
// session-refresh outcome as warnings.
func executeStructuredLoginCommand(session *ChatSession, command string) (CommandResult, bool) {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话")), true
	}
	parsed, err := parseChatLoginCommandRequest(command)
	if err != nil {
		return commandTextResult("错误: " + err.Error()), true
	}
	timeout := time.Duration(parsed.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	oauthTimeout := time.Duration(parsed.OAuthTimeout) * time.Second
	if oauthTimeout <= 0 {
		oauthTimeout = 15 * time.Minute
	}
	req := providerLoginRequest{
		Context:              context.Background(),
		Config:               session.Config,
		ProviderName:         parsed.Provider,
		LoginProtocol:        parsed.Protocol,
		AuthMode:             parsed.Mode,
		BaseURL:              parsed.BaseURL,
		APIKey:               parsed.APIKey,
		ModelsPath:           parsed.ModelsPath,
		DefaultModel:         parsed.DefaultModel,
		SetDefault:           parsed.SetDefault,
		DryRun:               parsed.DryRun,
		Interactive:          true,
		Timeout:              timeout,
		AuthRef:              parsed.AuthRef,
		OAuthIssuer:          parsed.OAuthIssuer,
		OAuthClientID:        parsed.OAuthClientID,
		OAuthTimeout:         oauthTimeout,
		ModelCardCatalogPath: parsed.ModelCardsPath,
		DisableModelCards:    parsed.NoModelCards,
		ModelCardsStrict:     parsed.ModelCardsStrict,
		SiteType:             parsed.SiteType,
		SkipSiteDetect:       parsed.SkipSiteDetect,
		SkipAccount:          parsed.SkipAccount,
		RequireAccount:       parsed.RequireAccount,
		NewAPIAccessToken:    parsed.NewAPIAccessToken,
		NewAPIUserID:         parsed.NewAPIUserID,
	}
	req.Prompter = chatLoginPrompter{session: session}
	result, err := runProviderLogin(req)
	if err != nil {
		return commandErrorResult(err), true
	}

	warnings := refreshUnifiedLoginSession(session, result, parsed.Switch)
	return commandResultWithWarnings(buildChatLoginResultDocument(result), warnings...), true
}

// buildChatLoginResultDocument is the terminal-neutral projection of the legacy
// renderLoginCommandResult text path.
func buildChatLoginResultDocument(result *providerLoginResult) render.Document {
	lines := []string{"Provider 登录成功"}
	lines = append(lines, formatChatSessionMetaRow("Provider:", chatDebugValueOrNone(result.ProviderName)))
	protocol := strings.TrimSpace(result.Protocol)
	if strings.TrimSpace(result.LoginProtocol) != "" {
		protocol = fmt.Sprintf("%s (%s)", protocol, result.LoginProtocol)
	}
	lines = append(lines, formatChatSessionMetaRow("Protocol:", chatDebugValueOrNone(protocol)))
	lines = append(lines, formatChatSessionMetaRow("Auth mode:", chatDebugValueOrNone(result.AuthMode)))
	if result.AuthRef != "" {
		lines = append(lines, formatChatSessionMetaRow("Auth ref:", result.AuthRef))
	}
	if result.APIKeyRef != "" {
		lines = append(lines, formatChatSessionMetaRow("API key ref:", result.APIKeyRef))
	}
	if result.APIKeyMasked != "" && result.AuthMode != providerAuthModeOAuth {
		lines = append(lines, formatChatSessionMetaRow("API key:", result.APIKeyMasked))
	}
	lines = append(lines, formatChatSessionMetaRow("Base URL:", chatDebugValueOrNone(result.BaseURL)))
	lines = append(lines, formatChatSessionMetaRow("Models endpoint:", chatDebugValueOrNone(result.ModelsEndpoint)))
	lines = append(lines, formatChatSessionMetaRow("Default model:", chatDebugValueOrNone(result.DefaultModel)))
	lines = append(lines, formatChatSessionMetaRow("Models:", fmt.Sprintf("%d", len(result.SupportedModels))))
	for _, model := range previewModelList(result.SupportedModels, 20) {
		lines = append(lines, "    - "+model)
	}
	if len(result.ProviderConfigs) > 0 {
		lines = append(lines, formatChatSessionMetaRow("Provider configs:", fmt.Sprintf("%d", len(result.ProviderConfigs))))
		for _, item := range result.ProviderConfigs {
			template := item.ProviderTemplate
			if template == "" {
				template = item.Protocol
			}
			lines = append(lines, fmt.Sprintf("    - %s (%s): %d models, default %s", item.ProviderName, template, len(item.SupportedModels), item.DefaultModel))
		}
	}
	if len(result.ModelsSkippedByProtocol) > 0 {
		lines = append(lines, formatChatSessionMetaRow("Models skipped:", fmt.Sprintf("%d by provider template", len(result.ModelsSkippedByProtocol))))
		for _, skipped := range previewModelsSkippedByProtocol(result.ModelsSkippedByProtocol, 10) {
			if skipped.RecommendedProviderTemplate == "" {
				lines = append(lines, "    - "+skipped.Model)
				continue
			}
			lines = append(lines, fmt.Sprintf("    - %s -> %s", skipped.Model, skipped.RecommendedProviderTemplate))
		}
	}
	if len(result.ModelCardsApplied) > 0 {
		lines = append(lines, formatChatSessionMetaRow("Model cards:", fmt.Sprintf("%d applied", len(result.ModelCardsApplied))))
		for _, applied := range previewModelCardsApplied(result.ModelCardsApplied, 10) {
			if applied.CardID == "" {
				lines = append(lines, "    - "+applied.Model)
				continue
			}
			lines = append(lines, fmt.Sprintf("    - %s <- %s", applied.Model, applied.CardID))
		}
	}
	if len(result.ModelCardWarnings) > 0 {
		lines = append(lines, formatChatSessionMetaRow("Model warnings:", fmt.Sprintf("%d", len(result.ModelCardWarnings))))
		for _, warning := range previewModelCardWarnings(result.ModelCardWarnings, 5) {
			if warning.Source != "" {
				lines = append(lines, fmt.Sprintf("    - %s: %s", warning.Source, warning.Message))
				continue
			}
			lines = append(lines, "    - "+warning.Message)
		}
	}
	if result.DryRun {
		lines = append(lines, formatChatSessionMetaRow("Config:", "dry-run，未写入"))
	} else if result.ConfigPath != "" {
		lines = append(lines, formatChatSessionMetaRow("Config:", result.ConfigPath))
	}
	if result.AuthStorePath != "" {
		lines = append(lines, formatChatSessionMetaRow("Auth store:", result.AuthStorePath))
	}
	if result.SiteType != "" {
		if result.SiteTypeConfidence != "" {
			lines = append(lines, formatChatSessionMetaRow("Site type:", fmt.Sprintf("%s (%s)", result.SiteType, result.SiteTypeConfidence)))
		} else {
			lines = append(lines, formatChatSessionMetaRow("Site type:", result.SiteType))
		}
	}
	if result.BalanceLine != "" {
		lines = append(lines, formatChatSessionMetaRow("Balance:", result.BalanceLine))
	} else if result.Account != nil && result.Account.Source != "" {
		lines = append(lines, formatChatSessionMetaRow("Account source:", result.Account.Source))
	}
	if result.AccountAuthRef != "" {
		lines = append(lines, formatChatSessionMetaRow("Account auth:", result.AccountAuthRef))
	}
	for _, warning := range result.SiteAccountWarnings {
		lines = append(lines, formatChatSessionMetaRow("Warning:", warning))
	}
	return textLinesDocument(lines)
}

// refreshUnifiedLoginSession applies the post-login session refresh without
// writing to stdout; failures and the success notice are returned as warnings.
func refreshUnifiedLoginSession(session *ChatSession, result *providerLoginResult, switchProvider bool) []error {
	if session == nil || result == nil || result.DryRun {
		return nil
	}
	shouldRefresh := switchProvider || strings.EqualFold(strings.TrimSpace(session.ProviderName), strings.TrimSpace(result.ProviderName))
	if !shouldRefresh {
		return nil
	}
	providerCtx, _, err := resolveModelCommandExecutionContext(session, result.ProviderName, result.DefaultModel)
	if err != nil {
		return []error{fmt.Errorf("登录成功，但刷新当前会话失败: %w", err)}
	}
	if err := applyModelCommandSelection(session, providerCtx, providerCtx.RequestedModel, session.ReasoningEffort); err != nil {
		return []error{fmt.Errorf("登录成功，但刷新当前会话失败: %w", err)}
	}
	return []error{fmt.Errorf("当前 chat 会话已刷新到最新 provider 配置")}
}
