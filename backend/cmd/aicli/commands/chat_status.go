package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

const (
	chatStatusAppName             = "AI CLI"
	chatStatusLabelWidth          = 18
	chatStatusDefaultContentWidth = 74
	chatStatusMinimumContentWidth = 52
)

var (
	chatStatusVersion   = "dev"
	chatStatusBuildTime = "unknown"
)

// SetChatStatusBuildInfo allows main to inject ldflag build metadata so /status
// can present the same version string that the version command prints.
func SetChatStatusBuildInfo(version, buildTime string) {
	if value := strings.TrimSpace(version); value != "" {
		chatStatusVersion = value
	}
	if value := strings.TrimSpace(buildTime); value != "" {
		chatStatusBuildTime = value
	}
}

func handleStatusCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	if strings.TrimSpace(extractCommandArgument(command)) != "" {
		fmt.Println("错误: /status 不接受参数")
		return false
	}

	printChatStatus(session)
	return false
}

func printChatStatus(session *ChatSession) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}

	contentWidth := resolveChatStatusBoxContentWidth()
	lines := buildChatStatusBoxLines(session, contentWidth)
	text := strings.Join(lines, "\n") + "\n"

	// Fixed-bottom surfaces defer scroll-down when ClearPrompt shrinks the
	// bottom reserve. Raw fmt.Println after BeginOutput never flushes that
	// compensation or marks output written, so restoring the prompt cancels
	// the matching growth scroll and paints over the status box bottom
	// (Token usage / Limits / bottom border). Write through the surface so
	// pending shrink is flushed before text lands.
	if writeDirectInteractiveOutput(session, text) {
		return
	}

	beginDirectInteractiveOutput(session)
	for _, line := range lines {
		fmt.Println(line)
	}
}

func buildChatStatusBoxLines(session *ChatSession, contentWidth int) []string {
	if contentWidth < chatStatusMinimumContentWidth {
		contentWidth = chatStatusMinimumContentWidth
	}

	// contentWidth is a hard layout budget from resolveChatStatusBoxContentWidth
	// (terminal width capped). Do not expand it to fit long Directory/Session/
	// Agents.md values: that makes the box wider than the terminal, the shell
	// wraps mid-border, and /status looks like only half the data rendered.
	// Long values wrap via buildChatStatusKeyValueLines instead.
	innerLines := make([]string, 0, 16)
	headerText := buildChatStatusHeaderText()
	innerLines = append(innerLines, headerText)
	innerLines = append(innerLines, "")
	rows := []struct {
		Label string
		Value string
	}{
		{Label: "Model", Value: buildChatStatusModelValue(session)},
		{Label: "Model provider", Value: buildChatStatusProviderValue(session)},
		{Label: "Account balance", Value: buildChatStatusBalanceValue(session)},
		{Label: "Directory", Value: buildChatStatusDirectoryValue(session)},
		{Label: "Permissions", Value: buildChatStatusPermissionsValue(session)},
		{Label: "Agents.md", Value: buildChatStatusAgentsMarkdownValue(session)},
		{Label: "Collaboration mode", Value: buildChatStatusCollaborationModeValue(session)},
		{Label: "Reasoning output", Value: buildChatStatusReasoningOutputValue(session)},
	}
	if chatSessionSupportsFastMode(session) {
		rows = append(rows, struct {
			Label string
			Value string
		}{Label: "Fast mode", Value: buildChatStatusFastModeValue(session)})
	}
	rows = append(rows,
		struct {
			Label string
			Value string
		}{Label: "Session", Value: buildChatStatusSessionValue(session)},
		struct {
			Label string
			Value string
		}{Label: "Context used", Value: buildChatStatusContextUsedValue(session)},
		struct {
			Label string
			Value string
		}{Label: "Token count", Value: buildChatStatusTokenCountValue(session)},
		struct {
			Label string
			Value string
		}{Label: "Token usage", Value: buildChatStatusTokenUsageValue(session)},
		struct {
			Label string
			Value string
		}{Label: "Limits", Value: buildChatStatusLimitsValue(session)},
	)
	for _, row := range rows {
		innerLines = append(innerLines, buildChatStatusKeyValueLines(row.Label, row.Value, contentWidth)...)
	}

	lines := make([]string, 0, len(innerLines)+2)
	lines = append(lines, "╭"+strings.Repeat("─", contentWidth)+"╮")
	for _, inner := range innerLines {
		lines = append(lines, "│"+padChatStatusInnerLine(inner, contentWidth)+"│")
	}
	lines = append(lines, "╰"+strings.Repeat("─", contentWidth)+"╯")
	return lines
}

