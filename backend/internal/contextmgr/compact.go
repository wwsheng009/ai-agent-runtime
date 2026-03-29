package contextmgr

import (
	"crypto/sha1"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/memory"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func compactMessages(messages []types.Message) *types.Message {
	if len(messages) == 0 {
		return nil
	}

	userItems := make([]string, 0, 3)
	assistantItems := make([]string, 0, 3)
	toolItems := make([]string, 0, 4)

	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}

		switch message.Role {
		case "user":
			userItems = appendLimited(userItems, summarizeLine(content, 160), 3)
		case "assistant":
			if len(message.ToolCalls) > 0 {
				names := make([]string, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					if call.Name != "" {
						names = append(names, call.Name)
					}
				}
				if len(names) > 0 {
					toolItems = appendLimited(toolItems, "assistant requested tools: "+strings.Join(names, ", "), 4)
				}
			}
			assistantItems = appendLimited(assistantItems, summarizeLine(content, 160), 3)
		case "tool":
			toolItems = appendLimited(toolItems, summarizeLine(content, 180), 4)
		}
	}

	lines := []string{"Compacted context from earlier turns:"}
	if len(userItems) > 0 {
		lines = append(lines, "User goals:")
		for _, item := range userItems {
			lines = append(lines, "- "+item)
		}
	}
	if len(assistantItems) > 0 {
		lines = append(lines, "Assistant decisions:")
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

	message := types.NewAssistantMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "compaction"
	message.Metadata["source_messages"] = len(messages)
	return message
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
		} else if output, ok := observation.Output.(string); ok {
			detail = output
		} else if observation.Output != nil {
			detail = fmt.Sprintf("%v", observation.Output)
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
