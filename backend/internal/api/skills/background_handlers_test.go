package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestGetBackgroundJobIncludesRestartPolicy(t *testing.T) {
	tempDir := t.TempDir()
	logDir := filepath.Join(tempDir, "logs")
	storeDSN := "file:background-handler-test-" + time.Now().UTC().Format("150405.000000000") + "?mode=memory&cache=shared"

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	config := runtimecfg.DefaultRuntimeConfig()
	config.Background.StoreDSN = storeDSN
	config.Background.LogDir = logDir
	handler.SetRuntimeConfig(config, "")

	manager := handler.getBackgroundManager(config)
	require.NotNil(t, manager)

	job, err := manager.SubmitShell(context.Background(), "session-1", background.BackgroundTaskArgs{
		Command:       "echo ok",
		RestartPolicy: background.RestartPolicyRerun,
	})
	require.NoError(t, err)
	require.NotNil(t, job)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/background/jobs/"+job.ID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Job background.Job `json:"job"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, job.ID, resp.Job.ID)
	require.Equal(t, background.RestartPolicyRerun, resp.Job.RestartPolicy)
}