func buildChatStatusHeaderText() string {
	version := strings.TrimSpace(chatStatusVersion)
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("  >_ %s (%s)", chatStatusAppName, version)
}

func buildChatStatusKeyValueLines(label, value string, contentWidth int) []string {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = "<none>"
	}

	valueWidth := contentWidth - 2 - chatStatusLabelWidth - 1
	if valueWidth < 8 {
		valueWidth = 8
	}
	segments := splitChatStatusValueSegments(value, valueWidth)
	if len(segments) == 0 {
		segments = []string{""}
	}

	firstPrefix := "  " + fmt.Sprintf("%-*s ", chatStatusLabelWidth, label+":")
	nextPrefix := "  " + strings.Repeat(" ", chatStatusLabelWidth+1)
	lines := make([]string, 0, len(segments))
	for index, segment := range segments {
		prefix := firstPrefix
		if index > 0 {
			prefix = nextPrefix
		}
		lines = append(lines, prefix+segment)
	}
	return lines
}

func padChatStatusInnerLine(line string, contentWidth int) string {
	lineWidth := ui.DisplayWidth(line)
	if lineWidth > contentWidth {
		line = truncateStatusValue(line, contentWidth)
		lineWidth = ui.DisplayWidth(line)
	}
	if lineWidth < contentWidth {
		line += strings.Repeat(" ", contentWidth-lineWidth)
	}
	return line
}

func splitChatStatusValueSegments(value string, maxWidth int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{""}
	}
	if maxWidth <= 0 || ui.DisplayWidth(value) <= maxWidth {
		return []string{value}
	}

	runes := []rune(value)
	segments := make([]string, 0, 2)
	start := 0
	for start < len(runes) {
		width := 0
		end := start
		lastBreak := -1
		for end < len(runes) {
			r := runes[end]
			rw := ui.DisplayWidth(string(r))
			if width+rw > maxWidth {
				break
			}
			width += rw
			if unicode.IsSpace(r) || r == '/' || r == '\\' {
				lastBreak = end
			}
			end++
		}

		if end == len(runes) {
			segment := strings.TrimSpace(string(runes[start:end]))
			if segment != "" {
				segments = append(segments, segment)
			}
			break
		}

		if lastBreak >= start {
			end = lastBreak + 1
		} else if end == start {
			end = start + 1
		}

		segment := strings.TrimSpace(string(runes[start:end]))
		if segment == "" {
			segment = string(runes[start:end])
		}
		segments = append(segments, segment)
		start = end
		for start < len(runes) && unicode.IsSpace(runes[start]) {
			start++
		}
	}

	if len(segments) == 0 {
		return []string{""}
	}
	return segments
}

func buildChatStatusModelValue(session *ChatSession) string {
	if session == nil {
		return "<none>"
	}
	model := strings.TrimSpace(session.Model)
	if model == "" {
		return "<none>"
	}
	reasoning := strings.TrimSpace(session.ReasoningEffort)
	if reasoning == "" {
		return model
	}
	return fmt.Sprintf("%s (reasoning %s)", model, reasoning)
}

func buildChatStatusProviderValue(session *ChatSession) string {
	if session == nil {
		return "<none>"
	}
	provider := strings.TrimSpace(session.ProviderName)
	if provider == "" {
		provider = strings.TrimSpace(session.Provider.GetProtocol())
	}
	if provider == "" {
		provider = "<none>"
	}
	endpoint := strings.TrimSpace(session.BaseURL)
	if endpoint == "" {
		endpoint = strings.TrimSpace(buildChatSessionEndpoint(session))
	}
	if endpoint == "" {
		return provider
	}
	if provider == "<none>" {
		return endpoint
	}
	return fmt.Sprintf("%s - %s", provider, endpoint)
}

