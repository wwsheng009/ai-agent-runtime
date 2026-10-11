package policy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveGrantPatternForWriteStdinIsJobScoped(t *testing.T) {
	assert.Equal(t, "job:job_a", DeriveGrantPattern("write_stdin", map[string]interface{}{"job_id": "job_a", "chars": "x\n"}))
	assert.Equal(t, "job:job_b", DeriveGrantPattern("task_input", map[string]interface{}{"task_id": "job_b"}))
	assert.Equal(t, "job:job_c", DeriveGrantPattern("write_stdin", map[string]interface{}{"session_id": "job_c"}))
	assert.Equal(t, "", DeriveGrantPattern("write_stdin", map[string]interface{}{"chars": "x\n"}))
}

func TestJobGrantPatternMatchesOnlySameJob(t *testing.T) {
	assert.True(t, grantPatternMatches(map[string]interface{}{"job_id": "job_a"}, "job:job_a", ""))
	assert.False(t, grantPatternMatches(map[string]interface{}{"job_id": "job_b"}, "job:job_a", ""))
	assert.False(t, grantPatternMatches(map[string]interface{}{}, "job:job_a", ""))
}

func TestWriteStdinGrantMustBeJobScoped(t *testing.T) {
	store := &MemoryGrantStore{}
	// A tool-wide write_stdin grant is refused: it would authorize every session.
	require.ErrorIs(t, store.Remember(Grant{Tool: "write_stdin"}), errDangerousGrant)
	// A job-scoped grant is accepted.
	require.NoError(t, store.Remember(Grant{Tool: "write_stdin", Pattern: "job:job_a", Scope: RememberScopeSession}))
}

type countingApprovalHandler struct {
	calls    int
	response ApprovalResponse
}

func (h *countingApprovalHandler) RequestApproval(_ context.Context, _ ApprovalRequest) (ApprovalResponse, error) {
	h.calls++
	return h.response, nil
}

func TestEngineWriteStdinSessionGrantIsJobScoped(t *testing.T) {
	handler := &countingApprovalHandler{response: ApprovalResponse{Allowed: true, RememberScope: RememberScopeSession}}
	store := &MemoryGrantStore{}
	engine := &Engine{
		Mode:       ModeDefault,
		AskHandler: handler,
		Grants:     store,
		Policy:     NewToolExecutionPolicy(nil, false),
	}
	ctx := context.Background()
	argsA := map[string]interface{}{"job_id": "job_a", "chars": "hi\n"}

	first, err := engine.Evaluate(ctx, EvalRequest{ToolName: "write_stdin", SessionID: "s1", Args: argsA})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, first.Type)
	require.Equal(t, 1, handler.calls, "the first write must ask for approval")

	second, err := engine.Evaluate(ctx, EvalRequest{ToolName: "write_stdin", SessionID: "s1", Args: argsA})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, second.Type)
	require.Equal(t, StageGrants, second.Stage, "the second write to the same job must be covered by the remembered grant")
	require.Equal(t, 1, handler.calls, "the second write must not ask again")

	_, err = engine.Evaluate(ctx, EvalRequest{
		ToolName:  "write_stdin",
		SessionID: "s1",
		Args:      map[string]interface{}{"job_id": "job_b", "chars": "yo\n"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, handler.calls, "a different job must ask again")
}
