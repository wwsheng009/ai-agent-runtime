package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// Batch 11a：`/profile` 命令面测试（设计 §17.1/§17.2）。
// 覆盖：只读子命令零副作用、显式切换走唯一执行核心、off 回基线、
// save 的层语义与二次确认、pick 无选择器面退化、生命周期子命令显式拒绝。

const profileCommandFixtureYAML = `profile:
  name: %s
  default_agent: coder
agents:
  coder:
    model: fixture
`

func writeProfileCommandFixture(t *testing.T, root, name, prompt, policy string) {
	t.Helper()
	writeProfileSwitchFixture(t, root, name, fmt.Sprintf(profileCommandFixtureYAML, name), prompt, policy)
}

// writeProfileCommandFixtures 生成 coding（allowlist: read_file,grep）与
// minimal（allowlist: read_file）两个 fixture。
func writeProfileCommandFixtures(t *testing.T, root string) {
	t.Helper()
	writeProfileCommandFixture(t, root, "coding", "Coding prompt.", "allowlist: [read_file, grep]\ndenylist: [write_file]\n")
	writeProfileCommandFixture(t, root, "minimal", "Minimal prompt.", "allowlist: [read_file]\n")
}

func TestProfileCommandStatusWithoutBinding(t *testing.T) {
	t.Parallel()
	session, cleanup := newProfileSwitchTestSession(t, t.TempDir())
	defer cleanup()

	text, handled := chatProfileCommandText(session, "/profile")
	if !handled {
		t.Fatal("/profile 必须被命令面接管")
	}
	if !strings.Contains(text, "无 profile 基线") {
		t.Fatalf("status 应说明当前为无 profile 基线，got: %s", text)
	}
}

func TestProfileCommandStatusWithBinding(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}

	text, _ := chatProfileCommandText(session, "/profile status")
	if !strings.Contains(text, "profile: coding") || !strings.Contains(text, "工具面:") {
		t.Fatalf("status 应包含引用与工具面摘要，got: %s", text)
	}
}

func TestProfileCommandListIncludesAvailableProfiles(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	text, handled := chatProfileCommandText(session, "/profile list")
	if !handled {
		t.Fatal("/profile list 必须被命令面接管")
	}
	for _, want := range []string{"coding", "minimal", "当前: (无 profile)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("list 缺少 %q，got: %s", want, text)
		}
	}
}

// A6（只读零副作用）：show/diff 不得改动任何会话字段。
func TestProfileCommandShowIsReadOnly(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile show coding")
	if !strings.Contains(text, "只读预览") || !strings.Contains(text, "工具面: 2 个允许") {
		t.Fatalf("show 应输出只读预览与工具面计数，got: %s", text)
	}
	if session.ProfileReference != "" || session.ProfileName != "" || session.ToolPolicy != nil {
		t.Fatalf("show 不得改动会话状态：ref=%q name=%q policy=%v",
			session.ProfileReference, session.ProfileName, session.ToolPolicy)
	}
}

func TestProfileCommandShowUnknownProfileFails(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile show nope")
	if !strings.Contains(text, "未知 profile") {
		t.Fatalf("未知 profile 必须显式报错（不猜），got: %s", text)
	}
	if session.ProfileReference != "" {
		t.Fatalf("失败路径必须零状态改动，got ref=%q", session.ProfileReference)
	}
}

func TestProfileCommandShowRequiresName(t *testing.T) {
	t.Parallel()
	session, cleanup := newProfileSwitchTestSession(t, t.TempDir())
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile show")
	if !strings.Contains(text, "用法: /profile show <name>") {
		t.Fatalf("缺参数必须报用法，got: %s", text)
	}
}

func TestProfileCommandDiffReportsToolDelta(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}

	text, _ := chatProfileCommandText(session, "/profile diff minimal")
	if !strings.Contains(text, "差异: coding → minimal") || !strings.Contains(text, "tools -") {
		t.Fatalf("diff 应报告工具面收窄，got: %s", text)
	}
	if session.ProfileReference != "coding" {
		t.Fatalf("diff 不得切换，got ref=%q", session.ProfileReference)
	}
}

