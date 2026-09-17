package commands

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resetChatWebAuthForTest 清理令牌与来源，避免用例间互相污染。
func resetChatWebAuthForTest(t *testing.T) {
	t.Helper()
	setChatWebAuthTokenForTest("")
	t.Cleanup(func() { setChatWebAuthTokenForTest("") })
}

// TestSetChatWebAuthToken_Validation 锁定显式令牌的校验边界：
// 长度不足/超长、首尾空白、非 URL 安全字符一律拒绝，且错误信息不回显令牌。
func TestSetChatWebAuthToken_Validation(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "生成的 32 位 hex", token: "0123456789abcdef0123456789abcdef"},
		{name: "URL 安全字符集", token: "abc-DEF_123.~-xyz0"},
		{name: "长度恰好 16", token: "0123456789abcdef"},
		{name: "长度恰好 256", token: strings.Repeat("a", 256)},
		{name: "空", token: "", wantErr: true},
		{name: "过短", token: "0123456789abcde", wantErr: true},
		{name: "超长 257", token: strings.Repeat("a", 257), wantErr: true},
		{name: "首尾空白", token: " 0123456789abcdef ", wantErr: true},
		{name: "内部空白", token: "0123456789abc def", wantErr: true},
		{name: "非法字符", token: "0123456789abcdef!", wantErr: true},
		{name: "非 ASCII", token: "0123456789abcdef中文", wantErr: true},
		{name: "查询参数需转义的字符", token: "0123456789abcde&", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetChatWebAuthForTest(t)
			err := SetChatWebAuthToken(tc.token)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SetChatWebAuthToken(%d 字节) = nil, want error", len(tc.token))
				}
				if tc.token != "" && strings.Contains(err.Error(), tc.token) {
					t.Fatalf("错误信息不得回显令牌内容: %v", err)
				}
				if got := ChatWebAuthToken(); got != "" {
					t.Fatalf("被拒绝的输入不得改变令牌状态: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetChatWebAuthToken() error = %v", err)
			}
			if got := ChatWebAuthToken(); got != tc.token {
				t.Fatalf("token = %q, want %q", got, tc.token)
			}
			if got := ChatWebAuthTokenSource(); got != chatWebAuthTokenSourceFlag {
				t.Fatalf("source = %q, want %q", got, chatWebAuthTokenSourceFlag)
			}
		})
	}
}

// TestSetChatWebAuthToken_CannotReplaceLiveToken 锁定"令牌一旦生效不可运行中替换"：
// 换成另一个值报错（避免页面 meta 与服务器期望值不一致），同值重复设置幂等。
func TestSetChatWebAuthToken_CannotReplaceLiveToken(t *testing.T) {
	resetChatWebAuthForTest(t)
	const first = "0123456789abcdef0123456789abcdef"

	if err := SetChatWebAuthToken(first); err != nil {
		t.Fatalf("首次设置失败: %v", err)
	}
	if err := SetChatWebAuthToken("ffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatal("运行中替换令牌应报错")
	}
	if got := ChatWebAuthToken(); got != first {
		t.Fatalf("被拒绝的替换不得改变令牌: %q", got)
	}
	if err := SetChatWebAuthToken(first); err != nil {
		t.Fatalf("同值重复设置应幂等: %v", err)
	}
}

