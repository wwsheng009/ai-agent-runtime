package agent

import (
	"context"
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// Supervised-suspension durability gate (design §6.13, I9: 探测即降级).
//
// The runtime may only park a parent turn (return a batch handle and keep the
// obligations in a durable ledger) when the batch control plane actually
// survives a process restart. The probe is deliberately separate from the
// model-visible tool gate (shouldExposeSpawnSubagents): visibility is a policy
// question ("may this agent delegate at all?"), durability is a capability
// question ("can this host remember the parked turn?"). A host can answer the
// first with yes and the second with no.

// Stable degradation reasons. Hosts and tests assert on these strings, and the
// one-shot projection deduplicates on them, so they must not be reworded
// casually.
const (
	// SuspensionReasonNoCoordinator means no batch coordinator was injected
	// (the lazy in-memory default is intentionally not accepted here).
	SuspensionReasonNoCoordinator = "batch coordinator is not configured for this session"
	// SuspensionReasonNoStore means the coordinator has no store at all.
	SuspensionReasonNoStore = "batch control plane has no store"
	// SuspensionReasonNotDurable means the store is process-local (in-memory
	// SQLite): a parked turn written there would die with the process.
	SuspensionReasonNotDurable = "batch store is process-local and does not survive a restart"

	// SuspensionSeverityWarning is the severity every I9 degradation carries.
	// The design (§6.13) requires a single non-alarming warning rather than a
	// critical alert, because the dispatch still succeeds - since P3 / C4-1 it
	// only means the parked turn cannot be resumed after a restart (there is no
	// synchronous fallback left to fall back to). The literal matches
	// supervision.SeverityWarning; the agent package keeps it as a string so the
	// tool layer does not depend on the supervision plane.
	SuspensionSeverityWarning = "warning"
)

// SuspensionDegradation is the host-neutral payload of the one-shot I9
// degradation projection: the session asked for supervised suspension but the
// batch control plane cannot remember it, so a crash would lose the parked
// turn even though the asynchronous dispatch itself succeeded.
type SuspensionDegradation struct {
	// ParentSessionID is the session whose dispatch degraded.
	ParentSessionID string
	// RootScopeID is the supervision scope the host should project into
	// (normally the session id).
	RootScopeID string
	// Reason is one of the SuspensionReason* constants (or a store error).
	Reason string
	// Severity is SuspensionSeverityWarning for every degradation; hosts map it
	// straight onto supervision.SeverityWarning.
	Severity string
}

// SuspensionDegradationProjector persists the degradation projection in a host's
// supervision control plane. Like BatchLifecycleProjector it is host-neutral and
// best-effort: a projection failure must never change the dispatch decision or
// the runs already in flight, and the runtime degrades at most once per
// (session, reason) so a chatty model cannot flood the control plane.
type SuspensionDegradationProjector func(context.Context, SuspensionDegradation) error

// SetSuspensionDegradationProjector installs the host adapter used for the
// one-shot I9 degradation projection. A nil projector keeps the degradation
// silent (the runtime still refuses to park), which is the pre-#11 behavior for
// hosts that never wire the control plane.
func (a *Agent) SetSuspensionDegradationProjector(projector SuspensionDegradationProjector) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.suspensionDegradationProjector = projector
}

// SuspensionProbe reports whether this agent may park a turn for supervised
// suspension, together with the stable reason when it may not (I9). It is the
// single probe behind SupportsSuspension, the dispatch gate and the tool
// description, so the three can never disagree.
//
// The probe never triggers lazy coordinator creation: the lazy default store is
// process-local, so "no coordinator yet" is answered as a degradation instead
// of quietly standing up state that cannot survive a restart.
func (a *Agent) SuspensionProbe() (string, bool) {
	if a == nil {
		return SuspensionReasonNoCoordinator, false
	}
	a.mu.RLock()
	coordinator := a.batchCoordinator
	a.mu.RUnlock()
	if coordinator == nil {
		return SuspensionReasonNoCoordinator, false
	}
	store := coordinator.Store()
	if store == nil {
		return SuspensionReasonNoStore, false
	}
	if !store.IsDurable() {
		return SuspensionReasonNotDurable, false
	}
	return "", true
}

// SupportsSuspension reports whether a parked turn would survive a restart
// (§6.13 I9). False means no parked state may be written: the dispatch is still
// asynchronous (P3 / C4-1 deleted the blocking path) but the host must project
// the degradation warning instead of promising a resumable turn.
func (a *Agent) SupportsSuspension() bool {
	_, ok := a.SuspensionProbe()
	return ok
}

// reportSuspensionDegraded projects the I9 degradation exactly once per
// (session, reason). It is safe to call on every dispatch attempt: repeats are
// dropped after the first projection so the control plane is not spammed, and a
// missing projector simply means the host has no durable supervision plane.
func (a *Agent) reportSuspensionDegraded(ctx context.Context, sessionID, reason string) {
	if a == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	reason = strings.TrimSpace(reason)
	if sessionID == "" {
		return
	}
	if reason == "" {
		reason = SuspensionReasonNotDurable
	}

	key := sessionID + "\x00" + reason
	a.mu.Lock()
	if a.suspensionDegraded == nil {
		a.suspensionDegraded = make(map[string]bool)
	}
	if a.suspensionDegraded[key] {
		a.mu.Unlock()
		return
	}
	a.suspensionDegraded[key] = true
	projector := a.suspensionDegradationProjector
	a.mu.Unlock()

	payload := map[string]interface{}{
		"parent_session_id": sessionID,
		"root_scope_id":     sessionID,
		"reason":            reason,
		"severity":          "warning",
	}
	// 发射点只引用常量：事件名散落成裸字面量时，注册表与发射点之间没有机械
	// 约束，漏登记的症状只是"前端没反应"（见 internal/events/contract_test.go
	// 的扫描门禁）。
	a.emitRuntimeEvent(runtimeevents.EventSubagentSuspensionUnavailable, sessionID, SpawnSubagentsToolName, payload)

	if projector == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = projector(ctx, SuspensionDegradation{
		ParentSessionID: sessionID,
		RootScopeID:     sessionID,
		Reason:          reason,
		Severity:        SuspensionSeverityWarning,
	})
}
