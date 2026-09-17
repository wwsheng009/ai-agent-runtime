package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
)

// TestPprofServerWebRemoteInvokeEndpoints 走真实 loopback mux 验证：
//   - /web/api/invoke 的路由注册与无会话行为（不依赖 LLM 会话）；
//   - P0 鉴权：Host/Origin 校验 + 写操作令牌 + 页面自动注入令牌。
func TestPprofServerWebRemoteInvokeEndpoints(t *testing.T) {
	handle, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer handle.Close()
	base := "http://" + handle.Addr()

	token := commands.ChatWebAuthToken()
	if token == "" {
		t.Fatal("write token was not generated at server start")
	}

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, string(body)
	}
	post := func(tokenHeader, origin, payload string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+commands.ChatWebAPIInvokePath,
			strings.NewReader(payload))
		if err != nil {
			t.Fatalf("NewRequest error = %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if tokenHeader != "" {
			req.Header.Set(commands.ChatWebAuthTokenHeader, tokenHeader)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST invoke error = %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, string(body)
	}

	// 页面注入写令牌 meta + fetch 包装（内置 Web 客户端据此自动带令牌）。
	status, pageHTML := get("/web/")
	if status != http.StatusOK {
		t.Fatalf("GET /web/ status = %d", status)
	}
	if !strings.Contains(pageHTML, `name="aicli-web-token" content="`+token+`"`) {
		t.Fatalf("page is missing injected token meta")
	}
	if !strings.Contains(pageHTML, commands.ChatWebAuthTokenHeader) {
		t.Fatalf("page is missing fetch wrapper for %s", commands.ChatWebAuthTokenHeader)
	}

	// 写操作缺少令牌 → 403。
	status, body := post("", "", `{"prompt":"hello"}`)
	if status != http.StatusForbidden || !strings.Contains(body, commands.ChatWebAuthTokenHeader) {
		t.Fatalf("token-less POST status = %d body=%s, want 403", status, body)
	}

	// 跨域 Origin → 403（即使令牌正确）。
	status, body = post(token, "https://evil.com", `{"prompt":"hello"}`)
	if status != http.StatusForbidden || !strings.Contains(body, "cross-origin") {
		t.Fatalf("cross-origin POST status = %d body=%s, want 403", status, body)
	}

	// 带令牌：无活动会话 → 409。
	status, body = post(token, "", `{"prompt":"hello"}`)
	if status != http.StatusConflict || !strings.Contains(body, "no active chat session") {
		t.Fatalf("authorized POST status = %d body=%s, want 409", status, body)
	}

	// 伪造 Host（DNS rebinding 形态）→ 403。
	req, err := http.NewRequest(http.MethodGet, base+commands.ChatWebAPIScreenPath, nil)
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	req.Host = "evil.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rebinding GET error = %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding GET status = %d, want 403", resp.StatusCode)
	}

	// 非 POST → 405。
	if status, body := get(commands.ChatWebAPIInvokePath); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET invoke status = %d, want %d body=%s", status, http.StatusMethodNotAllowed, body)
	}

	// TUI 帧视图在无会话时返回 available=false 的文本提示。
	status, body = get(commands.ChatWebAPIScreenPath + "?view=tui")
	if status != http.StatusOK {
		t.Fatalf("GET screen?view=tui status = %d body=%s", status, body)
	}
	if !strings.Contains(body, "Debug Screen: no active chat session") {
		t.Fatalf("GET screen?view=tui body = %s", body)
	}

	// 调试端点清单仍可用（无会话时为 available=false 的轻量快照；
	// 端点族与写鉴权说明由 commands 包用例覆盖）。
	status, body = get(chatEndpointsPath)
	if status != http.StatusOK {
		t.Fatalf("GET endpoints status = %d body=%s", status, body)
	}
	if !strings.Contains(body, `"available"`) {
		t.Fatalf("endpoints body = %s", body)
	}

	// turn 查询在无会话时为 503（有稳定错误体）。
	if status, body := get(commands.ChatWebAPITurnPath); status != http.StatusServiceUnavailable {
		t.Fatalf("GET turn status = %d, want 503 body=%s", status, body)
	}
}
