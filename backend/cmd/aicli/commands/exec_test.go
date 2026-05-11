package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
)

func newTestExecCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "exec"}
	registerExecFlags(cmd)
	return cmd
}

func TestParseExecOptions_JSONModeConflictsWithOutput(t *testing.T) {
	cmd := newTestExecCommand()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json: %v", err)
	}
	if err := cmd.Flags().Set("output", "json"); err != nil {
		t.Fatalf("set output: %v", err)
	}

	_, err := parseExecOptions(cmd, []string{"hello"})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if got := execExitCode(err); got != execExitUsage {
		t.Fatalf("expected usage exit code, got %d", got)
	}
}

func TestParseExecOptions_MapsCommonFlags(t *testing.T) {
	cmd := newTestExecCommand()
	sets := map[string]string{
		"profile":          "dev",
		"agent":            "coder",
		"provider":         "local",
		"model":            "gpt-test",
		"stream":           "true",
		"reasoning-effort": "high",
		"approval-reuse":   "off",
		"debug-http":       "true",
		"skills-top-k":     "3",
		"config-override":  "model=gpt-test",
	}
	for name, value := range sets {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}
	opts, err := parseExecOptions(cmd, []string{"hello"})
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	if opts.ProfileFlag != "dev" || opts.AgentFlag != "coder" || opts.ProviderFlag != "local" || opts.ModelFlag != "gpt-test" {
		t.Fatalf("common flags were not mapped: %+v", opts)
	}
	if !opts.StreamFlag || !opts.StreamChanged || opts.ReasoningEffortFlag != "high" || !opts.HTTPDebug || opts.CLISkillsTopK != 3 {
		t.Fatalf("advanced flags were not mapped: %+v", opts)
	}
	if len(opts.ConfigOverrides) != 1 || opts.ConfigOverrides[0] != "model=gpt-test" {
		t.Fatalf("config overrides not mapped: %+v", opts.ConfigOverrides)
	}
}

func TestApplyConfigOverrides(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "default",
			Items: map[string]config.Provider{
				"default": {DefaultModel: "old", BaseURL: "http://old", APIKey: "old-key"},
			},
		},
	}
	err := applyConfigOverrides(cfg, []string{
		"model=new-model",
		"provider.base_url=http://new",
		"provider.api_key=new-key",
	})
	if err != nil {
		t.Fatalf("apply overrides: %v", err)
	}
	provider := cfg.Providers.Items["default"]
	if provider.DefaultModel != "new-model" || provider.BaseURL != "http://new" || provider.APIKey != "new-key" {
		t.Fatalf("unexpected provider after overrides: %+v", provider)
	}
}

func TestValidateExecFinalMessageSchema(t *testing.T) {
	schema := `{"type":"object","required":["summary"]}`
	if err := validateExecFinalMessageSchema(schema, `{"summary":"ok"}`); err != nil {
		t.Fatalf("expected valid message: %v", err)
	}
	if err := validateExecFinalMessageSchema(schema, `{"other":"no"}`); err == nil {
		t.Fatal("expected missing required field error")
	}
	if err := validateExecFinalMessageSchema(schema, `not-json`); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestJSONLEventProcessorEmitsThreadEvents(t *testing.T) {
	var buf bytes.Buffer
	processor := NewExecEventProcessor(true, &buf, "")
	processor.OnThreadStarted(ThreadStartedEvent{ThreadID: "thread_1", Model: "m", Provider: "p"})
	processor.OnWarning("careful")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 jsonl lines, got %d: %q", len(lines), buf.String())
	}
	var event ThreadEvent
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("invalid jsonl: %v", err)
	}
	if event.Type != EventTypeThreadStarted || event.ThreadID != "thread_1" || event.Sequence != 1 {
		t.Fatalf("unexpected first event: %+v", event)
	}
	if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
		t.Fatalf("invalid warning jsonl: %v", err)
	}
	if event.Type != EventTypeWarning || event.ThreadID != "thread_1" || event.Sequence != 2 {
		t.Fatalf("unexpected warning event: %+v", event)
	}
}

