package commands

import (
	"context"
	"encoding/json"
	"strings"

	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func reconcileGoalCompletionFromToolMessages(session *ChatSession) (bool, error) {
	if session == nil || session.RuntimeSession == nil {
		return false, nil
	}
	var completed *runtimegoal.SessionGoal
	for _, message := range session.Messages {
		if strings.TrimSpace(message.Role) != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var payload struct {
			Goal *runtimegoal.SessionGoal `json:"goal"`
		}
		if err := json.Unmarshal([]byte(message.Content), &payload); err != nil {
			continue
		}
		if payload.Goal == nil || payload.Goal.Status != runtimegoal.StatusComplete {
			continue
		}
		if strings.TrimSpace(payload.Goal.CompletedBy) != "model" {
			continue
		}
		completed = payload.Goal
	}
	if completed == nil {
		return false, nil
	}
	store := runtimegoal.NewMetadataStore()
	if session.SessionManager != nil && session.SessionManager.GetStorage() != nil {
		updated, err := store.PutPersistent(context.Background(), session.SessionManager.GetStorage(), session.RuntimeSession.ID, *completed, runtimegoal.MutationModel)
		if err != nil {
			return false, err
		}
		if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
			return false, err
		}
	} else if err := store.Put(session.RuntimeSession, *completed); err != nil {
		return false, err
	}
	ensureChatSystemPromptMessage(session)
	return true, nil
}
