package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// chat_lsp_command_test.go 覆盖 /lsp 命令族：只读状态、按需诊断与手动
// 启动/重启的文本投影，以及注册表/忙时策略的接入。
//
// 池使用真实 Manager + 不存在的 server 二进制：桥接构造即可用（Enabled），
// 启动失败路径完全走「缺失二进制 → unavailable」的真实降级链路（A8/A11），
// 不 spawn 任何常驻进程。

func newChatLSPTestSession(t *testing.T) *ChatSession {
	t.Helper()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = t.TempDir()
	cfg.LSP.Enabled = true
	cfg.LSP.Servers = []runtimelsp.ServerSpec{{
		Name:       "fake-go",
		Command:    "aicli-missing-lsp-binary-for-tests",
		Languages:  []string{"go"},
		Extensions: []string{".go"},
	}}
	return &ChatSession{
		ChatToolManager:   runtimetools.NewDefaultManagerWithRuntimeConfig(nil, cfg),
		RuntimeConfigPath: "/tmp/runtime.yaml",
	}
}

func TestChatLSPCommandTextDisabled(t *testing.T) {
	session := &ChatSession{RuntimeConfigPath: "/tmp/runtime.yaml"}
	for _, line := range []string{"/lsp", "/lsp status", "/lsp list", "/lsp servers", "/lsp diagnostics main.go", "/lsp restart", "/lsp start"} {
		text := chatLSPCommandText(session, line)
		if !strings.Contains(text, "LSP 未启用") {
			t.Fatalf("%q 未按未启用降级：%q", line, text)
		}
		if !strings.Contains(text, "lsp.enabled") {
			t.Fatalf("%q 未给出启用入口：%q", line, text)
		}
	}
}

func TestChatLSPCommandTextNilSession(t *testing.T) {
	for _, line := range []string{"/lsp", "/lsp status", "/lsp diagnostics a.go", "/lsp restart"} {
		if text := chatLSPCommandText(nil, line); !strings.Contains(text, "LSP 未启用") {
			t.Fatalf("%q 在 nil 会话下应降级为未启用，得到 %q", line, text)
		}
	}
}

func TestChatLSPCommandTextUnknownSubcommand(t *testing.T) {
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp nope")
	if !strings.Contains(text, "未知子命令") || !strings.Contains(text, "/lsp diagnostics <file>") {
		t.Fatalf("未知子命令应返回用法，得到 %q", text)
	}
}

func TestChatLSPCommandTextDiagnosticsRequiresFile(t *testing.T) {
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp diagnostics")
	if !strings.Contains(text, "需要指定文件路径") {
		t.Fatalf("缺少文件参数应报错，得到 %q", text)
	}
}

func TestChatLSPCommandTextHelp(t *testing.T) {
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp help")
	for _, want := range []string{"/lsp status", "/lsp diagnostics <file>", "/lsp restart [name]", "/lsp start [name]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("帮助缺少 %q：%q", want, text)
		}
	}
}

func TestChatLSPCommandTextStatusWithPool(t *testing.T) {
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp status")
	for _, want := range []string{"LSP 已启用", "工作区:", "fake-go", "pending first use", "scope=all", "wait_ms="} {
		if !strings.Contains(text, want) {
			t.Fatalf("状态输出缺少 %q：%q", want, text)
		}
	}
}

// 懒启动成员的二进制探测：缺失时状态页提前标注「首次使用将降级」，
// 存在时不产生噪音（与自动扫描 filterRunnableServerSpecs 共用 lspLookPath）。
func TestChatLSPStatusAnnotatesMissingBinaryForPendingServers(t *testing.T) {
	stubLSPLookPath(t) // 全部视为缺失
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp status")
	if !strings.Contains(text, "未找到可执行文件 aicli-missing-lsp-binary-for-tests") {
		t.Fatalf("pending 成员缺失二进制应提前标注：%q", text)
	}

	stubLSPLookPath(t, "aicli-missing-lsp-binary-for-tests")
	text = chatLSPCommandText(newChatLSPTestSession(t), "/lsp status")
	if strings.Contains(text, "未找到可执行文件") {
		t.Fatalf("二进制存在时不应产生噪音标注：%q", text)
	}
}

