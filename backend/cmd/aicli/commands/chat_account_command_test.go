package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

// chatAccountTestSession 构造一个带缓存账户快照的会话：alpha 是当前生效
// provider，beta 只有基础配置，用于覆盖 provider 解析与 /accounts 汇总。
func chatAccountTestSession() *ChatSession {
	remaining := 12.34
	snapshot := &config.ProviderAccountSnapshot{
		Source:           string(siteaccount.SiteTypeSub2API),
		Mode:             "subscription",
		Currency:         "USD",
		QuotaRemaining:   &remaining,
		QuotaDisplayUnit: "USD",
		FetchedAt:        "2026-07-29T01:02:03Z",
	}
	alpha := config.Provider{
		Enabled:            true,
		Protocol:           "openai",
		BaseURL:            "https://alpha.test",
		SiteType:           string(siteaccount.SiteTypeSub2API),
		SiteTypeConfidence: "high",
		Account:            snapshot,
	}
	return &ChatSession{
		ProviderName: "alpha",
		Provider:     alpha,
		Model:        "gpt-4.1",
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "alpha",
				Items: map[string]config.Provider{
					"alpha": alpha,
					"beta":  {Enabled: true, Protocol: "openai", BaseURL: "https://beta.test"},
				},
			},
		},
	}
}

func chatAccountCommandText(t *testing.T, result CommandResult) string {
	t.Helper()
	if len(result.Blocks) == 0 {
		t.Fatal("expected at least one render block")
	}
	parts := make([]string, 0, len(result.Blocks))
	for _, block := range result.Blocks {
		parts = append(parts, block.Document.PlainText())
	}
	return strings.Join(parts, "\n")
}

func TestParseChatAccountCommandDefaultsToRefreshForCurrentProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		command     string
		wantMode    chatAccountCommandMode
		wantVariant chatAccountCommandVariant
		wantProd    string
		wantSave    bool
		wantJSON    bool
	}{
		{name: "bare refreshes current provider", command: "/account", wantMode: chatAccountModeRefresh, wantVariant: chatAccountVariantCurrent},
		{name: "refresh alias sync", command: "/account sync", wantMode: chatAccountModeRefresh, wantVariant: chatAccountVariantCurrent},
		{name: "refresh with provider and save", command: "/account refresh beta --save", wantMode: chatAccountModeRefresh, wantVariant: chatAccountVariantCurrent, wantProd: "beta", wantSave: true},
		{name: "positional provider", command: "/account beta", wantMode: chatAccountModeRefresh, wantVariant: chatAccountVariantCurrent, wantProd: "beta"},
		{name: "show alias status", command: "/account status", wantMode: chatAccountModeShow, wantVariant: chatAccountVariantCurrent},
		{name: "no-refresh normalizes to show", command: "/account --no-refresh", wantMode: chatAccountModeShow, wantVariant: chatAccountVariantCurrent},
		{name: "show with provider", command: "/account show beta", wantMode: chatAccountModeShow, wantVariant: chatAccountVariantCurrent, wantProd: "beta"},
		{name: "detect alias site", command: "/account site beta", wantMode: chatAccountModeDetect, wantVariant: chatAccountVariantCurrent, wantProd: "beta"},
		{name: "json flag", command: "/account show --json", wantMode: chatAccountModeShow, wantVariant: chatAccountVariantCurrent, wantJSON: true},
		{name: "json flag after refresh", command: "/account refresh --json", wantMode: chatAccountModeRefresh, wantVariant: chatAccountVariantCurrent, wantJSON: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req, errText := parseChatAccountCommand(test.command)
			if errText != "" {
				t.Fatalf("unexpected error: %s", errText)
			}
			if req.Mode != test.wantMode {
				t.Fatalf("mode = %q, want %q", req.Mode, test.wantMode)
			}
			if req.Variant != test.wantVariant {
				t.Fatalf("variant = %q, want %q", req.Variant, test.wantVariant)
			}
			if req.Provider != test.wantProd {
				t.Fatalf("provider = %q, want %q", req.Provider, test.wantProd)
			}
			if req.Save != test.wantSave {
				t.Fatalf("save = %v, want %v", req.Save, test.wantSave)
			}
			if req.JSON != test.wantJSON {
				t.Fatalf("json = %v, want %v", req.JSON, test.wantJSON)
			}
			if req.Timeout != chatAccountBalanceRefreshTimeout {
				t.Fatalf("timeout = %s, want default %s", req.Timeout, chatAccountBalanceRefreshTimeout)
			}
		})
	}

	if req, _ := parseChatAccountCommand("/account refresh --timeout=30s"); req.Timeout.String() != "30s" {
		t.Fatalf("timeout = %s, want 30s", req.Timeout)
	}
	if req, _ := parseChatAccountCommand("/account refresh --timeout 1m"); req.Timeout.String() != "1m0s" {
		t.Fatalf("timeout = %s, want 1m0s", req.Timeout)
	}
}

