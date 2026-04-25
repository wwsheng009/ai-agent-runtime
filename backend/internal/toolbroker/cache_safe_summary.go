package toolbroker

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const cacheSafeSummaryMetadataKey = "cache_safe_summary"

func attachCacheSafeSummary(meta map[string]interface{}, summary string) map[string]interface{} {
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta[toolresult.MetadataKey] = toolresult.KindStructured
	summary = strings.TrimSpace(summary)
	if summary != "" {
		meta[cacheSafeSummaryMetadataKey] = summary
	}
	return meta
}

func spawnTeamCacheSafeSummary(result SpawnTeamResult) string {
	action := "Created"
	if !result.CreatedTeam {
		action = "Reused"
	}
	parts := []string{
		fmt.Sprintf("%s team run with %d teammates and %d tasks.", action, result.TeammateCount, result.TaskCount),
	}
	if result.AutoStarted {
		parts = append(parts, "Background orchestration auto-started.")
	} else {
		parts = append(parts, "Background orchestration not auto-started.")
	}
	return strings.Join(parts, " ")
}

func sendTeamMessageCacheSafeSummary(result SendTeamMessageResult) string {
	parts := []string{
		fmt.Sprintf("Sent %s team message from %s to %s.", firstNonEmptyString(strings.TrimSpace(result.Kind), "info"), firstNonEmptyString(strings.TrimSpace(result.FromAgent), "current_agent"), firstNonEmptyString(strings.TrimSpace(result.ToAgent), "*")),
	}
	if strings.TrimSpace(result.TaskID) != "" {
		parts = append(parts, "Linked to the current task.")
	}
	return strings.Join(parts, " ")
}

func readMailboxDigestCacheSafeSummary(result ReadMailboxDigestResult) string {
	header := fmt.Sprintf("Mailbox digest for %s with %d messages.", firstNonEmptyString(strings.TrimSpace(result.AgentID), "current_agent"), result.MessageCount)
	if result.MarkedRead {
		header += " Messages marked read."
	} else {
		header += " Messages left unread."
	}
	digest := truncateCacheSafeSummary(result.Digest, 400)
	if digest == "" {
		return header
	}
	return header + "\nDigest: " + digest
}

func readTaskSpecCacheSafeSummary(result ReadTaskSpecResult) string {
	lines := make([]string, 0, 5)
	if title := truncateCacheSafeSummary(result.Title, 160); title != "" {
		lines = append(lines, "Task: "+title)
	} else {
		lines = append(lines, "Task specification loaded.")
	}
	if status := strings.TrimSpace(result.Status); status != "" {
		lines = append(lines, "Status: "+status)
	}
	if goal := truncateCacheSafeSummary(result.Goal, 220); goal != "" {
		lines = append(lines, "Goal: "+goal)
	}
	if summary := truncateCacheSafeSummary(result.Summary, 220); summary != "" {
		lines = append(lines, "Summary: "+summary)
	}
	if assignee := strings.TrimSpace(result.Assignee); assignee != "" {
		lines = append(lines, "Assignee: "+assignee)
	}
	return strings.Join(lines, "\n")
}

func readTaskContextCacheSafeSummary(result ReadTaskContextResult) string {
	lines := make([]string, 0, 6)
	specSummary := readTaskSpecCacheSafeSummary(result.Spec)
	if specSummary != "" {
		lines = append(lines, specSummary)
	}
	if teamContext := truncateCacheSafeSummary(result.TeamContext, 260); teamContext != "" {
		lines = append(lines, "Team context: "+teamContext)
	}
	if mailbox := truncateCacheSafeSummary(result.MailboxDigest, 220); mailbox != "" {
		lines = append(lines, "Mailbox: "+mailbox)
	}
	if len(result.Dependencies) > 0 || len(result.Dependents) > 0 {
		lines = append(lines, fmt.Sprintf("Dependencies: %d. Dependents: %d.", len(result.Dependencies), len(result.Dependents)))
	}
	if result.MessageCount > 0 {
		if result.MarkedRead {
			lines = append(lines, fmt.Sprintf("Included %d mailbox messages and marked them read.", result.MessageCount))
		} else {
			lines = append(lines, fmt.Sprintf("Included %d mailbox messages without marking them read.", result.MessageCount))
		}
	}
	return strings.Join(lines, "\n")
}

