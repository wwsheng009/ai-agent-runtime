package commands

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 回归（用户报告）：$EDITOR 含空格的 Windows 路径被 strings.Fields 切成
// "C:\Program" + 其余，报 exec: "C:\\Program": executable file not found in %PATH%。
func TestResolveEditorCommandHandlesPathsWithSpaces(t *testing.T) {
	editorDir := filepath.Join(t.TempDir(), "Program Files", "My Editor")
	if err := os.MkdirAll(editorDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	editorPath := filepath.Join(editorDir, "My Editor.exe")
	if err := os.WriteFile(editorPath, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write editor stub: %v", err)
	}

	for name, testCase := range map[string]struct {
		value    string
		wantName string
		wantArgs []string
	}{
		"裸路径（含空格）": {
			value:    editorPath,
			wantName: editorPath,
		},
		"双引号路径 + 参数": {
			value:    `"` + editorPath + `" --wait`,
			wantName: editorPath,
			wantArgs: []string{"--wait"},
		},
		"单引号路径 + 参数": {
			value:    `'` + editorPath + `' -n`,
			wantName: editorPath,
			wantArgs: []string{"-n"},
		},
		"未加引号路径 + 参数（贪心合并）": {
			value:    editorPath + " --wait",
			wantName: editorPath,
			wantArgs: []string{"--wait"},
		},
		"PATH 上的命令 + 参数": {
			value:    "definitely-not-a-real-editor-xyz --wait",
			wantName: "definitely-not-a-real-editor-xyz",
			wantArgs: []string{"--wait"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			gotName, gotArgs, err := resolveEditorCommand(testCase.value)
			if err != nil {
				t.Fatalf("resolveEditorCommand(%q): %v", testCase.value, err)
			}
			if gotName != testCase.wantName {
				t.Fatalf("可执行文件 = %q, want %q", gotName, testCase.wantName)
			}
			if strings.Join(gotArgs, "\x00") != strings.Join(testCase.wantArgs, "\x00") {
				t.Fatalf("参数 = %v, want %v", gotArgs, testCase.wantArgs)
			}
		})
	}

	if _, _, err := resolveEditorCommand("   "); err == nil {
		t.Fatal("空编辑器命令必须报错")
	}
	if _, _, err := resolveEditorCommand(`"` + editorPath + ` --wait`); err == nil {
		t.Fatal("引号未闭合必须报错（不猜）")
	}
}

// 端到端：EDITOR 是含空格的路径时，--open 必须真正把 profile.yaml 交给编辑器，
// 而不是把 "C:\Program" 当成可执行文件。
func TestProfileCommandEditOpenLaunchesEditorWithSpacedPath(t *testing.T) {
	session, userRoot, cleanup := newProfileLifecycleTestSession(t)
	defer cleanup()

	if text, _ := chatProfileCommandText(session, "/profile create tui-open --template minimal"); !strings.Contains(text, "已创建") {
		t.Fatalf("前置 create 失败：%s", text)
	}

	editorDir := filepath.Join(t.TempDir(), "Program Files", "Fake Editor")
	if err := os.MkdirAll(editorDir, 0o755); err != nil {
		t.Fatalf("mkdir editor dir: %v", err)
	}
	markerPath := filepath.Join(editorDir, "editor-args.txt")
	editorPath := writeFakeEditor(t, editorDir)

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editorPath)

	text, _ := chatProfileCommandText(session, "/profile edit tui-open --open")
	if strings.Contains(text, "编辑器退出异常") || strings.Contains(text, "无法解析") {
		t.Fatalf("--open 不应报错，got: %s", text)
	}
	if !strings.Contains(text, "编辑器进程已退出") {
		t.Fatalf("--open 应报告编辑器进程已退出，got: %s", text)
	}
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("编辑器未被拉起（缺少参数记录 %s）: %v", markerPath, err)
	}
	wantYAML := filepath.Join(userRoot, "tui-open", "profile.yaml")
	if !strings.Contains(string(raw), wantYAML) {
		t.Fatalf("编辑器应收到 %s，got: %s", wantYAML, raw)
	}
}

// writeFakeEditor 生成一个把参数写入同目录 editor-args.txt 的假编辑器，
// 目录名由调用方决定（测试用它制造"含空格的编辑器路径"）。
func writeFakeEditor(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "Fake Editor.cmd")
		content := "@echo off\r\necho %* > \"%~dp0editor-args.txt\"\r\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fake editor: %v", err)
		}
		return path
	}
	path := filepath.Join(dir, "Fake Editor.sh")
	content := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/editor-args.txt\"\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake editor: %v", err)
	}
	return path
}
