package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const (
	skillCatalogSelectionPrompt = "编号/名称 (回车=1, q取消): "
	skillExecutionPrompt        = "请输入 prompt (q取消): "
)

func handleSkillsMenuCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	query, jsonOutput := extractCommandArgumentOptions(command)
	query = strings.TrimSpace(query)
	useJSON := jsonOutput || session.JSONOutput

	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		fmt.Println(formatCommandError("Function Catalog: 未初始化", useJSON))
		return false
	}

	report := buildFunctionCatalogReport(catalog)
	if report == nil {
		fmt.Println(formatCommandError("Function Catalog: 未初始化", useJSON))
		return false
	}

	skills := filterSkillCatalogEntries(report.Skills, query)
	if useJSON {
		payload := struct {
			Count  int                             `json:"count"`
			Query  string                          `json:"query,omitempty"`
			Skills []aicliFunctionDescriptorReport `json:"skills,omitempty"`
		}{
			Count:  len(skills),
			Query:  query,
			Skills: append([]aicliFunctionDescriptorReport(nil), skills...),
		}
		fmt.Println(marshalIndentedJSON(payload))
		return false
	}

	if session.NoInteractive {
		printSkillCatalogReport(skills, query)
		return false
	}

	if len(skills) == 0 {
		fmt.Println("错误: 未找到匹配 skill")
		fmt.Println("输入 /functions 查看全部 function catalog")
		return false
	}

	beginDirectInteractiveOutput(session)
	selected, err := promptSkillCatalogSelection(session, skills, query)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if selected == nil {
		fmt.Println("已取消选择 skill")
		return false
	}

	prompt, err := promptSkillExecutionInput(session)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		fmt.Println("已取消执行 skill")
		return false
	}

	beginDirectInteractiveOutput(session)
	return handleDirectSkillCommand(session, fmt.Sprintf("/skill %s %s", selected.FunctionName, prompt))
}

func printSkillCatalogReport(skills []aicliFunctionDescriptorReport, query string) {
	for _, line := range buildSkillCatalogLines(skills, query, "") {
		fmt.Println(line)
	}
}

func buildSkillCatalogLines(skills []aicliFunctionDescriptorReport, query, warning string) []string {
	lines := []string{
		fmt.Sprintf("Skill Catalog: total=%d", len(skills)),
	}
	if query != "" {
		lines = append(lines, "Filter: "+query)
	}
	if warning != "" {
		lines = append(lines, warning)
	}
	if len(skills) == 0 {
		lines = append(lines, "  <none>")
		return lines
	}

	labelWidth := 0
	for _, item := range skills {
		if w := ui.DisplayWidth(skillCatalogEntryLabel(item)); w > labelWidth {
			labelWidth = w
		}
	}
	if labelWidth < 8 {
		labelWidth = 8
	}
	if labelWidth > 24 {
		labelWidth = 24
	}

	width := ui.GetTerminalWidth()
	for i, item := range skills {
		lines = append(lines, formatSkillCatalogItemLine(i, item, labelWidth, width))
	}
	return lines
}

func promptSkillCatalogSelection(session *ChatSession, skills []aicliFunctionDescriptorReport, query string) (*aicliFunctionDescriptorReport, error) {
	if len(skills) == 0 {
		return nil, nil
	}

	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}

	warning := ""
	for {
		lines := buildSkillCatalogLines(skills, query, warning)
		if usePopup {
			showRuntimeSelectionPopup(session, lines, skillCatalogSelectionPrompt)
		} else {
			printChatSelectionSection("选择 Skill")
			for _, line := range lines {
				printChatSelectionLine("%s", line)
			}
			printChatSelectionPrompt(skillCatalogSelectionPrompt)
		}

		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), skillCatalogSelectionPrompt)
		if !usePopup {
			fmt.Println()
		}
		if err != nil {
			return nil, err
		}

		choice := strings.TrimSpace(normalizeQueuedInputLine(text))
		warning = ""
		switch strings.ToLower(choice) {
		case "", "1":
			return &skills[0], nil
		case "q", "quit", "cancel", "exit":
			return nil, nil
		}

		if index, ok := findSkillCatalogEntryIndex(skills, choice); ok {
			return &skills[index], nil
		}

		if usePopup {
			warning = "  无效的选择，请重新输入"
		} else {
			printChatSelectionWarning("无效的选择，请重新输入")
		}
	}
}

func promptSkillExecutionInput(session *ChatSession) (string, error) {
	for {
		printChatSelectionPrompt(skillExecutionPrompt)
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), skillExecutionPrompt)
		fmt.Println()
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(normalizeQueuedInputLine(text))
		switch strings.ToLower(value) {
		case "q", "quit", "cancel", "exit":
			return "", nil
		}
		if value == "" {
			printChatSelectionWarning("prompt 不能为空，请重新输入")
			continue
		}
		return value, nil
	}
}

