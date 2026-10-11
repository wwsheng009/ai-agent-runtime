package commands

import "sync"

// gatewaySessionState is the process-local projection of one session's runtime
// state reported to the gateway (docs/gateway/08-notifications.md §3).
type gatewaySessionState struct {
	Attention string
	Busy      bool
}

// gatewayAttentionStore is a tiny concurrency-safe sessionID→state map. It is
// process-local and never persisted: the gateway registry is the durable
// projection (rebuilt from the next SESSION_UPDATE after a reconnect).
type gatewayAttentionStoreImpl struct {
	mu     sync.Mutex
	values map[string]gatewaySessionState
}

func newGatewayAttentionStore() *gatewayAttentionStoreImpl {
	return &gatewayAttentionStoreImpl{values: make(map[string]gatewaySessionState)}
}

func (s *gatewayAttentionStoreImpl) setAttention(sessionID, attention string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.values[sessionID]
	st.Attention = attention
	s.values[sessionID] = st
}

func (s *gatewayAttentionStoreImpl) setBusy(sessionID string, busy bool) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.values[sessionID]
	st.Busy = busy
	s.values[sessionID] = st
}

func (s *gatewayAttentionStoreImpl) get(sessionID string) gatewaySessionState {
	if sessionID == "" {
		return gatewaySessionState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[sessionID]
}