func TestParseChatAccountsCommandAcceptsFlagsOnly(t *testing.T) {
	t.Parallel()

	req, errText := parseChatAccountCommand("/accounts")
	if errText != "" {
		t.Fatalf("unexpected error: %s", errText)
	}
	if req.Variant != chatAccountVariantAll || req.Mode != chatAccountModeList {
		t.Fatalf("variant/mode = %q/%q, want accounts/list", req.Variant, req.Mode)
	}
	// /accounts 默认刷新全部：NoRefresh 是显式的非默认行为。
	if req.NoRefresh {
		t.Fatal("/accounts must refresh by default")
	}

	req, errText = parseChatAccountCommand("/accounts --enabled-only --no-refresh --json --timeout 30s")
	if errText != "" {
		t.Fatalf("unexpected error: %s", errText)
	}
	if !req.EnabledOnly || !req.NoRefresh || !req.JSON || req.Timeout.String() != "30s" {
		t.Fatalf("flag projection = %+v", req)
	}

	req, errText = parseChatAccountCommand("/accounts --timeout=1m")
	if errText != "" || req.Timeout.String() != "1m0s" {
		t.Fatalf("--timeout= form: err=%q timeout=%s", errText, req.Timeout)
	}
}

func TestParseChatAccountCommandRejectsInvalidCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		want    string
		usage   string
	}{
		{name: "list subcommand removed", command: "/account list", want: "已移除", usage: chatAccountsCommandUsage},
		{name: "list alias removed", command: "/account ls", want: "/accounts", usage: chatAccountsCommandUsage},
		{name: "list after flag", command: "/account --json list", want: "已移除", usage: chatAccountsCommandUsage},
		{name: "save outside refresh", command: "/account show --save", want: "--save 只适用于", usage: chatAccountCommandUsage},
		{name: "save with no-refresh", command: "/account --save --no-refresh", want: "不能同时使用", usage: chatAccountCommandUsage},
		{name: "no-refresh outside refresh", command: "/account detect --no-refresh", want: "--no-refresh 只适用于", usage: chatAccountCommandUsage},
		// /account 不接受 /accounts 专属标志：静默忽略会让两条命令的语义再次重叠。
		{name: "enabled-only belongs to accounts", command: "/account show --enabled-only", want: "未知选项", usage: chatAccountCommandUsage},
		{name: "timeout without value", command: "/account refresh --timeout", want: "--timeout 需要时长参数", usage: chatAccountCommandUsage},
		{name: "timeout invalid", command: "/account refresh --timeout soon", want: "--timeout 非法", usage: chatAccountCommandUsage},
		// 子命令写在标志之后会被当成 provider 名：显式报错而不是静默接受。
		{name: "subcommand after flag", command: "/account --json refresh", want: "必须放在标志之前", usage: chatAccountCommandUsage},
		{name: "extra provider", command: "/account show alpha beta", want: "多余参数", usage: chatAccountCommandUsage},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req, errText := parseChatAccountCommand(test.command)
			if errText == "" {
				t.Fatalf("expected error, got %+v", req)
			}
			if !strings.Contains(errText, test.want) {
				t.Fatalf("error %q does not contain %q", errText, test.want)
			}
			if !strings.Contains(errText, test.usage) {
				t.Fatalf("error %q should carry the usage text %q", errText, test.usage)
			}
		})
	}
}

