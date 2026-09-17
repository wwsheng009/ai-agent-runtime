package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
)

// TestPprofServerWebRemoteInvokeEndpoints 走真实 loopback mux 验证 /web/api/invoke
// 与 /web/api/screen?view=tui 的路由注册与无会话行为（不依赖 LLM 会话）。
func TestPprofServerWebRemoteInvokeEndpoints(t *testing.T) {
	handle, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer handle.Close()
	base := "http://" + handle.Addr()

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

	// 无活动会话：POST /web/api/invoke → 409。
	resp, err := http.Post(base+commands.ChatWebAPIInvokePath, "application/json",
		strings.NewReader(`{"prompt":"hello"}`))
	if err != nil {
		t.Fatalf("POST invoke error = %v", err)
	}
	postBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST invoke status = %d, want %d body=%s", resp.StatusCode, http.StatusConflict, postBody)
	}
	if !strings.Contains(string(postBody), "no active chat session") {
		t.Fatalf("POST invoke body = %s", postBody)
	}

	// 非 POST → 405。
	if status, body := get(commands.ChatWebAPIInvokePath); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET invoke status = %d, want %d body=%s", status, http.StatusMethodNotAllowed, body)
	}

	// TUI 帧视图在无会话时返回 available=false 的文本提示。
	status, body := get(commands.ChatWebAPIScreenPath + "?view=tui")
	if status != http.StatusOK {
		t.Fatalf("GET screen?view=tui status = %d body=%s", status, body)
	}
	if !strings.Contains(body, "Debug Screen: no active chat session") {
		t.Fatalf("GET screen?view=tui body = %s", body)
	}

	// 调试端点清单仍可用（无会话时为轻量提示）。
	status, body = get(chatEndpointsPath)
	if status != http.StatusOK {
		t.Fatalf("GET endpoints status = %d body=%s", status, body)
	}
}