// TestApplyChatWebAuthToken_Precedence 锁定启动参数解析：
// --web-token > AICLI_WEB_TOKEN > 随机；未指定时不改动状态；
// 无效输入直接报错且**不回退**到 env/随机（避免"传错却静默用别的令牌"）。
func TestApplyChatWebAuthToken_Precedence(t *testing.T) {
	const (
		flagToken = "flag-token-0123456789abcdef"
		envToken  = "env-token-0123456789abcdef"
	)

	t.Run("都未指定时保持未初始化", func(t *testing.T) {
		resetChatWebAuthForTest(t)
		t.Setenv(ChatWebAuthTokenEnv, "")
		source, err := ApplyChatWebAuthToken("")
		if err != nil || source != "" {
			t.Fatalf("source = %q, err = %v; want 空来源与 nil 错误", source, err)
		}
		if got := ChatWebAuthToken(); got != "" {
			t.Fatalf("token = %q, want 未初始化（保持随机生成）", got)
		}
	})

	t.Run("仅环境变量", func(t *testing.T) {
		resetChatWebAuthForTest(t)
		t.Setenv(ChatWebAuthTokenEnv, envToken)
		source, err := ApplyChatWebAuthToken("")
		if err != nil {
			t.Fatalf("ApplyChatWebAuthToken() error = %v", err)
		}
		if source != ChatWebAuthTokenEnv || ChatWebAuthTokenSource() != ChatWebAuthTokenEnv {
			t.Fatalf("source = %q / %q, want %q", source, ChatWebAuthTokenSource(), ChatWebAuthTokenEnv)
		}
		if got := ChatWebAuthToken(); got != envToken {
			t.Fatalf("token = %q, want %q", got, envToken)
		}
	})

	t.Run("flag 优先于环境变量", func(t *testing.T) {
		resetChatWebAuthForTest(t)
		t.Setenv(ChatWebAuthTokenEnv, envToken)
		source, err := ApplyChatWebAuthToken(flagToken)
		if err != nil {
			t.Fatalf("ApplyChatWebAuthToken() error = %v", err)
		}
		if source != chatWebAuthTokenSourceFlag {
			t.Fatalf("source = %q, want %q", source, chatWebAuthTokenSourceFlag)
		}
		if got := ChatWebAuthToken(); got != flagToken {
			t.Fatalf("token = %q, want flag 值", got)
		}
	})

	t.Run("无效 flag 不回退到环境变量", func(t *testing.T) {
		resetChatWebAuthForTest(t)
		t.Setenv(ChatWebAuthTokenEnv, envToken)
		if _, err := ApplyChatWebAuthToken("short"); err == nil {
			t.Fatal("无效 flag 应报错")
		}
		if got := ChatWebAuthToken(); got != "" {
			t.Fatalf("token = %q, 无效输入后应保持未初始化而不是回退 env", got)
		}
	})

	t.Run("无效环境变量同样报错", func(t *testing.T) {
		resetChatWebAuthForTest(t)
		t.Setenv(ChatWebAuthTokenEnv, "too-short")
		if _, err := ApplyChatWebAuthToken(""); err == nil {
			t.Fatal("无效环境变量应报错")
		}
		if got := ChatWebAuthToken(); got != "" {
			t.Fatalf("token = %q, want 未初始化", got)
		}
	})
}

// TestChatWebAuthGuard_ProvidedToken 验证显式令牌接入守卫后：
// 该令牌（请求头或查询参数）放行，其他令牌/no token 一律 403。
func TestChatWebAuthGuard_ProvidedToken(t *testing.T) {
	resetChatWebAuthForTest(t)
	const token = "provided-token-0123456789abcdef"
	if err := SetChatWebAuthToken(token); err != nil {
		t.Fatalf("SetChatWebAuthToken() error = %v", err)
	}

	handler := ChatWebAuthGuard(guardProbeHandler())
	cases := []struct {
		name       string
		header     string
		queryToken string
		want       int
	}{
		{name: "显式令牌放行", header: token, want: http.StatusOK},
		{name: "查询参数放行", queryToken: token, want: http.StatusOK},
		{name: "随机形态的其他令牌拒绝", header: "0123456789abcdef0123456789abcdef", want: http.StatusForbidden},
		{name: "无令牌拒绝", want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "http://127.0.0.1:64562/web/api/invoke"
			if tc.queryToken != "" {
				target += "?token=" + tc.queryToken
			}
			req := httptest.NewRequest(http.MethodPost, target, nil)
			req.Host = "127.0.0.1:64562"
			if tc.header != "" {
				req.Header.Set(ChatWebAuthTokenHeader, tc.header)
			}
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