func buildChatStatusBalanceValue(session *ChatSession) string {
	if session == nil {
		return "<none>"
	}
	// Prefer the concurrency-safe live refresh snapshot; direct/unit sessions
	// fall back to the provider/config cache.
	account, siteType, confidence := currentChatAccountBalance(session)
	line := formatProviderAccountBalanceLine(account, siteType, confidence)
	if line == "" {
		if siteType != "" && !strings.EqualFold(siteType, "unknown") {
			return fmt.Sprintf("no cached account (%s); run aicli balance --refresh or aicli login --require-account", siteType)
		}
		return "<none>"
	}
	if account != nil && strings.TrimSpace(account.FetchedAt) != "" {
		return fmt.Sprintf("%s · fetched %s", line, formatChatStatusLocalTime(account.FetchedAt))
	}
	return line
}

func formatChatStatusLocalTime(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return trimmed
	}
	return parsed.In(time.Local).Format("2006-01-02 15:04:05 -07:00")
}

func buildChatStatusDirectoryValue(session *ChatSession) string {
	if session == nil {
		return "<none>"
	}
	cwd := resolveChatStatusCurrentDirectory(session)
	if cwd == "" {
		return "<none>"
	}
	return resolveAbsoluteChatPath(cwd)
}

func buildChatStatusPermissionsValue(session *ChatSession) string {
	if session == nil {
		return "Default"
	}
	switch runtimepolicy.Mode(strings.TrimSpace(string(session.PermissionMode))) {
	case runtimepolicy.ModeBypassPermissions:
		return "Full Access"
	case runtimepolicy.ModeAcceptEdits:
		return "Accept Edits"
	case runtimepolicy.ModePlan:
		return "Plan"
	case runtimepolicy.ModeDefault, "":
		return "Default"
	default:
		return strings.ReplaceAll(strings.TrimSpace(string(session.PermissionMode)), "_", " ")
	}
}

func buildChatStatusAgentsMarkdownValue(session *ChatSession) string {
	path := resolveChatStatusAgentsMarkdownPath(session)
	if path == "" {
		return "<none>"
	}
	return path
}

func buildChatStatusCollaborationModeValue(session *ChatSession) string {
	if session != nil {
		if value := buildChatStatusCollaborationModeFromContext(session.ProfileContext); value != "" {
			return value
		}
	}
	for _, key := range []string{"AICLI_COLLABORATION_MODE", "AICLI_CHAT_COLLABORATION_MODE", "COLLABORATION_MODE"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return formatChatStatusModeValue(value)
		}
	}
	return "Default"
}

func buildChatStatusReasoningOutputValue(session *ChatSession) string {
	if chatReasoningOutputEnabled(session) {
		return "on"
	}
	return "off"
}

// buildChatStatusFastModeValue reports Codex Fast (service_tier=priority).
// Call only when chatSessionSupportsFastMode(session) is true.
func buildChatStatusFastModeValue(session *ChatSession) string {
	if session != nil && session.FastMode {
		return "on (priority)"
	}
	return "off"
}

func buildChatStatusSessionValue(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return "<none>"
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return "<none>"
	}
	parts := []string{sessionID}
	title := strings.TrimSpace(runtimeResumeSessionTitle(session.RuntimeSession))
	if title != "" && title != "(untitled)" {
		parts = append(parts, title)
	}
	// Only append a separate badge when the title does not already embed
	// "· compact #N" (sticky compact titles do). Keeps /status scannable without
	// "title · compact #2 · compact #2" redundancy.
	if generation := runtimeSessionCompactGeneration(session.RuntimeSession); generation > 0 {
		badge := fmt.Sprintf("compact #%d", generation)
		if title == "" || title == "(untitled)" || !strings.Contains(strings.ToLower(title), strings.ToLower(badge)) {
			parts = append(parts, badge)
		}
	}
	return strings.Join(parts, " · ")
}

