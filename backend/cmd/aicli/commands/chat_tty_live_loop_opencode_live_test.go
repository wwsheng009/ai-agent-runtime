package commands

// 真实 provider TTY live smoke：opencode.ai + deepseek-v4-flash（reasoning effort max）。
//
// 与 chat_tty_live_loop_test.go 的 fake executor 测试不同，本测试用真实
// bootstrapChatSession 构造会话（真实 provider / 真实 executor / 真实 LLM
// 请求），再复用 driveTTYLiveLoop 驱动真实主循环 + 真实 stdin 注入 +
// 真实渲染字节流 + vt.Screen 屏幕级断言。
//
// env 门控（默认 skip，完整包回归不受影响）：
//
//	AICLI_LIVE_OPENCODE_TTY_TEST=1
//
// API key 解析顺序：OPENCODE_API_KEY 环境变量 → opencode CLI 本地认证
// （~/.local/share/opencode/auth.json 的 opencode-go 条目 → deepseek 条目）。
// 两者都无则 skip。
// BaseURL 默认 https://opencode.ai/zen/go（opencode-console-go-2026-07 方言），
// 可用 AICLI_LIVE_OPENCODE_BASE_URL 覆盖。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

const (
	liveOpenCodeTTYTestEnv = "AICLI_LIVE_OPENCODE_TTY_TEST"
	liveOpenCodeProvider   = "opencode.ai"
	liveOpenCodeModel      = "deepseek-v4-flash"
	liveOpenCodeEffort     = "max"
	liveOpenCodeDefaultURL = "https://opencode.ai/zen/go"
)

// ttyCountingExecutor 包装真实 executor，统计 Execute 调用次数（验证输入
// 真实到达 provider 路径，而非被 slash 命令/输入队列吞掉）。
type ttyCountingExecutor struct {
	inner aicliChatExecutor
	mu    sync.Mutex
	calls int
}

func (e *ttyCountingExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.inner.Execute(ctx, session, prompt)
}

func (e *ttyCountingExecutor) RuntimeDescriptor() aicliRuntimeExecutorDescriptor {
	return e.inner.RuntimeDescriptor()
}

func (e *ttyCountingExecutor) ToolAvailable(session *ChatSession, toolName string) bool {
	return e.inner.ToolAvailable(session, toolName)
}

func (e *ttyCountingExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// liveOpenCodeAPIKey 依次解析 OPENCODE_API_KEY 与 opencode CLI auth.json。
func liveOpenCodeAPIKey(t *testing.T) string {
	t.Helper()
	if key := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")); key != "" {
		return key
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	authPath := filepath.Join(homeDir, ".local", "share", "opencode", "auth.json")
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return ""
	}
	var auth map[string]struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Logf("opencode auth.json 解析失败: %v", err)
		return ""
	}
	if key := strings.TrimSpace(auth["opencode-go"].Key); key != "" {
		return key
	}
	return strings.TrimSpace(auth["deepseek"].Key)
}

// injectLiveOpenCodeProvider 把 opencode.ai provider 条目注入 cfg（config 中
// 通常没有该条目；live 测试需要独立于配置文件完成接线）。
func injectLiveOpenCodeProvider(t *testing.T, cfg *config.Config) config.Provider {
	t.Helper()
	if cfg == nil || cfg.Providers.Items == nil {
		t.Fatal("expected config with providers")
	}
	if existing, ok := cfg.Providers.Items[liveOpenCodeProvider]; ok {
		return existing
	}
	baseURL := strings.TrimSpace(os.Getenv("AICLI_LIVE_OPENCODE_BASE_URL"))
	if baseURL == "" {
		baseURL = liveOpenCodeDefaultURL
	}
	provider := config.Provider{
		Enabled:      true,
		Protocol:     "openai",
		BaseURL:      baseURL,
		APIKey:       liveOpenCodeAPIKey(t),
		DefaultModel: liveOpenCodeModel,
		SupportedModels: []string{
			liveOpenCodeModel,
			"deepseek-v4-pro",
		},
		Compatibility: config.CompatibilityConfig{
			Profile: config.CompatibilityProfileOpenCodeConsoleGo,
		},
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			liveOpenCodeModel: {
				ReasoningModel:   true,
				ReasoningEfforts: []string{"high", "max"},
			},
		},
		MaxTokensLimit: 8000,
		Timeout:        300 * time.Second,
	}
	cfg.Providers.Items[liveOpenCodeProvider] = provider
	return provider
}

