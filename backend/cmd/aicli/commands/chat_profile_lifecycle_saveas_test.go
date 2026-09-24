package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
)

// §23 G1/D24 + A9 + E2E-2：save-as 把会话实际生效面固化为**差分声明**，产物可被
// 同一 loader 解析、重开后复现同一工具面，且 prompt 等无字段可表达项在报告中明示。
func TestProfileCommandSaveAsWritesDiffAndReproducesSurface(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	session.ToolPolicy = runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep", "shell"}, false)
	session.ProfileSkillSelection = runtimeprofileinput.ResolvedSkillSelection{Denylist: []string{"docs"}}
	session.ProfileMCPSelection = runtimeprofileinput.ResolvedMCPSelection{ExcludeServers: []string{"remote"}}
	session.SystemPromptText = "临时会话 prompt"

	text, handled := chatProfileCommandText(session, "/profile save-as tui-saved")
	if !handled {
		t.Fatal("/profile save-as 必须被命令面接管")
	}
	if !strings.Contains(text, "已固化 profile: tui-saved") || !strings.Contains(text, "来源: 当前会话生效面差分") {
		t.Fatalf("save-as 应报告固化成功与来源，got: %s", text)
	}
	if !strings.Contains(text, "差分声明: tools.allowlist 3 项；skills.denylist 1 项；mcp.exclude_servers 1 项") {
		t.Fatalf("save-as 应逐项报告写入的差分字段，got: %s", text)
	}
	if !strings.Contains(text, "未包含: prompt") {
		t.Fatalf("save-as 必须明示 prompt 未固化（D24），got: %s", text)
	}
	if !strings.Contains(text, "基线: 无") {
		t.Fatalf("未绑定 profile 时应报告基线为内置默认，got: %s", text)
	}
	if !strings.Contains(text, "校验: 通过") {
		t.Fatalf("产物应通过同一校验器（D28 纪律），got: %s", text)
	}
	if !strings.Contains(text, "下一步: /profile use tui-saved") {
		t.Fatalf("save-as 应给出下一步引导，got: %s", text)
	}

	root := filepath.Join(userRoot, "tui-saved")
	spec, err := profilesys.LoadProfile(root)
	if err != nil {
		t.Fatalf("产物无法解析: %v", err)
	}
	if got := strings.Join(spec.Tools.Allowlist, ","); got != "grep,shell,view" {
		t.Fatalf("tools.allowlist = %q，want grep,shell,view", got)
	}
	if got := strings.Join(spec.Skills.Denylist, ","); got != "docs" {
		t.Fatalf("skills.denylist = %q，want docs", got)
	}
	if got := strings.Join(spec.MCP.ExcludeServers, ","); got != "remote" {
		t.Fatalf("mcp.exclude_servers = %q，want remote", got)
	}
	if strings.Contains(string(mustReadSaveAsFile(t, filepath.Join(root, "profile.yaml"))), "临时会话 prompt") {
		t.Fatal("会话 prompt 不得进入产物（D24）")
	}

	// E2E-2：重开（按名解析）后工具面与固化时一致。
	state, err := resolveChatProfilePreviewState(session, "tui-saved")
	if err != nil {
		t.Fatalf("固化产物应可按名解析: %v", err)
	}
	if state.ToolPolicy == nil {
		t.Fatal("解析结果应带工具策略")
	}
	if got := state.ToolPolicy.AllowedToolNames(); !reflect.DeepEqual(got, session.ToolPolicy.AllowedToolNames()) {
		t.Fatalf("重开后工具面 = %#v，want %#v", got, session.ToolPolicy.AllowedToolNames())
	}
}

