package runtimeapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// The non-streaming /api/agent/chat path used to skip active-turn registration,
// so an interrupt had no registry entry to cancel and fell through to the hub,
// where it collided with the web turn's own session lease (409). Registering the
// turn keeps interrupt on the registry path and lets it actually cancel.
func TestNonStreamActiveTurnIsRegisteredAndCancelable(t *testing.T) {
	handler := &Handler{}
	session := chat.NewSession("user-nonstream")
	session.ID = "session-nonstream"
	ctx := context.Background()

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	release := handler.getActiveTurnRegistry().beginCancelable(
		session.ID, "turn-nonstream", agentChatActiveTurnSource, false,
		func(string) bool {
			cancelRun()
			return true
		},
	)
	defer release()

	entry, ok := handler.getActiveTurnRegistry().get(session.ID)
	require.True(t, ok, "non-stream turn must be visible to /runtime")
	require.Equal(t, "turn-nonstream", entry.TurnID)
	require.False(t, entry.Detached, "non-stream turns cannot resume, so they are not detached")

	result := handler.getActiveTurnRegistry().cancel(session.ID, "turn-nonstream", activeTurnCancelSourceUserInterrupt)
	require.True(t, result.Cancelled)
	require.Equal(t, activeTurnCancelReasonCancelled, result.Reason)
	require.Error(t, runCtx.Err(), "cancel handle must cancel the run context")
}

// A spawn request must not hand the HTTP request context to the child run:
// once the response is written the request context is cancelled, which would
// kill every delegated child with "context canceled".
func TestSpawnDetachesRequestContext(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/s1/agents", strings.NewReader("{}")).
		WithContext(requestCtx)

	spawnCtx := context.WithoutCancel(req.Context())

	// Values survive; cancellation does not.
	cancelRequest()
	require.Error(t, requestCtx.Err(), "the request context is cancelled after the response")
	require.NoError(t, spawnCtx.Err(), "the detached spawn context must outlive the request")
}

// A deliberate interrupt must not be reported as a server error: clients use
// 5xx to trigger retry loops and error banners, so a user stop that returns 500
// looks like a crash. Cancelled turns map to 499 with a cancel_source marker.
func TestAgentChatErrorStatusMapsCancellation(t *testing.T) {
	require.Equal(t, http.StatusInternalServerError,
		agentChatErrorStatus(errors.New("boom"), false))
	require.Equal(t, http.StatusInternalServerError,
		agentChatErrorStatus(context.Canceled, false),
		"a cancellation the host did not attribute to an interrupt stays a failure")
	require.Equal(t, statusClientClosedRequest,
		agentChatErrorStatus(context.Canceled, true))
	require.Equal(t, http.StatusInternalServerError,
		agentChatErrorStatus(errors.New("boom"), true),
		"an interrupt flag must not mask an unrelated failure")
}

// The JSON error body for a cancelled turn carries cancel_source so clients can
// distinguish it from a failure without parsing prose.
func TestWriteAgentChatExecutionErrorMarksCancelledTurn(t *testing.T) {
	handler := &Handler{}
	rec := httptest.NewRecorder()

	handler.writeAgentChatExecutionError(
		context.Background(), rec, statusClientClosedRequest, context.Canceled, nil, "trace-cancel",
	)

	require.Equal(t, statusClientClosedRequest, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"cancel_source":"user_interrupt"`)
	require.Contains(t, body, `"trace_id":"trace-cancel"`)
}
