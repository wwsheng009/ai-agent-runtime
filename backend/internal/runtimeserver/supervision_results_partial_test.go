package runtimeserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// TestCompletionRecordCarriesKillSalvage pins the §2.4-1 mapping: the durable
// mailbox row written by the kill/cancel path maps into the result record's
// partial product and workspace fields, so read_agent_result can render them.
func TestCompletionRecordCarriesKillSalvage(t *testing.T) {
	message := team.MailMessage{
		Metadata: map[string]interface{}{
			"session_id":      "child-killed",
			"status":          "canceled",
			"success":         false,
			"partial_summary": "halfway deliverable before ESC",
			"partial_source":  "last_assistant_message",
			"partial_steps":   3,
			"isolation":       "worktree",
			"worktree_path":   "/tmp/wt/child-killed",
			"worktree_branch": "agent/child-killed",
		},
		CreatedAt: time.Now().UTC(),
	}

	record, ok := completionRecord(message)
	require.True(t, ok)
	require.NotNil(t, record.PartialProduct)
	require.Equal(t, "halfway deliverable before ESC", record.PartialProduct.Summary)
	require.Equal(t, "last_assistant_message", record.PartialProduct.Source)
	require.Equal(t, 3, record.PartialProduct.Steps)
	require.NotNil(t, record.Workspace)
	require.Equal(t, "worktree", record.Workspace.Isolation)
	require.Equal(t, "/tmp/wt/child-killed", record.Workspace.WorktreePath)
	require.Equal(t, "agent/child-killed", record.Workspace.Branch)

	// A plain completion payload maps without fabricating salvage.
	plain, ok := completionRecord(team.MailMessage{
		Metadata:  map[string]interface{}{"session_id": "child-ok", "status": "succeeded", "success": true},
		CreatedAt: time.Now().UTC(),
	})
	require.True(t, ok)
	require.Nil(t, plain.PartialProduct)
	require.Nil(t, plain.Workspace)

	// Steps-only salvage (no summary yet) is still recorded: progress is
	// information, even when the last message had nothing to clip.
	stepsOnly, ok := completionRecord(team.MailMessage{
		Metadata:  map[string]interface{}{"session_id": "child-steps", "status": "stopped", "partial_steps": 2},
		CreatedAt: time.Now().UTC(),
	})
	require.True(t, ok)
	require.NotNil(t, stepsOnly.PartialProduct)
	require.Equal(t, 2, stepsOnly.PartialProduct.Steps)
	require.Empty(t, stepsOnly.PartialProduct.Summary)
}