// A9：无差分时报错，且不生成任何文件。
func TestProfileCommandSaveAsRejectsNoDiff(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	text, handled := chatProfileCommandText(session, "/profile save-as tui-nodiff")
	if !handled {
		t.Fatal("/profile save-as 必须被命令面接管")
	}
	if !strings.Contains(text, "当前无差异，无需固化") {
		t.Fatalf("无差分应明确报错，got: %s", text)
	}
	if dirExists(filepath.Join(userRoot, "tui-nodiff")) {
		t.Fatalf("无差分不得产出空 profile：%s", filepath.Join(userRoot, "tui-nodiff"))
	}

	// 缺参数：给用法，不猜名字。
	usage, _ := chatProfileCommandText(session, "/profile save-as")
	if !strings.Contains(usage, "用法: /profile save-as <name>") {
		t.Fatalf("缺参数应给用法，got: %s", usage)
	}
}

// D24：deny 形态保持 deny 形态（不转写成 allowlist），read_only 仅在开启时落盘。
func TestProfileCommandSaveAsKeepsDenyFormAndReadOnly(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	policy := runtimepolicy.NewToolExecutionPolicy(nil, true)
	policy.DeniedTools = map[string]bool{"shell": true, "write_file": true}
	session.ToolPolicy = policy

	text, _ := chatProfileCommandText(session, "/profile save-as tui-deny")
	if !strings.Contains(text, "差分声明: tools.denylist 2 项；tools.read_only") {
		t.Fatalf("deny 形态应写 denylist + read_only，got: %s", text)
	}
	spec, err := profilesys.LoadProfile(filepath.Join(userRoot, "tui-deny"))
	if err != nil {
		t.Fatalf("产物无法解析: %v", err)
	}
	if len(spec.Tools.Allowlist) != 0 {
		t.Fatalf("deny 形态不应转写成 allowlist: %+v", spec.Tools)
	}
	if got := strings.Join(spec.Tools.Denylist, ","); got != "shell,write_file" {
		t.Fatalf("denylist = %q，want shell,write_file", got)
	}
	if spec.Tools.ReadOnly == nil || !*spec.Tools.ReadOnly {
		t.Fatalf("read_only 应落盘为 true: %+v", spec.Tools)
	}
}

// §23 G4：save-as 不覆盖既有 profile（差分合并进旧目录会造成"半覆盖"）。
func TestProfileCommandSaveAsRefusesExistingTarget(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	if text, _ := chatProfileCommandText(session, "/profile create tui-taken --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}
	session.ToolPolicy = runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false)

	text, _ := chatProfileCommandText(session, "/profile save-as tui-taken")
	if !strings.Contains(text, "目标已存在") || !strings.Contains(text, "save-as 不覆盖既有 profile") {
		t.Fatalf("已存在目标应拒绝，got: %s", text)
	}
	if strings.Contains(string(mustReadSaveAsFile(t, filepath.Join(userRoot, "tui-taken", "profile.yaml"))), "从会话固化") {
		t.Fatal("拒绝路径不得改写既有 profile.yaml")
	}
}

// D24/D35：基线 profile 与不可声明项（权限模式）在报告中逐项明示，不静默丢弃。
func TestProfileCommandSaveAsReportsBaselineAndOmittedItems(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	if text, _ := chatProfileCommandText(session, "/profile create tui-base --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}
	session.ProfileReference = "tui-base"
	session.ProfileName = "tui-base"
	session.PermissionMode = runtimepolicy.ModePlan
	session.ToolPolicy = runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, false)

	text, _ := chatProfileCommandText(session, "/profile save-as tui-derived")
	if !strings.Contains(text, "基线: tui-base") {
		t.Fatalf("应报告基线 profile，got: %s", text)
	}
	if !strings.Contains(text, "相对基线") {
		t.Fatalf("应报告与基线的差异，got: %s", text)
	}
	if !strings.Contains(text, "权限模式") || !strings.Contains(text, "未包含") {
		t.Fatalf("非默认权限模式应明示未固化（D35），got: %s", text)
	}
	spec, err := profilesys.LoadProfile(filepath.Join(userRoot, "tui-derived"))
	if err != nil {
		t.Fatalf("产物无法解析: %v", err)
	}
	if !strings.Contains(spec.Profile.Description, "基线 profile: tui-base") {
		t.Fatalf("description 应记录来源与基线，got: %q", spec.Profile.Description)
	}
}

func mustReadSaveAsFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return raw
}
