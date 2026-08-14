package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// loginPickerLeaseHooks binds the /login picker stages to their UI-actor
// barrier actions (OpenLoginPicker/CloseLoginPicker).
func loginPickerLeaseHooks() chatPickerLeaseHooks {
	return chatPickerLeaseHooks{
		Open: func(leaseID uint64) ui.UIAction {
			return ui.OpenLoginPicker{LeaseID: leaseID}
		},
		Close: func(leaseID uint64) ui.UIAction {
			return ui.CloseLoginPicker{LeaseID: leaseID}
		},
	}
}

// chatLoginPickerCreateRowTitle is the trailing row that lets the /login
// provider stage fall back to a text prompt for a brand-new provider name.
const chatLoginPickerCreateRowTitle = "＋ 新建 provider（手动输入名称）"

// isChatLoginCancelError reports whether a login-flow error means the user
// aborted the interaction (full-screen picker Esc/q or a text prompt
// interrupt). Callers map it to a neutral "已取消" outcome instead of an error.
func isChatLoginCancelError(err error) bool {
	return errors.Is(err, errChatLoginPickerCancelled) || isChatInteractivePromptCancelError(err)
}

// PromptSelect implements providerLoginSelectPrompter through the full-screen
// searchable picker shared with /model (instant search + up/down navigation).
// When the unified surface cannot host the list it returns
// ui.ErrFullScreenUnavailable so the login flow falls back to the numbered
// text picker; cancellation surfaces as cancelled=true.
func (p chatLoginPrompter) PromptSelect(label, kind string, options []string, current string, allowCreate bool) (string, bool, error) {
	session := p.session
	if session == nil || !chatPickerSurfaceReady(session) {
		return "", false, ui.ErrFullScreenUnavailable
	}

	items := buildChatPickerItems(options, current, kind, kind)
	createIndex := -1
	if allowCreate {
		createIndex = len(items)
		items = append(items, ui.FullScreenListItem{
			Title:      chatLoginPickerCreateRowTitle,
			Detail:     kind,
			SearchText: "create new " + kind + " 新建 provider",
		})
	}

	lease, err := chatPickerOpen(session, "选择 "+label, loginPickerLeaseHooks())
	if err != nil {
		return "", false, err
	}
	index, cancelled, stageErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
		Title:        "选择 " + label,
		Subtitle:     "Enter 确认，Esc 取消",
		EmptyMessage: fmt.Sprintf("没有匹配的 %s", kind),
		ConfirmLabel: fmt.Sprintf("使用选中 %s", kind),
		Items:        items,
	})
	_ = chatPickerClose(session, lease, loginPickerLeaseHooks())
	if stageErr != nil {
		return "", false, stageErr
	}
	if cancelled {
		return "", true, nil
	}
	if index == createIndex {
		name, nameErr := p.PromptText("新 "+kind+" 名称", "", true)
		if nameErr != nil {
			return "", false, nameErr
		}
		return strings.TrimSpace(name), false, nil
	}
	return options[index], false, nil
}

// executeStructuredLoginCommand is the unified interactive entry point for
// /login. It reuses the legacy parse + runProviderLogin chain (whose prompter
// already routes through the unified composer), but renders the result as one
// unified command cell instead of raw stdout, and collects the post-login
// session-refresh outcome as warnings.
func executeStructuredLoginCommand(session *ChatSession, command string) (CommandResult, bool) {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话")), true
	}
	// The prompter routes through the unified composer only when a surface is
	// enabled; without one its fallback would write prompts straight to stdout.
	// Fail closed instead of reviving a raw writer under TerminalSession.
	if unifiedDirectInteractiveOutput(session) &&
		(session.Surface == nil || !session.Surface.Enabled()) {
		return commandErrorResult(fmt.Errorf("当前统一终端缺少可用 surface，无法安全进行交互式登录；请使用非交互参数（--api-key/--provider）重试")), true
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
		if isChatLoginCancelError(err) {
			return commandTextResult("已取消登录"), true
		}
		return commandErrorResult(err), true
	}

	warnings, notice := refreshUnifiedLoginSession(session, result, parsed.Switch)
	doc := buildChatLoginResultDocument(result)
	if notice != "" {
		lines := strings.Split(strings.TrimRight(ui.RenderDocumentPlain(doc), "\n"), "\n")
		lines = append(lines, notice)
		doc = textLinesDocument(lines)
	}
	return commandResultWithWarnings(doc, warnings...), true
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
// writing to stdout. Failures are returned as warnings; the success notice is
// an ordinary informational line appended to the result document (not a
// warning, which would render with a "警告:" prefix).
func refreshUnifiedLoginSession(session *ChatSession, result *providerLoginResult, switchProvider bool) ([]error, string) {
	if session == nil || result == nil || result.DryRun {
		return nil, ""
	}
	shouldRefresh := switchProvider || strings.EqualFold(strings.TrimSpace(session.ProviderName), strings.TrimSpace(result.ProviderName))
	if !shouldRefresh {
		return nil, ""
	}
	providerCtx, _, err := resolveModelCommandExecutionContext(session, result.ProviderName, result.DefaultModel)
	if err != nil {
		return []error{fmt.Errorf("登录成功，但刷新当前会话失败: %w", err)}, ""
	}
	if err := applyModelCommandSelection(session, providerCtx, providerCtx.RequestedModel, session.ReasoningEffort); err != nil {
		return []error{fmt.Errorf("登录成功，但刷新当前会话失败: %w", err)}, ""
	}
	return nil, "当前 chat 会话已刷新到最新 provider 配置"
}
