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
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
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
	// 与生产 getScheduleService 同口径：store 一并挂到 handler（S2 交付账本用）。
	handler.scheduleStore = store
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
// the service-side error code as a structured response field.
func TestCreateScheduleTask_ReusesServiceValidation(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{
			name:     "zero selectors",
			body:     `{"session_id":"s","title":"t","prompt":"p"}`,
			wantCode: "invalid_selector",
		},
		{
			name:     "two selectors",
			body:     `{"session_id":"s","title":"t","prompt":"p","after_seconds":60,"every_seconds":60}`,
			wantCode: "invalid_selector",
		},
		{
			name:     "empty prompt",
			body:     `{"session_id":"s","title":"t","prompt":"   ","after_seconds":60}`,
			wantCode: "invalid_prompt",
		},
		{
			name:     "at in the past",
			body:     `{"session_id":"s","title":"t","prompt":"p","at":"2000-01-01T00:00:00Z"}`,
			wantCode: "not_future",
		},
		{
			name:     "missing session_id",
			body:     `{"title":"t","prompt":"p","after_seconds":60}`,
			wantCode: "invalid_selector",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			var response struct {
				Error   string                 `json:"error"`
				Code    string                 `json:"code"`
				Context map[string]interface{} `json:"context"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, tc.wantCode, response.Code)
			require.Equal(t, tc.wantCode, response.Context["schedule_code"])
			require.NotEmpty(t, response.Error)
			require.NotContains(t, response.Error, tc.wantCode+":")
		})
	}
}

// TestCreateScheduleTask_CarriesDriverAbsentNotice 钉住 S3（§4.1）：创建回执
// 携带 presence 快照；无调度者在岗时 view.notice 给出可操作警告（创建不受阻）。
func TestCreateScheduleTask_CarriesDriverAbsentNotice(t *testing.T) {
	handler, router := newScheduleTestHandler(t)
	handler.schedulePresenceOverride = func() schedulehost.Presence {
		return schedulehost.Presence{
			State:       schedulehost.PresenceAbsent,
			StoreSchema: schedule.SupportedSchemaVersion(),
		}
	}

	body := `{"session_id":"sess-notice","title":"Notice ping","prompt":"p","after_seconds":600}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		View     schedule.View         `json:"view"`
		Presence schedulehost.Presence `json:"presence"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.Equal(t, schedulehost.PresenceAbsent, created.Presence.State)
	require.Contains(t, created.View.Notice, "无调度者在岗")
}

func TestScheduleTaskPauseResumeEndpoints(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	body := `{"session_id":"sess-control","title":"Editable task","prompt":"ping","every_seconds":300}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		Record schedule.Record `json:"record"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	controlBody := `{"session_id":"sess-control","id":"` + created.Record.ID + `"}`
	for _, path := range []string{"/api/runtime/schedule/pause", "/api/runtime/schedule/resume"} {
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(controlBody))
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var result schedule.UpdateResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.True(t, result.Updated)
		if strings.HasSuffix(path, "/pause") {
			require.Equal(t, schedule.StatusPaused, result.Status)
		} else {
			require.Equal(t, schedule.StatusActive, result.Status)
		}
	}
}

// TestSessionlessHeadlessTaskManagementEndpoints 钉住全局管理面：headless 任务
// 没有创建者会话时，暂停/编辑/恢复/删除都只凭任务 id 即可完成。
func TestSessionlessHeadlessTaskManagementEndpoints(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	body := `{"title":"Nightly","prompt":"run checks","after_seconds":600,"goal":"keep healthy","target_kind":"headless","target_provider":"gateway","target_model":"model-a","target_reasoning_effort":"high"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		Record schedule.Record `json:"record"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.Record.ID)

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/schedule/catalog", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var catalog struct {
		Tasks []schedule.CatalogEntry `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &catalog))
	require.Len(t, catalog.Tasks, 1)
	require.Equal(t, "gateway", catalog.Tasks[0].TargetProvider)
	require.Equal(t, "model-a", catalog.Tasks[0].TargetModel)
	require.Equal(t, "high", catalog.Tasks[0].TargetReasoningEffort)

	idBody := func() string {
		encoded, err := json.Marshal(map[string]any{"id": created.Record.ID})
		require.NoError(t, err)
		return string(encoded)
	}
	for _, path := range []string{"/api/runtime/schedule/pause", "/api/runtime/schedule/resume"} {
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(idBody()))
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var result schedule.UpdateResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.True(t, result.Updated)
	}

	updatePayload, err := json.Marshal(map[string]any{
		"id":             created.Record.ID,
		"expected":       created.Record,
		"title":          "Nightly edited",
		"target_kind":    "headless",
		"target_profile": "scheduled",
	})
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/update", strings.NewReader(string(updatePayload)))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updated schedule.UpdateResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	require.True(t, updated.Updated)

	req = httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/delete", strings.NewReader(idBody()))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var deleted schedule.DeleteResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &deleted))
	require.True(t, deleted.Deleted)
}

