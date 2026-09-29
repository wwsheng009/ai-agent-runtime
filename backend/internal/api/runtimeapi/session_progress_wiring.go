package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
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

// progressRecorderForSession returns the run-level progress sink for one
// session (P0-1): the returned callback stamps LastProgressAt/progress_seq on
// the session's active ExecutionRun. It returns nil when durable supervision is
// not configured, in which case the ReAct loop keeps its previous behavior
// (nil hook = no-op). Spawned child sessions carry exactly one non-terminal
// agent run; sessions without a run resolve to "" and the tick is dropped.
func (h *Handler) progressRecorderForSession(sessionID string) func(kind string) {
	if h == nil || strings.TrimSpace(sessionID) == "" || h.getExecutionSupervisor() == nil {
		return nil
	}
	return func(kind string) {
		h.recordSessionRunProgress(sessionID, kind)
	}
}

// recordSessionRunProgress resolves the session's active execution run and
// records one progress tick. Best-effort by contract: the progress channel
// must never fail the run it observes, so errors are swallowed and the write
// is bounded by a short timeout.
func (h *Handler) recordSessionRunProgress(sessionID, kind string) {
	supervisor := h.getExecutionSupervisor()
	if supervisor == nil || supervisor.Store == nil {
		return
	}
	runID := h.sessionRunIDForProgress(supervisor.Store, sessionID)
	if runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = supervisor.RecordProgress(ctx, supervision.RunProgressEvent{
		RunID: runID,
		Kind:  strings.TrimSpace(kind),
	})
}

// budgetRecorderForSession returns the run-level budget watermark sink for one
// session (建议稿 §4.2 运行中水位): the returned callback stamps the live turn
// budget watermark (level/line/ratio) on the session's active ExecutionRun, so
// the supervision snapshot can show "tokens 84%" while the child still runs.
// It returns nil when durable supervision is not configured (nil hook = the
// ReAct loop keeps its previous behavior).
func (h *Handler) budgetRecorderForSession(sessionID string) func(agent.TurnBudgetState) {
	if h == nil || strings.TrimSpace(sessionID) == "" || h.getExecutionSupervisor() == nil {
		return nil
	}
	return func(state agent.TurnBudgetState) {
		h.recordSessionRunBudget(sessionID, state)
	}
}

// recordSessionRunBudget writes one budget watermark tick. Best-effort by the
// same contract as recordSessionRunProgress: swallowed errors, bounded timeout,
// and an empty watermark level is never sent (the store treats it as "no
// reading" and would keep the previous one anyway).
func (h *Handler) recordSessionRunBudget(sessionID string, state agent.TurnBudgetState) {
	supervisor := h.getExecutionSupervisor()
	if supervisor == nil || supervisor.Store == nil {
		return
	}
	if strings.TrimSpace(state.Level) == "" && strings.TrimSpace(state.Line) == "" {
		return
	}
	runID := h.sessionRunIDForProgress(supervisor.Store, sessionID)
	if runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = supervisor.RecordProgress(ctx, supervision.RunProgressEvent{
		RunID:       runID,
		Kind:        "turn_budget",
		BudgetLevel: state.Level,
		BudgetLine:  state.Line,
		BudgetRatio: state.Ratio,
	})
}

// sessionRunIDForProgress resolves (and briefly caches) the newest execution
// run registered for a session.
func (h *Handler) sessionRunIDForProgress(store supervision.ExecutionRunStore, sessionID string) string {
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
