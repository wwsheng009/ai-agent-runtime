package toolbroker

import "strings"

func aliasAgentStatusResult(result *AgentStatusResult, aliases *handleAliasRegistry) *AgentStatusResult {
	if result == nil {
		return nil
	}
	cloned := *result
	if aliases != nil {
		cloned.ID = aliases.Sessions.aliasFor(cloned.ID)
		cloned.SessionID = aliases.Sessions.aliasFor(cloned.SessionID)
		cloned.ParentSessionID = aliasSessionValue(cloned.ParentSessionID, aliases)
	}
	return &cloned
}

func aliasAgentWaitResult(result *AgentWaitResult, aliases *handleAliasRegistry) *AgentWaitResult {
	if result == nil {
		return nil
	}
	cloned := *result
	if result.Agent != nil {
		cloned.Agent = aliasAgentStatusResult(result.Agent, aliases)
	}
	if len(result.Agents) > 0 {
		cloned.Agents = make([]AgentStatusResult, 0, len(result.Agents))
		for _, agent := range result.Agents {
			aliased := aliasAgentStatusResult(&agent, aliases)
			if aliased != nil {
				cloned.Agents = append(cloned.Agents, *aliased)
			}
		}
	}
	if result.Event != nil {
		event := *result.Event
		event.SessionID = aliasSessionValue(event.SessionID, aliases)
		cloned.Event = &event
	}
	if len(result.Events) > 0 {
		cloned.Events = make([]AgentEventItem, len(result.Events))
		copy(cloned.Events, result.Events)
		for index := range cloned.Events {
			cloned.Events[index].SessionID = aliasSessionValue(cloned.Events[index].SessionID, aliases)
		}
	}
	cloned.MatchedID = aliasSessionValue(cloned.MatchedID, aliases)
	cloned.MatchedSessionID = aliasSessionValue(cloned.MatchedSessionID, aliases)
	return &cloned
}

func aliasAgentListResult(result *AgentListResult, aliases *handleAliasRegistry) *AgentListResult {
	if result == nil {
		return nil
	}
	cloned := *result
	if len(result.Agents) > 0 {
		cloned.Agents = make([]AgentStatusResult, 0, len(result.Agents))
		for _, agent := range result.Agents {
			aliased := aliasAgentStatusResult(&agent, aliases)
			if aliased != nil {
				cloned.Agents = append(cloned.Agents, *aliased)
			}
		}
	}
	return &cloned
}

func aliasAgentMessageResult(result *AgentMessageResult, aliases *handleAliasRegistry) *AgentMessageResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.TargetSessionID = aliasSessionValue(cloned.TargetSessionID, aliases)
	if result.Status != nil {
		cloned.Status = aliasAgentStatusResult(result.Status, aliases)
	}
	return &cloned
}

func aliasAgentEventsResult(result *AgentEventsResult, aliases *handleAliasRegistry) *AgentEventsResult {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.SessionID = aliasSessionValue(cloned.SessionID, aliases)
	if len(result.Events) > 0 {
		cloned.Events = make([]AgentEventItem, len(result.Events))
		for index, event := range result.Events {
			clonedEvent := event
			clonedEvent.SessionID = aliasSessionValue(clonedEvent.SessionID, aliases)
			clonedEvent.Payload = aliasSessionPayloadMap(clonedEvent.Payload, aliases)
			cloned.Events[index] = clonedEvent
		}
	}
	return &cloned
}

func aliasSessionValue(value string, aliases *handleAliasRegistry) string {
	if aliases == nil {
		return strings.TrimSpace(value)
	}
	return aliases.Sessions.aliasFor(value)
}

func aliasSessionPayloadMap(payload map[string]interface{}, aliases *handleAliasRegistry) map[string]interface{} {
	if len(payload) == 0 || aliases == nil {
		return cloneAliasPayloadMap(payload)
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = aliasSessionPayloadValue(strings.TrimSpace(key), value, aliases)
	}
	return cloned
}

func aliasSessionPayloadValue(key string, value interface{}, aliases *handleAliasRegistry) interface{} {
	switch typed := value.(type) {
	case string:
		switch key {
		case "session_id", "parent_session_id", "matched_session_id":
			return aliasSessionValue(typed, aliases)
		default:
			return typed
		}
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = aliasSessionPayloadValue(key, item, aliases)
		}
		return cloned
	case []string:
		if key != "session_ids" && key != "ids" {
			return append([]string(nil), typed...)
		}
		cloned := make([]string, len(typed))
		for index, item := range typed {
			cloned[index] = aliasSessionValue(item, aliases)
		}
		return cloned
	case map[string]interface{}:
		return aliasSessionPayloadMap(typed, aliases)
	default:
		return value
	}
}

func cloneAliasPayloadMap(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}
