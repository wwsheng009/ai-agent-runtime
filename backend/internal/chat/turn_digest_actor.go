package chat

import (
	"context"
	"strings"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func (a *SessionActor) recordTurnDigest(digest TurnDigest) {
	if a == nil || a.turnDigestSink == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.turnDigestSink.RecordTurnDigest(ctx, digest); err != nil {
		logpkg.Warnf("memory turn digest write failed: session=%s turn=%s: %v", a.id, digest.TurnID, err)
	}
}

// turnDigestMessageIDs resolves canonical message IDs for the final user and
// assistant messages of a turn. Missing IDs are allowed; the digest keeps the
// turn_id as the stable correlation key.
func turnDigestMessageIDs(session *Session, turnID string) (userID, assistantID string) {
	if session == nil {
		return "", ""
	}
	turnID = strings.TrimSpace(turnID)
	for index := len(session.History) - 1; index >= 0; index-- {
		message := session.History[index]
		if turnID != "" && strings.TrimSpace(types.TurnID(message)) != turnID {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			if assistantID == "" {
				assistantID = strings.TrimSpace(types.MessageID(message))
			}
		case "user":
			if userID == "" {
				userID = strings.TrimSpace(types.MessageID(message))
			}
		}
		if userID != "" && assistantID != "" {
			break
		}
	}
	return userID, assistantID
}
