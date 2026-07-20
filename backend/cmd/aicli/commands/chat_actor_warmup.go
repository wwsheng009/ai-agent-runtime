package commands

import (
	"context"
	"fmt"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

type chatActorWarmup struct {
	sessionID string
	done      chan struct{}
	actor     *runtimechat.SessionActor
	err       error
}

func startChatActorWarmup(session *ChatSession) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil || session.RuntimeSession == nil {
		setChatActorWarmup(session, nil)
		return
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		setChatActorWarmup(session, nil)
		return
	}

	warmup := &chatActorWarmup{
		sessionID: sessionID,
		done:      make(chan struct{}),
	}
	setChatActorWarmup(session, warmup)

	hub := session.LocalRuntimeHost.SessionHub
	go func() {
		actor, err := hub.GetOrCreate(sessionID)
		warmup.complete(actor, err)
		if err != nil {
			logpkg.Warnf("AICLI actor warmup failed for session %s: %v", sessionID, err)
		}
	}()
}

func setChatActorWarmup(session *ChatSession, warmup *chatActorWarmup) {
	if session == nil {
		return
	}
	session.actorWarmupMu.Lock()
	session.actorWarmup = warmup
	session.actorWarmupMu.Unlock()
}

func currentChatActorWarmup(session *ChatSession, sessionID string) *chatActorWarmup {
	if session == nil {
		return nil
	}
	session.actorWarmupMu.Lock()
	defer session.actorWarmupMu.Unlock()
	if session.actorWarmup == nil || session.actorWarmup.sessionID != strings.TrimSpace(sessionID) {
		return nil
	}
	return session.actorWarmup
}

func clearChatActorWarmup(session *ChatSession, completed *chatActorWarmup) {
	if session == nil || completed == nil {
		return
	}
	session.actorWarmupMu.Lock()
	if session.actorWarmup == completed {
		session.actorWarmup = nil
	}
	session.actorWarmupMu.Unlock()
}

func (w *chatActorWarmup) complete(actor *runtimechat.SessionActor, err error) {
	if w == nil {
		return
	}
	w.actor = actor
	w.err = err
	close(w.done)
}

func (w *chatActorWarmup) wait(ctx context.Context) (*runtimechat.SessionActor, error) {
	if w == nil {
		return nil, fmt.Errorf("actor warmup is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
		return w.actor, w.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func chatActorForSession(ctx context.Context, session *ChatSession) (*runtimechat.SessionActor, error) {
	if session == nil {
		return nil, fmt.Errorf("chat session is nil")
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return nil, fmt.Errorf("local runtime host is not configured")
	}
	if session.RuntimeSession == nil {
		return nil, fmt.Errorf("runtime session is not configured")
	}

	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return nil, fmt.Errorf("runtime session id is required")
	}
	if warmup := currentChatActorWarmup(session, sessionID); warmup != nil {
		// Warmup only gates the initial factory call. The hub remains the source
		// of truth because it may evict and stop the warmed actor after an idle
		// period while the interactive ChatSession continues to live.
		_, warmupErr := warmup.wait(ctx)
		if warmupErr == nil || ctx == nil || ctx.Err() == nil {
			clearChatActorWarmup(session, warmup)
		}
		if warmupErr != nil && ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	return session.LocalRuntimeHost.SessionHub.GetOrCreate(sessionID)
}
