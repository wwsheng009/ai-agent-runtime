package commands

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerops"
)

// ---------------------------------------------------------------------------
// /provider refresh —— /provider 的二级子命令，重新拉取 provider 的 /models
// 端点并刷新 supported_models / model_capabilities / models_verified_at。
//
// 与 CLI `aicli provider refresh-models` 的分工：
//   - CLI 命令只刷新 model_capabilities（端点优先合并，支持的入口是 web 的
//     「刷新模型」按钮）；
//   - 本命令对齐 web 的「获取模型列表 + 保存」：同时重写 supported_models
//     （按协议分类取主组，退化时取全量）与 capabilities，并同步回活动会话，
//     因此模型选择器与下一回合请求立即看到新列表。
//
// 用法：
//
//	/provider refresh               # 刷新当前会话 provider
//	/provider refresh openrouter    # 刷新指定 provider（可多个）
//	/provider refresh --all         # 刷新全部 provider
//	/provider refresh --dry-run     # 只预览变更，不写配置
//	/provider refresh --models-path /api/v1/models --timeout 30
// ---------------------------------------------------------------------------

const chatProviderRefreshToken = "refresh"

// chatProviderRefreshRequest 是 /provider refresh 的解析结果。
type chatProviderRefreshRequest struct {
	Names      []string
	All        bool
	DryRun     bool
	ModelsPath string
	TimeoutSec int
}

// parseChatProviderRefreshCommand 只识别 `/provider refresh ...`。第一个 token
// 不是 refresh 时返回 ok=false，调用方继续
// 走既有的 provider / status 解析路径——必须保证 "refresh" 永远不会被
// parseModelCommandRequest 当成 provider 名或模型名。
func parseChatProviderRefreshCommand(command string) (chatProviderRefreshRequest, bool, error) {
	request := chatProviderRefreshRequest{}
	tokens := executor.SplitCommandTokens(command)
	if len(tokens) < 2 {
		return request, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(tokens[1]), chatProviderRefreshToken) {
		return request, false, nil
	}

	for i := 2; i < len(tokens); i++ {
		token := strings.TrimSpace(tokens[i])
		if token == "" {
			continue
		}
		switch {
		case token == "--all":
			request.All = true
		case token == "--dry-run":
			request.DryRun = true
		case token == "--models-path":
			value, next, err := consumeModelCommandValue(tokens, i)
			if err != nil {
				return request, true, err
			}
			request.ModelsPath = value
			i = next
		case strings.HasPrefix(token, "--models-path="):
			request.ModelsPath = strings.TrimSpace(strings.TrimPrefix(token, "--models-path="))
		case token == "--timeout":
			value, next, err := consumeModelCommandValue(tokens, i)
			if err != nil {
				return request, true, err
			}
			seconds, convErr := strconv.Atoi(strings.TrimSpace(value))
			if convErr != nil || seconds < 0 {
				return request, true, fmt.Errorf("--timeout 需要非负整数秒数")
			}
			request.TimeoutSec = seconds
			i = next
		case strings.HasPrefix(token, "--timeout="):
			seconds, convErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(token, "--timeout=")))
			if convErr != nil || seconds < 0 {
				return request, true, fmt.Errorf("--timeout 需要非负整数秒数")
			}
			request.TimeoutSec = seconds
		default:
			if strings.HasPrefix(token, "-") {
				return request, true, fmt.Errorf("未知的 /provider refresh 参数: %s", token)
			}
			request.Names = append(request.Names, token)
		}
	}
	return request, true, nil
}

// chatProviderRefreshProviderOutcome 是单个 provider 的刷新结果，用于渲染。
type chatProviderRefreshProviderOutcome struct {
	Name          string
	Protocol      string
	Endpoint      string
	Category      string
	LoginProtocol string
	ModelCount    int
	Added         []string
	Removed       []string
	ChangedModels int
	VerifiedAt    string
	Reason        string // updated | already_up_to_date | fetch_failed: ... | persist_failed: ...
}

