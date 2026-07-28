package contextmgr

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memory"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func compactMessages(messages []types.Message) *types.Message {
	return compactMessagesWithContinuation(messages, false)
}

func compactMessagesWithContinuation(messages []types.Message, continued bool) *types.Message {
	if len(messages) == 0 {
		return nil
	}

	const (
		userBudget         = 1400
		assistantBudget    = 1600
		toolBudget         = 2200
		failureBudget      = 1200
		constraintBudget   = 1000
		referenceBudget    = 1200
		remainingBudget    = 1000
		durableBudget      = 1400
		priorSummaryBudget = 2400
	)

	userItems := make([]string, 0, 12)
	assistantItems := make([]string, 0, 16)
	decisionItems := make([]string, 0, 8)
	toolItems := make([]string, 0, 20)
	failureItems := make([]string, 0, 10)
	constraintItems := make([]string, 0, 10)
	referenceItems := make([]string, 0, 12)
	remainingItems := make([]string, 0, 10)
	durableItems := make([]string, 0, 8)
	priorSummaryItems := make([]string, 0, 4)

	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		stage := strings.ToLower(strings.TrimSpace(message.Metadata.GetString("context_stage", "")))
		if stage == "compaction" {
			if content != "" {
				priorSummaryItems = appendWithinBudgetLatest(priorSummaryItems, summarizeLine(content, 900), 3, priorSummaryBudget)
			}
			continue
		}
		if isDurablePromptStage(stage) {
			if content != "" {
				durableItems = appendWithinBudgetLatest(durableItems, durablePromptStageLabel(stage)+": "+summarizeLine(content, 280), 6, durableBudget)
				constraintItems = appendManyWithinBudgetLatest(constraintItems, extractConstraintHints(content), 8, constraintBudget)
				remainingItems = appendManyWithinBudgetLatest(remainingItems, extractRemainingHints(content), 8, remainingBudget)
			}
			continue
		}
		if stage != "" {
			continue
		}
		switch message.Role {
		case "user", "developer":
			if content != "" {
				userItems = appendWithinBudgetLatest(userItems, summarizeLine(content, 240), 10, userBudget)
				constraintItems = appendManyWithinBudgetLatest(constraintItems, extractConstraintHints(content), 8, constraintBudget)
				remainingItems = appendManyWithinBudgetLatest(remainingItems, extractRemainingHints(content), 8, remainingBudget)
				referenceItems = appendManyWithinBudgetLatest(referenceItems, extractReferenceHints(content), 10, referenceBudget)
			}
		case "assistant":
			if len(message.ToolCalls) > 0 {
				names := make([]string, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					if call.Name != "" {
						names = append(names, call.Name)
					}
					referenceItems = appendManyWithinBudgetLatest(referenceItems, extractToolCallReferences(call), 10, referenceBudget)
				}
				if len(names) > 0 {
					toolItems = appendWithinBudgetLatest(toolItems, "assistant requested tools: "+strings.Join(names, ", "), 12, toolBudget)
				}
			}
			if content != "" {
				summary := summarizeLine(content, 220)
				if looksLikeDecision(strings.ToLower(content)) {
					decisionItems = appendWithinBudgetLatest(decisionItems, summary, 8, assistantBudget)
				} else {
					assistantItems = appendWithinBudgetLatest(assistantItems, summary, 12, assistantBudget)
				}
				constraintItems = appendManyWithinBudgetLatest(constraintItems, extractConstraintHints(content), 8, constraintBudget)
				remainingItems = appendManyWithinBudgetLatest(remainingItems, extractRemainingHints(content), 8, remainingBudget)
				referenceItems = appendManyWithinBudgetLatest(referenceItems, extractReferenceHints(content), 10, referenceBudget)
				if looksLikeFailure(strings.ToLower(content)) {
					failureItems = appendWithinBudgetLatest(failureItems, summary, 8, failureBudget)
				}
			}
		case "tool":
			if content != "" {
				toolItems = appendWithinBudgetLatest(toolItems, summarizeLine(content, 260), 16, toolBudget)
				referenceItems = appendManyWithinBudgetLatest(referenceItems, extractReferenceHints(content), 10, referenceBudget)
			}
			toolErr := strings.TrimSpace(message.Metadata.GetString("tool_error", ""))
			if toolErr == "" && looksLikeFailure(strings.ToLower(content)) {
				toolErr = content
			}
			if toolErr != "" {
				failureItems = appendWithinBudgetLatest(failureItems, summarizeLine(toolErr, 260), 8, failureBudget)
			}
		}
	}

	lines := []string{compactionHeading(continued)}
	if len(priorSummaryItems) > 0 {
		lines = append(lines, "Prior compacted context:")
		for _, item := range priorSummaryItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(userItems) > 0 {
		lines = append(lines, "Current goal / recent user requests:")
		for _, item := range userItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(durableItems) > 0 {
		lines = append(lines, "Durable session context:")
		for _, item := range durableItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(constraintItems) > 0 {
		lines = append(lines, "Constraints and preferences:")
		for _, item := range constraintItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(decisionItems) > 0 {
		lines = append(lines, "Key decisions:")
		for _, item := range decisionItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(assistantItems) > 0 {
		lines = append(lines, "Assistant progress:")
		for _, item := range assistantItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(toolItems) > 0 {
		lines = append(lines, "Tool outcomes:")
		for _, item := range toolItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(referenceItems) > 0 {
		lines = append(lines, "Critical references:")
		for _, item := range referenceItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(failureItems) > 0 {
		lines = append(lines, "Failures to account for:")
		for _, item := range failureItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(remainingItems) > 0 {
		lines = append(lines, "Remaining work:")
		for _, item := range remainingItems {
			lines = append(lines, "- "+item)
		}
	}

	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "compaction"
	message.Metadata["source_messages"] = len(messages)
	return message
}

func compactMessageText(messages []types.Message, continued bool) string {
	message := compactMessagesWithContinuation(messages, continued)
	if message == nil {
		return ""
	}
	return message.Content
}

func compactionHeading(continued bool) string {
	if continued {
		return "Compacted context from earlier turns (continued):"
	}
	return "Compacted context from earlier turns:"
}

func deriveMemoryEntries(sessionID, taskID, reason string, messages []types.Message, extraSourceRefs []string) []artifact.MemoryEntry {
	if len(messages) == 0 {
		return nil
	}

	entries := make([]artifact.MemoryEntry, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}

		lower := strings.ToLower(content)
		sourceRefs := mergeSourceRefs(extractArtifactRefs(message.Metadata), extraSourceRefs)
		entry := artifact.MemoryEntry{
			SessionID:  sessionID,
			TaskID:     taskID,
			Kind:       "fact",
			Priority:   60,
			Content:    map[string]interface{}{"summary": summarizeLine(content, 300), "reason": reason, "role": message.Role},
			SourceRefs: sourceRefs,
			CreatedAt:  messageTimeOrNow(message.Metadata),
		}

		switch {
		case looksLikeDecision(lower):
			entry.Kind = "decision"
			entry.Priority = 90
		case looksLikePlan(lower):
			entry.Kind = "plan"
			entry.Priority = 80
		case looksLikeOpenQuestion(lower):
			entry.Kind = "open_question"
			entry.Priority = 70
		case looksLikeFailure(lower):
			entry.Kind = "failure"
			entry.Priority = 85
		}

		entry.SourceHash = hashMessageEntry(entry.Kind, entry.Content, entry.SourceRefs)
		entries = append(entries, entry)
	}

	return dedupeEntries(entries)
}

func buildObservationMessage(observations []types.Observation, limit int) *types.Message {
	if limit <= 0 {
		limit = 6
	}

	source := observations
	if len(source) == 0 {
		return nil
	}
	if len(source) > limit {
		source = source[len(source)-limit:]
	}

	lines := []string{"Recent observations:"}
	for _, observation := range source {
		status := "ok"
		detail := ""
		if !observation.Success {
			status = "failed"
			detail = observation.Error
		} else if observation.Output != nil {
			detail = stableObservationDetail(observation.Output)
		}

		line := fmt.Sprintf("- [%s] %s", status, observation.Tool)
		if strings.TrimSpace(detail) != "" {
			line += ": " + summarizeLine(detail, 180)
		}
		lines = append(lines, line)
	}

	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "warm_memory"
	message.Metadata["observation_count"] = len(source)
	return message
}

func selectObservationsForMode(mem *memory.Memory, observations []types.Observation, mode string) []types.Observation {
	source := observations
	if len(source) == 0 && mem != nil {
		source = mem.Recent(12)
	}
	if len(source) == 0 {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ObservationModeFailures:
		failures := make([]types.Observation, 0, len(source))
		for _, observation := range source {
			if !observation.Success || strings.TrimSpace(observation.Error) != "" {
				failures = append(failures, observation)
			}
		}
		if len(failures) > 0 {
			return failures
		}
		return nil
	default:
		return source
	}
}

func appendLimited(items []string, item string, limit int) []string {
	if strings.TrimSpace(item) == "" || len(items) >= limit {
		return items
	}
	return append(items, item)
}

func appendWithinBudgetLatest(items []string, item string, maxItems, maxRunes int) []string {
	item = strings.TrimSpace(item)
	if item == "" || maxItems <= 0 || maxRunes <= 0 {
		return items
	}
	items = append(items, item)
	usedRunes := 0
	start := len(items)
	for index := len(items) - 1; index >= 0 && len(items)-index <= maxItems; index-- {
		itemRunes := len([]rune(items[index]))
		if usedRunes+itemRunes > maxRunes {
			break
		}
		usedRunes += itemRunes
		start = index
	}
	if start == len(items) {
		return []string{summarizeLine(item, maxRunes)}
	}
	return append([]string(nil), items[start:]...)
}

func appendManyWithinBudgetLatest(items []string, more []string, maxItems, maxRunes int) []string {
	for _, item := range more {
		items = appendWithinBudgetLatest(items, item, maxItems, maxRunes)
	}
	return items
}

func isDurablePromptStage(stage string) bool {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "active_goal", "todo_state", "team", "fact_ledger", "project_memory", "observation":
		return true
	default:
		return false
	}
}

func durablePromptStageLabel(stage string) string {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "active_goal":
		return "active goal"
	case "todo_state":
		return "todos"
	case "team":
		return "team"
	case "fact_ledger":
		return "fact ledger"
	case "project_memory":
		return "project memory"
	case "observation":
		return "observations"
	default:
		return strings.TrimSpace(stage)
	}
}

func extractConstraintHints(text string) []string {
	out := make([]string, 0, 4)
	for _, line := range splitPromptLines(text) {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "must ") ||
			strings.Contains(lower, "must not") ||
			strings.Contains(lower, "do not") ||
			strings.Contains(lower, "don't") ||
			strings.Contains(lower, "never ") ||
			strings.Contains(lower, "only ") ||
			strings.Contains(lower, "constraint") ||
			strings.Contains(lower, "preference") ||
			strings.Contains(lower, "require") ||
			strings.Contains(lower, "必须") ||
			strings.Contains(lower, "不要") ||
			strings.Contains(lower, "禁止") ||
			strings.Contains(lower, "只能") {
			out = append(out, summarizeLine(line, 220))
		}
	}
	return out
}

func extractRemainingHints(text string) []string {
	out := make([]string, 0, 4)
	for _, line := range splitPromptLines(text) {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "todo") ||
			strings.Contains(lower, "next step") ||
			strings.Contains(lower, "remaining") ||
			strings.Contains(lower, "still need") ||
			strings.Contains(lower, "follow up") ||
			strings.Contains(lower, "待办") ||
			strings.Contains(lower, "下一步") ||
			strings.Contains(lower, "剩余") ||
			strings.HasPrefix(lower, "- [ ]") ||
			strings.HasPrefix(lower, "* [ ]") {
			out = append(out, summarizeLine(line, 220))
		}
	}
	return out
}

func extractToolCallReferences(call types.ToolCall) []string {
	if len(call.Args) == 0 {
		return nil
	}
	keys := []string{
		"path", "file_path", "filepath", "file", "cwd", "workdir",
		"command", "cmd", "url", "query", "pattern", "target",
		"session_id", "team_id", "goal_id", "id",
	}
	out := make([]string, 0, len(keys))
	name := strings.TrimSpace(call.Name)
	for _, key := range keys {
		value, ok := call.Args[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprintf("%v", value))
		if text == "" {
			continue
		}
		item := key + "=" + summarizeLine(text, 180)
		if name != "" {
			item = name + ": " + item
		}
		out = append(out, item)
	}
	return out
}

func extractReferenceHints(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	candidates := make([]string, 0, 8)
	for _, token := range strings.Fields(text) {
		token = strings.Trim(token, "`,\"'()[]{}<>")
		if token == "" {
			continue
		}
		if looksLikeReferenceToken(token) {
			candidates = append(candidates, summarizeLine(token, 180))
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, 8)
	for _, item := range candidates {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func looksLikeReferenceToken(token string) bool {
	if strings.Contains(token, "://") {
		return true
	}
	if strings.ContainsAny(token, `/\`) && len(token) >= 3 {
		return true
	}
	if strings.HasPrefix(token, "evt_") || strings.HasPrefix(token, "goal_") || strings.HasPrefix(token, "session-") || strings.HasPrefix(token, "team-") {
		return true
	}
	return false
}

func splitPromptLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return []string{trimmed}
		}
	}
	return out
}

func summarizeLine(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))), " ")
	if text == "" || limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func stableObservationDetail(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	}

	payload, err := json.Marshal(value)
	if err == nil && len(payload) > 0 {
		return string(payload)
	}
	return fmt.Sprintf("%v", value)
}

func looksLikeDecision(s string) bool {
	return strings.Contains(s, "decision:") ||
		strings.Contains(s, "conclusion:") ||
		strings.Contains(s, "most likely") ||
		strings.Contains(s, "we should")
}

func looksLikePlan(s string) bool {
	return strings.Contains(s, "plan:") ||
		strings.Contains(s, "next steps") ||
		strings.Contains(s, "step 1") ||
		strings.Contains(s, "i will")
}

func looksLikeOpenQuestion(s string) bool {
	return strings.Contains(s, "unknown") ||
		strings.Contains(s, "unclear") ||
		strings.Contains(s, "need to verify") ||
		strings.Contains(s, "not sure")
}

func looksLikeFailure(s string) bool {
	return strings.Contains(s, "failed") ||
		strings.Contains(s, "error") ||
		strings.Contains(s, "denied") ||
		strings.Contains(s, "panic")
}

func extractArtifactRefs(metadata types.Metadata) []string {
	if metadata == nil {
		return nil
	}
	refs := make([]string, 0)
	for _, key := range []string{"artifact_refs", "source_refs"} {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			refs = append(refs, typed...)
		case []interface{}:
			for _, ref := range typed {
				if text, ok := ref.(string); ok && text != "" {
					refs = append(refs, text)
				}
			}
		}
	}
	return mergeSourceRefs(refs)
}

func hashHistory(messages []types.Message) string {
	parts := make([]string, 0, len(messages)*2)
	for _, message := range messages {
		parts = append(parts, message.Role)
		parts = append(parts, strings.TrimSpace(message.Content))
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("%x", sum[:])
}

func hashMessageEntry(kind string, content map[string]interface{}, refs []string) string {
	keys := make([]string, 0, len(content))
	for key := range content {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := []string{kind}
	for _, key := range keys {
		parts = append(parts, key+"="+fmt.Sprintf("%v", content[key]))
	}
	if len(refs) > 0 {
		parts = append(parts, strings.Join(refs, ","))
	}

	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("%x", sum[:])
}

func dedupeEntries(entries []artifact.MemoryEntry) []artifact.MemoryEntry {
	seen := make(map[string]bool, len(entries))
	out := make([]artifact.MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		if seen[entry.SourceHash] {
			continue
		}
		seen[entry.SourceHash] = true
		out = append(out, entry)
	}
	return out
}

func mergeSourceRefs(groups ...[]string) []string {
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, group := range groups {
		for _, ref := range group {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			if _, ok := seen[ref]; ok {
				continue
			}
			seen[ref] = struct{}{}
			merged = append(merged, ref)
		}
	}
	return merged
}

func messageTimeOrNow(metadata types.Metadata) time.Time {
	return time.Now().UTC()
}
