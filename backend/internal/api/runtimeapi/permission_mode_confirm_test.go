package runtimeapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// §13 F1：runtime API 切 bypass 是提权动作，必须显式 confirm=true
// （复用 §5.4/I-2 config 层写入的确认语义）。缺确认时 400 且不改写会话模式；
// disable_bypass 生效时的 403 优先于本确认门（见 disable_bypass 测试）。

func TestUpdateSessionPermissionModeRequiresConfirmForBypass(t *testing.T) {
	isolatePermissionModeHome(t)
	ctx := context.Background()
	manager, router, session := newPermissionModeRouter(t)

	rec := postPermissionMode(t, router, session.ID, `{"mode":"bypass_permissions"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "confirm=true")
	require.Contains(t, rec.Body.String(), string(errors.ErrValidationFailed))

	stored, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, string(runtimepolicy.ModeDefault), sessionPermissionMode(stored),
		"缺确认的切换不得改写会话模式")

	// 正向对照：带确认仍可切换（门没有把正常路径关掉）。
	ok := postPermissionMode(t, router, session.ID, `{"mode":"bypass_permissions","confirm":true}`)
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())

	stored, err = manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, string(runtimepolicy.ModeBypassPermissions), sessionPermissionMode(stored))
}

func TestUpdateSessionPermissionModeConfirmNotRequiredForSafeModes(t *testing.T) {
	isolatePermissionModeHome(t)
	_, router, session := newPermissionModeRouter(t)

	rec := postPermissionMode(t, router, session.ID, `{"mode":"accept_edits"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
