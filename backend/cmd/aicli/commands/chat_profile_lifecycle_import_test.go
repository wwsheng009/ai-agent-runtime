package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// writeProfileImportTestBundle 用内置模板渲染一个源 profile 并打成 zip 包，作为
// `/profile import` 的导入源（复用生产同源的模板渲染/收集/打包实现，不另造包格式）。
func writeProfileImportTestBundle(t *testing.T, dir, name string) string {
	t.Helper()
	files, err := profilesys.RenderTemplate("minimal", name, "")
	if err != nil {
		t.Fatalf("RenderTemplate(%s): %v", name, err)
	}
	srcRoot := filepath.Join(dir, "src-"+name)
	if err := writeProfileTemplateFiles(srcRoot, files, sortedProfileTemplatePaths(files)); err != nil {
		t.Fatalf("物化源 profile 失败: %v", err)
	}
	bundle, err := profilesys.CollectBundleFiles(srcRoot)
	if err != nil {
		t.Fatalf("CollectBundleFiles: %v", err)
	}
	zipPath := filepath.Join(dir, name+".zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("创建 zip 失败: %v", err)
	}
	writeErr := profilesys.WriteBundleZip(file, bundle)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatalf("WriteBundleZip: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("关闭 zip 失败: %v", closeErr)
	}
	return zipPath
}

// §23 G5（Batch 13 slice 7）：TUI `/profile import` 闭环——dry-run 零落盘；真实导入
// 物化 + 同一 validate 报告；同名不覆盖；导入不自动激活（D28）。
func TestProfileCommandImportLifecycleDryRunThenImport(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()
	bundle := writeProfileImportTestBundle(t, t.TempDir(), "tui-imported")
	target := filepath.Join(userRoot, "tui-imported")

	dry, handled := chatProfileCommandText(session, "/profile import "+bundle+" --dry-run")
	if !handled {
		t.Fatal("/profile import 必须被命令面接管")
	}
	if !strings.Contains(dry, "未写盘") || !strings.Contains(dry, "tui-imported") {
		t.Fatalf("dry-run 应报告将导入且未写盘，got: %s", dry)
	}
	if dirExists(target) {
		t.Fatalf("dry-run 不得落盘：%s", target)
	}

	text, _ := chatProfileCommandText(session, "/profile import "+bundle+" --to user")
	if !strings.Contains(text, "已导入 profile: tui-imported") {
		t.Fatalf("import 应报告成功，got: %s", text)
	}
	if !strings.Contains(text, "校验: 通过") {
		t.Fatalf("import 应附同一 validate 的校验结论（D28-1），got: %s", text)
	}
	if !strings.Contains(text, "未激活") {
		t.Fatalf("import 应明示不自动激活（D28-2），got: %s", text)
	}
	if !fileExists(filepath.Join(target, "profile.yaml")) {
		t.Fatalf("import 未物化 profile.yaml：%s", target)
	}
	if strings.TrimSpace(session.ProfileReference) != "" {
		t.Fatalf("导入不得自动激活（session.ProfileReference=%q）", session.ProfileReference)
	}

	again, _ := chatProfileCommandText(session, "/profile import "+bundle+" --to user")
	if !strings.Contains(again, "已存在同名") || !strings.Contains(again, "不覆盖") {
		t.Fatalf("同名应拒绝且不覆盖（D28-4），got: %s", again)
	}
}

// D28-1/§23 G5：坏源（不存在 / 缺 profile.yaml）必须拒绝，且目标层根不留 profile 目录。
func TestProfileCommandImportLifecycleRejectsBadSource(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	missing, handled := chatProfileCommandText(session, "/profile import "+filepath.Join(t.TempDir(), "nope.zip"))
	if !handled {
		t.Fatal("/profile import 必须被命令面接管")
	}
	if !strings.Contains(missing, "导入源不存在") {
		t.Fatalf("不存在的源应报错，got: %s", missing)
	}

	badDir := filepath.Join(t.TempDir(), "bad-src")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("准备坏源失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("准备坏源失败: %v", err)
	}
	bad, _ := chatProfileCommandText(session, "/profile import "+badDir)
	if !strings.Contains(bad, "profile.yaml") || strings.Contains(bad, "已导入") {
		t.Fatalf("缺 profile.yaml 的源必须被拒绝，got: %s", bad)
	}
	if dirExists(userRoot) {
		entries, err := os.ReadDir(userRoot)
		if err != nil {
			t.Fatalf("读取层根失败: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("失败导入不得在层根留下任何条目，got: %v", entries)
		}
	}

	// 未知层：必须报错，不静默回退默认层。
	bundle := writeProfileImportTestBundle(t, t.TempDir(), "tui-layer-check")
	unknown, _ := chatProfileCommandText(session, "/profile import "+bundle+" --to bogus")
	if strings.Contains(unknown, "已导入") || !strings.Contains(unknown, "bogus") {
		t.Fatalf("未知层应报错且不落盘，got: %s", unknown)
	}
}

// M16/INV-A3：子会话的真实导入被写守卫挡住（写了也不生效）；--dry-run 是只读预演，不挡。
func TestProfileCommandImportLifecycleChildSessionGuard(t *testing.T) {
	session, _, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()
	if session.RuntimeSession == nil {
		t.Fatal("测试会话缺少 RuntimeSession，无法模拟子会话")
	}
	session.RuntimeSession.SetContext(toolbroker.AgentSessionContextDepth, 1)
	if !chatRoutingSessionIsChildAgent(session) {
		t.Fatal("depth>0 应命中子会话判据")
	}
	bundle := writeProfileImportTestBundle(t, t.TempDir(), "tui-child-import")

	text, handled := chatProfileCommandText(session, "/profile import "+bundle)
	if !handled {
		t.Fatal("/profile import 必须被命令面接管")
	}
	if !strings.Contains(text, "子会话只读") {
		t.Fatalf("子会话真实导入必须被挡，got: %s", text)
	}

	dry, _ := chatProfileCommandText(session, "/profile import "+bundle+" --dry-run")
	if strings.Contains(dry, "子会话只读") {
		t.Fatalf("dry-run 是只读预演，不应被子会话守卫挡住，got: %s", dry)
	}
	if !strings.Contains(dry, "未写盘") {
		t.Fatalf("dry-run 应正常预演，got: %s", dry)
	}
}
