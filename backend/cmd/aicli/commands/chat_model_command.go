package commands

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	httpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type modelCommandRequest struct {
	Provider          string
	Model             string
	ReasoningEffort   string
	ShowStatus        bool
	ClearReasoning    bool
	ProviderExplicit  bool
	ModelExplicit     bool
	ReasoningExplicit bool
}

func (r modelCommandRequest) HasMutation() bool {
	return r.ProviderExplicit || r.ModelExplicit || r.ReasoningExplicit || r.ClearReasoning
}

func parseModelCommandRequest(command string) (modelCommandRequest, error) {
	tokens := executor.SplitCommandTokens(command)
	if len(tokens) == 0 {
		return modelCommandRequest{}, fmt.Errorf("无效的 /model 命令")
	}
	req := modelCommandRequest{}
	for i := 1; i < len(tokens); i++ {
		tok := strings.TrimSpace(tokens[i])
		if tok == "" {
			continue
		}
		switch {
		case tok == "status" || tok == "--status":
			req.ShowStatus = true
		case tok == "clear-reasoning" || tok == "clear-reasoning-effort" || tok == "--clear-reasoning" || tok == "--clear-reasoning-effort":
			req.ClearReasoning = true
		case tok == "--provider" || tok == "-p":
			value, next, err := consumeModelCommandValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.Provider = value
			req.ProviderExplicit = true
			i = next
		case strings.HasPrefix(tok, "--provider="):
			req.Provider = strings.TrimSpace(strings.TrimPrefix(tok, "--provider="))
			req.ProviderExplicit = true
		case strings.HasPrefix(tok, "-p="):
			req.Provider = strings.TrimSpace(strings.TrimPrefix(tok, "-p="))
			req.ProviderExplicit = true
		case tok == "--model" || tok == "-m":
			value, next, err := consumeModelCommandValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.Model = value
			req.ModelExplicit = true
			i = next
		case strings.HasPrefix(tok, "--model="):
			req.Model = strings.TrimSpace(strings.TrimPrefix(tok, "--model="))
			req.ModelExplicit = true
		case strings.HasPrefix(tok, "-m="):
			req.Model = strings.TrimSpace(strings.TrimPrefix(tok, "-m="))
			req.ModelExplicit = true
		case tok == "--reasoning-effort" || tok == "-r":
			value, next, err := consumeModelCommandValue(tokens, i)
			if err != nil {
				return req, err
			}
			req.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(value)
			req.ReasoningExplicit = true
			i = next
		case strings.HasPrefix(tok, "--reasoning-effort="):
			req.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(strings.TrimPrefix(tok, "--reasoning-effort="))
			req.ReasoningExplicit = true
		case strings.HasPrefix(tok, "-r="):
			req.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(strings.TrimPrefix(tok, "-r="))
			req.ReasoningExplicit = true
		default:
			if strings.HasPrefix(tok, "-") {
				return req, fmt.Errorf("未知的 /model 参数: %s", tok)
			}
			if req.Model == "" {
				req.Model = tok
				req.ModelExplicit = true
				continue
			}
			return req, fmt.Errorf("无法解析 /model 参数: %s", tok)
		}
	}
	return req, nil
}

func consumeModelCommandValue(tokens []string, index int) (string, int, error) {
	if index+1 >= len(tokens) {
		return "", index, fmt.Errorf("参数 %s 缺少值", tokens[index])
	}
	value := strings.TrimSpace(tokens[index+1])
	if value == "" {
		return "", index, fmt.Errorf("参数 %s 缺少值", tokens[index])
	}
	return value, index + 1, nil
}

