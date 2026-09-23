package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// localExecutionSupervisorModeEnv overrides the CLI watchdog mode. The plan
// (§P0-4 风险) recommends observe for local sessions so long-running children
// keep executing while the parent only receives deadline/stall notices;
// {"enforce"} opts into the API-equivalent interrupt semantics.
const localExecutionSupervisorModeEnv = "AICLI_EXECUTION_SUPERVISOR_MODE"

// getLocalExecutionSupervisor lazily builds the CLI child-run watchdog
// (plan §P0-4): durable run records for spawned children, deadline / progress /
// approval scans, and terminal completion dispatch into the parent mailbox.
// The loop is bound to the host lifecycle so Close() stops it. It returns nil
// when the durable control plane is not configured, in which case local
// spawns keep working without run supervision (previous behavior).
func (h *localChatRuntimeHost) getLocalExecutionSupervisor() *supervision.ExecutionSupervisor {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return nil
	}
	h.executionSupervisorOnce.Do(func() {
		runStore, ok := h.Supervision.Store.(supervision.ExecutionRunStore)
		if !ok || runStore == nil {
			return
		}
		supervisor := &supervision.ExecutionSupervisor{
			Store:     runStore,
			StoreFull: h.Supervision.Store,
			Wakes:     h.Supervision.Wakes,
			Config:    localExecutionSupervisorConfig(h.supervisionConfig),
		}
		if h.ActorRegistry != nil {
			supervisor.Interrupter = toolbroker.AgentSessionRunInterrupter{Controller: h.ActorRegistry}
		}
		if h.EventStore != nil {
			mailboxStore := runtimechat.SessionEventMailboxStore{Events: h.EventStore}
			supervisor.Dispatcher = toolbroker.CompletionDispatchFunc(func(ctx context.Context, entry supervision.CompletionOutboxEntry) (int64, error) {
				payload := map[string]interface{}{}
				if strings.TrimSpace(entry.PayloadJSON) != "" {
					_ = json.Unmarshal([]byte(entry.PayloadJSON), &payload)
				}
				payload["status"] = entry.Status
				payload["run_id"] = entry.RunID
				message := toolbroker.BuildSubagentCompletionMailboxMessage(
					entry.ParentSessionID, entry.SessionID, "", "", "completion_outbox", payload)
				_, seq, err := mailboxStore.AppendAgentControlMailbox(ctx, entry.ParentSessionID, message)
				return seq, err
			})
		}
		h.executionSupervisorMu.Lock()
		h.executionSupervisor = supervisor
		h.executionSupervisorMu.Unlock()
		if h.lifecycleCtx == nil || h.lifecycleCtx.Err() != nil {
			// The host is closing or was never fully initialized: keep the
			// supervisor usable for synchronous scans, but never start a
			// background loop that Close() would no longer wait for.
			return
		}
		ctx, cancel := context.WithCancel(h.lifecycleCtx)
		h.executionSupervisorCtx = ctx
		h.executionSupervisorStop = cancel
		h.asyncWG.Add(1)
		go func() {
			defer h.asyncWG.Done()
			supervisor.RunLoop(ctx)
		}()
	})
	return h.executionSupervisor
}

// peekLocalExecutionSupervisor reports the watchdog only when it was already
// built. The /debug surface uses it so that rendering state never starts a
// background scan loop as a side effect; callers that need a working watchdog
// keep using getLocalExecutionSupervisor.
//
// newLocalExecutionSupervisor builds the same durable wiring the watchdog uses
// (store + lifecycle projection + wakes + config) **without** installing it on
// the host and without starting a scan loop. The startup recovery pass uses it
// for the one-shot restart reconciliation (C4-3 / AC-P3-3b), so a restarted
// host can decide "恢复 or orphaned" before the first scan interval elapses.
func (h *localChatRuntimeHost) newLocalExecutionSupervisor() *supervision.ExecutionSupervisor {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return nil
	}
	runStore, ok := h.Supervision.Store.(supervision.ExecutionRunStore)
	if !ok || runStore == nil {
		return nil
	}
	return &supervision.ExecutionSupervisor{
		Store:     runStore,
		StoreFull: h.Supervision.Store,
		Wakes:     h.Supervision.Wakes,
		Config:    localExecutionSupervisorConfig(h.supervisionConfig),
	}
}

func (h *localChatRuntimeHost) peekLocalExecutionSupervisor() *supervision.ExecutionSupervisor {
	if h == nil {
		return nil
	}
	h.executionSupervisorMu.Lock()
	defer h.executionSupervisorMu.Unlock()
	return h.executionSupervisor
}

// localExecutionSupervisorAvailable reports whether this host has the durable
// control plane the watchdog needs. It is the "could be wired" half of the
// /debug snapshot, independent from "already built".
func (h *localChatRuntimeHost) localExecutionSupervisorAvailable() bool {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return false
	}
	runStore, ok := h.Supervision.Store.(supervision.ExecutionRunStore)
	return ok && runStore != nil
}

// localExecutionSupervisorConfig maps the durable control-plane knobs onto the
// watchdog config. The mode defaults to observe; scan cadence and the
// execution/progress deadlines follow the supervision config so CLI and API
// share the same thresholds.
func localExecutionSupervisorConfig(cfg supervision.Config) supervision.ExecutionSupervisorConfig {
	result := supervision.DefaultExecutionSupervisorConfig()
	result.Enabled = true
	result.Mode = localExecutionSupervisorMode()
	cfg = cfg.WithDefaults()
	if cfg.ExecutionDeadline > 0 {
		result.DefaultExecutionTimeout = cfg.ExecutionDeadline
	}
	if cfg.HeartbeatTimeout > 0 {
		result.DefaultProgressTimeout = cfg.HeartbeatTimeout
	}
	return result
}

func localExecutionSupervisorMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(localExecutionSupervisorModeEnv))) {
	case "enforce":
		return "enforce"
	case "observe":
		return "observe"
	default:
		return "observe"
	}
}
