package runtimehost

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// ResumeLineageSessionStore is the minimal storage surface needed to stamp a
// resumed session's prompt-cache lineage.
type ResumeLineageSessionStore interface {
	Get(ctx context.Context, sessionID string) (*chat.Session, error)
	Update(ctx context.Context, session *chat.Session) error
}

// StampResumeCacheLineage marks a session that is being resumed from durable
// history. Empty sessions are skipped because they have no prefix to resume;
// batch child sessions that do not live in SessionStore also fall through as a
// no-op. The tag never changes behavior beyond cache observability, and a write
// failure is reported through warn without failing the resume.
func StampResumeCacheLineage(ctx context.Context, store ResumeLineageSessionStore, sessionID string, warn func(error)) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := store.Get(ctx, sessionID)
	if err != nil || session == nil || len(session.GetMessages()) == 0 {
		return
	}
	session.SetContext(agent.CacheLineageModeSessionContextKey, agent.CacheLineageModeResume)
	if err := store.Update(ctx, session); err != nil && warn != nil {
		warn(err)
	}
}