func executeModelCommand(session *ChatSession, request modelCommandRequest, interactive bool) error {
	if session == nil {
		return fmt.Errorf("当前没有活动会话")
	}
	if request.ShowStatus && !request.HasMutation() {
		return nil
	}
	if !request.HasMutation() {
		if !interactive {
			return nil
		}
		providerName, err := promptModelCommandProviderSelection(session, currentModelCommandProvider(session))
		if err != nil {
			return err
		}
		providerCtx, _, err := resolveModelCommandExecutionContext(session, providerName, "")
		if err != nil {
			return err
		}
		currentModel := ""
		if strings.EqualFold(strings.TrimSpace(providerName), strings.TrimSpace(currentModelCommandProvider(session))) {
			currentModel = effectiveRuntimeModel(session)
		}
		modelName, err := promptModelCommandModelSelection(session, providerCtx.Provider, currentModel)
		if err != nil {
			return err
		}
		finalCtx, _, err := resolveModelCommandExecutionContext(session, providerName, modelName)
		if err != nil {
			return err
		}
		reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
		if reasoning, err = promptModelCommandReasoningSelection(session, finalCtx.Provider, finalCtx.Model, reasoning); err != nil {
			return err
		}
		printModelCommandMappingNotice(finalCtx.RequestedModel, finalCtx.Model, true)
		return applyModelCommandSelection(session, finalCtx, finalCtx.RequestedModel, reasoning)
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
		return err
	}

	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if request.ReasoningExplicit {
		reasoning = request.ReasoningEffort
	} else if request.ClearReasoning {
		reasoning = ""
	} else if interactive && request.ModelExplicit {
		reasoning, err = promptModelCommandReasoningSelection(session, providerCtx.Provider, providerCtx.Model, reasoning)
		if err != nil {
			return err
		}
	} else if reasoning != "" && reasoningEffortCatalogForModel(providerCtx.Provider, providerCtx.Model).supported && !reasoningEffortAllowed(reasoning, reasoningEffortCatalogForModel(providerCtx.Provider, providerCtx.Model).options) {
		fmt.Fprintf(os.Stderr, "Warning: reasoning_effort %q 不被模型 %s 支持，已清空\n", reasoning, providerCtx.Model)
		reasoning = ""
	}

	if request.ReasoningExplicit {
		var warning string
		reasoning, warning, err = resolveChatReasoningEffort(providerCtx.Provider, providerCtx.Model, reasoning, true)
		if err != nil {
			return err
		}
		if warning != "" {
			fmt.Fprintln(os.Stderr, warning)
		}
	}

	printModelCommandMappingNotice(providerCtx.RequestedModel, providerCtx.Model, request.ModelExplicit)
	return applyModelCommandSelection(session, providerCtx, providerCtx.RequestedModel, reasoning)
}

func currentModelCommandProvider(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if provider := strings.TrimSpace(session.ProviderName); provider != "" {
		return provider
	}
	return strings.TrimSpace(session.Provider.GetProtocol())
}

func resolveModelCommandExecutionContext(session *ChatSession, providerName, modelName string) (*providerExecutionContext, map[string]interface{}, error) {
	if session == nil {
		return nil, nil, fmt.Errorf("当前没有活动会话")
	}
	cfg := session.Config
	if cfg != nil {
		return resolveProviderExecutionContext(cfg, providerName, modelName)
	}

	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		providerName = currentModelCommandProvider(session)
	}
	if providerName == "" {
		return nil, nil, fmt.Errorf("provider 配置不可用")
	}
	if session.ProviderName != "" && !strings.EqualFold(providerName, strings.TrimSpace(session.ProviderName)) {
		return nil, nil, fmt.Errorf("当前会话未绑定配置文件，无法切换到 provider %s", providerName)
	}

	provider := session.Provider
	if strings.TrimSpace(modelName) == "" {
		modelName = provider.DefaultModel
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = strings.TrimSpace(session.Model)
	}
	if strings.TrimSpace(modelName) == "" {
		return nil, nil, fmt.Errorf("provider '%s' 未配置默认模型", providerName)
	}
	requestedModel := strings.TrimSpace(modelName)
	mappedModel := config.ApplyModelMapping(&provider, requestedModel)
	return &providerExecutionContext{
		ProviderName:   providerName,
		Provider:       provider,
		Adapter:        adapter.GetAdapterOrDefault(provider.GetProtocol()),
		Model:          mappedModel,
		RequestedModel: requestedModel,
		ModelMapped:    mappedModel != requestedModel,
	}, map[string]interface{}{}, nil
}