func filterSkillCatalogEntries(entries []aicliFunctionDescriptorReport, query string) []aicliFunctionDescriptorReport {
	if len(entries) == 0 {
		return nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return append([]aicliFunctionDescriptorReport(nil), entries...)
	}

	filtered := make([]aicliFunctionDescriptorReport, 0, len(entries))
	for _, item := range entries {
		if skillCatalogEntryMatchesQuery(item, query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func findSkillCatalogEntryIndex(entries []aicliFunctionDescriptorReport, choice string) (int, bool) {
	choice = strings.TrimSpace(choice)
	if choice == "" || len(entries) == 0 {
		return -1, false
	}

	if num, err := strconv.Atoi(choice); err == nil {
		if num >= 1 && num <= len(entries) {
			return num - 1, true
		}
		return -1, false
	}

	normalized := strings.ToLower(choice)
	exactIndex := -1
	exactCount := 0
	prefixIndex := -1
	prefixCount := 0
	for i, item := range entries {
		tokens := skillCatalogEntrySelectionTokens(item)
		matchedExact := false
		matchedPrefix := false
		for _, token := range tokens {
			token = strings.TrimSpace(strings.ToLower(token))
			if token == "" {
				continue
			}
			if token == normalized {
				matchedExact = true
				break
			}
			if strings.HasPrefix(token, normalized) {
				matchedPrefix = true
			}
		}
		if matchedExact {
			exactIndex = i
			exactCount++
			continue
		}
		if matchedPrefix {
			prefixIndex = i
			prefixCount++
		}
	}
	if exactCount == 1 {
		return exactIndex, true
	}
	if exactCount > 1 {
		return -1, false
	}
	if prefixCount == 1 {
		return prefixIndex, true
	}
	return -1, false
}

func skillCatalogEntryMatchesQuery(item aicliFunctionDescriptorReport, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	return strings.Contains(skillCatalogEntrySearchText(item), query)
}

func skillCatalogEntrySearchText(item aicliFunctionDescriptorReport) string {
	desc := item.Descriptor
	parts := []string{
		skillCatalogEntryLabel(item),
		strings.TrimSpace(item.FunctionName),
	}
	if desc != nil {
		parts = append(parts, strings.TrimSpace(desc.Description), strings.TrimSpace(desc.Category))
		parts = append(parts, strings.Join(desc.Labels, " "), strings.Join(desc.Capabilities, " "))
		if desc.Source != nil {
			parts = append(parts, strings.TrimSpace(desc.Source.Path), strings.TrimSpace(desc.Source.Dir), strings.TrimSpace(desc.Source.Layer))
		}
		if desc.Metadata != nil {
			if value, _ := desc.Metadata["skill_name"].(string); value != "" {
				parts = append(parts, value)
			}
			if value, _ := desc.Metadata["skill_path"].(string); value != "" {
				parts = append(parts, value)
			}
			if value, _ := desc.Metadata["function_name"].(string); value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func skillCatalogEntrySelectionTokens(item aicliFunctionDescriptorReport) []string {
	tokens := []string{skillCatalogEntryLabel(item), strings.TrimSpace(item.FunctionName)}
	if desc := item.Descriptor; desc != nil {
		if desc.Metadata != nil {
			if value, _ := desc.Metadata["skill_name"].(string); value != "" {
				tokens = append(tokens, value)
			}
			if value, _ := desc.Metadata["skill_path"].(string); value != "" {
				tokens = append(tokens, value)
			}
			if value, _ := desc.Metadata["function_name"].(string); value != "" {
				tokens = append(tokens, value)
			}
		}
		if desc.Source != nil && desc.Source.Path != "" {
			tokens = append(tokens, desc.Source.Path)
		}
	}
	return tokens
}

func skillCatalogEntryLabel(item aicliFunctionDescriptorReport) string {
	if desc := item.Descriptor; desc != nil {
		if name := strings.TrimSpace(desc.Name); name != "" {
			return name
		}
	}
	if name := strings.TrimSpace(item.FunctionName); name != "" {
		return name
	}
	return "skill"
}

func skillCatalogEntryDetail(item aicliFunctionDescriptorReport) string {
	desc := item.Descriptor
	if desc == nil {
		if fn := strings.TrimSpace(item.FunctionName); fn != "" {
			return "function=" + fn
		}
		return "skill"
	}

	parts := make([]string, 0, 4)
	if description := strings.TrimSpace(desc.Description); description != "" {
		parts = append(parts, description)
	}

	metaParts := make([]string, 0, 3)
	if fn, _ := desc.Metadata["function_name"].(string); strings.TrimSpace(fn) != "" {
		metaParts = append(metaParts, "function="+strings.TrimSpace(fn))
	} else if fn := strings.TrimSpace(item.FunctionName); fn != "" {
		metaParts = append(metaParts, "function="+fn)
	}
	if category := strings.TrimSpace(desc.Category); category != "" {
		metaParts = append(metaParts, "category="+category)
	}
	if path, _ := desc.Metadata["skill_path"].(string); strings.TrimSpace(path) != "" {
		metaParts = append(metaParts, "path="+strings.TrimSpace(path))
	}
	if len(metaParts) > 0 {
		parts = append(parts, strings.Join(metaParts, ", "))
	}

	if len(parts) == 0 {
		if fn := strings.TrimSpace(item.FunctionName); fn != "" {
			return "function=" + fn
		}
		return "skill"
	}
	return strings.Join(parts, " | ")
}

func formatSkillCatalogItemLine(index int, item aicliFunctionDescriptorReport, labelWidth, width int) string {
	label := skillCatalogEntryLabel(item)
	detail := skillCatalogEntryDetail(item)
	line := fmt.Sprintf("  [%d] %-*s  %s", index+1, labelWidth, label, detail)
	return truncateStatusValue(line, width)
}
