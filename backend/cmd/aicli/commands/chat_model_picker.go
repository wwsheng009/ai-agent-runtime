package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// modelCommandVariant selects the picker behaviour shared by /model and
// /provider. Both commands mutate the same model state; they differ only in
// whether a bare invocation leads with the provider selection stage.
type modelCommandVariant int

const (
	// modelCommandVariantModel is the /model command: switch the model within
	// the current provider (plus optional reasoning).
	modelCommandVariantModel modelCommandVariant = iota
	// modelCommandVariantProvider is the /provider command: switch the
	// provider (then pick its model and reasoning).
	modelCommandVariantProvider
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

	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)

	// 批次 2：租约、open 屏障与 close 序列收敛到统一副屏框架；provider→model→
	// reasoning 三级选择（含模型删除的确认与重开）仍在同一租约内推进，选择结果
	// 与会话变更全部在租约释放后应用（I3）。
	var early *CommandResult
	res := runChatPickerScreen(session, chatPickerScreen{
		ID:    "model.picker",
		Title: "切换模型",
		Hooks: modelPickerLeaseHooks(),
		Run: func(session *ChatSession, lease ui.ScreenLease) error {

			// Stage 1: provider. Only the typed /provider command runs it (bare
			// form), and a request that pinned the provider explicitly skips it. The
			// typed /model command never asks for a provider: it switches models
			// within the current provider.
			if request.ProviderPicker && providerName == "" {
				providers := runtimeProviderSelectionOptions(session, currentModelCommandProvider(session))
				if len(providers) == 0 {
					result := commandTextResult("没有可用的 provider 配置")
					early = &result
					return nil
				}
				index, cancelled, pickErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
					Title:        "选择 Provider",
					Subtitle:     "Enter 确认，Esc 取消",
					EmptyMessage: "没有匹配的 provider",
					ConfirmLabel: "使用选中 provider",
					Items:        buildModelProviderFullScreenItems(providers, currentModelCommandProvider(session)),
				})
				if pickErr != nil {
					result := commandErrorResult(fmt.Errorf("选择 provider 失败: %w", pickErr))
					early = &result
					return nil
				}
				if cancelled {
					result := commandTextResult("已取消切换模型")
					early = &result
					return nil
				}
				providerName = providers[index]
			}

			// /model never runs the provider stage (ProviderPicker is false), so a bare
			// invocation leaves providerName empty. Resolve it against the session's
			// current provider — the one selected by /provider or already active — so
			// the model stage lists that provider's catalog instead of falling back to
			// the config default provider.
			if providerName == "" {
				providerName = currentModelCommandProvider(session)
			}

			// Resolve the provider context so the model stage lists its real catalog.
			providerCtx, _, err := resolveModelCommandExecutionContext(session, providerName, "")
			if err != nil {
				result := commandErrorResult(err)
				early = &result
				return nil
			}

			// Stage 2: model. Skipped when the request pinned one explicitly.
			if modelName == "" {
				for {
					models := modelPickerModelOptions(providerCtx.Provider, currentModelForProvider(session, providerName))
					if len(models) == 0 {
						result := commandTextResult(fmt.Sprintf("provider %s 没有可用的模型", providerName))
						early = &result
						return nil
					}
					currentModel := currentModelForProvider(session, providerName)
					pickerList := buildModelPickerItemsWithAddRow(models, currentModel)
					picked, pickErr := chatPickerStageResult(context.Background(), session, lease, ui.FullScreenListOptions{
						Title:        "选择模型",
						Subtitle:     fmt.Sprintf("provider: %s · Enter 确认，Delete 删除选中模型，Esc 取消", providerName),
						EmptyMessage: "没有匹配的模型",
						ConfirmLabel: "使用选中模型",
						Items:        pickerList.items,
						OnDelete:     func(int) error { return nil },
					})
					if pickErr != nil {
						result := commandErrorResult(fmt.Errorf("选择模型失败: %w", pickErr))
						early = &result
						return nil
					}
					if picked.Cancelled {
						result := commandTextResult("已取消切换模型")
						early = &result
						return nil
					}
					if picked.Index == pickerList.addIndex {
						// 自由文本录入模型 id。模型 id 不含空白，所以按空白/逗号切分
						// 不会歧义，一次粘贴可加多个（与 Web 端 provider 编辑器一致）。
						// 校验失败保持输入框打开并就地说明原因，不丢用户已输入的内容。
						typed, cancelled, inputErr := chatPickerFreeTextStage(context.Background(), session, lease, ui.FullScreenListOptions{
							Title:        "添加模型",
							Subtitle:     fmt.Sprintf("provider: %s · 输入模型 id 后 Enter 保存到配置文件，Esc 取消", providerName),
							ConfirmLabel: "添加并保存",
							FreeTextHint: "可粘贴多个，空格或逗号分隔",
							OnConfirmText: func(text string) error {
								return chatModelAdditionTextError(providerCtx.Provider, text)
							},
						})
						if inputErr != nil {
							result := commandErrorResult(fmt.Errorf("添加模型失败: %w", inputErr))
							early = &result
							return nil
						}
						if cancelled {
							// 放弃添加 ≠ 放弃整个命令：回到模型列表。
							continue
						}
						added, skipped := splitChatModelAdditions(typed, providerCtx.Provider)
						if persistErr := persistChatModelAddition(session.Config, providerName, added); persistErr != nil {
							_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("添加模型失败: %w", persistErr)), false)
							continue
						}
						// 点名跳过项，不静默丢弃：用户粘的是一份清单，少了一个要看得见。
						summary := fmt.Sprintf("已添加模型 %s（已保存到配置文件）", strings.Join(added, "、"))
						if len(skipped) > 0 {
							summary += fmt.Sprintf("；已存在跳过 %s", strings.Join(skipped, "、"))
						}
						_ = renderChatCommandResult(session, commandTextResult(summary), false)
						// 与删除同构：刷新上下文后重开列表，让新模型出现在原位。
						if reloadedCtx, _, reloadErr := resolveModelCommandExecutionContext(session, providerName, ""); reloadErr == nil {
							providerCtx = reloadedCtx
						}
						continue
					}
					if picked.DeleteRequested {
						// The add row occupies index 0, so a raw index would both
						// shift every model by one and panic on the add row itself
						// (models[-1]). Map through the offset first.
						modelIndex := picked.Index - pickerList.optionIndexOff
						if modelIndex < 0 || modelIndex >= len(models) {
							continue
						}
						target := models[modelIndex]
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
					modelIndex := picked.Index - pickerList.optionIndexOff
					if modelIndex < 0 || modelIndex >= len(models) {
						continue
					}
					modelName = models[modelIndex]
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
			reasoning = runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
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
						result := commandErrorResult(fmt.Errorf("选择 reasoning effort 失败: %w", pickErr))
						early = &result
						return nil
					}
					if cancelled {
						result := commandTextResult("已取消切换模型")
						early = &result
						return nil
					}
					reasoning = catalog.options[index]
				}
			}

			return nil
		},
	})
	if res.Degraded {
		// 框架未进入副屏（能力不足/租约忙/嵌套）：与批次 2 之前取租约失败的
		// 文案一致。
		_ = renderChatCommandResult(session, commandErrorResult(fmt.Errorf("打开模型选择器失败: %w", errChatPickerScreenUnavailable)), false)
		return
	}
	if early != nil {
		// 取消/无候选/删除反馈等提前退出：与 legacy 一致，close 阶段错误不覆盖
		// 用户可见结果；此时框架已释放租约。
		_ = renderChatCommandResult(session, *early, false)
		return
	}
	if res.Err != nil {
		_ = renderChatCommandResult(session, commandErrorResult(chatPickerScreenErrorText("模型选择器", res)), false)
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

// chatModelPickerAddRowTitle is the leading row that opens the add-model input.
const chatModelPickerAddRowTitle = "＋ 添加模型（手动输入 id）"

// modelPickerList is the model picker's rows plus the offset needed to map a
// picker item index back to an entry in models. The add row is deliberately
// prepended (mirroring the /login provider stage) so adding is the first
// action, independent of the model catalog or the current filter.
//
// Adding is a row rather than a key on purpose: type-to-filter owns every
// printable character, and 'a' is common in model ids (llama, audio, large).
// Reserving it would force a "/" prefix that nothing advertises, which reads
// as a dead key rather than a documented escape hatch.
type modelPickerList struct {
	items          []ui.FullScreenListItem
	addIndex       int
	optionIndexOff int
}

func buildModelPickerItemsWithAddRow(models []string, current string) modelPickerList {
	items := buildChatPickerItems(models, current, "model", "model")
	addItem := ui.FullScreenListItem{
		Title:      chatModelPickerAddRowTitle,
		Detail:     "model",
		SearchText: "create new model 添加模型 新建",
	}
	return modelPickerList{
		items:          append([]ui.FullScreenListItem{addItem}, items...),
		addIndex:       0,
		optionIndexOff: 1,
	}
}

func buildModelPickerModelItems(models []string, current string) []ui.FullScreenListItem {
	return buildModelPickerItemsWithAddRow(models, current).items
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
	canonical, provider, err := findChatProviderConfig(cfg, providerName)
	if err != nil {
		return err
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

// splitChatModelAdditions parses the free-text input of the add stage into
// candidate model ids. Model ids never contain whitespace, so splitting on it
// (and on commas, for parity with the web provider editor) is unambiguous and
// lets one paste add several models. Candidates are de-duplicated
// case-insensitively and keep input order; the second return value is the ones
// already present in the provider, which are reported rather than silently
// dropped.
func splitChatModelAdditions(text string, provider config.Provider) (add []string, skipped []string) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == ','
	})
	seen := make(map[string]bool, len(fields))
	present := make(map[string]bool, len(provider.SupportedModels))
	present[strings.ToLower(strings.TrimSpace(provider.DefaultModel))] = true
	for _, m := range provider.SupportedModels {
		present[strings.ToLower(strings.TrimSpace(m))] = true
	}
	for _, f := range fields {
		key := strings.ToLower(f)
		if seen[key] {
			continue
		}
		seen[key] = true
		if present[key] {
			skipped = append(skipped, f)
			continue
		}
		add = append(add, f)
	}
	return add, skipped
}

