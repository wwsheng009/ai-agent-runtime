package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildSubagentCompletionMailboxMessageCarriesKillSalvage pins §2.4-1's
// transport link: the chat actor's interrupted/stalled terminal payload carries
// partial_summary / partial_steps and (for isolated children) worktree_path;
// the durable mailbox row must carry them too, otherwise read_agent_result can
// never show what a killed child had produced.
func TestBuildSubagentCompletionMailboxMessageCarriesKillSalvage(t *testing.T) {
	message := BuildSubagentCompletionMailboxMessage("parent-1", "child-1", "/root/child-1", "worker", "session.interrupted", map[string]interface{}{
		"status":          "stopped",
		"success":         false,
		"partial_summary": "halfway deliverable before ESC",
		"partial_source":  "last_assistant_message",
		"partial_steps":   3,
		"isolation":       "worktree",
		"worktree_path":   "/tmp/wt/child-1",
		"worktree_branch": "agent/child-1",
	})
	require.Equal(t, "halfway deliverable before ESC", message.Metadata["partial_summary"])
	require.Equal(t, "last_assistant_message", message.Metadata["partial_source"])
	require.Equal(t, 3, message.Metadata["partial_steps"])
	require.Equal(t, "worktree", message.Metadata["isolation"])
	require.Equal(t, "/tmp/wt/child-1", message.Metadata["worktree_path"])
	require.Equal(t, "agent/child-1", message.Metadata["worktree_branch"])

	// Absent keys stay absent: a normal completion must not fabricate salvage.
	plain := BuildSubagentCompletionMailboxMessage("parent-1", "child-1", "", "", "session.end", map[string]interface{}{
		"status":  "completed",
		"success": true,
	})
	require.NotContains(t, plain.Metadata, "partial_summary")
	require.NotContains(t, plain.Metadata, "worktree_path")
}
