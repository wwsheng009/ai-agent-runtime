package commands

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type reasoningEffortCatalog struct {
	options   []string
	supported bool
}

func resolveChatReasoningEffort(provider config.Provider, modelName, raw string, explicit bool) (string, string, error) {
	catalog := reasoningEffortCatalogForModel(provider, modelName)
	normalized := runtimetypes.NormalizeReasoningEffort(raw)

	if normalized == "" {
		return "", "", nil
	}

	if !catalog.supported {
		return normalized, "", nil
	}

	if !reasoningEffortAllowed(normalized, catalog.options) {
		allowed := strings.Join(catalog.options, "|")
		if explicit {
			return "", "", fmt.Errorf("无效的 reasoning-effort: %s（当前模型可选值: %s）", raw, allowed)
		}
		return "", fmt.Sprintf("Warning: reasoning-effort %q 不在当前模型支持列表中，已清空", normalized), nil
	}

	return normalized, "", nil
}

func reasoningEffortCatalogForModel(provider config.Provider, modelName string) reasoningEffortCatalog {
	if capability, ok := reasoningEffortCapabilityForModel(provider, modelName); ok {
		options := normalizeReasoningEffortOptions(capability.ReasoningEfforts)
		if len(options) > 0 {
			return reasoningEffortCatalog{
				options:   options,
				supported: true,
			}
		}
	}
	if capability, ok := fallbackReasoningEffortCapabilityForProvider("", provider); ok {
		options := normalizeReasoningEffortOptions(capability.ReasoningEfforts)
		if len(options) > 0 {
			return reasoningEffortCatalog{
				options:   options,
				supported: true,
			}
		}
	}

	return reasoningEffortCatalog{}
}

func reasoningEffortCapabilityForModel(provider config.Provider, modelName string) (config.ModelCapabilitySpec, bool) {
	if len(provider.ModelCapabilities) == 0 {
		return config.ModelCapabilitySpec{}, false
	}
	return runtimellm.ResolveModelCapabilitySpec(modelName, provider.ModelCapabilities)
}

func reasoningEffortCapabilityForRequest(session *ChatSession) (config.ModelCapabilitySpec, bool) {
	if session == nil {
		return config.ModelCapabilitySpec{}, false
	}
	if capability, ok := reasoningEffortCapabilityForModel(session.Provider, session.Model); ok {
		return capability, true
	}
	if capability, ok := fallbackReasoningEffortCapabilityForProvider(session.ProviderName, session.Provider); ok {
		return capability, true
	}
	return config.ModelCapabilitySpec{}, false
}

func fallbackReasoningEffortCapabilityForProvider(providerName string, provider config.Provider) (config.ModelCapabilitySpec, bool) {
	if !strings.EqualFold(strings.TrimSpace(provider.GetProtocol()), "openai") {
		return config.ModelCapabilitySpec{}, false
	}
	capability, ok := providercompat.DefaultRuntimeCapability(providercompat.Context{
		ProviderName: providerName,
		Protocol:     provider.GetProtocol(),
		BaseURL:      provider.BaseURL,
	})
	if !ok {
		return config.ModelCapabilitySpec{}, false
	}
	return config.ModelCapabilitySpec(capability), true
}

func normalizeReasoningEffortOptions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	options := make([]string, 0, len(values))
	for _, value := range values {
		normalized := runtimetypes.NormalizeReasoningEffort(value)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		options = append(options, normalized)
	}
	sortReasoningEffortOptions(options)
	return options
}

func supportedReasoningEffortForRequest(raw string, capability config.ModelCapabilitySpec, hasCapability bool) string {
	effort := runtimetypes.NormalizeReasoningEffort(raw)
	if effort == "" {
		return ""
	}
	if !hasCapability || len(capability.ReasoningEfforts) == 0 {
		return effort
	}
	if reasoningEffortAllowed(effort, normalizeReasoningEffortOptions(capability.ReasoningEfforts)) {
		return effort
	}
	return ""
}

