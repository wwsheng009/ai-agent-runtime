package runtimeapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// §4.9 F2 fail-loud：disable_bypass 生效时，切换接口直接 403，而不是写入一个
// 求值期会被降级回 default 的 bypass 元数据。

func isolatePermissionModeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	// os.UserHomeDir 在 Windows 读 USERPROFILE、其它平台读 HOME；两个都指向
	// 临时目录，测试才不会读到开发机真实的 ~/.aicli/permissions.yaml。
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func writeWorkspacePermissions(t *testing.T, workspace, content string) {
	t.Helper()
	dir := filepath.Join(workspace, ".aicli")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "permissions.yaml"), []byte(content), 0o644))
}

func TestUpdateSessionPermissionModeRejectsBypassWhenDisableBypass(t *testing.T) {
	isolatePermissionModeHome(t)
	ctx := context.Background()
	manager, router, session := newPermissionModeRouter(t)

	workspace := t.TempDir()
	writeWorkspacePermissions(t, workspace, "version: 1\ndisable_bypass: true\n")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	require.NoError(t, manager.Update(ctx, session))

	rec := postPermissionMode(t, router, session.ID, `{"mode":"bypass_permissions"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "disable_bypass")
	require.Contains(t, rec.Body.String(), string(errors.ErrAgentPermission))

	stored, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, string(runtimepolicy.ModeDefault), sessionPermissionMode(stored),
		"被拒绝的切换不得改写会话模式")

	// 非 bypass 模式不受影响。
	accept := postPermissionMode(t, router, session.ID, `{"mode":"accept_edits"}`)
	require.Equal(t, http.StatusOK, accept.Code, accept.Body.String())

	// 用户层（~/.aicli/permissions.yaml）同样生效，且工作区为空也拦得住。
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	writeWorkspacePermissions(t, home, "version: 1\ndisable_bypass: true\n")
	_, routerBare, sessionBare := newPermissionModeRouter(t)
	recBare := postPermissionMode(t, routerBare, sessionBare.ID, `{"mode":"bypass_permissions"}`)
	require.Equal(t, http.StatusForbidden, recBare.Code, recBare.Body.String())
}

func TestUpdateSessionPermissionModeAllowsBypassWithoutDisableBypass(t *testing.T) {
	isolatePermissionModeHome(t)
	ctx := context.Background()
	manager, router, session := newPermissionModeRouter(t)

	workspace := t.TempDir()
	writeWorkspacePermissions(t, workspace, "version: 1\nrules:\n  - name: ask-writes\n    tools: [write]\n    decision: ask\n")
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	require.NoError(t, manager.Update(ctx, session))

	rec := postPermissionMode(t, router, session.ID, `{"mode":"bypass_permissions"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stored, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, string(runtimepolicy.ModeBypassPermissions), sessionPermissionMode(stored))

	// 只有 disable_bypass=false 的显式声明 + 坏文件都不应误拦。
	writeWorkspacePermissions(t, workspace, "version: 1\ndisable_bypass: false\n")
	require.False(t, sessionBypassDisabled(stored))

	writeWorkspacePermissions(t, workspace, "version: 1\ndisable_bypass: [broken\n")
	require.False(t, sessionBypassDisabled(stored),
		"解析失败的层与引擎语义一致：不参与求值，也不降级 bypass")
}
