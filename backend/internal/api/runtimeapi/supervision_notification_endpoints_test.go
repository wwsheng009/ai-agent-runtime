package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func TestResolveSupervisionNotificationUsesScopedCAS(t *testing.T) {
	handler, store := newAPISupervisionManualAuditFixture(t, "api-resolve-notification")
	notifications, err := store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     "root-session",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, notifications)
	record := notifications[0]

	body, err := json.Marshal(map[string]interface{}{
		"root_scope_id":        "root-session",
		"resolution":           "closed",
		"expected_version":     record.Version,
		"has_expected_version": true,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/runtime/supervision/notifications/"+record.NotificationID+"/resolve",
		bytes.NewReader(body),
	)
	req = mux.SetURLVars(req, map[string]string{"id": record.NotificationID})
	rec := httptest.NewRecorder()

	handler.ResolveSupervisionNotification(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updated, err := store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     "root-session",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, updated)
	require.Equal(t, supervision.ResolutionClosed, updated[0].ResolutionState)
}

func TestResolveSupervisionNotificationRejectsOutOfScope(t *testing.T) {
	handler, store := newAPISupervisionManualAuditFixture(t, "api-resolve-notification-scope")
	notifications, err := store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     "root-session",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, notifications)
	record := notifications[0]

	body, err := json.Marshal(map[string]interface{}{
		"root_scope_id":        "other-session",
		"resolution":           "closed",
		"expected_version":     record.Version,
		"has_expected_version": true,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/runtime/supervision/notifications/"+record.NotificationID+"/resolve",
		bytes.NewReader(body),
	)
	req = mux.SetURLVars(req, map[string]string{"id": record.NotificationID})
	rec := httptest.NewRecorder()

	handler.ResolveSupervisionNotification(rec, req)
	require.NotEqual(t, http.StatusOK, rec.Code)
}

func TestAcknowledgeSupervisionNotificationScopedWithoutVersion(t *testing.T) {
	handler, store := newAPISupervisionManualAuditFixture(t, "api-ack-notification")
	notifications, err := store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     "root-session",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, notifications)
	record := notifications[0]

	body, err := json.Marshal(map[string]interface{}{
		"root_scope_id": "root-session",
		"note":          "reviewed by operator",
	})
	require.NoError(t, err)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/runtime/supervision/notifications/"+record.NotificationID+"/ack",
		bytes.NewReader(body),
	)
	req = mux.SetURLVars(req, map[string]string{"id": record.NotificationID})
	rec := httptest.NewRecorder()

	handler.AcknowledgeSupervisionNotification(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updated, err := store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     "root-session",
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, updated)
	require.Equal(t, supervision.DecisionAcknowledged, updated[0].DecisionState)
}