// chatProviderRefreshRun 是刷新流程的结构化结果：outcomes 供报告渲染，totals
// 与 Target 供汇总行使用。模型列表首行「刷新」据此拼一行摘要，/provider refresh
// 据此渲染完整报告，两者语义同源。
type chatProviderRefreshRun struct {
	Outcomes []chatProviderRefreshProviderOutcome
	Target   string
	Added    int
	Removed  int
	Changed  int
}

// chatProviderRefreshProviders 拉取目标 provider 的 /models 并写回配置。第二个
// 返回值是若干非致命警告（单个 provider 拉取/写入失败、配置重载失败）。
func chatProviderRefreshProviders(session *ChatSession, request chatProviderRefreshRequest) (chatProviderRefreshRun, []error, error) {
	if session == nil {
		return chatProviderRefreshRun{}, nil, fmt.Errorf("当前没有活动会话")
	}
	if session.Config == nil {
		return chatProviderRefreshRun{}, nil, fmt.Errorf("provider 配置不可用")
	}
	cfg := session.Config

	names := append([]string(nil), request.Names...)
	targetLabel := ""
	if len(names) == 0 && !request.All {
		current := strings.TrimSpace(currentModelCommandProvider(session))
		if current == "" {
			return chatProviderRefreshRun{}, nil, fmt.Errorf("未指定 provider：请用 /provider refresh <name>、--all，或先切换 provider")
		}
		names = []string{current}
		targetLabel = current
	}

	selected, err := selectProvidersForRefresh(cfg, names, request.All, "")
	if err != nil {
		return chatProviderRefreshRun{}, nil, err
	}
	configPath, err := providerCommandConfigPath(cfg)
	if err != nil {
		return chatProviderRefreshRun{}, nil, err
	}

	timeout := time.Duration(request.TimeoutSec) * time.Second
	outcomes := make([]chatProviderRefreshProviderOutcome, 0, len(selected))
	warnings := make([]error, 0)
	updatedProviders := make([]string, 0, len(selected))
	totalAdded, totalRemoved, totalChanged := 0, 0, 0

	for _, item := range selected {
		loginProtocol := loginProtocolFromProvider(item.Provider, item.Provider.AuthMode)
		outcome := chatProviderRefreshProviderOutcome{
			Name:          item.Name,
			Protocol:      item.Provider.GetProtocol(),
			LoginProtocol: loginProtocol,
		}

		fetch, fetchErr := providerops.FetchModels(context.Background(), providerops.FetchModelsRequest{
			Config:        cfg,
			ProviderName:  item.Name,
			Provider:      item.Provider,
			LoginProtocol: loginProtocol,
			ModelsPath:    request.ModelsPath,
			Timeout:       timeout,
		})
		if fetchErr != nil {
			outcome.Reason = "fetch_failed: " + fetchErr.Error()
			warnings = append(warnings, fmt.Errorf("%s: 拉取模型列表失败: %v", item.Name, fetchErr))
			outcomes = append(outcomes, outcome)
			continue
		}

		outcome.Endpoint = fetch.Endpoint
		outcome.Category = providerops.ModelListCategoryName(fetch.Category)
		outcome.VerifiedAt = strings.TrimSpace(fetch.VerifiedAt)

		fetchedIDs := chatProviderRefreshFetchedModelIDs(item.Name, item.Provider, loginProtocol, fetch.Models, cfg)
		outcome.ModelCount = len(fetchedIDs)
		nextSupported := chatProviderRefreshModelList(fetchedIDs, item.Provider.DefaultModel)
		outcome.Added, outcome.Removed = diffProviderModelLists(item.Provider.SupportedModels, nextSupported)

		build := refreshProviderModelCapabilitiesFromEndpoint(item.Name, loginProtocol, item.Provider, fetch.Models)
		outcome.ChangedModels = build.summary.ChangedModels

		listsChanged := len(outcome.Added) > 0 || len(outcome.Removed) > 0
		if !listsChanged && !build.summary.Updated {
			outcome.Reason = "already_up_to_date"
			outcomes = append(outcomes, outcome)
			continue
		}

		totalAdded += len(outcome.Added)
		totalRemoved += len(outcome.Removed)
		totalChanged += build.summary.ChangedModels

		if request.DryRun {
			outcome.Reason = "updated"
			outcomes = append(outcomes, outcome)
			continue
		}

		capabilities := cloneProviderLoginModelCapabilities(build.nextCapabilities)
		if capabilities == nil {
			capabilities = map[string]config.ModelCapabilitySpec{}
		}
		supported := nextSupported
		update := config.ProviderConfigUpdate{
			Name:              item.Name,
			SupportedModels:   &supported,
			ModelCapabilities: &capabilities,
		}
		if outcome.VerifiedAt != "" {
			verifiedAt := outcome.VerifiedAt
			update.ModelsVerifiedAt = &verifiedAt
		}
		if strings.TrimSpace(item.Provider.DefaultModel) == "" && len(nextSupported) > 0 {
			first := nextSupported[0]
			update.DefaultModel = &first
		}

		persisted, persistErr := config.UpdateProviderConfig(configPath, update)
		if persistErr != nil {
			outcome.Reason = "persist_failed: " + persistErr.Error()
			warnings = append(warnings, fmt.Errorf("%s: 写入配置失败: %v", item.Name, persistErr))
			outcomes = append(outcomes, outcome)
			continue
		}
		switch {
		case persisted != nil:
			applyProviderLoginConfigUpdate(cfg, item.Name, *persisted, false)
		default:
			if current, ok := cfg.Providers.Items[item.Name]; ok {
				current.SupportedModels = supported
				current.ModelCapabilities = capabilities
				current.ModelsVerifiedAt = outcome.VerifiedAt
				applyProviderLoginConfigUpdate(cfg, item.Name, current, false)
			}
		}
		updatedProviders = append(updatedProviders, item.Name)
		outcome.Reason = "updated"
		outcomes = append(outcomes, outcome)
	}

	if len(updatedProviders) > 0 {
		if syncErr := refreshChatSessionAfterProviderModelRefresh(session, updatedProviders); syncErr != nil {
			warnings = append(warnings, syncErr)
		}
	}

	return chatProviderRefreshRun{
		Outcomes: outcomes,
		Target:   targetLabel,
		Added:    totalAdded,
		Removed:  totalRemoved,
		Changed:  totalChanged,
	}, warnings, nil
}