func buildChatStatusTokenCountValue(session *ChatSession) string {
	if session == nil {
		return "<none>"
	}
	return compactStatusCount(session.TokenCount)
}

// chatStatusContextBaselineTokens mirrors Codex BASELINE_TOKENS: fixed system /
// tool overhead that should not count against the user-controllable window %.
const chatStatusContextBaselineTokens = 12000

func buildChatStatusContextUsedValue(session *ChatSession) string {
	if session == nil {
		return "<none>"
	}
	usedTokens := resolveChatStatusContextUsedTokens(session)
	windowTokens := resolveChatStatusContextWindowTokens(session)
	if windowTokens <= 0 {
		if usedTokens > 0 {
			return fmt.Sprintf("%d", usedTokens)
		}
		return "0"
	}
	percent := chatStatusContextUsedPercent(usedTokens, windowTokens)
	return fmt.Sprintf("%d / %d (%d%%)", usedTokens, windowTokens, percent)
}

func resolveChatStatusContextWindowTokens(session *ChatSession) int {
	if session == nil {
		return 0
	}
	budget := resolveSharedChatPromptBudget(session)
	if session.ContextWindowTokenCount > 0 {
		return session.ContextWindowTokenCount
	}
	if budget.ModelCapabilityMaxContextTokens > 0 {
		return budget.ModelCapabilityMaxContextTokens
	}
	if budget.ProviderContextLimit > 0 {
		return budget.ProviderContextLimit
	}
	return 0
}

// chatStatusContextUsedPercent returns the used portion of the effective context
// window, Codex-aligned:
//
//	effective = window - baseline
//	used_eff  = max(used - baseline, 0)
//	percent   = clamp(round(used_eff / effective * 100), 0, 100)
//
// Absolute used/window counts remain unclamped in the status line; only the
// percentage is bounded so UIs never show values like 114%.
func chatStatusContextUsedPercent(usedTokens, windowTokens int) int {
	if windowTokens <= 0 || usedTokens <= 0 {
		return 0
	}
	if windowTokens <= chatStatusContextBaselineTokens {
		percent := int(float64(usedTokens)*100/float64(windowTokens) + 0.5)
		return clampChatStatusPercent(percent)
	}
	effective := windowTokens - chatStatusContextBaselineTokens
	usedEff := usedTokens - chatStatusContextBaselineTokens
	if usedEff < 0 {
		usedEff = 0
	}
	percent := int(float64(usedEff)*100/float64(effective) + 0.5)
	return clampChatStatusPercent(percent)
}

func clampChatStatusPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func resolveChatStatusContextUsedTokens(session *ChatSession) int {
	if session == nil {
		return 0
	}
	if session.ContextTokenCount > 0 {
		return session.ContextTokenCount
	}
	return resolveChatContextSnapshotTokens(session, nil)
}

func buildChatStatusTokenUsageValue(session *ChatSession) string {
	usage := chatStatusTokenUsageSnapshotForSession(session)
	return fmt.Sprintf("%d total (%d input + %d output)", usage.Total, usage.Input, usage.Output)
}

func buildChatStatusLimitsValue(session *ChatSession) string {
	if session == nil {
		return "data not available yet"
	}
	budget := resolveSharedChatPromptBudget(session)
	if budget.ModelCapabilityMaxContextTokens > 0 {
		if budget.ActiveTurnMaxTokens > 0 && budget.ActiveTurnMaxTokens != budget.ModelCapabilityMaxContextTokens {
			return fmt.Sprintf("%d context tokens (active turn %d)", budget.ModelCapabilityMaxContextTokens, budget.ActiveTurnMaxTokens)
		}
		return fmt.Sprintf("%d context tokens", budget.ModelCapabilityMaxContextTokens)
	}
	// Capability missing: still surface the same window used for Context used
	// so /status never shows "unavailable" while the denominator is a real fallback.
	windowTokens := 0
	source := ""
	if session.ContextWindowTokenCount > 0 {
		windowTokens = session.ContextWindowTokenCount
		source = "session window"
	} else if budget.ProviderContextLimit > 0 {
		windowTokens = budget.ProviderContextLimit
		source = "provider default"
	}
	if windowTokens <= 0 {
		return "data not available yet"
	}
	if budget.ActiveTurnMaxTokens > 0 && budget.ActiveTurnMaxTokens != windowTokens {
		return fmt.Sprintf("%d context tokens (%s, active turn %d)", windowTokens, source, budget.ActiveTurnMaxTokens)
	}
	return fmt.Sprintf("%d context tokens (%s)", windowTokens, source)
}

