package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// chat_mcp_auth_test.go 覆盖 chat 侧 OAuth：状态投影、发起/继续/完成/清除四条路径、
// 以及选择器动作随授权态变化。端到端用例跑真实的 PKCE 起流程 + 粘贴回调兑换，
// 令牌写入 AICLI_MCP_TOKENS_FILE 指向的临时文件（绝不碰用户真实令牌）。

func newChatMCPAuthTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	writeMeta := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":                 server.URL,
			"authorization_endpoint": server.URL + "/authorize",
			"token_endpoint":         server.URL + "/token",
		})
	}
	mux.HandleFunc("/.well-known/oauth-authorization-server", writeMeta)
	mux.HandleFunc("/.well-known/openid-configuration", writeMeta)
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.TrimSpace(r.Form.Get("code")) == "" {
			http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "access-" + r.Form.Get("code"),
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "refresh-1",
			"scope":         "read",
		})
	})
	return server
}

func newChatMCPAuthTestItem(name, url string, authCfg *config.MCPAuthConfig) mcpadmin.Item {
	return mcpadmin.Item{
		Config: config.MCPConfig{Name: name, Type: "streamable", URL: url, Enabled: true, Auth: authCfg},
		Status: &config.MCPStatus{Name: name, Type: "streamable", Enabled: true, RequiresAuth: true},
	}
}

// 非 OAuth 配置下必须给出明确空态与用法，而不是静默“已授权”。
func TestChatMCPAuthStatusWithoutOAuthServers(t *testing.T) {
	service := newFakeChatMCPService()
	service.items = []mcpadmin.Item{{
		Config: config.MCPConfig{Name: "chrome-mcp", Type: "streamable", URL: "http://127.0.0.1:12306/mcp"},
	}}
	text := chatMCPCommandTextWithService("/mcp auth", service, nil)
	if !strings.Contains(text, "当前没有配置 auth: oauth 的 MCP Server") {
		t.Fatalf("空态文案不符:\n%s", text)
	}
	if !strings.Contains(text, "/mcp add <name> <url> --auth oauth") {
		t.Fatalf("空态应给出添加方式:\n%s", text)
	}
}

// 未配置 auth 的 server：报错并提示如何补配置。
func TestChatMCPAuthBeginWithoutOAuthConfig(t *testing.T) {
	service := newFakeChatMCPService()
	service.items = []mcpadmin.Item{{Config: config.MCPConfig{Name: "local-fs", Type: "stdio", Command: "npx"}}}
	service.configs["local-fs"] = &config.MCPConfig{Name: "local-fs", Type: "stdio", Command: "npx"}

	text := chatMCPCommandTextWithService("/mcp auth local-fs", service, nil)
	if !strings.Contains(text, "未配置 auth: oauth") {
		t.Fatalf("缺少未配置提示:\n%s", text)
	}
	if !strings.Contains(text, "--auth oauth") {
		t.Fatalf("缺少修复用法:\n%s", text)
	}
}

