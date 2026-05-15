package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	chatRuntimeContextPermissionMode = sessionmeta.LegacyAICLIPermissionMode
	chatRuntimeContextActiveTeamID   = sessionmeta.LegacyAICLIActiveTeamID
	chatRuntimeContextActiveAgentID  = sessionmeta.LegacyAICLIActiveTeamAgentID
	chatRuntimeContextActiveTaskID   = sessionmeta.LegacyAICLIActiveTeamTaskID
	chatRuntimeContextSelectedAgent  = sessionmeta.LegacyAICLISelectedAgent
)

type chatTeamBinding struct {
	TeamID         string
	AgentID        string
	TaskID         string
	PermissionMode runtimepolicy.Mode
}

func (b *chatTeamBinding) Clone() *chatTeamBinding {
	if b == nil {
		return nil
	}
	clone := *b
	return &clone
}

func restoreChatRuntimeContext(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil || runtimeSession == nil {
		return
	}
	if mode, err := parseChatApprovalReuseMode(runtimeSessionContextString(runtimeSession, chatRuntimeContextApprovalReuse)); err == nil {
		session.ApprovalReuseMode = mode
	}
	if mode, err := parseChatPermissionMode(runtimeSessionContextString(runtimeSession, chatRuntimeContextPermissionMode), false); err == nil {
		session.PermissionMode = mode
	}
	if debugMode, ok := runtimeSessionContextBool(runtimeSession, chatRuntimeContextDebugMode); ok {
		session.DebugMode = debugMode
	} else {
		session.DebugMode = false
	}
	session.SelectedAgentTarget = runtimeSessionContextString(runtimeSession, chatRuntimeContextSelectedAgent)
	teamID := runtimeSessionContextString(runtimeSession, chatRuntimeContextActiveTeamID)
	if teamID == "" {
		session.ActiveTeam = nil
		return
	}
	session.ActiveTeam = &chatTeamBinding{
		TeamID:         teamID,
		AgentID:        firstNonEmptyChatValue(runtimeSessionContextString(runtimeSession, chatRuntimeContextActiveAgentID), "lead"),
		TaskID:         runtimeSessionContextString(runtimeSession, chatRuntimeContextActiveTaskID),
		PermissionMode: session.PermissionMode,
	}
}

func validateAmbientTeamBinding(session *ChatSession, store team.Store) {
	if session == nil || session.ActiveTeam == nil || store == nil {
		return
	}
	binding := session.ActiveTeam.Clone()
	record, err := store.GetTeam(contextBackground(), binding.TeamID)
	if err != nil || record == nil {
		fmt.Fprintf(os.Stderr, "Warning: 清理失效的 active team 绑定: %s\n", binding.TeamID)
		session.ActiveTeam = nil
		return
	}
	if binding.TaskID == "" {
		return
	}
	task, err := store.GetTask(contextBackground(), binding.TaskID)
	if err != nil || task == nil || strings.TrimSpace(task.TeamID) != strings.TrimSpace(binding.TeamID) {
		fmt.Fprintf(os.Stderr, "Warning: 清理失效的 active task 绑定: %s\n", binding.TaskID)
		session.ActiveTeam.TaskID = ""
	}
}

func inferAmbientTeamBinding(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil || runtimeSession == nil {
		return
	}
	toolCallNames := map[string]string{}
	for _, message := range runtimeSession.History {
		if message.Role != "assistant" {
			continue
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				continue
			}
			toolCallNames[strings.TrimSpace(call.ID)] = strings.TrimSpace(call.Name)
		}
	}

	for i := len(runtimeSession.History) - 1; i >= 0; i-- {
		message := runtimeSession.History[i]
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		toolName := toolCallNames[strings.TrimSpace(message.ToolCallID)]
		if toolName == "" {
			continue
		}
		payload := decodeToolResultPayload(message.Content)
		if payload == nil {
			payload = map[string]interface{}{}
		}
		if metadataPayload := messageToolMetadata(message.Metadata); len(metadataPayload) > 0 {
			for key, value := range metadataPayload {
				if _, exists := payload[key]; !exists {
					payload[key] = value
				}
			}
		}
		switch toolName {
		case toolbroker.ToolSpawnTeam:
			teamID := firstNonEmptyChatValue(payloadStringValue(payload["team_id"]))
			taskID := firstNonEmptyChatValue(payloadStringValue(payload["task_id"]))
			if teamID == "" {
				continue
			}
			if session.ActiveTeam == nil {
				session.ActiveTeam = &chatTeamBinding{}
			}
			session.ActiveTeam.TeamID = teamID
			session.ActiveTeam.AgentID = "lead"
			if taskID != "" {
				session.ActiveTeam.TaskID = taskID
			}
			if session.ActiveTeam.PermissionMode == "" {
				session.ActiveTeam.PermissionMode = session.PermissionMode
			}
			return
		case toolbroker.ToolReadTaskSpec, toolbroker.ToolReadTaskContext, toolbroker.ToolReportTaskOutcome, toolbroker.ToolBlockCurrentTask:
			teamID := firstNonEmptyChatValue(payloadStringValue(payload["team_id"]), payloadNestedStringValue(payload, "spec", "team_id"))
			taskID := firstNonEmptyChatValue(payloadStringValue(payload["task_id"]), payloadNestedStringValue(payload, "spec", "task_id"))
			if teamID == "" && session.ActiveTeam == nil {
				continue
			}
			if session.ActiveTeam == nil {
				session.ActiveTeam = &chatTeamBinding{AgentID: "lead"}
			}
			if teamID != "" {
				session.ActiveTeam.TeamID = teamID
			}
			if taskID != "" {
				session.ActiveTeam.TaskID = taskID
			} else if toolName == toolbroker.ToolReportTaskOutcome || toolName == toolbroker.ToolBlockCurrentTask {
				session.ActiveTeam.TaskID = ""
			}
			if session.ActiveTeam.AgentID == "" {
				session.ActiveTeam.AgentID = "lead"
			}
			return
		}
	}
}

