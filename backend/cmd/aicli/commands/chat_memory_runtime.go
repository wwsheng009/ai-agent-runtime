package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

func (h *localChatRuntimeHost) initializeMemoryRuntime(session *ChatSession) error {
	if h == nil || h.RuntimeConfig == nil || session == nil || session.SessionManager == nil {
		return nil
	}
	if h.Bootstrap == nil || h.Bootstrap.LLMRuntime() == nil {
		return fmt.Errorf("memoryruntime: LLM runtime is not configured")
	}
	workspace := resolveLocalWorkspacePath(h.RuntimeConfig, session)
	rt, release, err := acquireSharedMemoryRuntime(memoryassembly.BuildOptions{
		RuntimeConfig:  h.RuntimeConfig,
		Workspace:      workspace,
		SessionManager: session.SessionManager,
		LLMRuntime:     h.Bootstrap.LLMRuntime(),
		UserID:         session.SessionUserID,
		WorkerID:       fmt.Sprintf("aicli-%d", os.Getpid()),
	})
	if err != nil || rt == nil {
		return err
	}
	h.MemoryRuntime = rt
	h.memoryMaintenanceShared = true
	session.SessionManager.SetSessionPurgeCoordinatorV2(func(
		ctx context.Context, sessionID string, sessionGeneration, purgeGeneration int64,
	) error {
		_, err := rt.PurgeSessionWithGenerations(ctx, sessionID, sessionGeneration, purgeGeneration)
		return err
	})
	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 30*time.Second)
	if _, err := session.SessionManager.ResumePendingSessionPurges(recoveryCtx); err != nil {
		logpkg.Warnf("memory session purge recovery failed: %v", err)
	}
	cancelRecovery()
	h.cleanupFns = append(h.cleanupFns, release)
	h.bindMemoryDigestInvalidation()
	return nil
}

// localTurnDigestSink is the single wiring point for both actor assembly paths
// (legacy buildSessionActor and runtimehost.Deps). Keeping it here prevents the
// unified executor path from silently dropping the memory outbox.
func localTurnDigestSink(h *localChatRuntimeHost) runtimechat.TurnDigestSink {
	if h == nil || h.MemoryRuntime == nil {
		return nil
	}
	return h.MemoryRuntime
}

// bindMemoryDigestInvalidation prevents pending turn digests and existing
// session-derived memory records from being reused after compaction/backtrack
// changed the canonical transcript.
func (h *localChatRuntimeHost) bindMemoryDigestInvalidation() {
	if h == nil || h.EventBus == nil || h.MemoryRuntime == nil {
		return
	}
	handler := func(event runtimeevents.Event) {
		if strings.TrimSpace(event.SessionID) == "" {
			return
		}
		_, _ = h.MemoryRuntime.InvalidateFromRuntimeEvent(
			context.Background(), event.Type, event.SessionID, event.Payload)
	}
	for _, eventType := range []string{
		runtimechat.EventSessionCompactCompleted,
		runtimechat.EventBacktrackFinished,
	} {
		if unsubscribe := h.EventBus.SubscribeCancelable(eventType, handler); unsubscribe != nil {
			h.cleanupFns = append(h.cleanupFns, unsubscribe)
		}
	}
}

func (h *localChatRuntimeHost) startMemoryIdleSweep() func() {
	if h != nil && h.memoryMaintenanceShared {
		return func() {}
	}
	if h == nil || h.MemoryRuntime == nil || h.RuntimeConfig == nil || h.lifecycleCtx == nil {
		return func() {}
	}
	return memoryassembly.StartMaintenance(h.lifecycleCtx, memoryassembly.MaintenanceOptions{
		Runtime: h.MemoryRuntime,
		Config:  h.RuntimeConfig,
		Host:    localMemoryMaintenanceHost{host: h},
		OnError: func(stage string, err error) {
			logpkg.Warnf("memory maintenance failed: stage=%s: %v", stage, err)
		},
	})
}

type localMemoryMaintenanceHost struct {
	host *localChatRuntimeHost
}

func (h localMemoryMaintenanceHost) RunConsolidation(ctx context.Context) error {
	if h.host == nil || h.host.MemoryRuntime == nil {
		return nil
	}
	consolidator := memoryruntime.NewConsolidator(h.host.MemoryRuntime.Store, h.host.MemoryRuntime.Config)
	_, err := consolidator.Run(ctx)
	return err
}

func (h *localChatRuntimeHost) triggerMemorySessionEnd(sessionID string) {
	if h == nil || h.MemoryRuntime == nil || h.RuntimeConfig == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	generation := h.RuntimeConfig.Memory.Generation
	if !generation.Enabled {
		return
	}
	trigger := strings.ToLower(strings.TrimSpace(generation.Trigger))
	if trigger != "session_end" && trigger != "both" {
		return
	}
	h.asyncWG.Add(1)
	go func() {
		defer h.asyncWG.Done()
		base := h.lifecycleCtx
		if base == nil {
			base = context.Background()
		}
		// Session-end extraction is durable work: a host Close must stop new
		// work but let already-admitted extraction finish before the store is
		// closed. Keep values, drop cancellation, and bound the drain.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(base), 2*time.Minute)
		defer cancel()
		result, err := h.MemoryRuntime.RunSessionEndPipeline(ctx, sessionID, memoryruntime.SessionEndPipelineOptions{
			Consolidate: h.RuntimeConfig.Memory.Consolidation.Enabled,
		})
		if err != nil {
			logpkg.Warnf("memory session-end failed: session=%s stage=%s processed=%d failed=%d rollup_failed=%d",
				sessionID, result.Stage, result.Run.Processed, result.Run.Failed, result.Run.RollupFailed)
		}
	}()
}
