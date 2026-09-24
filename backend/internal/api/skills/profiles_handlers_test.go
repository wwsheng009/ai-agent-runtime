package skills

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// Batch 8 断言测试（Profiles 设置页 API）：
//   - 创建（模板/复制）→ 列表 → 详情 → 更新（含 mtime 冲突与校验失败零副作用）
//   - 删除的 default 引用门控（A10：被 default 引用时默认拒绝，force 才清理）
//   - rename 的目录 + 配置同改
//   - 未落地能力必须显式 501（from_session / apply，指向 Batch 13），不允许假成功

type profilesAPIHarness struct {
	router    http.Handler
	handler   *Handler
	root      string // profiles.root（测试专用临时目录）
	config    string // 宿主 config.yaml（测试专用临时文件）
	layerBase string // 显式 root 的父目录
}

func newProfilesAPIHarness(t *testing.T) *profilesAPIHarness {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "profiles")
	require.NoError(t, os.MkdirAll(root, 0o755))
	configPath := filepath.Join(tmp, "config.yaml")

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAICLIConfig(&agentconfig.Config{ConfigFilePath: configPath})
	handler.SetProfileSupport(ProfileSupportConfig{Registry: profilesys.NewRegistry(root)})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	return &profilesAPIHarness{router: router, handler: handler, root: root, config: configPath, layerBase: root}
}

// do 发起一次 API 调用（回环来源，写端点因此通过 authorizeProfileWrite）。
func (h *profilesAPIHarness) do(t *testing.T, method, path string, body interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
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
	h.router.ServeHTTP(rec, req)

	payload := map[string]interface{}{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	}
	return rec, payload
}

func (h *profilesAPIHarness) profileRoot(name string) string {
	return filepath.Join(h.layerBase, name)
}

// TestSetAICLIConfigKeepsHostConfigFilePathAndProfiles 钉住配置快照必须保留
// 「宿主配置事实」（ConfigFilePath / Profiles）：cloneAICLIRoutingConfig 曾经
// 只拷 aicli 节，导致 profiles 写端点把 default 写进搜索路径里"碰巧找到"的
// 另一个文件、且 default/items 变更在快照里丢失（2026-09-24 实锤）。
func TestSetAICLIConfigKeepsHostConfigFilePathAndProfiles(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry:       profilesys.NewRegistry(""),
		DefaultProfile: "wired-default",
	})
	source := &agentconfig.Config{
		ConfigFilePath: filepath.Join(t.TempDir(), "config.yaml"),
		Profiles: &agentconfig.ProfilesConfig{
			Root:           filepath.Join(t.TempDir(), "profiles"),
			DefaultProfile: "from-config",
			Items:          map[string]agentconfig.ProfileConfig{"from-config": {Root: "/tmp/from-config"}},
		},
	}
	handler.SetAICLIConfig(source)

	snapshot := handler.aicliConfigSnapshot()
	require.NotNil(t, snapshot)
	assert.Equal(t, source.ConfigFilePath, snapshot.ConfigFilePath, "宿主配置路径必须随快照保留")
	require.NotNil(t, snapshot.Profiles, "profiles 节必须随快照保留")
	assert.Equal(t, "from-config", snapshot.Profiles.DefaultProfile)
	assert.Equal(t, "/tmp/from-config", snapshot.Profiles.Items["from-config"].Root)
	assert.NotSame(t, source.Profiles, snapshot.Profiles, "profiles 节必须深拷贝，不能与源配置共享指针")

	// 深拷贝：改快照不得污染源配置（热重载与写回路径共享 map 会互相影响）。
	snapshot.Profiles.Items["from-config"] = agentconfig.ProfileConfig{Root: "/tmp/mutated"}
	snapshot.Profiles.DefaultProfile = "mutated"
	assert.Equal(t, "/tmp/from-config", source.Profiles.Items["from-config"].Root)
	assert.Equal(t, "from-config", source.Profiles.DefaultProfile)
}

