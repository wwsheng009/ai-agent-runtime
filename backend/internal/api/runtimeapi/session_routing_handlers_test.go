package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 会话级路由 API 的后端用例（方案 §10.1：U-1/U-5/U-8/U-9/U-10/U-11/U-13）。
//
// 测试隔离：HOME/USERPROFILE 指向临时目录，避免 workspace 层与 config 层写入
// 污染真实用户目录（`WorkspacePrefsPathForPath` / `AICLIConfigWriteTargetForRouting`
// 都基于 home 解析）。

func newSessionRoutingTestHandler(t *testing.T) (*Handler, *chat.SessionManager, *mux.Router) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return handler, sessionManager, router
}

func doSessionRoutingRequest(t *testing.T, router *mux.Router, method, sessionID, body string, loopback bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, "/api/runtime/sessions/"+sessionID+"/routing", reader)
	req.Header.Set("Content-Type", "application/json")
	if loopback {
		req.RemoteAddr = "127.0.0.1:54321"
	} else {
		req.RemoteAddr = "203.0.113.7:54321"
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// U-1：写入 session 层 → context 落库、actor_invalidated=true、投影反映覆盖。
func TestSessionRoutingPatchSessionLayer(t *testing.T) {
	handler, sessionManager, router := newSessionRoutingTestHandler(t)
	session, err := sessionManager.Create(context.Background(), "user-routing-session-layer")
	require.NoError(t, err)

	// 档位表不含 expert：§5.10 规则要求 allow_expert=true 才能列出 expert，
	// 否则阶梯回退会整体关闭路由（U-3 的语义），测试要覆盖「合法写入」路径。
	body := `{"target_layer":"session","updated_by":"web","main_agent":{"enabled":true,"levels":["easy","normal","hard"],"profiles":{"hard":{"provider":"anthropic","model":"claude-opus-4","reasoning_effort":"high"}}}}`
	rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, body, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.True(t, payload.Updated)
	assert.True(t, payload.ActorInvalidated, "冷会话无 actor，仍应报告已无陈旧 actor")
	assert.True(t, payload.Panel.SessionOverride)
	assert.True(t, payload.Routing.Enabled, "覆盖启用后投影应为 enabled")
	assert.NotEmpty(t, payload.Routing.Revision)

	// 物理落点：session.Metadata.Context（U-14 同口径）。
	stored, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	raw := sessionRoutingOverrideRaw(stored)
	require.NotEmpty(t, raw, "会话覆盖必须写入 context")
	decoded, err := agentconfig.DecodeSessionRoutingOverride(raw)
	require.NoError(t, err)
	require.NotNil(t, decoded)
	require.NotNil(t, decoded.MainAgent)
	require.NotNil(t, decoded.MainAgent.Enabled)
	assert.True(t, *decoded.MainAgent.Enabled)
	assert.Equal(t, "web", decoded.UpdatedBy)

	// GET 与 PATCH 同源投影。
	getRec := doSessionRoutingRequest(t, router, http.MethodGet, session.ID, "", true)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	var getPayload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &getPayload))
	assert.True(t, getPayload.Panel.SessionOverride)
	assert.Equal(t, payload.Routing.Revision, getPayload.Routing.Revision)

	// U-8：清除 → context 键消失、解析回落（source 不再是 session）。
	clearRec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, `{"clear":true}`, true)
	require.Equal(t, http.StatusOK, clearRec.Code, clearRec.Body.String())
	var clearPayload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(clearRec.Body.Bytes(), &clearPayload))
	assert.False(t, clearPayload.Panel.SessionOverride)
	assert.False(t, clearPayload.Routing.Enabled, "清除后应回落到关闭态（零行为变化）")

	stored, err = sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Empty(t, sessionRoutingOverrideRaw(stored), "清除后不得残留 context 键")

	_ = handler
}

// U-5：子会话写入 409；GET 仍可读。
func TestSessionRoutingChildSessionConflict(t *testing.T) {
	_, sessionManager, router := newSessionRoutingTestHandler(t)
	child, err := sessionManager.Create(context.Background(), "user-routing-child")
	require.NoError(t, err)
	child.SetContext(toolbroker.AgentSessionContextAgentType, "worker")
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	require.NoError(t, sessionManager.Update(context.Background(), child))

	rec := doSessionRoutingRequest(t, router, http.MethodPatch, child.ID, `{"main_agent":{"enabled":true}}`, true)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	getRec := doSessionRoutingRequest(t, router, http.MethodGet, child.ID, "", true)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	var payload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &payload))
	assert.True(t, payload.Panel.ChildSession)
	assert.Empty(t, payload.Panel.WritableLayers, "子会话不提供可写层")

	stored, err := sessionManager.Get(context.Background(), child.ID)
	require.NoError(t, err)
	assert.Empty(t, sessionRoutingOverrideRaw(stored))
}

