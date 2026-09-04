package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ---------------------------------------------------------------------------
// 配置管理端点（/web/api/config*）测试
// ---------------------------------------------------------------------------

const webConfigTestYAML = `providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://api.example.com
      api_path: /v1/chat/completions
      default_model: gpt-4o
      supported_models:
        - gpt-4o
        - gpt-4o-mini
      model_capabilities:
        gpt-4o:
          reasoning_model: true
          reasoning_efforts: [low, medium, high]
          default_reasoning_effort: medium
        gpt-4o-mini:
          reasoning_model: false
          reasoning_efforts: [low, medium]
    beta:
      enabled: false
      protocol: anthropic
      default_model: claude-3-5-sonnet
aicli:
  chat:
    default_provider: alpha
    default_model: gpt-4o
    reasoning_effort: medium
`

// withWebConfigTestSession 写临时配置文件并注册带 ConfigFilePath 的会话。
func withWebConfigTestSession(t *testing.T, yamlContent string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	session := newWebTestSession()
	session.RuntimeConfigPath = path
	session.Config = &agentconfig.Config{ConfigFilePath: path}
	withWebTestSession(t, session)
	return path
}

func postConfigJSON(t *testing.T, handler http.HandlerFunc, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/web/api/config", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	handler(rec, req)
	return rec
}

func decodeConfigSnapshot(t *testing.T, body string) chatWebConfigSnapshot {
	t.Helper()
	var snap chatWebConfigSnapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("decode config snapshot: %v", err)
	}
	return snap
}

