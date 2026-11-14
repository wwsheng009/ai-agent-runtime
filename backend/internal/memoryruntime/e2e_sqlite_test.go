package memoryruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// TestE2ESQLiteCanonicalRestartRecovery covers the full durable path:
// SQLite canonical transcript -> turn digest outbox -> process restart ->
// canonical window extraction -> L0 rollup/L1 windows -> Consolidation/L2.
func TestE2ESQLiteCanonicalRestartRecovery(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	memoryRoot := filepath.Join(workspace, ".aicli", "memory")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false

	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	session := chat.NewSession("e2e-user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	for index := 1; index <= 6; index++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("user turn %d", index)))
		session.AddMessage(*types.NewAssistantMessage(fmt.Sprintf("assistant answer %d", index)))
	}
	session.State = chat.StateClosed
	session.UpdatedAt = time.Now().UTC()
	require.NoError(t, storage.Save(ctx, session))
	page, err := storage.GetMessagePage(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 12)
	sessionID := session.ID
	writerManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(writerManager.Stop)

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: memoryRoot, Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	require.NoError(t, err)
	writer := New(store, &ChatSessionSource{Manager: writerManager}, nil, store, Config{
		Mode: "shadow", ProjectKey: store.ProjectKey(), Workspace: workspace,
		TurnDigestEnabled: true, TurnDigestStoreText: true,
	})
	for turn := 0; turn < 3; turn++ {
		userIndex := turn * 4
		assistantIndex := userIndex + 3
		userID := messageIDForTest(page.Messages[userIndex], page.FirstSeq+userIndex)
		assistantID := messageIDForTest(page.Messages[assistantIndex], page.FirstSeq+assistantIndex)
		require.NoError(t, writer.RecordTurnDigest(ctx, chat.TurnDigest{
			SessionID: sessionID, TurnID: fmt.Sprintf("turn-%d", turn+1),
			UserMessageID: userID, AssistantMessageID: assistantID,
			Prompt:          page.Messages[userIndex].Content,
			AssistantOutput: page.Messages[assistantIndex].Content,
			MessageSeqEnd:   int64(page.FirstSeq + assistantIndex),
			Outcome:         "success",
		}))
	}
	require.NoError(t, store.Close())
	writerManager.Stop()

	// Simulate a process restart: reopen both durable stores from disk.
	storage, err = chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.CloseStorage()) })
	manager := chat.NewSessionManager(storage, nil)
	source := &ChatSessionSource{Manager: manager}
	store, err = memorystore.New(memoryCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	extractor := &e2eTranscriptExtractor{}
	summarizer := &e2eWindowSummarizer{}
	runtime := New(store, source, extractor, fakeModes{}, Config{
		Mode: "shadow", Workspace: workspace, ProjectKey: store.ProjectKey(),
		MinIdle: time.Hour, MaxSessionsPerRun: 5, MaxSourceMessages: 4,
		MaxCandidatesPerSession: 20, MaxCandidateTextRunes: 4000,
		TurnDigestEnabled: true, TurnDigestWindowSummaryEnabled: true,
		TurnDigestWindowTurns: 2, TurnDigestMaxTurnsPerBatch: 1,
		TurnDigestWindowRetentionDays: 30,
	})
	runtime.WindowSummarizer = summarizer

	// Startup/idle recovery consumes one bounded window batch per sweep.
	totalProcessed := 0
	for sweep := 0; sweep < 3; sweep++ {
		result, err := runtime.RunPendingTurnDigests(ctx, TriggerSessionEnd)
		require.NoError(t, err)
		require.Empty(t, result.Errors)
		totalProcessed += result.Processed
	}
	require.Equal(t, 3, totalProcessed)
	require.Equal(t, 3, extractor.calls)
	require.Equal(t, 2, summarizer.calls)

	pending, err := store.ListPendingTurnDigests(ctx, sessionID, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	ready, err := store.ListTurnDigestsForRollup(ctx, sessionID, 10)
	require.NoError(t, err)
	require.Empty(t, ready)
	windows, err := store.ListTurnWindows(ctx, sessionID, 10)
	require.NoError(t, err)
	require.Len(t, windows, 2)
	for _, window := range windows {
		require.Equal(t, memorystore.TurnWindowDone, window.Status)
		require.NotEmpty(t, window.SummaryText)
	}
	for turn := 1; turn <= 3; turn++ {
		digest, err := store.GetTurnDigestByTurn(ctx, sessionID, fmt.Sprintf("turn-%d", turn))
		require.NoError(t, err)
		require.Equal(t, memorystore.TurnDigestDone, digest.Status)
		require.Equal(t, memorystore.TurnDigestRollupDone, digest.RollupStatus)
	}

	consolidator := NewConsolidator(store, runtime.Config)
	consolidator.Clock = runtime.Clock
	consolidated, err := consolidator.Run(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, consolidated.SummaryID)
	require.Contains(t, consolidated.SummaryText, "E2E window summary")

	active, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusActive, Limit: 50})
	require.NoError(t, err)
	require.NotEmpty(t, active.Items)
}

