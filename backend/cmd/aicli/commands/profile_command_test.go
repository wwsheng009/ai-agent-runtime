package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// 本文件钉住 Batch 2（`aicli profile` 命令组）的对外契约：
// 子命令注册面、create 的写盘/名称校验、list 的三来源、show 的生效面、
// validate 的 error/warning 分级。

func TestNewProfileCommandRegistersSubcommands(t *testing.T) {
	cmd := NewProfileCommand(func() *config.Config { return nil })
	if cmd == nil {
		t.Fatal("NewProfileCommand returned nil")
	}
	if got := strings.TrimSpace(cmd.Use); got != "profile" {
		t.Fatalf("Use = %q, want profile", got)
	}
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"list", "show", "validate", "create", "export", "import"} {
		if !names[want] {
			t.Fatalf("profile 子命令缺少 %q（现有：%v）", want, names)
		}
	}
	// 输出选项必须存在，否则 --output json 静默无效。
	for _, sub := range cmd.Commands() {
		if sub.Flags().Lookup("output") == nil {
			t.Fatalf("子命令 %s 缺少 --output flag", sub.Name())
		}
	}
}

func TestValidateProfileCreateName(t *testing.T) {
	valid := []string{"coding", "my-profile", "a.b_c-1", "P1", "x"}
	for _, name := range valid {
		if err := validateProfileCreateName(name); err != nil {
			t.Fatalf("名称 %q 应合法：%v", name, err)
		}
	}
	invalid := []string{"", ".", "..", "a/b", `a\b`, "a b", "profile:", strings.Repeat("x", 65)}
	for _, name := range invalid {
		if err := validateProfileCreateName(name); err == nil {
			t.Fatalf("名称 %q 应非法", name)
		}
	}
}