type chatStatusTokenUsageSnapshot struct {
	Input  int
	Output int
	Total  int
}

func chatStatusTokenUsageSnapshotForSession(session *ChatSession) chatStatusTokenUsageSnapshot {
	snapshot := chatStatusTokenUsageSnapshot{}
	if session != nil {
		if session.InputTokenCount > 0 || session.OutputTokenCount > 0 {
			snapshot.Input = session.InputTokenCount
			snapshot.Output = session.OutputTokenCount
			if session.TokenCount > 0 {
				snapshot.Total = session.TokenCount
			} else {
				snapshot.Total = session.InputTokenCount + session.OutputTokenCount
			}
			return snapshot
		}
	}
	if session != nil && session.Logger != nil && session.Logger.sessionLog != nil {
		for _, entry := range session.Logger.sessionLog.Messages {
			if !strings.EqualFold(strings.TrimSpace(entry.MessageType), "response") {
				continue
			}
			usage, ok := chatStatusUsageFromAny(entry.Content)
			if !ok && len(entry.RawContentJSON) > 0 {
				usage, ok = chatStatusUsageFromAny(entry.RawContentJSON)
			}
			if !ok && strings.TrimSpace(entry.RawContent) != "" && json.Valid([]byte(entry.RawContent)) {
				usage, ok = chatStatusUsageFromAny([]byte(entry.RawContent))
			}
			if !ok {
				continue
			}
			snapshot.Input += usage.Input
			snapshot.Output += usage.Output
			if usage.Total > 0 {
				snapshot.Total += usage.Total
			} else {
				snapshot.Total += usage.Input + usage.Output
			}
		}
	}

	if snapshot.Total <= 0 && session != nil {
		if session.TokenCount > 0 {
			snapshot.Total = session.TokenCount
		} else if session.Logger != nil {
			if summary := session.Logger.CurrentSummary(); summary != nil && summary.TotalTokens > 0 {
				snapshot.Total = summary.TotalTokens
			}
		}
	}
	return snapshot
}

func chatStatusUsageFromAny(value interface{}) (chatStatusTokenUsageSnapshot, bool) {
	switch typed := value.(type) {
	case nil:
		return chatStatusTokenUsageSnapshot{}, false
	case map[string]interface{}:
		return chatStatusUsageFromMap(typed)
	case json.RawMessage:
		return chatStatusUsageFromRawJSON([]byte(typed))
	case []byte:
		return chatStatusUsageFromRawJSON(typed)
	case string:
		if strings.TrimSpace(typed) == "" || !json.Valid([]byte(typed)) {
			return chatStatusTokenUsageSnapshot{}, false
		}
		return chatStatusUsageFromRawJSON([]byte(typed))
	default:
		return chatStatusTokenUsageSnapshot{}, false
	}
}

func chatStatusUsageFromRawJSON(raw []byte) (chatStatusTokenUsageSnapshot, bool) {
	if len(raw) == 0 || !json.Valid(raw) {
		return chatStatusTokenUsageSnapshot{}, false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return chatStatusTokenUsageSnapshot{}, false
	}
	return chatStatusUsageFromMap(payload)
}

func chatStatusUsageFromMap(payload map[string]interface{}) (chatStatusTokenUsageSnapshot, bool) {
	if len(payload) == 0 {
		return chatStatusTokenUsageSnapshot{}, false
	}

	usage := chatStatusUsageFromFields(payload)
	found := usage.hasAny()
	if nested, ok := chatStatusUsageFromAny(payload["usage"]); ok {
		usage = chatStatusUsageBetter(usage, nested)
		found = true
	}
	return usage, found
}

