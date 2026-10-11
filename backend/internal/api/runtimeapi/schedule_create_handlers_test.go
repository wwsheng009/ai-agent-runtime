package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// newScheduleTestHandler returns a handler whose schedule service is backed by
// an in-memory store, plus the router used to serve it. The runtime config
// enables the schedule subsystem so getScheduleService() resolves.
func newScheduleTestHandler(t *testing.T) (*Handler, *mux.Router) {
	t.Helper()
	handler := NewHandler(nil, nil, nil)
	path := filepath.Join(t.TempDir(), "schedule.sqlite")
	config := runtimecfg.DefaultRuntimeConfig()
	config.Schedule.Enabled = true
	config.Schedule.StorePath = path
	handler.SetRuntimeConfig(config, "")
	store, err := schedule.OpenStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	handler.scheduleService = schedule.NewService(store, schedule.SystemClock)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return handler, router
}

// TestCreateScheduleTask_ManualOrigin pins the manual creation entry: the task
// is persisted with origin=manual, binds the requested session, and the next
// catalog read surfaces the origin.
func TestCreateScheduleTask_ManualOrigin(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	body := `{"session_id":"sess-manual","title":"Manual ping","prompt":"created by the management surface","after_seconds":600}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		Record schedule.Record `json:"record"`
		Origin schedule.Origin `json:"origin"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Equal(t, schedule.OriginManual, created.Origin)
	require.Equal(t, "Manual ping", created.Record.Title)
	require.NotEmpty(t, created.Record.ID)

	// Catalog read: the origin survives a full store round-trip.
	req = httptest.NewRequest(http.MethodGet, "/api/runtime/schedule/catalog", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var catalog struct {
		Tasks []schedule.CatalogEntry `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &catalog))
	require.Len(t, catalog.Tasks, 1)
	require.Equal(t, "sess-manual", catalog.Tasks[0].SessionID)
	require.Equal(t, schedule.OriginManual, catalog.Tasks[0].Origin)
}

// TestCreateScheduleTask_ReusesServiceValidation pins that the HTTP entry
// applies the same validation pipeline as the model tool: zero or two
// selectors, empty prompt, and an at target in the past are all rejected with
// the service-side error code in the message.
func TestCreateScheduleTask_ReusesServiceValidation(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{
			name:    "zero selectors",
			body:    `{"session_id":"s","title":"t","prompt":"p"}`,
			wantMsg: "invalid_selector",
		},
		{
			name:    "two selectors",
			body:    `{"session_id":"s","title":"t","prompt":"p","after_seconds":60,"every_seconds":60}`,
			wantMsg: "invalid_selector",
		},
		{
			name:    "empty prompt",
			body:    `{"session_id":"s","title":"t","prompt":"   ","after_seconds":60}`,
			wantMsg: "invalid_prompt",
		},
		{
			name:    "at in the past",
			body:    `{"session_id":"s","title":"t","prompt":"p","at":"2000-01-01T00:00:00Z"}`,
			wantMsg: "not_future",
		},
		{
			name:    "missing session_id",
			body:    `{"title":"t","prompt":"p","after_seconds":60}`,
			wantMsg: "session_id is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), tc.wantMsg)
		})
	}
}