func TestDiscoverProfileSkillNames(t *testing.T) {
	root := t.TempDir()
	direct := filepath.Join(root, "direct-skill")
	if err := os.MkdirAll(direct, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(direct, "SKILL.md"), []byte("# skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	container := filepath.Join(root, "container")
	for _, sub := range []string{"beta", "not-a-skill"} {
		if err := os.MkdirAll(filepath.Join(container, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(container, "beta", "SKILL.md"), []byte("# beta"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := discoverProfileSkillNames([]string{direct, container, filepath.Join(root, "missing")})
	want := []string{"beta", "direct-skill"}
	if len(got) != len(want) {
		t.Fatalf("discoverProfileSkillNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("discoverProfileSkillNames = %v, want %v", got, want)
		}
	}
}

// writeRenderedProfile 把内置模板渲染结果落到目标目录，供 show/validate/list 测试复用。
func writeRenderedProfile(t *testing.T, root, template, name string) {
	t.Helper()
	files, err := profilesys.RenderTemplate(template, name, "")
	if err != nil {
		t.Fatalf("RenderTemplate(%s): %v", template, err)
	}
	if err := writeProfileTemplateFiles(root, files, sortedProfileTemplatePaths(files)); err != nil {
		t.Fatalf("writeProfileTemplateFiles: %v", err)
	}
}

func TestRunProfileCreateCommandDryRunThenWrite(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	target := filepath.Join(root, "coding-test")

	dry, err := runProfileCreateCommand(cfg, profileCreateOptions{
		Name: "coding-test", Template: "coding", Root: target, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Created {
		t.Fatal("dry-run 不应标记 created")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run 不应创建目录：%v", err)
	}
	if len(dry.Files) == 0 {
		t.Fatal("dry-run 应列出将生成的文件")
	}

	created, err := runProfileCreateCommand(cfg, profileCreateOptions{
		Name: "coding-test", Template: "coding", Root: target,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Created {
		t.Fatal("create 应标记 created")
	}
	raw, err := os.ReadFile(filepath.Join(target, "profile.yaml"))
	if err != nil {
		t.Fatalf("读取生成的 profile.yaml: %v", err)
	}
	if !strings.Contains(string(raw), "coding-test") {
		t.Fatalf("profile.yaml 未渲染名称占位符：\n%s", string(raw))
	}
	if _, err := os.Stat(filepath.Join(target, "agents", "default", "agent.yaml")); err != nil {
		t.Fatalf("缺少 agents/default/agent.yaml: %v", err)
	}

	// 非空目录必须报错；--force 才允许覆盖。
	if _, err := runProfileCreateCommand(cfg, profileCreateOptions{
		Name: "coding-test", Template: "coding", Root: target,
	}); err == nil {
		t.Fatal("目标目录非空时应报错")
	}
	if _, err := runProfileCreateCommand(cfg, profileCreateOptions{
		Name: "coding-test", Template: "coding", Root: target, Force: true,
	}); err != nil {
		t.Fatalf("--force 应允许覆盖：%v", err)
	}

	// 非法名称与未知模板必须在写盘前失败。
	if _, err := runProfileCreateCommand(cfg, profileCreateOptions{
		Name: "../evil", Template: "coding", Root: filepath.Join(root, "evil"),
	}); err == nil {
		t.Fatal("非法名称应报错")
	}
	if _, err := runProfileCreateCommand(cfg, profileCreateOptions{
		Name: "unknown-template-profile", Template: "does-not-exist", Root: filepath.Join(root, "unknown"),
	}); err == nil {
		t.Fatal("未知模板应报错")
	}
	if _, err := os.Stat(filepath.Join(root, "unknown")); !os.IsNotExist(err) {
		t.Fatal("未知模板失败时不应创建目录")
	}
}

func TestRunProfileCreateCommandRegistersConfig(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{ConfigFilePath: filepath.Join(root, "config.yaml")}
	target := filepath.Join(root, "profiles", "coding-x")

	result, err := runProfileCreateCommand(cfg, profileCreateOptions{
		Name: "coding-x", Template: "coding", Root: target, Use: true, SetDefault: true,
	})
	if err != nil {
		t.Fatalf("create --use --set-default: %v", err)
	}
	if !result.Registered || !result.DefaultProfileSet {
		t.Fatalf("结果未标记 config 变更：%+v", result)
	}
	if strings.TrimSpace(result.ConfigPath) == "" {
		t.Fatal("应返回写入的 config 路径")
	}
	raw, err := os.ReadFile(result.ConfigPath)
	if err != nil {
		t.Fatalf("读取 config: %v", err)
	}
	text := string(raw)
	for _, want := range []string{"profiles:", "items:", "coding-x", "default_profile"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config 缺少 %q：\n%s", want, text)
		}
	}
}

func TestRunProfileListCommandSources(t *testing.T) {
	root := t.TempDir()
	profilesRoot := filepath.Join(root, "profiles")
	writeRenderedProfile(t, filepath.Join(profilesRoot, "coding-a"), "coding", "coding-a")
	itemRoot := filepath.Join(root, "custom", "review-b")
	writeRenderedProfile(t, itemRoot, "review", "review-b")
	explicitRoot := filepath.Join(root, "explicit")
	writeRenderedProfile(t, explicitRoot, "docs", "explicit")

	cfg := &config.Config{Profiles: &config.ProfilesConfig{
		Root:           profilesRoot,
		DefaultProfile: "coding-a",
		Items:          map[string]config.ProfileConfig{"review-b": {Root: itemRoot}},
	}}

	result, err := runProfileListCommand(cfg, explicitRoot)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := make(map[string]profileListEntry, len(result.Profiles))
	for _, entry := range result.Profiles {
		byName[entry.Name] = entry
	}
	if entry, ok := byName["coding-a"]; !ok || entry.Source != "root" || !entry.IsDefault || !entry.Exists {
		t.Fatalf("root 来源条目不正确：%+v", entry)
	}
	if entry, ok := byName["review-b"]; !ok || entry.Source != "config" || !entry.Exists {
		t.Fatalf("config 来源条目不正确：%+v", entry)
	}
	if entry, ok := byName["explicit"]; !ok || entry.Source != "path" || !entry.Exists {
		t.Fatalf("path 来源条目不正确：%+v", entry)
	}
	if result.DefaultProfile != "coding-a" || result.DefaultRoot != profilesRoot {
		t.Fatalf("默认项不正确：%+v", result)
	}
}

func TestRunProfileShowCommandEffectiveSurface(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{}}

	codingRoot := filepath.Join(root, "coding-x")
	writeRenderedProfile(t, codingRoot, "coding", "coding-x")
	coding, err := runProfileShowCommand(cfg, codingRoot, "")
	if err != nil {
		t.Fatalf("show coding: %v", err)
	}
	if coding.ProfileName != "coding-x" || coding.AgentID != "default" {
		t.Fatalf("解析结果不正确：%+v", coding)
	}
	// coding 模板不声明 allowlist → 全量（-1）。
	if coding.ToolPolicy.EffectiveAllowCount != -1 {
		t.Fatalf("未声明 allowlist 时应为 -1，got %d", coding.ToolPolicy.EffectiveAllowCount)
	}
	// D17 可发现性：默认权限模式与其来源必须在 `profile show` 里可见
	// （模板 agent 声明 permission_mode: default）。
	if coding.PermissionMode != "default" {
		t.Fatalf("coding 模板默认权限模式应为 default，got %q", coding.PermissionMode)
	}
	if strings.TrimSpace(coding.PermissionModeSource) == "" {
		t.Fatal("默认权限模式必须带来源标注（D17）")
	}

	minimalRoot := filepath.Join(root, "minimal-x")
	writeRenderedProfile(t, minimalRoot, "minimal", "minimal-x")
	minimal, err := runProfileShowCommand(cfg, minimalRoot, "")
	if err != nil {
		t.Fatalf("show minimal: %v", err)
	}
	if minimal.ToolPolicy.EffectiveAllowCount != 2 {
		t.Fatalf("minimal allowlist 生效数量应为 2（view/grep），got %d", minimal.ToolPolicy.EffectiveAllowCount)
	}
	if minimal.ToolPolicy.ReadOnly == nil || !*minimal.ToolPolicy.ReadOnly {
		t.Fatalf("minimal 应解析 read_only=true：%+v", minimal.ToolPolicy.ReadOnly)
	}
	if len(minimal.Skills.Effective) != 0 {
		t.Fatalf("minimal denylist=[*] 应清空技能：%v", minimal.Skills.Effective)
	}
	if minimal.Skills.Declared != true {
		t.Fatal("minimal 应标记 skills 已声明")
	}
}

// TestRenderedReviewTemplateDeniesDelegationTools 是 Batch 9 / T2 的验收线：
// 子代理/团队工具属"运行时自有"工具，allowlist 裁不掉它们
// （policy.IsRuntimeOwnedEssentialTool），模板必须用显式 denylist 才能真正关闭；
// 且 deny 必须精确命中——未被 deny 的运行时自有工具仍可用（不是"全禁"）。
func TestRenderedReviewTemplateDeniesDelegationTools(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{}}
	reviewRoot := filepath.Join(root, "review-x")
	writeRenderedProfile(t, reviewRoot, "review", "review-x")

	shown, err := runProfileShowCommand(cfg, reviewRoot, "")
	if err != nil {
		t.Fatalf("show review: %v", err)
	}
	delegationTools := []string{"spawn_agent", "spawn_subagents", "spawn_team"}
	for _, name := range delegationTools {
		found := false
		for _, declared := range shown.ToolPolicy.Denylist {
			if declared == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("review 模板 denylist 必须声明 %s：%v", name, shown.ToolPolicy.Denylist)
		}
	}

	state, err := resolveProfileStateForCLI(cfg, reviewRoot, "")
	if err != nil {
		t.Fatalf("resolve review: %v", err)
	}
	if state.ToolPolicy == nil {
		t.Fatal("review 应解析出工具策略")
	}
	for _, name := range delegationTools {
		if err := state.ToolPolicy.AllowTool(name); err == nil {
			t.Fatalf("review 模板必须真正拒绝 %s（allowlist 裁不掉运行时自有工具，只有显式 denylist 生效）", name)
		}
	}
	// 精确命中：未被 deny 的运行时自有工具仍然可用。
	if err := state.ToolPolicy.AllowTool("todos"); err != nil {
		t.Fatalf("todos 未被 deny，应仍可用：%v", err)
	}
}

func TestRunProfileValidateCommandSeverity(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{}}

	goodRoot := filepath.Join(root, "minimal-x")
	writeRenderedProfile(t, goodRoot, "minimal", "minimal-x")
	good, err := runProfileValidateCommand(cfg, goodRoot, "")
	if err != nil {
		t.Fatalf("validate minimal: %v", err)
	}
	if !good.Valid || good.ErrorCount != 0 {
		t.Fatalf("内置模板应无 error：%+v", good.Issues)
	}
	// 内置模板不得产生任何噪音（无假阳性），否则 CI 门禁不可用。
	if len(good.Issues) != 0 {
		t.Fatalf("内置模板不应产生任何 issue：%+v", good.Issues)
	}

	// 引用不存在的 skill → error（可直接用于 CI 门禁）。
	badRoot := filepath.Join(root, "bad")
	if err := os.MkdirAll(filepath.Join(badRoot, "agents", "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	badProfile := "profile:\n  name: bad\n  default_agent: default\nskills:\n  allowlist:\n    - not-exist-skill\n"
	if err := os.WriteFile(filepath.Join(badRoot, "profile.yaml"), []byte(badProfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badRoot, "agents", "default", "agent.yaml"), []byte("name: default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad, err := runProfileValidateCommand(cfg, badRoot, "")
	if err != nil {
		t.Fatalf("validate bad: %v", err)
	}
	if bad.Valid || bad.ErrorCount == 0 {
		t.Fatalf("缺失 skill 引用应产生 error：%+v", bad.Issues)
	}
	found := false
	for _, issue := range bad.Issues {
		if issue.Path == "skills.allowlist" && issue.Severity == profileValidateSeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("未命中 skills.allowlist error：%+v", bad.Issues)
	}

	// 未注册引用必须报错，而不是静默全量。
	if _, err := runProfileValidateCommand(cfg, filepath.Join(root, "not-registered"), ""); err == nil {
		t.Fatal("未注册 profile 引用应报错")
	}
}
