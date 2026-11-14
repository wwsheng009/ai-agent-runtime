package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

type lifecycleMemorySource struct {
	session    memoryruntime.SessionRef
	transcript memoryruntime.Transcript
}

func (s lifecycleMemorySource) ListSessions(context.Context, string, int) ([]memoryruntime.SessionRef, error) {
	return []memoryruntime.SessionRef{s.session}, nil
}

func (s lifecycleMemorySource) ReadTranscript(context.Context, memoryruntime.SessionRef, int) (memoryruntime.Transcript, error) {
	return s.transcript, nil
}

type lifecycleMemoryExtractor func(context.Context, memoryruntime.SessionRef, memoryruntime.Transcript) (memoryruntime.ExtractionResult, error)

func (f lifecycleMemoryExtractor) Extract(ctx context.Context, session memoryruntime.SessionRef, transcript memoryruntime.Transcript) (memoryruntime.ExtractionResult, error) {
	return f(ctx, session, transcript)
}

func TestMemorySessionEndDrainsBeforeRuntimeStoreCloses(t *testing.T) {
	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)

	now := time.Now().UTC()
	source := lifecycleMemorySource{
		session: memoryruntime.SessionRef{
			ID: "lifecycle-session", State: "closed", CanonicalCount: 1, LastActivityAt: now,
		},
		transcript: memoryruntime.Transcript{
			SessionID: "lifecycle-session", Hash: "lifecycle-source", Revision: 1,
			Messages: []memoryruntime.TranscriptMessage{{ID: "message-1", Seq: 1, Role: "user", Content: "preserve this"}},
		},
	}
	started := make(chan struct{})
	finished := make(chan struct{})
	extractor := lifecycleMemoryExtractor(func(ctx context.Context, _ memoryruntime.SessionRef, _ memoryruntime.Transcript) (memoryruntime.ExtractionResult, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return memoryruntime.ExtractionResult{}, ctx.Err()
	})
	runtime := memoryruntime.New(store, source, extractor, nil, memoryruntime.Config{
		Mode: "on", ProjectKey: store.ProjectKey(), MaxSourceMessages: 10,
		ConsolidationDisabled: true,
	})
	handler := NewHandler(nil, nil, nil)
	handler.SetMemoryRuntime(runtime)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Generation.Enabled = true
	cfg.Memory.Generation.Trigger = "session_end"
	cfg.Memory.Consolidation.Enabled = false
	handler.SetRuntimeConfig(cfg, "")

	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	handler.SetMemoryRuntimeLifecycleContext(lifecycleCtx)
	handler.TriggerMemorySessionEnd(source.session.ID)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancelLifecycle()
		_ = handler.CloseMemoryRuntime()
		t.Fatal("session-end extraction did not start")
	}

	cancelLifecycle()
	require.NoError(t, handler.CloseMemoryRuntime())
	select {
	case <-finished:
	default:
		t.Fatal("runtime store closed before session-end extraction drained")
	}
}