func TestExecEventBridgeUsesStableToolItemID(t *testing.T) {
	var buf bytes.Buffer
	processor := NewExecEventProcessor(true, &buf, "")
	processor.OnThreadStarted(ThreadStartedEvent{ThreadID: "thread_1", Model: "m", Provider: "p"})
	bridge := newExecEventBridge(processor)

	bridge.HandleChatCoreEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_requested",
		ToolName:   "shell",
		ToolCallID: "call-1",
		Arguments:  map[string]interface{}{"command": "echo hi"},
	})
	bridge.HandleChatCoreEvent(runtimechatcore.ChatEvent{
		Type:       runtimechatcore.EventTool,
		Stage:      "tool_result",
		ToolName:   "shell",
		ToolCallID: "call-1",
		Output:     "hi",
		Success:    true,
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 jsonl lines, got %d: %q", len(lines), buf.String())
	}
	started := decodeThreadEventData[ItemStartedEvent](t, lines[1])
	completed := decodeThreadEventData[ItemCompletedEvent](t, lines[2])
	if started.ItemID == "" || started.ItemID != completed.ItemID {
		t.Fatalf("expected stable item id, started=%q completed=%q", started.ItemID, completed.ItemID)
	}
}

func TestExecResumeLastTreatsArgsAsPrompt(t *testing.T) {
	cmd := newExecResumeCommand(func() *config.Config { return &config.Config{} })
	if err := cmd.Flags().Set("last", "true"); err != nil {
		t.Fatalf("set last: %v", err)
	}
	opts, err := parseExecOptionsNoPrompt(cmd)
	if err != nil {
		t.Fatalf("parse options: %v", err)
	}
	args := []string{"继续", "上次任务"}
	prompt := strings.TrimSpace(strings.Join(args, " "))
	opts.Prompt = prompt
	opts.ResumeArgs = &ExecResumeArgs{Last: true, Prompt: prompt}
	if opts.ResumeArgs.Prompt != "继续 上次任务" {
		t.Fatalf("expected --last args to be prompt, got %+v", opts.ResumeArgs)
	}
}

func TestExecReviewPromptCustomInstructionDefaultsUncommitted(t *testing.T) {
	prompt := buildReviewPrompt("diff --git a/a b/a", "检查安全漏洞", "", true, "", "", false)
	if !strings.Contains(prompt, "检查安全漏洞") {
		t.Fatalf("expected custom review instruction, got %q", prompt)
	}
	if !strings.Contains(prompt, "审查目标：未提交的更改") {
		t.Fatalf("expected default uncommitted target, got %q", prompt)
	}
}

func TestExecOutputLastMessageWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last.txt")
	processor := NewExecEventProcessor(true, &bytes.Buffer{}, path)
	processor.SetFinalResult(ExecFinalResult{Status: "completed", Message: "final message"})
	if err := processor.PrintFinalOutput(&ExecOptions{JSONMode: true}); err != nil {
		t.Fatalf("print final output: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read last message file: %v", err)
	}
	if string(data) != "final message" {
		t.Fatalf("unexpected last message file content: %q", string(data))
	}
}

func TestBuildReviewPrompt_DefaultInstructionAndShortSHA(t *testing.T) {
	prompt := buildReviewPrompt("diff --git a/a b/a", "", "fix bug", false, "", "abc123", false)
	if !strings.Contains(prompt, "fix bug (abc123)") {
		t.Fatalf("expected short sha without panic, got %q", prompt)
	}
	if !strings.Contains(prompt, "请对以下代码变更进行审查") {
		t.Fatalf("expected default review instruction, got %q", prompt)
	}
}

func decodeThreadEventData[T any](t *testing.T, line string) T {
	t.Helper()
	var event ThreadEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	var data T
	if err := json.Unmarshal(event.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return data
}
