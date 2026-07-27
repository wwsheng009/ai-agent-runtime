package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func TestAnalyticsHandlersListSummaryAndDetail(t *testing.T) {
	root := t.TempDir()
	sessionID := "20260727_080000.000_analytics1"
	day := time.Date(2026, 7, 27, 8, 0, 0, 0, time.Local)
	sessionDir := filepath.Join(root, "2026", "07", "27", sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	project := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(project, 0o755))

	chatBody := `{
  "session_id": "` + sessionID + `",
	"runtime_session_id": "runtime-session-analytics",
	"title": "Analyze runtime usage",
	"project_path": "` + filepath.ToSlash(project) + `",
  "start_time": "` + day.Format(time.RFC3339) + `",
  "status": "completed",
  "provider": "openai",
  "model": "gpt-5",
  "summary": {
    "total_requests": 2,
    "total_responses": 2,
    "total_tool_calls": 1,
    "total_tokens": 42,
    "total_duration_ms": 900
  }
}`
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "chat_"+sessionID+".json"), []byte(chatBody), 0o644))
	debugBody := `[2026-07-27 08:00:01.000] [llm-debug] request_finished trace_id=t1 step=1 success=true usage_prompt_tokens=20 usage_completion_tokens=10 usage_total_tokens=30
`
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "debug.log"), []byte(debugBody), 0o644))

	handler := newRuntimeLogTestHandler()
	handler.SetChatLogsDir(root)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/sessions?limit=10", nil)
	listReq.RemoteAddr = "127.0.0.1:4321"
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)

	var listPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listPayload))
	require.EqualValues(t, 1, listPayload["total"])
	require.Equal(t, "runtime.analytics.v1", listPayload["schema_version"])
	require.NotNil(t, listPayload["coverage"])
	sessions := listPayload["sessions"].([]interface{})
	require.Equal(t, filepath.Clean(project), sessions[0].(map[string]interface{})["project"])
	require.Equal(t, "Analyze runtime usage", sessions[0].(map[string]interface{})["title"])

	summaryReq := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/summary?group_by=provider", nil)
	summaryReq.RemoteAddr = "127.0.0.1:4321"
	summaryRec := httptest.NewRecorder()
	router.ServeHTTP(summaryRec, summaryReq)
	require.Equal(t, http.StatusOK, summaryRec.Code)

	var summaryPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(summaryRec.Body.Bytes(), &summaryPayload))
	require.Equal(t, "provider", summaryPayload["group_by"])
	require.EqualValues(t, 1, summaryPayload["matched"])

	projectReq := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/summary?group_by=project&project="+url.QueryEscape(project), nil)
	projectReq.RemoteAddr = "127.0.0.1:4321"
	projectRec := httptest.NewRecorder()
	router.ServeHTTP(projectRec, projectReq)
	require.Equal(t, http.StatusOK, projectRec.Code)
	var projectPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(projectRec.Body.Bytes(), &projectPayload))
	require.Equal(t, "project", projectPayload["group_by"])
	require.EqualValues(t, 1, projectPayload["matched"])

	dimensionsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/dimensions", nil)
	dimensionsReq.RemoteAddr = "127.0.0.1:4321"
	dimensionsRec := httptest.NewRecorder()
	router.ServeHTTP(dimensionsRec, dimensionsReq)
	require.Equal(t, http.StatusOK, dimensionsRec.Code)
	var dimensionsPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(dimensionsRec.Body.Bytes(), &dimensionsPayload))
	require.Contains(t, dimensionsPayload["projects"], filepath.Clean(project))

	detailReq := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/sessions/"+sessionID+"/usage", nil)
	detailReq.RemoteAddr = "127.0.0.1:4321"
	detailRec := httptest.NewRecorder()
	router.ServeHTTP(detailRec, detailReq)
	require.Equal(t, http.StatusOK, detailRec.Code)

	var detailPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(detailRec.Body.Bytes(), &detailPayload))
	require.EqualValues(t, 1, detailPayload["step_count"])
	require.Len(t, detailPayload["turns"], 1)

	turnsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/analytics/sessions/"+sessionID+"/turns", nil)
	turnsReq.RemoteAddr = "127.0.0.1:4321"
	turnsRec := httptest.NewRecorder()
	router.ServeHTTP(turnsRec, turnsReq)
	require.Equal(t, http.StatusOK, turnsRec.Code)
}

func TestParseAnalyticsQueryTreatsDateEndAsInclusiveDay(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?from=2026-07-26&to=2026-07-27&project=C%3A%5Cwork", nil)
	query, err := parseAnalyticsQuery(req)
	require.NoError(t, err)
	require.Equal(t, "2026-07-26", query.From.Format("2006-01-02"))
	require.Equal(t, "2026-07-28", query.To.Format("2006-01-02"))
	require.Equal(t, `C:\work`, query.Project)
}
