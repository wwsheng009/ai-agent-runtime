package runtimeapi

// plan_review 控制器的单测（§16.5 第一步：web 适配只读面）。
// 归档分支走临时 planstore（经 planmode.ArchivePlan 造两轮快照）；
// session 分支用 InMemory 会话 + workspace 锚定文件。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func newPlanReviewTestHandler(t *testing.T) (*Handler, *chat.Session, string) {
	t.Helper()
	handler := NewHandler(nil, nil, nil)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "plan-review-user")
	require.NoError(t, err)
	workspace := t.TempDir()
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	require.NoError(t, sessionManager.Update(context.Background(), session))
	return handler, session, workspace
}

func TestPlanReviewController_SessionPlanReadsWorkspaceFile(t *testing.T) {
	handler, session, workspace := newPlanReviewTestHandler(t)
	planPath := filepath.Join(workspace, "plan.md")
	require.NoError(t, os.WriteFile(planPath, []byte("# plan\n- step 1"), 0o644))

	controller := handler.planReviewControllerForSession()
	require.NotNil(t, controller)
	result, err := controller.ReviewPlan(context.Background(), session.ID, toolbroker.PlanReviewArgs{})
	require.NoError(t, err)
	require.Equal(t, "session", result.Source)
	require.Contains(t, result.Content, "step 1")
	require.NotEmpty(t, result.VerdictOptions)
	require.NotEmpty(t, result.Hint)
	// 无 planmode 状态 → 非活跃。
	require.False(t, result.Active)
	// session plan 不支持跨轮 diff。
	_, err = controller.ReviewPlan(context.Background(), session.ID, toolbroker.PlanReviewArgs{CompareVersion: 1})
	require.Error(t, err)
}

func TestPlanReviewController_RelativePathEscapesWorkspaceRejected(t *testing.T) {
	handler, session, _ := newPlanReviewTestHandler(t)
	controller := handler.planReviewControllerForSession()
	// 用 filepath.Join 生成平台原生的「上一级」相对路径。原先硬编码
	// Windows 字面量 `..\outside.md`：在 Linux 上反斜杠不是路径分隔符，
	// 该字符串只是一个普通文件名，不构成越界，测试因此没等到期望错误。
	escapePath := filepath.Join("..", "outside.md")
	_, err := controller.ReviewPlan(context.Background(), session.ID, toolbroker.PlanReviewArgs{PlanPath: escapePath})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes the session workspace")
}

func TestPlanReviewController_ArchivePlanWithDiff(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	storeRoot := t.TempDir()
	handler.plansStore = planstore.NewStore(storeRoot)
	workspace := t.TempDir()
	planPath := filepath.Join(workspace, "p.md")

	require.NoError(t, os.WriteFile(planPath, []byte("v1 body"), 0o644))
	recV1, err := planmode.ArchivePlan(context.Background(), planmode.ArchiveOptions{
		Store:     handler.plansStore,
		SessionID: "probe",
		Workspace: workspace,
		PlanPath:  planPath,
		Decision:  "approve",
		Source:    "user",
		Content:   []byte("v1 body"),
		Title:     "probe",
	})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(planPath, []byte("v2 body extended"), 0o644))
	recV2, err := planmode.ArchivePlan(context.Background(), planmode.ArchiveOptions{
		Store:     handler.plansStore,
		SessionID: "probe",
		Workspace: workspace,
		PlanPath:  planPath,
		Decision:  "approve",
		Source:    "user",
		Content:   []byte("v2 body extended"),
		Title:     "probe",
	})
	require.NoError(t, err)

	controller := handler.planReviewControllerForSession()
	result, err := controller.ReviewPlan(context.Background(), "any-session", toolbroker.PlanReviewArgs{
		PlanID:         recV2.ID,
		Version:        recV2.Version,
		CompareVersion: recV1.Version,
	})
	require.NoError(t, err)
	require.Equal(t, "archive", result.Source)
	require.Equal(t, recV2.ID, result.PlanID)
	require.Contains(t, result.Content, "v2 body extended")
	require.NotNil(t, result.Diff)
	require.Equal(t, recV1.Version, result.Diff.FromVersion)
	require.Equal(t, recV2.Version, result.Diff.ToVersion)
	require.Contains(t, result.Diff.Text, "v2 body extended")
}

func TestPlanModeStateDefaultsInactiveOnManagedSession(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "user-1")
	require.NoError(t, err)
	state := planmode.Load(session)
	require.False(t, planmode.IsActive(state))
}
