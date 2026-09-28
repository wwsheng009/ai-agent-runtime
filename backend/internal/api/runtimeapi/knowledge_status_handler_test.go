package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// 未接线（mode=off / 启动期降级 / 未注入句柄）时端点必须 200 + mode=off：
// 状态面的语义是"知识层现在是什么状态"，404/503 只会迫使调用方去猜。
func TestGetKnowledgeStatus_OffWhenNotWired(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Handler{}).GetKnowledgeStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime/knowledge/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var report knowledge.StatusReport
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &report))
	require.Equal(t, knowledge.ModeOff, report.Mode)
	require.False(t, report.Enabled)
	require.Equal(t, knowledge.RoleNone, report.Role)
	require.Greater(t, report.GeneratedAt, int64(0))
}

// nil Handler 不得 panic（防御性路径：handler 尚未构造完就被探测）。
func TestGetKnowledgeStatus_NilHandler(t *testing.T) {
	recorder := httptest.NewRecorder()
	var handler *Handler
	handler.GetKnowledgeStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime/knowledge/status", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
}

// 注入 shadow 激活句柄后：端点报告 mode/role/workspace/db 与"未索引"降级原因。
func TestGetKnowledgeStatus_WithActivation(t *testing.T) {
	root := t.TempDir()
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeShadow
	activation, err := knowledge.Activate(context.Background(), cfg, root, knowledge.ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.NotNil(t, activation)
	t.Cleanup(func() { _ = activation.Close() })

	handler := &Handler{}
	handler.SetKnowledgeActivation(activation)
	recorder := httptest.NewRecorder()
	handler.GetKnowledgeStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime/knowledge/status", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var report knowledge.StatusReport
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &report))
	require.Equal(t, knowledge.ModeShadow, report.Mode)
	require.True(t, report.Enabled)
	require.Equal(t, knowledge.RoleOwner, report.Role)
	require.Equal(t, root, report.Workspace)
	require.NotEmpty(t, report.DBPath)
	require.Equal(t, "workspace is not indexed yet", report.DegradedReason)
	require.False(t, report.IndexRunning)
}

// SetKnowledgeActivation(nil) 必须把端点恢复为 off 载荷（可显式关闭）。
func TestSetKnowledgeActivation_NilResets(t *testing.T) {
	handler := &Handler{}
	handler.SetKnowledgeActivation(nil)
	report, err := handler.knowledgeStatusReport(context.Background())
	require.NoError(t, err)
	require.Equal(t, knowledge.ModeOff, report.Mode)
}
