package commands

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestNewChatCommandRegistersSharedFlagsAndHelp(t *testing.T) {
	cmd := NewChatCommand(func() *config.Config { return nil })
	if cmd == nil {
		t.Fatal("NewChatCommand returned nil")
	}
	if got := strings.TrimSpace(cmd.Use); got != "chat" {
		t.Fatalf("Use = %q, want chat", got)
	}

	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"/help",
		"/login ...",
		"/functions <prompt>",
		"/skill <skill> <prompt>",
		"docs/aicli/quickstart.md",
		"docs/aicli/install.md",
		"docs/aicli/faq.md",
		"docs/aicli/agents.md",
		"docs/skill_runtime/aicli_skills_usage.md",
		"aicli chat --resume",
		"aicli chat --resume --cwd=false",
		"aicli chat --prompt \"检查当前项目\"",
		"aicli resume",
		"/resume",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("chat help missing %q\n%s", want, text)
		}
	}

	for _, name := range []string{
		"provider",
		"prompt",
		"message",
		"model",
		"session",
		"resume",
		"list-sessions",
		"cwd",
		"permission-mode",
		"runtime-server",
		"image",
		"input-mode",
	} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("chat flag %q not registered", name)
		}
	}
	currentDirOnly, err := cmd.Flags().GetBool("cwd")
	if err != nil {
		t.Fatalf("GetBool cwd: %v", err)
	}
	if !currentDirOnly {
		t.Fatal("cwd filtering should be enabled by default")
	}
	if got := cmd.Flags().Lookup("provider").Shorthand; got != "p" {
		t.Fatalf("provider shorthand = %q, want p", got)
	}
	if got := cmd.Flags().Lookup("prompt").Shorthand; got != "" {
		t.Fatalf("prompt unexpectedly claimed shorthand %q", got)
	}
}

func TestParseChatCommandOptionsConsoleInputMode(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr string
	}{
		{name: "default auto", want: chatConsoleInputAuto},
		{name: "system", value: "system", want: chatConsoleInputSystem},
		{name: "custom case insensitive", value: " CUSTOM ", want: chatConsoleInputCustom},
		{name: "invalid", value: "raw", wantErr: "--input-mode 必须是"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewChatCommand(func() *config.Config { return nil })
			if tt.value != "" {
				if err := cmd.Flags().Set("input-mode", tt.value); err != nil {
					t.Fatalf("Set input-mode: %v", err)
				}
			}
			opts, err := parseChatCommandOptions(cmd, &config.Config{})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChatCommandOptions: %v", err)
			}
			if opts.InputMode != tt.want {
				t.Fatalf("InputMode = %q, want %q", opts.InputMode, tt.want)
			}
		})
	}
}

func TestParseChatCommandOptionsInitialPromptAliases(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "prompt", args: []string{"--prompt", "inspect project"}, want: "inspect project"},
		{name: "message alias", args: []string{"--message", "inspect project"}, want: "inspect project"},
		{name: "short message alias", args: []string{"-M", "inspect project"}, want: "inspect project"},
		{name: "same values", args: []string{"--prompt", "inspect project", "--message", "inspect project"}, want: "inspect project"},
		{name: "different values", args: []string{"--prompt", "inspect project", "--message", "other"}, wantErr: "不同的启动消息"},
		{name: "provider shorthand remains provider", args: []string{"-p", "codex", "--prompt", "inspect project"}, want: "inspect project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewChatCommand(func() *config.Config { return nil })
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			opts, err := parseChatCommandOptions(cmd, &config.Config{})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChatCommandOptions: %v", err)
			}
			if opts.Message != tt.want {
				t.Fatalf("startup prompt = %q, want %q", opts.Message, tt.want)
			}
			if tt.name == "provider shorthand remains provider" && opts.ProviderFlag != "codex" {
				t.Fatalf("provider = %q, want codex", opts.ProviderFlag)
			}
		})
	}
}

func TestChatAndResumeShareFlagSurface(t *testing.T) {
	chatCmd := NewChatCommand(func() *config.Config { return nil })
	resumeCmd := NewResumeCommand(func() *config.Config { return nil })

	chatCmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if resumeCmd.Flags().Lookup(flag.Name) == nil {
			t.Fatalf("resume missing shared chat flag %q", flag.Name)
		}
	})
}

func TestChatDefaultsToCurrentDirectoryFilterWithoutForcingSessionFeatures(t *testing.T) {
	cmd := NewChatCommand(func() *config.Config { return nil })
	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions: %v", err)
	}
	expectedWorkspace := resolveLocalWorkspacePath(loadRuntimeToolConfig(&config.Config{}, nil), nil)
	if !opts.SessionCurrentDirOnly {
		t.Fatalf("expected current-directory filtering by default, got %+v", opts)
	}
	if !sameChatSessionWorkspace(opts.SessionFilter.Workspace, expectedWorkspace) {
		t.Fatalf("workspace = %q, want %q", opts.SessionFilter.Workspace, expectedWorkspace)
	}
	if opts.SessionFeaturesRequested {
		t.Fatal("the implicit cwd filter should not make a plain chat an explicit session-feature request")
	}
}
