package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestInitCommandHelpMentionsStarterPathAndDocs(t *testing.T) {
	cmd := NewInitCommand()
	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		".aicli/config.yaml",
		"--global",
		"aicli login --provider openai",
		"docs/aicli/quickstart.md",
		"aicli init --global",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("init help missing %q\n%s", want, text)
		}
	}
}

func TestLoginCommandHelpMentionsOnboardingAndDocs(t *testing.T) {
	cmd := NewLoginCommand(func() *config.Config { return nil })
	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"aicli init --global",
		"aicli doctor provider",
		"/login",
		"docs/aicli/faq.md",
		"--set-default",
		"codex-oauth",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("login help missing %q\n%s", want, text)
		}
	}
}

func TestDoctorCommandHelpMentionsProviderMatrixAndDocs(t *testing.T) {
	cmd := NewDoctorCommand(func() *config.Config { return nil })
	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"doctor provider",
		"doctor search",
		"doctor subagent-route",
		"aicli doctor provider",
		"docs/aicli/faq.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor help missing %q\n%s", want, text)
		}
	}
	searchCmd, _, err := cmd.Find([]string{"search"})
	if err != nil {
		t.Fatalf("Find search: %v", err)
	}
	searchText := searchCmd.Long + "\n" + searchCmd.Example
	for _, want := range []string{"aicli doctor search", "AICLI_RG_PATH", "builtin"} {
		if !strings.Contains(searchText, want) {
			t.Fatalf("doctor search help missing %q\n%s", want, searchText)
		}
	}

	providerCmd, _, err := cmd.Find([]string{"provider"})
	if err != nil {
		t.Fatalf("Find provider: %v", err)
	}
	providerText := providerCmd.Long + "\n" + providerCmd.Example
	for _, want := range []string{
		"aicli doctor provider",
		"--include-yolo=false",
		"--json",
	} {
		if !strings.Contains(providerText, want) {
			t.Fatalf("doctor provider help missing %q\n%s", want, providerText)
		}
	}
}

func TestProviderCommandHelpMentionsManagementAndDocs(t *testing.T) {
	cmd := NewProviderCommand(func() *config.Config { return nil })
	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"aicli provider list",
		"aicli provider show",
		"aicli provider set-default",
		"aicli login",
		"aicli doctor provider",
		"docs/aicli/quickstart.md",
		"docs/aicli/faq.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("provider help missing %q\n%s", want, text)
		}
	}
}

func TestExecCommandHelpMentionsHeadlessPathAndDocs(t *testing.T) {
	cmd := NewExecCommand(func() *config.Config { return nil })
	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"aicli exec resume",
		"aicli exec review",
		"aicli login",
		"aicli doctor provider",
		"docs/aicli/exec.md",
		"docs/aicli/faq.md",
		"--json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("exec help missing %q\n%s", want, text)
		}
	}
}

func TestUninstallCommandHelpMentionsInstallDocs(t *testing.T) {
	cmd := NewUninstallCommand()
	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"docs/aicli/install.md",
		"--dry-run",
		"--user-only",
		"--local-only",
		"不删除 aicli 可执行文件",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("uninstall help missing %q\n%s", want, text)
		}
	}
}

func TestImageCommandHelpMentionsImageDocs(t *testing.T) {
	cmd := NewImageCommand(func() *config.Config { return nil })
	text := cmd.Long + "\n" + cmd.Example
	for _, want := range []string{
		"docs/aicli/tool_image_generate.md",
		"path=auto",
		"/image",
		"openai_image_generate",
		"--output-dir",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("image help missing %q\n%s", want, text)
		}
	}
}

func TestAgentCommandHelpMentionsAgentsAndExecDocs(t *testing.T) {
	cmd := NewAgentCommand(func() *config.Config { return nil })
	text := cmd.Long
	for _, want := range []string{
		"aicli agent stdio",
		"docs/aicli/agents.md",
		"docs/aicli/install.md",
		"docs/aicli/exec.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent help missing %q\n%s", want, text)
		}
	}

	stdioCmd, _, err := cmd.Find([]string{"stdio"})
	if err != nil {
		t.Fatalf("Find stdio: %v", err)
	}
	stdioText := stdioCmd.Long + "\n" + stdioCmd.Example
	for _, want := range []string{
		"Agent Client Protocol",
		"docs/aicli/agents.md",
		"docs/aicli/exec.md",
		"--enable-tools",
	} {
		if !strings.Contains(stdioText, want) {
			t.Fatalf("agent stdio help missing %q\n%s", want, stdioText)
		}
	}
}

func TestExecSubcommandsHelpMentionExecDocs(t *testing.T) {
	cmd := NewExecCommand(func() *config.Config { return nil })
	for _, name := range []string{"resume", "review"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil {
			t.Fatalf("Find %s: %v", name, err)
		}
		if !strings.Contains(sub.Long, "docs/aicli/exec.md") {
			t.Fatalf("exec %s help missing docs/aicli/exec.md\n%s", name, sub.Long)
		}
	}
}

func TestDoctorSubagentRouteHelpMentionsAgentsDocs(t *testing.T) {
	cmd := NewDoctorCommand(func() *config.Config { return nil })
	sub, _, err := cmd.Find([]string{"subagent-route"})
	if err != nil {
		t.Fatalf("Find subagent-route: %v", err)
	}
	text := sub.Long
	for _, want := range []string{
		"docs/aicli/agents.md",
		"docs/aicli/faq.md",
		"spawn_agent",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor subagent-route help missing %q\n%s", want, text)
		}
	}
}

