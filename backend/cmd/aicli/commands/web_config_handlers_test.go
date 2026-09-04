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
      api_key: sk-test-secret-123
      api_key_ref: authref-alpha
      auth_mode: api_key
      auth_ref: oauth-alpha
      forward_url: https://fw.example.com/v1
      proxy:
        enabled: true
        http: http://127.0.0.1:7890
        https: http://127.0.0.1:7890
        no_proxy: localhost,127.0.0.1
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
	if !alpha.APIKeySet {
		t.Error("alpha.api_key_set = false, want true（fixture 已写 api_key）")
	}
	if alpha.APIKeySource != "key_store" {
		t.Errorf("alpha.api_key_source = %q, want key_store（api_key_ref 存在时按解析链优先于内联）", alpha.APIKeySource)
	}
	if alpha.APIKeyRef != "authref-alpha" || alpha.AuthMode != "api_key" || alpha.AuthRef != "oauth-alpha" {
		t.Errorf("alpha auth refs mismatch: %+v", alpha)
	}
	if strings.Contains(rec.Body.String(), "sk-test-secret-123") {
		t.Error("快照泄露 API key 明文，必须只回传 api_key_set")
	}
	if alpha.ForwardURL != "https://fw.example.com/v1" {
		t.Errorf("alpha.forward_url = %q", alpha.ForwardURL)
	}
	if alpha.Proxy == nil || !alpha.Proxy.Enabled || alpha.Proxy.HTTP != "http://127.0.0.1:7890" ||
		alpha.Proxy.HTTPS != "http://127.0.0.1:7890" || alpha.Proxy.NoProxy != "localhost,127.0.0.1" {
		t.Errorf("alpha.proxy mismatch: %+v", alpha.Proxy)
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

// TestHandleChatWebAPIConfigProviders_UpsertAPIKeyProxy 验证写 api_key /
// forward_url / proxy：快照只回传 api_key_set（不泄露明文），YAML 落盘。
func TestHandleChatWebAPIConfigProviders_UpsertAPIKeyProxy(t *testing.T) {
	path := withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProviders, map[string]interface{}{
		"name":        "alpha",
		"api_key":     "sk-web-new-key-999",
		"forward_url": "https://fw2.example.com",
		"proxy": map[string]interface{}{
			"enabled":  true,
			"http":     "http://127.0.0.1:1080",
			"https":    "http://127.0.0.1:1080",
			"no_proxy": "localhost",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// YAML 落盘检查
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"sk-web-new-key-999", "https://fw2.example.com", "http://127.0.0.1:1080", "no_proxy: localhost"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config yaml 缺少 %q", want)
		}
	}

	// 快照检查（masked）
	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())
	alpha := configProviderByName(t, snap, "alpha")
	if !alpha.APIKeySet {
		t.Error("alpha.api_key_set = false, want true")
	}
	if strings.Contains(rec2.Body.String(), "sk-web-new-key-999") {
		t.Error("快照泄露新 API key 明文")
	}
	if alpha.ForwardURL != "https://fw2.example.com" {
		t.Errorf("alpha.forward_url = %q", alpha.ForwardURL)
	}
	if alpha.Proxy == nil || !alpha.Proxy.Enabled || alpha.Proxy.HTTP != "http://127.0.0.1:1080" ||
		alpha.Proxy.NoProxy != "localhost" {
		t.Errorf("alpha.proxy mismatch: %+v", alpha.Proxy)
	}
}

