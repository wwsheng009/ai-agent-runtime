package sessionmeta

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	ProviderName       = "provider_name"
	ProviderProtocol   = "provider_protocol"
	Model              = "model"
	ReasoningEffort    = "reasoning_effort"
	ApprovalReuse      = "approval_reuse"
	Stream             = "stream"
	DisableTools       = "disable_tools"
	DebugMode          = "debug_mode"
	ProfileRef         = "profile_ref"
	ProfileName        = "profile_name"
	ProfileAgent       = "profile_agent"
	ProfileRoot        = "profile_root"
	WorkspacePath      = "workspace_path"
	Client             = "client"
	Entrypoint         = "entrypoint"
	MessageCount       = "message_count"
	TokenCount         = "token_count"
	ContextTokenCount  = "context_token_count"
	ContextWindowCount = "context_window_token_count"
	TurnContextCount   = "turn_context_token_count"
	PermissionMode     = "permission_mode"
	ActiveTeamID       = "active_team_id"
	ActiveTeamAgentID  = "active_team_agent_id"
	ActiveTeamTaskID   = "active_team_task_id"
	SelectedAgent      = "selected_agent_target"

	LegacyAICLIProviderName       = "aicli_provider_name"
	LegacyAICLIProviderProtocol   = "aicli_protocol"
	LegacyAICLIModel              = "aicli_model"
	LegacyAICLIReasoningEffort    = "aicli_reasoning_effort"
	LegacyAICLIApprovalReuse      = "aicli_approval_reuse"
	LegacyAICLIStream             = "aicli_stream"
	LegacyAICLIDisableTools       = "aicli_disable_tools"
	LegacyAICLIDebugMode          = "aicli_debug_mode"
	LegacyAICLIMessageCount       = "aicli_message_count"
	LegacyAICLIProfileName        = "aicli_profile_name"
	LegacyAICLIProfileAgent       = "aicli_profile_agent"
	LegacyAICLIProfileRoot        = "aicli_profile_root"
	LegacyAICLITokenCount         = "aicli_token_count"
	LegacyAICLIContextTokenCount  = "aicli_context_token_count"
	LegacyAICLIContextWindowCount = "aicli_context_window_token_count"
	LegacyAICLITurnContextCount   = "aicli_turn_context_token_count"
	LegacyAICLIPermissionMode     = "aicli_permission_mode"
	LegacyAICLIActiveTeamID       = "aicli_active_team_id"
	LegacyAICLIActiveTeamAgentID  = "aicli_active_team_agent_id"
	LegacyAICLIActiveTeamTaskID   = "aicli_active_task_id"
	LegacyAICLISelectedAgent      = "aicli_selected_agent_target"

	LegacyAPIProfileReference = "profile_reference"
)

var legacyToCanonical = map[string]string{
	LegacyAICLIProviderName:       ProviderName,
	LegacyAICLIProviderProtocol:   ProviderProtocol,
	LegacyAICLIModel:              Model,
	LegacyAICLIReasoningEffort:    ReasoningEffort,
	LegacyAICLIApprovalReuse:      ApprovalReuse,
	LegacyAICLIStream:             Stream,
	LegacyAICLIDisableTools:       DisableTools,
	LegacyAICLIDebugMode:          DebugMode,
	LegacyAICLIMessageCount:       MessageCount,
	LegacyAICLIProfileName:        ProfileName,
	LegacyAICLIProfileAgent:       ProfileAgent,
	LegacyAICLIProfileRoot:        ProfileRoot,
	LegacyAICLITokenCount:         TokenCount,
	LegacyAICLIContextTokenCount:  ContextTokenCount,
	LegacyAICLIContextWindowCount: ContextWindowCount,
	LegacyAICLITurnContextCount:   TurnContextCount,
	LegacyAICLIPermissionMode:     PermissionMode,
	LegacyAICLIActiveTeamID:       ActiveTeamID,
	LegacyAICLIActiveTeamAgentID:  ActiveTeamAgentID,
	LegacyAICLIActiveTeamTaskID:   ActiveTeamTaskID,
	LegacyAICLISelectedAgent:      SelectedAgent,
	LegacyAPIProfileReference:     ProfileRef,
}

var canonicalToLegacy = map[string][]string{
	ProviderName:       {LegacyAICLIProviderName},
	ProviderProtocol:   {LegacyAICLIProviderProtocol},
	Model:              {LegacyAICLIModel},
	ReasoningEffort:    {LegacyAICLIReasoningEffort},
	ApprovalReuse:      {LegacyAICLIApprovalReuse},
	Stream:             {LegacyAICLIStream},
	DisableTools:       {LegacyAICLIDisableTools},
	DebugMode:          {LegacyAICLIDebugMode},
	MessageCount:       {LegacyAICLIMessageCount},
	ProfileRef:         {LegacyAPIProfileReference},
	ProfileName:        {LegacyAICLIProfileName},
	ProfileAgent:       {LegacyAICLIProfileAgent},
	ProfileRoot:        {LegacyAICLIProfileRoot},
	TokenCount:         {LegacyAICLITokenCount},
	ContextTokenCount:  {LegacyAICLIContextTokenCount},
	ContextWindowCount: {LegacyAICLIContextWindowCount},
	TurnContextCount:   {LegacyAICLITurnContextCount},
	PermissionMode:     {LegacyAICLIPermissionMode},
	ActiveTeamID:       {LegacyAICLIActiveTeamID},
	ActiveTeamAgentID:  {LegacyAICLIActiveTeamAgentID},
	ActiveTeamTaskID:   {LegacyAICLIActiveTeamTaskID},
	SelectedAgent:      {LegacyAICLISelectedAgent},
}

func CanonicalKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if canonical := legacyToCanonical[key]; canonical != "" {
		return canonical
	}
	return key
}

func CandidateKeys(key string) []string {
	canonical := CanonicalKey(key)
	if canonical == "" {
		return nil
	}
	keys := []string{canonical}
	for _, legacy := range canonicalToLegacy[canonical] {
		if legacy != "" && legacy != canonical {
			keys = append(keys, legacy)
		}
	}
	if key != canonical {
		found := false
		for _, candidate := range keys {
			if candidate == key {
				found = true
				break
			}
		}
		if !found {
			keys = append(keys, key)
		}
	}
	return keys
}

func Set(ctx map[string]interface{}, key string, value interface{}, legacyKeys ...string) {
	if ctx == nil {
		return
	}
	key = CanonicalKey(key)
	if key == "" {
		return
	}
	ctx[key] = value
	for _, legacy := range legacyKeys {
		legacy = strings.TrimSpace(legacy)
		if legacy == "" || legacy == key {
			continue
		}
		ctx[legacy] = value
	}
}

func Delete(ctx map[string]interface{}, key string, legacyKeys ...string) {
	if ctx == nil {
		return
	}
	keys := CandidateKeys(key)
	keys = append(keys, legacyKeys...)
	for _, candidate := range keys {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			delete(ctx, candidate)
		}
	}
}

func Value(ctx map[string]interface{}, key string) (interface{}, bool) {
	if ctx == nil {
		return nil, false
	}
	for _, candidate := range CandidateKeys(key) {
		value, ok := ctx[candidate]
		if ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func String(ctx map[string]interface{}, key string) string {
	value, ok := Value(ctx, key)
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func Bool(ctx map[string]interface{}, key string) (bool, bool) {
	value, ok := Value(ctx, key)
	if !ok || value == nil {
		return false, false
	}
	boolean, ok := value.(bool)
	return boolean, ok
}

func Int(ctx map[string]interface{}, key string) (int, bool) {
	value, ok := Value(ctx, key)
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