func TestRuntimeProfilesAPI_CreateTemplateListGetUpdateDelete(t *testing.T) {
	h := newProfilesAPIHarness(t)
	root := h.profileRoot("batch8-life")
	profileFile := filepath.Join(root, "profile.yaml")

	// 创建（模板模式 + 设为默认）
	rec, payload := h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":        "batch8-life",
		"template":    "coding",
		"root":        root,
		"set_default": true,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, true, payload["created"])
	assert.Equal(t, "template", payload["mode"])
	assert.Equal(t, true, payload["default_profile_set"])
	assert.Equal(t, "new_sessions_only", payload["affects"])
	assert.FileExists(t, profileFile)

	// 列表：新 profile 可见、valid、标记为 default
	rec, payload = h.do(t, http.MethodGet, "/api/runtime/profiles", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	entries, ok := payload["profiles"].([]interface{})
	require.True(t, ok, "profiles 字段缺失：%v", payload)
	var found map[string]interface{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]interface{})
		if entry["name"] == "batch8-life" {
			found = entry
			break
		}
	}
	require.NotNil(t, found, "列表里没有 batch8-life：%v", payload)
	assert.Equal(t, true, found["valid"], "条目应可解析：%v", found)
	assert.Equal(t, true, found["is_default"])
	assert.Equal(t, "batch8-life", payload["default_profile"])

	// 详情
	rec, view := h.do(t, http.MethodGet, "/api/runtime/profiles/batch8-life", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "batch8-life", view["name"])
	assert.Equal(t, root, view["path"])
	assert.Equal(t, true, view["valid"], "%v", view)

	// 更新：携带正确 mtime → 写入成功
	original, err := os.ReadFile(profileFile)
	require.NoError(t, err)
	mtime := profileFileMtime(profileFile)
	updated := string(original) + "\n# updated by batch8 test\n"
	rec, payload = h.do(t, http.MethodPut, "/api/runtime/profiles/batch8-life", map[string]interface{}{
		"yaml":           updated,
		"expected_mtime": mtime,
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, payload["updated"], "%v", payload)
	onDisk, err := os.ReadFile(profileFile)
	require.NoError(t, err)
	assert.Equal(t, updated, string(onDisk))

	// 更新：携带过期 mtime → 409，且磁盘内容不变
	rec, payload = h.do(t, http.MethodPut, "/api/runtime/profiles/batch8-life", map[string]interface{}{
		"yaml":           updated + "# stale\n",
		"expected_mtime": mtime,
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, false, payload["updated"])
	assert.Equal(t, mtime, payload["expected_mtime"])
	onDisk, err = os.ReadFile(profileFile)
	require.NoError(t, err)
	assert.Equal(t, updated, string(onDisk), "冲突时必须零写入")

	// 更新：校验失败 → 400 且零副作用
	rec, payload = h.do(t, http.MethodPut, "/api/runtime/profiles/batch8-life", map[string]interface{}{
		"yaml": "name: [unclosed\n",
	})
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.NotEmpty(t, payload["error"], "校验失败必须带回错误信息：%v", payload)
	onDisk, err = os.ReadFile(profileFile)
	require.NoError(t, err)
	assert.Equal(t, updated, string(onDisk), "校验失败时必须零写入")

	// 删除：被 default 引用 → 409（A10 门控）
	rec, payload = h.do(t, http.MethodDelete, "/api/runtime/profiles/batch8-life", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, false, payload["deleted"])
	refs, ok := payload["references"].(map[string]interface{})
	require.True(t, ok, "references 缺失：%v", payload)
	assert.Equal(t, true, refs["is_default"])
	assert.DirExists(t, root)

	// 删除：force=true → 清 default 并硬删
	rec, payload = h.do(t, http.MethodDelete, "/api/runtime/profiles/batch8-life?force=true", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, payload["deleted"])
	assert.Equal(t, true, payload["default_cleared"])
	assert.Equal(t, false, payload["recoverable"])
	assert.NoDirExists(t, root)

	rec, _ = h.do(t, http.MethodGet, "/api/runtime/profiles/batch8-life", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// 配置里不应残留 default_profile 指向
	configRaw, err := os.ReadFile(h.config)
	require.NoError(t, err)
	assert.NotContains(t, string(configRaw), "default_profile: batch8-life")
}

func TestRuntimeProfilesAPI_DuplicateRenameAndConflicts(t *testing.T) {
	h := newProfilesAPIHarness(t)
	srcRoot := h.profileRoot("batch8-src")
	copyRoot := h.profileRoot("batch8-copy")

	rec, _ := h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":     "batch8-src",
		"template": "coding",
		"root":     srcRoot,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// 复制
	rec, payload := h.do(t, http.MethodPost, "/api/runtime/profiles/batch8-src/duplicate", map[string]interface{}{
		"name": "batch8-copy",
		"root": copyRoot,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "duplicate", payload["mode"])
	assert.Equal(t, "batch8-src", payload["from_ref"])
	assert.FileExists(t, filepath.Join(copyRoot, "profile.yaml"))

	// 目标已存在且非空 → 409（不静默混入）
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles/batch8-src/duplicate", map[string]interface{}{
		"name": "batch8-copy",
		"root": copyRoot,
	})
	assert.Equal(t, http.StatusConflict, rec.Code)

	// 引用检查（只读）：文件清单可见，default 未指向它
	rec, payload = h.do(t, http.MethodGet, "/api/runtime/profiles/batch8-copy/references", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, payload["is_default"])
	files, _ := payload["files"].([]interface{})
	assert.NotEmpty(t, files, "文件清单不应为空：%v", payload)

	// 改名：目录 + 引用一起改
	rec, payload = h.do(t, http.MethodPost, "/api/runtime/profiles/batch8-copy/rename", map[string]interface{}{
		"new_name": "batch8-renamed",
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, payload["renamed"])
	assert.Equal(t, "batch8-copy", payload["old_ref"])
	assert.Equal(t, "batch8-renamed", payload["new_ref"])
	assert.NoDirExists(t, copyRoot)
	assert.DirExists(t, h.profileRoot("batch8-renamed"))

	rec, _ = h.do(t, http.MethodGet, "/api/runtime/profiles/batch8-renamed", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec, _ = h.do(t, http.MethodGet, "/api/runtime/profiles/batch8-copy", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// 改名到已存在的名字 → 409
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles/batch8-renamed/rename", map[string]interface{}{
		"new_name": "batch8-src",
	})
	assert.Equal(t, http.StatusConflict, rec.Code)

	// 非法名字 → 400（与 CLI 同一套校验：ValidateProfileName）
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles/batch8-src/rename", map[string]interface{}{
		"new_name": "bad name",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name": "..",
		"root": h.profileRoot("nope"),
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRuntimeProfilesAPI_SetDefaultAndNotImplementedBoundaries(t *testing.T) {
	h := newProfilesAPIHarness(t)
	root := h.profileRoot("batch8-default")

	rec, _ := h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":     "batch8-default",
		"template": "coding",
		"root":     root,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// 设为默认：只影响新会话（D26）
	rec, payload := h.do(t, http.MethodPost, "/api/runtime/profiles/batch8-default/default", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "batch8-default", payload["default_profile"])
	assert.Equal(t, "new_sessions_only", payload["affects"])
	assert.NotEmpty(t, payload["current_session_note"])
	// 写目标必须是宿主配置路径（cloneAICLIRoutingConfig 漏拷 ConfigFilePath 时
	// 这里会指向搜索路径里"碰巧找到"的另一个文件——2026-09-24 测试实锤过）。
	assert.Equal(t, h.config, payload["config_path"])

	// from_session（D24 差分固化）属 Batch 13：显式 501，不允许假成功
	rec, payload = h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":         "batch8-from-session",
		"from_session": "sess-123",
		"root":         h.profileRoot("batch8-from-session"),
	})
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	assert.Contains(t, payload["error"], "Batch 13")
	assert.NoDirExists(t, h.profileRoot("batch8-from-session"))

	// apply 属 Batch 13：同样显式 501
	rec, payload = h.do(t, http.MethodPost, "/api/runtime/profiles/batch8-default/apply", nil)
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	assert.Contains(t, payload["error"], "Batch 13")

	// template 与 from_ref 互斥
	rec, _ = h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":     "batch8-both",
		"template": "coding",
		"from_ref": "batch8-default",
		"root":     h.profileRoot("batch8-both"),
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 非回环来源的写请求 → 403（M4 门控）
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/profiles", strings.NewReader(`{"name":"x"}`))
	req.RemoteAddr = "203.0.113.9:40000"
	rec2 := httptest.NewRecorder()
	h.router.ServeHTTP(rec2, req)
	assert.Equal(t, http.StatusForbidden, rec2.Code)
}
