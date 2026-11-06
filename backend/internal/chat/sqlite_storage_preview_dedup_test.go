package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// preview_json is a listing fast path, not a second copy of the canonical
// message. Small messages must store NULL so new sessions do not double every
// payload; messages above the bounded preview size still keep a truncated
// preview for large-page rendering.
func TestSQLiteStorageOmitsRedundantPreviewJSON(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("preview-dedup-user")
	small := types.NewUserMessage("small canonical message")
	large := types.NewUserMessage(strings.Repeat("x", maxCanonicalPreviewBytes+4096))
	session.AddMessage(*small)
	session.AddMessage(*large)
	require.NoError(t, store.Save(ctx, session))

	type row struct {
		payload []byte
		preview []byte
	}
	var rows []row
	result, err := store.db.QueryContext(ctx, `
		SELECT payload_json, preview_json
		FROM session_messages
		WHERE session_id = ?
		ORDER BY seq ASC
	`, session.ID)
	require.NoError(t, err)
	defer result.Close()
	for result.Next() {
		var payload, preview []byte
		require.NoError(t, result.Scan(&payload, &preview))
		rows = append(rows, row{payload: payload, preview: preview})
	}
	require.NoError(t, result.Err())
	require.Len(t, rows, 2)
	require.NotEmpty(t, rows[0].payload)
	require.Nil(t, rows[0].preview, "small message must not duplicate payload_json")
	require.NotEmpty(t, rows[1].payload)
	require.NotEmpty(t, rows[1].preview, "oversized message keeps the bounded preview")
	require.Less(t, len(rows[1].preview), len(rows[1].payload))
	require.Contains(t, string(rows[1].preview), "session_storage_truncated")

	page, err := store.GetMessagePage(ctx, session.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Messages, 2)
	require.Equal(t, small.Content, page.Messages[0].Content)
}
