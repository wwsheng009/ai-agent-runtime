package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 方案 §5.2/§5.4：`/routing` 命令面（行内文本一族 + 三层写入）单测。

// routingCommandSessionWithOverride 构造带 session 覆盖的最小会话：
// hard = claude-opus-4 · anthropic · xhigh，来源应为 session。
func routingCommandSessionWithOverride(t *testing.T) *ChatSession {
	t.Helper()
	override := &agentconfig.AICLISessionRoutingOverride{
		MainAgent: &agentconfig.AICLISessionMainAgentRoutingOverride{
			Enabled:           routingTestBoolPtr(true),
			Levels:            routingTestStringsPtr("easy", "normal", "hard"),
			DefaultDifficulty: routingTestStringPtr("hard"),
			Profiles: map[string]agentconfig.AICLISessionRouteProfileOverride{
				"hard": {
					Provider:        routingTestStringPtr("anthropic"),
					Model:           routingTestStringPtr("claude-opus-4"),
					ReasoningEffort: routingTestStringPtr("xhigh"),
				},
			},
		},
		UpdatedBy: "tui",
	}
	return routingStatusTestSession(t, override, &agentconfig.AICLIConfig{})
}

func TestChatRoutingCommandShowRendersSessionOverride(t *testing.T) {
	session := routingCommandSessionWithOverride(t)

	text, handled := chatRoutingCommandText(session, "/routing show")
	if !handled {
		t.Fatal("/routing show 必须被结构化入口接管")
	}
	for _, want := range []string{"路由（main）· 启用", "hard", "claude-opus-4", "xhigh", "(session*)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("show 输出缺少 %q：\n%s", want, text)
		}
	}
}

