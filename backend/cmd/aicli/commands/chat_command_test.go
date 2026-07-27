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
		"aicli resume",
		"/resume",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("chat help missing %q\n%s", want, text)
		}
	}

	for _, name := range []string{
		"provider",
		"model",
		"session",
		"resume",
		"list-sessions",
		"cwd",
		"permission-mode",
		"runtime-server",
		"image",
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