func TestProfileCommandUseSwitchesSession(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile use minimal")
	if !strings.Contains(text, "已切换 profile") || !strings.Contains(text, "生效时点: "+profileSwitchEffectiveAt) {
		t.Fatalf("use 应渲染 Switch Report，got: %s", text)
	}
	if !strings.Contains(text, profileSwitchCacheNotice) {
		t.Fatalf("use 报告必须含 cache_notice，got: %s", text)
	}
	if session.ProfileReference != "minimal" || session.ToolPolicy == nil {
		t.Fatalf("use 必须落地绑定与工具面：ref=%q policy=%v", session.ProfileReference, session.ToolPolicy)
	}
	if got := sortedCopy(session.ToolPolicy.AllowedToolNames()); len(got) != 1 || got[0] != "read_file" {
		t.Fatalf("minimal 工具面 = %#v, want [read_file]", got)
	}
}

func TestProfileCommandUseRequiresName(t *testing.T) {
	t.Parallel()
	session, cleanup := newProfileSwitchTestSession(t, t.TempDir())
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile use")
	if !strings.Contains(text, "用法: /profile use <name>") {
		t.Fatalf("缺参数必须报用法（不隐式选第一个），got: %s", text)
	}
}

func TestProfileCommandReloadWithoutBindingFails(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile reload")
	if !strings.Contains(text, "未绑定 profile") {
		t.Fatalf("reload 无绑定时必须报错，got: %s", text)
	}
}

// R18：reload 解析失败时保留旧状态（不静默降级、不崩）。
func TestProfileCommandReloadFailureKeepsOldState(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(profilesRoot, "coding")); err != nil {
		t.Fatalf("remove fixture: %v", err)
	}

	text, _ := chatProfileCommandText(session, "/profile reload")
	if !strings.Contains(text, "失败") || !strings.Contains(text, "旧状态保持不变") {
		t.Fatalf("reload 失败必须明示且保留旧状态，got: %s", text)
	}
	if session.ProfileReference != "coding" || session.ToolPolicy == nil {
		t.Fatalf("reload 失败后旧绑定必须保留：ref=%q policy=%v", session.ProfileReference, session.ToolPolicy)
	}
}

// off：回到无 profile 基线（等价启动时不带 --profile）。
func TestProfileCommandOffRestoresBaseline(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}
	session.ContextWindowTokenCount = 2048
	session.stableSharedToolSessionID = currentRuntimeSessionID(session)
	session.stableSharedToolSelection = &aicliFunctionSelection{Mode: "fixture"}

	text, _ := chatProfileCommandText(session, "/profile off")
	if !strings.Contains(text, "无 profile 基线") {
		t.Fatalf("off 应说明已回基线，got: %s", text)
	}
	if session.ProfileReference != "" || session.ProfileName != "" || session.ProfileRoot != "" {
		t.Fatalf("off 必须清空绑定：ref=%q name=%q root=%q",
			session.ProfileReference, session.ProfileName, session.ProfileRoot)
	}
	if session.ToolPolicy != nil || session.BaseToolPolicy != nil {
		t.Fatalf("off 必须回落无策略基线：policy=%v base=%v", session.ToolPolicy, session.BaseToolPolicy)
	}
	if session.ContextWindowTokenCount != 0 {
		t.Fatalf("off 必须重置 token 计数，got %d", session.ContextWindowTokenCount)
	}
	if session.stableSharedToolSelection != nil {
		t.Fatal("off 必须清空稳定工具面缓存")
	}
	if len(session.ProfileSkillSelection.Allowlist) != 0 || len(session.ProfileMCPSelection.UseServers) != 0 {
		t.Fatalf("off 必须清空 skills/mcp 裁剪声明：%+v / %+v",
			session.ProfileSkillSelection, session.ProfileMCPSelection)
	}

	// 幂等：再次 off 不应报错，也不应有状态变化。
	second, _ := chatProfileCommandText(session, "/profile off")
	if !strings.Contains(second, "已处于无 profile 基线") {
		t.Fatalf("重复 off 应说明已在基线，got: %s", second)
	}
}

