// Package subagentquota enforces the live-subagent ceiling (agents.maxThreads)
// across BOTH spawn paths.
//
// The single-spawn path (subagent_spawn) already reserves a slot in the durable
// agent registry at spawn time and rejects an over-limit child with
// agentcontrol.ThreadLimitMessage. The batch path (subagent_spawn_batch) only had a
// concurrency limiter: a slot was held for the duration of one child execution
// and released when it finished, so a session could accumulate an unbounded
// number of idle/terminal child sessions. This package closes that gap by
// counting the live (non-terminal) footprint of both sources and rejecting a
// new batch before any batch/task row is written.
//
// The count is the union of:
//   - registry children that still hold quota (agentcontrol.QuotaChildren), and
//   - non-terminal task rows in the durable batch ledger (subagentbatch.LiveTaskCounter).
//
// Release is implicit: a child that reaches a terminal status (or a registry row
// that becomes closed/stale) stops counting, so no explicit release is needed.
package subagentquota

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// RegistryCounter is the read surface the gate needs from the durable agent
// registry. *agentcontrol.SQLiteGlobalAgentRegistryStore satisfies it.
type RegistryCounter interface {
	ListAgentControlAgents(ctx context.Context, filter agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error)
}

// Gate enforces the live-subagent ceiling across both spawn paths.
//
// It is safe for concurrent use: admission is serialized process-wide so two
// batches dispatched at the same instant cannot both observe "room for N" and
// overshoot. The count spans two tables and is therefore not a single
// transaction; the mutex collapses the local race window, leaving only the
// cross-process case unguarded (documented in the design note).
type Gate struct {
	registry   RegistryCounter
	batches    subagentbatch.LiveTaskCounter
	maxThreads int

	mu sync.Mutex
}

// New builds a gate from the host's stores and the configured agents.maxThreads.
//
// rawMaxThreads is resolved through agentcontrol.ResolveMaxThreads: a negative
// value is the explicit "unlimited" opt-in and yields a nil gate (nothing to
// enforce); 0 falls back to defaultMax (never unlimited); a positive value is
// the ceiling. A nil gate is a valid, no-op gate — callers may inject it
// unconditionally.
//
// store is the host's BatchStore; the live-task counter is the optional
// LiveTaskCounter extension, resolved by type assertion. A store that does not
// implement it yields a nil gate (the batch footprint cannot be counted, and
// the registry path already gates itself at subagent_spawn time).
func New(registry RegistryCounter, store subagentbatch.BatchStore, rawMaxThreads, defaultMax int) *Gate {
	counter, _ := store.(subagentbatch.LiveTaskCounter)
	return newWithCounter(registry, counter, rawMaxThreads, defaultMax)
}

// newWithCounter is the injectable constructor used by tests.
func newWithCounter(registry RegistryCounter, counter subagentbatch.LiveTaskCounter, rawMaxThreads, defaultMax int) *Gate {
	limit, unlimited := agentcontrol.ResolveMaxThreads(rawMaxThreads, defaultMax)
	if unlimited || counter == nil || limit <= 0 {
		return nil
	}
	return &Gate{registry: registry, batches: counter, maxThreads: limit}
}

// MaxThreads reports the effective ceiling (0 for a nil/unlimited gate).
func (g *Gate) MaxThreads() int {
	if g == nil {
		return 0
	}
	return g.maxThreads
}

// AdmitSubagentBatch checks whether a batch of `requested` child tasks may be
// admitted for the scope. It returns nil when the batch fits under the live
// ceiling, or an actionable error (agentcontrol.ThreadLimitMessage, naming the
// limit, the current occupants and the recovery next_action) when it does not.
//
// A nil gate, a non-positive request, or an unscoped call admits (returns nil):
// the gate must never block work it cannot reason about.
func (g *Gate) AdmitSubagentBatch(ctx context.Context, parentSessionID, rootScopeID string, requested int) error {
	if g == nil || requested <= 0 {
		return nil
	}
	scope := strings.TrimSpace(rootScopeID)
	if scope == "" {
		scope = strings.TrimSpace(parentSessionID)
	}
	if scope == "" {
		return nil
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	live := 0
	if g.registry != nil {
		records, err := g.registry.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: scope})
		if err != nil {
			return fmt.Errorf("subagent live-slot gate: count registry children for scope %q: %w", scope, err)
		}
		live += len(agentcontrol.QuotaChildren(records))
	}
	if g.batches != nil {
		count, err := g.batches.CountLiveTasks(ctx, subagentbatch.LiveTaskFilter{RootScopeID: scope})
		if err != nil {
			return fmt.Errorf("subagent live-slot gate: count live batch tasks for scope %q: %w", scope, err)
		}
		live += count
	}

	if live+requested <= g.maxThreads {
		return nil
	}
	return fmt.Errorf("%s", agentcontrol.ThreadLimitMessage(
		g.maxThreads, live, nil,
		fmt.Sprintf("batch_requested=%d", requested),
	))
}
