package memoryruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// SessionIdentity is trusted session metadata, never model-supplied digest
// text. Hosts use it to enforce producer isolation before saving an outbox row.
type SessionIdentity struct {
	Session         SessionRef
	ParentSessionID string
	AgentType       string
}

// SessionIdentitySource avoids listing sessions or loading transcript history
// on the turn-end path. Other source implementations can use the bounded
// SessionSource lookup fallback.
type SessionIdentitySource interface {
	ReadSessionIdentity(context.Context, string) (SessionIdentity, error)
}

func (s *ChatSessionSource) ReadSessionIdentity(ctx context.Context, sessionID string) (SessionIdentity, error) {
	if s == nil || s.Manager == nil || s.Manager.GetStorage() == nil {
		return SessionIdentity{}, fmt.Errorf("memoryruntime: chat session storage is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionIdentity{}, fmt.Errorf("memoryruntime: session id is required")
	}
	storage := s.Manager.GetStorage()
	var session *chat.Session
	var err error
	if reader, ok := storage.(chat.SessionStorageMetadataReader); ok {
		session, err = reader.LoadMetadata(ctx, sessionID)
	} else {
		session, err = storage.Load(ctx, sessionID)
	}
	if err != nil {
		return SessionIdentity{}, err
	}
	if session == nil || session.ID != sessionID {
		return SessionIdentity{}, fmt.Errorf("memoryruntime: session identity is unavailable")
	}
	if userID := strings.TrimSpace(s.UserID); userID != "" && session.UserID != userID {
		return SessionIdentity{}, fmt.Errorf("memoryruntime: session user mismatch")
	}
	return SessionIdentity{
		Session:         s.toSessionRef(session),
		ParentSessionID: agentcontrol.ContextString(session, agentcontrol.SessionContextParentSessionID),
		AgentType:       agentcontrol.ContextString(session, agentcontrol.SessionContextAgentType),
	}, nil
}

func (r *Runtime) digestSessionIdentity(ctx context.Context, sessionID string) (SessionIdentity, error) {
	if source, ok := r.Source.(SessionIdentitySource); ok {
		return source.ReadSessionIdentity(ctx, sessionID)
	}
	session, err := r.findSession(ctx, sessionID)
	return SessionIdentity{Session: session}, err
}