func applyModelCommandSelection(session *ChatSession, providerCtx *providerExecutionContext, requestedModel, reasoning string) error {
	if session == nil || providerCtx == nil {
		return fmt.Errorf("当前没有活动会话")
	}

	session.ProviderName = providerCtx.ProviderName
	session.Provider = providerCtx.Provider
	session.Adapter = providerCtx.Adapter
	session.Model = providerCtx.Model
	session.ReasoningEffort = runtimetypes.NormalizeReasoningEffort(reasoning)

	apiPath := ""
	if session.Adapter != nil {
		apiPath = session.Adapter.GetAPIPath()
	}
	session.BaseURL = buildProviderURL(session.Provider, apiPath, session.Model)
	if session.Config != nil {
		session.HTTPClient = httpclient.GetHTTPClientWithProvider(session.Config, &session.Provider)
	}
	if session.FunctionCatalog != nil {
		session.FunctionCatalog.builder = functions.GetFunctionCallBuilder(session.Provider.GetProtocol())
		session.FunctionBuilder = session.FunctionCatalog.builder
	} else {
		session.FunctionBuilder = functions.GetFunctionCallBuilder(session.Provider.GetProtocol())
	}
	syncChatLoggerModelState(session)
	warnIfChatSessionSyncFails(session, "toggle model", syncRuntimeSessionFromChat(session))
	if err := refreshLocalRuntimeAfterModelSelection(session); err != nil {
		warnIfChatSessionSyncFails(session, "refresh local runtime after model switch", err)
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return persistModelCommandPreferences(session)
}

func persistModelCommandPreferences(session *ChatSession) error {
	if session == nil || session.Config == nil {
		return nil
	}
	if err := persistChatPreferences(session.Config, session.ProviderName, session.Model, session.ReasoningEffort); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 /model 偏好失败: %v\n", err)
	}
	return nil
}

func printModelCommandMappingNotice(requestedModel, resolvedModel string, enabled bool) {
	if !enabled {
		return
	}
	requestedModel = strings.TrimSpace(requestedModel)
	resolvedModel = strings.TrimSpace(resolvedModel)
	if requestedModel == "" || strings.EqualFold(requestedModel, resolvedModel) {
		return
	}
	fmt.Printf("提示: 模型已映射 %s -> %s\n", requestedModel, resolvedModel)
}

func promptModelCommandProviderSelection(session *ChatSession, current string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	if useRuntimeSelectionPopup(session) {
		return promptModelCommandProviderSelectionPopup(session, current)
	}
	return promptModelCommandProviderSelectionLegacy(session, current)
}

func promptModelCommandProviderSelectionPopup(session *ChatSession, current string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}

	options := runtimeProviderSelectionOptions(session, current)
	if len(options) == 0 {
		return "", fmt.Errorf("没有可用的 providers")
	}

	currentMatch, currentValid := matchCaseInsensitive(options, current)
	defaultOption := ""
	if !currentValid {
		defaultOption = options[0]
	}

	notice := discardPendingInteractiveInputForPriorityPrompt(session, "provider 选择")
	hint := "  提示: 输入编号或名称，回车保持当前"
	popupLines := renderSelectionPopupLines("选择 Provider", "provider", current, options, currentMatch, defaultOption, hint, notice, "")
	prompt := providerSelectionPrompt(currentValid, defaultOption)
	showRuntimeSelectionPopup(session, popupLines, prompt)
	defer clearRuntimeSelectionPopup(session)

	for {
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		selected, ok := resolveRuntimeSelectionInput(text, current, defaultOption, options, false, false)
		if ok {
			return selected, nil
		}
		popupLines = renderSelectionPopupLines("选择 Provider", "provider", current, options, currentMatch, defaultOption, hint, notice, "  无效的选择，请重新输入")
		showRuntimeSelectionPopup(session, popupLines, prompt)
	}
}