// openCodeLiveSession 完成 opencode.ai live 接线并返回已包装 counting
// executor 的真实会话：加载仓库 config → 注入 opencode.ai provider →
// prepareChatPersistence/prepareChatRuntimeState/bootstrapChatSession →
// 交互模式翻转 → 管道 stdin 注入接线。env 未门控时返回 (nil, nil, nil)
// 由调用方 skip；凭据缺失时直接 t.Skipf。
func openCodeLiveSession(t *testing.T) (*ChatSession, config.Provider, *ttyCountingExecutor) {
	t.Helper()
	if os.Getenv(liveOpenCodeTTYTestEnv) != "1" {
		return nil, config.Provider{}, nil
	}
	root := findCommandsRepoRoot(t)
	configPath := filepath.Join(root, "configs", "config.yaml")
	envPath := filepath.Join(root, "configs", ".env")
	if _, err := os.Stat(envPath); err == nil {
		if loadErr := godotenv.Overload(envPath); loadErr != nil {
			t.Fatalf("godotenv.Overload: %v", loadErr)
		}
	}
	cfgManager, err := config.NewManager(configPath)
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	cfg := cfgManager.Config()
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.AICLI != nil && cfg.AICLI.MCP != nil {
		cfg.AICLI.MCP.AutoConnect = false
	}

	provider := injectLiveOpenCodeProvider(t, cfg)
	if strings.TrimSpace(provider.APIKey) == "" {
		t.Skip("no OPENCODE_API_KEY / opencode auth deepseek key; set OPENCODE_API_KEY to enable live opencode.ai TTY smoke")
	}

	sessionRoot := t.TempDir()
	opts := &chatCommandOptions{
		ProviderFlag:           liveOpenCodeProvider,
		ProviderChanged:        true,
		ModelFlag:              liveOpenCodeModel,
		ModelChanged:           true,
		StreamFlag:             false,
		StreamChanged:          true,
		NoInteractive:          true,
		LogDir:                 filepath.Join(sessionRoot, "logs"),
		SessionDirFlag:         filepath.Join(sessionRoot, "sessions"),
		ReasoningEffortFlag:    liveOpenCodeEffort,
		ReasoningEffortChanged: true,
		PermissionMode:         runtimepolicy.ModeBypassPermissions,
		OutputFormat:           "text",
	}

	persistenceState, err := prepareChatPersistence(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatPersistence: %v", err)
	}
	if persistenceState.runtimeSessionManager != nil {
		t.Cleanup(persistenceState.runtimeSessionManager.Stop)
	}

	runtimeState, details, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v details=%v", err, details)
	}
	if runtimeState == nil || runtimeState.providerName != liveOpenCodeProvider {
		t.Fatalf("expected runtime provider %q, got %+v", liveOpenCodeProvider, runtimeState)
	}
	if runtimeState.modelName != liveOpenCodeModel {
		t.Fatalf("expected runtime model %q, got %q", liveOpenCodeModel, runtimeState.modelName)
	}
	if !strings.EqualFold(strings.TrimSpace(runtimeState.reasoningEffort), liveOpenCodeEffort) {
		t.Fatalf("expected reasoning effort %q, got %q", liveOpenCodeEffort, runtimeState.reasoningEffort)
	}

	session, cleanup, err := bootstrapChatSession(cfg, opts, nil, persistenceState, runtimeState)
	if err != nil {
		t.Fatalf("bootstrapChatSession: %v", err)
	}
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if session.ChatExecutor == nil {
		t.Fatal("expected real chat executor")
	}
	session.NoInteractive = false
	session.JSONOutput = false
	session.OutputFormat = "interactive"
	t.Logf("ui.IsInteractiveTerminal()=%v (drive 后 os.Stdin 为管道)", ui.IsInteractiveTerminal())
	// 输入注入接线：bootstrap 的 InputBox 绑定 layout，非 TTY 管道 stdin 下
	// 读取路径与 fake TTY 测试不一致（实测读不到管道输入）；换用 layout=nil
	// 的 InputBox 与 chat_tty_live_loop_test.go 完全一致（ui.IsInteractiveTerminal
	// 为 false 时走 readBufferedLine(os.Stdin)，管道注入稳定）。真实主循环、
	// 真实 executor 与真实 LLM 请求不受影响。
	session.InputBox = ui.NewInputBox(nil)

	counter := &ttyCountingExecutor{inner: session.ChatExecutor}
	session.ChatExecutor = counter

	t.Logf("live opencode.ai TTY smoke: provider=%s model=%s effort=%s base_url=%s",
		liveOpenCodeProvider, liveOpenCodeModel, liveOpenCodeEffort, provider.BaseURL)
	return session, provider, counter
}

