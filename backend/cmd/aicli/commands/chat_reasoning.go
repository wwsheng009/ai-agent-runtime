package commands

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
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
		return normalized, fmt.Sprintf("Warning: reasoning-effort %q 不在当前模型支持列表中，已原样透传", normalized), nil
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

	return reasoningEffortCatalog{}
}

func reasoningEffortCapabilityForModel(provider config.Provider, modelName string) (config.ModelCapabilitySpec, bool) {
	if len(provider.ModelCapabilities) == 0 {
		return config.ModelCapabilitySpec{}, false
	}
	return runtimellm.ResolveModelCapabilitySpec(modelName, provider.ModelCapabilities)
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
	return options
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
	ui.PrintSection("选择 reasoning_effort 值")

	normalizedOptions := normalizeReasoningEffortOptions(options)
	normalizedCurrent := runtimetypes.NormalizeReasoningEffort(current)

	if len(normalizedOptions) == 0 {
		if normalizedCurrent != "" {
			fmt.Printf("  [1] %s %s\n", normalizedCurrent, ui.GetTheme(ui.ThemeAuto).Dimmed("(当前)"))
		}
		ui.PrintEmptyLine()
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
		if option == normalizedCurrent {
			fmt.Printf("  [%d] %-*s  %s\n", i+1, maxLabelLen, labels[i], ui.GetTheme(ui.ThemeAuto).Dimmed("(当前)"))
		} else {
			fmt.Printf("  [%d] %-*s\n", i+1, maxLabelLen, labels[i])
		}
	}
	ui.PrintEmptyLine()

	for {
		defaultHint := normalizedCurrent
		if defaultHint == "" {
			defaultHint = "无"
		}
		fmt.Printf("请输入选项 (回车保留当前: %s): ", defaultHint)
		input, _ := reader.ReadString('\n')
		input = runtimetypes.NormalizeReasoningEffort(input)

		if input == "" {
			return normalizedCurrent
		}

		if num, err := strconv.Atoi(input); err == nil {
			switch {
			case num >= 1 && num <= len(normalizedOptions):
				return normalizedOptions[num-1]
			default:
				ui.PrintWarning("无效的选择，请重新输入")
				continue
			}
		}

		if matched, ok := reasoningEffortOptionMatch(input, normalizedOptions); ok {
			return matched
		}

		ui.PrintWarning("无效的选择，请重新输入")
	}
}