// runChatProviderRefresh 把刷新结果渲染成 /provider refresh 的报告文本行。
func runChatProviderRefresh(session *ChatSession, request chatProviderRefreshRequest) ([]string, []error, error) {
	run, warnings, err := chatProviderRefreshProviders(session, request)
	if err != nil {
		return nil, nil, err
	}
	lines := renderChatProviderRefreshOutcome(run.Outcomes, request, run.Target, run.Added, run.Removed, run.Changed)
	return lines, warnings, nil
}

// chatProviderRefreshFetchedModelIDs 复刻 web「获取模型列表」的模型选择语义：
// 按协议分类取主组；仅 openai 协议下并入 assumed（无 model card 证据但 /v1/models
// 几乎必然支持 chat）——其他协议下 assumed 会得到错误列表，故只取 verified。
// 分类不可用时退化为全量清单（旧行为）。
func chatProviderRefreshFetchedModelIDs(
	providerName string,
	provider config.Provider,
	loginProtocol string,
	models []providerModelInfo,
	cfg *config.Config,
) []string {
	groupingProtocol := resolveProviderFetchModelsLoginProtocol(loginProtocol, provider)
	if classification := classifyProviderFetchedModels(providerName, provider, groupingProtocol, groupingProtocol, models, cfg); classification != nil {
		selected := classification.PrimaryModels
		if !strings.EqualFold(classification.RuntimeProtocol, "openai") && len(classification.AssumedModels) > 0 {
			selected = classification.VerifiedModels
		}
		return providerops.ModelIDs(selected)
	}
	return providerops.ModelIDs(dedupeProviderModels(models))
}

// chatProviderRefreshModelList 生成下一版 supported_models：端点清单去重后保留
// 已配置的 default_model（与 web「获取模型列表」一致：默认模型即使不在端点清单
// 里也不丢，避免刷新把用户选定的模型抹掉）。
func chatProviderRefreshModelList(fetched []string, defaultModel string) []string {
	out := make([]string, 0, len(fetched)+1)
	seen := make(map[string]struct{}, len(fetched)+1)
	appendModel := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}
	for _, id := range fetched {
		appendModel(id)
	}
	appendModel(defaultModel)
	return out
}