// 普通 TUI 会话（无 profile 投影、RuntimeConfigPath 为空）也应在状态页显示
// 项目层配置来源：回退到 <workspace>/.aicli/runtime.yaml。
func TestChatLSPStatusShowsWorkspaceConfigPathFallback(t *testing.T) {
	stubLSPLookPath(t, "aicli-missing-lsp-binary-for-tests")
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = t.TempDir()
	cfg.LSP.Enabled = true
	cfg.LSP.Servers = []runtimelsp.ServerSpec{{
		Name:       "fake-go",
		Command:    "aicli-missing-lsp-binary-for-tests",
		Extensions: []string{".go"},
	}}
	manager := runtimetools.NewDefaultManagerWithRuntimeConfig(nil, cfg)
	session := &ChatSession{ChatToolManager: manager}

	root := manager.LSPRoot()
	if strings.TrimSpace(root) == "" {
		t.Fatal("测试管理器未绑定工作区根目录")
	}
	path := agentconfig.WorkspaceRuntimeConfigPath(root)
	if path == "" {
		t.Fatal("工作区层 runtime.yaml 路径解析为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建 .aicli 目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("lsp:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("写入工作区 runtime.yaml 失败: %v", err)
	}

	text := chatLSPCommandText(session, "/lsp status")
	if !strings.Contains(text, "配置: "+path) {
		t.Fatalf("状态页应回退显示工作区配置路径 %s：%q", path, text)
	}
}

func TestChatLSPCommandTextDiagnosticsUnhandledFile(t *testing.T) {
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp diagnostics notes.txt")
	if !strings.Contains(text, "未覆盖该文件") {
		t.Fatalf("非归属文件应明确跳过，得到 %q", text)
	}
}

func TestChatLSPCommandTextDiagnosticsDegradesOnMissingServer(t *testing.T) {
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp diagnostics main.go")
	if strings.TrimSpace(text) == "" {
		t.Fatal("归属文件的诊断读取必须有可见输出（降级提示）")
	}
	if !strings.Contains(text, "main.go") {
		t.Fatalf("降级提示应包含目标文件：%q", text)
	}
}

func TestChatLSPCommandTextStartFailureSurfacesReason(t *testing.T) {
	session := newChatLSPTestSession(t)
	text := chatLSPCommandText(session, "/lsp start")
	if !strings.Contains(text, "启动 fake-go 失败") {
		t.Fatalf("缺失二进制应返回启动失败，得到 %q", text)
	}
	// 失败后状态应转为 unavailable 并带失败原因（可观测，不中断会话）。
	status := chatLSPCommandText(session, "/lsp status")
	if !strings.Contains(status, "unavailable") {
		t.Fatalf("启动失败后状态应为 unavailable：%q", status)
	}
}

func TestChatLSPCommandTextRestartUnknownServer(t *testing.T) {
	text := chatLSPCommandText(newChatLSPTestSession(t), "/lsp restart nope")
	if !strings.Contains(text, "重启 nope 失败") {
		t.Fatalf("未知 server 应返回失败原因，得到 %q", text)
	}
}

// 注册表接入：只读长文档忙时走副屏通道（S，批次 3 尾批同款），help 内联
// 立即执行（I），restart/start 忙时排队（D）。
func TestChatLSPCommandBusyPolicy(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	cases := []struct {
		line string
		want chatBusyCommandPolicy
	}{
		{"/lsp", chatBusyPolicyScreen},
		{"/lsp status", chatBusyPolicyScreen},
		{"/lsp list", chatBusyPolicyScreen},
		{"/lsp servers", chatBusyPolicyScreen},
		{"/lsp diagnostics main.go", chatBusyPolicyScreen},
		{"/lsp help", chatBusyPolicyImmediate},
		{"/lsp restart", chatBusyPolicyDeferred},
		{"/lsp restart fake-go", chatBusyPolicyDeferred},
		{"/lsp start", chatBusyPolicyDeferred},
	}
	for _, tc := range cases {
		if got := chatSlashCommandBusyPolicyFor(tc.line); got != tc.want {
			t.Errorf("%q 忙时策略 = %s，期望 %s", tc.line, got, tc.want)
		}
	}
}

// 未知子命令由注册表 fail-safe 收敛为 queue（不丢输入），执行时给出用法错误。
func TestChatLSPCommandUnknownSubcommandFallsBackToQueue(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	if got := chatSlashCommandBusyPolicyFor("/lsp nope"); got != chatBusyPolicyDeferred {
		t.Fatalf("/lsp nope 应 fail-safe 入队，实际 %s", got)
	}
}

// 统一结构化路径必须认领 /lsp（handleCommand 的 JSON/legacy 出口与
// tryExecuteStructuredChatCommand 的注册表路由同源），nil 会话也要降级认领。
func TestChatLSPCommandStructuredDispatch(t *testing.T) {
	session := newChatLSPTestSession(t)
	result, handled, err := tryExecuteStructuredChatCommand(session, "/lsp status")
	if !handled || err != nil {
		t.Fatalf("tryExecuteStructuredChatCommand(/lsp status) handled=%v err=%v", handled, err)
	}
	if len(result.Blocks) == 0 {
		t.Fatal("/lsp status 应产出内联文本单元")
	}
	if _, handled, err := tryExecuteStructuredChatCommand(nil, "/lsp"); err != nil || !handled {
		t.Fatalf("nil 会话 /lsp 应被结构化路径认领：handled=%v err=%v", handled, err)
	}
}

// 只读长文档在具备副屏能力时按 §5.1 迁入 ScreenDocument（批次 3 口径）；
// 短输出（未覆盖文件的说明）、restart/start 回执、help 与未启用提示保持内联。
func TestChatLSPReadOnlyVariantsUseScreenDocument(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)

	session := newChatLSPTestSession(t)
	result, handled, err := tryExecuteStructuredChatCommand(session, "/lsp status")
	if err != nil {
		t.Fatalf("tryExecuteStructuredChatCommand(/lsp status) err=%v", err)
	}
	assertBatch3ScreenDocument(t, result, handled, "lsp.status", "LSP 已启用", "fake-go")

	// 身份表覆盖 diagnostics 变体（正文只有超预算时才开屏，见下方短输出用例）。
	if id, _, ok := chatLSPReadOnlyScreenIdentity("/lsp diagnostics main.go"); !ok || id != "lsp.diagnostics" {
		t.Fatalf("/lsp diagnostics 副屏身份 = %q ok=%v，期望 lsp.diagnostics", id, ok)
	}
	if _, _, ok := chatLSPReadOnlyScreenIdentity("/lsp restart"); ok {
		t.Fatal("/lsp restart 不应有副屏身份")
	}
	if _, _, ok := chatLSPReadOnlyScreenIdentity("/lsp help"); ok {
		t.Fatal("/lsp help 不应有副屏身份")
	}

	// 短输出（缺失 server 的降级提示只有一行）不为一屏空态闪全屏。
	result, handled, err = tryExecuteStructuredChatCommand(session, "/lsp diagnostics main.go")
	if !handled || err != nil {
		t.Fatalf("tryExecuteStructuredChatCommand(/lsp diagnostics main.go) handled=%v err=%v", handled, err)
	}
	if result.Screen != nil {
		t.Fatalf("短诊断输出不应进入副屏：%+v", *result.Screen)
	}

	// 写入类回执与用法卡保持内联。
	for _, line := range []string{"/lsp restart", "/lsp start", "/lsp help"} {
		result, handled, err := tryExecuteStructuredChatCommand(session, line)
		if !handled || err != nil {
			t.Fatalf("tryExecuteStructuredChatCommand(%s) handled=%v err=%v", line, handled, err)
		}
		if result.Screen != nil {
			t.Fatalf("%s 不应进入副屏", line)
		}
	}

	// 未启用池的提示卡（A11）即使能力满足也不占副屏。
	disabled := &ChatSession{}
	result, handled, err = tryExecuteStructuredChatCommand(disabled, "/lsp status")
	if !handled || err != nil {
		t.Fatalf("tryExecuteStructuredChatCommand(未启用 /lsp status) handled=%v err=%v", handled, err)
	}
	if result.Screen != nil {
		t.Fatal("LSP 未启用提示不应进入副屏")
	}
}