// chatModelAdditionTextError is the free-text stage validator. A non-nil error
// keeps the stage open and shows the reason inline, so the user's typing is
// never lost. Only a fully-redundant input is rejected; partial duplicates are
// accepted and named in the result line, because refusing the whole batch over
// one duplicate is hostile when pasting a long list.
func chatModelAdditionTextError(provider config.Provider, text string) error {
	add, skipped := splitChatModelAdditions(text, provider)
	if len(add) == 0 && len(skipped) == 0 {
		return fmt.Errorf("请输入要添加的模型 id")
	}
	if len(add) == 0 {
		return fmt.Errorf("模型 %s 已存在，无需重复添加", strings.Join(skipped, "、"))
	}
	return nil
}

// persistChatModelAddition appends the model ids to the provider's
// supported_models in the on-disk config and syncs the in-memory copy so the
// reopened picker stage lists the persisted catalog. Order is preserved and
// the provider default is left alone: adding a model must not silently change
// which model is active.
func persistChatModelAddition(cfg *config.Config, providerName string, added []string) error {
	if cfg == nil {
		return fmt.Errorf("配置未加载")
	}
	canonical, provider, err := findChatProviderConfig(cfg, providerName)
	if err != nil {
		return err
	}
	if len(added) == 0 {
		return fmt.Errorf("没有要添加的模型")
	}
	next := make([]string, 0, len(provider.SupportedModels)+len(added))
	next = append(next, provider.SupportedModels...)
	seen := make(map[string]bool, len(next))
	for _, m := range next {
		seen[strings.ToLower(strings.TrimSpace(m))] = true
	}
	appended := make([]string, 0, len(added))
	for _, m := range added {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		key := strings.ToLower(m)
		if seen[key] {
			continue
		}
		seen[key] = true
		next = append(next, m)
		appended = append(appended, m)
	}
	if len(appended) == 0 {
		return fmt.Errorf("模型 %s 已存在，无需重复添加", strings.Join(added, "、"))
	}
	if _, err := config.UpdateProviderConfig(cfg.ConfigFilePath,
		config.ProviderConfigUpdate{Name: canonical, SupportedModels: &next}); err != nil {
		return err
	}
	provider.SupportedModels = next
	cfg.Providers.Items[canonical] = provider
	return nil
}

