package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Fail any accidental full-history load or unbounded page request. The runtime
// must use metadata lookup and bounded canonical pages even for a single turn.
type boundedRecoveryStorage struct {
	*chat.SQLiteSessionStorage
	maxPage int
	pages   int
}

func (s *boundedRecoveryStorage) Load(context.Context, string) (*chat.Session, error) {
	return nil, errors.New("unbounded history load is forbidden in recovery")
}

func (s *boundedRecoveryStorage) GetMessagePage(ctx context.Context, id string, before, limit int) (*chat.SessionHistoryPage, error) {
	if limit <= 0 || limit > s.maxPage {
		return nil, fmt.Errorf("unbounded canonical page: %d", limit)
	}
	s.pages++
	return s.SQLiteSessionStorage.GetMessagePage(ctx, id, before, limit)
}

func TestRecoverySQLiteCoversEntireCanonicalRangeAcrossRestart(t *testing.T) {
	for _, singleTurn := range []bool{false, true} {
		for _, failMiddle := range []bool{false, true} {
			t.Run(fmt.Sprintf("single_turn=%t/retry=%t", singleTurn, failMiddle), func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				sessionCfg := chat.DefaultPersistentSessionStorageConfig(filepath.Join(root, "sessions"))
				sessionCfg.ImportLegacyJSON = false
				storage, err := chat.NewSQLiteSessionStorage(sessionCfg)
				require.NoError(t, err)
				session := chat.NewSession("coverage-user")
				session.SetContext(sessionmeta.WorkspacePath, root)
				for seq := 1; seq <= 12; seq++ {
					session.AddMessage(*types.NewAssistantMessage(fmt.Sprintf("canonical evidence %d", seq)))
				}
				session.State = chat.StateClosed
				require.NoError(t, storage.Save(ctx, session))
				memoryCfg := memorystore.Config{
					ProjectRoot: root, Root: filepath.Join(root, "memory"), Mode: "on",
				}
				store, err := memorystore.New(memoryCfg)
				require.NoError(t, err)
				endpoints := []int{4, 8, 12}
				if singleTurn {
					endpoints = []int{12}
				}
				for index, endpoint := range endpoints {
					_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
						SessionID: session.ID, SessionGeneration: 1, TurnID: fmt.Sprintf("turn-%d", index),
						MessageSeqEnd: int64(endpoint), Outcome: "success", AssistantText: "bounded digest",
					})
					require.NoError(t, err)
				}

				now := time.Now().UTC()
				calls := map[int]int{}
				var extracted []int
				failed := false
				extractor := recoveryExtractorFunc(func(_ context.Context, _ SessionRef, transcript Transcript) (ExtractionResult, error) {
					require.LessOrEqual(t, len(transcript.Messages), 4)
					start := transcript.Messages[0].Seq
					calls[start]++
					if failMiddle && start == 5 && !failed {
						failed = true
						return ExtractionResult{}, errors.New("temporary extractor failure")
					}
					out := ExtractionResult{}
					for _, message := range transcript.Messages {
						extracted = append(extracted, message.Seq)
						out.Candidates = append(out.Candidates, Candidate{
							Kind: "decision", Text: fmt.Sprintf("Canonical recovery decision %02d", message.Seq),
							Confidence: 0.9, SourceMessageIDs: []string{message.ID},
						})
					}
					return out, nil
				})
				wrapped := &boundedRecoveryStorage{SQLiteSessionStorage: storage, maxPage: 4}
				manager := chat.NewSessionManager(wrapped, nil)
				t.Cleanup(manager.Stop)
				source := &ChatSessionSource{Manager: manager}
				cfg := Config{
					Mode: "shadow", Workspace: root, ProjectKey: store.ProjectKey(),
					MaxSourceMessages: 4, TurnDigestEnabled: true,
					TurnDigestMaxTurnsPerBatch: 20,
				}
				rt := New(store, source, extractor, fakeModes{}, cfg)
				rt.Clock = func() time.Time { return now }
				page, err := source.ReadTranscriptWindow(ctx, SessionRef{ID: session.ID}, 1, 12, 4)
				require.NoError(t, err)
				require.Len(t, page.Messages, 4)
				require.Equal(t, 1, page.Messages[0].Seq, "bounded reader must return the head, not the tail")

				first, err := rt.RunPendingTurnDigests(ctx, TriggerStartup)
				require.NoError(t, err)
				pending, err := store.ListPendingTurnDigests(ctx, session.ID, 20)
				require.NoError(t, err)
				if failMiddle {
					require.Equal(t, 1, first.Failed)
					require.Equal(t, 1, first.Processed)
					require.Len(t, pending, len(endpoints), "no digest may be acknowledged before the entire batch completes")
				} else {
					require.Empty(t, first.Errors)
					require.Equal(t, 3, first.Processed)
					require.Empty(t, pending)
				}
				manager.Stop()
				require.NoError(t, store.Close())

				// Both canonical history and job/outbox state survive recreation.
				storage, err = chat.NewSQLiteSessionStorage(sessionCfg)
				require.NoError(t, err)
				wrapped = &boundedRecoveryStorage{SQLiteSessionStorage: storage, maxPage: 4}
				manager = chat.NewSessionManager(wrapped, nil)
				t.Cleanup(manager.Stop)
				store, err = memorystore.New(memoryCfg)
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				rt = New(store, &ChatSessionSource{Manager: manager}, extractor, fakeModes{}, cfg)
				rt.Clock = func() time.Time { return now }
				if failMiddle {
					backoff, err := rt.RunPendingTurnDigests(ctx, TriggerStartup)
					require.NoError(t, err)
					require.Zero(t, backoff.Processed)
					pending, err = store.ListPendingTurnDigests(ctx, session.ID, 20)
					require.NoError(t, err)
					require.Len(t, pending, len(endpoints))
				}
				rt.Clock = func() time.Time { return now.Add(time.Minute) }
				retry, err := rt.RunPendingTurnDigests(ctx, TriggerStartup)
				require.NoError(t, err)
				require.Empty(t, retry.Errors)
				require.Equal(t, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, extracted)
				require.Equal(t, 1, calls[1], "persisted first subwindow must not re-extract after restart")
				require.Equal(t, 1, calls[9])
				if failMiddle {
					require.Equal(t, 2, calls[5])
				} else {
					require.Equal(t, 1, calls[5])
				}
				pending, err = store.ListPendingTurnDigests(ctx, session.ID, 20)
				require.NoError(t, err)
				require.Empty(t, pending)
				records, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 50})
				require.NoError(t, err)
				require.Len(t, records.Items, 12)
				jobs, err := store.ListJobs(ctx, 20)
				require.NoError(t, err)
				require.Len(t, jobs, 3)
				for _, job := range jobs {
					require.Equal(t, memorystore.JobStatusSucceeded, job.Status)
					require.NotEmpty(t, job.LastSuccessWatermark)
				}
				again, err := rt.RunPendingTurnDigests(ctx, TriggerStartup)
				require.NoError(t, err)
				require.Zero(t, again.Processed)
			})
		}
	}
}
