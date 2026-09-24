package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// newProfileLifecycleTestSession 把 user 层根指向临时 HOME，并让会话 registry 的
// 默认 root 指向同一目录——这是"创建 → 按名解析 → 切换"闭环的前提（否则 create 写到
// 真实用户目录，测试会污染本机并互相打架）。
func newProfileLifecycleTestSession(t *testing.T) (*ChatSession, string, func()) {
	t.Helper()
	home := useTemporaryHome(t)
	userRoot, err := profilesys.LayerRoot("user")
	if err != nil {
		t.Fatalf("LayerRoot(user): %v", err)
	}
	if !strings.HasPrefix(filepath.Clean(userRoot), filepath.Clean(home)) {
		t.Fatalf("user 层根未遵循 HOME 重定向：%s（home=%s）", userRoot, home)
	}
	session, cleanup := newProfileSwitchTestSession(t, userRoot)
	return session, userRoot, cleanup
}

// §23 G4：create 走模板渲染 + 名称/层校验；失败路径不留下半成品。
func TestProfileCommandCreateLifecycleWritesTemplate(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	text, handled := chatProfileCommandText(session, "/profile create tui-coder --template minimal")
	if !handled {
		t.Fatal("/profile create 必须被命令面接管")
	}
	if !strings.Contains(text, "已创建 profile: tui-coder") {
		t.Fatalf("create 应报告成功，got: %s", text)
	}
	if !strings.Contains(text, "template: minimal") || !strings.Contains(text, "layer: user") {
		t.Fatalf("create 应报告模板与层，got: %s", text)
	}
	if !strings.Contains(text, "下一步: /profile use tui-coder") {
		t.Fatalf("create 应给出下一步引导，got: %s", text)
	}
	root := filepath.Join(userRoot, "tui-coder")
	if !fileExists(filepath.Join(root, "profile.yaml")) {
		t.Fatalf("create 未写出 profile.yaml：%s", root)
	}
	if !dirExists(filepath.Join(root, "agents")) {
		t.Fatalf("模板应带 agents 目录：%s", root)
	}

	// 重名不覆盖（§23 G4：不自动改名）。
	again, _ := chatProfileCommandText(session, "/profile create tui-coder --template minimal")
	if !strings.Contains(again, "已存在") {
		t.Fatalf("重名应拒绝而不是覆盖，got: %s", again)
	}

	// 未知模板：报错并列出可用模板。
	unknown, _ := chatProfileCommandText(session, "/profile create tui-other --template nope")
	if !strings.Contains(unknown, "未知模板") || !strings.Contains(unknown, "coding") {
		t.Fatalf("未知模板应报错并列出可用项，got: %s", unknown)
	}
	if dirExists(filepath.Join(userRoot, "tui-other")) {
		t.Fatalf("未知模板不应落盘：%s", filepath.Join(userRoot, "tui-other"))
	}

	// 非法名（路径分隔符）：必须挡在写盘之前。
	invalid, _ := chatProfileCommandText(session, "/profile create ../escape")
	if strings.Contains(invalid, "已创建") {
		t.Fatalf("非法名不应创建成功，got: %s", invalid)
	}
	if dirExists(filepath.Join(filepath.Dir(userRoot), "escape")) {
		t.Fatalf("非法名逃出了层根：%s", filepath.Join(filepath.Dir(userRoot), "escape"))
	}

	// 未知层：必须报错（不静默回退默认层）。
	badLayer, _ := chatProfileCommandText(session, "/profile create tui-layer --to cloud")
	if !strings.Contains(badLayer, "未知层") {
		t.Fatalf("未知层应报错，got: %s", badLayer)
	}
}