// findChatProviderConfig resolves a provider name to its canonical map key and
// value, case-insensitively. Shared by the add/remove persistence paths.
func findChatProviderConfig(cfg *config.Config, providerName string) (string, config.Provider, error) {
	if cfg == nil {
		return "", config.Provider{}, fmt.Errorf("配置未加载")
	}
	for name, p := range cfg.Providers.Items {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(providerName)) {
			return name, p, nil
		}
	}
	return "", config.Provider{}, fmt.Errorf("provider %s 不存在", providerName)
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
	before := snapshotChatRuntimeSelection(session)
	if err := applyChatExecutionContext(session, providerCtx, reasoning); err != nil {
		return []error{err}
	}
	session.RequestedProvider = strings.TrimSpace(providerCtx.ProviderName)
	session.RequestedModel = strings.TrimSpace(firstNonEmptyChatValue(requestedModel, providerCtx.RequestedModel, providerCtx.Model))
	session.RequestedReasoningEffort = runtimetypes.NormalizeReasoningEffort(reasoning)
	session.RouteWarnings = nil
	session.FallbackUsed = false
	session.FallbackReason = ""
	// 与 applyModelCommandSelection 一致：同目标重选也要让缓存窗口与最新配置
	// 能力对账（例如配置从 128K 上调到 1M 后重选同一模型）。
	reconcileChatSessionAfterConfigReload(session)
	if err := syncRuntimeSessionFromChat(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换模型后同步会话失败: %w", err))
	}
	if err := refreshLocalRuntimeAfterSelection(session, before.changed(session), chatActorRebuildReasonModelSelection); err != nil {
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
	return executeStructuredModelCommandVariant(session, command, modelCommandVariantModel)
}