// D22/V17：session 层随 sessionmeta 持久化，无需落盘。
func TestProfileCommandSaveSessionLayerIsNoOp(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}

	text, _ := chatProfileCommandText(session, "/profile save")
	if !strings.Contains(text, "无需额外保存") {
		t.Fatalf("session 层应说明随会话持久化，got: %s", text)
	}
	if session.ProfileReference != "coding" {
		t.Fatalf("save 不得改动会话绑定，got ref=%q", session.ProfileReference)
	}
}

func TestProfileCommandSaveConfigRequiresConfirmation(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}

	text, _ := chatProfileCommandText(session, "/profile save --to config")
	if !strings.Contains(text, "--yes") {
		t.Fatalf("config 层缺 --yes 必须拒绝，got: %s", text)
	}
}

// V17/D22：workspace 层落到会话绑定工作区的偏好文件（N9：不随 cwd 漂移），
// 与 `/routing save --to workspace` 同源语义。HOME 隔离，避免写用户真实配置目录。
func TestProfileCommandSaveWorkspaceLayerWritesPrefs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	workspace := t.TempDir()
	if strings.TrimSpace(agentconfig.WorkspacePrefsPathForPath(workspace)) == "" {
		t.Skip("当前环境无法解析工作区偏好文件路径")
	}

	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	session.RuntimeSession.Metadata.Context[sessionmeta.WorkspacePath] = workspace
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}

	// 目标 = 会话解析出的 workspace（N9：不随 cwd 漂移）。基线语义
	// （chat_actor_host.go:3134-3136 + chat_team_binding.go:323-324）：
	// profile 生效且 runtime.yaml 未声明绝对 workspace.root 时，该值解析为
	// profile 根目录，因此断言按"会话当前 workspace"推导而不是写死 cwd。
	effective := strings.TrimSpace(chatSessionRoutingWorkspacePath(session))
	if effective == "" {
		t.Fatal("切换后会话必须仍绑定 workspace")
	}
	if cwd, err := os.Getwd(); err == nil && strings.EqualFold(filepath.Clean(effective), filepath.Clean(cwd)) {
		t.Fatalf("workspace 层目标不得回落到 cwd（N9），got %q", effective)
	}
	target := agentconfig.WorkspacePrefsPathForPath(effective)
	text, _ := chatProfileCommandText(session, "/profile save --to workspace")
	if !strings.Contains(text, target) {
		t.Fatalf("workspace 写入应回显目标文件 %q，got: %s", target, text)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取工作区偏好: %v", err)
	}
	if !strings.Contains(string(raw), "default_profile: coding") {
		t.Fatalf("workspace 层应记录默认 profile，got: %s", string(raw))
	}
	if session.ProfileReference != "coding" {
		t.Fatalf("save 不得改动会话绑定，got ref=%q", session.ProfileReference)
	}
}

func TestProfileCommandSaveWithoutBindingFails(t *testing.T) {
	t.Parallel()
	session, cleanup := newProfileSwitchTestSession(t, t.TempDir())
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile save --to config --yes")
	if !strings.Contains(text, "未绑定 profile") {
		t.Fatalf("无绑定时 save 必须拒绝，got: %s", text)
	}
}

func TestProfileCommandSaveUnknownLayerFails(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}

	text, _ := chatProfileCommandText(session, "/profile save --to nowhere")
	if !strings.Contains(text, "未知保存目标") {
		t.Fatalf("未知层必须报错（不静默回退默认层），got: %s", text)
	}
}

// pick：无选择器面（非交互 / JSON / 无终端）时退化为只读列表，绝不静默切换。
func TestProfileCommandPickWithoutSurfaceFallsBackToList(t *testing.T) {
	t.Parallel()
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	session.NoInteractive = true

	text, _ := chatProfileCommandText(session, "/profile pick")
	if !strings.Contains(text, "无法打开交互选择器") || !strings.Contains(text, "coding") {
		t.Fatalf("pick 退化输出应列出候选，got: %s", text)
	}
	if session.ProfileReference != "" {
		t.Fatalf("pick 退化路径不得切换，got ref=%q", session.ProfileReference)
	}
}

