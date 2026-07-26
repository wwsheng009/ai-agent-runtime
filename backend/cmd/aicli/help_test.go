package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestRootCommandHelpMentionsOnboardingDocs(t *testing.T) {
	for _, want := range []string{
		"aicli init --global",
		"aicli login --provider openai",
		"aicli doctor provider",
		"docs/aicli/quickstart.md",
		"docs/aicli/install.md",
		"docs/aicli/faq.md",
		"docs/aicli/exec.md",
		"docs/aicli/agents.md",
		"docs/aicli/README.md",
		"docs/skill_runtime/aicli_skills_usage.md",
	} {
		if !strings.Contains(rootCommandLongHelp, want) && !strings.Contains(rootCommandExampleHelp, want) {
			t.Fatalf("root help missing %q", want)
		}
	}
}

func TestChatCommandHelpMentionsSlashCommandsAndDocs(t *testing.T) {
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
	} {
		if !strings.Contains(chatCommandLongHelp, want) && !strings.Contains(chatCommandExampleHelp, want) {
			t.Fatalf("chat help missing %q", want)
		}
	}
}

func TestConfigCommandHelpMentionsInstallAndFAQDocs(t *testing.T) {
	for _, want := range []string{
		"docs/aicli/install.md",
		"docs/aicli/faq.md",
		"aicli config --no-tui",
		"aicli config --models",
		"providers",
	} {
		if !strings.Contains(configCommandLongHelp, want) && !strings.Contains(configCommandExampleHelp, want) {
			t.Fatalf("config help missing %q", want)
		}
	}
}

func TestTestCommandHelpMentionsInstallAndFAQDocs(t *testing.T) {
	for _, want := range []string{
		"docs/aicli/faq.md",
		"docs/aicli/install.md",
		"aicli test --stream",
		"endpoint",
	} {
		if !strings.Contains(testCommandLongHelp, want) && !strings.Contains(testCommandExampleHelp, want) {
			t.Fatalf("test help missing %q", want)
		}
	}
}

func TestContextCommandHelpMentionsInstallDocs(t *testing.T) {
	for _, want := range []string{
		"docs/aicli/install.md",
		"aicli context --model",
		"--step",
		"--max-output-only",
	} {
		if !strings.Contains(contextCommandLongHelp, want) && !strings.Contains(contextCommandExampleHelp, want) {
			t.Fatalf("context help missing %q", want)
		}
	}
}

func TestPipeCommandHelpMentionsExecAndInstallDocs(t *testing.T) {
	for _, want := range []string{
		"docs/aicli/exec.md",
		"docs/aicli/install.md",
		"缓冲模式",
		"--stream",
		"aicli pipe -p",
	} {
		if !strings.Contains(pipeCommandLongHelp, want) && !strings.Contains(pipeCommandExampleHelp, want) {
			t.Fatalf("pipe help missing %q", want)
		}
	}
}

func TestMainPackageHelpDocsPathsExist(t *testing.T) {
	repoRoot := mainHelpDocsRepoRoot(t)
	texts := []string{
		rootCommandLongHelp,
		rootCommandExampleHelp,
		chatCommandLongHelp,
		chatCommandExampleHelp,
		configCommandLongHelp,
		configCommandExampleHelp,
		testCommandLongHelp,
		testCommandExampleHelp,
		contextCommandLongHelp,
		contextCommandExampleHelp,
		pipeCommandLongHelp,
		pipeCommandExampleHelp,
	}

	re := regexp.MustCompile(`(?:docs|backend/docs)/(?:aicli|skill_runtime)/[A-Za-z0-9_./-]+\.md`)
	seen := map[string]struct{}{}
	var missing []string
	for _, text := range texts {
		for _, match := range re.FindAllString(text, -1) {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			path := filepath.Join(repoRoot, filepath.FromSlash(match))
			if _, err := os.Stat(path); err != nil {
				missing = append(missing, match)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("main package help references missing docs: %s", strings.Join(missing, ", "))
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one docs path in main package help")
	}
}

func mainHelpDocsRepoRoot(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// testFile is backend/cmd/aicli/help_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))
}