// §23 G4 + G5：duplicate 复用收集/物化核心；export 复用打包核心（zip 可被读回）。
func TestProfileCommandDuplicateAndExportLifecycle(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	if text, _ := chatProfileCommandText(session, "/profile create tui-src --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}

	dup, _ := chatProfileCommandText(session, "/profile duplicate tui-src tui-copy")
	if !strings.Contains(dup, "已复制 profile: tui-src → tui-copy") {
		t.Fatalf("duplicate 应报告源与目标，got: %s", dup)
	}
	if !strings.Contains(dup, "仍声明 name: tui-src") {
		t.Fatalf("duplicate 应提示副本声明名未改写（D34），got: %s", dup)
	}
	copied := filepath.Join(userRoot, "tui-copy")
	if !fileExists(filepath.Join(copied, "profile.yaml")) {
		t.Fatalf("duplicate 未复制 profile.yaml：%s", copied)
	}

	dupAgain, _ := chatProfileCommandText(session, "/profile duplicate tui-src tui-copy")
	if !strings.Contains(dupAgain, "目标已存在") {
		t.Fatalf("duplicate 不覆盖同名目标，got: %s", dupAgain)
	}

	out := filepath.Join(t.TempDir(), "tui-copy.zip")
	exported, _ := chatProfileCommandText(session, "/profile export tui-copy --out "+out)
	if !strings.Contains(exported, "已导出 profile: tui-src（ref: tui-copy）") {
		t.Fatalf("export 应以声明名为主并标注 ref，got: %s", exported)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("导出文件不可读：%v", err)
	}
	files, err := profilesys.ReadBundleZip(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("导出的 zip 无法读回：%v", err)
	}
	foundRoot := false
	for _, file := range files {
		if file.Path == "profile.yaml" {
			foundRoot = true
		}
	}
	if !foundRoot {
		t.Fatalf("zip 缺少 profile.yaml：%v", files)
	}
}

// §23 G4 + D25：rename 同层改名并改写配置引用；move 同层/未知层拒绝；delete 删目录 + 清单。
func TestProfileCommandRenameMoveDeleteLifecycle(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	if text, _ := chatProfileCommandText(session, "/profile create tui-ren --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}

	renamed, _ := chatProfileCommandText(session, "/profile rename tui-ren tui-renamed")
	if !strings.Contains(renamed, "已重命名 profile: tui-ren → tui-renamed") {
		t.Fatalf("rename 应报告旧名与新名，got: %s", renamed)
	}
	if !strings.Contains(renamed, "声明名已同步为 tui-renamed") {
		t.Fatalf("rename 应报告 profile.yaml 声明名同步（D34），got: %s", renamed)
	}
	if dirExists(filepath.Join(userRoot, "tui-ren")) {
		t.Fatal("rename 后旧目录不应存在")
	}
	if !fileExists(filepath.Join(userRoot, "tui-renamed", "profile.yaml")) {
		t.Fatal("rename 后新目录应含 profile.yaml")
	}
	rawRenamed, err := os.ReadFile(filepath.Join(userRoot, "tui-renamed", "profile.yaml"))
	if err != nil {
		t.Fatalf("读改名后的 profile.yaml 失败：%v", err)
	}
	if !strings.Contains(string(rawRenamed), "name: tui-renamed") {
		t.Fatalf("profile.yaml 声明名未同步为 tui-renamed：%s", rawRenamed)
	}

	if text, _ := chatProfileCommandText(session, "/profile create tui-keep --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}
	clash, _ := chatProfileCommandText(session, "/profile rename tui-renamed tui-keep")
	if !strings.Contains(clash, "目标已存在") {
		t.Fatalf("rename 不覆盖同名目标，got: %s", clash)
	}

	sameLayer, _ := chatProfileCommandText(session, "/profile move tui-renamed --to user")
	if !strings.Contains(sameLayer, "同层移动无需 move") {
		t.Fatalf("同层 move 应拒绝，got: %s", sameLayer)
	}
	badLayer, _ := chatProfileCommandText(session, "/profile move tui-renamed --to cloud")
	if !strings.Contains(badLayer, "未知层") {
		t.Fatalf("未知层 move 应拒绝，got: %s", badLayer)
	}

	deleted, _ := chatProfileCommandText(session, "/profile delete tui-renamed")
	if !strings.Contains(deleted, "已删除 profile: tui-renamed") {
		t.Fatalf("delete 应报告成功，got: %s", deleted)
	}
	if !strings.Contains(deleted, "profile.yaml") {
		t.Fatalf("delete 应展示删除清单，got: %s", deleted)
	}
	if dirExists(filepath.Join(userRoot, "tui-renamed")) {
		t.Fatal("delete 后目录应不存在")
	}
}

// D25：被 default_profile 引用的 profile 无 --force 时阻止删除；--force 同时清空 default。
func TestProfileCommandDeleteProtectsDefaultProfile(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	if text, _ := chatProfileCommandText(session, "/profile create tui-default --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}
	session.Config.Profiles.DefaultProfile = "tui-default"

	blocked, _ := chatProfileCommandText(session, "/profile delete tui-default")
	if !strings.Contains(blocked, "--force") {
		t.Fatalf("default 引用应阻止删除并提示 --force，got: %s", blocked)
	}
	if !dirExists(filepath.Join(userRoot, "tui-default")) {
		t.Fatal("被阻止时不应删除目录")
	}

	forced, _ := chatProfileCommandText(session, "/profile delete tui-default --force")
	if !strings.Contains(forced, "已删除 profile: tui-default") {
		t.Fatalf("--force 应完成删除，got: %s", forced)
	}
	if !strings.Contains(forced, "default 已清空") {
		t.Fatalf("--force 应在报告中明示 default 清空，got: %s", forced)
	}
	if dirExists(filepath.Join(userRoot, "tui-default")) {
		t.Fatal("--force 后目录应不存在")
	}
}

// D27：edit 默认只给路径与命令；--open 需要 $EDITOR/$VISUAL，缺失时如实报错。
func TestProfileCommandEditPrintsPathAndRequiresEditorForOpen(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	if text, _ := chatProfileCommandText(session, "/profile create tui-edit --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")

	text, _ := chatProfileCommandText(session, "/profile edit tui-edit")
	wantPath := filepath.Join(userRoot, "tui-edit", "profile.yaml")
	if !strings.Contains(text, wantPath) {
		t.Fatalf("edit 应打印 profile.yaml 路径 %s，got: %s", wantPath, text)
	}
	if !strings.Contains(text, "未检测到 $EDITOR") {
		t.Fatalf("无编辑器时应给出手工路径提示，got: %s", text)
	}

	open, _ := chatProfileCommandText(session, "/profile edit tui-edit --open")
	if !strings.Contains(open, "需要 $EDITOR") {
		t.Fatalf("--open 缺编辑器时应报错，got: %s", open)
	}
}

// 无显式 ref 时回落到当前会话绑定；都没有则报错（不猜第一个）。
func TestProfileCommandLifecycleRequiresRefWithoutBinding(t *testing.T) {
	session, _, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile export")
	if !strings.Contains(text, "缺少 profile 引用") {
		t.Fatalf("无绑定且无 ref 应报错，got: %s", text)
	}
	edit, _ := chatProfileCommandText(session, "/profile edit")
	if !strings.Contains(edit, "缺少 profile 引用") {
		t.Fatalf("edit 无绑定且无 ref 应报错，got: %s", edit)
	}
}

// §23 G4：save-as（D24 差分固化）随 Batch 13 后续 slice 落地，当前必须显式拒绝。
func TestProfileCommandSaveAsIsExplicitlyDisabled(t *testing.T) {
	session, _, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	text, handled := chatProfileCommandText(session, "/profile save-as tui-x")
	if !handled {
		t.Fatal("/profile save-as 必须被命令面接管")
	}
	if !strings.Contains(text, "尚未启用") {
		t.Fatalf("/profile save-as 应显式说明未启用，got: %s", text)
	}
}

// help 文本必须覆盖新增的生命周期子命令（Tab 补全与文档同源）。
func TestProfileCommandHelpTextListsLifecycleSubcommands(t *testing.T) {
	session, _, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile help")
	for _, want := range []string{
		"/profile create <name>",
		"/profile duplicate <ref> <name>",
		"/profile rename <ref> <new-name>",
		"/profile move <ref> --to user|project",
		"/profile delete <ref> [--force]",
		"/profile export [<ref>] [--out <file|dir>]",
		"/profile edit [<ref>] [--open]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help 缺少 %q，got: %s", want, text)
		}
	}
}