func syncChatRuntimeContext(session *ChatSession, runtimeSession *runtimechat.Session) {
	if session == nil || runtimeSession == nil {
		return
	}
	if runtimeSession.Metadata.Context == nil {
		runtimeSession.Metadata.Context = make(map[string]interface{})
	}
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.Client, "aicli")
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.Entrypoint, "aicli")
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.PermissionMode, string(session.PermissionMode), chatRuntimeContextPermissionMode)
	if profileRef := strings.TrimSpace(session.ProfileReference); profileRef != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ProfileRef, profileRef, sessionmeta.LegacyAPIProfileReference)
	} else {
		sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ProfileRef, sessionmeta.LegacyAPIProfileReference)
	}
	if workspacePath := strings.TrimSpace(resolveLocalWorkspacePath(loadRuntimeToolConfig(session.Config, session), session)); workspacePath != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.WorkspacePath, workspacePath)
	}
	selectedAgentTarget := strings.TrimSpace(session.SelectedAgentTarget)
	if selectedAgentTarget != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.SelectedAgent, selectedAgentTarget, chatRuntimeContextSelectedAgent)
	} else {
		sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.SelectedAgent, chatRuntimeContextSelectedAgent)
	}
	requestedModel := strings.TrimSpace(session.Model)
	if requestedModel != "" {
		runtimeSession.Metadata.Context[toolbroker.AgentSessionContextRequestedModel] = requestedModel
	} else {
		delete(runtimeSession.Metadata.Context, toolbroker.AgentSessionContextRequestedModel)
	}
	sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamID, chatRuntimeContextActiveTeamID)
	sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamAgentID, chatRuntimeContextActiveAgentID)
	sessionmeta.Delete(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamTaskID, chatRuntimeContextActiveTaskID)
	if session.ActiveTeam == nil || strings.TrimSpace(session.ActiveTeam.TeamID) == "" {
		return
	}
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamID, strings.TrimSpace(session.ActiveTeam.TeamID), chatRuntimeContextActiveTeamID)
	sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamAgentID, firstNonEmptyChatValue(session.ActiveTeam.AgentID, "lead"), chatRuntimeContextActiveAgentID)
	if strings.TrimSpace(session.ActiveTeam.TaskID) != "" {
		sessionmeta.Set(runtimeSession.Metadata.Context, sessionmeta.ActiveTeamTaskID, strings.TrimSpace(session.ActiveTeam.TaskID), chatRuntimeContextActiveTaskID)
	}
}

func payloadStringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func payloadNestedStringValue(payload map[string]interface{}, key string, nested string) string {
	if payload == nil {
		return ""
	}
	child, _ := payload[key].(map[string]interface{})
	if child == nil {
		return ""
	}
	return payloadStringValue(child[nested])
}

func decodeToolResultPayload(content string) map[string]interface{} {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil
	}
	content = content[start : end+1]
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}
	return payload
}

func contextBackground() context.Context { return context.Background() }

func messageToolMetadata(metadata runtimetypes.Metadata) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	merged := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		merged[key] = value
	}
	if value, ok := metadata.Get("tool_metadata"); ok {
		if child, ok := value.(map[string]interface{}); ok {
			for key, nested := range child {
				if _, exists := merged[key]; !exists {
					merged[key] = nested
				}
			}
		}
	}
	return merged
}
