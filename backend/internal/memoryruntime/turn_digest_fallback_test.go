package memoryruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestOversizedDigestCoverageDoesNotFallBackToSessionExtraction(t *testing.T) {
	for _, entrypoint := range []string{"idle", "session_end"} {
		for _, status := range []memorystore.TurnDigestStatus{memorystore.TurnDigestDone, memorystore.TurnDigestSkipped} {
			t.Run(entrypoint+"/"+string(status), func(t *testing.T) {
				ctx := context.Background()
				transcript := validTranscript("s1", "latest-tail")
				transcript.Messages[0].Seq = 20
				transcript.Messages[1].Seq = 21
				source := &fakeSource{
					sessions: []SessionRef{{
						ID: "s1", WorkspacePath: "/workspace", State: "closed", CanonicalCount: 21,
					}},
					transcripts: map[string]Transcript{"s1": transcript},
				}
				extractor := &fakeExtractor{}
				rt, store := newRuntimeForTest(t, "shadow", source, extractor)
				rt.Config.TurnDigestEnabled = true
				_, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
					SessionID: "s1", TurnID: "covered", MessageSeqEnd: 21,
					Status: status, RollupStatus: memorystore.TurnDigestRollupSkipped,
				})
				require.NoError(t, err)
				var result RunResult
				if entrypoint == "idle" {
					result, err = rt.Run(ctx, TriggerIdle)
				} else {
					result, err = rt.RunSessionEnd(ctx, "s1")
				}
				require.NoError(t, err)
				require.Empty(t, result.Errors)
				require.Zero(t, extractor.calls, "terminal outbox coverage is not legacy history without an outbox")
			})
		}
	}
}
