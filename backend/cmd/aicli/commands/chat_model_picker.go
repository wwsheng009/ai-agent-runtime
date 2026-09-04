package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// canOpenChatModelPicker is intentionally stricter than a generic list
// capability check. Switching the model mutates session state, so the picker
// may only begin while the unified primary presenter is idle, owns its
// viewport, and no competing popup or alternate screen owns input. It shares
// the common chat picker readiness gate with /login.
func canOpenChatModelPicker(session *ChatSession) bool {
	return chatPickerSurfaceReady(session)
}

// modelPickerLeaseHooks binds the model picker stages to their UI-actor
// barrier actions (OpenModelPicker/CloseModelPicker).
func modelPickerLeaseHooks() chatPickerLeaseHooks {
	return chatPickerLeaseHooks{
		Open: func(leaseID uint64) ui.UIAction {
			return ui.OpenModelPicker{LeaseID: leaseID}
		},
		Close: func(leaseID uint64) ui.UIAction {
			return ui.CloseModelPicker{LeaseID: leaseID}
		},
	}
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
	if err := reloadChatConfigForModelCommand(session); err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}

	providerName := strings.TrimSpace(request.Provider)
	modelName := strings.TrimSpace(request.Model)

	lease, err := chatPickerOpen(session, "切换模型", modelPickerLeaseHooks())
	if err != nil {
		switch {
		case errors.Is(err, errChatPickerStateUncommitted):
			err = fmt.Errorf("模型选择器状态未提交")
		case errors.Is(err, errChatPickerRenderNotReady):
			err = fmt.Errorf("模型选择器渲染未就绪")
		default:
			err = fmt.Errorf("打开模型选择器失败: %w", err)
		}
		_ = renderChatCommandResult(session, commandErrorResult(err), false)
		return
	}

	// Stage 1: provider. Skipped when the request pinned one explicitly.
	if providerName == "" {
		providers := runtimeProviderSelectionOptions(session, currentModelCommandProvider(session))
		if len(providers) == 0 {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandTextResult("没有可用的 provider 配置"), false)
			return
		}
		index, cancelled, pickErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
			Title:        "选择 Provider",
			Subtitle:     "Enter 确认，Esc 取消",
			EmptyMessage: "没有匹配的 provider",
			ConfirmLabel: "使用选中 provider",
			Items:        buildModelProviderFullScreenItems(providers, currentModelCommandProvider(session)),
		})
		if pickErr != nil {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择 provider 失败: %w", pickErr)), false)
			return
		}
		if cancelled {
			closeModelPickerLease(session, lease)
			_ = renderChatCommandResult(session, commandTextResult("已取消切换模型"), false)
			return
		}
		providerName = providers[index]
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
		for {
			models := modelPickerModelOptions(providerCtx.Provider, currentModelForProvider(session, providerName))
			if len(models) == 0 {
				closeModelPickerLease(session, lease)
				_ = renderChatCommandResult(session, commandTextResult(fmt.Sprintf("provider %s 没有可用的模型", providerName)), false)
				return
			}
			currentModel := currentModelForProvider(session, providerName)
			picked, pickErr := chatPickerStageResult(context.Background(), session, lease, ui.FullScreenListOptions{
				Title:        "选择模型",
				Subtitle:     fmt.Sprintf("provider: %s · Enter 确认，x/Delete 删除选中模型，Esc 取消", providerName),
				EmptyMessage: "没有匹配的模型",
				ConfirmLabel: "使用选中模型",
				Items:        buildModelPickerModelItems(models, currentModel),
				OnDelete:     func(int) error { return nil },
			})
			if pickErr != nil {
				closeModelPickerLease(session, lease)
				_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择模型失败: %w", pickErr)), false)
				return
			}
			if picked.Cancelled {
				closeModelPickerLease(session, lease)
				_ = renderChatCommandResult(session, commandTextResult("已取消切换模型"), false)
				return
			}
			if picked.DeleteRequested {
				if picked.Index < 0 || picked.Index >= len(models) {
					continue
				}
				target := models[picked.Index]
				if guardErr := chatModelRemovalGuard(providerCtx.Provider, currentModel, target); guardErr != nil {
					_ = renderChatCommandResult(session, commandTextResult(guardErr.Error()), false)
					continue
				}
				if !confirmChatModelDeletion(session, lease, providerName, target) {
					continue
				}
				if persistErr := persistChatModelRemoval(session.Config, providerName, target); persistErr != nil {
					_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("删除模型 %s 失败: %w", target, persistErr)), false)
					continue
				}
				_ = renderChatCommandResult(session, commandTextResult(fmt.Sprintf("已删除模型 %s（已保存到配置文件）", target)), false)
				// Reload so the reopened stage lists the persisted catalog.
				if reloadedCtx, _, reloadErr := resolveModelCommandExecutionContext(session, providerName, ""); reloadErr == nil {
					providerCtx = reloadedCtx
				} else {
					providerCtx.Provider.SupportedModels = filterChatProviderModels(providerCtx.Provider.SupportedModels, target)
				}
				continue
			}
			if picked.Index < 0 || picked.Index >= len(models) {
				continue
			}
			modelName = models[picked.Index]
			break
		}
	}

	// Stage 2 continued: the model stage may have removed the active session
	// model indirectly; keep the resolved context in sync before reasoning.
	if modelName == "" {
		modelName = currentModelForProvider(session, providerName)
	}

	// Stage 3: reasoning effort. Only when the caller asked for it and the
	// model card actually advertises a supported catalog.
	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if request.NeedReasoning {
		catalog := reasoningEffortCatalogForModel(providerCtx.Provider, modelName)
		if catalog.supported && len(catalog.options) > 0 {
			index, cancelled, pickErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
				Title:        "选择 reasoning effort",
				Subtitle:     fmt.Sprintf("%s · Enter 确认，Esc 取消", modelName),
				EmptyMessage: "没有可用的 reasoning effort",
				ConfirmLabel: "使用选中值",
				Items:        buildModelPickerReasoningItems(catalog.options, reasoning),
			})
			if pickErr != nil {
				closeModelPickerLease(session, lease)
				_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("选择 reasoning effort 失败: %w", pickErr)), false)
				return
			}
			if cancelled {
				closeModelPickerLease(session, lease)
				_ = renderChatCommandResult(session, commandTextResult("已取消切换模型"), false)
				return
			}
			reasoning = catalog.options[index]
		}
	}

	if closeErr := chatPickerClose(session, lease, modelPickerLeaseHooks()); closeErr != nil {
		if errors.Is(closeErr, errChatPickerActorNotIdle) {
			_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("模型选择器关闭未就绪")), false)
			return
		}
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("关闭模型选择器失败: %w", closeErr)), false)
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
	_ = chatPickerClose(session, lease, modelPickerLeaseHooks())
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
	values := make([]string, 0, 1+len(provider.SupportedModels))
	values = append(values, current, provider.DefaultModel)
	values = append(values, provider.SupportedModels...)
	return normalizeChatPickerOptions(values)
}

