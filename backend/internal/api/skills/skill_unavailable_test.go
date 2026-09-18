package skills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// skillUnavailableMCPManager 只暴露 allowed 中的工具，用于稳定制造
// "工具缺失 → 技能 unavailable" 的场景。
type skillUnavailableMCPManager struct {
	allowed map[string]struct{}
}

func newSkillUnavailableMCPManager(allowed ...string) *skillUnavailableMCPManager {
	manager := &skillUnavailableMCPManager{allowed: make(map[string]struct{}, len(allowed))}
	for _, name := range allowed {
		manager.allowed[name] = struct{}{}
	}
	return manager
}

func (m *skillUnavailableMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	if _, ok := m.allowed[toolName]; ok {
		return skill.ToolInfo{Name: toolName, Description: toolName, Enabled: true}, nil
	}
	return skill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
}

func (m *skillUnavailableMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("tool %s is not available", toolName)
}

func (m *skillUnavailableMCPManager) ListTools() []skill.ToolInfo {
	out := make([]skill.ToolInfo, 0, len(m.allowed))
	for name := range m.allowed {
		out = append(out, skill.ToolInfo{Name: name, Description: name, Enabled: true})
	}
	return out
}

type unavailableSkillContractResponse struct {
	Skills           []map[string]interface{} `json:"skills"`
	Count            int                      `json:"count"`
	Unavailable      []unavailableSkillEntry  `json:"unavailable"`
	UnavailableCount int                      `json:"unavailable_count"`
	AvailableCount   int                      `json:"available_count"`
	TotalSkills      int                      `json:"total_skills"`
	Counts           map[string]int           `json:"counts"`
}

type unavailableSkillEntry struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Scope        string   `json:"scope"`
	MissingTools []string `json:"missing_tools"`
	Reason       string   `json:"reason"`
	Message      string   `json:"message"`
}

// newUnavailableSkillTestRegistry 构造一个只含 unavailable 记录的注册表：
// run_shell 依赖的 bash/view 不在 surface 中，fetch_page 依赖的 fetch 可用。
func newUnavailableSkillTestRegistry(t *testing.T) *skill.Registry {
	t.Helper()

	mcp := newSkillUnavailableMCPManager("fetch")
	registry := skill.NewRegistry(mcp)

	// 通过真实 loader 注册路径产生记录，避免手工调用 RecordUnavailable 绕过
	// 被测逻辑：fetch_page 正常注册，run_shell 因工具缺失被软跳过并登记。
	loader := skill.NewLoader(mcp)
	dir := t.TempDir()
	writeUnavailableSkillManifest(t, dir, "fetch_page", "fetch")
	writeUnavailableSkillManifest(t, dir, "run_shell", "bash", "view")
	require.NoError(t, loader.LoadAllWithRegistry([]string{dir}, registry))
	return registry
}

func writeUnavailableSkillManifest(t *testing.T, root, name string, tools ...string) {
	t.Helper()

	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))

	content := fmt.Sprintf("name: %s\ndescription: %s skill\nversion: \"1.0.0\"\ntools:\n", name, name)
	for _, tool := range tools {
		content += "  - " + tool + "\n"
	}
	content += "triggers:\n  - type: keyword\n    values:\n      - " + name + "\n    weight: 1.0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte(content), 0o644))
}

// TestListSkills_IncludesUnavailableGroup 验证 GET /api/runtime/skills 在既有
// skills/count 字段不变的前提下，加性返回 unavailable 分组与缺失工具。
func TestListSkills_IncludesUnavailableGroup(t *testing.T) {
	registry := newUnavailableSkillTestRegistry(t)
	handler := NewHandler(registry, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload unavailableSkillContractResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	// 既有契约字段保持：只有 fetch_page 进入可执行列表。
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Skills, 1)
	assert.Equal(t, "fetch_page", payload.Skills[0]["name"])

	require.Equal(t, 1, payload.UnavailableCount)
	require.Len(t, payload.Unavailable, 1)
	item := payload.Unavailable[0]
	assert.Equal(t, "run_shell", item.Name)
	assert.Equal(t, skill.UnavailableReasonMissingTools, item.Reason)
	assert.Equal(t, []string{"bash", "view"}, item.MissingTools)
	assert.Equal(t, skill.SkillSourceLayerSystem, item.Scope)
	assert.NotEmpty(t, item.Path)
	assert.Contains(t, item.Message, "bash")
}

