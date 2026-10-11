package tunnel

import "github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"

// applySessionUpdate mutates the node's session list from a SESSION_UPDATE
// frame. reconcile replaces the list; upsert/remove adjust a single entry.
func (s *Server) applySessionUpdate(nodeID string, update wire.SessionUpdate) {
	node, ok := s.registry.Get(nodeID)
	if !ok {
		return
	}
	switch update.Op {
	case wire.SessionOpUpsert:
		if update.Session != nil {
			node.Sessions = upsertSession(node.Sessions, *update.Session)
		}
	case wire.SessionOpRemove:
		id := update.SessionID
		if id == "" && update.Session != nil {
			id = update.Session.SessionID
		}
		node.Sessions = removeSession(node.Sessions, id)
	default: // reconcile / empty
		node.Sessions = append([]wire.SessionSummary(nil), update.Sessions...)
	}
	s.registry.Upsert(node)
	s.notifyRegistryChangedAndPersist()
}

func upsertSession(sessions []wire.SessionSummary, session wire.SessionSummary) []wire.SessionSummary {
	if session.SessionID == "" {
		return sessions
	}
	out := make([]wire.SessionSummary, 0, len(sessions)+1)
	replaced := false
	for _, existing := range sessions {
		if existing.SessionID == session.SessionID {
			out = append(out, session)
			replaced = true
			continue
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, session)
	}
	return out
}

func removeSession(sessions []wire.SessionSummary, id string) []wire.SessionSummary {
	if id == "" {
		return sessions
	}
	out := make([]wire.SessionSummary, 0, len(sessions))
	for _, existing := range sessions {
		if existing.SessionID == id {
			continue
		}
		out = append(out, existing)
	}
	return out
}
