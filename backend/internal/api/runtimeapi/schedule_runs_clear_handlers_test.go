package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

func TestClearScheduleJobRunsEndpoint(t *testing.T) {
	handler, router := newScheduleTestHandler(t)

	body := `{"session_id":"sess-clear","title":"Cleanup target","prompt":"p","goal":"Clear the run history","after_seconds":600,"target_kind":"headless"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var created struct {
		Record schedule.Record `json:"record"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.Record.ID)

	store, ok := handler.getScheduleService().Store().(*schedule.SQLiteStore)
	require.True(t, ok)
	now := time.Now().UTC()
	for _, output := range []string{"first", "second"} {
		require.NoError(t, store.AppendJobRun(schedule.JobRun{
			ScheduleID: created.Record.ID,
			Status:     schedule.JobRunDone,
			Output:     output,
			StartedAt:  now,
			FinishedAt: now,
		}))
	}

	// An unrelated caller cannot clear the history, and the records remain.
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/runtime/schedule/runs/clear",
		strings.NewReader(`{"session_id":"someone-else","id":"`+created.Record.ID+`"}`),
	)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	runs, err := store.ListJobRuns(created.Record.ID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)

	// The owner clears only run rows; the schedule itself stays addressable.
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/runtime/schedule/runs/clear",
		strings.NewReader(`{"session_id":"sess-clear","id":"`+created.Record.ID+`"}`),
	)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var cleared struct {
		ID           string `json:"id"`
		DeletedCount int64  `json:"deletedCount"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cleared))
	require.Equal(t, created.Record.ID, cleared.ID)
	require.EqualValues(t, 2, cleared.DeletedCount)

	runs, err = store.ListJobRuns(created.Record.ID, 10)
	require.NoError(t, err)
	require.Empty(t, runs)
	_, exists, err := store.Get(created.Record.ID)
	require.NoError(t, err)
	require.True(t, exists)

	// Clearing is idempotent.
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/runtime/schedule/runs/clear",
		strings.NewReader(`{"session_id":"sess-clear","id":"`+created.Record.ID+`"}`),
	)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cleared))
	require.Zero(t, cleared.DeletedCount)
}

func TestClearAllScheduleJobRunsEndpoint(t *testing.T) {
	handler, router := newScheduleTestHandler(t)

	body := `{"session_id":"sess-clear-all","title":"Cleanup target","prompt":"p","goal":"Clear all run history","after_seconds":600,"target_kind":"headless"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var created struct {
		Record schedule.Record `json:"record"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.Record.ID)

	store, ok := handler.getScheduleService().Store().(*schedule.SQLiteStore)
	require.True(t, ok)
	now := time.Now().UTC()
	for _, scheduleID := range []string{created.Record.ID, "deleted-schedule"} {
		require.NoError(t, store.AppendJobRun(schedule.JobRun{
			ScheduleID: scheduleID,
			Status:     schedule.JobRunDone,
			Output:     "result",
			StartedAt:  now,
			FinishedAt: now,
		}))
	}

	req = httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/runs/clear-all", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var cleared struct {
		DeletedCount int64 `json:"deletedCount"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cleared))
	require.EqualValues(t, 2, cleared.DeletedCount)
	for _, scheduleID := range []string{created.Record.ID, "deleted-schedule"} {
		runs, err := store.ListJobRuns(scheduleID, 10)
		require.NoError(t, err)
		require.Empty(t, runs, "run history for %s should be cleared", scheduleID)
	}
	_, exists, err := store.Get(created.Record.ID)
	require.NoError(t, err)
	require.True(t, exists, "clearing run logs must preserve schedules")

	// Global clearing is idempotent too.
	req = httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/runs/clear-all", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cleared))
	require.Zero(t, cleared.DeletedCount)
}