// TestSessionTaskStillRequiresSessionAtHTTPBoundary 确认放宽只覆盖非会话目标。
func TestSessionTaskStillRequiresSessionAtHTTPBoundary(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	body := `{"session_id":"sess-owner","title":"Ping","prompt":"p","after_seconds":600}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		Record schedule.Record `json:"record"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	idBody, err := json.Marshal(map[string]any{"id": created.Record.ID})
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/pause", strings.NewReader(string(idBody)))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var result schedule.UpdateResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.False(t, result.Updated)
	require.Equal(t, schedule.CodeSessionRequired, result.Code)
}

// TestUpdateScheduleTask_FullJobFieldsAndAfterRule 钉住 HTTP 编辑面：
// goal/plan/执行上下文与 after 时间规则都通过同一 update 端点落库。
func TestUpdateScheduleTask_FullJobFieldsAndAfterRule(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	createBody := `{"session_id":"sess-edit","title":"Old title","prompt":"old prompt","every_seconds":300}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(createBody))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created struct {
		Record schedule.Record `json:"record"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	updatePayload, err := json.Marshal(map[string]any{
		"session_id":              "sess-edit",
		"id":                      created.Record.ID,
		"expected":                created.Record,
		"title":                   "Edited title",
		"prompt":                  "edited prompt",
		"goal":                    "edited goal",
		"plan":                    []string{"step one", "step two"},
		"target_kind":             "headless",
		"target_profile":          "scheduled",
		"target_provider":         "gateway",
		"target_model":            "model-b",
		"target_reasoning_effort": "medium",
		"change": map[string]any{
			"kind":          "after",
			"after_seconds": 120,
		},
	})
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/update", strings.NewReader(string(updatePayload)))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updated schedule.UpdateResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	require.True(t, updated.Updated)
	require.NotNil(t, updated.Record)
	require.Equal(t, schedule.KindAfter, updated.Record.Kind)
	require.Equal(t, int64(120), updated.Record.AfterSeconds)
	require.Equal(t, "edited goal", updated.Record.Goal)

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/schedule/catalog", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var catalog struct {
		Tasks []schedule.CatalogEntry `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &catalog))
	require.Len(t, catalog.Tasks, 1)
	entry := catalog.Tasks[0]
	require.Equal(t, "", entry.SessionID)
	require.Equal(t, "sess-edit", entry.CreatedBySessionID)
	require.Equal(t, schedule.TargetHeadless, entry.TargetKind)
	require.Equal(t, "scheduled", entry.TargetProfile)
	require.Equal(t, []string{"step one", "step two"}, entry.Plan)
	require.Equal(t, "gateway", entry.TargetProvider)
	require.Equal(t, "model-b", entry.TargetModel)
	require.Equal(t, "medium", entry.TargetReasoningEffort)
}
