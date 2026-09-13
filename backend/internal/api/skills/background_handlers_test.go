package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestGetBackgroundJobIncludesRestartPolicy(t *testing.T) {
	tempDir := t.TempDir()
	logDir := filepath.Join(tempDir, "logs")
	// 同一 tick 内的两次测试曾得到同一个 DSN（时间格式化的小数位不代表
	// 真实时钟分辨率），改用 uuid 保证每个 handler 独占一个内存库。
	storeDSN := "file:background-handler-test-" + uuid.NewString() + "?mode=memory&cache=shared"

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