func sortReasoningEffortOptions(options []string) {
	if len(options) < 2 {
		return
	}
	sort.SliceStable(options, func(i, j int) bool {
		leftRank, leftKnown := reasoningEffortSortRank(options[i])
		rightRank, rightKnown := reasoningEffortSortRank(options[j])
		switch {
		case leftKnown && rightKnown:
			if leftRank != rightRank {
				return leftRank < rightRank
			}
		case leftKnown != rightKnown:
			return leftKnown
		}

		left := strings.ToLower(strings.TrimSpace(options[i]))
		right := strings.ToLower(strings.TrimSpace(options[j]))
		if left == right {
			return strings.TrimSpace(options[i]) < strings.TrimSpace(options[j])
		}
		return left < right
	})
}

func reasoningEffortSortRank(value string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "minimal":
		return 0, true
	case "low":
		return 1, true
	case "medium":
		return 2, true
	case "high":
		return 3, true
	case "max":
		return 4, true
	case "xhigh":
		return 5, true
	default:
		return 0, false
	}
}

func reasoningEffortAllowed(value string, options []string) bool {
	normalized := runtimetypes.NormalizeReasoningEffort(value)
	if normalized == "" {
		return false
	}
	for _, option := range options {
		if strings.EqualFold(option, normalized) {
			return true
		}
	}
	return false
}

func reasoningEffortOptionMatch(value string, options []string) (string, bool) {
	normalized := runtimetypes.NormalizeReasoningEffort(value)
	if normalized == "" {
		return "", false
	}
	for _, option := range options {
		if strings.EqualFold(option, normalized) {
			return option, true
		}
	}
	return "", false
}

func selectReasoningEffortWithReader(current string, options []string, reader *bufio.Reader) string {
	printChatSelectionSection("选择 reasoning_effort 值")

	normalizedOptions := normalizeReasoningEffortOptions(options)
	normalizedCurrent := runtimetypes.NormalizeReasoningEffort(current)
	currentMatch, currentValid := reasoningEffortOptionMatch(normalizedCurrent, normalizedOptions)
	defaultOption := ""
	if !currentValid && len(normalizedOptions) > 0 {
		defaultOption = normalizedOptions[0]
	}

	if len(normalizedOptions) == 0 {
		if normalizedCurrent != "" {
			printChatSelectionLine("  [1] %s %s", normalizedCurrent, ui.GetTheme(ui.ThemeAuto).Dimmed("(当前)"))
		}
		printChatSelectionBlankLine()
		return normalizedCurrent
	}

	maxLabelLen := 0
	labels := make([]string, len(normalizedOptions))
	for i, option := range normalizedOptions {
		labels[i] = option
		if len(labels[i]) > maxLabelLen {
			maxLabelLen = len(labels[i])
		}
	}

	for i, option := range normalizedOptions {
		if currentValid && option == currentMatch {
			printChatSelectionLine("  [%d] %-*s  %s", i+1, maxLabelLen, labels[i], ui.GetTheme(ui.ThemeAuto).Dimmed("(当前)"))
		} else if defaultOption != "" && option == defaultOption {
			printChatSelectionLine("  [%d] %-*s  %s", i+1, maxLabelLen, labels[i], ui.GetTheme(ui.ThemeAuto).Dimmed("(默认)"))
		} else {
			printChatSelectionLine("  [%d] %-*s", i+1, maxLabelLen, labels[i])
		}
	}
	printChatSelectionBlankLine()

	for {
		if currentValid {
			printChatSelectionPrompt("请输入选项 (回车保留当前: %s / 输入 0 清空): ", currentMatch)
		} else if defaultOption != "" {
			printChatSelectionPrompt("请输入选项 (回车默认: %s / 输入 0 清空): ", defaultOption)
		} else {
			printChatSelectionPrompt("请输入选项 (回车清空当前无效值 / 输入 0 清空): ")
		}
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		normalizedInput := runtimetypes.NormalizeReasoningEffort(input)

		if normalizedInput == "" {
			if currentValid {
				return currentMatch
			}
			if defaultOption != "" {
				return defaultOption
			}
			return ""
		}

		switch strings.ToLower(normalizedInput) {
		case "0", "clear", "none", "off", "清空", "无":
			return ""
		}

		if num, err := strconv.Atoi(normalizedInput); err == nil {
			switch {
			case num >= 1 && num <= len(normalizedOptions):
				return normalizedOptions[num-1]
			default:
				printChatSelectionWarning("无效的选择，请重新输入")
				continue
			}
		}

		if matched, ok := reasoningEffortOptionMatch(normalizedInput, normalizedOptions); ok {
			return matched
		}

		printChatSelectionWarning("无效的选择，请重新输入")
	}
}