func TestChatRoutingCommandShowJSONMatchesProjection(t *testing.T) {
	session := routingCommandSessionWithOverride(t)

	text, handled := chatRoutingCommandText(session, "/routing show --json")
	if !handled {
		t.Fatal("/routing show --json 必须被接管")
	}
	var payload struct {
		Scope   string `json:"scope"`
		Routing struct {
			Enabled bool   `json:"enabled"`
			Level   string `json:"level"`
			Model   string `json:"model"`
			Source  string `json:"source"`
		} `json:"routing"`
		Levels []struct {
			Level string `json:"level"`
		} `json:"levels"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("show --json 输出不是合法 JSON: %v\n%s", err, text)
	}
	if payload.Scope != "main" || !payload.Routing.Enabled || payload.Routing.Level != "hard" {
		t.Fatalf("JSON 投影不符：%+v", payload)
	}
	if payload.Routing.Model != "claude-opus-4" || payload.Routing.Source != string(agentconfig.RoutingSourceSession) {
		t.Fatalf("JSON 投影字段不符：%+v", payload.Routing)
	}
	if len(payload.Levels) == 0 {
		t.Fatalf("JSON 应包含逐级表格：%+v", payload.Levels)
	}
}

// TestChatRoutingCommandWriteThenReset 覆盖 U-1 同口径的 TUI 写入链路：
// on → level 糖写入 → show 反映 → reset 清除（context 键回收）。
func TestChatRoutingCommandWriteThenReset(t *testing.T) {
	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	text, handled := chatRoutingCommandText(session, "/routing on")
	if !handled {
		t.Fatal("/routing on 必须被接管")
	}
	if strings.Contains(text, "错误") {
		t.Fatalf("/routing on 失败：%s", text)
	}
	override, err := agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session))
	if err != nil || override == nil || override.MainAgent == nil || override.MainAgent.Enabled == nil || !*override.MainAgent.Enabled {
		t.Fatalf("on 未写入 enabled：err=%v override=%+v", err, override)
	}

	text, handled = chatRoutingCommandText(session, "/routing main level hard model claude-opus-4")
	if !handled || strings.Contains(text, "错误") {
		t.Fatalf("level 糖写入失败：%s", text)
	}
	override, err = agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session))
	if err != nil || override == nil || override.MainAgent == nil {
		t.Fatalf("写入后解码失败：%v", err)
	}
	profile, ok := override.MainAgent.Profiles["hard"]
	if !ok || profile.Model == nil || *profile.Model != "claude-opus-4" {
		t.Fatalf("profiles.hard.model 未写入：%+v", override.MainAgent.Profiles)
	}
	if override.UpdatedBy != "aicli-tui" {
		t.Fatalf("UpdatedBy = %q，期望 aicli-tui", override.UpdatedBy)
	}

	showText, _ := chatRoutingCommandText(session, "/routing show")
	if !strings.Contains(showText, "claude-opus-4") {
		t.Fatalf("写入后 show 未反映新值：\n%s", showText)
	}

	resetText, handled := chatRoutingCommandText(session, "/routing reset main")
	if !handled || !strings.Contains(resetText, "已清除会话路由覆盖") {
		t.Fatalf("reset 输出不符：%s", resetText)
	}
	if raw := chatSessionRoutingOverrideRaw(session); raw != "" {
		t.Fatalf("reset 后 context 键应被回收，得到 %q", raw)
	}
}

func TestChatRoutingCommandRejectsUnknownKey(t *testing.T) {
	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	text, handled := chatRoutingCommandText(session, "/routing main bogus.key value")
	if !handled {
		t.Fatal("未知键仍必须被接管（返回错误文本）")
	}
	if !strings.Contains(text, "错误") {
		t.Fatalf("未知键应返回错误：%s", text)
	}
	if raw := chatSessionRoutingOverrideRaw(session); raw != "" {
		t.Fatalf("校验失败不得落盘，得到 %q", raw)
	}
}

// TestChatRoutingCommandWriteLayerBoundaries 钉住 §5.4 写入层边界（I-2 负路径）：
// 未知层被拒绝；workspace 层已落地（I-2），不得再返回「尚未在此版本启用」占位，
// 未绑定工作区的会话给出明确错误。
func TestChatRoutingCommandWriteLayerBoundaries(t *testing.T) {
	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	text, handled := chatRoutingCommandText(session, "/routing main enabled true --to global")
	if !handled {
		t.Fatal("未知写入层仍必须被接管")
	}
	if !strings.Contains(text, "未知写入层") {
		t.Fatalf("未知层应被拒绝：%s", text)
	}

	text, handled = chatRoutingCommandText(session, "/routing main enabled true --to workspace")
	if !handled {
		t.Fatal("--to workspace 仍必须被接管")
	}
	if !strings.Contains(text, "未绑定工作区") {
		t.Fatalf("未绑定工作区应给出明确错误：%s", text)
	}
	if strings.Contains(text, "尚未在此版本启用") {
		t.Fatalf("workspace 层已落地（I-2），不得再返回占位文案：%s", text)
	}
}

// TestChatRoutingCommandWritesWorkspaceLayer 覆盖 §10.2 I-2 正路径：
// workspace 层写入落到会话绑定工作区的偏好文件（N9：不随 cwd 漂移），
// 字段级合并保留既有值、不污染会话覆盖，reset 后逐层回退可见。
func TestChatRoutingCommandWritesWorkspaceLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	workspace := t.TempDir()
	targetPath := agentconfig.WorkspaceRoutingTargetPath(workspace)
	if strings.TrimSpace(targetPath) == "" {
		t.Skip("当前环境无法解析 workspace 偏好文件路径")
	}

	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	session.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = workspace

	// §3.5 校验：enabled 要求 levels 已列出（与 API 同源），先写档位表再开启。
	if _, handled := chatRoutingCommandText(session, "/routing main levels easy,normal,hard --to workspace"); !handled {
		t.Fatal("档位表写入必须被接管")
	}
	text, handled := chatRoutingCommandText(session, "/routing main enabled true --to workspace")
	if !handled {
		t.Fatal("--to workspace 写入必须被接管")
	}
	if !strings.Contains(text, targetPath) {
		t.Fatalf("写入结果应回显目标文件路径 %q：%s", targetPath, text)
	}
	prefs, err := agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspace)
	if err != nil {
		t.Fatalf("读取 workspace 偏好: %v", err)
	}
	if prefs == nil || prefs.MainAgent == nil || !prefs.MainAgent.Enabled {
		t.Fatalf("workspace 层应记录 enabled=true：%+v", prefs)
	}
	if raw := chatSessionRoutingOverrideRaw(session); raw != "" {
		t.Fatalf("workspace 写入不得改动会话覆盖，得到 %q", raw)
	}
	if projection := chatRoutingProjectionForScope(session, chatRoutingScopeMain); !projection.Enabled {
		t.Fatalf("workspace 写入应被解析器消费（下一 turn 生效）：%+v", projection)
	}

	// 字段级合并：档位写入保留已写的 enabled。
	if _, handled := chatRoutingCommandText(session, "/routing main profiles.hard.model claude-opus-4 --to workspace"); !handled {
		t.Fatal("档位写入必须被接管")
	}
	prefs, err = agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspace)
	if err != nil {
		t.Fatalf("读取 workspace 偏好: %v", err)
	}
	if prefs == nil || prefs.MainAgent == nil || !prefs.MainAgent.Enabled {
		t.Fatalf("字段级合并不应丢失 enabled：%+v", prefs)
	}
	if _, ok := prefs.MainAgent.Profiles["hard"]; !ok {
		t.Fatalf("档位写入未落盘：%+v", prefs.MainAgent.Profiles)
	}

	// 逐层回退：清除 workspace 层后不再有主 Agent 覆盖。
	text, handled = chatRoutingCommandText(session, "/routing reset main --to workspace")
	if !handled {
		t.Fatal("reset --to workspace 必须被接管")
	}
	if !strings.Contains(text, "已清除工作区路由偏好") {
		t.Fatalf("reset 结果应说明清除：%s", text)
	}
	prefs, err = agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspace)
	if err != nil {
		t.Fatalf("读取 workspace 偏好: %v", err)
	}
	if prefs != nil && prefs.MainAgent != nil {
		t.Fatalf("清除后不应残留主 Agent 覆盖：%+v", prefs.MainAgent)
	}
}

// TestChatRoutingCommandSaveToWorkspaceLayer 覆盖 §5.4 `/routing save`：
// 无参数默认会话层（随会话持久化，无需额外保存）；`save workspace` 把会话覆盖
// 整体物化到工作区层且不自动清除会话层（会话层仍优先，需显式 reset --to session）。
func TestChatRoutingCommandSaveToWorkspaceLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	workspace := t.TempDir()
	if strings.TrimSpace(agentconfig.WorkspaceRoutingTargetPath(workspace)) == "" {
		t.Skip("当前环境无法解析 workspace 偏好文件路径")
	}

	session := routingCommandSessionWithOverride(t)
	session.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = workspace

	// 无参数：默认会话层，明确告知无需额外保存。
	text, handled := chatRoutingCommandText(session, "/routing save")
	if !handled {
		t.Fatal("save 必须被接管")
	}
	if !strings.Contains(text, "随会话持久化") {
		t.Fatalf("默认应说明会话层随会话持久化：%s", text)
	}

	// save workspace：整体物化到工作区层（字段级合并写入）。
	text, handled = chatRoutingCommandText(session, "/routing save workspace")
	if !handled {
		t.Fatal("save workspace 必须被接管")
	}
	if !strings.Contains(text, "已保存到工作区路由偏好") || !strings.Contains(text, "会话层覆盖仍优先") {
		t.Fatalf("save workspace 结果应说明落点与优先级：%s", text)
	}
	prefs, err := agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspace)
	if err != nil {
		t.Fatalf("读取 workspace 偏好: %v", err)
	}
	if prefs == nil || prefs.MainAgent == nil || !prefs.MainAgent.Enabled {
		t.Fatalf("workspace 层应记录主 Agent 覆盖：%+v", prefs)
	}
	if _, ok := prefs.MainAgent.Profiles["hard"]; !ok {
		t.Fatalf("save 应物化档位字段：%+v", prefs.MainAgent.Profiles)
	}
	if got := fmt.Sprintf("%+v", prefs.MainAgent.Profiles["hard"]); !strings.Contains(got, "claude-opus-4") {
		t.Fatalf("save 应物化档位模型：%s", got)
	}
	if raw := chatSessionRoutingOverrideRaw(session); raw == "" {
		t.Fatal("save 不得自动清除会话层（需显式 reset --to session）")
	}

	// 显式回落：清除会话层后，解析器应消费刚保存的工作区层（下一 turn 生效）。
	if _, handled := chatRoutingCommandText(session, "/routing reset main --to session"); !handled {
		t.Fatal("reset --to session 必须被接管")
	}
	if raw := chatSessionRoutingOverrideRaw(session); raw != "" {
		t.Fatalf("会话层清除后不应残留覆盖：%q", raw)
	}
	if projection := chatRoutingProjectionForScope(session, chatRoutingScopeMain); !projection.Enabled {
		t.Fatalf("回落应命中刚保存的 workspace 层：%+v", projection)
	}
}

// TestChatRoutingCommandConfigLayerRequiresConfirm 覆盖 §5.4 config 层写入规则：
// 目标文件按层路由（无项目层 → 用户层）、未带 --yes 拒绝且不落盘、
// 带 --yes 落盘并经 reset --yes 清除。
func TestChatRoutingCommandConfigLayerRequiresConfirm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	chdirTest(t, t.TempDir())

	configPath, _ := agentconfig.AICLIConfigWriteTargetForRouting()
	if strings.TrimSpace(configPath) == "" {
		t.Skip("当前环境无法解析 config 写入目标")
	}

	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	// 未确认时不得落盘（先写档位表，同样受二次确认约束）。
	text, handled := chatRoutingCommandText(session, "/routing main levels easy,normal,hard --to config")
	if !handled {
		t.Fatal("config 层写入必须被接管")
	}
	if !strings.Contains(text, "--yes") {
		t.Fatalf("config 层写入必须要求二次确认：%s", text)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("未确认时不得落盘（%s）: %v", configPath, err)
	}

	if _, handled := chatRoutingCommandText(session, "/routing main levels easy,normal,hard --to config --yes"); !handled {
		t.Fatal("确认后的 config 层写入必须被接管")
	}
	text, handled = chatRoutingCommandText(session, "/routing main enabled true --to config --yes")
	if !handled {
		t.Fatal("确认后的 config 层写入必须被接管")
	}
	if !strings.Contains(text, configPath) {
		t.Fatalf("写入结果应回显目标路径 %q：%s", configPath, text)
	}
	reloaded, err := agentconfig.ReloadGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("重新读取配置: %v", err)
	}
	if reloaded == nil || reloaded.AICLI == nil || reloaded.AICLI.MainAgent == nil ||
		reloaded.AICLI.MainAgent.Routing == nil || !reloaded.AICLI.MainAgent.Routing.Enabled {
		t.Fatalf("config 层未落盘 enabled=true：%+v", reloaded)
	}

	// reset 同样需要 --yes，清除后不再残留启用态。
	text, handled = chatRoutingCommandText(session, "/routing reset main --to config")
	if !handled {
		t.Fatal("config 层 reset 必须被接管")
	}
	if !strings.Contains(text, "--yes") {
		t.Fatalf("config 层清除必须要求二次确认：%s", text)
	}
	text, handled = chatRoutingCommandText(session, "/routing reset main --to config --yes")
	if !handled {
		t.Fatal("确认后的 config 层清除必须被接管")
	}
	if !strings.Contains(text, "已清除") {
		t.Fatalf("清除结果应说明清除：%s", text)
	}
	reloaded, err = agentconfig.ReloadGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("重新读取配置: %v", err)
	}
	if reloaded != nil && reloaded.AICLI != nil && reloaded.AICLI.MainAgent != nil &&
		reloaded.AICLI.MainAgent.Routing != nil && reloaded.AICLI.MainAgent.Routing.Enabled {
		t.Fatalf("清除后不应残留启用态：%+v", reloaded.AICLI.MainAgent.Routing)
	}
}

func TestChatRoutingCommandUsageOnUnknownSubcommand(t *testing.T) {
	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	text, handled := chatRoutingCommandText(session, "/routing bogus")
	if !handled || !strings.Contains(text, "用法:") {
		t.Fatalf("未知子命令应回退到用法：%s", text)
	}
}

func TestChatRoutingCommandPanelEntryDegradesToSummary(t *testing.T) {
	session := routingCommandSessionWithOverride(t)

	text, handled := chatRoutingCommandText(session, "/routing")
	if !handled {
		t.Fatal("裸 /routing 必须被接管")
	}
	if !strings.Contains(text, "路由（main）· 启用") || !strings.Contains(text, "无法开启全屏面板") {
		t.Fatalf("面板入口应退化为只读摘要并给出提示：\n%s", text)
	}
}

// TestChatRoutingCommandQueueSafety 钉住忙时排队策略（§5.2/§4.5）：
// 只读可排队；写入类与裸面板入口忙时拒绝。
func TestChatRoutingCommandQueueSafety(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"show"}, true},
		{[]string{"doctor", "main"}, true},
		{[]string{"show", "--json"}, true},
		{[]string{"main"}, true},
		{[]string{"sub"}, true},
		{[]string{"main", "hard"}, true},
		{[]string{"main", "profiles.hard.model"}, true},
		{[]string{"on"}, false},
		{[]string{"main", "enabled", "true"}, false},
		{[]string{"main", "level", "hard", "model", "x"}, false},
		{[]string{"reset"}, false},
		{[]string{"save"}, false},
	}
	for _, tc := range cases {
		if got := chatRoutingSubcommandQueueSafe(tc.args); got != tc.want {
			t.Fatalf("queueSafe(%v) = %v，期望 %v", tc.args, got, tc.want)
		}
	}
}

// TestChatRoutingCommandSubScopeWritesLevels 覆盖 sub 作用域键空间（levels.<level>.<field>）。
func TestChatRoutingCommandSubScopeWritesLevels(t *testing.T) {
	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})

	text, handled := chatRoutingCommandText(session, "/routing sub enabled true")
	if !handled || strings.Contains(text, "错误") {
		t.Fatalf("sub on 失败：%s", text)
	}
	text, handled = chatRoutingCommandText(session, "/routing sub levels.hard.model deepseek-r1")
	if !handled || strings.Contains(text, "错误") {
		t.Fatalf("sub level 写入失败：%s", text)
	}
	override, err := agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session))
	if err != nil || override == nil || override.SubAgent == nil {
		t.Fatalf("sub 覆盖未写入：err=%v override=%+v", err, override)
	}
	profile, ok := override.SubAgent.Levels["hard"]
	if !ok || profile.Model == nil || *profile.Model != "deepseek-r1" {
		t.Fatalf("sub levels.hard.model 未写入：%+v", override.SubAgent.Levels)
	}
}

// TestChatRoutingCommandWriteFeedsActorResolution 钉住 §4.2 读取链：
// TUI 写入的会话覆盖必须被宿主解析消费（下一 turn 的 actor 生效路径），
// 且零覆盖时仍返回配置层同一指针（M8/REG 前提）。
func TestChatRoutingCommandWriteFeedsActorResolution(t *testing.T) {
	cfg := &agentconfig.AICLIConfig{
		MainAgent: &agentconfig.AICLIMainAgentConfig{
			Routing: &agentconfig.AICLIMainAgentRoutingConfig{
				Enabled:           true,
				Levels:            []string{"easy", "normal", "hard"},
				DefaultDifficulty: "normal",
				Profiles: map[string]agentconfig.AICLISubagentRouteProfile{
					"normal": {Provider: "deepseek", Model: "deepseek-v3", ReasoningEffort: "medium"},
				},
			},
		},
	}
	session := routingStatusTestSession(t, nil, cfg)

	before := resolveLocalChatMainAgentRouting(session)
	if before == nil || before != cfg.MainAgent.Routing {
		t.Fatalf("零覆盖时必须返回配置层同一指针（M8）：%+v", before)
	}

	text, handled := chatRoutingCommandText(session, "/routing main level hard provider anthropic")
	if !handled || strings.Contains(text, "错误") {
		t.Fatalf("写入 hard.provider 失败：%s", text)
	}
	text, handled = chatRoutingCommandText(session, "/routing main level hard model claude-opus-4")
	if !handled || strings.Contains(text, "错误") {
		t.Fatalf("写入 hard.model 失败：%s", text)
	}

	after := resolveLocalChatMainAgentRouting(session)
	if after == nil {
		t.Fatal("写入后宿主解析不应为 nil")
	}
	if after == before {
		t.Fatal("写入后不应再返回配置层同一指针（会话覆盖必须生效）")
	}
	if got := after.Profiles["hard"].Model; got != "claude-opus-4" {
		t.Fatalf("会话覆盖未进入宿主解析：hard.model = %q", got)
	}
	if got := after.Profiles["normal"].Model; got != "deepseek-v3" {
		t.Fatalf("配置层其余档位应保留：normal.model = %q", got)
	}
}

// TestChatRoutingCommandWriteRefreshesLocalRuntime 钉住 §4.5/M6 的 TUI 侧闭环：
// 主 Agent 路由在 actor 构建期被克隆进 loop 配置（internal/agent/loop.go 的
// cloneMainAgentRoutingConfig），旧 actor 不会感知新配置，因此任何一层的写入都必须
// 让缓存的本地 actor 失效重建，否则「下一 turn 生效」要等到 actor 被闲置淘汰
// （与 /model /provider /reasoning 的 refreshLocalRuntimeAfterModelSelection 同口径）。
func TestChatRoutingCommandWriteRefreshesLocalRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workspace := t.TempDir()

	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	session.RuntimeSession.ID = "routing-refresh-session"
	// 模拟已持久化的进行中会话：warmup 只对有 durable row 的会话预热
	// （未持久化新会话由 actor 工厂在请求时自行 flush，见 chat_actor_warmup.go:20-32）。
	session.runtimeSessionUnpersisted = false
	session.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = workspace

	var calls atomic.Int32
	hub := runtimechat.NewSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		calls.Add(1)
		return nil, fmt.Errorf("factory stub: %s", sessionID)
	})
	t.Cleanup(hub.StopAll)
	session.LocalRuntimeHost = &localChatRuntimeHost{SessionHub: hub}

	text, handled := chatRoutingCommandText(session, "/routing main levels easy,normal,hard --to workspace")
	if !handled {
		t.Fatal("workspace 层写入必须被接管")
	}
	if strings.Contains(text, "警告") {
		t.Fatalf("重建路径可用时不应产生刷新警告：\n%s", text)
	}

	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("路由写入后必须重建本地 actor（预热应重新向 hub 请求 actor）")
	}
}

// TestChatRoutingCommandChildSessionReadOnly 钉住 §5.6/I-8/INV-A3（M16）：
// 非主会话（子 Agent 会话）的 `/routing` 只读打开，写入类子命令一律拒绝。
//
// 子会话标记与宿主接线同源（chat_actor_host.go:1379-1403 的 child 标记：
// agent_type / depth / read_only）：`aicli chat --session <child>`、`/load`、
// `/resume` 与 ACP（agent_stdio.go:680 的 SessionIDFlag）都能把子会话载入 TUI，
// 而宿主接线只把主 Agent 路由接主会话（isBaseSession 分支），因此判据必须落在
// 命令面自身，否则会写出「UI 显示成功但下一 turn 不生效」的层（方案 R1）。
func TestChatRoutingCommandChildSessionReadOnly(t *testing.T) {
	childMarkers := []struct {
		name  string
		apply func(session *runtimechat.Session)
	}{
		{name: "agent_type", apply: func(session *runtimechat.Session) {
			session.SetContext(toolbroker.AgentSessionContextAgentType, "worker")
		}},
		{name: "depth", apply: func(session *runtimechat.Session) {
			session.SetContext(toolbroker.AgentSessionContextDepth, 1)
		}},
		{name: "read_only", apply: func(session *runtimechat.Session) {
			session.SetContext(toolbroker.AgentSessionContextReadOnly, true)
		}},
	}

	writes := []string{
		"/routing on",
		"/routing off",
		"/routing main enabled true",
		"/routing main level hard model claude-opus-4",
		"/routing main profiles.hard.model claude-opus-4",
		"/routing reset main",
		"/routing save session",
	}

	for _, marker := range childMarkers {
		t.Run(marker.name, func(t *testing.T) {
			session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
			marker.apply(session.RuntimeSession)

			for _, command := range writes {
				text, handled := chatRoutingCommandText(session, command)
				if !handled {
					t.Fatalf("%s 必须被结构化入口接管（返回拒绝文本）", command)
				}
				if !strings.Contains(text, "子会话只读") {
					t.Fatalf("%s 应被拒绝为子会话只读：%s", command, text)
				}
			}
			if raw := chatSessionRoutingOverrideRaw(session); raw != "" {
				t.Fatalf("子会话不得落盘路由覆盖，得到 %q", raw)
			}

			// 只读子命令保持可用（§5.6：只读打开）。
			showText, handled := chatRoutingCommandText(session, "/routing show")
			if !handled || strings.Contains(showText, "子会话只读") {
				t.Fatalf("/routing show 在子会话必须可用：%s", showText)
			}
			doctorText, handled := chatRoutingCommandText(session, "/routing doctor")
			if !handled || strings.Contains(doctorText, "子会话只读") {
				t.Fatalf("/routing doctor 在子会话必须可用：%s", doctorText)
			}

			// 面板入口退化为只读摘要 + 提示，不进入可写选择器（也不触碰 TTY）。
			panel := chatRoutingPanelEntry(session, chatRoutingScopeMain, "", "")
			if !strings.Contains(panel, "子会话只读") {
				t.Fatalf("子会话面板入口应给出只读提示：\n%s", panel)
			}
		})
	}
}

// TestChatRoutingCommandChildSessionBlocksWorkspaceSideEffects 钉住子会话拒绝发生在
// 落盘之前：workspace 层写入会改工作区配置文件（真实副作用），不能先写再报错。
func TestChatRoutingCommandChildSessionBlocksWorkspaceSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workspace := t.TempDir()

	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	session.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = workspace
	session.RuntimeSession.SetContext(toolbroker.AgentSessionContextDepth, 1)

	text, handled := chatRoutingCommandText(session, "/routing main levels easy,normal,hard --to workspace")
	if !handled || !strings.Contains(text, "子会话只读") {
		t.Fatalf("workspace 层写入应在子会话被拒绝：%s", text)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatalf("读取工作区失败：%v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("子会话被拒绝后不应产生工作区文件，得到 %d 项", len(entries))
	}

	// 主会话（无任何子标记）仍可正常写入，确保判据没有误伤。
	base := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	base.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = workspace
	baseText, handled := chatRoutingCommandText(base, "/routing main levels easy,normal,hard --to workspace")
	if !handled || strings.Contains(baseText, "子会话只读") {
		t.Fatalf("主会话写入不得被 M16 判据误伤：%s", baseText)
	}
}

// TestChatRoutingCommandRejectsLevelOutsideLevels 钉住 §10.2 I-4：
// 写入未纳入生效 levels 的档位字段时，拒绝文案必须按字段回显并给出「先设 levels」
// 的建议动作，且不落盘；已在 levels 中的档位不受影响（不改变校验语义）。
func TestChatRoutingCommandRejectsLevelOutsideLevels(t *testing.T) {
	session := routingCommandSessionWithOverride(t)

	text, handled := chatRoutingCommandText(session, "/routing main profiles.expert.model gpt-4.1")
	if !handled {
		t.Fatal("档位字段写入必须被接管")
	}
	for _, want := range []string{
		"错误",
		"字段 profiles.expert.model 未写入",
		"不在生效 levels 中",
		"当前 levels: easy,normal,hard",
		"/routing main levels easy,normal,hard,expert",
		"allow_expert",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("拒绝文案缺少 %q：\n%s", want, text)
		}
	}
	if raw := chatSessionRoutingOverrideRaw(session); strings.Contains(raw, "expert") {
		t.Fatalf("非法档位字段不得落盘：%s", raw)
	}

	// §3.5.2 校验失败同样按字段回显 + 建议动作（levels 含 expert 但 allow_expert=false）。
	text, handled = chatRoutingCommandText(session, "/routing main levels easy,normal,hard,expert")
	if !handled || !strings.Contains(text, "错误") {
		t.Fatalf("含 expert 的 levels 应被拒绝：%s", text)
	}
	for _, want := range []string{"字段 levels 未写入", "allow_expert on"} {
		if !strings.Contains(text, want) {
			t.Fatalf("校验失败文案缺少 %q：\n%s", want, text)
		}
	}

	// 已在 levels 中的档位仍可正常写入。
	// 解析类非法输入同样按字段回显（保留原始报错，不落盘）。
	text, handled = chatRoutingCommandText(session, "/routing main max_consecutive_expensive_steps abc")
	if !handled || !strings.Contains(text, "错误") {
		t.Fatalf("非法整数应被拒绝：%s", text)
	}
	for _, want := range []string{"字段 max_consecutive_expensive_steps 未写入", "需要整数"} {
		if !strings.Contains(text, want) {
			t.Fatalf("解析失败文案缺少 %q：\n%s", want, text)
		}
	}

	// 未知键已自带字段名，只回显一次（不叠加前缀）。
	text, handled = chatRoutingCommandText(session, "/routing main bogus.key value")
	if !handled || strings.Count(text, "bogus.key") != 1 {
		t.Fatalf("未知键应回显一次字段名：%s", text)
	}

	text, handled = chatRoutingCommandText(session, "/routing main level hard model claude-sonnet-4")
	if !handled || strings.Contains(text, "错误") {
		t.Fatalf("合法档位字段写入失败：%s", text)
	}
	if raw := chatSessionRoutingOverrideRaw(session); !strings.Contains(raw, "claude-sonnet-4") {
		t.Fatalf("合法写入未落盘：%s", raw)
	}
}

// TestChatRoutingCommandReportsRollbackCause 钉住 §10.2 I-4 的回退分支：
// 非法值让合并结果不合法时，§3.5.2 阶梯回退会把 enabled 丢回下层；此时拒绝文案
// 必须给出真实原因与建议动作，而不是误报「主 Agent 路由未启用」。
func TestChatRoutingCommandReportsRollbackCause(t *testing.T) {
	session := routingCommandSessionWithOverride(t)

	if text, handled := chatRoutingCommandText(session, "/routing main max_consecutive_expensive_steps 5"); !handled || strings.Contains(text, "错误") {
		t.Fatalf("写入 max_consecutive_expensive_steps 失败：%s", text)
	}
	if text, handled := chatRoutingCommandText(session, "/routing main allow_expert on"); !handled || strings.Contains(text, "错误") {
		t.Fatalf("开启 allow_expert 失败：%s", text)
	}

	text, handled := chatRoutingCommandText(session, "/routing main max_consecutive_expensive_steps 0")
	if !handled || !strings.Contains(text, "错误") {
		t.Fatalf("非法 max_consecutive_expensive_steps 应被拒绝：%s", text)
	}
	for _, want := range []string{
		"字段 max_consecutive_expensive_steps 未写入",
		"must be a finite value",
		"/routing main max_consecutive_expensive_steps <N>（>0），再 /routing main allow_expert on",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("回退拒绝文案缺少 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "路由未启用") {
		t.Fatalf("回退分支不得误报「未启用」：%s", text)
	}

	override, err := agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session))
	if err != nil || override == nil || override.MainAgent == nil || override.MainAgent.MaxConsecutiveExpensiveSteps == nil {
		t.Fatalf("回退后覆盖应保持回退前的合法值：err=%v override=%+v", err, override)
	}
	if got := *override.MainAgent.MaxConsecutiveExpensiveSteps; got != 5 {
		t.Fatalf("非法值不得落盘，得到 %d", got)
	}
}

// TestChatRoutingCommandLayerWriteRejectsWithFieldEcho 钉住 §10.2 I-4 的层写入分支：
// workspace/config 层的写前校验失败同样「按字段回显 + 建议动作」，且不落盘。
func TestChatRoutingCommandLayerWriteRejectsWithFieldEcho(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	workspace := t.TempDir()
	if strings.TrimSpace(agentconfig.WorkspaceRoutingTargetPath(workspace)) == "" {
		t.Skip("当前环境无法解析 workspace 偏好文件路径")
	}

	session := routingStatusTestSession(t, nil, &agentconfig.AICLIConfig{})
	session.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = workspace

	// levels 含 expert 但 allow_expert=false：写前校验必须拒绝并给出下一步命令。
	text, handled := chatRoutingCommandText(session, "/routing main levels easy,normal,hard,expert --to workspace")
	if !handled || !strings.Contains(text, "错误") {
		t.Fatalf("workspace 层非法写入应被拒绝：%s", text)
	}
	for _, want := range []string{"字段 levels 未写入", "allow_expert on"} {
		if !strings.Contains(text, want) {
			t.Fatalf("层写入拒绝文案缺少 %q：\n%s", want, text)
		}
	}
	prefs, err := agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspace)
	if err != nil {
		t.Fatalf("读取 workspace 偏好: %v", err)
	}
	if prefs != nil && prefs.MainAgent != nil && len(prefs.MainAgent.Levels) > 0 {
		t.Fatalf("非法写入不得落盘：%+v", prefs.MainAgent.Levels)
	}
}
