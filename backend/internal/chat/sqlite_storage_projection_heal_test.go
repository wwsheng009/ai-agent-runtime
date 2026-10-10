package chat

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestLoadHealsLegacyCapTruncatedProjection: projections persisted by the
// removed fixed caps kept only a tail window with no compaction summary. Load
// must rebuild the model-visible history from the canonical transcript so those
// sessions regain the request messages the cap dropped.
func TestLoadHealsLegacyCapTruncatedProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("legacy-heal-user")
	for index := 0; index < 30; index++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("message-%02d", index)))
	}
	require.NoError(t, store.Save(ctx, session))

	// Simulate a legacy cap-trimmed projection: keep only the newest five
	// prompt rows, without any compaction checkpoint.
	_, err := store.db.ExecContext(ctx, `
		DELETE FROM session_prompt_messages
		WHERE session_id = ? AND position <= (
			SELECT MAX(position) - 5 FROM session_prompt_messages WHERE session_id = ?
		)
	`, session.ID, session.ID)
	require.NoError(t, err)

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, 30, loaded.CanonicalMessageCount)
	require.Equal(t, 30, len(loaded.History),
		"legacy cap-truncated projection must be healed from canonical")
	require.Equal(t, "message-00", loaded.History[0].Content)
	require.Equal(t, "message-29", loaded.History[len(loaded.History)-1].Content)
}

// TestLoadKeepsCompactedProjectionWithoutHealing: a projection shorter than the
// canonical transcript is only a legacy cap artifact when it carries no
// compaction checkpoint. Model-driven compaction replacements must survive.
func TestLoadKeepsCompactedProjectionWithoutHealing(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("compacted-keep-user")
	for index := 0; index < 30; index++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("message-%02d", index)))
	}
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	summary := *types.NewUserMessage("compacted summary")
	summary.Metadata["context_stage"] = "compaction"
	loaded.ReplaceHistory([]types.Message{summary, *types.NewUserMessage("recent")})
	require.NoError(t, store.Update(ctx, loaded))

	after, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, after.History, 2, "a real compaction replacement must not be healed away")
	require.Equal(t, "compacted summary", after.History[0].Content)
	require.Equal(t, 30, after.CanonicalMessageCount)
}