func configProviderByName(t *testing.T, snap chatWebConfigSnapshot, name string) chatWebConfigProvider {
	t.Helper()
	for _, p := range snap.Providers {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("provider %q not found in snapshot", name)
	return chatWebConfigProvider{}
}

// TestHandleChatWebAPIConfig_NoSession 无会话时返回空快照而不是错误。
func TestHandleChatWebAPIConfig_NoSession(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/web/api/config", nil)
	HandleChatWebAPIConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	snap := decodeConfigSnapshot(t, rec.Body.String())
	if len(snap.Providers) != 0 {
		t.Fatalf("providers = %d, want 0", len(snap.Providers))
	}
}

// TestHandleChatWebAPIConfig_Snapshot 验证快照结构：provider 全量（含禁用）、
// 模型 capabilities、aicli.chat 默认偏好。
func TestHandleChatWebAPIConfig_Snapshot(t *testing.T) {
	path := withWebConfigTestSession(t, webConfigTestYAML)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/web/api/config", nil)
	HandleChatWebAPIConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	snap := decodeConfigSnapshot(t, rec.Body.String())

	if snap.ConfigPath != path {
		t.Errorf("config_path = %q, want %q", snap.ConfigPath, path)
	}
	if snap.DefaultProvider != "alpha" {
		t.Errorf("default_provider = %q, want alpha", snap.DefaultProvider)
	}
	if len(snap.Providers) != 2 {
		t.Fatalf("providers = %d, want 2 (含禁用项)", len(snap.Providers))
	}
	if snap.Providers[0].Name != "alpha" || snap.Providers[1].Name != "beta" {
		t.Errorf("providers not sorted: %s, %s", snap.Providers[0].Name, snap.Providers[1].Name)
	}

	alpha := configProviderByName(t, snap, "alpha")
	if !alpha.Enabled {
		t.Error("alpha.enabled = false, want true")
	}
	if alpha.Protocol != "openai" || alpha.BaseURL != "https://api.example.com" || alpha.APIPath != "/v1/chat/completions" {
		t.Errorf("alpha connection fields mismatch: %+v", alpha)
	}
	if alpha.DefaultModel != "gpt-4o" {
		t.Errorf("alpha.default_model = %q, want gpt-4o", alpha.DefaultModel)
	}
	if len(alpha.SupportedModels) != 2 {
		t.Errorf("alpha.supported_models = %v, want len 2", alpha.SupportedModels)
	}
	var gpt4o, gpt4omini *chatWebConfigModel
	for i := range alpha.Models {
		if alpha.Models[i].Name == "gpt-4o" {
			gpt4o = &alpha.Models[i]
		}
		if alpha.Models[i].Name == "gpt-4o-mini" {
			gpt4omini = &alpha.Models[i]
		}
	}
	if gpt4o == nil || gpt4omini == nil {
		t.Fatalf("alpha models missing capability entries: %+v", alpha.Models)
	}
	if !gpt4o.ReasoningModel {
		t.Error("gpt-4o.reasoning_model = false, want true")
	}
	if strings.Join(gpt4o.ReasoningEfforts, ",") != "low,medium,high" {
		t.Errorf("gpt-4o.reasoning_efforts = %v", gpt4o.ReasoningEfforts)
	}
	if gpt4o.DefaultReasoningEffort != "medium" {
		t.Errorf("gpt-4o.default_reasoning_effort = %q", gpt4o.DefaultReasoningEffort)
	}
	if gpt4omini.ReasoningModel {
		t.Error("gpt-4o-mini.reasoning_model = true, want false")
	}
	if strings.Join(gpt4omini.ReasoningEfforts, ",") != "low,medium" {
		t.Errorf("gpt-4o-mini.reasoning_efforts = %v", gpt4omini.ReasoningEfforts)
	}

	beta := configProviderByName(t, snap, "beta")
	if beta.Enabled {
		t.Error("beta.enabled = true, want false")
	}

	if snap.Chat.DefaultProvider != "alpha" || snap.Chat.DefaultModel != "gpt-4o" || snap.Chat.ReasoningEffort != "medium" {
		t.Errorf("chat defaults mismatch: %+v", snap.Chat)
	}
}

// TestHandleChatWebAPIConfigProviders_Upsert 验证新增 provider + reasoning
// capabilities + 设默认。事务性：仅提交模型列表内的 reasoning 配置。
func TestHandleChatWebAPIConfigProviders_Upsert(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProviders, map[string]interface{}{
		"name":                 "gamma",
		"protocol":             "openai",
		"base_url":             "https://gamma.example.com",
		"enabled":              true,
		"default_model":        "g-1",
		"supported_models":     []string{"g-1", "g-2"},
		"set_default_provider": true,
		"reasoning": map[string]interface{}{
			"g-1": map[string]interface{}{
				"reasoning_model":          true,
				"reasoning_efforts":        []string{"low", "medium"},
				"default_reasoning_effort": "medium",
				"compact_reasoning_effort": "low",
			},
			"g-2": map[string]interface{}{
				"reasoning_model":   false,
				"reasoning_efforts": []string{},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("response status = %v, body: %s", resp["status"], rec.Body.String())
	}

	// 重新拉取快照验证落盘结果
	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())

	if snap.DefaultProvider != "gamma" {
		t.Errorf("default_provider = %q, want gamma", snap.DefaultProvider)
	}
	gamma := configProviderByName(t, snap, "gamma")
	if !gamma.Enabled || gamma.Protocol != "openai" || gamma.BaseURL != "https://gamma.example.com" {
		t.Errorf("gamma fields mismatch: %+v", gamma)
	}
	if len(gamma.SupportedModels) != 2 {
		t.Errorf("gamma.supported_models = %v", gamma.SupportedModels)
	}
	var g1, g2 *chatWebConfigModel
	for i := range gamma.Models {
		if gamma.Models[i].Name == "g-1" {
			g1 = &gamma.Models[i]
		}
		if gamma.Models[i].Name == "g-2" {
			g2 = &gamma.Models[i]
		}
	}
	if g1 == nil || g2 == nil {
		t.Fatalf("gamma models missing: %+v", gamma.Models)
	}
	if !g1.ReasoningModel || strings.Join(g1.ReasoningEfforts, ",") != "low,medium" ||
		g1.DefaultReasoningEffort != "medium" || g1.CompactReasoningEffort != "low" {
		t.Errorf("g-1 reasoning mismatch: %+v", g1)
	}
	if g2.ReasoningModel {
		t.Error("g-2.reasoning_model = true, want false")
	}
	if len(g2.ReasoningEfforts) != 0 {
		t.Errorf("g-2.reasoning_efforts = %v, want empty", g2.ReasoningEfforts)
	}

	// 未触碰的 alpha 仍完好
	alpha := configProviderByName(t, snap, "alpha")
	if !alpha.Enabled || alpha.DefaultModel != "gpt-4o" {
		t.Errorf("alpha unexpectedly changed: %+v", alpha)
	}
}

// TestHandleChatWebAPIConfigProviders_UpsertMergePreservesOthers 验证按模型
// 合并：只改提交的模型，其余模型 capabilities 原样保留。
func TestHandleChatWebAPIConfigProviders_UpsertMergePreservesOthers(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProviders, map[string]interface{}{
		"name": "alpha",
		"reasoning": map[string]interface{}{
			"gpt-4o": map[string]interface{}{
				"default_reasoning_effort": "high",
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())
	alpha := configProviderByName(t, snap, "alpha")

	var gpt4o, gpt4omini *chatWebConfigModel
	for i := range alpha.Models {
		if alpha.Models[i].Name == "gpt-4o" {
			gpt4o = &alpha.Models[i]
		}
		if alpha.Models[i].Name == "gpt-4o-mini" {
			gpt4omini = &alpha.Models[i]
		}
	}
	if gpt4o == nil || gpt4omini == nil {
		t.Fatalf("alpha models missing: %+v", alpha.Models)
	}
	if gpt4o.DefaultReasoningEffort != "high" {
		t.Errorf("gpt-4o.default_reasoning_effort = %q, want high", gpt4o.DefaultReasoningEffort)
	}
	if !gpt4o.ReasoningModel || strings.Join(gpt4o.ReasoningEfforts, ",") != "low,medium,high" {
		t.Errorf("gpt-4o 原有字段被破坏: %+v", gpt4o)
	}
	if strings.Join(gpt4omini.ReasoningEfforts, ",") != "low,medium" || gpt4omini.DefaultReasoningEffort != "" {
		t.Errorf("gpt-4o-mini 未被触碰的字段被破坏: %+v", gpt4omini)
	}
}

// TestHandleChatWebAPIConfigProviders_Delete 验证删除 provider。
func TestHandleChatWebAPIConfigProviders_Delete(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProvidersDelete, map[string]interface{}{
		"names": []string{"alpha"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())
	if len(snap.Providers) != 1 || snap.Providers[0].Name != "beta" {
		t.Fatalf("providers after delete = %+v, want only beta", snap.Providers)
	}
}

// TestHandleChatWebAPIConfigProviders_DeleteEmptyNames 空名称列表返回 400。
func TestHandleChatWebAPIConfigProviders_DeleteEmptyNames(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProvidersDelete, map[string]interface{}{
		"names": []string{},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleChatWebAPIConfigProviders_Enabled 验证批量启用/禁用。
func TestHandleChatWebAPIConfigProviders_Enabled(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProvidersEnabled, map[string]interface{}{
		"names":   []string{"alpha"},
		"enabled": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())
	if alpha := configProviderByName(t, snap, "alpha"); alpha.Enabled {
		t.Error("alpha.enabled = true, want false after disable")
	}
}

// TestHandleChatWebAPIConfigChat 验证 aicli.chat 默认偏好更新。
func TestHandleChatWebAPIConfigChat(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigChat, map[string]interface{}{
		"default_provider": "beta",
		"default_model":    "claude-3-5-sonnet",
		"reasoning_effort": "high",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())
	if snap.Chat.DefaultProvider != "beta" || snap.Chat.DefaultModel != "claude-3-5-sonnet" || snap.Chat.ReasoningEffort != "high" {
		t.Errorf("chat defaults after update = %+v", snap.Chat)
	}
}

// TestHandleChatWebAPIConfigProviders_MethodNotAllowed 非 POST 请求返回 405。
func TestHandleChatWebAPIConfigProviders_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/web/api/config/providers", nil)
	HandleChatWebAPIConfigProviders(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "rejected" {
		t.Errorf("status = %q, want rejected", resp["status"])
	}
}