func promptModelCommandProviderSelectionLegacy(session *ChatSession, current string) (string, error) {
	beginDirectInteractiveOutput(session)
	options := runtimeProviderSelectionOptions(session, current)
	if len(options) == 0 {
		return "", fmt.Errorf("没有可用的 providers")
	}

	currentMatch, currentValid := matchCaseInsensitive(options, current)
	defaultOption := ""
	if !currentValid {
		defaultOption = options[0]
	}

	if notice := discardPendingInteractiveInputForPriorityPrompt(session, "provider 选择"); notice != "" {
		fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine(notice))
	}

	ui.PrintSection("选择 Provider")
	theme := ui.GetTheme(ui.ThemeAuto)
	switch {
	case currentMatch != "":
		fmt.Printf("  当前 provider: %s %s\n", theme.Dimmed(currentMatch), theme.Dimmed("(当前)"))
	case current != "":
		fmt.Printf("  当前 provider: %s %s\n", theme.Dimmed(current), theme.Dimmed("(当前无效或已禁用)"))
	default:
		fmt.Println("  当前 provider: (无)")
	}

	maxLen := 0
	for _, option := range options {
		if len(option) > maxLen {
			maxLen = len(option)
		}
	}
	for i, option := range options {
		summary := runtimeProviderSelectionSummary(session, option)
		switch {
		case strings.EqualFold(option, currentMatch):
			if summary != "" {
				fmt.Printf("  [%d] %-*s  %s %s\n", i+1, maxLen, option, theme.Dimmed(summary), theme.Dimmed("(当前)"))
			} else {
				fmt.Printf("  [%d] %-*s  %s\n", i+1, maxLen, option, theme.Dimmed("(当前)"))
			}
		case defaultOption != "" && strings.EqualFold(option, defaultOption):
			if summary != "" {
				fmt.Printf("  [%d] %-*s  %s %s\n", i+1, maxLen, option, theme.Dimmed(summary), theme.Dimmed("(默认)"))
			} else {
				fmt.Printf("  [%d] %-*s  %s\n", i+1, maxLen, option, theme.Dimmed("(默认)"))
			}
		default:
			if summary != "" {
				fmt.Printf("  [%d] %-*s  %s\n", i+1, maxLen, option, theme.Dimmed(summary))
			} else {
				fmt.Printf("  [%d] %-*s\n", i+1, maxLen, option)
			}
		}
	}
	fmt.Println("  提示: 也可以直接输入 provider 名称")

	prompt := providerSelectionPrompt(currentValid, defaultOption)
	ui.PrintEmptyLine()
	for {
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		selected, ok := resolveRuntimeSelectionInput(text, current, defaultOption, options, false, false)
		if ok {
			return selected, nil
		}
		ui.PrintWarning("无效的选择，请重新输入")
	}
}

func promptModelCommandModelSelection(session *ChatSession, provider config.Provider, current string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}

	savedProvider := session.Provider
	savedModel := session.Model
	session.Provider = provider
	session.Model = current
	defer func() {
		session.Provider = savedProvider
		session.Model = savedModel
	}()

	selected, _, err := promptRuntimeModelSelection(session)
	return selected, err
}

func promptModelCommandReasoningSelection(session *ChatSession, provider config.Provider, modelName, current string) (string, error) {
	catalog := reasoningEffortCatalogForModel(provider, modelName)
	if !catalog.supported {
		return runtimetypes.NormalizeReasoningEffort(current), nil
	}
	selected, _, err := selectRuntimeReasoningEffort(session, current, catalog.options)
	return selected, err
}

func runtimeProviderSelectionOptions(session *ChatSession, current string) []string {
	seen := make(map[string]struct{})
	options := make([]string, 0, 1)
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
	if session != nil && session.Config != nil {
		add(session.Config.Providers.DefaultProvider)
		for name, provider := range session.Config.Providers.Items {
			if provider.Enabled {
				add(name)
			}
		}
	}
	if session != nil && strings.TrimSpace(session.ProviderName) != "" {
		add(session.ProviderName)
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

func runtimeProviderSelectionSummary(session *ChatSession, providerName string) string {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return ""
	}
	if session != nil && session.Config != nil {
		if provider, ok := session.Config.Providers.Items[providerName]; ok {
			return describeProviderSelection(provider)
		}
	}
	if session != nil && strings.EqualFold(providerName, strings.TrimSpace(session.ProviderName)) {
		return describeProviderSelection(session.Provider)
	}
	return ""
}

func providerSelectionPrompt(currentValid bool, defaultOption string) string {
	switch {
	case currentValid:
		return "请输入选项 (回车保持当前): "
	case defaultOption != "":
		return fmt.Sprintf("请输入选项 (回车默认: %s): ", defaultOption)
	default:
		return "请输入选项: "
	}
}