// U-10：非回环且无 token 的 PATCH 被拒（与既有会话写端点同级）。
func TestSessionRoutingPatchUnauthorized(t *testing.T) {
	_, sessionManager, router := newSessionRoutingTestHandler(t)
	session, err := sessionManager.Create(context.Background(), "user-routing-unauthorized")
	require.NoError(t, err)

	rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, `{"main_agent":{"enabled":true}}`, false)
	require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	stored, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Empty(t, sessionRoutingOverrideRaw(stored), "未授权请求不得落盘")
}

// U-11：通用 PATCH /sessions/{id} 的 context 不得绕过校验写路由键。
func TestUpdateSessionRejectsRoutingContextKey(t *testing.T) {
	_, sessionManager, router := newSessionRoutingTestHandler(t)
	session, err := sessionManager.Create(context.Background(), "user-routing-generic-patch")
	require.NoError(t, err)

	body := `{"context":{"aicli_routing_override":"{\"main_agent\":{\"enabled\":true}}"}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/runtime/sessions/"+session.ID, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stored, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Empty(t, sessionRoutingOverrideRaw(stored))
}

// U-9：workspace 层以会话绑定 workspace 路径写 chat-prefs.yaml 并回显路径（N9）。
func TestSessionRoutingPatchWorkspaceLayer(t *testing.T) {
	_, sessionManager, router := newSessionRoutingTestHandler(t)
	session, err := sessionManager.Create(context.Background(), "user-routing-workspace-layer")
	require.NoError(t, err)
	workspacePath := t.TempDir()
	session.SetContext(sessionmeta.WorkspacePath, workspacePath)
	require.NoError(t, sessionManager.Update(context.Background(), session))

	body := `{"target_layer":"workspace","main_agent":{"profiles":{"hard":{"model":"claude-opus-4"}}}}`
	rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, body, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, sessionRoutingLayerWorkspace, payload.TargetLayer)
	expectedPath := agentconfig.WorkspaceRoutingTargetPath(workspacePath)
	assert.Equal(t, expectedPath, payload.TargetPath)
	assert.True(t, payload.Panel.WorkspaceOverride)

	raw, err := os.ReadFile(expectedPath)
	require.NoError(t, err, "workspace 层写入必须落到会话绑定 workspace 的 chat-prefs.yaml")
	var document map[string]interface{}
	require.NoError(t, yaml.Unmarshal(raw, &document))
	// 工作区偏好文件沿用既有 `aicli.chat` 结构（与 LoadWorkspaceRoutingPreferencesForPath 同源）。
	aicliSection, ok := document["aicli"].(map[string]interface{})
	require.True(t, ok, "chat-prefs.yaml 应包含 aicli 节，实际 %s", string(raw))
	chatSection, ok := aicliSection["chat"].(map[string]interface{})
	require.True(t, ok, "chat-prefs.yaml 应包含 aicli.chat 节，实际 %s", string(raw))
	routing, ok := chatSection["routing"].(map[string]interface{})
	require.True(t, ok, "aicli.chat 应包含 routing 节，实际 %s", string(raw))
	main, ok := routing["main_agent"].(map[string]interface{})
	require.True(t, ok, "routing 应包含 main_agent 节，实际 %s", string(raw))
	profiles, ok := main["profiles"].(map[string]interface{})
	require.True(t, ok, "main_agent 应包含 profiles，实际 %s", string(raw))
	hard, ok := profiles["hard"].(map[string]interface{})
	require.True(t, ok, "profiles 应包含 hard，实际 %s", string(raw))
	assert.Equal(t, "claude-opus-4", hard["model"])

	// 会话层未被隐式写入（层路由互不串写）。
	stored, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Empty(t, sessionRoutingOverrideRaw(stored))
}

// U-13：config 层按层路由写入；无 confirm 拒绝；写前校验失败不落盘。
func TestSessionRoutingPatchConfigLayer(t *testing.T) {
	_, sessionManager, router := newSessionRoutingTestHandler(t)
	session, err := sessionManager.Create(context.Background(), "user-routing-config-layer")
	require.NoError(t, err)

	targetPath, _ := agentconfig.AICLIConfigWriteTargetForRouting()
	require.NotEmpty(t, targetPath)

	// 无 confirm → 400，不落盘。
	body := `{"target_layer":"config","main_agent":{"enabled":true,"levels":["easy","normal","hard"],"profiles":{"hard":{"model":"claude-opus-4"}}}}`
	rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, body, true)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	_, statErr := os.Stat(targetPath)
	assert.True(t, os.IsNotExist(statErr), "未确认的 config 写入不得创建文件")

	// confirm=true → 写入并回显目标路径。
	confirmed := `{"target_layer":"config","confirm":true,"main_agent":{"enabled":true,"levels":["easy","normal","hard"],"profiles":{"hard":{"model":"claude-opus-4"}}}}`
	rec = doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, confirmed, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, targetPath, payload.TargetPath)

	raw, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "claude-opus-4")

	// 校验失败（profiles 档位不在 levels 内）→ 拒绝且文件不变。
	before, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	invalid := `{"target_layer":"config","confirm":true,"main_agent":{"enabled":true,"levels":["easy"],"profiles":{"hard":{"model":"claude-opus-4"}}}}`
	rec = doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, invalid, true)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	after, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "校验失败不得改写配置文件")

	assert.True(t, filepath.IsAbs(targetPath))
}

// P1-1 回归：config 层写入成功后必须同步刷新 handler 的进程内快照。
//
// 不刷新的话，文件已是新值而快照仍是旧值：PATCH 回显 / 后续 GET 的投影会回退到
// 旧配置，主 Agent 接线也要等进程重启才生效（写文件与生效状态漂移）。
func TestSessionRoutingConfigLayerWriteRefreshesSnapshot(t *testing.T) {
	handler, sessionManager, router := newSessionRoutingTestHandler(t)
	session, err := sessionManager.Create(context.Background(), "user-routing-config-snapshot")
	require.NoError(t, err)

	// runtime-server 启动时会注入配置快照（这里模拟「已有快照但还没有 routing 节」）。
	handler.SetAICLIConfig(&agentconfig.Config{})

	body := `{"target_layer":"config","confirm":true,"main_agent":{"enabled":true,"levels":["easy","normal","hard"],"profiles":{"hard":{"model":"claude-opus-4"}}}}`
	rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, body, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	snapshot := handler.aicliConfigSnapshot()
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.AICLI, "写入后快照应保留 aicli 节")
	require.NotNil(t, snapshot.AICLI.MainAgent, "写入后快照应出现 aicli.main_agent")
	require.NotNil(t, snapshot.AICLI.MainAgent.Routing, "写入后快照应出现 main_agent.routing")
	assert.True(t, snapshot.AICLI.MainAgent.Routing.Enabled)
	assert.Equal(t, []string{"easy", "normal", "hard"}, snapshot.AICLI.MainAgent.Routing.Levels)
	assert.Contains(t, snapshot.AICLI.MainAgent.Routing.Profiles, "hard")

	// 投影同样反映新配置：GET 不再回显旧值。
	rec = doSessionRoutingRequest(t, router, http.MethodGet, session.ID, "", true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var payload sessionRoutingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.True(t, payload.Routing.Enabled)
	assert.True(t, payload.Panel.ConfigOverride, "投影应把 config 层记为已覆盖（快照已刷新）")
}

// ---------------------------------------------------------------------------
// U-2（M11）并发写串行化 + U-14（§3.4）会话层物理落点（2026-09-23 增补）

// newSessionRoutingTestHandlerWithStorage 与 newSessionRoutingTestHandler 同构，
// 但允许注入自定义会话存储（放大并发窗口 / 核对物理落点）。
func newSessionRoutingTestHandlerWithStorage(t *testing.T, storage chat.SessionStorage) (*Handler, *chat.SessionManager, *mux.Router) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(storage, nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return handler, sessionManager, router
}

// racingRoutingStorage 放大「读-改-写」窗口：第一个写者要等第二个写者落盘之后
// 才写回。没有会话级锁时，第一个写者会带着旧快照覆盖第二个写者的字段（丢更新）。
type racingRoutingStorage struct {
	*chat.FileStorage

	writes     atomic.Int32
	secondDone chan struct{}
	closeOnce  sync.Once
}

func newRacingRoutingStorage(t *testing.T) *racingRoutingStorage {
	t.Helper()
	store, err := chat.NewFileStorage(t.TempDir())
	require.NoError(t, err)
	return &racingRoutingStorage{FileStorage: store, secondDone: make(chan struct{})}
}

func (s *racingRoutingStorage) Update(ctx context.Context, session *chat.Session) error {
	n := s.writes.Add(1)
	if n == 1 {
		select {
		case <-s.secondDone:
		case <-time.After(time.Second):
		}
	}
	err := s.FileStorage.Update(ctx, session)
	if n == 2 {
		s.closeOnce.Do(func() { close(s.secondDone) })
	}
	return err
}

// U-2（§3.4/M11）：两会话端并发写同一 override —— 会话级锁串行化「读-改-写」，
// 后写胜出但不得丢字段。
func TestSessionRoutingConcurrentWritesDoNotLoseFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	_, sessionManager, router := newSessionRoutingTestHandlerWithStorage(t, newRacingRoutingStorage(t))
	session, err := sessionManager.Create(context.Background(), "user-routing-concurrent")
	require.NoError(t, err)

	bodies := []string{
		`{"target_layer":"session","main_agent":{"enabled":true}}`,
		`{"target_layer":"session","main_agent":{"levels":["easy","normal","hard"]}}`,
	}
	codes := make([]int, len(bodies))
	var wg sync.WaitGroup
	for i, body := range bodies {
		wg.Add(1)
		go func(i int, body string) {
			defer wg.Done()
			rec := doSessionRoutingRequest(t, router, http.MethodPatch, session.ID, body, true)
			codes[i] = rec.Code
		}(i, body)
	}
	wg.Wait()
	for i, code := range codes {
		require.Equal(t, http.StatusOK, code, "第 %d 个并发写入应成功", i)
	}

	stored, err := sessionManager.Get(context.Background(), session.ID)
	require.NoError(t, err)
	decoded, err := agentconfig.DecodeSessionRoutingOverride(sessionRoutingOverrideRaw(stored))
	require.NoError(t, err)
	require.NotNil(t, decoded)
	require.NotNil(t, decoded.MainAgent)
	require.NotNil(t, decoded.MainAgent.Enabled, "并发写不得丢掉 enabled（丢更新）")
	require.NotNil(t, decoded.MainAgent.Levels, "并发写不得丢掉 levels（丢更新）")
	assert.True(t, *decoded.MainAgent.Enabled)
	assert.Equal(t, []string{"easy", "normal", "hard"}, *decoded.MainAgent.Levels)
}

// U-14（§3.4 核实结论）：会话层 override 的物理落点——文件后端写进会话 JSON 的
// metadata.context.aicli_routing_override（日期分区路径）；运行时（sqlite）后端
// 同键可读回；两者都不得产生 ~/.aicli/workspaces（复数）目录。
func TestSessionRoutingSessionLayerPhysicalLanding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	body := `{"target_layer":"session","main_agent":{"enabled":true,"levels":["easy","normal","hard"]}}`

	// 文件后端：会话 JSON 落盘。
	sessionDir := t.TempDir()
	fileStore, err := chat.NewFileStorage(sessionDir)
	require.NoError(t, err)
	_, fileManager, fileRouter := newSessionRoutingTestHandlerWithStorage(t, fileStore)
	fileSession, err := fileManager.Create(context.Background(), "user-routing-physical-file")
	require.NoError(t, err)
	rec := doSessionRoutingRequest(t, fileRouter, http.MethodPatch, fileSession.ID, body, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var sessionFiles []string
	require.NoError(t, filepath.WalkDir(sessionDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			sessionFiles = append(sessionFiles, path)
		}
		return nil
	}))
	require.Len(t, sessionFiles, 1, "会话记录应落在文件后端目录内")
	raw, err := os.ReadFile(sessionFiles[0])
	require.NoError(t, err)
	assert.Contains(t, sessionFiles[0], time.Now().Format("2006"), "文件后端按日期分区（YYYY/MM/DD）")

	// 落点是 metadata.context.aicli_routing_override（JSON 字符串，§3.4 核实结论）。
	var storedSession struct {
		Metadata struct {
			Context map[string]interface{} `json:"context"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(raw, &storedSession))
	overrideRaw, _ := storedSession.Metadata.Context[agentconfig.SessionRoutingOverrideContextKey].(string)
	require.NotEmpty(t, overrideRaw, "会话 JSON 必须携带 context.%s", agentconfig.SessionRoutingOverrideContextKey)
	assert.Contains(t, overrideRaw, `"enabled":true`)

	// 运行时后端：sqlite 同键可读回。
	sqliteDir := t.TempDir()
	sqliteStore, err := chat.NewSQLiteSessionStorage(chat.PersistentSessionStorageConfig{
		Backend: "sqlite",
		Dir:     sqliteDir,
		Path:    filepath.Join(sqliteDir, "session_runtime.sqlite"),
	})
	require.NoError(t, err)
	_, sqliteManager, sqliteRouter := newSessionRoutingTestHandlerWithStorage(t, sqliteStore)
	sqliteSession, err := sqliteManager.Create(context.Background(), "user-routing-physical-sqlite")
	require.NoError(t, err)
	rec = doSessionRoutingRequest(t, sqliteRouter, http.MethodPatch, sqliteSession.ID, body, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	readBack, err := sqliteManager.Get(context.Background(), sqliteSession.ID)
	require.NoError(t, err)
	assert.Contains(t, sessionRoutingOverrideRaw(readBack), `"enabled":true`,
		"运行时后端应能读回会话路由覆盖")

	// §3.4 核实结论：会话层写入不新建 ~/.aicli/workspaces（复数）目录。
	_, statErr := os.Stat(filepath.Join(home, ".aicli", "workspaces"))
	assert.True(t, os.IsNotExist(statErr), "会话层写入不得创建 ~/.aicli/workspaces 目录")
}
