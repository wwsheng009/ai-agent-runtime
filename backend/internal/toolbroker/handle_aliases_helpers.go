package toolbroker

import "strings"

func firstNonEmptyToolValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func valueOrEmptyAgentStatus(result *AgentStatusResult) string {
	if result == nil {
		return ""
	}
	return strings.TrimSpace(result.Status)
}

func valueOrEmptyWaitMatchedSession(result *AgentWaitResult) string {
	if result == nil {
		return ""
	}
	return strings.TrimSpace(result.MatchedSessionID)
}

func valueOrZeroWaitReadyCount(result *AgentWaitResult) int {
	if result == nil {
		return 0
	}
	return result.ReadyCount
}

func valueOrZeroAgentListCount(result *AgentListResult) int {
	if result == nil {
		return 0
	}
	return result.Count
}

func valueOrEmptyEventsSession(result *AgentEventsResult) string {
	if result == nil {
		return ""
	}
	return strings.TrimSpace(result.SessionID)
}

func valueOrZeroEventsCount(result *AgentEventsResult) int {
	if result == nil {
		return 0
	}
	return result.Count
}

func valueOrZeroEventsSeq(result *AgentEventsResult) int64 {
	if result == nil {
		return 0
	}
	return result.LatestSeq
}
