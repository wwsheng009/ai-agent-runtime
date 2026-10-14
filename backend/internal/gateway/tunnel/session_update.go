package tunnel

import (
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// applySessionUpdate mutates the node's session list from a SESSION_UPDATE
// frame. reconcile replaces the list; upsert/remove adjust a single entry.
func (s *Server) applySessionUpdate(nodeID string, update wire.SessionUpdate) {
	node, ok := s.registry.Get(nodeID)
	if !ok {
		return
	}
	now := nowUTC()
	prior := sessionsByID(node.Sessions)
	var changedIDs []string
	var transitions []sessionTransition
	removedAny := false
	switch update.Op {
	case wire.SessionOpUpsert:
		if update.Session != nil {
			merged := stampIncomingTransitions(*update.Session, prior[update.Session.SessionID], now)
			node.Sessions = upsertSession(node.Sessions, merged)
			stampSessionSynced(node, merged.SessionID, now)
			changedIDs = append(changedIDs, merged.SessionID)
			if tr, ok := transitionFor(merged, prior[merged.SessionID]); ok {
				transitions = append(transitions, tr)
			}
		}
	case wire.SessionOpRemove:
		id := update.SessionID
		if id == "" && update.Session != nil {
			id = update.Session.SessionID
		}
		node.Sessions = removeSession(node.Sessions, id)
		removedAny = true
	default: // reconcile / empty
		mergedList := make([]wire.SessionSummary, 0, len(update.Sessions))
		for _, incoming := range update.Sessions {
			merged := stampIncomingTransitions(incoming, prior[incoming.SessionID], now)
			mergedList = append(mergedList, merged)
			if tr, ok := transitionFor(merged, prior[merged.SessionID]); ok {
				transitions = append(transitions, tr)
			}
		}
		node.Sessions = mergedList
		stampAllSessionsSynced(node, now)
		for _, sess := range node.Sessions {
			if id := strings.TrimSpace(sess.SessionID); id != "" {
				changedIDs = append(changedIDs, id)
			}
		}
		nextByID := sessionsByID(node.Sessions)
		for id := range prior {
			if _, still := nextByID[id]; !still {
				removedAny = true
			}
		}
	}
	s.registry.Upsert(node)
	s.emitPresence(nodeID, node.Sessions)
	// State/attention transitions are published as lightweight console events
	// (the strip) independently of the row-patch broadcast below.
	s.emitTransitions(nodeID, transitions)
	// Delta path (P1): hosts that wired OnSessionChanged receive single-session
	// events so the console patches one row instead of re-rendering the whole
	// snapshot. Structural changes (removals) and hosts without the callback
	// keep the full broadcast; the periodic meta tick reconciles drift.
	if s.cfg.OnSessionChanged == nil {
		s.notifyRegistryChangedAndPersist()
		return
	}
	if s.persist != nil {
		s.persist.Schedule()
	}
	if removedAny {
		s.notifyRegistryChanged()
	} else {
		for _, id := range changedIDs {
			s.cfg.OnSessionChanged(nodeID, id)
		}
	}
	if s.cfg.OnRegistryTouch != nil {
		s.cfg.OnRegistryTouch()
	}
}

// sessionTransition is one gateway-observed State/Attention change.
type sessionTransition struct {
	SessionID string
	State     string
	Attention string
}

// sessionsByID builds the id-keyed snapshot of a session list (reconcile /
// upsert diffing for transition stamping).
func sessionsByID(sessions []wire.SessionSummary) map[string]wire.SessionSummary {
	out := make(map[string]wire.SessionSummary, len(sessions))
	for _, s := range sessions {
		if id := strings.TrimSpace(s.SessionID); id != "" {
			out[id] = s
		}
	}
	return out
}

// stampIncomingTransitions preserves or refreshes the gateway-stamped
// transition timestamps on an incoming summary: unchanged fields keep their
// previous timestamp, changed fields (and brand-new sessions) get now.
func stampIncomingTransitions(in wire.SessionSummary, prev wire.SessionSummary, now time.Time) wire.SessionSummary {
	out := in
	if prev.SessionID == "" {
		out.StateChangedAt = now
		out.AttentionChangedAt = now
		return out
	}
	if in.State != prev.State {
		out.StateChangedAt = now
	} else {
		out.StateChangedAt = prev.StateChangedAt
	}
	if in.Attention != prev.Attention {
		out.AttentionChangedAt = now
	} else {
		out.AttentionChangedAt = prev.AttentionChangedAt
	}
	return out
}

// transitionFor reports whether next differs from prev and returns the
// transition payload for the console event strip.
func transitionFor(next, prev wire.SessionSummary) (sessionTransition, bool) {
	tr := sessionTransition{SessionID: next.SessionID, State: next.State, Attention: next.Attention}
	if prev.SessionID == "" {
		if tr.State == "" && tr.Attention == "" {
			return sessionTransition{}, false
		}
		return tr, true
	}
	if next.State != prev.State || next.Attention != prev.Attention {
		return tr, true
	}
	return sessionTransition{}, false
}

// emitTransitions fans gateway-observed transitions out to the host.
func (s *Server) emitTransitions(nodeID string, transitions []sessionTransition) {
	if s.cfg.OnSessionTransition == nil {
		return
	}
	for _, tr := range transitions {
		if tr.SessionID == "" {
			continue
		}
		s.cfg.OnSessionTransition(nodeID, tr.SessionID, tr.State, tr.Attention)
	}
}

// emitPresence renews host-source presence for sessions whose host reports a
// local foreground viewer (docs/bots/10-presence-signals-design.md §3.2).
// Viewing=false 不显式清除：host 源由 TTL（心跳停止）自然失效。
func (s *Server) emitPresence(nodeID string, sessions []wire.SessionSummary) {
	if s.cfg.OnSessionPresence == nil {
		return
	}
	for _, sess := range sessions {
		if !sess.Viewing {
			continue
		}
		if id := strings.TrimSpace(sess.SessionID); id != "" {
			s.cfg.OnSessionPresence(nodeID, id)
		}
	}
}

// watchActions diffs the desired live-tail watch set against what this
// connection was last told, returning the control actions and the new sent
// set. Actions are sorted for deterministic frames/tests.
func watchActions(desired, sent map[string]bool) ([]string, map[string]bool) {
	var actions []string
	next := make(map[string]bool, len(desired))
	for id := range desired {
		next[id] = true
		if !sent[id] {
			actions = append(actions, "watch:"+id)
		}
	}
	for id := range sent {
		if !desired[id] {
			actions = append(actions, "unwatch:"+id)
		}
	}
	sort.Strings(actions)
	return actions, next
}

// stampSessionSynced marks one session as accepted by the gateway at now.
// SyncedAt is gateway-stamped metadata: agents never set it (any inbound
// value is overwritten so it is always server time).
func stampSessionSynced(node *Node, sessionID string, now time.Time) {
	if node == nil || sessionID == "" {
		return
	}
	for i := range node.Sessions {
		if node.Sessions[i].SessionID == sessionID {
			node.Sessions[i].SyncedAt = now
		}
	}
}

// stampAllSessionsSynced stamps every session on the node (used for
// reconcile frames and the initial REGISTER payload).
func stampAllSessionsSynced(node *Node, now time.Time) {
	if node == nil {
		return
	}
	for i := range node.Sessions {
		node.Sessions[i].SyncedAt = now
	}
}

// stampDigestSessions marks every session covered by a heartbeat digest as
// accepted at now. Stamping is intentionally invisible to the changed-flag:
// a heartbeat must not broadcast or persist just because time passed.
func stampDigestSessions(node *Node, digests []wire.SessionDigest, now time.Time) {
	for _, d := range digests {
		stampSessionSynced(node, strings.TrimSpace(d.SessionID), now)
	}
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

// applyHeartbeatDigests merges the compact per-session state carried by a
// HEARTBEAT frame into the node's session list. It reports whether a visible
// field actually changed (so the caller can skip the broadcast/persist for the
// common unchanged heartbeat) and returns the observed State/Attention
// transitions for the console event strip.
func applyHeartbeatDigests(node *Node, digests []wire.SessionDigest, now time.Time) (bool, []sessionTransition) {
	if node == nil || len(digests) == 0 {
		return false, nil
	}
	changed := false
	var transitions []sessionTransition
	for _, d := range digests {
		id := strings.TrimSpace(d.SessionID)
		if id == "" {
			continue
		}
		for i := range node.Sessions {
			s := &node.Sessions[i]
			if s.SessionID != id {
				continue
			}
			stateChanged, attentionChanged := false, false
			if d.State != "" && s.State != d.State {
				s.State = d.State
				changed = true
				stateChanged = true
			}
			if s.Attention != d.Attention {
				s.Attention = d.Attention
				changed = true
				attentionChanged = true
			}
			if stateChanged {
				s.StateChangedAt = now
			}
			if attentionChanged {
				s.AttentionChangedAt = now
			}
			if stateChanged || attentionChanged {
				transitions = append(transitions, sessionTransition{
					SessionID: s.SessionID, State: s.State, Attention: s.Attention,
				})
			}
			// StatusText/StatusTail are owned by the ordered event stream:
			// a delayed digest must not overwrite fresher activity text with
			// a stale snapshot (callers stamp/merge digests out of order).
			if !d.UpdatedAt.IsZero() && s.UpdatedAt.Before(d.UpdatedAt) {
				s.UpdatedAt = d.UpdatedAt
				changed = true
			}
			// Viewing 是宿主机在场位（docs/bots/10 §3.1）：心跳高频刷新、直接
			// 跟随最新摘要；不参与 changed（在场翻转不触发广播/持久化），乱序
			// 造成的短暂翻转由 host TTL 兜底。
			if s.Viewing != d.Viewing {
				s.Viewing = d.Viewing
			}
		}
	}
	return changed, transitions
}
