package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// canOpenChatModelPicker is intentionally stricter than a generic list
// capability check. Switching the model mutates session state, so the picker
// may only begin while the unified primary presenter is idle, owns its
// viewport, and no competing popup or alternate screen owns input.
func canOpenChatModelPicker(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	if session.RuntimeEventBridge != nil && session.RuntimeEventBridge.isRunActive() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// openChatModelPicker executes the typed alternate-screen interaction for the
// /model command. The lease ends before any session mutation, so the primary
// TerminalSession keeps one clear recovery boundary: provider→model→reasoning
// stages all borrow the same alternate screen, and the apply step runs only
// after lease release and primary presenter recovery.
func openChatModelPicker(session *ChatSession, request ModelPickerRequest) {
	if !canOpenChatModelPicker(session) {
		return
	}

	providerName := strings.TrimSpace(request.Provider)
	modelName := strings.TrimSpace(request.Model)

	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: "切换模型",
	})
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开模型选择器失败: %w", err)), false)
		return
	}
	if !session.Interaction.postUIAction(ui.OpenModelPicker{LeaseID: lease.ID()}) {
		_ = lease.Release(context.Background())
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("模型选择器状态未提交")), false)
		return
	}
	// Lifecycle barrier only: the first list frame sees the matching actor
	// state. Key navigation stays local to the fullscreen list.
	session.Interaction.waitUIActorIdle()

	// Stage 1: provider. Skipped when the request pinned one explicitly.
	if providerName == "" {
		providers := runtimeProviderSelectionOptions(session, currentModelCommandProvider(session))
		if len(providers) == 0 {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandTextResult("没有可用的 provider 配置"), false)
			return
		}
		picked, pickErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
			Title:        "选择 Provider",
			Subtitle:     "Enter 确认，Esc 取消",
			EmptyMessage: "没有匹配的 provider",
			ConfirmLabel: "使用选中 provider",
			Items:        buildModelProviderFullScreenItems(providers, currentModelCommandProvider(session)),
		}, lease)
		if pickErr != nil {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择 provider 失败: %w", pickErr)), false)
			return
		}
		if picked.Cancelled || picked.Index < 0 || picked.Index >= len(providers) {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandTextResult("已取消切换模型"), false)
			return
		}
		providerName = providers[picked.Index]
	}

	// Resolve the provider context so the model stage lists its real catalog.
	providerCtx, _, err := resolveModelCommandExecutionContext(session, providerName, "")
	if err != nil {
		closeModelPickerLease(session, lease)
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}

	// Stage 2: model. Skipped when the request pinned one explicitly.
	if modelName == "" {
		models := modelPickerModelOptions(providerCtx.Provider, currentModelForProvider(session, providerName))
		if len(models) == 0 {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandTextResult(fmt.Sprintf("provider %s 没有可用的模型", providerName)), false)
			return
		}
		picked, pickErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
			Title:        "选择模型",
			Subtitle:     fmt.Sprintf("provider: %s · Enter 确认，Esc 取消", providerName),
			EmptyMessage: "没有匹配的模型",
			ConfirmLabel: "使用选中模型",
			Items:        buildModelPickerModelItems(models, currentModelForProvider(session, providerName)),
		}, lease)
		if pickErr != nil {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择模型失败: %w", pickErr)), false)
			return
		}
		if picked.Cancelled || picked.Index < 0 || picked.Index >= len(models) {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandTextResult("已取消切换模型"), false)
			return
		}
		modelName = models[picked.Index]
	}

	// Stage 3: reasoning effort. Only when the caller asked for it and the
	// model card actually advertises a supported catalog.
	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if request.NeedReasoning {
		catalog := reasoningEffortCatalogForModel(providerCtx.Provider, modelName)
		if catalog.supported && len(catalog.options) > 0 {
			picked, pickErr := ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), ui.FullScreenListOptions{
				Title:        "选择 reasoning effort",
				Subtitle:     fmt.Sprintf("%s · Enter 确认，Esc 取消", modelName),
				EmptyMessage: "没有可用的 reasoning effort",
				ConfirmLabel: "使用选中值",
				Items:        buildModelPickerReasoningItems(catalog.options, reasoning),
			}, lease)
			if pickErr != nil {
				closeModelPickerLease(session, lease)
				_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择 reasoning effort 失败: %w", pickErr)), false)
				return
			}
			if picked.Cancelled || picked.Index < 0 || picked.Index >= len(catalog.options) {
				closeModelPickerLease(session, lease)
				_ = renderChatCommandResult(session, commandTextResult("已取消切换模型"), false)
				return
			}
			reasoning = catalog.options[picked.Index]
		}
	}

	_ = session.Interaction.postUIAction(ui.CloseModelPicker{LeaseID: lease.ID()})
	releaseErr := lease.Release(context.Background())
	// LeaseReleased is the primary recovery barrier. Do not mutate session
	// state until the actor has observed it, otherwise a replacement frame
	// could race the final alternate-screen exit/recovery transaction.
	session.Interaction.waitUIActorIdle()

	if releaseErr != nil {
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭模型选择器失败: %w", releaseErr)), false)
		return
	}

	finalCtx, _, err := resolveModelCommandExecutionContext(session, providerName, modelName)
	if err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}
	warnings := applyUnifiedModelCommandSelection(session, finalCtx, finalCtx.RequestedModel, reasoning)
	lines := []string{runtimeModelStateText(session)}
	if requested := strings.TrimSpace(finalCtx.RequestedModel); requested != "" &&
		!strings.EqualFold(requested, strings.TrimSpace(finalCtx.Model)) {
		lines = append(lines, fmt.Sprintf("提示: 模型已映射 %s -> %s", requested, finalCtx.Model))
	}
	doc := buildChatPlainTextCommandDocument(strings.Join(lines, "\n"))
	_ = renderChatCommandResult(session, commandResultWithWarnings(doc, warnings...), false)
}