func TestParseChatAccountsCommandRejectsPositionalArgsAndSave(t *testing.T) {
	t.Parallel()

	_, errText := parseChatAccountCommand("/accounts alpha")
	if !strings.Contains(errText, "不接受位置参数") || !strings.Contains(errText, "/account alpha") {
		t.Fatalf("/accounts must route positional args to /account, got %q", errText)
	}
	if !strings.Contains(errText, chatAccountsCommandUsage) {
		t.Fatalf("error %q should carry the /accounts usage text", errText)
	}

	_, errText = parseChatAccountCommand("/accounts --save")
	if !strings.Contains(errText, "--save 只适用于 /account") {
		t.Fatalf("--save must be rejected on /accounts, got %q", errText)
	}

	_, errText = parseChatAccountCommand("/accounts --verbose")
	if !strings.Contains(errText, "未知选项") {
		t.Fatalf("unknown option must be rejected, got %q", errText)
	}
}

func TestExecuteStructuredChatAccountCommandShowUsesCachedSnapshot(t *testing.T) {
	t.Parallel()

	session := chatAccountTestSession()
	// 只走缓存路径：/account 的默认模式需要联网，纯单元测试不触发真实请求。
	for _, command := range []string{"/account show", "/account --no-refresh", "/account status"} {
		result := executeStructuredChatAccountCommand(session, command)
		if result.Action != CommandContinue {
			t.Fatalf("%s action = %v, want continue", command, result.Action)
		}
		text := chatAccountCommandText(t, result)
		for _, want := range []string{"alpha", "sub2api", "12.34"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s output %q missing %q", command, text, want)
			}
		}
	}
}

func TestExecuteStructuredChatAccountCommandShowExplicitProvider(t *testing.T) {
	t.Parallel()

	session := chatAccountTestSession()
	result := executeStructuredChatAccountCommand(session, "/account show beta")
	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "beta") {
		t.Fatalf("expected beta provider in output, got %q", text)
	}
	if strings.Contains(text, "12.34") {
		t.Fatalf("beta has no cached account, output should not carry alpha balance: %q", text)
	}
	if !strings.Contains(text, "no_account") {
		t.Fatalf("expected no_account status for beta, got %q", text)
	}
}

func TestExecuteStructuredChatAccountCommandKeepsErrorsInCell(t *testing.T) {
	t.Parallel()

	session := chatAccountTestSession()
	for _, test := range []struct {
		command string
		want    string
	}{
		{command: "/account show nope", want: "未找到 provider"},
		{command: "/account bogus", want: "未找到 provider"},
		{command: "/account list", want: "已移除"},
		{command: "/accounts alpha", want: "不接受位置参数"},
	} {
		result := executeStructuredChatAccountCommand(session, test.command)
		if result.Action != CommandContinue {
			t.Fatalf("%s action = %v, want continue", test.command, result.Action)
		}
		text := chatAccountCommandText(t, result)
		if !strings.Contains(text, test.want) {
			t.Fatalf("%s output %q missing %q", test.command, text, test.want)
		}
	}
}

func TestExecuteStructuredChatAccountCommandJSONProjection(t *testing.T) {
	t.Parallel()

	session := chatAccountTestSession()
	result := executeStructuredChatAccountCommand(session, "/account show --json")
	var report chatAccountReport
	if err := json.Unmarshal([]byte(chatAccountCommandText(t, result)), &report); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if report.Provider != "alpha" {
		t.Fatalf("provider = %q, want alpha", report.Provider)
	}
	if report.SiteType != string(siteaccount.SiteTypeSub2API) {
		t.Fatalf("site_type = %q, want sub2api", report.SiteType)
	}
	if report.Account == nil {
		t.Fatal("expected account projection in JSON output")
	}
}

func TestExecuteStructuredChatAccountsCommandCountsProviders(t *testing.T) {
	t.Parallel()

	session := chatAccountTestSession()
	// 默认刷新全部需要联网：这里用 --no-refresh 验证缓存投影本身。
	result := executeStructuredChatAccountCommand(session, "/accounts --no-refresh")
	text := chatAccountCommandText(t, result)
	if !strings.Contains(text, "2 provider(s), 1 with account") {
		t.Fatalf("accounts summary missing: %q", text)
	}
	if strings.Contains(text, "· refreshed") {
		t.Fatalf("--no-refresh must not report a refresh: %q", text)
	}

	onlyEnabled := executeStructuredChatAccountCommand(session, "/accounts --enabled-only --no-refresh")
	enabledText := chatAccountCommandText(t, onlyEnabled)
	if !strings.Contains(enabledText, "alpha") || !strings.Contains(enabledText, "beta") {
		t.Fatalf("enabled-only list should keep both enabled providers: %q", enabledText)
	}
}

