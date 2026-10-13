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

// TestScheduleJobRunsEndpoint 验证 J3 的运行历史 HTTP 端点：
// 创建 headless 作业 → 落一条 run → GET /schedule/runs 返回它。
func TestScheduleJobRunsEndpoint(t *testing.T) {
	handler, router := newScheduleTestHandler(t)

	body := `{"session_id":"sess-runs","title":"t","prompt":"p","after_seconds":600,"goal":"做检查","target_kind":"headless"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var created struct {
		Record schedule.Record `json:"record"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.Record.ID)

	// daemon 在真实执行时才写 run；这里直接落一条验证读取链路。
	store, ok := handler.getScheduleService().Store().(*schedule.SQLiteStore)
	require.True(t, ok)
	now := time.Now().UTC()
	require.NoError(t, store.AppendJobRun(schedule.JobRun{
		ScheduleID: created.Record.ID,
		Status:     schedule.JobRunDone,
		Output:     "ok",
		StartedAt:  now,
		FinishedAt: now,
	}))

	// 创建者会话可读（headless 作业无绑定会话，归属走 CreatedBySessionID）。
	req = httptest.NewRequest(http.MethodGet,
		"/api/runtime/schedule/runs?id="+created.Record.ID+"&session_id=sess-runs", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload struct {
		ID    string            `json:"id"`
		Count int               `json:"count"`
		Runs  []schedule.JobRun `json:"runs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, created.Record.ID, payload.ID)
	require.Equal(t, 1, payload.Count)
	require.Equal(t, schedule.JobRunDone, payload.Runs[0].Status)
	require.Equal(t, "ok", payload.Runs[0].Output)

	// 非归属会话读不到（404）。
	req = httptest.NewRequest(http.MethodGet,
		"/api/runtime/schedule/runs?id="+created.Record.ID+"&session_id=someone-else", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
