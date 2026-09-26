package chat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func isolateChatPermissionsHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	// os.UserHomeDir 在 Windows 读 USERPROFILE、其它平台读 HOME。
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func writeChatWorkspacePermissions(t *testing.T, workspace, content string) {
	t.Helper()
	dir := filepath.Join(workspace, ".aicli")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "permissions.yaml"), []byte(content), 0o644))
}

// §4.9 F2：从 bypass 进入 plan 再退出时，若分层文件禁用 bypass，退出不能把
// bypass 还原回来（否则元数据/引擎不同步）。

func TestSessionActorExitPlanModeDowngradesBypassWhenDisabled(t *testing.T) {
	isolateChatPermissionsHome(t)
	actor, session, engine := newPlanModeTestActor(t, "plan-bypass-disabled-1", runtimepolicy.ModeBypassPermissions)
	ctx := context.Background()

	workspace := t.TempDir()
	writeChatWorkspacePermissions(t, workspace, "version: 1\ndisable_bypass: true\n")
	session.SetContext(planModeWorkspacePathKey, workspace)
	require.NoError(t, actor.sessionStore.Save(ctx, session))

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)
	require.Equal(t, runtimepolicy.ModePlan, engine.Mode)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{Decision: "quit"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, string(runtimepolicy.ModeDefault), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeDefault, engine.Mode)

	stored, err := actor.sessionStore.Load(ctx, actor.id)
	require.NoError(t, err)
	raw, ok := stored.GetContext(planModeEffectivePermissionModeKey)
	require.True(t, ok)
	assert.Equal(t, string(runtimepolicy.ModeDefault), raw)
}

// 对照组：没有 disable_bypass 时保持原语义（还原为 bypass）。
func TestSessionActorExitPlanModeKeepsBypassWithoutDisableBypass(t *testing.T) {
	isolateChatPermissionsHome(t)
	actor, session, engine := newPlanModeTestActor(t, "plan-bypass-kept-1", runtimepolicy.ModeBypassPermissions)
	ctx := context.Background()

	workspace := t.TempDir()
	writeChatWorkspacePermissions(t, workspace, "version: 1\nrules:\n  - tools: [write]\n    decision: ask\n")
	session.SetContext(planModeWorkspacePathKey, workspace)
	require.NoError(t, actor.sessionStore.Save(ctx, session))

	_, err := actor.EnterPlanMode(ctx, "", toolbroker.EnterPlanModeArgs{PlanPath: "plan.md"})
	require.NoError(t, err)

	result, err := actor.ExitPlanMode(ctx, "", toolbroker.ExitPlanModeArgs{Decision: "quit"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, string(runtimepolicy.ModeBypassPermissions), result.PermissionMode)
	assert.Equal(t, runtimepolicy.ModeBypassPermissions, engine.Mode)
}
