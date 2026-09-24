package skills

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
)

// Batch 14 slice 5：D29 工作区信任的 UI 闭环后端半程（Q22）。
//
// 覆盖一条完整闭环：未信任 → 清单逐条标记 prompts 扣留 → 一键信任 →
// 同一清单标记消失（"信任后重载恢复"的 API 侧证据），以及"只支持 grant"的边界。
func TestHarnessTrustClosesProfileSuppressionLoop(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	t.Setenv(foldertrust.EnvFolderTrust, "1")

	workspace := t.TempDir()
	harnessTrustTestWriteProjectProfile(t, workspace, "demo")

	// 项目层 profile 落在 <workspace>/.aicli/profiles（D29 门控按
	// foldertrust.IsProjectScopedPath 判定项目归属，与进程 cwd 无关）；
	// 把宿主 profiles root 指到该目录，清单即以 root 来源发现它。
	handler, router := newHarnessTestRouter(t)
	handler.SetAICLIConfig(&agentconfig.Config{
		Profiles: &agentconfig.ProfilesConfig{Root: filepath.Join(workspace, ".aicli", "profiles")},
	})

	// 1) 未信任：读侧如实报告（server 永不弹提示 → 失败关闭）。
	trust := harnessTrustTestServe(t, router, http.MethodGet, "/api/runtime/harness/trust"+harnessWorkspaceQuery(workspace), nil)
	require.Equal(t, http.StatusOK, trust.Code, trust.Body.String())
	var before harnessTrustResponse
	require.NoError(t, json.NewDecoder(trust.Body).Decode(&before))
	require.True(t, before.FeatureEnabled)
	require.False(t, before.Trusted)
	require.NotEmpty(t, before.WorkspacePath)

	// 2) 未信任：清单标记项目层 profile 的 prompts 被扣留。
	listBefore := harnessTrustTestLoadProfiles(t, router, workspace)
	require.NotEmpty(t, listBefore.WorkspacePath)
	require.False(t, listBefore.WorkspaceTrusted)
	require.True(t, listBefore.WorkspaceTrustFeatureEnabled)
	entry := harnessTrustTestFindProfile(t, listBefore, "demo")
	require.True(t, entry.PromptSuppressed, "project profile prompts must be flagged as suppressed")
	require.NotEmpty(t, entry.PromptSuppressionReason)

	// 3) 一键信任（显式确认后的写动作；回环来源通过写授权）。
	grant := harnessTrustTestServe(t, router, http.MethodPost, "/api/runtime/harness/trust", map[string]any{
		"workspace_path": workspace,
		"action":         "grant",
	})
	require.Equal(t, http.StatusOK, grant.Code, grant.Body.String())
	var granted harnessTrustResponse
	require.NoError(t, json.NewDecoder(grant.Body).Decode(&granted))
	require.True(t, granted.Trusted)
	require.Equal(t, "grant", granted.Action)

	// 4) 授予后重载：同一清单不再标记扣留（信任即恢复，无需重启）。
	listAfter := harnessTrustTestLoadProfiles(t, router, workspace)
	require.True(t, listAfter.WorkspaceTrusted)
	require.False(t, harnessTrustTestFindProfile(t, listAfter, "demo").PromptSuppressed)

	// 5) 读侧与写侧同源：再 GET 一次仍是信任态（决策已持久化）。
	reread := harnessTrustTestServe(t, router, http.MethodGet, "/api/runtime/harness/trust"+harnessWorkspaceQuery(workspace), nil)
	var rereadResp harnessTrustResponse
	require.NoError(t, json.NewDecoder(reread.Body).Decode(&rereadResp))
	require.True(t, rereadResp.Trusted)
}

// TestHarnessTrustRejectsDestructiveAction 锁住"只支持 grant"的边界：
// 撤销信任会让项目级配置整体失效，UI 闭环不提供该动作（避免误触）。
func TestHarnessTrustRejectsDestructiveAction(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	t.Setenv(foldertrust.EnvFolderTrust, "1")

	workspace := t.TempDir()
	_, router := newHarnessTestRouter(t)

	rec := harnessTrustTestServe(t, router, http.MethodPost, "/api/runtime/harness/trust", map[string]any{
		"workspace_path": workspace,
		"action":         "untrust",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// harnessTrustTestServe 发起一次回环来源的请求（写端点因此通过
// authorizeProfileWrite；GET 侧与既有 harness 用例同形）。
func harnessTrustTestServe(t *testing.T, router *mux.Router, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "127.0.0.1:34567"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func harnessTrustTestLoadProfiles(t *testing.T, router *mux.Router, workspace string) runtimeProfileListResult {
	t.Helper()
	rec := harnessTrustTestServe(t, router, http.MethodGet,
		"/api/runtime/profiles?workspace="+url.QueryEscape(workspace), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list runtimeProfileListResult
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&list))
	return list
}

func harnessTrustTestFindProfile(t *testing.T, list runtimeProfileListResult, name string) runtimeProfileEntry {
	t.Helper()
	for _, entry := range list.Profiles {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("profile %q not found in %#v", name, list.Profiles)
	return runtimeProfileEntry{}
}

// harnessTrustTestWriteProjectProfile 在工作区里落一个带 prompts 的项目层 profile：
// prompts 是 D29 唯一被扣留的载体（tools/skills/MCP 声明照常生效）。
func harnessTrustTestWriteProjectProfile(t *testing.T, workspace, name string) {
	t.Helper()
	root := filepath.Join(workspace, ".aicli", "profiles", name)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "agents", "coder", "prompts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "profile.yaml"), []byte(`
profile:
  name: `+name+`
  description: demo profile
  default_agent: coder
prompts:
  mode: replace
`), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "agents", "coder", "prompts", "system.md"),
		[]byte("demo system prompt"), 0o644))
}

