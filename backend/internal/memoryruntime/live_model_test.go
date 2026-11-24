package memoryruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestLiveModelMemoryPipeline is opt-in: normal go test runs never contact a
// model service. CONFIG/PROVIDER/MODEL/REASONING must all be explicitly supplied
// as AICLI_MEMORY_LIVE_* variables along with AICLI_MEMORY_LIVE=1.
// Only synthetic transcript data and temporary SQLite databases are used.
func TestLiveModelMemoryPipeline(t *testing.T) {
	llmRuntime, audit := newLiveMemoryLLM(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(filepath.Join(workspace, "sessions"))
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	manager := chat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	session := chat.NewSession("synthetic-memory-live-user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	for _, turn := range [][2]string{
		{
			"For the synthetic Orion project, preserve these durable decisions: the C++ worker uses clang++ 18, while the separate C# worker uses dotnet 9. Project documentation must be written in English.",
			"Confirmed Orion project decisions: C++ uses clang++ 18; C# uses dotnet 9. These are independent workers. Documentation is written in English.",
		},
		{
			"For the synthetic Orion project, Server 1 API timeout is 30s and Server 2 API timeout is 60s. These are different servers, not a correction of one server. The release workflow requires running schema migration tests before publishing.",
			"Verified: Server 1 API timeout is 30s; Server 2 API timeout is 60s. Preserve both settings. Run schema migration tests before publishing every Orion release.",
		},
	} {
		session.AddMessage(*types.NewUserMessage(turn[0]))
		session.AddMessage(*types.NewAssistantMessage(turn[1]))
	}
	session.State = chat.StateClosed
	session.UpdatedAt = time.Now().UTC()
	require.NoError(t, storage.Save(ctx, session))
	page, err := storage.GetMessagePage(ctx, session.ID, 0, 4)
	require.NoError(t, err)
	require.Len(t, page.Messages, 4)

	storeCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, "memory"), Mode: "on",
		InjectionMode: "hybrid", RecallTraceEnabled: true, RecallTraceSampleRate: 1,
	}
	store, err := memorystore.New(storeCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	cfg := Config{
		Mode: "on", Workspace: workspace, ProjectKey: store.ProjectKey(), RequireWorkspaceMatch: true,
		ExtractionProvider: audit.provider, ExtractionModel: audit.model, ExtractionReasoningEffort: audit.effort,
		ConsolidationProvider: audit.provider, ConsolidationModel: audit.model,
		MaxSourceMessages: 2, MaxAttempts: 1, SummaryTokenBudget: 1100,
		TurnDigestEnabled: true, TurnDigestStoreText: true, TurnDigestWindowSummaryEnabled: true,
		TurnDigestWindowTurns: 2, TurnDigestMaxTurnsPerBatch: 2,
	}
	cfg = WithLLMProjectSummarizer(cfg, llmRuntime)
	newRuntime := func() *Runtime {
		rt := New(store, &ChatSessionSource{Manager: manager},
			&LLMExtractor{Runtime: llmRuntime, Config: cfg}, store, cfg)
		rt.WindowSummarizer = &LLMWindowSummarizer{Runtime: llmRuntime, Config: cfg}
		return rt
	}
	rt := newRuntime()
	for turn := 0; turn < 2; turn++ {
		user, assistant := turn*2, turn*2+1
		require.NoError(t, rt.RecordTurnDigest(ctx, chat.TurnDigest{
			SessionID: session.ID, TurnID: fmt.Sprintf("synthetic-turn-%d", turn+1),
			UserMessageID:      messageIDForTest(page.Messages[user], page.FirstSeq+user),
			AssistantMessageID: messageIDForTest(page.Messages[assistant], page.FirstSeq+assistant),
			Prompt:             page.Messages[user].Content, AssistantOutput: page.Messages[assistant].Content,
			MessageSeqEnd: int64(page.FirstSeq + assistant), Outcome: "success",
		}))
	}

	result, err := rt.RunSessionEnd(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Equal(t, 2, result.Processed, "must extract both bounded canonical subwindows")
	require.Equal(t, 1, result.RolledUp)
	pending, err := store.ListPendingTurnDigests(ctx, session.ID, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	rollupPending, err := store.ListTurnDigestsForRollup(ctx, session.ID, 10)
	require.NoError(t, err)
	require.Empty(t, rollupPending)
	windows, err := store.ListTurnWindows(ctx, session.ID, 10)
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Equal(t, memorystore.TurnWindowDone, windows[0].Status)
	require.NotEmpty(t, windows[0].SummaryText)

	candidates, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 100})
	require.NoError(t, err)
	var extractedText strings.Builder
	var extracted, seeds int
	for _, record := range candidates.Items {
		switch record.SourceKind {
		case "session_extract":
			extracted++
			extractedText.WriteString(record.Text + "\n")
			require.NotEmpty(t, record.SourceRefs, "real extraction must retain canonical provenance")
		case "turn_window":
			seeds++
		}
	}
	require.Positive(t, extracted)
	require.Equal(t, 1, seeds)
	for _, identity := range []string{"C++", "C#", "Server 1", "Server 2"} {
		require.Contains(t, extractedText.String(), identity)
	}
	t.Logf("LIVE persisted extraction: candidates=%d L2_seeds=%d L1_windows=%d canonical=1-4 pending=0",
		extracted, seeds, len(windows))

	consolidated, err := NewConsolidator(store, cfg).Run(ctx)
	require.NoError(t, err)
	require.Positive(t, consolidated.Promoted)
	require.Zero(t, consolidated.Superseded, "independent entities must not overwrite each other")
	require.NotEmpty(t, consolidated.SummaryID)
	summary, err := store.ActiveSummary(ctx)
	require.NoError(t, err)
	require.NotNil(t, summary)
	require.Equal(t, audit.model, consolidated.SummaryModel,
		"summary must use the live model; fallback_reason=%v", summary.Metadata["fallback_reason"])
	require.Equal(t, "llm_v1", summary.Metadata["compiler"],
		"deterministic fallback does not count as a live summary pass; fallback_reason=%v", summary.Metadata["fallback_reason"])
	require.NotContains(t, summary.Metadata, "fallback_reason")
	require.Contains(t, summary.Text, "C++")
	require.Contains(t, summary.Text, "C#")
	require.LessOrEqual(t, summary.TokenEstimate, cfg.SummaryTokenBudget)
	selection, err := store.SelectForInjectV2(memorystore.InjectOptions{
		Query: "Orion C++ C# timeout release workflow", Limit: 10, TokenBudget: 2000, SessionID: "next-live-session",
	})
	require.NoError(t, err)
	require.NotNil(t, selection.Summary)
	require.Equal(t, summary.ID, selection.Summary.ID)
	require.NotEmpty(t, selection.IDs)
	t.Logf("LIVE project summary: model=%s compiler=llm_v1 promoted=%d summary_tokens=%d injected_ids=%d",
		summary.Model, consolidated.Promoted, summary.TokenEstimate, len(selection.IDs))
	audit.verify(t)

	// Reopen both stores; recovery/idempotency must not make another paid call.
	require.NoError(t, store.Close())
	manager.Stop()
	storage, err = chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	manager = chat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	store, err = memorystore.New(storeCfg)
	require.NoError(t, err)
	rt = newRuntime()
	recovered, err := rt.RunPendingTurnDigests(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Empty(t, recovered.Errors)
	require.Zero(t, recovered.Processed)
	require.Zero(t, recovered.RolledUp)
	again, err := NewConsolidator(store, cfg).Run(ctx)
	require.NoError(t, err)
	require.Zero(t, again.Promoted)
	restored, err := store.ActiveSummary(ctx)
	require.NoError(t, err)
	require.NotNil(t, restored)
	require.Equal(t, summary.ID, restored.ID)
	require.Equal(t, summary.Text, restored.Text)
	audit.verify(t)
	t.Log("LIVE restart: canonical/jobs/digests/L1/L2/summary persisted; no extra model requests")
}
