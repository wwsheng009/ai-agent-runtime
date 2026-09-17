package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleChatWebAPIToken 锁定 GET /web/api/token 契约：返回头名/令牌/查询参数名，
// 并带 no-store（令牌是进程内动态秘密，任何缓存层不得保留）。
func TestHandleChatWebAPIToken(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	setChatWebAuthTokenForTest(token)
	defer setChatWebAuthTokenForTest("")

	req := httptest.NewRequest(http.MethodGet, ChatWebAPITokenPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	var resp chatWebTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if resp.Status != "ok" {
		t.Fatalf("status field = %q, want ok", resp.Status)
	}
	if resp.Token != token {
		t.Fatalf("token = %q, want %q", resp.Token, token)
	}
	if resp.Header != ChatWebAuthTokenHeader {
		t.Fatalf("header = %q, want %q", resp.Header, ChatWebAuthTokenHeader)
	}
	if resp.QueryParam != "token" {
		t.Fatalf("query_param = %q, want token", resp.QueryParam)
	}
	if resp.Source != chatWebAuthTokenSourceFlag {
		t.Fatalf("source = %q, want %q（显式指定）", resp.Source, chatWebAuthTokenSourceFlag)
	}
	if !strings.Contains(resp.Hint, "重启不轮换") {
		t.Fatalf("hint 应说明显式令牌不轮换: %q", resp.Hint)
	}
	if !strings.Contains(resp.Hint, ChatWebAuthTokenHeader) {
		t.Fatalf("hint = %q, want mention of %s", resp.Hint, ChatWebAuthTokenHeader)
	}
}

// TestHandleChatWebAPIToken_RandomSourceHint 锁定默认（随机）路径的措辞：
// source=random + "重启即轮换"，脚本据此判断令牌不能跨重启复用。
func TestHandleChatWebAPIToken_RandomSourceHint(t *testing.T) {
	setChatWebAuthTokenForTest("")
	defer setChatWebAuthTokenForTest("")
	if generated := EnsureChatWebAuthToken(); len(generated) != 32 { // 走随机生成分支
		t.Fatalf("随机令牌长度 = %d, want 32（16 字节 hex）", len(generated))
	}

	req := httptest.NewRequest(http.MethodGet, ChatWebAPITokenPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp chatWebTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if resp.Source != chatWebAuthTokenSourceRandom {
		t.Fatalf("source = %q, want %q", resp.Source, chatWebAuthTokenSourceRandom)
	}
	if !strings.Contains(resp.Hint, "重启即轮换") {
		t.Fatalf("随机令牌 hint 应说明重启即轮换: %q", resp.Hint)
	}
}

// TestHandleChatWebAPIToken_MethodNotAllowed 锁定只读语义：非 GET 一律 405。
func TestHandleChatWebAPIToken_MethodNotAllowed(t *testing.T) {
	setChatWebAuthTokenForTest("0123456789abcdef0123456789abcdef")
	defer setChatWebAuthTokenForTest("")

	req := httptest.NewRequest(http.MethodPost, ChatWebAPITokenPath, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	HandleChatWebAPIToken(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (body=%s)", rec.Code, rec.Body.String())
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
	if strings.Contains(rec.Body.String(), "0123456789abcdef") {
		t.Fatal("405 response must not echo the token")
	}
}

// TestHandleChatWebAPIToken_NotInitialized 锁定未初始化（进程内直调、无服务器）时
// 返回 503 且不带令牌字段，脚本据此区分「还没起来」与「拿不到」。
func TestHandleChatWebAPIToken_NotInitialized(t *testing.T) {
	setChatWebAuthTokenForTest("")
	defer setChatWebAuthTokenForTest("")

	req := httptest.NewRequest(http.MethodGet, ChatWebAPITokenPath, nil)
	rec := httptest.NewRecorder()
	HandleChatWebAPIToken(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp chatWebTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if resp.Status != "error" || resp.Token != "" {
		t.Fatalf("resp = %+v, want error without token", resp)
	}
}

// TestChatWebAuthGuard_TokenEndpointStillGuarded 锁定端点没有绕过 Host/Origin 两层：
// 非回环 Host 与跨域 Origin 都必须 403，即便这是只读 GET。
func TestChatWebAuthGuard_TokenEndpointStillGuarded(t *testing.T) {
	setChatWebAuthTokenForTest("0123456789abcdef0123456789abcdef")
	defer setChatWebAuthTokenForTest("")

	handler := ChatWebAuthGuard(HandleChatWebAPIToken)

	cases := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{name: "loopback ok", host: "127.0.0.1:64562", want: http.StatusOK},
		{name: "dns rebinding host", host: "evil.example:64562", want: http.StatusForbidden},
		{name: "cross origin", host: "127.0.0.1:64562", origin: "http://evil.example", want: http.StatusForbidden},
		{name: "same origin", host: "127.0.0.1:64562", origin: "http://127.0.0.1:64562", want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, ChatWebAPITokenPath, nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusForbidden && strings.Contains(rec.Body.String(), "0123456789abcdef") {
				t.Fatal("rejected response must not leak the token")
			}
		})
	}
}
