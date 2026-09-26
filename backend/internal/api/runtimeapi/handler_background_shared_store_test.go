package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// 服务端在 sessionRuntime.defaultPersistence 保持 memory 的前提下，只配置显式
// background.storePath 时，必须从共享文件读到“另一个进程（aicli local）”写入的作业，
// 且列表/事件/输出三个只读端点都可见。这是 background 缺口修复后的线上配置形态：
// 显式 storePath 不级联 Team/AgentControl/Artifact 到共享目录。
func TestHandlerReadsExplicitSharedBackgroundStore(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "shared", "background.sqlite")
	logDir := filepath.Join(root, "shared", "background_logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir shared background log dir: %v", err)
	}
	logPath := filepath.Join(logDir, "job_external.log")
	if err := os.WriteFile(logPath, []byte("external background output\n"), 0o644); err != nil {
		t.Fatalf("write shared background log: %v", err)
	}

	// 外部进程写入后退出：共享文件是服务端唯一的可见性来源。
	external, err := background.NewSQLiteStore(&background.StoreConfig{Path: storePath})
	if err != nil {
		t.Fatalf("open shared background store: %v", err)
	}
	finishedAt := time.Now().UTC()
	exitCode := 0
	job := background.Job{
		ID:         "job_external",
		SessionID:  "session-external",
		Kind:       "shell",
		Command:    "echo external",
		Status:     background.StatusCompleted,
		Message:    "done",
		CreatedAt:  finishedAt.Add(-time.Second),
		FinishedAt: &finishedAt,
		ExitCode:   &exitCode,
		LogPath:    logPath,
		Metadata:   map[string]interface{}{"origin": "aicli-local"},
	}
	if err := external.SaveJob(context.Background(), job); err != nil {
		t.Fatalf("save shared background job: %v", err)
	}
	if err := external.AppendEvent(context.Background(), job.ID, "completed", map[string]interface{}{"origin": "aicli-local"}); err != nil {
		t.Fatalf("append shared background event: %v", err)
	}
	if err := external.Close(); err != nil {
		t.Fatalf("close shared background store: %v", err)
	}

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Background.StorePath = storePath
	cfg.Background.LogDir = logDir

	handler := NewHandler(nil, nil, nil)
	handler.SetRuntimeConfig(cfg, filepath.Join(root, "runtime.yaml"))
	defer closeHandlerPersistenceStoresForTest(handler)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/background/jobs?session_id=session-external", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected background job list status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Jobs  []background.Job `json:"jobs"`
		Count int              `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode background job list: %v", err)
	}
	if listResp.Count != 1 || len(listResp.Jobs) != 1 || listResp.Jobs[0].ID != job.ID {
		t.Fatalf("expected one shared background job, got %#v", listResp)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/background/jobs/"+job.ID+"/events", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected background events status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var eventsResp struct {
		Events []background.JobEvent `json:"events"`
		Count  int                   `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&eventsResp); err != nil {
		t.Fatalf("decode background events: %v", err)
	}
	if eventsResp.Count != 1 || len(eventsResp.Events) != 1 || eventsResp.Events[0].Type != "completed" {
		t.Fatalf("expected completed event from shared store, got %#v", eventsResp)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/background/jobs/"+job.ID+"/output", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected background output status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var outputResp struct {
		Output background.TaskOutputResult `json:"output"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&outputResp); err != nil {
		t.Fatalf("decode background output: %v", err)
	}
	if !strings.Contains(outputResp.Output.Output, "external background output") {
		t.Fatalf("expected output from shared log file, got %#v", outputResp.Output)
	}
}
