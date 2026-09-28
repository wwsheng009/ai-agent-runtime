package supervision

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadResultCarriesKillSalvage is the 建议稿 §2.4-1 read-side guard: a
// killed/cancelled record's salvage (partial product) and its isolated
// workspace must reach the model, so the parent can rescue work instead of
// treating the run as empty.
func TestReadResultCarriesKillSalvage(t *testing.T) {
	record := AgentResultRecord{
		Source:    ResultSourceCompletionPayload,
		SessionID: "child-killed",
		Status:    "canceled",
		Summary:   "Subagent child-killed completed with status canceled.",
		PartialProduct: &AgentResultPartial{
			Summary: "halfway deliverable before ESC",
			Source:  "last_assistant_message",
			Steps:   3,
		},
		Workspace: &AgentResultWorkspace{
			Isolation:    "worktree",
			WorktreePath: "/tmp/wt/child-killed",
			Branch:       "agent/child-killed",
		},
	}

	payload := BuildReadResultPayload(record, ReadResultArgs{SessionID: "child-killed"})

	require.NotNil(t, payload.PartialProduct)
	require.Equal(t, "halfway deliverable before ESC", payload.PartialProduct.Summary)
	require.Equal(t, "last_assistant_message", payload.PartialProduct.Source)
	require.Equal(t, 3, payload.PartialProduct.Steps)
	require.NotNil(t, payload.Workspace)
	require.Equal(t, "worktree", payload.Workspace.Isolation)
	require.Equal(t, "/tmp/wt/child-killed", payload.Workspace.WorktreePath)
	require.Equal(t, "agent/child-killed", payload.Workspace.Branch)
	require.Contains(t, payload.Workspace.Hint, "apply_agent_worktree")
	// The salvage is a deliverable: the read must keep the §2.4-3 guidance
	// (read it, do not blindly re-dispatch).
	require.True(t, payload.ResultAvailable)
	require.True(t, payload.DoNotRetry)

	// The new fields must survive serialization under their named keys: the
	// model reads the JSON, not the Go struct.
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"partial_product"`)
	require.Contains(t, string(raw), `"workspace"`)
}

// TestReadResultKillSalvageShedsUnderBudget pins the budget order: the salvage
// is shed part by part (summary → source → steps → pointer) after the wrap-up
// and before the workspace pointer, usage and failure metadata.
func TestReadResultKillSalvageShedsUnderBudget(t *testing.T) {
	payload := &ReadResultPayload{
		PartialProduct: &ReadResultPartialProduct{Summary: "salvage", Source: "last_assistant_message", Steps: 2},
		Workspace:      &ReadResultWorkspace{WorktreePath: "/tmp/wt/x"},
	}
	require.True(t, dropTrailingReadResultEntry(payload)) // summary
	require.Empty(t, payload.PartialProduct.Summary)
	require.True(t, dropTrailingReadResultEntry(payload)) // source
	require.Empty(t, payload.PartialProduct.Source)
	require.True(t, dropTrailingReadResultEntry(payload)) // steps
	require.Zero(t, payload.PartialProduct.Steps)
	require.True(t, dropTrailingReadResultEntry(payload)) // pointer
	require.Nil(t, payload.PartialProduct)
	require.NotNil(t, payload.Workspace)
	require.True(t, dropTrailingReadResultEntry(payload)) // workspace
	require.Nil(t, payload.Workspace)
	require.False(t, dropTrailingReadResultEntry(payload))
}