// TestDispatchChatAccountCommandsPublishDocumentCells 通过统一 dispatch 验证两条
// 命令都落在 CommandResult 文档 cell 上：没有可承载备用屏的 TTY 时不得写原始
// stdout，也不得把 /accounts 的结果混进 /account 的 cell。
func TestDispatchChatAccountCommandsPublishDocumentCells(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := chatAccountTestSession()
	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	var retained bytes.Buffer
	coord.SetWriter(&retained)
	raw := captureStdout(t, func() {
		for _, command := range []string{"/account show", "/accounts --no-refresh"} {
			if dispatchChatCommand(session, command, false) {
				t.Fatalf("%s unexpectedly requested chat exit", command)
			}
		}
	})
	if raw != "" {
		t.Fatalf("structured account commands wrote raw stdout:\n%q", raw)
	}

	output := retained.String()
	single := chatAccountCommandText(t, executeStructuredChatAccountCommand(session, "/account show"))
	for _, line := range strings.Split(strings.TrimSpace(single), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(output, line) {
			t.Fatalf("rendered output missing /account line %q:\n%s", line, output)
		}
	}
	if !strings.Contains(output, "2 provider(s), 1 with account") {
		t.Fatalf("rendered output missing the /accounts summary:\n%s", output)
	}
}

func TestCompleteChatAccountSlashArgsOffersSubcommandsAndProviders(t *testing.T) {
	t.Parallel()

	session := chatAccountTestSession()

	assertCandidates := func(t *testing.T, got []chatSlashCompletionCandidate, want ...string) {
		t.Helper()
		names := make(map[string]struct{}, len(got))
		for _, candidate := range got {
			names[candidate.Command] = struct{}{}
		}
		for _, name := range want {
			if _, ok := names[name]; !ok {
				t.Fatalf("candidate %q missing from %+v", name, got)
			}
		}
	}
	assertAbsent := func(t *testing.T, got []chatSlashCompletionCandidate, unwanted ...string) {
		t.Helper()
		for _, candidate := range got {
			for _, name := range unwanted {
				if strings.EqualFold(candidate.Command, name) {
					t.Fatalf("candidate %q must not be offered by /account: %+v", name, got)
				}
			}
		}
	}

	assertCandidates(t, completeChatAccountSlashArgs(session, "", 0), "refresh", "show", "detect", "--json", "--timeout")
	// list/ls 是 /account 的已移除子命令，补全不得再提供。
	assertAbsent(t, completeChatAccountSlashArgs(session, "", 0), "list", "ls")
	assertCandidates(t, completeChatAccountSlashArgs(session, "show ", 5), "alpha", "beta", "--json")
	assertCandidates(t, completeChatAccountSlashArgs(session, "refresh ", 8), "--save", "alpha")
	assertCandidates(t, completeChatAccountSlashArgs(session, "refresh --timeout=", 19), "15s", "30s")

	if got := completeChatAccountSlashArgs(session, "show alp", 8); len(got) == 0 || got[0].Command != "alpha" {
		t.Fatalf("expected alpha to lead provider completion, got %+v", got)
	}
}

func TestCompleteChatAccountsSlashArgsOffersFlagsOnly(t *testing.T) {
	t.Parallel()

	session := chatAccountTestSession()
	flags := completeChatAccountsSlashArgs(session, "", 0)
	for _, want := range []string{"--enabled-only", "--no-refresh", "--json", "--timeout"} {
		if !containsSlashCandidate(flags, want) {
			t.Fatalf("candidate %q missing from %+v", want, flags)
		}
	}
	// /accounts 不接受 provider 位置参数：补全不得给出 provider 名。
	if containsSlashCandidate(flags, "alpha") || containsSlashCandidate(flags, "beta") {
		t.Fatalf("/accounts must not complete provider names: %+v", flags)
	}
	if got := completeChatAccountsSlashArgs(session, "--timeout=", 10); !containsSlashCandidate(got, "15s") {
		t.Fatalf("expected timeout values, got %+v", got)
	}
}