type e2eTranscriptExtractor struct {
	calls int
}

func (e *e2eTranscriptExtractor) Extract(_ context.Context, _ SessionRef, transcript Transcript) (ExtractionResult, error) {
	e.calls++
	if len(transcript.Messages) == 0 {
		return ExtractionResult{}, fmt.Errorf("empty transcript")
	}
	last := transcript.Messages[len(transcript.Messages)-1]
	return ExtractionResult{Candidates: []Candidate{{
		Kind: "decision", Text: fmt.Sprintf("E2E extracted memory from %s", last.ID),
		Confidence: 0.9, SourceMessageIDs: []string{last.ID},
	}}}, nil
}

type e2eWindowSummarizer struct {
	calls int
}

func (s *e2eWindowSummarizer) SummarizeWindow(_ context.Context, _ WindowSummaryInput) (WindowSummary, error) {
	s.calls++
	return WindowSummary{
		Text:  fmt.Sprintf("E2E window summary %d", s.calls),
		Model: "e2e-fake",
	}, nil
}

func messageIDForTest(message types.Message, seq int) string {
	if id := types.MessageID(message); id != "" {
		return id
	}
	return fmt.Sprintf("msg-%d", seq)
}

const e2eCrashChildEnv = "AICLI_E2E_CRASH_CHILD"

type e2eCrashMarker struct {
	SessionID string `json:"session_id"`
}

// TestE2ESQLiteAbruptProcessExitRecovery verifies that a child process can
// persist the session transcript + digest outbox and then exit without closing
// either store. The parent reopens the same SQLite files and drains the outbox.
func TestE2ESQLiteAbruptProcessExitRecovery(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	childCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, os.Args[0],
		"-test.run=^TestE2ESQLiteCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		e2eCrashChildEnv+"=1",
		"AICLI_E2E_CRASH_WORKSPACE="+workspace,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	marker := waitE2ECrashMarker(t, workspace, 30*time.Second)
	assertE2ESQLiteCrashRecovery(t, workspace, marker)
}

// TestE2ESQLiteHardKillRecovery kills the writer process after it commits the
// session + digest rows. On Windows Process.Kill maps to TerminateProcess; on
// POSIX it is the hard-kill equivalent. No application cleanup runs.
func TestE2ESQLiteHardKillRecovery(t *testing.T) {
	workspace := t.TempDir()
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestE2ESQLiteCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		e2eCrashChildEnv+"=1",
		"AICLI_E2E_CRASH_WORKSPACE="+workspace,
		"AICLI_E2E_CRASH_WAIT=1",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	marker := waitE2ECrashMarker(t, workspace, 30*time.Second)
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	waited = true
	assertE2ESQLiteCrashRecovery(t, workspace, marker)
}

