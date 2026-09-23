package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// parseExportCommandOptionsForTest 复现 cobra 的解析流程：先把命令行参数交给
// 命令自身的 flag set，再把 flag 与位置参数还原成 /export 的选项 token 并解析。
func parseExportCommandOptionsForTest(t *testing.T, args []string) (chatExportOptions, error) {
	t.Helper()
	cmd := NewExportCommand(nil)
	cmd.SetArgs(args)
	if err := cmd.ParseFlags(args); err != nil {
		return chatExportOptions{}, err
	}
	fields, err := exportOptionFields(cmd, cmd.Flags().Args())
	if err != nil {
		return chatExportOptions{}, err
	}
	return parseChatExportOptionFields(fields)
}

// runExportCommandForTest 执行 `aicli export` 并返回退出码。成功路径下
// exportExit 直接返回（不调用钩子），此时按 0 处理。
func runExportCommandForTest(t *testing.T, args []string) (int, error) {
	t.Helper()
	original := exportExitHook
	code := exportExitOK
	called := false
	exportExitHook = func(exitCode int) {
		code = exitCode
		called = true
	}
	t.Cleanup(func() { exportExitHook = original })

	cmd := NewExportCommand(func() *config.Config { return nil })
	cmd.SetArgs(args)
	err := cmd.Execute()
	if !called {
		return exportExitOK, err
	}
	return code, err
}

func TestExportCommandOptionParsingMatchesSlashExport(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantTarget string
		wantFormat chatExportFormat
		wantOutput string
		wantDir    string
	}{
		{name: "默认格式为完整 JSON", args: nil, wantFormat: chatExportFormatFull},
		{name: "位置参数目标加 --trace", args: []string{"latest", "--trace"}, wantTarget: "latest", wantFormat: chatExportFormatMarkdownTrace},
		{name: "裸格式词与目标混用", args: []string{"session-1", "md-tools"}, wantTarget: "session-1", wantFormat: chatExportFormatMarkdownTools},
		{name: "--format 取值", args: []string{"--format", "md-trace"}, wantFormat: chatExportFormatMarkdownTrace},
		{name: "--body 加输出文件", args: []string{"--body", "--output", "out.md"}, wantFormat: chatExportFormatBody, wantOutput: "out.md"},
		{name: "--full 加输出目录", args: []string{"--full", "--dir", "exports"}, wantFormat: chatExportFormatFull, wantDir: "exports"},
		{name: "多个格式来源但语义一致", args: []string{"--format", "trace", "--trace"}, wantFormat: chatExportFormatMarkdownTrace},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			opts, err := parseExportCommandOptionsForTest(t, test.args)
			if err != nil {
				t.Fatalf("parse export options: %v", err)
			}
			if opts.Format != test.wantFormat {
				t.Fatalf("format = %q, want %q", opts.Format, test.wantFormat)
			}
			if opts.Target != test.wantTarget {
				t.Fatalf("target = %q, want %q", opts.Target, test.wantTarget)
			}
			if opts.ExplicitTarget != (test.wantTarget != "") {
				t.Fatalf("explicit target = %v, want %v", opts.ExplicitTarget, test.wantTarget != "")
			}
			if opts.OutputPath != test.wantOutput {
				t.Fatalf("output = %q, want %q", opts.OutputPath, test.wantOutput)
			}
			if opts.OutputDir != test.wantDir {
				t.Fatalf("dir = %q, want %q", opts.OutputDir, test.wantDir)
			}
		})
	}
}

func TestExportCommandOptionParsingRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "冲突的格式 flag", args: []string{"--full", "--trace"}, wantErr: "导出格式冲突"},
		{name: "冲突的 flag 与 --format", args: []string{"--format", "body", "--tools"}, wantErr: "导出格式冲突"},
		{name: "冲突的 flag 与裸格式词", args: []string{"--body", "md-trace"}, wantErr: "导出格式冲突"},
		{name: "未知格式值", args: []string{"--format", "bogus"}, wantErr: "未知导出格式"},
		{name: "空的输出文件路径", args: []string{"--output="}, wantErr: "--output 需要指定输出文件路径"},
		{name: "空的输出目录", args: []string{"--dir="}, wantErr: "--dir 需要指定输出目录"},
		{name: "两个会话目标", args: []string{"latest", "session-1"}, wantErr: "只能指定一个导出会话目标"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseExportCommandOptionsForTest(t, test.args)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), test.wantErr)
			}
		})
	}
}

func TestExportCommandExportsLatestSessionToFile(t *testing.T) {
	sessionDir := t.TempDir()
	manager, userID, _, err := newChatSessionManager(sessionDir)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	runtimeSession, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		manager.Stop()
		t.Fatalf("create runtime session: %v", err)
	}
	messages := []runtimetypes.Message{
		{Role: "user", Content: "please run status", Metadata: runtimetypes.NewMetadata()},
		{
			Role:    "assistant",
			Content: "I will check.",
			ToolCalls: []runtimetypes.ToolCall{{
				ID:   "call-1",
				Name: "execute_shell_command",
				Args: map[string]interface{}{"command": "git status --short"},
			}},
			Metadata: runtimetypes.NewMetadata(),
		},
		{Role: "tool", ToolCallID: "call-1", Content: " M file.go", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "Done.", Metadata: runtimetypes.NewMetadata()},
	}
	for index := range messages {
		if err := manager.AddMessage(context.Background(), runtimeSession.ID, messages[index]); err != nil {
			manager.Stop()
			t.Fatalf("append message %d: %v", index, err)
		}
	}
	manager.Stop()

	cases := []struct {
		name   string
		target string
	}{
		{name: "latest", target: "latest"},
		{name: "current 等价于 latest", target: "current"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "session.md")
			code, err := runExportCommandForTest(t, []string{
				test.target,
				"--trace",
				"--output", outputPath,
				"--session-dir", sessionDir,
				"--user", userID,
			})
			if err != nil {
				t.Fatalf("execute aicli export: %v", err)
			}
			if code != exportExitOK {
				t.Fatalf("exit code = %d, want %d", code, exportExitOK)
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read export file: %v", err)
			}
			doc := string(data)
			for _, want := range []string{"please run status", "execute_shell_command", "git status --short", "Done."} {
				if !strings.Contains(doc, want) {
					t.Fatalf("export missing %q, got:\n%s", want, doc)
				}
			}
		})
	}
}

func TestExportCommandExitCodes(t *testing.T) {
	t.Run("参数错误返回 1", func(t *testing.T) {
		code, _ := runExportCommandForTest(t, []string{"--full", "--trace"})
		if code != exportExitUsage {
			t.Fatalf("exit code = %d, want %d", code, exportExitUsage)
		}
	})

	t.Run("会话不存在返回 2", func(t *testing.T) {
		sessionDir := t.TempDir()
		manager, _, _, err := newChatSessionManager(sessionDir)
		if err != nil {
			t.Fatalf("create session manager: %v", err)
		}
		manager.Stop()

		code, err := runExportCommandForTest(t, []string{
			"latest",
			"--session-dir", sessionDir,
			"--user", "tester",
		})
		if err != nil {
			t.Fatalf("execute aicli export: %v", err)
		}
		if code != exportExitFailure {
			t.Fatalf("exit code = %d, want %d", code, exportExitFailure)
		}
	})
}
