package sessionctx

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

type chatSessionContextStore struct {
	storage chat.SessionStorage
}

// New returns a toolbroker session-context store backed by runtime chat sessions.
func New(storage chat.SessionStorage) toolbroker.SessionContextStore {
	if storage == nil {
		return nil
	}
	return &chatSessionContextStore{storage: storage}
}

func (s *chatSessionContextStore) LoadContextValue(ctx context.Context, sessionID, key string) (interface{}, error) {
	if s == nil || s.storage == nil {
		return nil, fmt.Errorf("session storage is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	key = strings.TrimSpace(key)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if key == "" {
		return nil, fmt.Errorf("context key is required")
	}
	session, err := s.storage.Load(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	value, _ := session.GetContext(key)
	return value, nil
}

func (s *chatSessionContextStore) SaveContextValue(ctx context.Context, sessionID, key string, value interface{}) error {
	if s == nil || s.storage == nil {
		return fmt.Errorf("session storage is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	key = strings.TrimSpace(key)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	if key == "" {
		return fmt.Errorf("context key is required")
	}
	session, err := s.storage.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	session.SetContext(key, value)
	return s.storage.Update(ctx, session)
}
