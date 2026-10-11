package tunnel

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// notifyDedupCap bounds the NOTIFY dedup set; when exceeded it is reset. A reset
// can re-admit an old dedup key, which is harmless: the notifier still only
// fires on an attention change.
const notifyDedupCap = 4096

// applyNotify folds an instantaneous NOTIFY event into the registry. It updates
// the session's attention (without clobbering other fields) and fires the
// registry-changed callback so the console and the push notifier observe it.
func (s *Server) applyNotify(nodeID string, n wire.Notify) {
	sessionID := strings.TrimSpace(n.SessionID)
	if sessionID == "" {
		return
	}
	n.SessionID = sessionID
	if key := strings.TrimSpace(n.DedupKey); key != "" && s.markNotifySeen(key) {
		return // duplicate event
	}
	node, ok := s.registry.Get(nodeID)
	if !ok {
		return
	}
	node.Sessions = applyNotifyToSessions(node.Sessions, n)
	s.registry.Upsert(node)
	s.notifyRegistryChangedAndPersist()
}

// markNotifySeen records a dedup key, reporting whether it was already present.
func (s *Server) markNotifySeen(key string) bool {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	if s.notifySeen == nil {
		s.notifySeen = make(map[string]struct{})
	}
	if _, ok := s.notifySeen[key]; ok {
		return true
	}
	if len(s.notifySeen) >= notifyDedupCap {
		s.notifySeen = make(map[string]struct{})
	}
	s.notifySeen[key] = struct{}{}
	return false
}

// applyNotifyToSessions updates the target session in place, preserving fields
// the agent reported through SESSION_UPDATE; unknown sessions are appended.
func applyNotifyToSessions(sessions []wire.SessionSummary, n wire.Notify) []wire.SessionSummary {
	attention := attentionForNotifyKind(n.Kind)
	title := strings.TrimSpace(n.Title)
	for i := range sessions {
		if sessions[i].SessionID != n.SessionID {
			continue
		}
		if attention != "" {
			sessions[i].Attention = attention
		}
		if title != "" {
			sessions[i].Title = title
		}
		if !n.TS.IsZero() {
			sessions[i].UpdatedAt = n.TS
		}
		return sessions
	}
	return append(sessions, wire.SessionSummary{
		SessionID: n.SessionID,
		Title:     title,
		Attention: attention,
		UpdatedAt: n.TS,
	})
}

// attentionForNotifyKind maps a NOTIFY kind to the persistent attention value
// carried by SESSION_UPDATE (docs/gateway/08-notifications.md §3).
func attentionForNotifyKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case wire.NotifyKindTurnCompleted:
		return "completed"
	case wire.NotifyKindApproval:
		return "approval"
	case wire.NotifyKindQuestion:
		return "question"
	case wire.NotifyKindError:
		return "error"
	case wire.NotifyKindWaiting:
		return "waiting"
	default:
		return ""
	}
}