// TestHandleChatWebAPIConfigProviders_ClearAPIKeyProxy 验证显式清空语义：
// api_key / api_key_ref / auth_ref 空串与 api_keys 空数组移除全部凭据来源、
// forward_url 空串清空、clear_proxy 移除 proxy 节点。
func TestHandleChatWebAPIConfigProviders_ClearAPIKeyProxy(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProviders, map[string]interface{}{
		"name":        "alpha",
		"api_key":     "",
		"api_key_ref": "",
		"auth_ref":    "",
		"api_keys":    []string{},
		"forward_url": "",
		"clear_proxy": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())
	alpha := configProviderByName(t, snap, "alpha")
	if alpha.APIKeySet {
		t.Error("alpha.api_key_set = true, want false（已清除）")
	}
	if alpha.APIKeySource != "" {
		t.Errorf("alpha.api_key_source = %q, want 空（全部凭据来源已清除）", alpha.APIKeySource)
	}
	if alpha.ForwardURL != "" {
		t.Errorf("alpha.forward_url = %q, want 空（已清除）", alpha.ForwardURL)
	}
	if alpha.Proxy != nil {
		t.Errorf("alpha.proxy = %+v, want nil（已移除节点）", alpha.Proxy)
	}
}

// TestHandleChatWebAPIConfigProviders_APIKeySources 验证快照凭据状态覆盖
// 全部来源：oauth（auth_mode+auth_ref）/ key_store（api_key_ref）/ pool
// （api_keys）/ inline（api_key）/ 无；api_key_source 与 GetAllAPIKeys 的
// 解析链顺序一致。
func TestHandleChatWebAPIConfigProviders_APIKeySources(t *testing.T) {
	const yaml = `providers:
  default_provider: none1
  items:
    oauth1:
      enabled: true
      protocol: openai
      auth_mode: oauth
      auth_ref: auth-oauth-1
    ks1:
      enabled: true
      protocol: openai
      api_key_ref: authref-ks1
    pool1:
      enabled: true
      protocol: openai
      api_keys:
        - sk-pool-a
        - sk-pool-b
    inline1:
      enabled: true
      protocol: openai
      api_key: sk-inline-1
    none1:
      enabled: true
      protocol: openai
`
	path := withWebConfigTestSession(t, yaml)

	rec := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	snap := decodeConfigSnapshot(t, rec.Body.String())
	got := map[string]chatWebConfigProvider{}
	for _, p := range snap.Providers {
		got[p.Name] = p
	}
	for _, c := range []struct {
		name string
		want string
	}{
		{"oauth1", "oauth"},
		{"ks1", "key_store"},
		{"pool1", "pool"},
		{"inline1", "inline"},
		{"none1", ""},
	} {
		p, ok := got[c.name]
		if !ok {
			t.Errorf("provider %s missing from snapshot", c.name)
			continue
		}
		if p.APIKeySource != c.want {
			t.Errorf("%s.api_key_source = %q, want %q", c.name, p.APIKeySource, c.want)
		}
		if p.APIKeySet != (c.want != "") {
			t.Errorf("%s.api_key_set = %v, want %v", c.name, p.APIKeySet, c.want != "")
		}
	}

	// 清除 pool1 的密钥池：空数组应移除 api_keys 节点，快照回到未配置。
	rec2 := postConfigJSON(t, HandleChatWebAPIConfigProviders, map[string]interface{}{
		"name":     "pool1",
		"api_keys": []string{},
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200; body: %s", rec2.Code, rec2.Body.String())
	}
	rec3 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec3, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	pool1 := configProviderByName(t, decodeConfigSnapshot(t, rec3.Body.String()), "pool1")
	if pool1.APIKeySet || pool1.APIKeySource != "" {
		t.Errorf("pool1 after clear: api_key_set=%v api_key_source=%q, want false/空",
			pool1.APIKeySet, pool1.APIKeySource)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if strings.Contains(string(raw), "sk-pool-a") {
		t.Error("api_keys 池清除后 YAML 仍包含旧密钥")
	}
}

// TestHandleChatWebAPIConfigProviders_APIKeyMergePreserved 验证未提交
// api_key / auth 字段时保留原值（nil=不修改合并语义）。
func TestHandleChatWebAPIConfigProviders_APIKeyMergePreserved(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProviders, map[string]interface{}{
		"name":    "alpha",
		"api_key": nil,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	HandleChatWebAPIConfig(rec2, httptest.NewRequest(http.MethodGet, "/web/api/config", nil))
	snap := decodeConfigSnapshot(t, rec2.Body.String())
	alpha := configProviderByName(t, snap, "alpha")
	if !alpha.APIKeySet {
		t.Error("未提交 api_key 时原密钥被清除，合并语义错误")
	}
	if alpha.APIKeyRef != "authref-alpha" || alpha.AuthRef != "oauth-alpha" {
		t.Errorf("未提交 auth 字段时被改动: %+v", alpha)
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

// fetchModelsTestServer 起一个 mock 的 OpenAI 风格 /v1/models 端点，
// 记录收到的 Authorization 头以便断言内联/已保存 key 的传递。
func fetchModelsTestServer(t *testing.T, authHeader *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		*authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"},{"id":"o3"}]}`))
	})
	return httptest.NewServer(mux)
}

// TestHandleChatWebAPIConfigProvidersFetchModels_SavedProvider 验证以磁盘
// 已保存 provider 为基底拉取模型列表（api key 用已保存的）。
func TestHandleChatWebAPIConfigProvidersFetchModels_SavedProvider(t *testing.T) {
	var authHeader string
	srv := fetchModelsTestServer(t, &authHeader)
	defer srv.Close()

	yaml := strings.Replace(webConfigTestYAML, "https://api.example.com", srv.URL, 1)
	withWebConfigTestSession(t, yaml)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProvidersFetchModels, map[string]interface{}{
		"name": "alpha",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status   string   `json:"status"`
		Endpoint string   `json:"endpoint"`
		Models   []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" || len(resp.Models) != 3 {
		t.Fatalf("resp = %+v, want 3 models", resp)
	}
	if resp.Endpoint != srv.URL+"/v1/models" {
		t.Errorf("endpoint = %q, want %q", resp.Endpoint, srv.URL+"/v1/models")
	}
	if authHeader != "Bearer sk-test-secret-123" {
		t.Errorf("Authorization = %q, want 已保存 key", authHeader)
	}
	for _, want := range []string{"gpt-4o", "o3"} {
		found := false
		for _, m := range resp.Models {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("models 缺少 %q: %v", want, resp.Models)
		}
	}
}

// TestHandleChatWebAPIConfigProvidersFetchModels_NewProvider 验证新增流程：
// name 尚未保存（可缺省）时用请求里的 base_url / api_key 临时探测，
// 内联 key 优先于磁盘保存的 key。
func TestHandleChatWebAPIConfigProvidersFetchModels_NewProvider(t *testing.T) {
	var authHeader string
	srv := fetchModelsTestServer(t, &authHeader)
	defer srv.Close()

	withWebConfigTestSession(t, webConfigTestYAML)

	rec := postConfigJSON(t, HandleChatWebAPIConfigProvidersFetchModels, map[string]interface{}{
		"protocol": "openai",
		"base_url": srv.URL,
		"api_key":  "sk-temp-probe-777",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status string   `json:"status"`
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" || len(resp.Models) != 3 {
		t.Fatalf("resp = %+v, want 3 models", resp)
	}
	if authHeader != "Bearer sk-temp-probe-777" {
		t.Errorf("Authorization = %q, want 请求内联 key", authHeader)
	}
}

// TestHandleChatWebAPIConfigProvidersFetchModels_Errors 验证参数错误：
// 缺 name / 无 key / provider 不存在且无 base_url。
func TestHandleChatWebAPIConfigProvidersFetchModels_Errors(t *testing.T) {
	withWebConfigTestSession(t, webConfigTestYAML)

	cases := []struct {
		name string
		body map[string]interface{}
		want int
	}{
		{"缺 name 且缺 base_url", map[string]interface{}{}, http.StatusBadRequest},
		{"已保存但无 api key", map[string]interface{}{"name": "beta", "base_url": "https://mock.invalid"}, http.StatusBadGateway},
		{"未保存且无 base_url", map[string]interface{}{"name": "ghost"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postConfigJSON(t, HandleChatWebAPIConfigProvidersFetchModels, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