func waitE2ECrashMarker(t *testing.T, workspace string, timeout time.Duration) e2eCrashMarker {
	t.Helper()
	markerPath := filepath.Join(workspace, "crash-marker.json")
	deadline := time.Now().Add(timeout)
	for {
		rawMarker, err := os.ReadFile(markerPath)
		if err == nil {
			var marker e2eCrashMarker
			if json.Unmarshal(rawMarker, &marker) == nil && marker.SessionID != "" {
				return marker
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for crash marker %s", markerPath)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func assertE2ESQLiteCrashRecovery(t *testing.T, workspace string, marker e2eCrashMarker) {
	t.Helper()
	ctx := context.Background()
	markerPath := filepath.Join(workspace, "crash-marker.json")
	require.FileExists(t, markerPath)

	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.CloseStorage()) })
	recoveredSession, err := storage.Load(ctx, marker.SessionID)
	require.NoError(t, err)
	require.Equal(t, chat.StateClosed, recoveredSession.State)
	require.EqualValues(t, 12, recoveredSession.CanonicalMessageCount)

	manager := chat.NewSessionManager(storage, nil)
	source := &ChatSessionSource{Manager: manager}
	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	extractor := &e2eTranscriptExtractor{}
	summarizer := &e2eWindowSummarizer{}
	runtime := New(store, source, extractor, fakeModes{}, Config{
		Mode: "shadow", Workspace: workspace, ProjectKey: store.ProjectKey(),
		MinIdle: time.Hour, MaxSessionsPerRun: 5, MaxSourceMessages: 4,
		MaxCandidatesPerSession: 20, MaxCandidateTextRunes: 4000,
		TurnDigestEnabled: true, TurnDigestWindowSummaryEnabled: true,
		TurnDigestWindowTurns: 2, TurnDigestMaxTurnsPerBatch: 1,
		TurnDigestWindowRetentionDays: 30,
	})
	runtime.WindowSummarizer = summarizer

	totalProcessed := 0
	for sweep := 0; sweep < 3; sweep++ {
		result, err := runtime.RunPendingTurnDigests(ctx, TriggerSessionEnd)
		require.NoError(t, err)
		require.Empty(t, result.Errors)
		totalProcessed += result.Processed
	}
	require.Equal(t, 3, totalProcessed)
	require.Equal(t, 3, extractor.calls)
	require.Equal(t, 2, summarizer.calls)
	pending, err := store.ListPendingTurnDigests(ctx, marker.SessionID, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	windows, err := store.ListTurnWindows(ctx, marker.SessionID, 10)
	require.NoError(t, err)
	require.Len(t, windows, 2)
	for _, window := range windows {
		require.Equal(t, memorystore.TurnWindowDone, window.Status)
		require.NotEmpty(t, window.SummaryText)
	}
	consolidated, err := NewConsolidator(store, runtime.Config).Run(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, consolidated.SummaryID)
	require.Contains(t, consolidated.SummaryText, "E2E window summary")
}

// TestE2ESQLiteCrashChild is the helper process for the abrupt-exit E2E. It is
// intentionally a test entry so `go test` can launch it via os.Args[0].
func TestE2ESQLiteCrashChild(t *testing.T) {
	workspace := strings.TrimSpace(os.Getenv("AICLI_E2E_CRASH_WORKSPACE"))
	if os.Getenv(e2eCrashChildEnv) != "1" || workspace == "" {
		t.Skip("helper process only")
	}
	ctx := context.Background()
	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	if err != nil {
		t.Fatalf("open session storage: %v", err)
	}
	session := chat.NewSession("e2e-crash-user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	for index := 1; index <= 6; index++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("crash user turn %d", index)))
		session.AddMessage(*types.NewAssistantMessage(fmt.Sprintf("crash assistant answer %d", index)))
	}
	session.State = chat.StateClosed
	session.UpdatedAt = time.Now().UTC()
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("save crash session: %v", err)
	}
	page, err := storage.GetMessagePage(ctx, session.ID, 0, 0)
	if err != nil {
		t.Fatalf("read crash canonical page: %v", err)
	}

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	if err != nil {
		t.Fatalf("open crash memory store: %v", err)
	}
	writer := New(store, &ChatSessionSource{Manager: chat.NewSessionManager(storage, nil)}, nil, store, Config{
		Mode: "shadow", ProjectKey: store.ProjectKey(), Workspace: workspace,
		TurnDigestEnabled: true, TurnDigestStoreText: true,
	})
	for turn := 0; turn < 3; turn++ {
		userIndex := turn * 4
		assistantIndex := userIndex + 3
		if err := writer.RecordTurnDigest(ctx, chat.TurnDigest{
			SessionID: session.ID, TurnID: fmt.Sprintf("turn-%d", turn+1),
			UserMessageID:      messageIDForTest(page.Messages[userIndex], page.FirstSeq+userIndex),
			AssistantMessageID: messageIDForTest(page.Messages[assistantIndex], page.FirstSeq+assistantIndex),
			Prompt:             page.Messages[userIndex].Content,
			AssistantOutput:    page.Messages[assistantIndex].Content,
			MessageSeqEnd:      int64(page.FirstSeq + assistantIndex),
			Outcome:            "success",
		}); err != nil {
			t.Fatalf("write crash digest: %v", err)
		}
	}
	raw, err := json.Marshal(e2eCrashMarker{SessionID: session.ID})
	if err != nil {
		t.Fatalf("marshal crash marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "crash-marker.json"), raw, 0o644); err != nil {
		t.Fatalf("write crash marker: %v", err)
	}
	if os.Getenv("AICLI_E2E_CRASH_WAIT") == "1" {
		// Wait for the parent to issue a hard process kill.
		time.Sleep(24 * time.Hour)
	}
	// Deliberately skip CloseStorage/Store: the process disappears with open
	// SQLite handles after all commits and the marker write.
	os.Exit(0)
}