func reportTaskOutcomeCacheSafeSummary(result ReportTaskOutcomeResult) string {
	lines := make([]string, 0, 6)
	status := firstNonEmptyString(strings.TrimSpace(result.Outcome), strings.TrimSpace(result.Status))
	if status != "" {
		lines = append(lines, "Task outcome: "+status)
	} else {
		lines = append(lines, "Task outcome recorded.")
	}
	if summary := truncateCacheSafeSummary(result.Summary, 220); summary != "" {
		lines = append(lines, "Summary: "+summary)
	}
	if blocker := truncateCacheSafeSummary(result.Blocker, 180); blocker != "" {
		lines = append(lines, "Blocker: "+blocker)
	}
	if handoff := strings.TrimSpace(result.HandoffTo); handoff != "" {
		lines = append(lines, "Handoff to: "+handoff)
	}
	if result.Replanned {
		lines = append(lines, fmt.Sprintf("Replanned %d follow-up tasks.", len(result.PlannedTaskIDs)))
	}
	if replanError := truncateCacheSafeSummary(result.ReplanError, 180); replanError != "" {
		lines = append(lines, "Replan error: "+replanError)
	}
	return strings.Join(lines, "\n")
}

func agentStatusCacheSafeSummary(result *AgentStatusResult) string {
	if result == nil {
		return ""
	}
	lines := make([]string, 0, 5)
	sessionRef := firstNonEmptyString(strings.TrimSpace(result.SessionID), strings.TrimSpace(result.ID), "child_agent")
	status := firstNonEmptyString(strings.TrimSpace(result.Status), "unknown")
	lines = append(lines, fmt.Sprintf("Child agent %s status: %s.", sessionRef, status))
	if strings.TrimSpace(result.AgentType) != "" {
		lines = append(lines, "Agent type: "+strings.TrimSpace(result.AgentType))
	}
	stateNotes := make([]string, 0, 4)
	if result.Created {
		stateNotes = append(stateNotes, "created")
	}
	if result.Queued {
		stateNotes = append(stateNotes, "queued")
	}
	if result.PendingApproval {
		stateNotes = append(stateNotes, "waiting approval")
	}
	if result.PendingQuestion {
		stateNotes = append(stateNotes, "waiting question answer")
	}
	if result.TimedOut {
		stateNotes = append(stateNotes, "timed out")
	}
	if len(stateNotes) > 0 {
		lines = append(lines, "Flags: "+strings.Join(stateNotes, ", "))
	}
	if result.MessageCount > 0 {
		lines = append(lines, fmt.Sprintf("Messages: %d.", result.MessageCount))
	}
	outputPreview := truncateCacheSafeSummary(result.Output, 200)
	if outputPreview != "" {
		lines = append(lines, "Output: "+outputPreview)
	}
	if preview := truncateCacheSafeSummary(result.LastMessagePreview, 180); preview != "" && outputPreview == "" {
		lines = append(lines, "Last message: "+preview)
	}
	if errText := truncateCacheSafeSummary(result.Error, 180); errText != "" {
		lines = append(lines, "Error: "+errText)
	}
	return strings.Join(lines, "\n")
}

func agentWaitCacheSafeSummary(result *AgentWaitResult) string {
	if result == nil {
		return ""
	}
	lines := []string{
		fmt.Sprintf("Waited on child agents. Ready: %d. Pending: %d.", result.ReadyCount, result.PendingCount),
	}
	if result.TimedOut {
		lines = append(lines, "Wait timed out before a ready agent was found.")
	}
	if result.Agent != nil {
		lines = append(lines, agentStatusCacheSafeSummary(result.Agent))
	}
	return strings.Join(lines, "\n")
}

func agentEventsCacheSafeSummary(result *AgentEventsResult) string {
	if result == nil {
		return ""
	}
	lines := []string{
		fmt.Sprintf("Read %d events from child agent %s.", result.Count, firstNonEmptyString(strings.TrimSpace(result.SessionID), "child_agent")),
	}
	if result.TimedOut {
		lines = append(lines, "Read timed out while waiting for new events.")
	}
	if len(result.Events) > 0 {
		types := make([]string, 0, len(result.Events))
		seen := make(map[string]struct{}, len(result.Events))
		for _, event := range result.Events {
			eventType := strings.TrimSpace(event.Type)
			if eventType == "" {
				continue
			}
			if _, ok := seen[eventType]; ok {
				continue
			}
			seen[eventType] = struct{}{}
			types = append(types, eventType)
			if len(types) == 4 {
				break
			}
		}
		if len(types) > 0 {
			lines = append(lines, "Event types: "+strings.Join(types, ", "))
		}
	}
	return strings.Join(lines, "\n")
}

func truncateCacheSafeSummary(text string, maxLen int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || maxLen <= 0 {
		return text
	}
	if len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return text[:maxLen-3] + "..."
}