// TestTTY_LiveLoop_RealProvider_OpenCode_DeepSeekV4FlashMax 用真实 provider
// opencode.ai + 模型 deepseek-v4-flash（reasoning effort max）驱动真实交互
// 主循环：stdin 注入 "hi" 与 "/exit"，屏幕级断言渲染健康且 LLM 调用真实发生。
func TestTTY_LiveLoop_RealProvider_OpenCode_DeepSeekV4FlashMax(t *testing.T) {
	session, _, counter := openCodeLiveSession(t)
	if session == nil {
		t.Skip("set AICLI_LIVE_OPENCODE_TTY_TEST=1 to enable live opencode.ai TTY smoke")
	}

	raw := driveTTYLiveLoop(t, session, []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: "hi\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, 5*time.Minute)

	if counter.callCount() == 0 {
		screen := vt.NewScreen(80, 24)
		screen.Feed(raw)
		t.Logf("--- raw bytes (%d) ---\n%q", len(raw), raw)
		t.Logf("--- rendered screen ---\n%s", screen.Dump())
		if session != nil && session.Interaction != nil {
			t.Logf("interaction ready=%v", session.Interaction.IsReady())
		}
		t.Fatalf("real executor 未被调用（输入未到达 provider 路径）")
	}
	if strings.TrimSpace(raw) == "" {
		t.Fatalf("真实循环未产生任何渲染输出")
	}

	screen := vt.NewScreen(80, 24)
	screen.Feed(raw)
	lines := screen.Lines(0, 24)
	t.Logf("--- live opencode.ai rendered screen (%d bytes, %d lines) ---\n%s",
		len(raw), len(lines), screen.Dump())
	assertNoAdjacentDuplicateLines(t, lines)

	// 屏幕健康哨兵：至少有一行非空内容（用户输入回显 / 状态行 / assistant 回复）。
	nonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		t.Fatalf("屏幕全空：渲染未呈现任何内容")
	}
}

// TestTTY_LiveLoop_RealProvider_OpenCode_LongOutput 超一屏 live 渲染：
// 要求 deepseek-v4-flash max 输出 40 行编号长文（> 24 行视口），验证真实
// LLM 长输出经真实主循环渲染后完整写出（字节流含首尾行）、滚动正常
// （无相邻重复行），并打印最终屏幕供人工核查。
func TestTTY_LiveLoop_RealProvider_OpenCode_LongOutput(t *testing.T) {
	session, _, counter := openCodeLiveSession(t)
	if session == nil {
		t.Skip("set AICLI_LIVE_OPENCODE_TTY_TEST=1 to enable live opencode.ai TTY smoke")
	}

	prompt := "请输出一篇关于 Go 语言并发的短文：恰好 40 个段落，每个段落单独一行，每行以“第1段：”“第2段：”……“第40段：”开头，每行不超过 40 个汉字。不要输出除这 40 行以外的任何内容。"
	raw := driveTTYLiveLoop(t, session, []ttyLiveScriptStep{
		{wait: 700 * time.Millisecond, line: prompt + "\n"},
		{waitReady: true, wait: 100 * time.Millisecond, line: "/exit\n"},
	}, 5*time.Minute)

	if counter.callCount() == 0 {
		t.Fatalf("real executor 未被调用（长文输入未到达 provider 路径）")
	}
	if strings.TrimSpace(raw) == "" {
		t.Fatalf("真实循环未产生任何渲染输出")
	}
	// 超一屏证据：40 行 > 24 行视口。头部行（第1段）与尾部行（第40段）
	// 都必须出现在渲染字节流中——内容完整渲染，未被截断/截短。
	if !strings.Contains(raw, "第1段") {
		t.Errorf("渲染字节流中未找到长文头部（第1段）; raw head=%q", raw[:min(len(raw), 200)])
	}
	if !strings.Contains(raw, "第40段") {
		t.Errorf("渲染字节流中未找到长文尾部（第40段）——超过一屏的内容未完整渲染")
	}

	screen := vt.NewScreen(80, 24)
	screen.Feed(raw)
	lines := screen.Lines(0, 24)
	t.Logf("--- live opencode.ai long-output rendered screen (%d bytes, %d lines) ---\n%s",
		len(raw), len(lines), screen.Dump())
	assertNoAdjacentDuplicateLines(t, lines)
}
