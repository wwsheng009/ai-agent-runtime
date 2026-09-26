package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestGetSupervisionSnapshot_ModeChildrenNarrowsSubtree pins the HTTP-level
// regression for the P0-A fix (plan §9 risk): before it, ?mode=children
// returned the whole subtree, so a parent asking for its direct children also
// received grandchildren. The narrowing rule lives in BuildSnapshot; this test
// keeps the handler path (query → Scope.Mode) and the rollup consistent, so
// neither host can quietly restore the old semantics.
func TestGetSupervisionSnapshot_ModeChildrenNarrowsSubtree(t *testing.T) {
	handler, _ := newAPISupervisionToolTestHandler(t, "api-snapshot-mode-children")
	handler.SetSupervisionDescendantProvider(&recordingAPIDescendantProvider{states: []supervision.DescendantState{
		{
			Kind:             supervision.SubjectAgentSession,
			ID:               "child-1",
			ParentPath:       []string{"parent-session"},
			ExecutionStatus:  "running",
			SupervisionState: supervision.SupervisionRunning,
		},
		{
			Kind:             supervision.SubjectAgentSession,
			ID:               "grandchild-1",
			ParentPath:       []string{"parent-session", "child-1"},
			ExecutionStatus:  "running",
			SupervisionState: supervision.SupervisionRunning,
		},
	}})

	read := func(mode string) ([]string, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.GetSupervisionSnapshot(rec, httptest.NewRequest(
			http.MethodGet,
			"/api/runtime/supervision/snapshot?root_session_id=parent-session&mode="+mode,
			nil,
		))
		require.Equal(t, http.StatusOK, rec.Code)
		var payload struct {
			Snapshot struct {
				Descendants []struct {
					ID string `json:"id"`
				} `json:"descendants"`
				Summary struct {
					Running int `json:"running"`
				} `json:"summary"`
			} `json:"snapshot"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		ids := make([]string, 0, len(payload.Snapshot.Descendants))
		for _, item := range payload.Snapshot.Descendants {
			ids = append(ids, item.ID)
		}
		return ids, payload.Snapshot.Summary.Running
	}

	children, childrenRunning := read(supervision.ScopeModeChildren)
	require.Equal(t, []string{"child-1"}, children, "mode=children keeps only direct descendants")
	require.Equal(t, 1, childrenRunning, "summary describes the rows the caller received")

	subtree, subtreeRunning := read(supervision.ScopeModeDescendants)
	require.ElementsMatch(t, []string{"child-1", "grandchild-1"}, subtree, "mode=descendants keeps the whole subtree")
	require.Equal(t, 2, subtreeRunning)
}
