package commands

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestNewResumeCommandHelpAndUse(t *testing.T) {
	cmd := NewResumeCommand(func() *config.Config { return nil })
	if cmd == nil {
		t.Fatal("NewResumeCommand returned nil")
	}
	if !strings.HasPrefix(strings.TrimSpace(cmd.Use), "resume") {
		t.Fatalf("Use = %q, want resume...", cmd.Use)
	}

	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"aicli resume",
		"aicli resume session_xxx",
		"aicli resume --cwd=false",
		"aicli chat --resume",
		"--list-sessions",
		"docs/aicli/quickstart.md",
		"docs/aicli/faq.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("resume help missing %q\n%s", want, text)
		}
	}
}

func TestResumeCommandDefaultsToCurrentDirectoryFilter(t *testing.T) {
	cmd := NewResumeCommand(func() *config.Config { return nil })
	if err := applyResumeCommandArgs(cmd, nil); err != nil {
		t.Fatalf("applyResumeCommandArgs: %v", err)
	}
	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions: %v", err)
	}
	expectedWorkspace := resolveLocalWorkspacePath(loadRuntimeToolConfig(&config.Config{}, nil), nil)
	if !opts.ResumeFlag || !opts.SessionCurrentDirOnly {
		t.Fatalf("expected resume + cwd options, got %+v", opts)
	}
	if !sameChatSessionWorkspace(opts.SessionFilter.Workspace, expectedWorkspace) {
		t.Fatalf("workspace = %q, want %q", opts.SessionFilter.Workspace, expectedWorkspace)
	}
}

func TestResumeCommandCanDisableCurrentDirectoryFilter(t *testing.T) {
	cmd := NewResumeCommand(func() *config.Config { return nil })
	if err := cmd.Flags().Set("cwd", "false"); err != nil {
		t.Fatalf("Set cwd: %v", err)
	}
	if err := applyResumeCommandArgs(cmd, nil); err != nil {
		t.Fatalf("applyResumeCommandArgs: %v", err)
	}
	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions: %v", err)
	}
	if !opts.ResumeFlag || opts.SessionCurrentDirOnly {
		t.Fatalf("expected resume without cwd filter, got %+v", opts)
	}
	if opts.SessionFilter.Workspace != "" {
		t.Fatalf("workspace = %q, want empty", opts.SessionFilter.Workspace)
	}
}

func TestApplyResumeCommandArgs(t *testing.T) {
	t.Parallel()

	newCmd := func() *cobra.Command {
		return NewResumeCommand(func() *config.Config { return nil })
	}

	t.Run("bare resume defaults to latest", func(t *testing.T) {
		cmd := newCmd()
		if err := applyResumeCommandArgs(cmd, nil); err != nil {
			t.Fatalf("applyResumeCommandArgs: %v", err)
		}
		resume, err := cmd.Flags().GetBool("resume")
		if err != nil {
			t.Fatalf("GetBool resume: %v", err)
		}
		if !resume {
			t.Fatal("expected --resume=true for bare aicli resume")
		}
		session, _ := cmd.Flags().GetString("session")
		if strings.TrimSpace(session) != "" {
			t.Fatalf("session = %q, want empty", session)
		}
	})

	t.Run("positional session id sets --session", func(t *testing.T) {
		cmd := newCmd()
		if err := applyResumeCommandArgs(cmd, []string{"session_abc"}); err != nil {
			t.Fatalf("applyResumeCommandArgs: %v", err)
		}
		session, err := cmd.Flags().GetString("session")
		if err != nil {
			t.Fatalf("GetString session: %v", err)
		}
		if session != "session_abc" {
			t.Fatalf("session = %q, want session_abc", session)
		}
		resume, _ := cmd.Flags().GetBool("resume")
		if resume {
			t.Fatal("expected --resume=false when SESSION_ID is provided")
		}
	})

	t.Run("matching --session and positional is ok", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("session", "session_abc"); err != nil {
			t.Fatalf("Set session: %v", err)
		}
		if err := applyResumeCommandArgs(cmd, []string{"session_abc"}); err != nil {
			t.Fatalf("applyResumeCommandArgs: %v", err)
		}
	})

	t.Run("conflicting --session and positional fails", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("session", "session_a"); err != nil {
			t.Fatalf("Set session: %v", err)
		}
		err := applyResumeCommandArgs(cmd, []string{"session_b"})
		if err == nil {
			t.Fatal("expected conflict error")
		}
		if !strings.Contains(err.Error(), "冲突") {
			t.Fatalf("error = %v, want 冲突", err)
		}
	})

	t.Run("existing --session without positional keeps session", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("session", "session_keep"); err != nil {
			t.Fatalf("Set session: %v", err)
		}
		if err := applyResumeCommandArgs(cmd, nil); err != nil {
			t.Fatalf("applyResumeCommandArgs: %v", err)
		}
		session, _ := cmd.Flags().GetString("session")
		if session != "session_keep" {
			t.Fatalf("session = %q, want session_keep", session)
		}
		resume, _ := cmd.Flags().GetBool("resume")
		if resume {
			t.Fatal("did not expect --resume when --session already set")
		}
	})

	t.Run("list-sessions rejects positional id", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("list-sessions", "true"); err != nil {
			t.Fatalf("Set list-sessions: %v", err)
		}
		err := applyResumeCommandArgs(cmd, []string{"session_abc"})
		if err == nil {
			t.Fatal("expected error for list-sessions + SESSION_ID")
		}
	})

	t.Run("list-sessions without positional is ok", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("list-sessions", "true"); err != nil {
			t.Fatalf("Set list-sessions: %v", err)
		}
		if err := applyResumeCommandArgs(cmd, nil); err != nil {
			t.Fatalf("applyResumeCommandArgs: %v", err)
		}
		resume, _ := cmd.Flags().GetBool("resume")
		if resume {
			t.Fatal("list-sessions should not force --resume")
		}
	})

	t.Run("explicit --resume=false is preserved for bare resume", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("resume", "false"); err != nil {
			t.Fatalf("Set resume: %v", err)
		}
		// Flag.Changed becomes true after Set in pflag.
		if err := applyResumeCommandArgs(cmd, nil); err != nil {
			t.Fatalf("applyResumeCommandArgs: %v", err)
		}
		resume, _ := cmd.Flags().GetBool("resume")
		if resume {
			t.Fatal("explicit --resume=false should be preserved")
		}
	})
}
