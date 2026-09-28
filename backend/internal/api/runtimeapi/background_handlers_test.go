package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

func TestBackgroundJobControlEndpoints(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	storeDSN := "file:background-control-test-" + uuid.NewString() + "?mode=memory&cache=shared"

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	config := runtimecfg.DefaultRuntimeConfig()
	config.Background.StoreDSN = storeDSN
	config.Background.LogDir = filepath.Join(tempDir, "logs")
	config.Background.MaxConcurrentJobs = 1
	handler.SetRuntimeConfig(config, "")

	manager := handler.getBackgroundManager(config)
	require.NotNil(t, manager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	// Occupy the single slot so the second job stays queued and controllable.
	blocker, err := manager.SubmitShell(ctx, "session-control", background.BackgroundTaskArgs{
		Command: controlTestDelayCommand(3),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		job, getErr := manager.GetJob(ctx, blocker.ID)
		return getErr == nil && job != nil && job.Status == background.StatusRunning
	}, 20*time.Second, 100*time.Millisecond)

	queued, err := manager.SubmitShell(ctx, "session-control", background.BackgroundTaskArgs{
		Command: "echo queued",
	})
	require.NoError(t, err)

	post := func(id, action string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/background/jobs/"+id+"/"+action, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	decodeJob := func(rec *httptest.ResponseRecorder) background.Job {
		var resp struct {
			Job background.Job `json:"job"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		return resp.Job
	}

	rec := post(queued.ID, "pause")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, background.StatusPaused, decodeJob(rec).Status)
	require.Equal(t, http.StatusOK, post(queued.ID, "pause").Code, "pause is idempotent")

	rec = post(queued.ID, "resume")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, background.StatusPending, decodeJob(rec).Status)

	rec = post(queued.ID, "abandon")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, background.StatusAbandoned, decodeJob(rec).Status)
	require.Equal(t, http.StatusConflict, post(queued.ID, "resume").Code)

	rec = post(queued.ID, "requeue")
	require.Equal(t, http.StatusOK, rec.Code)
	var requeueResp struct {
		Job          background.Job `json:"job"`
		RequeuedFrom string         `json:"requeued_from"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&requeueResp))
	require.Equal(t, queued.ID, requeueResp.RequeuedFrom)
	require.NotEqual(t, queued.ID, requeueResp.Job.ID)

	// Running jobs are cancel-only: pause / abandon / requeue all conflict.
	require.Equal(t, http.StatusConflict, post(blocker.ID, "pause").Code)
	require.Equal(t, http.StatusConflict, post(blocker.ID, "abandon").Code)
	require.Equal(t, http.StatusConflict, post(blocker.ID, "requeue").Code)
	require.Equal(t, http.StatusNotFound, post("job_missing", "pause").Code)

	_, _ = manager.CancelJob(ctx, blocker.ID)
}

func controlTestDelayCommand(seconds int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`powershell -NoProfile -Command "Start-Sleep -Seconds %d"`, seconds)
	}
	return fmt.Sprintf("sleep %d", seconds)
}
