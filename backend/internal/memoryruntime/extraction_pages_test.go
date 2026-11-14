package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestExtractionBytePagesCoverEveryMessageAndFragment(t *testing.T) {
	session := SessionRef{ID: "byte-session", WorkspacePath: "/workspace"}
	source := Transcript{SessionID: session.ID, Revision: 3, Messages: []TranscriptMessage{
		{ID: "one", Seq: 1, Role: "user", Content: strings.Repeat("甲", 120)},
		{ID: "two", Seq: 2, Role: "assistant", Content: strings.Repeat("bc ", 60)},
		{ID: "three", Seq: 3, Role: "user", Content: "tail must not be skipped"},
	}}
	cfg := Config{MaxSourceBytes: 260}
	pages, err := planExtractionPages(session, source, cfg)
	require.NoError(t, err)
	require.Greater(t, len(pages), 3)
	received := map[string]string{}
	for _, page := range pages {
		prompt, err := renderExtractionPrompt(session, page, cfg)
		require.NoError(t, err)
		require.LessOrEqual(t, len(prompt), cfg.MaxSourceBytes)
		require.True(t, utf8.ValidString(prompt))
		for _, message := range page.Messages {
			if message.FragmentTotal > 0 {
				require.Equal(t, len(received[message.ID]), message.FragmentOffset)
			}
			received[message.ID] += message.Content
		}
	}
	for _, message := range source.Messages {
		require.Equal(t, message.Content, received[message.ID])
	}
	_, err = renderExtractionPrompt(session, source, cfg)
	require.ErrorContains(t, err, "split before extraction", "direct extractor callers must not silently truncate")
}

func TestExtractionBytePagesRedactBeforeFragmentation(t *testing.T) {
	session := SessionRef{ID: "redact"}
	secret := "sk-" + strings.Repeat("a", 60)
	text := strings.Repeat("safe ", 40) + secret + strings.Repeat(" after ", 40)
	source := Transcript{Messages: []TranscriptMessage{{ID: "one", Seq: 1, Role: "user", Content: text}}}
	pages, err := planExtractionPages(session, source, Config{MaxSourceBytes: 230})
	require.NoError(t, err)
	var joined strings.Builder
	for _, page := range pages {
		for _, message := range page.Messages {
			joined.WriteString(message.Content)
		}
	}
	redacted, _ := RedactForPrompt(text)
	require.NotEqual(t, text, redacted)
	require.Equal(t, redacted, joined.String())
	require.NotContains(t, joined.String(), secret)
}

func TestExtractionBytePagesResumeOnlyUnfinishedWork(t *testing.T) {
	for _, messageLimit := range []int{2, 20} {
		t.Run(fmt.Sprintf("message_limit_%d", messageLimit), func(t *testing.T) {
			rt, source, store := newRecoveryRuntime(t, 4, messageLimit)
			rt.Config.MaxSourceBytes = 320
			for i := range source.messages {
				source.messages[i].Content = strings.Repeat(fmt.Sprintf("durable-%d ", i), 18)
			}
			_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
				SessionID: source.session.ID, TurnID: "all", Outcome: "success", MessageSeqEnd: 4,
			})
			require.NoError(t, err)
			calls := map[string]int{}
			failOnce := true
			rt.Extractor = recoveryExtractorFunc(func(_ context.Context, session SessionRef, page Transcript) (ExtractionResult, error) {
				prompt, err := renderExtractionPrompt(session, page, rt.Config)
				require.NoError(t, err)
				require.LessOrEqual(t, len(prompt), rt.Config.MaxSourceBytes)
				require.Len(t, page.Messages, 1)
				id := page.Messages[0].ID
				calls[id]++
				if id == "message-2" && failOnce {
					failOnce = false
					return ExtractionResult{}, errors.New("injected page failure")
				}
				return ExtractionResult{Candidates: []Candidate{{
					Kind: "fact", Text: "fact for " + id, Confidence: 0.9, SourceMessageIDs: []string{id},
				}}}, nil
			})
			result, err := rt.RunSessionEnd(context.Background(), source.session.ID)
			require.True(t, err != nil || result.Failed > 0)
			pending, err := store.ListPendingTurnDigests(context.Background(), source.session.ID, 10)
			require.NoError(t, err)
			require.Len(t, pending, 1, "unprocessed tail must remain pending")
			require.Equal(t, 1, calls["message-1"])
			require.Zero(t, calls["message-3"])
			now := rt.now().Add(time.Minute)
			rt.Clock = func() time.Time { return now }
			result, err = rt.RunSessionEnd(context.Background(), source.session.ID)
			require.NoError(t, err)
			require.Empty(t, result.Errors)
			pending, err = store.ListPendingTurnDigests(context.Background(), source.session.ID, 10)
			require.NoError(t, err)
			require.Empty(t, pending)
			require.Equal(t, map[string]int{"message-1": 1, "message-2": 2, "message-3": 1, "message-4": 1}, calls)
			records, err := store.ListRecords(context.Background(), memorystore.RecordFilter{Status: memorystore.StatusCandidate, Limit: 20})
			require.NoError(t, err)
			require.Len(t, records.Items, 4)
			for _, record := range records.Items {
				require.Equal(t, memorystore.FreshnessFresh, record.Freshness, "byte pages must not stale one another")
			}
		})
	}
}