func TestMCPCommandHelpMentionsInstallDocs(t *testing.T) {
	cmd := MCPCommand()
	text := cmd.Long
	for _, want := range []string{
		"docs/aicli/install.md",
		"MCP",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("mcp help missing %q\n%s", want, text)
		}
	}
}

func TestSkillCommandHelpMentionsSkillsAndAgentsDocs(t *testing.T) {
	cmd := NewSkillCommand()
	text := cmd.Long
	for _, want := range []string{
		"docs/aicli/install.md",
		"docs/skill_runtime/aicli_skills_usage.md",
		"docs/aicli/agents.md",
		"SKILL.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("skill help missing %q\n%s", want, text)
		}
	}
}

func TestPluginCommandHelpMentionsSkillsAndAgentsDocs(t *testing.T) {
	cmd := NewPluginCommand()
	text := cmd.Long
	for _, want := range []string{
		"docs/aicli/install.md",
		"docs/skill_runtime/aicli_skills_usage.md",
		"docs/aicli/agents.md",
		"plugin.yaml",
		"aicli plugin trust",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plugin help missing %q\n%s", want, text)
		}
	}
}

func TestCommandHelpDocsPathsExist(t *testing.T) {
	repoRoot := helpDocsRepoRoot(t)
	agentCmd := NewAgentCommand(func() *config.Config { return nil })
	agentStdioLong := ""
	if stdioCmd, _, err := agentCmd.Find([]string{"stdio"}); err == nil {
		agentStdioLong = stdioCmd.Long
	}
	execCmd := NewExecCommand(func() *config.Config { return nil })
	execResumeLong, execReviewLong := "", ""
	if resumeCmd, _, err := execCmd.Find([]string{"resume"}); err == nil {
		execResumeLong = resumeCmd.Long
	}
	if reviewCmd, _, err := execCmd.Find([]string{"review"}); err == nil {
		execReviewLong = reviewCmd.Long
	}
	doctorCmd := NewDoctorCommand(func() *config.Config { return nil })
	doctorSubagentLong := ""
	if sub, _, err := doctorCmd.Find([]string{"subagent-route"}); err == nil {
		doctorSubagentLong = sub.Long
	}
	texts := []string{
		NewInitCommand().Long,
		NewLoginCommand(func() *config.Config { return nil }).Long,
		NewChatCommand(func() *config.Config { return nil }).Long,
		NewResumeCommand(func() *config.Config { return nil }).Long,
		doctorCmd.Long,
		doctorSubagentLong,
		NewProviderCommand(func() *config.Config { return nil }).Long,
		execCmd.Long,
		execResumeLong,
		execReviewLong,
		NewUninstallCommand().Long,
		NewImageCommand(func() *config.Config { return nil }).Long,
		agentCmd.Long,
		agentStdioLong,
		MCPCommand().Long,
		NewSkillCommand().Long,
		NewPluginCommand().Long,
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
		t.Fatalf("help references missing docs: %s", strings.Join(missing, ", "))
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one docs path in command help")
	}
}

func TestAICLIUserDocsCatalogExists(t *testing.T) {
	repoRoot := helpDocsRepoRoot(t)
	required := []string{
		"docs/aicli/README.md",
		"docs/aicli/quickstart.md",
		"docs/aicli/install.md",
		"docs/aicli/faq.md",
		"docs/aicli/exec.md",
		"docs/aicli/agents.md",
		"docs/aicli/tool_image_generate.md",
		"docs/skill_runtime/aicli_skills_usage.md",
		"backend/docs/aicli/prompt-layout-debug-note.md",
	}
	var missing []string
	for _, rel := range required {
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("required aicli docs missing: %s", strings.Join(missing, ", "))
	}

	readme, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash("docs/aicli/README.md")))
	if err != nil {
		t.Fatalf("read docs/aicli/README.md: %v", err)
	}
	for _, want := range []string{
		"quickstart.md",
		"install.md",
		"faq.md",
		"exec.md",
		"agents.md",
		"tool_image_generate.md",
		"skill / plugin / agent",
		"aicli agent stdio",
	} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("docs/aicli/README.md missing catalog entry %q", want)
		}
	}

	rootReadme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	for _, want := range []string{
		"docs/aicli/quickstart.md",
		"docs/aicli/install.md",
		"docs/aicli/faq.md",
		"docs/aicli/exec.md",
		"docs/aicli/agents.md",
		"docs/aicli/tool_image_generate.md",
		"docs/aicli/README.md",
	} {
		if !strings.Contains(string(rootReadme), want) {
			t.Fatalf("root README.md missing aicli docs entry %q", want)
		}
	}

	docsReadme, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash("docs/README.md")))
	if err != nil {
		t.Fatalf("read docs/README.md: %v", err)
	}
	for _, want := range []string{
		"aicli/quickstart.md",
		"aicli/exec.md",
		"aicli/agents.md",
		"aicli/tool_image_generate.md",
	} {
		if !strings.Contains(string(docsReadme), want) {
			t.Fatalf("docs/README.md missing aicli docs entry %q", want)
		}
	}
}

func helpDocsRepoRoot(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// testFile is backend/cmd/aicli/commands/help_docs_regression_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", "..", ".."))
}
