package commands

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// progressRunCacheTTL bounds how long a resolved session→run mapping is
// trusted before the durable store is consulted again (P0-1). Terminal runs
// simply produce no write (RecordExecutionProgress ignores terminal rows), so
// a stale entry only costs one extra query per window.
const progressRunCacheTTL = 10 * time.Second

// progressRunCacheEntry is one cached session→run mapping.
type progressRunCacheEntry struct {
	runID     string
	expiresAt time.Time
}

// progressRecorderForSession returns the run-level progress sink for one child
// session (P0-1): the returned callback stamps LastProgressAt/progress_seq on
// the session's active ExecutionRun. It returns nil when durable supervision is
// not wired, in which case the ReAct loop keeps its previous behavior
// (nil hook = no-op).
func (h *localChatRuntimeHost) progressRecorderForSession(sessionID string) func(kind string) {
	if h == nil || strings.TrimSpace(sessionID) == "" || !h.localExecutionSupervisorAvailable() {
		return nil
	}
	return func(kind string) {
		h.recordLocalRunProgress(sessionID, kind)
	}
}

// recordLocalRunProgress resolves the session's active execution run and
// records one progress tick. Best-effort by contract: the progress channel
// must never fail the run it observes, so errors are swallowed and the write
// is bounded by a short timeout.
func (h *localChatRuntimeHost) recordLocalRunProgress(sessionID, kind string) {
	store, ok := h.localExecutionRunStore()
	if !ok {
		return
	}
	runID := h.progressRunIDForSession(store, sessionID)
	if runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = store.RecordExecutionProgress(ctx, supervision.RunProgressEvent{
		RunID: runID,
		Kind:  strings.TrimSpace(kind),
	}, time.Now().UTC())
}

// localExecutionRunStore returns the durable run store when the control plane
// exposes it.
func (h *localChatRuntimeHost) localExecutionRunStore() (supervision.ExecutionRunStore, bool) {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return nil, false
	}
	store, ok := h.Supervision.Store.(supervision.ExecutionRunStore)
	if !ok || store == nil {
		return nil, false
	}
	return store, true
}

// progressRunIDForSession resolves (and briefly caches) the newest execution
// run registered for a child session. Spawned children have one non-terminal
// agent run; sessions without a run resolve to "" and the tick is dropped.
func (h *localChatRuntimeHost) progressRunIDForSession(store supervision.ExecutionRunStore, sessionID string) string {
	now := time.Now()
	h.progressRunMu.Lock()
	if entry, ok := h.progressRunIDs[sessionID]; ok && now.Before(entry.expiresAt) {
		h.progressRunMu.Unlock()
		return entry.runID
	}
	h.progressRunMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runs, err := store.ListExecutionRunsBySession(ctx, sessionID, 8)
	if err != nil || len(runs) == 0 {
		return ""
	}
	newest := runs[0]
	for _, run := range runs[1:] {
		if run.CreatedAt.After(newest.CreatedAt) {
			newest = run
		}
	}
	h.progressRunMu.Lock()
	if h.progressRunIDs == nil {
		h.progressRunIDs = make(map[string]progressRunCacheEntry)
	}
	h.progressRunIDs[sessionID] = progressRunCacheEntry{
		runID:     newest.RunID,
		expiresAt: now.Add(progressRunCacheTTL),
	}
	h.progressRunMu.Unlock()
	return newest.RunID
}