func TestExtractionFragmentCancellationPersistsRetryWithoutAcknowledging(t *testing.T) {
	rt, source, store := newRecoveryRuntime(t, 1, 20)
	rt.Config.MaxSourceBytes = 260
	source.messages[0].Content = strings.Repeat("长文本", 100)
	_, err := store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		SessionID: source.session.ID, TurnID: "one-long-turn", Outcome: "success", MessageSeqEnd: 1,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	offsets := map[int]int{}
	failOnce := true
	rt.Extractor = recoveryExtractorFunc(func(ctx context.Context, _ SessionRef, page Transcript) (ExtractionResult, error) {
		message := page.Messages[0]
		offsets[message.FragmentOffset]++
		if message.FragmentOffset > 0 && failOnce {
			failOnce = false
			cancel()
			return ExtractionResult{}, ctx.Err()
		}
		return ExtractionResult{}, nil
	})
	_, err = rt.RunSessionEnd(ctx, source.session.ID)
	require.ErrorIs(t, err, context.Canceled)
	pending, err := store.ListPendingTurnDigests(context.Background(), source.session.ID, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a partial message has no canonical completion receipt")
	db := recoverySQL(t, store)
	var running, retrying int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM memory_jobs WHERE status='running'`).Scan(&running))
	require.Zero(t, running, "cancellation cleanup must not leave a leased running job")
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM memory_jobs WHERE status='pending'`).Scan(&retrying))
	require.Equal(t, 1, retrying)
	now := rt.now().Add(time.Minute)
	rt.Clock = func() time.Time { return now }
	result, err := rt.RunSessionEnd(context.Background(), source.session.ID)
	require.NoError(t, err)
	require.True(t, result.Completed)
	require.Equal(t, int64(1), result.SourceEndSeq)
	require.Equal(t, 1, offsets[0], "durably completed fragments must not call the model again")
	pending, err = store.ListPendingTurnDigests(context.Background(), source.session.ID, 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestExtractionRejectsImpossibleByteBudget(t *testing.T) {
	rt, source, _ := newRecoveryRuntime(t, 1, 20)
	rt.Config.MaxSourceBytes = 10
	calls := 0
	rt.Extractor = recoveryExtractorFunc(func(context.Context, SessionRef, Transcript) (ExtractionResult, error) {
		calls++
		return ExtractionResult{}, nil
	})
	result, err := rt.RunSession(context.Background(), TriggerManual, source.session.ID)
	require.Error(t, err)
	require.False(t, result.Completed)
	require.Zero(t, calls)
}