func TestProfileCommandPickWithoutProfilesExplainsHow(t *testing.T) {
	t.Parallel()
	session, cleanup := newProfileSwitchTestSession(t, t.TempDir())
	defer cleanup()
	session.NoInteractive = true

	text, _ := chatProfileCommandText(session, "/profile pick")
	if !strings.Contains(text, "未发现任何 profile") {
		t.Fatalf("无候选时应给出创建引导，got: %s", text)
	}
}

// §23 G4：生命周期子命令已接线（Batch 13 slice 4/5）；缺参数时给用法或"缺少引用"，
// 而不是报"未启用"，也不静默挑一个 profile 下手。save-as（D24 差分固化）的
// 产物与无差分路径专测见 chat_profile_lifecycle_saveas_test.go。
func TestProfileCommandLifecycleSubcommandsRequireArguments(t *testing.T) {
	session, _, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	for _, sub := range []string{"create", "duplicate", "rename", "move", "delete", "export", "edit"} {
		text, handled := chatProfileCommandText(session, "/profile "+sub)
		if !handled {
			t.Fatalf("/profile %s 必须被命令面接管", sub)
		}
		if strings.Contains(text, "尚未启用") {
			t.Fatalf("/profile %s 已接线，不应再报未启用，got: %s", sub, text)
		}
		if !strings.Contains(text, "用法") && !strings.Contains(text, "缺少") {
			t.Fatalf("/profile %s 缺参数应给出用法或缺失说明，got: %s", sub, text)
		}
	}
}

func TestProfileCommandUnknownSubcommandFails(t *testing.T) {
	t.Parallel()
	session, cleanup := newProfileSwitchTestSession(t, t.TempDir())
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile bogus")
	if !strings.Contains(text, "未知子命令") || !strings.Contains(text, "用法:") {
		t.Fatalf("未知子命令应报错并附用法，got: %s", text)
	}
}

func TestProfileCommandHelpText(t *testing.T) {
	t.Parallel()
	session, cleanup := newProfileSwitchTestSession(t, t.TempDir())
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile help")
	for _, want := range []string{"用法:", "/profile use <name>", "/profile off", "只能收窄安全基线"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help 缺少 %q，got: %s", want, text)
		}
	}
}

func TestProfileCommandNilSessionIsHandled(t *testing.T) {
	t.Parallel()
	result, handled := tryExecuteStructuredProfileCommand(nil, "/profile")
	if !handled {
		t.Fatal("nil session 也必须被接管（返回错误而不是回退 legacy）")
	}
	if len(result.Blocks) == 0 {
		t.Fatal("nil session 应返回错误结果")
	}
}

// 任务 3（Batch 11a）：catalog 注册 + Tab 补全自动生效。
func TestProfileCommandCatalogRegistration(t *testing.T) {
	t.Parallel()
	var spec *chatSlashCommandSpec
	for index := range chatSlashCommandCatalog() {
		if chatSlashCommandCatalog()[index].Name == "/profile" {
			spec = &chatSlashCommandCatalog()[index]
			break
		}
	}
	if spec == nil {
		t.Fatal("catalog 必须注册 /profile")
	}
	if spec.Group != string(chatSlashCommandGroupSession) || !spec.AcceptsArgs {
		t.Fatalf("catalog spec 分组/参数声明不正确: %+v", spec)
	}
	tokens := make(map[string]bool, len(spec.Args))
	for _, arg := range spec.Args {
		tokens[arg.Token] = true
	}
	for _, want := range []string{"status", "list", "show", "diff", "use", "pick", "reload", "off", "save"} {
		if !tokens[want] {
			t.Fatalf("catalog 缺少子命令声明 %q", want)
		}
	}
	candidates := matchSlashCommandCandidates(chatSlashCommandCatalog(), "/prof")
	found := false
	for _, candidate := range candidates {
		if candidate.Command == "/profile" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("/prof 补全候选必须包含 /profile，got %#v", candidates)
	}
}