// diffProviderModelLists 返回新增 / 移除的模型（大小写不敏感）。
func diffProviderModelLists(previous, next []string) (added, removed []string) {
	previousSet := make(map[string]string, len(previous))
	for _, id := range previous {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			previousSet[strings.ToLower(trimmed)] = trimmed
		}
	}
	nextSet := make(map[string]string, len(next))
	for _, id := range next {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			nextSet[strings.ToLower(trimmed)] = trimmed
		}
	}
	for key, id := range nextSet {
		if _, exists := previousSet[key]; !exists {
			added = append(added, id)
		}
	}
	for key, id := range previousSet {
		if _, exists := nextSet[key]; !exists {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// refreshChatSessionAfterProviderModelRefresh 把刚写入的 provider 配置同步回活动
// 会话：重读配置 → 若刷新的是当前 provider 则对账会话状态 → 同步运行时会话快照。
// 刷新的 provider 与会话无关时只重读配置，不触碰当前选择。
func refreshChatSessionAfterProviderModelRefresh(session *ChatSession, updated []string) error {
	if session == nil {
		return nil
	}
	if err := reloadChatConfigForModelCommand(session); err != nil {
		return fmt.Errorf("provider 模型列表已更新，但重载会话配置失败: %w", err)
	}
	active := strings.TrimSpace(currentModelCommandProvider(session))
	if active == "" {
		return nil
	}
	touched := false
	for _, name := range updated {
		if strings.EqualFold(strings.TrimSpace(name), active) {
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}
	reconcileChatSessionAfterConfigReload(session)
	warnIfChatSessionSyncFails(session, "refresh provider models", syncRuntimeSessionFromChat(session))
	return nil
}

// renderChatProviderRefreshOutcome 渲染 /provider refresh 的结果文本。
func renderChatProviderRefreshOutcome(
	outcomes []chatProviderRefreshProviderOutcome,
	request chatProviderRefreshRequest,
	targetLabel string,
	added, removed, changed int,
) []string {
	mode := "已更新"
	if request.DryRun {
		mode = "预览（--dry-run，未写入配置）"
	}
	lines := []string{fmt.Sprintf("provider 模型列表刷新（%s）", mode)}
	if targetLabel != "" {
		lines = append(lines, "目标: "+targetLabel)
	}
	lines = append(lines, fmt.Sprintf("providers=%d  新增模型=%d  移除模型=%d  能力变更=%d",
		len(outcomes), added, removed, changed))

	for _, outcome := range outcomes {
		status := emptyIfBlank(outcome.Reason)
		switch {
		case outcome.Reason == "updated" && request.DryRun:
			status = "would_update"
		case outcome.Reason == "":
			status = "unchanged"
		}
		lines = append(lines, fmt.Sprintf("- %s [%s] protocol=%s category=%s endpoint=%s",
			outcome.Name,
			status,
			emptyIfBlank(outcome.Protocol),
			emptyIfBlank(outcome.Category),
			emptyIfBlank(outcome.Endpoint),
		))
		if strings.HasPrefix(outcome.Reason, "fetch_failed") {
			continue
		}
		lines = append(lines, fmt.Sprintf("    模型: %d 个（新增 %d / 移除 %d），能力变更 %d 个",
			outcome.ModelCount, len(outcome.Added), len(outcome.Removed), outcome.ChangedModels))
		if len(outcome.Added) > 0 {
			lines = append(lines, "    新增: "+strings.Join(outcome.Added, ", "))
		}
		if len(outcome.Removed) > 0 {
			lines = append(lines, "    移除: "+strings.Join(outcome.Removed, ", "))
		}
		if outcome.VerifiedAt != "" && !strings.HasPrefix(outcome.Reason, "persist_failed") {
			lines = append(lines, "    verified_at: "+outcome.VerifiedAt)
		}
	}
	lines = append(lines, "提示: 端点只覆盖它能声明的字段，本地特有字段（native_tools 等）保持不变；列出模型成功不代表 API key 有效。")
	return lines
}

// chatModelPickerRefreshReceipt 是一次「刷新模型列表」在副屏里就地可见的回执。
//
// 副屏（alternate screen）期间 transcript 不可见：只把结果写进 transcript 的话，
// 用户在列表里按下刷新后看到的界面与按下之前一模一样——刷新成功但清单没变、
// 以及刷新直接失败，都会表现为"没有反馈"。所以刷新行除了照常留一条 transcript
// 记录，还要带回 Summary（header 一行）与 Detail（刷新行 preview）供列表就地渲染。
type chatModelPickerRefreshReceipt struct {
	Summary string
	Detail  string
}

// chatModelPickerRefreshSummaryMaxRunes 限制 header 摘要长度：header 是单行且
// 不换行（超长会被 fit 截断），失败原因往往很长，所以 header 只留短摘要，完整
// 原因留给 Detail（preview 会按宽度换行，最多几行）。
const chatModelPickerRefreshSummaryMaxRunes = 44

// refreshChatPickerProviderModelsWithReceipt 与 refreshChatPickerProviderModels
// 同源：只刷新列表所在的 provider，把 /provider refresh 的报告压缩成一行摘要，
// 并额外返回副屏回执。调用方重开列表时把回执挂到 header 与刷新行上。
func refreshChatPickerProviderModelsWithReceipt(session *ChatSession, providerName string) (CommandResult, chatModelPickerRefreshReceipt) {
	name := strings.TrimSpace(providerName)
	if name == "" {
		message := "未指定 provider，无法刷新模型列表"
		return commandErrorResult(fmt.Errorf("%s", message)), chatModelPickerRefreshReceipt{
			Summary: "刷新失败：" + message,
			Detail:  message,
		}
	}
	run, warnings, err := chatProviderRefreshProviders(session, chatProviderRefreshRequest{Names: []string{name}})
	if err != nil {
		message := err.Error()
		return commandErrorResult(err), chatModelPickerRefreshReceipt{
			Summary: chatModelPickerRefreshCompactSummary("刷新失败：" + message),
			Detail:  message,
		}
	}
	detail := chatProviderRefreshInlineSummary(name, run)
	result := commandResultWithWarnings(buildChatPlainTextCommandDocument(detail), warnings...)
	return result, chatModelPickerRefreshReceipt{
		Summary: chatModelPickerRefreshCompactSummary(chatModelPickerRefreshPickerSummary(name, run)),
		Detail:  detail,
	}
}

// refreshChatPickerProviderModels 是模型列表首行「刷新」的执行体：只刷新列表
// 所在的 provider，并把 /provider refresh 的报告压缩成一行摘要。列表本身由调用
// 方重开，用户随即看到新模型，所以这里不需要整份报告。
func refreshChatPickerProviderModels(session *ChatSession, providerName string) CommandResult {
	result, _ := refreshChatPickerProviderModelsWithReceipt(session, providerName)
	return result
}

// chatModelPickerRefreshPickerSummary 是 header 用的短摘要：与
// chatProviderRefreshInlineSummary 同源（失败原因同样进正文，不得出现"已刷新"），
// 但只点出用户最关心的增量，便于塞进单行 header。
func chatModelPickerRefreshPickerSummary(providerName string, run chatProviderRefreshRun) string {
	if len(run.Outcomes) == 0 {
		return fmt.Sprintf("provider %s 没有可刷新的模型列表", providerName)
	}
	outcome := run.Outcomes[0]
	reason := strings.TrimSpace(outcome.Reason)
	if detail, ok := strings.CutPrefix(reason, "fetch_failed:"); ok {
		return "刷新失败：" + strings.TrimSpace(detail)
	}
	if detail, ok := strings.CutPrefix(reason, "persist_failed:"); ok {
		return "刷新失败（写入配置）：" + strings.TrimSpace(detail)
	}
	if reason == "already_up_to_date" {
		return fmt.Sprintf("已是最新（%d 个模型）", outcome.ModelCount)
	}
	segments := []string{fmt.Sprintf("共 %d 个模型", outcome.ModelCount)}
	if len(outcome.Added) > 0 {
		segments = append(segments, "新增 "+strings.Join(outcome.Added, "、"))
	}
	if len(outcome.Removed) > 0 {
		segments = append(segments, "移除 "+strings.Join(outcome.Removed, "、"))
	}
	if outcome.ChangedModels > 0 {
		segments = append(segments, fmt.Sprintf("能力更新 %d 个", outcome.ChangedModels))
	}
	return "已刷新：" + strings.Join(segments, "，")
}

// chatModelPickerRefreshCompactSummary 把 header 摘要压到单行可读长度：换行符
// 折叠成空格，超出部分截断并以省略号收尾（完整内容仍在 Detail 里）。
func chatModelPickerRefreshCompactSummary(summary string) string {
	summary = strings.Join(strings.Fields(summary), " ")
	if utf8.RuneCountInString(summary) <= chatModelPickerRefreshSummaryMaxRunes {
		return summary
	}
	runes := []rune(summary)
	return string(runes[:chatModelPickerRefreshSummaryMaxRunes-1]) + "…"
}

// chatProviderRefreshInlineSummary 把单 provider 的刷新结果压成一行。失败原因
// 必须出现在正文里（warnings 只是附带），否则用户在列表里按下刷新却看不到原因。
func chatProviderRefreshInlineSummary(providerName string, run chatProviderRefreshRun) string {
	if len(run.Outcomes) == 0 {
		return fmt.Sprintf("provider %s 没有可刷新的模型列表", providerName)
	}
	outcome := run.Outcomes[0]
	reason := strings.TrimSpace(outcome.Reason)
	if detail, ok := strings.CutPrefix(reason, "fetch_failed:"); ok {
		return fmt.Sprintf("刷新 provider %s 失败：%s", providerName, strings.TrimSpace(detail))
	}
	if detail, ok := strings.CutPrefix(reason, "persist_failed:"); ok {
		return fmt.Sprintf("刷新 provider %s 失败（写入配置）：%s", providerName, strings.TrimSpace(detail))
	}
	if reason == "already_up_to_date" {
		return fmt.Sprintf("provider %s 的模型列表已是最新（%d 个模型）", providerName, outcome.ModelCount)
	}
	segments := []string{fmt.Sprintf("共 %d 个模型", outcome.ModelCount)}
	if len(outcome.Added) > 0 {
		segments = append(segments, "新增 "+strings.Join(outcome.Added, "、"))
	}
	if len(outcome.Removed) > 0 {
		segments = append(segments, "移除 "+strings.Join(outcome.Removed, "、"))
	}
	if outcome.ChangedModels > 0 {
		segments = append(segments, fmt.Sprintf("能力更新 %d 个", outcome.ChangedModels))
	}
	return fmt.Sprintf("已刷新 provider %s 的模型列表：%s（已保存到配置文件）",
		providerName, strings.Join(segments, "，"))
}

// executeStructuredProviderRefreshCommand 走统一渲染通道：结果是一个文档单元格，
// 非致命问题作为 warnings 附在单元格上（不直写 stdout）。
func executeStructuredProviderRefreshCommand(session *ChatSession, request chatProviderRefreshRequest) CommandResult {
	lines, warnings, err := runChatProviderRefresh(session, request)
	if err != nil {
		return commandErrorResult(err)
	}
	return commandResultWithWarnings(textLinesDocument(lines), warnings...)
}

// handleLegacyProviderRefreshCommand 服务非 unified 的兼容终端。直写点集中在本
// 函数，避免给 handleProviderCommand 增加 stdout 写入（chat_command_result_test.go
// 冻结了每个函数的直写点计数）。
func handleLegacyProviderRefreshCommand(session *ChatSession, request chatProviderRefreshRequest, parseErr error) bool {
	if parseErr != nil {
		printDirectInteractiveOutput(session, fmt.Sprintf("错误: %v\n", parseErr))
		return false
	}
	lines, warnings, err := runChatProviderRefresh(session, request)
	if err != nil {
		printDirectInteractiveOutput(session, fmt.Sprintf("错误: %v\n", err))
		return false
	}
	printDirectInteractiveOutput(session, strings.Join(lines, "\n")+"\n")
	for _, warning := range warnings {
		printDirectInteractiveOutput(session, fmt.Sprintf("Warning: %v\n", warning))
	}
	return false
}
