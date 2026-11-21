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
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
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
const e2eCrashModeEnv = "AICLI_E2E_CRASH_MODE"

type e2eCrashMarker struct {
	SessionID         string   `json:"session_id"`
	ReceiptIDs        []string `json:"receipt_ids,omitempty"`
	DigestID          string   `json:"digest_id,omitempty"`
	JobID             string   `json:"job_id,omitempty"`
	SessionGeneration int64    `json:"session_generation,omitempty"`
	PurgeGeneration   int64    `json:"purge_generation,omitempty"`
	Stage             string   `json:"stage,omitempty"`
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

// TestE2ESQLiteActiveCaptureHardKillRecovery kills a process immediately after
// atomic canonical-session/capture-receipt commits. Recovery must use the
// durable closed turn ranges even though the canonical session remains active.
func TestE2ESQLiteActiveCaptureHardKillRecovery(t *testing.T) {
	workspace := t.TempDir()
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestE2ESQLiteCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		e2eCrashChildEnv+"=1",
		e2eCrashModeEnv+"=active_capture",
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
	require.Len(t, marker.ReceiptIDs, 2)
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	waited = true
	assertE2ESQLiteActiveCaptureRecovery(t, workspace, marker)
}

// TestE2ESQLiteMaterializationAckHardKillRecovery kills the process after the
// memory-side digest/job transaction commits but before the canonical receipt
// is acknowledged. Reopening both stores must replay the handoff idempotently.
func TestE2ESQLiteMaterializationAckHardKillRecovery(t *testing.T) {
	workspace := t.TempDir()
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestE2ESQLiteCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		e2eCrashChildEnv+"=1",
		e2eCrashModeEnv+"=materialized_unacked",
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
	require.Len(t, marker.ReceiptIDs, 1)
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	waited = true
	assertE2ESQLiteMaterializationAckRecovery(t, workspace, marker)
}

// TestE2ESQLiteCaptureWorkerHardKillRecovery kills the worker after it has
// claimed a receipt-backed job but before extraction, and again after the
// atomic candidate/output-receipt/job/digest commit. Recovery must reclaim the
// stale lease exactly once and must not re-extract committed output.
func TestE2ESQLiteCaptureWorkerHardKillRecovery(t *testing.T) {
	for _, stage := range []string{"worker_claimed", "worker_committed"} {
		t.Run(stage, func(t *testing.T) {
			workspace := t.TempDir()
			cmd := exec.Command(os.Args[0],
				"-test.run=^TestE2ESQLiteCrashChild$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				e2eCrashChildEnv+"=1",
				e2eCrashModeEnv+"="+stage,
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
			require.Equal(t, stage, marker.Stage)
			require.Len(t, marker.ReceiptIDs, 1)
			require.NotEmpty(t, marker.DigestID)
			require.NotEmpty(t, marker.JobID)
			require.NoError(t, cmd.Process.Kill())
			_ = cmd.Wait()
			waited = true

			assertE2ESQLiteCaptureWorkerRecovery(t, workspace, marker)
		})
	}
}

// TestE2ESQLiteSessionPurgeHardKillRecovery kills the process at each durable
// boundary in the cross-database purge protocol. Recovery is deliberately
// exercised through SessionManager.ResumePendingSessionPurges, the same
// generation-qualified coordinator used by normal application wiring.
func TestE2ESQLiteSessionPurgeHardKillRecovery(t *testing.T) {
	for _, stage := range []string{"fenced", "memory_purged", "acknowledged"} {
		t.Run(stage, func(t *testing.T) {
			workspace := t.TempDir()
			cmd := exec.Command(os.Args[0],
				"-test.run=^TestE2ESQLiteCrashChild$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				e2eCrashChildEnv+"=1",
				e2eCrashModeEnv+"=purge_"+stage,
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
			require.Equal(t, stage, marker.Stage)
			require.Positive(t, marker.SessionGeneration)
			require.Positive(t, marker.PurgeGeneration)
			require.NoError(t, cmd.Process.Kill())
			_ = cmd.Wait()
			waited = true

			assertE2ESQLiteSessionPurgeRecovery(t, workspace, marker)
		})
	}
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

func assertE2ESQLiteActiveCaptureRecovery(t *testing.T, workspace string, marker e2eCrashMarker) {
	t.Helper()
	ctx := context.Background()
	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.CloseStorage()) })

	session, err := storage.Load(ctx, marker.SessionID)
	require.NoError(t, err)
	require.Equal(t, chat.StateActive, session.State, "hard-kill recovery must not depend on a closed session")
	require.Equal(t, 4, session.CanonicalMessageCount)
	require.Positive(t, session.SessionGeneration)
	receipts, err := storage.ListTurnCaptureReceiptsForSession(ctx, marker.SessionID, 10)
	require.NoError(t, err)
	require.Len(t, receipts, 2)
	receiptIDs := make(map[string]bool, len(receipts))
	turnRanges := make(map[string][2]int64, len(receipts))
	for _, receipt := range receipts {
		receiptIDs[receipt.ID] = true
		turnRanges[receipt.TurnID] = [2]int64{receipt.MessageSeqStart, receipt.MessageSeqEnd}
		require.Equal(t, session.SessionGeneration, receipt.SessionGeneration)
		require.Positive(t, receipt.CanonicalRevision)
		require.Len(t, receipt.RangeHash, 64)
		require.Equal(t, "captured", receipt.State)
		require.NoError(t, storage.ValidateTurnCaptureReceipt(ctx, receipt))
	}
	for _, id := range marker.ReceiptIDs {
		require.True(t, receiptIDs[id], "receipt %q was not recovered from canonical SQLite", id)
	}
	require.Equal(t, [2]int64{1, 2}, turnRanges["hardkill-turn-1"])
	require.Equal(t, [2]int64{3, 4}, turnRanges["hardkill-turn-2"])

	manager := chat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	source := &ChatSessionSource{Manager: manager}
	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	extractor := &e2eTranscriptExtractor{}
	runtime := New(store, source, extractor, fakeModes{}, Config{
		Mode: "shadow", Workspace: workspace, ProjectKey: store.ProjectKey(),
		MaxSourceMessages: 8, MaxCandidatesPerSession: 10, MaxCandidateTextRunes: 4000,
		TurnDigestEnabled: true, TurnDigestStoreText: true,
	})

	materialized, err := runtime.MaterializeTurnCaptureReceiptsForSessionGeneration(
		ctx, marker.SessionID, session.SessionGeneration, 10)
	require.NoError(t, err)
	require.Equal(t, 2, materialized)
	for _, id := range marker.ReceiptIDs {
		receipt, err := storage.GetTurnCaptureReceipt(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "materialized", receipt.State)
	}

	recovered, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Empty(t, recovered.Errors)
	require.Equal(t, 2, recovered.Processed)
	require.Equal(t, 2, extractor.calls)
	for _, id := range marker.ReceiptIDs {
		digest, err := store.GetTurnDigestByCaptureReceipt(ctx, id)
		require.NoError(t, err)
		require.Equal(t, memorystore.TurnDigestDone, digest.Status)
		require.Equal(t, session.SessionGeneration, digest.SessionGeneration)
	}

	// Replaying both durable recovery stages is idempotent and does not invoke
	// extraction again for already-completed receipt-backed turns.
	materialized, err = runtime.MaterializeTurnCaptureReceiptsForSessionGeneration(
		ctx, marker.SessionID, session.SessionGeneration, 10)
	require.NoError(t, err)
	require.Zero(t, materialized)
	replayed, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Empty(t, replayed.Errors)
	require.Zero(t, replayed.Processed)
	require.Equal(t, 2, extractor.calls)
}

func assertE2ESQLiteMaterializationAckRecovery(t *testing.T, workspace string, marker e2eCrashMarker) {
	t.Helper()
	ctx := context.Background()
	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.CloseStorage()) })

	session, err := storage.Load(ctx, marker.SessionID)
	require.NoError(t, err)
	require.Equal(t, chat.StateActive, session.State)
	require.Positive(t, session.SessionGeneration)
	require.Len(t, marker.ReceiptIDs, 1)
	receipt, err := storage.GetTurnCaptureReceipt(ctx, marker.ReceiptIDs[0])
	require.NoError(t, err)
	require.Equal(t, "captured", receipt.State,
		"the child must have crashed before canonical acknowledgement")
	require.Equal(t, session.SessionGeneration, receipt.SessionGeneration)
	require.NoError(t, storage.ValidateTurnCaptureReceipt(ctx, receipt))

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	digestBefore, err := store.GetTurnDigestByCaptureReceipt(ctx, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestPending, digestBefore.Status)
	require.NotEmpty(t, digestBefore.ProcessJobID)
	jobsBefore, err := store.ListJobs(ctx, 100)
	require.NoError(t, err)
	require.Len(t, jobsBefore, 1)
	require.Equal(t, digestBefore.ProcessJobID, jobsBefore[0].ID)
	require.Equal(t, memorystore.JobStatusPending, jobsBefore[0].Status)

	manager := chat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	source := &ChatSessionSource{Manager: manager}
	extractor := &e2eTranscriptExtractor{}
	runtime := New(store, source, extractor, fakeModes{}, Config{
		Mode: "shadow", Workspace: workspace, ProjectKey: store.ProjectKey(),
		MaxSourceMessages: 8, MaxCandidatesPerSession: 10, MaxCandidateTextRunes: 4000,
		TurnDigestEnabled: true, TurnDigestStoreText: true,
	})
	materialized, err := runtime.MaterializeTurnCaptureReceiptsForSessionGeneration(
		ctx, session.ID, session.SessionGeneration, 10)
	require.NoError(t, err)
	require.Equal(t, 1, materialized)
	ackedReceipt, err := storage.GetTurnCaptureReceipt(ctx, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, "materialized", ackedReceipt.State)

	digestAfter, err := store.GetTurnDigestByCaptureReceipt(ctx, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, digestBefore.ID, digestAfter.ID, "retry must reuse the committed digest")
	require.Equal(t, digestBefore.ProcessJobID, digestAfter.ProcessJobID,
		"retry must reuse the committed extraction job")
	jobsAfter, err := store.ListJobs(ctx, 100)
	require.NoError(t, err)
	require.Len(t, jobsAfter, 1, "handoff retry must not create duplicate jobs")

	recovered, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Empty(t, recovered.Errors)
	require.Equal(t, 1, recovered.Processed)
	require.Equal(t, 1, extractor.calls)
	status, err := store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.ByStatus[string(memorystore.StatusCandidate)])
	require.EqualValues(t, 1, status.Jobs[string(memorystore.JobStatusSucceeded)])

	materialized, err = runtime.MaterializeTurnCaptureReceiptsForSessionGeneration(
		ctx, session.ID, session.SessionGeneration, 10)
	require.NoError(t, err)
	require.Zero(t, materialized)
	replayed, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Empty(t, replayed.Errors)
	require.Zero(t, replayed.Processed)
	require.Equal(t, 1, extractor.calls, "completed output must not be extracted twice")
	status, err = store.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.ByStatus[string(memorystore.StatusCandidate)])
}