// executeStructuredProviderCommand is the unified interactive entry point for
// /provider. It shares the /model executor and opens the leading provider
// selection stage on a bare invocation.
func executeStructuredProviderCommand(session *ChatSession, command string) (CommandResult, bool) {
	return executeStructuredModelCommandVariant(session, command, modelCommandVariantProvider)
}

// needProviderPickerStage reports whether the interactive flow must run the
// provider selection stage: only the /provider command asks for it, and only
// when no provider was pinned explicitly.
func needProviderPickerStage(variant modelCommandVariant, providerExplicit bool) bool {
	return variant == modelCommandVariantProvider && !providerExplicit
}

// modelStatusCommandResult 在统一出口把 /model|/provider 的只读状态页投影为
// ScreenDocument（批次 3 尾批）；plain/JSON/legacy 出口保持原文本单元格。
func modelStatusCommandResult(session *ChatSession, variant modelCommandVariant, text string) CommandResult {
	if unifiedDirectInteractiveOutput(session) {
		return chatScreenDocResult(chatScreenModelStatusSpec(variant, text))
	}
	return commandTextResult(text)
}

func executeStructuredModelCommandVariant(session *ChatSession, command string, variant modelCommandVariant) (CommandResult, bool) {
	request, err := parseModelCommandRequest(command)
	if err != nil {
		return commandErrorResult(err), true
	}
	request.Provider = resolveModelPickerProvider(session, variant, request)
	if request.ShowStatus && !request.HasMutation() {
		return modelStatusCommandResult(session, variant, runtimeModelStateText(session)), true
	}
	if !request.HasMutation() {
		if !canOpenChatModelPicker(session) {
			return modelStatusCommandResult(session, variant, runtimeModelStateText(session)), true
		}
		pickerRequest := ModelPickerRequest{
			Provider:       request.Provider,
			NeedReasoning:  true,
			ProviderPicker: needProviderPickerStage(variant, request.ProviderExplicit),
		}
		return CommandResult{
			Action: CommandContinue,
			Screen: chatScreenEffectSpec("model.picker", "切换模型", func(s *ChatSession) {
				openChatModelPicker(s, pickerRequest)
			}),
		}, true
	}

	// Explicit mutation. When the model is pinned but reasoning is not, the
	// interactive flow asks for reasoning through the picker; otherwise apply.
	// DirectApply（web 注入）跳过该交互：注入方无法驱动 TUI 键盘，
	// 弹 picker 会让 TUI 卡在全屏选择器、web 端轮询超时。
	needsReasoningInteraction := request.ModelExplicit && !request.ReasoningExplicit && !request.ClearReasoning &&
		!request.DirectApply && canOpenChatModelPicker(session)
	if needsReasoningInteraction {
		pickerRequest := ModelPickerRequest{
			Provider:       request.Provider,
			Model:          request.Model,
			NeedReasoning:  true,
			ProviderPicker: needProviderPickerStage(variant, request.ProviderExplicit),
		}
		return CommandResult{
			Action: CommandContinue,
			Screen: chatScreenEffectSpec("model.picker", "切换模型", func(s *ChatSession) {
				openChatModelPicker(s, pickerRequest)
			}),
		}, true
	}
	return executeStructuredModelMutation(session, request), true
}

// resolveModelPickerProvider pins the provider the interactive model stage
// must run against. /model is model-only: unless the caller pinned a provider
// explicitly, it operates within the session's current provider — the one
// selected by /provider or already active — never the config default provider.
// /provider only pin Provider when it was explicit; a bare invocation stays
// empty so the leading provider stage selects it.
func resolveModelPickerProvider(session *ChatSession, variant modelCommandVariant, request modelCommandRequest) string {
	if variant == modelCommandVariantModel && !request.ProviderExplicit {
		return currentModelCommandProvider(session)
	}
	return strings.TrimSpace(request.Provider)
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
