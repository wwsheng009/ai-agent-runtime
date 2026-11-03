package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

func withWebScheduleTestService(t *testing.T) *schedule.Service {
	t.Helper()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	service := schedule.NewService(store, schedule.SystemClock)
	processScheduleMu.Lock()
	previous := processScheduleService
	processScheduleService = service
	processScheduleMu.Unlock()
	t.Cleanup(func() {
		processScheduleMu.Lock()
		processScheduleService = previous
		processScheduleMu.Unlock()
	})
	withWebTestSession(t, newWebTestSession())
	return service
}

func scheduleRequest(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	HandleChatWebAPISchedule(rec, req)
	var payload map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), rec.Body.String())
	}
	return rec.Code, payload
}

func TestChatWebScheduleCatalogCreateDelete(t *testing.T) {
	withWebScheduleTestService(t)

	code, catalog := scheduleRequest(t, http.MethodGet, ChatWebAPISchedulePath+"/catalog", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, float64(0), catalog["count"])

	code, created := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/create",
		`{"session_id":"sess-web","title":"Web task","prompt":"ping","after_seconds":600}`)
	require.Equal(t, http.StatusOK, code, created)
	record, ok := created["record"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Web task", record["title"])
	require.Equal(t, "manual", created["origin"])
	id, ok := record["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, id)

	code, catalog = scheduleRequest(t, http.MethodGet, ChatWebAPISchedulePath+"/catalog", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, float64(1), catalog["count"])
	tasks, ok := catalog["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, tasks, 1)

	code, deleted := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/delete",
		`{"session_id":"sess-web","id":"`+id+`"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, deleted["deleted"])
}

func TestChatWebScheduleCreateReturnsValidationDetail(t *testing.T) {
	withWebScheduleTestService(t)

	code, payload := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/create",
		`{"session_id":"sess-web","title":"Web task","prompt":"ping","after_seconds":60,"every_seconds":60}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "invalid_selector", payload["code"])
	require.Contains(t, payload["error"], "invalid_selector")
	require.Contains(t, payload["error"], "exactly one")
}

func TestChatWebScheduleCreateHeadlessWithoutSessionID(t *testing.T) {
	withWebScheduleTestService(t)

	code, created := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/create",
		`{"title":"Nightly job","prompt":"run checks","goal":"numbering upkeep","target_kind":"headless","after_seconds":60}`)
	require.Equal(t, http.StatusOK, code, created)
	record := created["record"].(map[string]any)
	require.Equal(t, "Nightly job", record["title"])

	code, catalog := scheduleRequest(t, http.MethodGet, ChatWebAPISchedulePath+"/catalog", "")
	require.Equal(t, http.StatusOK, code)
	tasks := catalog["tasks"].([]any)
	require.Len(t, tasks, 1)
	task := tasks[0].(map[string]any)
	require.Equal(t, "headless", task["targetKind"])
	require.Equal(t, "", task["sessionId"])
}

func TestChatWebScheduleHeadlessCreatorCanManage(t *testing.T) {
	withWebScheduleTestService(t)

	code, created := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/create",
		`{"session_id":"creator-session","title":"Owned job","prompt":"run checks","goal":"owned lifecycle","target_kind":"headless","after_seconds":60}`)
	require.Equal(t, http.StatusOK, code, created)
	id := created["record"].(map[string]any)["id"].(string)

	code, catalog := scheduleRequest(t, http.MethodGet, ChatWebAPISchedulePath+"/catalog", "")
	require.Equal(t, http.StatusOK, code)
	task := catalog["tasks"].([]any)[0].(map[string]any)
	require.Equal(t, "", task["sessionId"])
	require.Equal(t, "creator-session", task["createdBySessionId"])

	code, paused := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/pause",
		`{"session_id":"creator-session","id":"`+id+`"}`)
	require.Equal(t, http.StatusOK, code, paused)
	require.Equal(t, true, paused["updated"])

	code, deleted := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/delete",
		`{"session_id":"creator-session","id":"`+id+`"}`)
	require.Equal(t, http.StatusOK, code, deleted)
	require.Equal(t, true, deleted["deleted"])
}