func assertE2ESQLiteCaptureWorkerRecovery(t *testing.T, workspace string, marker e2eCrashMarker) {
	t.Helper()
	ctx := context.Background()
	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.CloseStorage()) })

	session, err := storage.Load(ctx, marker.SessionID)
	require.NoError(t, err)
	require.Equal(t, chat.StateActive, session.State)
	require.Positive(t, session.SessionGeneration)
	require.Len(t, marker.ReceiptIDs, 1)
	receipt, err := storage.GetTurnCaptureReceipt(ctx, marker.ReceiptIDs[0])
	require.NoError(t, err)
	require.Equal(t, "materialized", receipt.State)
	require.Equal(t, session.SessionGeneration, receipt.SessionGeneration)
	require.NoError(t, storage.ValidateTurnCaptureReceipt(ctx, receipt))

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	digestBefore, err := store.GetTurnDigestByCaptureReceipt(ctx, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, marker.DigestID, digestBefore.ID)
	require.Equal(t, marker.JobID, digestBefore.ProcessJobID)
	require.Equal(t, session.SessionGeneration, digestBefore.SessionGeneration)

	jobsBefore, err := store.ListJobs(ctx, 100)
	require.NoError(t, err)
	require.Len(t, jobsBefore, 1)
	require.Equal(t, marker.JobID, jobsBefore[0].ID)
	require.Equal(t, "phase1_extract/v2", jobsBefore[0].Kind)

	memoryDB, err := sqliteutil.OpenFile(store.DBPath(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, memoryDB.Close()) })
	outputReceipts := func() int {
		var count int
		require.NoError(t, memoryDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM memory_turn_output_receipts
			WHERE project_key = ? AND capture_receipt_id = ?
		`, store.ProjectKey(), receipt.ID).Scan(&count))
		return count
	}
	candidates := func() int {
		status, err := store.Status(ctx)
		require.NoError(t, err)
		return status.ByStatus[string(memorystore.StatusCandidate)]
	}

	switch marker.Stage {
	case "worker_claimed":
		require.Equal(t, memorystore.TurnDigestPending, digestBefore.Status)
		require.Equal(t, memorystore.JobStatusRunning, jobsBefore[0].Status)
		require.True(t, jobsBefore[0].LeaseUntil.After(time.Now().UTC()))
		require.Zero(t, outputReceipts())
		require.Zero(t, candidates())
	case "worker_committed":
		require.Equal(t, memorystore.TurnDigestDone, digestBefore.Status)
		require.Equal(t, memorystore.JobStatusSucceeded, jobsBefore[0].Status)
		require.Equal(t, 1, outputReceipts())
		require.Equal(t, 1, candidates())
	default:
		t.Fatalf("unexpected capture worker crash stage %q", marker.Stage)
	}

	manager := chat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	extractor := &e2eTranscriptExtractor{}
	runtime := New(store, &ChatSessionSource{Manager: manager}, extractor, fakeModes{}, Config{
		Mode: "shadow", Workspace: workspace, ProjectKey: store.ProjectKey(),
		MaxSourceMessages: 8, MaxCandidatesPerSession: 10, MaxCandidateTextRunes: 4000,
		TurnDigestEnabled: true, TurnDigestStoreText: true,
	})
	if marker.Stage == "worker_claimed" {
		// Advance beyond the crashed child's short lease instead of sleeping in
		// the test. Commit-time validation uses the store's real clock, while the
		// claim path uses this injected worker clock.
		runtime.Clock = func() time.Time { return time.Now().UTC().Add(2 * time.Minute) }
	}

	recovered, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Empty(t, recovered.Errors)
	if marker.Stage == "worker_claimed" {
		require.Equal(t, 1, recovered.Processed)
		require.Equal(t, 1, extractor.calls)
	} else {
		require.Zero(t, recovered.Processed)
		require.Zero(t, extractor.calls, "committed output must not be re-extracted")
	}

	digestAfter, err := store.GetTurnDigestByCaptureReceipt(ctx, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, digestAfter.Status)
	require.Equal(t, marker.JobID, digestAfter.ProcessJobID)
	jobsAfter, err := store.ListJobs(ctx, 100)
	require.NoError(t, err)
	require.Len(t, jobsAfter, 1, "recovery must reuse the claimed job identity")
	require.Equal(t, marker.JobID, jobsAfter[0].ID)
	require.Equal(t, memorystore.JobStatusSucceeded, jobsAfter[0].Status)
	require.Equal(t, 1, outputReceipts())
	require.Equal(t, 1, candidates())

	replayed, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerStartup)
	require.NoError(t, err)
	require.Empty(t, replayed.Errors)
	require.Zero(t, replayed.Processed)
	if marker.Stage == "worker_claimed" {
		require.Equal(t, 1, extractor.calls)
	} else {
		require.Zero(t, extractor.calls)
	}
	require.Equal(t, 1, outputReceipts(), "replay must not duplicate the output receipt")
	require.Equal(t, 1, candidates(), "replay must not duplicate candidates")
}

func assertE2ESQLiteSessionPurgeRecovery(t *testing.T, workspace string, marker e2eCrashMarker) {
	t.Helper()
	ctx := context.Background()

	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.CloseStorage()) })

	pending, err := storage.ListPendingSessionPurges(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, marker.SessionID, pending[0].SessionID)
	require.Equal(t, marker.SessionGeneration, pending[0].SessionGeneration)
	require.Equal(t, marker.PurgeGeneration, pending[0].PurgeGeneration)
	require.True(t, pending[0].MemoryRequired)
	require.Equal(t, marker.Stage == "acknowledged", pending[0].MemoryAcknowledged)

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	memoryDB, err := sqliteutil.OpenFile(store.DBPath(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, memoryDB.Close()) })
	candidateCount := func() int {
		var count int
		require.NoError(t, memoryDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM memory_entries
			WHERE project_key = ? AND source_session_id = ? AND source_session_generation = ?
		`, store.ProjectKey(), marker.SessionID, marker.SessionGeneration).Scan(&count))
		return count
	}
	tombstoneCount := func() int {
		var count int
		require.NoError(t, memoryDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM memory_session_purges
			WHERE project_key = ? AND session_id = ?
		`, store.ProjectKey(), marker.SessionID).Scan(&count))
		return count
	}

	switch marker.Stage {
	case "fenced":
		require.Zero(t, tombstoneCount(), "the child must have crashed before the memory purge")
		require.Equal(t, 1, candidateCount(), "pre-fence memory projection should still exist")
	case "memory_purged", "acknowledged":
		require.Equal(t, 1, tombstoneCount(), "the memory purge transaction must have committed")
		require.Zero(t, candidateCount(), "the target incarnation projection should already be removed")
	default:
		t.Fatalf("unexpected session purge crash stage %q", marker.Stage)
	}

	manager := chat.NewSessionManager(storage, &chat.SessionManagerConfig{
		CleanupInterval: 0, AutoArchive: false,
	})
	t.Cleanup(manager.Stop)
	manager.SetSessionPurgeCoordinatorV2(func(
		ctx context.Context, sessionID string, sessionGeneration, purgeGeneration int64,
	) error {
		_, err := store.PurgeSessionWithGenerations(
			ctx, sessionID, sessionGeneration, purgeGeneration)
		return err
	})

	completed, err := manager.ResumePendingSessionPurges(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, completed)
	completed, err = manager.ResumePendingSessionPurges(ctx)
	require.NoError(t, err)
	require.Zero(t, completed, "recovery replay should be idempotent")

	pending, err = storage.ListPendingSessionPurges(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	_, err = storage.Load(ctx, marker.SessionID)
	require.ErrorIs(t, err, chat.ErrSessionNotFound)
	require.Zero(t, candidateCount())

	var memoryState string
	var fencedGeneration, purgeGeneration int64
	require.NoError(t, memoryDB.QueryRowContext(ctx, `
		SELECT state, fenced_session_generation, purge_generation
		FROM memory_session_purges
		WHERE project_key = ? AND session_id = ?
	`, store.ProjectKey(), marker.SessionID).Scan(&memoryState, &fencedGeneration, &purgeGeneration))
	require.Equal(t, "purged", memoryState)
	require.Equal(t, marker.SessionGeneration, fencedGeneration)
	require.Equal(t, marker.PurgeGeneration, purgeGeneration)

	canonicalDB, err := sqliteutil.OpenFile(sessionCfg.Path, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, canonicalDB.Close()) })
	var journalState string
	var acknowledgedGeneration int64
	require.NoError(t, canonicalDB.QueryRowContext(ctx, `
		SELECT state, memory_ack_generation
		FROM session_purge_journal WHERE session_id = ?
	`, marker.SessionID).Scan(&journalState, &acknowledgedGeneration))
	require.Equal(t, "complete", journalState)
	require.Equal(t, marker.PurgeGeneration, acknowledgedGeneration)
}

// TestE2ESQLiteCrashChild is the helper process for the abrupt-exit E2E. It is
// intentionally a test entry so `go test` can launch it via os.Args[0].
func TestE2ESQLiteCrashChild(t *testing.T) {
	workspace := strings.TrimSpace(os.Getenv("AICLI_E2E_CRASH_WORKSPACE"))
	if os.Getenv(e2eCrashChildEnv) != "1" || workspace == "" {
		t.Skip("helper process only")
	}
	ctx := context.Background()
	if os.Getenv(e2eCrashModeEnv) == "active_capture" {
		runE2EActiveCaptureCrashChild(t, ctx, workspace)
		return
	}
	if os.Getenv(e2eCrashModeEnv) == "materialized_unacked" {
		runE2EMaterializedUnackedCrashChild(t, ctx, workspace)
		return
	}
	if mode := os.Getenv(e2eCrashModeEnv); mode == "worker_claimed" || mode == "worker_committed" {
		runE2ECaptureWorkerCrashChild(t, ctx, workspace, mode)
		return
	}
	if mode := os.Getenv(e2eCrashModeEnv); strings.HasPrefix(mode, "purge_") {
		runE2ESessionPurgeCrashChild(t, ctx, workspace, strings.TrimPrefix(mode, "purge_"))
		return
	}
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

func runE2ESessionPurgeCrashChild(t *testing.T, ctx context.Context, workspace, stage string) {
	t.Helper()

	switch stage {
	case "fenced", "memory_purged", "acknowledged":
	default:
		t.Fatalf("unsupported session purge crash stage %q", stage)
	}

	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	if err != nil {
		t.Fatalf("open session purge crash storage: %v", err)
	}
	storage.SetSessionPurgeMemoryRequired(true)

	session := chat.NewSession("e2e-session-purge-user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	session.AddMessage(*types.NewUserMessage("temporary purge recovery fixture"))
	session.State = chat.StateClosed
	session.UpdatedAt = time.Now().UTC()
	if err := storage.Save(ctx, session); err != nil {
		t.Fatalf("save session purge fixture: %v", err)
	}
	session, err = storage.Load(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload session purge fixture: %v", err)
	}
	if session.SessionGeneration <= 0 {
		t.Fatalf("session purge fixture has invalid generation %d", session.SessionGeneration)
	}

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	if err != nil {
		t.Fatalf("open session purge crash memory store: %v", err)
	}
	if _, err := store.InsertCandidate(ctx, memorystore.Candidate{
		SessionID: session.ID, SessionGeneration: session.SessionGeneration,
		ProjectKey: store.ProjectKey(), Kind: "fact", SourceKind: "session_extract",
		Text: "temporary generation-qualified purge fixture",
	}); err != nil {
		t.Fatalf("insert generation-qualified purge fixture: %v", err)
	}

	purge, err := storage.BeginSessionPurge(ctx, session.ID, "e2e_hard_kill")
	if err != nil {
		t.Fatalf("begin session purge: %v", err)
	}
	if purge.SessionGeneration != session.SessionGeneration || purge.PurgeGeneration <= 0 {
		t.Fatalf("unexpected session purge generations: session=%d purge=%d",
			purge.SessionGeneration, purge.PurgeGeneration)
	}
	if !purge.MemoryRequired {
		t.Fatal("session purge fixture did not require memory acknowledgement")
	}

	if stage == "memory_purged" || stage == "acknowledged" {
		if _, err := store.PurgeSessionWithGenerations(
			ctx, purge.SessionID, purge.SessionGeneration, purge.PurgeGeneration); err != nil {
			t.Fatalf("purge memory session incarnation: %v", err)
		}
	}
	if stage == "acknowledged" {
		if err := storage.AcknowledgeSessionMemoryPurgeWithGenerations(
			ctx, purge.SessionID, purge.SessionGeneration, purge.PurgeGeneration); err != nil {
			t.Fatalf("acknowledge memory session purge: %v", err)
		}
	}

	raw, err := json.Marshal(e2eCrashMarker{
		SessionID: purge.SessionID, SessionGeneration: purge.SessionGeneration,
		PurgeGeneration: purge.PurgeGeneration, Stage: stage,
	})
	if err != nil {
		t.Fatalf("marshal session purge crash marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "crash-marker.json"), raw, 0o644); err != nil {
		t.Fatalf("write session purge crash marker: %v", err)
	}
	if os.Getenv("AICLI_E2E_CRASH_WAIT") == "1" {
		// Keep both SQLite handles open until the parent terminates this process.
		time.Sleep(24 * time.Hour)
	}
	os.Exit(0)
}

func runE2EActiveCaptureCrashChild(t *testing.T, ctx context.Context, workspace string) {
	t.Helper()
	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	if err != nil {
		t.Fatalf("open active capture session storage: %v", err)
	}

	session := chat.NewSession("e2e-active-crash-user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	appendE2ECrashTurn(session, "hardkill-turn-1", "first committed active turn")
	first, err := storage.SaveWithTurnCapture(ctx, session, chat.TurnCaptureReceipt{
		SessionID: session.ID, TurnID: "hardkill-turn-1", Outcome: "success",
	})
	if err != nil {
		t.Fatalf("atomically save first active turn and capture receipt: %v", err)
	}

	appendE2ECrashTurn(session, "hardkill-turn-2", "second committed active turn")
	second, err := storage.UpdateWithTurnCapture(ctx, session, chat.TurnCaptureReceipt{
		SessionID: session.ID, TurnID: "hardkill-turn-2", Outcome: "success",
	})
	if err != nil {
		t.Fatalf("atomically update active session and capture receipt: %v", err)
	}
	if session.State != chat.StateActive {
		t.Fatalf("capture fixture unexpectedly changed session state to %q", session.State)
	}

	raw, err := json.Marshal(e2eCrashMarker{
		SessionID: session.ID, ReceiptIDs: []string{first.ID, second.ID},
	})
	if err != nil {
		t.Fatalf("marshal active capture crash marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "crash-marker.json"), raw, 0o644); err != nil {
		t.Fatalf("write active capture crash marker: %v", err)
	}
	if os.Getenv("AICLI_E2E_CRASH_WAIT") == "1" {
		time.Sleep(24 * time.Hour)
	}
	// Intentionally leave canonical SQLite open: the parent kills this process
	// after the transaction commits and marker is durable.
	os.Exit(0)
}

func runE2EMaterializedUnackedCrashChild(t *testing.T, ctx context.Context, workspace string) {
	t.Helper()
	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	if err != nil {
		t.Fatalf("open materialization crash session storage: %v", err)
	}

	session := chat.NewSession("e2e-materialization-crash-user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	appendE2ECrashTurn(session, "materialization-crash-turn", "materialization ack lost")
	receipt, err := storage.SaveWithTurnCapture(ctx, session, chat.TurnCaptureReceipt{
		SessionID: session.ID, TurnID: "materialization-crash-turn", Outcome: "success",
	})
	if err != nil {
		t.Fatalf("atomically save active turn and capture receipt: %v", err)
	}

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	if err != nil {
		t.Fatalf("open materialization crash memory store: %v", err)
	}
	_, err = store.MaterializeTurnCapture(ctx, memorystore.TurnCapture{
		ID: receipt.ID, ProjectKey: store.ProjectKey(), SessionID: receipt.SessionID,
		SessionGeneration: receipt.SessionGeneration, TurnID: receipt.TurnID,
		MessageSeqStart: receipt.MessageSeqStart, MessageSeqEnd: receipt.MessageSeqEnd,
		CanonicalRevision: receipt.CanonicalRevision, RangeHash: receipt.RangeHash,
		TraceID: receipt.TraceID, UserMessageID: receipt.UserMessageID,
		AssistantMessageID: receipt.AssistantMessageID, Outcome: receipt.Outcome,
		Steps: receipt.Steps, UsageJSON: receipt.UsageJSON, CreatedAt: receipt.CreatedAt,
	})
	if err != nil {
		t.Fatalf("commit memory-side digest/job materialization: %v", err)
	}
	if receipt.State != "captured" {
		t.Fatalf("unexpected canonical receipt state before ack: %q", receipt.State)
	}

	raw, err := json.Marshal(e2eCrashMarker{
		SessionID: session.ID, ReceiptIDs: []string{receipt.ID},
	})
	if err != nil {
		t.Fatalf("marshal materialization crash marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "crash-marker.json"), raw, 0o644); err != nil {
		t.Fatalf("write materialization crash marker: %v", err)
	}
	if os.Getenv("AICLI_E2E_CRASH_WAIT") == "1" {
		time.Sleep(24 * time.Hour)
	}
	// The memory transaction is committed while the canonical receipt remains
	// captured. The parent hard-kills this process before any ack call.
	os.Exit(0)
}

func runE2ECaptureWorkerCrashChild(t *testing.T, ctx context.Context, workspace, stage string) {
	t.Helper()
	switch stage {
	case "worker_claimed", "worker_committed":
	default:
		t.Fatalf("unsupported capture worker crash stage %q", stage)
	}

	sessionDir := filepath.Join(workspace, ".aicli", "sessions")
	sessionCfg := chat.DefaultPersistentSessionStorageConfig(sessionDir)
	sessionCfg.Path = filepath.Join(sessionDir, "sessions.sqlite")
	sessionCfg.ImportLegacyJSON = false
	storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
	if err != nil {
		t.Fatalf("open capture worker crash session storage: %v", err)
	}

	session := chat.NewSession("e2e-capture-worker-crash-user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	appendE2ECrashTurn(session, "capture-worker-crash-turn", "capture worker crash boundary")
	receipt, err := storage.SaveWithTurnCapture(ctx, session, chat.TurnCaptureReceipt{
		SessionID: session.ID, TurnID: "capture-worker-crash-turn", Outcome: "success",
	})
	if err != nil {
		t.Fatalf("atomically save active turn and capture receipt: %v", err)
	}

	memoryCfg := memorystore.Config{
		ProjectRoot: workspace, Root: filepath.Join(workspace, ".aicli", "memory"), Mode: "on",
	}
	store, err := memorystore.New(memoryCfg)
	if err != nil {
		t.Fatalf("open capture worker crash memory store: %v", err)
	}
	capture := memorystore.TurnCapture{
		ID: receipt.ID, ProjectKey: store.ProjectKey(), SessionID: receipt.SessionID,
		SessionGeneration: receipt.SessionGeneration, TurnID: receipt.TurnID,
		MessageSeqStart: receipt.MessageSeqStart, MessageSeqEnd: receipt.MessageSeqEnd,
		CanonicalRevision: receipt.CanonicalRevision, RangeHash: receipt.RangeHash,
		TraceID: receipt.TraceID, UserMessageID: receipt.UserMessageID,
		AssistantMessageID: receipt.AssistantMessageID, Outcome: receipt.Outcome,
		Steps: receipt.Steps, UsageJSON: receipt.UsageJSON, CreatedAt: receipt.CreatedAt,
	}
	materialization, err := store.MaterializeTurnCapture(ctx, capture)
	if err != nil {
		t.Fatalf("commit memory-side digest/job materialization: %v", err)
	}
	if err := storage.MarkTurnCaptureReceiptMaterialized(ctx, receipt.ID); err != nil {
		t.Fatalf("acknowledge canonical receipt materialization: %v", err)
	}

	jobID := materialization.JobID
	if stage == "worker_claimed" {
		claimed, err := store.ClaimJobs(ctx, memorystore.ClaimJobOptions{
			WorkerID: "e2e-capture-worker-crash", Kind: "phase1_extract/v2",
			JobKey: materialization.JobKey, ProjectKey: store.ProjectKey(),
			Limit: 1, Now: time.Now().UTC(), Lease: 45 * time.Second,
		})
		if err != nil {
			t.Fatalf("claim capture worker job: %v", err)
		}
		if len(claimed) != 1 || claimed[0].ID != jobID {
			t.Fatalf("unexpected claimed capture worker jobs: %+v", claimed)
		}
	} else {
		manager := chat.NewSessionManager(storage, nil)
		runtime := New(store, &ChatSessionSource{Manager: manager}, &e2eTranscriptExtractor{}, fakeModes{}, Config{
			Mode: "shadow", Workspace: workspace, ProjectKey: store.ProjectKey(),
			MaxSourceMessages: 8, MaxCandidatesPerSession: 10, MaxCandidateTextRunes: 4000,
			TurnDigestEnabled: true, TurnDigestStoreText: true,
		})
		result, err := runtime.RunPendingTurnCaptureReceipts(ctx, TriggerSessionEnd)
		if err != nil {
			t.Fatalf("commit capture worker extraction: %v", err)
		}
		if result.Processed != 1 || result.Candidates != 1 {
			t.Fatalf("unexpected capture worker commit result: %+v", result)
		}
	}

	digest, err := store.GetTurnDigestByCaptureReceipt(ctx, receipt.ID)
	if err != nil {
		t.Fatalf("read capture worker crash digest: %v", err)
	}
	raw, err := json.Marshal(e2eCrashMarker{
		SessionID: session.ID, ReceiptIDs: []string{receipt.ID},
		DigestID: digest.ID, JobID: jobID, SessionGeneration: session.SessionGeneration,
		Stage: stage,
	})
	if err != nil {
		t.Fatalf("marshal capture worker crash marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "crash-marker.json"), raw, 0o644); err != nil {
		t.Fatalf("write capture worker crash marker: %v", err)
	}
	if os.Getenv("AICLI_E2E_CRASH_WAIT") == "1" {
		// Keep both SQLite handles open until the parent hard-kills the worker.
		time.Sleep(24 * time.Hour)
	}
	os.Exit(0)
}

func appendE2ECrashTurn(session *chat.Session, turnID, text string) {
	userMessage := types.NewUserMessage("user: " + text)
	userMessage.Metadata.Set(types.MetadataKeyTurnID, turnID)
	assistantMessage := types.NewAssistantMessage("assistant: " + text)
	assistantMessage.Metadata.Set(types.MetadataKeyTurnID, turnID)
	session.AddMessage(*userMessage)
	session.AddMessage(*assistantMessage)
}
