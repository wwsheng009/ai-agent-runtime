package commands

import (
	"path/filepath"
	"strings"
	"testing"
)

// 回归（用户报告）：TUI 的 create/duplicate/import/move 把 profile 落在标准层根
// （user = <home>/.aicli/profiles），而解析侧只认 config 注册项与 profiles.root。
// 结果是"创建成功 → /profile use|edit 报未知 profile"的写读分叉。
//
// 这里走完整的命令面（chatProfileCommandText），覆盖 create → list → show/edit/use
// 的整条用户路径，而不是只测 registry 兜底。
func TestProfileCommandLayerProfileCreatedByTUIResolves(t *testing.T) {
	home := useTemporaryHome(t) // create 的落盘目标 = user 层根
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	createText, handled := chatProfileCommandText(session, "/profile create xxx --template coding --to user --force")
	if !handled {
		t.Fatal("/profile create 必须被命令面接管")
	}
	if !strings.Contains(createText, "已创建 profile: xxx") {
		t.Fatalf("create 应成功，got: %s", createText)
	}
	layerRoot := filepath.Join(home, ".aicli", "profiles", "xxx")
	if !profileRootHasProfileYAML(layerRoot) {
		t.Fatalf("create 应落在 user 层根 %s，got: %s", layerRoot, createText)
	}

	listText, _ := chatProfileCommandText(session, "/profile list")
	if !strings.Contains(listText, "xxx") || !strings.Contains(listText, "user") {
		t.Fatalf("list 应把 user 层来源的 xxx 列出，got: %s", listText)
	}

	showText, _ := chatProfileCommandText(session, "/profile show xxx")
	if strings.Contains(showText, "未知 profile") || !strings.Contains(showText, "只读预览") {
		t.Fatalf("show 必须能解析层 profile，got: %s", showText)
	}

	editText, _ := chatProfileCommandText(session, "/profile edit xxx")
	if strings.Contains(editText, "未知 profile") || !strings.Contains(editText, layerRoot) {
		t.Fatalf("edit 必须给出层 profile 的路径 %s，got: %s", layerRoot, editText)
	}

	useText, _ := chatProfileCommandText(session, "/profile use xxx")
	if strings.Contains(useText, "未知 profile") {
		t.Fatalf("use 必须能切换到层 profile，got: %s", useText)
	}
	if session.ProfileName != "xxx" || session.ProfileRoot != layerRoot {
		t.Fatalf("切换后应绑定层 profile：name=%q root=%q", session.ProfileName, session.ProfileRoot)
	}
}

// 优先级纪律：config 注册项与 profiles.root 的同名目录仍压过层发现
// （层只是兜底来源，不改变既有解析语义）。
func TestProfileCommandRegisteredProfileWinsOverLayer(t *testing.T) {
	home := useTemporaryHome(t)
	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	// 层里放一个与 config 注册项同名的 profile：必须被忽略。
	layerRoot := filepath.Join(home, ".aicli", "profiles", "coding")
	writeTestFile(t, filepath.Join(layerRoot, "profile.yaml"), "profile:\n  name: coding\n")
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	text, _ := chatProfileCommandText(session, "/profile show coding")
	if strings.Contains(text, "未知 profile") {
		t.Fatalf("show coding: %s", text)
	}
	if !strings.Contains(text, filepath.Join(profilesRoot, "coding")) {
		t.Fatalf("config root 下的 coding 应胜出，got: %s", text)
	}
	if strings.Contains(text, layerRoot) {
		t.Fatalf("层同名 profile 不得覆盖 config root 来源，got: %s", text)
	}
}