func TestChatWebScheduleRuns(t *testing.T) {
	service := withWebScheduleTestService(t)

	code, created := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/create",
		`{"session_id":"creator-session","title":"Runs job","prompt":"run","goal":"run history","target_kind":"headless","after_seconds":60}`)
	require.Equal(t, http.StatusOK, code, created)
	id := created["record"].(map[string]any)["id"].(string)
	store, ok := service.Store().(*schedule.SQLiteStore)
	require.True(t, ok)
	started := time.Now().UTC().Add(-time.Second)
	require.NoError(t, store.AppendJobRun(schedule.JobRun{
		ScheduleID: id,
		Status:     schedule.JobRunDone,
		Output:     "all checks passed",
		SessionID:  "temp-session",
		StartedAt:  started,
		FinishedAt: started.Add(time.Second),
	}))

	code, payload := scheduleRequest(t, http.MethodGet,
		ChatWebAPISchedulePath+"/runs?id="+id+"&session_id=creator-session&limit=20", "")
	require.Equal(t, http.StatusOK, code, payload)
	require.Equal(t, float64(1), payload["count"])
	runs := payload["runs"].([]any)
	require.Len(t, runs, 1)
	run := runs[0].(map[string]any)
	require.Equal(t, "done", run["status"])
	require.Equal(t, "all checks passed", run["output"])
}

func TestChatWebScheduleCreateWorkspaceRequiresTarget(t *testing.T) {
	withWebScheduleTestService(t)

	code, payload := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/create",
		`{"title":"Workspace job","prompt":"run checks","goal":"workspace upkeep","target_kind":"workspace","after_seconds":60}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, payload["error"], "target_workspace")
}

func TestChatWebScheduleUpdatePauseResume(t *testing.T) {
	withWebScheduleTestService(t)

	code, created := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/create",
		`{"session_id":"sess-web","title":"Web task","prompt":"ping","every_seconds":300}`)
	require.Equal(t, http.StatusOK, code, created)
	record := created["record"].(map[string]any)
	id := record["id"].(string)
	expected, err := json.Marshal(record)
	require.NoError(t, err)

	code, updated := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/update",
		`{"session_id":"sess-web","id":"`+id+`","expected":`+string(expected)+`,"title":"Updated","prompt":"ping 2","change":{"kind":"every","every_seconds":600}}`)
	require.Equal(t, http.StatusOK, code, updated)
	require.Equal(t, true, updated["updated"], updated)
	require.Equal(t, "active", updated["status"])
	updatedRecord := updated["record"].(map[string]any)
	require.Equal(t, "Updated", updatedRecord["title"])
	require.Equal(t, float64(600), updatedRecord["everySeconds"])

	code, paused := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/pause",
		`{"session_id":"sess-web","id":"`+id+`"}`)
	require.Equal(t, http.StatusOK, code, paused)
	require.Equal(t, true, paused["updated"])
	require.Equal(t, "paused", paused["status"])

	code, resumed := scheduleRequest(t, http.MethodPost, ChatWebAPISchedulePath+"/resume",
		`{"session_id":"sess-web","id":"`+id+`"}`)
	require.Equal(t, http.StatusOK, code, resumed)
	require.Equal(t, true, resumed["updated"])
	require.Equal(t, "active", resumed["status"])
}

func TestChatWebScheduleServiceUnavailableExplainsWhy(t *testing.T) {
	processScheduleMu.Lock()
	previous := processScheduleService
	processScheduleService = nil
	processScheduleMu.Unlock()
	t.Cleanup(func() {
		processScheduleMu.Lock()
		processScheduleService = previous
		processScheduleMu.Unlock()
	})
	withWebTestSession(t, newWebTestSession())

	code, payload := scheduleRequest(t, http.MethodGet, ChatWebAPISchedulePath+"/catalog", "")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, "schedule_disabled", payload["code"])
	require.Contains(t, payload["error"], "schedule.enabled")
}