// 端到端：起流程 → 从输出里取回调地址与 state → 粘贴回调 URL 完成兑换 → 状态变为已授权。
func TestChatMCPAuthEndToEndWithPastedCallback(t *testing.T) {
	t.Setenv("AICLI_MCP_TOKENS_FILE", filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(resetChatMCPAuthPendings)

	oauthServer := newChatMCPAuthTestServer(t)
	authCfg := &config.MCPAuthConfig{Type: "oauth", ClientID: "test-client", AuthorizationServer: oauthServer.URL}
	service := newFakeChatMCPService()
	item := newChatMCPAuthTestItem("notion", oauthServer.URL+"/mcp", authCfg)
	service.items = []mcpadmin.Item{item}
	service.configs["notion"] = &item.Config

	mutations := 0
	onMutate := func() { mutations++ }

	beginText := chatMCPCommandTextWithService("/mcp auth notion --no-browser", service, onMutate)
	if !strings.Contains(beginText, "已发起 OAuth 授权：notion") {
		t.Fatalf("发起文案不符:\n%s", beginText)
	}
	authURL := firstLineStartingWith(t, beginText, oauthServer.URL+"/authorize")
	redirectURI := strings.TrimSpace(strings.TrimPrefix(firstLineContaining(t, beginText, "回调地址:"), "回调地址:"))
	if redirectURI == "" {
		t.Fatalf("缺少回调地址:\n%s", beginText)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse 授权 URL: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" || parsed.Query().Get("code_challenge") == "" {
		t.Fatalf("授权 URL 缺少 PKCE/state: %s", authURL)
	}

	// 再次运行（尚无回调）应提示继续等待并回放授权链接。
	waitingText := chatMCPCommandTextWithService("/mcp auth notion", service, onMutate)
	if !strings.Contains(waitingText, "仍在等待浏览器回调") || !strings.Contains(waitingText, authURL) {
		t.Fatalf("等待文案不符:\n%s", waitingText)
	}

	callback := redirectURI + "?code=abc123&state=" + url.QueryEscape(state)
	doneText := chatMCPCommandTextWithService("/mcp auth notion "+callback, service, onMutate)
	if !strings.Contains(doneText, "✅ 授权成功：notion（scope: read）") {
		t.Fatalf("完成文案不符:\n%s", doneText)
	}
	if service.reloads != 1 || mutations != 1 {
		t.Fatalf("授权成功后应热重载并刷新工具面: reloads=%d mutations=%d", service.reloads, mutations)
	}
	if active := chatMCPAuthPendingActive("notion"); active {
		t.Fatal("完成后不应残留进行中的授权流程")
	}

	statusText := chatMCPCommandTextWithService("/mcp auth", service, nil)
	if !strings.Contains(statusText, "● notion [已授权") || !strings.Contains(statusText, "scope: read") {
		t.Fatalf("状态应显示已授权:\n%s", statusText)
	}
	statusText = chatMCPCommandTextWithService("/mcp auth notion --status", service, nil)
	if !strings.Contains(statusText, "● notion [已授权") {
		t.Fatalf("单 server 状态应显示已授权:\n%s", statusText)
	}

	// 清除令牌后再查状态：回到未授权，且清除动作不再报告“已清除”。
	clearText := chatMCPCommandTextWithService("/mcp auth notion --clear", service, nil)
	if !strings.Contains(clearText, "已清除 OAuth 令牌：notion") {
		t.Fatalf("清除文案不符:\n%s", clearText)
	}
	againText := chatMCPCommandTextWithService("/mcp auth notion --clear", service, nil)
	if !strings.Contains(againText, "没有已保存的 OAuth 令牌") {
		t.Fatalf("重复清除应报告无令牌:\n%s", againText)
	}
	if active := chatMCPAuthPendingActive("notion"); active {
		t.Fatal("清除后不应残留进行中的授权流程")
	}
}

// state 不匹配必须拒绝（分段路径同样有 CSRF 防护）。
func TestChatMCPAuthRejectsStateMismatch(t *testing.T) {
	t.Setenv("AICLI_MCP_TOKENS_FILE", filepath.Join(t.TempDir(), "tokens.json"))
	t.Cleanup(resetChatMCPAuthPendings)

	oauthServer := newChatMCPAuthTestServer(t)
	authCfg := &config.MCPAuthConfig{Type: "oauth", ClientID: "test-client", AuthorizationServer: oauthServer.URL}
	service := newFakeChatMCPService()
	item := newChatMCPAuthTestItem("notion", oauthServer.URL+"/mcp", authCfg)
	service.items = []mcpadmin.Item{item}
	service.configs["notion"] = &item.Config

	chatMCPCommandTextWithService("/mcp auth notion --no-browser", service, nil)
	mismatch := "https://ignored/callback?code=abc&state=wrong"
	text := chatMCPCommandTextWithService("/mcp auth notion "+mismatch, service, nil)
	if !strings.Contains(text, "state 校验失败") {
		t.Fatalf("state 不匹配应报错:\n%s", text)
	}
	// 失败后流程已被丢弃：再粘贴应提示没有进行中的流程。
	text = chatMCPCommandTextWithService("/mcp auth notion "+mismatch, service, nil)
	if !strings.Contains(text, "没有进行中的授权流程") {
		t.Fatalf("失败后应清理流程:\n%s", text)
	}
}

// 选择器动作随授权态变化，且全部落在既有 /mcp 子命令上。
func TestChatMCPPickerAuthActionsTrackState(t *testing.T) {
	tokensPath := filepath.Join(t.TempDir(), "tokens.json")
	t.Setenv("AICLI_MCP_TOKENS_FILE", tokensPath)

	authCfg := &config.MCPAuthConfig{Type: "oauth", ClientID: "test-client"}
	item := newChatMCPAuthTestItem("notion", "https://mcp.notion.com/mcp", authCfg)
	item.Status.RequiresAuth = false

	actions := chatMCPPickerActions("notion", true, chatMCPPickerAuthStateOf(item))
	if got := actionCommandFor(t, actions, "认证"); got != "/mcp auth notion" {
		t.Fatalf("未授权时应给出认证动作，实际 %q", got)
	}
	if actionCommandFor(t, actions, chatMCPPickerActionClearAuth) != "" {
		t.Fatal("未授权时不应出现清除授权动作")
	}

	store, err := auth.NewTokenStore(tokensPath)
	if err != nil {
		t.Fatalf("NewTokenStore: %v", err)
	}
	if err := store.Put("notion", &auth.Token{ServerName: "notion", ServerURL: item.Config.URL, AccessToken: "at", Scope: "read"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	actions = chatMCPPickerActions("notion", true, chatMCPPickerAuthStateOf(item))
	if got := actionCommandFor(t, actions, "重新认证"); got != "/mcp auth notion" {
		t.Fatalf("已授权时应给出重新认证动作，实际 %q", got)
	}
	if got := actionCommandFor(t, actions, chatMCPPickerActionClearAuth); got != "/mcp auth notion --clear" {
		t.Fatalf("已授权时应给出清除授权动作，实际 %q", got)
	}
	for _, action := range actions {
		if action.Label == chatMCPPickerActionClearAuth && !action.Confirm {
			t.Fatal("清除授权应二次确认")
		}
	}

	// 非 OAuth server 不出现任何授权动作。
	plain := mcpadmin.Item{Config: config.MCPConfig{Name: "local-fs", Type: "stdio", Command: "npx"}}
	for _, action := range chatMCPPickerActions("local-fs", true, chatMCPPickerAuthStateOf(plain)) {
		if strings.HasPrefix(action.Command, "/mcp auth") {
			t.Fatalf("非 OAuth server 不应出现授权动作: %+v", action)
		}
	}
}

func actionCommandFor(t *testing.T, actions []mcpPickerAction, label string) string {
	t.Helper()
	for _, action := range actions {
		if action.Label == label {
			return action.Command
		}
	}
	return ""
}

func firstLineContaining(t *testing.T, text, marker string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("未找到包含 %q 的行:\n%s", marker, text)
	return ""
}

func firstLineStartingWith(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("未找到以 %q 开头的行:\n%s", prefix, text)
	return ""
}
