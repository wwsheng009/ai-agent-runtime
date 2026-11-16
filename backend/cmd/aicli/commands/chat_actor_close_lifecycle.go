package commands

import (
	"context"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// stopLocalAgentForTerminalState is the CLI twin of the API host's close
// cleanup: retire a parked interaction, stop the actor asynchronously, and
// disarm supervision resources before the durable session is marked closed.
func (r *localActorRegistry) stopLocalAgentForTerminalState(ctx context.Context, sessionID string, session *runtimechat.Session) {
	sessionID = strings.TrimSpace(sessionID)
	if r == nil || r.Host == nil || sessionID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	retiredLive := false
	if r.Host.SessionHub != nil {
		if actor, ok := r.Host.SessionHub.Get(sessionID); ok && actor != nil {
			if err := actor.RetirePendingInteraction(ctx, "agent_session_closed"); err != nil {
				logpkg.Warnf("local agent close: retire pending interaction %s: %v", sessionID, err)
			} else {
				retiredLive = true
			}
			r.Host.SessionHub.StopAsync(sessionID)
		}
	}
	if !retiredLive {
		r.retireLocalDurablePendingInteraction(ctx, sessionID)
	}
	disarmLocalChatSessionMonitorsForHost(r.Host, sessionID)
	resolveLocalChatSessionPendingWakesForHost(r.Host, sessionID)
}

func (r *localActorRegistry) retireLocalDurablePendingInteraction(ctx context.Context, sessionID string) {
	if r == nil || r.Host == nil || r.Host.RuntimeStore == nil {
		return
	}
	events, _, err := runtimechat.RetireDurablePendingInteraction(ctx, r.Host.RuntimeStore, sessionID, "agent_session_closed")
	if err != nil {
		logpkg.Warnf("local agent close: retire durable pending interaction %s: %v", sessionID, err)
		return
	}
	for _, event := range events {
		r.publishLocalTerminalPendingEvent(event)
	}
}

func (r *localActorRegistry) publishLocalTerminalPendingEvent(event runtimeevents.Event) {
	if r == nil || r.Host == nil {
		return
	}
	if r.Host.EventStore != nil {
		if seq, err := r.Host.EventStore.AppendEvent(context.Background(), event); err == nil {
			if event.Payload == nil {
				event.Payload = map[string]interface{}{}
			}
			event.Payload["seq"] = seq
		}
	}
	if r.Host.EventBus != nil {
		r.Host.EventBus.Publish(event)
	}
}
