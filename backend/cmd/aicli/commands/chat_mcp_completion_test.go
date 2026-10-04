// /mcp 参数补全契约测试：二级子命令、server 名位、auth 旗标与静默降级。

package commands

import (
	"errors"
	"testing"
	"unicode/utf8"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

func newMCPCompletionTestService() *fakeChatMCPService {
	service := newFakeChatMCPService()
	service.items = []mcpadmin.Item{
		{
			Config: config.MCPConfig{Name: "chrome-mcp", Type: "streamable", Enabled: true},
			Status: &config.MCPStatus{Name: "chrome-mcp", Enabled: true, Connected: true, ToolCount: 27},
		},
		{
			Config: config.MCPConfig{Name: "local-fs", Type: "stdio", Enabled: false},
			Status: &config.MCPStatus{Name: "local-fs", Enabled: false},
		},
		{
			Config: config.MCPConfig{Name: "notion", Type: "streamable", Enabled: true},
			Status: &config.MCPStatus{Name: "notion", Enabled: true, RequiresAuth: true},
		},
	}
	return service
}

func mcpCompletionCommandList(candidates []chatSlashCompletionCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.Command)
	}
	return out
}

func assertMCPCompletionCommands(t *testing.T, got []chatSlashCompletionCandidate, want ...string) {
	t.Helper()
	actual := mcpCompletionCommandList(got)
	if len(actual) != len(want) {
		t.Fatalf("candidates = %v, want %v", actual, want)
	}
	for i := range want {
		if actual[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", actual, want)
		}
	}
}

func completeMCPForTest(service chatMCPService, args string) []chatSlashCompletionCandidate {
	return completeMCPLineSlashArgsWithService(service, args, utf8.RuneCountInString(args))
}

func TestChatMCPSlashCompletionServerNamesForNameSubcommands(t *testing.T) {
	t.Parallel()

	service := newMCPCompletionTestService()
	want := []string{"chrome-mcp", "local-fs", "notion"}
	for _, subcommand := range []string{
		"disable ", "enable ", "status ", "show ", "info ",
		"remove ", "rm ", "delete ", "auth ", "login ",
	} {
		got := completeMCPForTest(service, subcommand)
		assertMCPCompletionCommands(t, got, want...)
	}
}

func TestChatMCPSlashCompletionFiltersServerNamesByPrefix(t *testing.T) {
	t.Parallel()

	service := newMCPCompletionTestService()
	assertMCPCompletionCommands(t, completeMCPForTest(service, "disable chr"), "chrome-mcp")
	assertMCPCompletionCommands(t, completeMCPForTest(service, "status local"), "local-fs")
	assertMCPCompletionCommands(t, completeMCPForTest(service, "auth not"), "notion")
	assertMCPCompletionCommands(t, completeMCPForTest(service, "remove zzz"))
}

func TestChatMCPSlashCompletionSubcommandsForFirstToken(t *testing.T) {
	t.Parallel()

	service := newMCPCompletionTestService()
	all := mcpCompletionCommandList(completeMCPForTest(service, ""))
	for _, want := range []string{"list", "select", "status", "add", "add-json", "enable", "disable", "remove", "reload", "auth", "help"} {
		if !containsSlashCandidate(completeMCPForTest(service, ""), want) {
			t.Fatalf("subcommand %q missing from %v", want, all)
		}
	}
	// 别名仍可出现，但主命令必须排在别名之前（弹窗首屏优先展示主命令）。
	if all[0] != "list" {
		t.Fatalf("expected primary subcommand first, got %v", all)
	}

	assertMCPCompletionCommands(t, completeMCPForTest(service, "dis"), "disable")
	assertMCPCompletionCommands(t, completeMCPForTest(service, "add-j"), "add-json")
}

func TestChatMCPSlashCompletionAuthFlagsAfterName(t *testing.T) {
	t.Parallel()

	service := newMCPCompletionTestService()
	assertMCPCompletionCommands(t, completeMCPForTest(service, "auth chrome-mcp "),
		"--status", "--clear", "--no-browser")
	assertMCPCompletionCommands(t, completeMCPForTest(service, "auth chrome-mcp --c"), "--clear")
}

func TestChatMCPSlashCompletionSessionFlagAfterName(t *testing.T) {
	t.Parallel()

	service := newMCPCompletionTestService()
	assertMCPCompletionCommands(t, completeMCPForTest(service, "disable chrome-mcp "), "--session")
	assertMCPCompletionCommands(t, completeMCPForTest(service, "enable chrome-mcp --s"), "--session")
	// 其它子命令的多余参数位仍不猜测（保持原语义）。
	if got := completeMCPForTest(service, "status chrome-mcp "); len(got) != 0 {
		t.Fatalf("status extra position: expected no candidates, got %v", mcpCompletionCommandList(got))
	}
}

func TestChatMCPSlashCompletionNoCandidatesForNonNamePositions(t *testing.T) {
	t.Parallel()

	service := newMCPCompletionTestService()
	for _, args := range []string{
		"list ",
		"reload ",
		"select ",
		"status chrome-mcp ",
		"bogus ",
	} {
		if got := completeMCPForTest(service, args); len(got) != 0 {
			t.Fatalf("%q: expected no candidates, got %v", args, mcpCompletionCommandList(got))
		}
	}
}

func TestChatMCPSlashCompletionSilentlyDegrades(t *testing.T) {
	t.Parallel()

	if got := completeMCPForTest(nil, "disable "); len(got) != 0 {
		t.Fatalf("nil service: expected no candidates, got %v", mcpCompletionCommandList(got))
	}
	service := newMCPCompletionTestService()
	service.listErr = errors.New("boom")
	if got := completeMCPForTest(service, "disable "); len(got) != 0 {
		t.Fatalf("list error: expected no candidates, got %v", mcpCompletionCommandList(got))
	}
}

func TestChatMCPSlashCompletionSummaryReflectsState(t *testing.T) {
	t.Parallel()

	service := newMCPCompletionTestService()
	got := completeMCPForTest(service, "disable ")
	byName := make(map[string]string, len(got))
	for _, candidate := range got {
		byName[candidate.Command] = candidate.Summary
	}
	want := map[string]string{
		"chrome-mcp": "已连接 · 27 tools",
		"local-fs":   "已停用",
		"notion":     "需认证",
	}
	for name, wantSummary := range want {
		if gotSummary := byName[name]; gotSummary != wantSummary {
			t.Fatalf("%s summary = %q, want %q", name, gotSummary, wantSummary)
		}
	}
}

// 覆盖率护栏：二级命令候选必须覆盖分派 switch 的全部子命令（含别名）。
func TestChatMCPSubcommandCandidatesCoverDispatch(t *testing.T) {
	t.Parallel()

	declared := make(map[string]struct{})
	for _, candidate := range mcpSubcommandArgumentCandidates() {
		declared[candidate.Command] = struct{}{}
	}
	for _, subcommand := range []string{
		"help", "list", "ls", "select", "pick", "menu", "choose", "status", "show", "info",
		"add", "add-json", "remove", "rm", "delete", "enable", "disable", "reload", "auth", "login",
	} {
		if _, ok := declared[subcommand]; !ok {
			t.Fatalf("dispatching subcommand %q is missing from completion candidates", subcommand)
		}
	}
}
