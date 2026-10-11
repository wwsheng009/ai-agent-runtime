package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

// testChatGatewayConfig 构造一个绑定指定配置文件路径的最小内存配置。
func testChatGatewayConfig(configPath string) *config.Config {
	return &config.Config{ConfigFilePath: configPath}
}

// --- /gateway 参数补全 ---

func TestGatewayCompletionSubcommandsForFirstToken(t *testing.T) {
	candidates := completeGatewaySlashArgs(nil, "", 0)
	names := completionCommandNames(candidates)
	for _, want := range []string{"status", "connect", "disconnect", "reconnect", "url", "config", "help"} {
		if !containsString(names, want) {
			t.Fatalf("gateway completion missing subcommand %q: %v", want, names)
		}
	}
}

func TestGatewayCompletionPrefixFilter(t *testing.T) {
	candidates := completeGatewaySlashArgs(nil, "conn", 4)
	names := completionCommandNames(candidates)
	if len(names) != 1 || names[0] != "connect" {
		t.Fatalf("expected only connect, got %v", names)
	}
}

func TestGatewayCompletionConfigKeys(t *testing.T) {
	candidates := completeGatewaySlashArgs(nil, "config ", 7)
	names := completionCommandNames(candidates)
	for _, want := range []string{"url", "token", "name", "auto-connect", "proxy", "disable"} {
		if !containsString(names, want) {
			t.Fatalf("config completion missing key %q: %v", want, names)
		}
	}
}

func TestGatewayCompletionProxyValues(t *testing.T) {
	candidates := completeGatewaySlashArgs(nil, "config proxy ", 13)
	names := completionCommandNames(candidates)
	if !containsString(names, "off") || !containsString(names, "none") {
		t.Fatalf("proxy value completion = %v, want off/none candidates", names)
	}
	// 等号形式同样给出取值候选。
	candidates = completeGatewaySlashArgs(nil, "config proxy=", len([]rune("config proxy=")))
	names = completionCommandNames(candidates)
	if !containsString(names, "off") {
		t.Fatalf("proxy= value completion = %v", names)
	}
	// 自由文本（socks5 地址）不枚举具体值：按前缀过滤后允许命中前缀提示或关闭弹窗。
	if candidates := completeGatewaySlashArgs(nil, "config proxy 127.0.0.1:1080", len([]rune("config proxy 127.0.0.1:1080"))); len(candidates) > 2 {
		t.Fatalf("free-text proxy value must not enumerate a large list: %v", completionCommandNames(candidates))
	}
}

func TestGatewayCompletionAutoConnectValues(t *testing.T) {
	candidates := completeGatewaySlashArgs(nil, "config auto-connect ", 20)
	names := completionCommandNames(candidates)
	if len(names) != 2 || !containsString(names, "on") || !containsString(names, "off") {
		t.Fatalf("auto-connect value completion = %v, want [on off]", names)
	}
	// 等号形式同样给出取值候选。
	candidates = completeGatewaySlashArgs(nil, "config auto-connect=", len([]rune("config auto-connect=")))
	names = completionCommandNames(candidates)
	if !containsString(names, "on") || !containsString(names, "off") {
		t.Fatalf("auto-connect= value completion = %v", names)
	}
}

func TestGatewayCompletionNoGuessForOtherPositions(t *testing.T) {
	// 非子命令位置不猜；自由文本取值位（token 的值）不枚举。
	if candidates := completeGatewaySlashArgs(nil, "status ", 7); len(candidates) != 0 {
		t.Fatalf("status takes no args, got %v", completionCommandNames(candidates))
	}
	if candidates := completeGatewaySlashArgs(nil, "config token ", 13); len(candidates) != 0 {
		t.Fatalf("token value is free text, got %v", completionCommandNames(candidates))
	}
}

// --- /gateway config 命令 ---

