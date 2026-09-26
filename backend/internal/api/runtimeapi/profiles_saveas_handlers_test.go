package runtimeapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// seedSaveAsTestProfile 在层根里写一份最小可解析 profile（测试夹具）。
func seedSaveAsTestProfile(t *testing.T, root, yaml string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "profile.yaml"), []byte(yaml), 0o644))
}

// TestRuntimeProfilesAPI_SaveAsWiresSessionSurface 钉住 Batch 13 slice 6：
// `POST /profiles {from_session}` 从 501 转为真实执行——生效面来源是会话绑定
// profile 的**解析结果**（与 set_profile / buildSessionActor 同一路径），产物只含
// 与内置默认面的差分声明（D24/D35），且重开后复现同一生效面（E2E-2 的 server 半程）。
func TestRuntimeProfilesAPI_SaveAsWiresSessionSurface(t *testing.T) {
	ctx := context.Background()
	h := newProfilesAPIHarness(t)

	// 源 profile：deny 形态 + read_only + skills 收窄（"形态保持"的输入面）
	sourceRoot := h.profileRoot("batch13-saveas-src")
	seedSaveAsTestProfile(t, sourceRoot, strings.Join([]string{
		"profile:",
		"  name: batch13-saveas-src",
		"  default_agent: default",
		"agents:",
		"  default: {}",
		"tools:",
		"  denylist: [shell, write]",
		"  read_only: true",
		"skills:",
		"  denylist: [dangerous-skill]",
		"",
	}, "\n"))

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	h.handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(ctx, "user-saveas-source")
	require.NoError(t, err)
	session.Metadata.Context = map[string]interface{}{
		sessionmeta.ProfileRef:   "batch13-saveas-src",
		sessionmeta.ProfileAgent: "default",
	}
	require.NoError(t, sessionManager.Update(ctx, session))

	targetRoot := h.profileRoot("batch13-saveas-out")
	rec, payload := h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":         "batch13-saveas-out",
		"from_session": session.ID,
		"root":         targetRoot,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "save_as", payload["mode"])
	assert.Equal(t, session.ID, payload["from_session"])
	assert.Equal(t, "batch13-saveas-src", payload["baseline"])
	assert.Equal(t, "new_sessions_only", payload["affects"])

	surface, ok := payload["surface"].(map[string]interface{})
	require.True(t, ok, "surface 摘要缺失：%v", payload)
	assert.Equal(t, float64(2), surface["tools.denylist"])
	assert.Equal(t, true, surface["tools.read_only"])
	assert.Equal(t, float64(1), surface["skills.denylist"])
	omitted, ok := payload["omitted"].([]interface{})
	require.True(t, ok, "omitted 缺失（D35 的诚实边界）：%v", payload)
	assert.NotEmpty(t, omitted)

	// 写盘后的同一 validate（D28）：entry 必须可解析
	entry, ok := payload["profile"].(map[string]interface{})
	require.True(t, ok, "profile 视图缺失：%v", payload)
	assert.Equal(t, true, entry["valid"], "产物必须可解析：%v", entry)
	assert.FileExists(t, filepath.Join(targetRoot, "profile.yaml"))

	// 产物只含差分声明：deny 形态不被转写为 allowlist，prompt 不落盘（仅注释提示）
	raw, err := os.ReadFile(filepath.Join(targetRoot, "profile.yaml"))
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, "denylist:")
	assert.Contains(t, text, "read_only: true")
	assert.NotContains(t, text, "allowlist:")
	assert.NotContains(t, text, "prompt:")

	// 重开复现（E2E-2 的 server 半程）：解析产物得到的生效面与固化时一致
	state, err := h.handler.resolveProfileSessionState("batch13-saveas-out", "default", "")
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NotNil(t, state.ToolPolicy)
	assert.True(t, state.ToolPolicy.ReadOnly)
	assert.True(t, state.ToolPolicy.DeniedTools["shell"])
	assert.True(t, state.ToolPolicy.DeniedTools["write"])
	assert.False(t, state.ToolPolicy.AllowlistEnabled)
	assert.Equal(t, []string{"dangerous-skill"}, state.Resolved.Skills.Denylist)
}

// TestRuntimeProfilesAPI_SaveAsRejectsNoDiffAndOverwrite 守住三条纪律：A9（无差分
// 报错且零落盘）、互斥（from_session 不与 template/from_ref 混用）、不覆盖
// （force 与既有目录都拒绝——半覆盖比拒绝更糟，D35）。
func TestRuntimeProfilesAPI_SaveAsRejectsNoDiffAndOverwrite(t *testing.T) {
	ctx := context.Background()
	h := newProfilesAPIHarness(t)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	h.handler.SetSessionManager(sessionManager)

	// A9：未绑定 profile 的会话 ⇒ 生效面等于内置默认面 ⇒ 400 且不产目录
	unbound, err := sessionManager.Create(ctx, "user-saveas-unbound")
	require.NoError(t, err)
	require.NoError(t, sessionManager.Update(ctx, unbound))
	targetRoot := h.profileRoot("batch13-saveas-nodiff")
	rec, payload := h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":         "batch13-saveas-nodiff",
		"from_session": unbound.ID,
		"root":         targetRoot,
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, payload["error"], "无差异")
	assert.NoDirExists(t, targetRoot)

	// 互斥：from_session 与 template 混用必须 400（不静默挑一个）
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":         "batch13-saveas-mixed",
		"from_session": unbound.ID,
		"template":     "coding",
		"root":         h.profileRoot("batch13-saveas-mixed"),
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.NoDirExists(t, h.profileRoot("batch13-saveas-mixed"))

	// force 与 save-as 语义冲突：显式拒绝（不提供半覆盖）
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":         "batch13-saveas-force",
		"from_session": unbound.ID,
		"force":        true,
		"root":         h.profileRoot("batch13-saveas-force"),
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.NoDirExists(t, h.profileRoot("batch13-saveas-force"))

	// 既有目录：409 冲突且内容零改写
	existing := h.profileRoot("batch13-saveas-existing")
	seedSaveAsTestProfile(t, existing, "profile:\n  name: batch13-saveas-existing\nagents:\n  default: {}\n")
	before, err := os.ReadFile(filepath.Join(existing, "profile.yaml"))
	require.NoError(t, err)
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":         "batch13-saveas-existing",
		"from_session": unbound.ID,
		"root":         existing,
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	after, err := os.ReadFile(filepath.Join(existing, "profile.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "冲突时必须零写入")
}