func closeModelPickerLease(session *ChatSession, lease ui.ScreenLease) {
	_ = session.Interaction.postUIAction(ui.CloseModelPicker{LeaseID: lease.ID()})
	_ = lease.Release(context.Background())
	session.Interaction.waitUIActorIdle()
}

// currentModelForProvider returns the effective model to highlight when the
// picker browses models for the given provider.
func currentModelForProvider(session *ChatSession, providerName string) string {
	if session == nil {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(providerName), strings.TrimSpace(session.ProviderName)) {
		return effectiveRuntimeModel(session)
	}
	return ""
}

// modelPickerModelOptions lists the selectable models for an explicit provider,
// mirroring runtimeModelSelectionOptions without borrowing mutable session state.
func modelPickerModelOptions(provider config.Provider, current string) []string {
	seen := make(map[string]struct{})
	options := make([]string, 0, 1+len(provider.SupportedModels))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		options = append(options, value)
	}

	add(current)
	add(provider.DefaultModel)
	for _, candidate := range provider.SupportedModels {
		add(candidate)
	}

	sort.SliceStable(options, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(options[i]))
		right := strings.ToLower(strings.TrimSpace(options[j]))
		if left == right {
			return strings.TrimSpace(options[i]) < strings.TrimSpace(options[j])
		}
		return left < right
	})
	return options
}

func buildModelProviderFullScreenItems(providers []string, current string) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(providers))
	for _, provider := range providers {
		title := provider
		if strings.EqualFold(strings.TrimSpace(provider), strings.TrimSpace(current)) {
			title += "  (当前)"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     "provider",
			SearchText: "provider " + provider,
		})
	}
	return items
}

func buildModelPickerModelItems(models []string, current string) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(models))
	for _, model := range models {
		title := model
		if strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(current)) {
			title += "  (当前)"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     "model",
			SearchText: "model " + model,
		})
	}
	return items
}

func buildModelPickerReasoningItems(options []string, current string) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(options))
	for _, option := range options {
		title := option
		if strings.EqualFold(strings.TrimSpace(option), strings.TrimSpace(current)) {
			title += "  (当前)"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     "reasoning effort",
			SearchText: "reasoning " + option,
		})
	}
	return items
}