// TestGetStats_CountsAvailableAndUnavailable 验证 stats 加性区分 available/
// unavailable 计数（当前注册表没有 disabled 概念，契约不虚构该字段）。
func TestGetStats_CountsAvailableAndUnavailable(t *testing.T) {
	registry := newUnavailableSkillTestRegistry(t)
	handler := NewHandler(registry, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/stats", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload unavailableSkillContractResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	assert.Equal(t, 1, payload.AvailableCount)
	assert.Equal(t, 1, payload.UnavailableCount)
	assert.Equal(t, 1, payload.TotalSkills)
	assert.Equal(t, 1, payload.Counts["available"])
	assert.Equal(t, 1, payload.Counts["unavailable"])
	_, hasDisabled := payload.Counts["disabled"]
	assert.False(t, hasDisabled, "disabled is not modelled yet and must not be fabricated")
	require.Len(t, payload.Unavailable, 1)
	assert.Equal(t, "run_shell", payload.Unavailable[0].Name)
}

// TestExecuteSkill_UnavailableReturnsActionableError 验证按名执行 unavailable
// 技能时返回 409 + 结构化引导，而不是笼统的 "skill not found"。
func TestExecuteSkill_UnavailableReturnsActionableError(t *testing.T) {
	registry := newUnavailableSkillTestRegistry(t)
	handler := NewHandler(registry, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/run_shell/execute",
		bytes.NewReader([]byte(`{"prompt":"run something"}`)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	var payload struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Context struct {
			Skill        string   `json:"skill"`
			Reason       string   `json:"reason"`
			MissingTools []string `json:"missing_tools"`
			Path         string   `json:"path"`
			Scope        string   `json:"scope"`
			Hint         string   `json:"hint"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, string(runtimeerrors.ErrSkillUnavailable), payload.Code)
	assert.Contains(t, payload.Error, "run_shell")
	assert.Contains(t, payload.Error, "bash")
	assert.Equal(t, "run_shell", payload.Context.Skill)
	assert.Equal(t, skill.UnavailableReasonMissingTools, payload.Context.Reason)
	assert.Equal(t, []string{"bash", "view"}, payload.Context.MissingTools)
	assert.Equal(t, skill.SkillSourceLayerSystem, payload.Context.Scope)
	assert.NotEmpty(t, payload.Context.Path)
	assert.Contains(t, payload.Context.Hint, "bash")
}

// TestGetSkill_UnavailableReturnsActionableError 验证按名读取路径同样给出
// 可操作结构化错误（列表可见的技能不应表现为陌生 not found）。
func TestGetSkill_UnavailableReturnsActionableError(t *testing.T) {
	registry := newUnavailableSkillTestRegistry(t)
	handler := NewHandler(registry, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/run_shell", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	var payload struct {
		Code    string `json:"code"`
		Context struct {
			MissingTools []string `json:"missing_tools"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, string(runtimeerrors.ErrSkillUnavailable), payload.Code)
	assert.Equal(t, []string{"bash", "view"}, payload.Context.MissingTools)
}

// TestExecuteSkill_UnknownSkillStillNotFound 验证回归：真正不存在的技能
// 仍然返回 404 / SKILL_NOT_FOUND，不被 unavailable 逻辑吞掉。
func TestExecuteSkill_UnknownSkillStillNotFound(t *testing.T) {
	registry := newUnavailableSkillTestRegistry(t)
	handler := NewHandler(registry, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/ghost/execute",
		bytes.NewReader([]byte(`{"prompt":"x"}`)))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "SKILL_NOT_FOUND")
}

// TestSearchSkills_IncludesUnavailableGroup 验证搜索契约加性包含 unavailable
// 技能，避免它们只在列表可见、搜索时再次"消失"。
func TestSearchSkills_IncludesUnavailableGroup(t *testing.T) {
	registry := newUnavailableSkillTestRegistry(t)
	handler := NewHandler(registry, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/skills/search?q=run", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload unavailableSkillContractResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 1, payload.UnavailableCount)
	require.Len(t, payload.Unavailable, 1)
	assert.Equal(t, "run_shell", payload.Unavailable[0].Name)
}

// TestListCodexSkills_IncludesUnavailableGroup 验证 Codex 兼容 list 接口
// 加性返回注册表 unavailable 分组，且缓存命中路径同样实时可见。
func TestListCodexSkills_IncludesUnavailableGroup(t *testing.T) {
	baseRoot := isolatedCodexSkillsTestRoot(t)
	homeRoot := filepath.Join(baseRoot, "home")
	repoRoot := filepath.Join(baseRoot, "workspace", "repo")
	configRoot := filepath.Join(baseRoot, "config")

	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)

	require.NoError(t, os.MkdirAll(repoRoot, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(homeRoot, ".aicli", "agents", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(configRoot, "skills"), 0o755))
	configFile := filepath.Join(configRoot, "runtime.yaml")

	registry := newUnavailableSkillTestRegistry(t)
	handler := NewHandler(registry, nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	perform := func(forceReload bool) (int, unavailableSkillContractResponse, bool) {
		body, err := json.Marshal(map[string]interface{}{
			"cwds":         []string{repoRoot},
			"config_file":  configFile,
			"force_reload": forceReload,
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/skills/list", bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var payload struct {
			unavailableSkillContractResponse
			CacheHit bool `json:"cache_hit"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		return rec.Code, payload.unavailableSkillContractResponse, payload.CacheHit
	}

	status, first, cacheHit := perform(true)
	require.Equal(t, http.StatusOK, status)
	assert.False(t, cacheHit)
	require.Equal(t, 1, first.UnavailableCount)
	require.Len(t, first.Unavailable, 1)
	assert.Equal(t, "run_shell", first.Unavailable[0].Name)
	assert.Equal(t, []string{"bash", "view"}, first.Unavailable[0].MissingTools)

	// 缓存命中路径也必须带上 unavailable（诊断信息实时，不受 discovery 缓存影响）。
	_, second, cacheHit := perform(false)
	assert.True(t, cacheHit)
	require.Equal(t, 1, second.UnavailableCount)
	require.Len(t, second.Unavailable, 1)
	assert.Equal(t, "run_shell", second.Unavailable[0].Name)
}
