package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type fakeRuntimeServiceControl struct {
	status  *RuntimeServiceStatus
	restart *RuntimeServiceRestartResult
}

func (s *fakeRuntimeServiceControl) Status() (*RuntimeServiceStatus, error) {
	return s.status, nil
}

func (s *fakeRuntimeServiceControl) Restart() (*RuntimeServiceRestartResult, error) {
	return s.restart, nil
}

func TestGetRuntimeServiceStatus(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetServiceControlService(&fakeRuntimeServiceControl{
		status: &RuntimeServiceStatus{
			Running:          true,
			PID:              4321,
			PIDFile:          "E:/projects/ai/ai-agent-runtime/backend/logs/runtime-server.pid",
			RestartSupported: true,
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/service", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]RuntimeServiceStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.True(t, payload["service"].Running)
	require.Equal(t, 4321, payload["service"].PID)
}

func TestRestartRuntimeService(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetServiceControlService(&fakeRuntimeServiceControl{
		restart: &RuntimeServiceRestartResult{
			Accepted: true,
			Message:  "restart helper started",
		},
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/service/restart", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)

	var payload map[string]RuntimeServiceRestartResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.True(t, payload["restart"].Accepted)
}