func buildModelProviderFullScreenItems(providers []string, current string) []ui.FullScreenListItem {
	return buildChatPickerItems(providers, current, "provider", "provider")
}

func buildModelPickerModelItems(models []string, current string) []ui.FullScreenListItem {
	return buildChatPickerItems(models, current, "model", "model")
}

// chatModelRemovalGuard rejects a model deletion that would break the current
// session or that targets an entry the picker does not own: runtime-derived
// entries (the provider default, a session override) are not part of the
// provider's managed supported_models list and stay untouched by /model.
func chatModelRemovalGuard(provider config.Provider, currentModel, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("无效的模型名")
	}
	if strings.EqualFold(strings.TrimSpace(currentModel), target) {
		return fmt.Errorf("模型 %s 正在使用中，不能删除", target)
	}
	for _, managed := range provider.SupportedModels {
		if strings.EqualFold(strings.TrimSpace(managed), target) {
			return nil
		}
	}
	return fmt.Errorf("模型 %s 不在 provider 的受管模型列表（supported_models）中，无法删除", target)
}

// confirmChatModelDeletion asks for explicit confirmation on the alternate
// screen. Raw-mode chat cannot accept free text, so the confirmation is a
// two-item full-screen list: [确认删除, 取消].
func confirmChatModelDeletion(session *ChatSession, lease ui.ScreenLease, providerName, model string) bool {
	picked, err := chatPickerStageResult(context.Background(), session, lease, ui.FullScreenListOptions{
		Title:        "删除模型",
		Subtitle:     fmt.Sprintf("将 %s 从 %s 的 supported_models 中移除并保存到配置文件", model, providerName),
		EmptyMessage: "没有可选项",
		ConfirmLabel: "确认删除",
		Items: []ui.FullScreenListItem{
			{Title: "确认删除 " + model, SearchText: "yes confirm delete 确认 删除"},
			{Title: "取消（返回模型列表）", SearchText: "cancel no 取消"},
		},
	})
	if err != nil || picked.Cancelled {
		return false
	}
	return picked.Index == 0
}

// persistChatModelRemoval removes the model from the provider's
// supported_models in the on-disk config and syncs the in-memory copy so the
// reopened picker stage lists the persisted catalog. When the removed model
// was the provider's default, the default reference is cleared alongside to
// avoid a dangling default_model node.
func persistChatModelRemoval(cfg *config.Config, providerName, model string) error {
	if cfg == nil {
		return fmt.Errorf("配置未加载")
	}
	canonical := ""
	var provider config.Provider
	for name, p := range cfg.Providers.Items {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(providerName)) {
			canonical, provider = name, p
			break
		}
	}
	if canonical == "" {
		return fmt.Errorf("provider %s 不存在", providerName)
	}
	kept := filterChatProviderModels(provider.SupportedModels, model)
	if len(kept) == len(provider.SupportedModels) {
		return fmt.Errorf("模型 %s 不在 %s 的 supported_models 中", model, canonical)
	}
	update := config.ProviderConfigUpdate{Name: canonical, SupportedModels: &kept}
	if strings.EqualFold(strings.TrimSpace(provider.DefaultModel), strings.TrimSpace(model)) {
		empty := ""
		update.DefaultModel = &empty
	}
	if _, err := config.UpdateProviderConfig(cfg.ConfigFilePath, update); err != nil {
		return err
	}
	provider.SupportedModels = kept
	if update.DefaultModel != nil {
		provider.DefaultModel = ""
	}
	cfg.Providers.Items[canonical] = provider
	return nil
}

// filterChatProviderModels returns a copy of models without target
// (case-insensitive compare), preserving order.
func filterChatProviderModels(models []string, target string) []string {
	kept := make([]string, 0, len(models))
	for _, m := range models {
		if !strings.EqualFold(strings.TrimSpace(m), strings.TrimSpace(target)) {
			kept = append(kept, m)
		}
	}
	return kept
}

func buildModelPickerReasoningItems(options []string, current string) []ui.FullScreenListItem {
	return buildChatPickerItems(options, current, "reasoning effort", "reasoning")
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
	if err := reloadChatConfigForModelCommand(session); err != nil {
		return commandErrorResult(err)
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