func TestParseGatewayConfigArgs(t *testing.T) {
	cases := []struct {
		command  string
		key      gatewayCommandToken
		value    string
		hasValue bool
	}{
		{"/gateway config", "", "", false},
		{"/gateway config url wss://gw.example.com", gatewayConfigKeyURL, "wss://gw.example.com", true},
		{"/gateway config token agw_x", gatewayConfigKeyToken, "agw_x", true},
		{"/gateway config name My Laptop", gatewayConfigKeyName, "My Laptop", true},
		{"/gateway config auto-connect off", gatewayConfigKeyAutoConnect, "off", true},
		{"/gateway config auto_connect off", gatewayConfigKeyAutoConnect, "off", true},
		{"/gateway config proxy socks5://127.0.0.1:1080", gatewayConfigKeyProxyURL, "socks5://127.0.0.1:1080", true},
		{"/gateway config proxy off", gatewayConfigKeyProxyURL, "off", true},
		{"/gateway config proxy-url http://127.0.0.1:3128", gatewayConfigKeyProxyURL, "http://127.0.0.1:3128", true},
		{"/gateway config proxy", gatewayConfigKeyProxyURL, "", false},
		{"/gateway config disable", gatewayConfigKeyDisable, "", false},
		{"/gateway config bogus x", "", "", false},
	}
	for _, tc := range cases {
		parsed := parseGatewayConfigArgs(tc.command)
		if parsed.key != tc.key || parsed.value != tc.value || parsed.hasValue != tc.hasValue {
			t.Fatalf("parseGatewayConfigArgs(%q) = %+v, want key=%v value=%q hasValue=%v",
				tc.command, parsed, tc.key, tc.value, tc.hasValue)
		}
	}
}

func TestGatewayConfigCommandWritesAndHotApplies(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{Config: testChatGatewayConfig(configPath)}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config url wss://gw.example.com")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if !strings.Contains(resultText(result), "已写入") {
		t.Fatalf("unexpected result: %s", resultText(result))
	}

	// token 写入后 controller 应已热挂载（Enabled 由 url+token 派生）。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/gateway config token agw_abcdef123456")
	if err != nil || !handled {
		t.Fatalf("token: handled=%v err=%v", handled, err)
	}
	if session.Gateway == nil {
		t.Fatal("gateway controller must be attached after config token write")
	}
	if session.Gateway.Status().TokenHint != "agw_…" {
		t.Fatalf("token hint = %q", session.Gateway.Status().TokenHint)
	}
	if !strings.Contains(resultText(result), "agw_…") {
		t.Fatalf("result must show only the hint: %s", resultText(result))
	}
	if strings.Contains(resultText(result), "agw_abcdef123456") {
		t.Fatalf("result must never echo the raw token: %s", resultText(result))
	}

	// 配置文件落盘校验。
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "wss://gw.example.com") || !strings.Contains(text, "agw_abcdef123456") {
		t.Fatalf("config file not persisted:\n%s", text)
	}
}

func TestGatewayConfigBareCommandShowsSummary(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("gateway:\n  url: wss://gw.example.com\n  token: agw_abcdef123456\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{Config: testChatGatewayConfig(configPath)}
	result, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	text := resultText(result)
	if !strings.Contains(text, "wss://gw.example.com") || !strings.Contains(text, "agw_…") {
		t.Fatalf("summary missing url/token hint: %s", text)
	}
	if strings.Contains(text, "agw_abcdef123456") {
		t.Fatalf("summary must not leak the raw token: %s", text)
	}
}

func TestGatewayConfigUnknownKeyShowsUsage(t *testing.T) {
	session := &ChatSession{Config: testChatGatewayConfig(filepath.Join(t.TempDir(), "config.yaml"))}
	result, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config bogus")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if !strings.Contains(resultText(result), "用法") {
		t.Fatalf("unknown key must show usage: %s", resultText(result))
	}
}

func TestGatewayConfigDisableClearsConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	original := "gateway:\n  url: wss://gw.example.com\n  token: agw_secret\n"
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	controller := gatewayagent.NewController(gatewayagent.Config{Enabled: true, URL: "wss://gw.example.com", Token: "agw_secret"}, nil)
	session := &ChatSession{Config: testChatGatewayConfig(configPath), Gateway: controller}

	result, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config disable")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	_ = result
	if session.Gateway.Status().State != gatewayagent.StateDisabled {
		t.Fatalf("controller state = %s, want disabled", session.Gateway.Status().State)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "agw_secret") || strings.Contains(text, "wss://gw.example.com") {
		t.Fatalf("disable must clear url/token on disk:\n%s", text)
	}
}

func TestGatewayConfigHelpTextListsConfig(t *testing.T) {
	got := gatewayHelpText()
	if !strings.Contains(got, "/gateway config") {
		t.Fatalf("help text missing /gateway config:\n%s", got)
	}
}

func TestGatewayConfigProxyWriteAndClear(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("gateway:\n  url: wss://gw.example.com\n  token: agw_secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{Config: testChatGatewayConfig(configPath)}

	// 写入代理：落盘 + 内存同步。
	result, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config proxy socks5://127.0.0.1:1080")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if !strings.Contains(resultText(result), "socks5://127.0.0.1:1080") {
		t.Fatalf("unexpected result: %s", resultText(result))
	}
	if got := session.Config.Gateway.ProxyURL; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("in-memory proxy_url = %q", got)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(raw); !strings.Contains(text, "proxy_url: socks5://127.0.0.1:1080") {
		t.Fatalf("proxy_url not persisted:\n%s", text)
	}

	// 无 scheme 的简写同样合法（拨号器默认 socks5://）。
	if _, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config proxy 192.168.1.1:7890"); err != nil || !handled {
		t.Fatalf("shorthand: handled=%v err=%v", handled, err)
	}
	if got := session.Config.Gateway.ProxyURL; got != "192.168.1.1:7890" {
		t.Fatalf("shorthand proxy_url = %q", got)
	}

	// off 清除代理（空串落盘 = 回到直连）。
	if _, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config proxy off"); err != nil || !handled {
		t.Fatalf("off: handled=%v err=%v", handled, err)
	}
	if got := session.Config.Gateway.ProxyURL; got != "" {
		t.Fatalf("proxy off must clear proxy_url, got %q", got)
	}
	raw, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(raw); strings.Contains(text, "proxy_url: off") || strings.Contains(text, "proxy_url: socks5") {
		t.Fatalf("proxy off must not leave a stale value on disk:\n%s", text)
	}

	// 非法 scheme 在配置时报错。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/gateway config proxy ftp://x")
	if err != nil || !handled {
		t.Fatalf("bad scheme: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(resultText(result), "socks5") {
		t.Fatalf("bad scheme must explain supported schemes: %s", resultText(result))
	}
}

func TestGatewayConfigProxySummaryShowsValue(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("gateway:\n  url: wss://gw.example.com\n  token: agw_secret\n  proxy_url: socks5://127.0.0.1:1080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &ChatSession{Config: testChatGatewayConfig(configPath)}
	result, handled, err := tryExecuteStructuredChatCommand(session, "/gateway config")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if !strings.Contains(resultText(result), "socks5://127.0.0.1:1080") {
		t.Fatalf("summary must show proxy_url: %s", resultText(result))
	}
	// 单键查看。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/gateway config proxy")
	if err != nil || !handled {
		t.Fatalf("key view: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(resultText(result), "socks5://127.0.0.1:1080") {
		t.Fatalf("key view must show proxy_url: %s", resultText(result))
	}
}

func resultText(result CommandResult) string {
	var b strings.Builder
	for _, block := range result.Blocks {
		b.WriteString(renderDocumentText(block.Document))
		b.WriteString("\n")
	}
	return b.String()
}

func completionCommandNames(candidates []chatSlashCompletionCandidate) []string {
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.Command)
	}
	return names
}