// applyUnifiedModelCommandSelection applies a resolved /model mutation without
// writing to stderr/stdout. It mirrors applyModelCommandSelection but collects
// sync/persist failures as warnings rendered through the unified result cell.
func applyUnifiedModelCommandSelection(session *ChatSession, providerCtx *providerExecutionContext, requestedModel, reasoning string) []error {
	var warnings []error
	if session == nil || providerCtx == nil {
		return []error{fmt.Errorf("当前没有活动会话")}
	}
	if err := applyChatExecutionContext(session, providerCtx, reasoning); err != nil {
		return []error{err}
	}
	session.RequestedProvider = strings.TrimSpace(providerCtx.ProviderName)
	session.RequestedModel = strings.TrimSpace(firstNonEmptyChatValue(requestedModel, providerCtx.RequestedModel, providerCtx.Model))
	session.RequestedReasoningEffort = runtimetypes.NormalizeReasoningEffort(reasoning)
	session.RouteWarnings = nil
	session.FallbackUsed = false
	session.FallbackReason = ""
	if err := syncRuntimeSessionFromChat(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换模型后同步会话失败: %w", err))
	}
	if err := refreshLocalRuntimeAfterModelSelection(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换模型后刷新本地运行时失败: %w", err))
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	if session.Config != nil {
		if err := persistChatPreferences(session.Config, session.ProviderName, session.Model, session.ReasoningEffort); err != nil {
			warnings = append(warnings, fmt.Errorf("保存 /model 偏好失败: %w", err))
		}
	}
	return warnings
}

// executeStructuredModelCommand is the unified interactive entry point for all
// /model variants. It owns the whole command:
//   - status → finite read-only document
//   - bare /model → typed picker effect (provider → model → reasoning)
//   - explicit mutation with reasoning pinned → direct apply + document
//   - explicit mutation needing reasoning interaction → typed picker that
//     skips the already-pinned provider/model stages and asks for reasoning
//
// When the picker is unavailable (non-TTY, no owned viewport) the mutation
// variants degrade to a direct apply that keeps the current reasoning value;
// bare /model falls back to a read-only status document.
func executeStructuredModelCommand(session *ChatSession, command string) (CommandResult, bool) {
	request, err := parseModelCommandRequest(command)
	if err != nil {
		return commandErrorResult(err), true
	}
	if request.ShowStatus && !request.HasMutation() {
		return commandTextResult(runtimeModelStateText(session)), true
	}
	if !request.HasMutation() {
		if !canOpenChatModelPicker(session) {
			return commandTextResult(runtimeModelStateText(session)), true
		}
		return CommandResult{
			Action: CommandContinue,
			OpenModelPicker: &ModelPickerRequest{
				NeedReasoning: true,
			},
		}, true
	}

	// Explicit mutation. When the model is pinned but reasoning is not, the
	// interactive flow asks for reasoning through the picker; otherwise apply.
	needsReasoningInteraction := request.ModelExplicit && !request.ReasoningExplicit && !request.ClearReasoning &&
		canOpenChatModelPicker(session)
	if needsReasoningInteraction {
		return CommandResult{
			Action: CommandContinue,
			OpenModelPicker: &ModelPickerRequest{
				Provider:      request.Provider,
				Model:         request.Model,
				NeedReasoning: true,
			},
		}, true
	}
	return executeStructuredModelMutation(session, request), true
}

// executeStructuredModelMutation applies an explicit /model mutation and
// renders one result cell. It never writes to stdout/stderr: sync and persist
// failures become warnings on the unified command cell.
func executeStructuredModelMutation(session *ChatSession, request modelCommandRequest) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}

	providerName := currentModelCommandProvider(session)
	modelName := effectiveRuntimeModel(session)
	if request.ProviderExplicit {
		providerName = request.Provider
	}
	if request.ModelExplicit {
		modelName = request.Model
	} else if request.ProviderExplicit {
		modelName = ""
	}

	providerCtx, _, err := resolveModelCommandExecutionContext(session, providerName, modelName)
	if err != nil {
		return commandErrorResult(err)
	}

	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if request.ReasoningExplicit {
		reasoning = request.ReasoningEffort
	} else if request.ClearReasoning {
		reasoning = ""
	}
	if request.ReasoningExplicit {
		var warning string
		reasoning, warning, err = resolveChatReasoningEffort(providerCtx.Provider, providerCtx.Model, reasoning, true)
		if err != nil {
			return commandErrorResult(err)
		}
		if warning != "" {
			return commandResultWithWarnings(
				buildChatPlainTextCommandDocument(runtimeModelStateText(session)),
				fmt.Errorf("%s", warning),
			)
		}
	}

	warnings := applyUnifiedModelCommandSelection(session, providerCtx, providerCtx.RequestedModel, reasoning)
	lines := []string{runtimeModelStateText(session)}
	if request.ModelExplicit {
		if requested := strings.TrimSpace(providerCtx.RequestedModel); requested != "" &&
			!strings.EqualFold(requested, strings.TrimSpace(providerCtx.Model)) {
			lines = append(lines, fmt.Sprintf("提示: 模型已映射 %s -> %s", requested, providerCtx.Model))
		}
	}
	return commandResultWithWarnings(buildChatPlainTextCommandDocument(strings.Join(lines, "\n")), warnings...)
}