func chatStatusUsageFromFields(payload map[string]interface{}) chatStatusTokenUsageSnapshot {
	usage := chatStatusTokenUsageSnapshot{
		Input:  firstPositiveChatStatusIntValue(payload, "input_tokens", "prompt_tokens", "usage_input_tokens", "usage_prompt_tokens"),
		Output: firstPositiveChatStatusIntValue(payload, "output_tokens", "completion_tokens", "usage_output_tokens", "usage_completion_tokens"),
		Total:  firstPositiveChatStatusIntValue(payload, "total_tokens", "usage_total_tokens"),
	}
	if usage.Total <= 0 && (usage.Input > 0 || usage.Output > 0) {
		usage.Total = usage.Input + usage.Output
	}
	return usage
}

func chatStatusUsageBetter(left, right chatStatusTokenUsageSnapshot) chatStatusTokenUsageSnapshot {
	if chatStatusUsageScore(right) > chatStatusUsageScore(left) {
		return right
	}
	if chatStatusUsageScore(right) == chatStatusUsageScore(left) && right.Total > left.Total {
		return right
	}
	return left
}

func chatStatusUsageScore(snapshot chatStatusTokenUsageSnapshot) int {
	score := 0
	if snapshot.Input > 0 {
		score++
	}
	if snapshot.Output > 0 {
		score++
	}
	if snapshot.Total > 0 {
		score++
	}
	return score
}

func (s chatStatusTokenUsageSnapshot) hasAny() bool {
	return s.Input > 0 || s.Output > 0 || s.Total > 0
}

func firstPositiveChatStatusIntValue(values map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if parsed := firstPositiveIntValue(value); parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func firstPositiveIntValue(value interface{}) int {
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return typed
		}
	case int8:
		if typed > 0 {
			return int(typed)
		}
	case int16:
		if typed > 0 {
			return int(typed)
		}
	case int32:
		if typed > 0 {
			return int(typed)
		}
	case int64:
		if typed > 0 {
			return int(typed)
		}
	case uint:
		if typed > 0 {
			return int(typed)
		}
	case uint8:
		if typed > 0 {
			return int(typed)
		}
	case uint16:
		if typed > 0 {
			return int(typed)
		}
	case uint32:
		if typed > 0 {
			return int(typed)
		}
	case uint64:
		if typed > 0 {
			return int(typed)
		}
	case float32:
		if typed > 0 {
			return int(typed)
		}
	case float64:
		if typed > 0 {
			return int(typed)
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed > 0 {
			return int(parsed)
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0
		}
		var parsed int
		if _, err := fmt.Sscanf(trimmed, "%d", &parsed); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func resolveChatStatusBoxContentWidth() int {
	width := ui.GetTerminalWidth()
	if width <= 0 {
		width = 80
	}
	contentWidth := width - 4
	if contentWidth > chatStatusDefaultContentWidth {
		contentWidth = chatStatusDefaultContentWidth
	}
	if contentWidth < chatStatusMinimumContentWidth {
		contentWidth = chatStatusMinimumContentWidth
	}
	return contentWidth
}

func resolveChatStatusAgentsMarkdownPath(session *ChatSession) string {
	startDir := resolveChatStatusCurrentDirectory(session)
	if startDir == "" {
		return ""
	}

	dir := resolveAbsoluteChatPath(startDir)
	if dir == "" {
		return ""
	}
	for {
		for _, name := range []string{"AGENTS.override.md", "AGENTS.md"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return resolveAbsoluteChatPath(candidate)
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func buildChatStatusCollaborationModeFromContext(ctx map[string]interface{}) string {
	if len(ctx) == 0 {
		return ""
	}
	for _, key := range []string{"collaboration_mode", "collaborationMode", "collaboration"} {
		if value, ok := ctx[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				return formatChatStatusModeValue(text)
			}
		}
	}
	return ""
}

func formatChatStatusModeValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch strings.ToLower(value) {
	case "default":
		return "Default"
	case "accept_edits":
		return "Accept edits"
	case "bypass_permissions":
		return "Full Access"
	case "plan":
		return "Plan"
	default:
		return strings.ReplaceAll(value, "_", " ")
	}
}
