package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type directMetadataFunction struct {
	name     string
	output   string
	metadata map[string]interface{}
}

func (f *directMetadataFunction) Name() string { return f.name }

func (f *directMetadataFunction) Description() string { return "direct metadata function" }

func (f *directMetadataFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
	}
}

func (f *directMetadataFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return f.output, nil
}

func (f *directMetadataFunction) ExecuteWithMeta(ctx context.Context, args map[string]interface{}) (string, map[string]interface{}, error) {
	return f.output, f.metadata, nil
}

func TestFormatFunctionCatalogSummary_IncludesBuiltinAndSkillGroups(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters: map[string]interface{}{
			"type": "object",
		},
	})
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__alpha",
		skill: &runtimeskill.Skill{
			Name:         "alpha",
			Description:  "Alpha skill",
			Category:     "search",
			Capabilities: []string{"lookup"},
		},
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	summary := formatFunctionCatalogSummary(session, false)
	for _, expected := range []string{
		"Function Catalog: total=2 builtin=1 skills=1",
		"Builtin Tools:",
		"Skill Functions:",
		"builtin__diagnose [tool]",
		"skill__alpha [skill]",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("expected %q in summary:\n%s", expected, summary)
		}
	}
}

func TestFormatFunctionDescriptor_ReturnsDetailedSkillMetadata(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__alpha",
		skill: &runtimeskill.Skill{
			Name:         "alpha",
			Description:  "Alpha skill",
			Category:     "search",
			Capabilities: []string{"lookup"},
			Tags:         []string{"team"},
			Triggers: []runtimeskill.Trigger{
				{Type: "keyword", Values: []string{"alpha", "search"}},
			},
		},
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	details := formatFunctionDescriptor(session, "skill__alpha", false)
	for _, expected := range []string{
		"Function: skill__alpha",
		"Kind: skill",
		"Capability: alpha",
		"Description: Alpha skill",
		"Category: search",
		"Capabilities: lookup",
		"Triggers: keyword:alpha|search",
		"Metadata:",
	} {
		if !strings.Contains(details, expected) {
			t.Fatalf("expected %q in details:\n%s", expected, details)
		}
	}
}

func TestFormatFunctionDescriptor_ReturnsErrorWhenMissing(t *testing.T) {
	session := &ChatSession{
		FunctionCatalog:  newAICLIFunctionCatalog("openai", functions.NewFunctionRegistry()),
		FunctionRegistry: functions.NewFunctionRegistry(),
	}

	details := formatFunctionDescriptor(session, "missing__function", false)
	if !strings.Contains(details, "错误: 未找到 function: missing__function") {
		t.Fatalf("unexpected missing function message: %s", details)
	}
}

func TestFormatFunctionExposurePreview_ShowsFinalSelectionForPrompt(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters: map[string]interface{}{
			"type": "object",
		},
	})
	skillFn := &SkillFunction{
		functionName: "skill__alpha",
		skill: &runtimeskill.Skill{
			Name:         "alpha",
			Description:  "Alpha skill",
			Category:     "search",
			Capabilities: []string{"lookup"},
		},
	}
	catalog.RegisterSkillFunction(skillFn)

	binding := &skillsRuntimeBinding{
		exposureMode: skillExposurePrefer,
		catalog:      catalog,
		skillFunctions: map[string]*SkillFunction{
			"skill__alpha": skillFn,
		},
	}
	catalog.SetSkillsBinding(binding)

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		SkillsBinding:    binding,
		SkillsMode:       skillExposurePrefer,
	}

	preview := formatFunctionExposurePreview(session, "please use skill__alpha for this request", false)
	for _, expected := range []string{
		"Function Exposure Preview:",
		"Mode: prefer",
		"Include Builtin: false",
		"Builtin Exposed: <none>",
		"Skill Exposed: skill__alpha",
		"Final Functions: skill__alpha",
		"Explicit Mentions: skill__alpha",
		"Routed Skills: skill__alpha",
	} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("expected %q in preview:\n%s", expected, preview)
		}
	}
}

func TestExtractCommandArgument(t *testing.T) {
	if got := extractCommandArgument("/functions   use skill__alpha now"); got != "use skill__alpha now" {
		t.Fatalf("unexpected command argument: %q", got)
	}
	if got := extractCommandArgument("/functions"); got != "" {
		t.Fatalf("expected empty command argument, got %q", got)
	}
}

func TestFormatFunctionCatalogSummary_JSON(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters: map[string]interface{}{
			"type": "object",
		},
	})
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__alpha",
		skill:        &runtimeskill.Skill{Name: "alpha", Description: "Alpha skill"},
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	output := formatFunctionCatalogSummary(session, true)
	var payload struct {
		Stats struct {
			TotalFunctions int `json:"total_functions"`
			BuiltinTools   int `json:"builtin_tools"`
			SkillFunctions int `json:"skill_functions"`
		} `json:"stats"`
		Builtin []struct {
			FunctionName string `json:"function_name"`
		} `json:"builtin"`
		Skills []struct {
			FunctionName string `json:"function_name"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("expected JSON output, got error: %v\n%s", err, output)
	}
	if payload.Stats.TotalFunctions != 2 || payload.Stats.BuiltinTools != 1 || payload.Stats.SkillFunctions != 1 {
		t.Fatalf("unexpected stats: %+v", payload.Stats)
	}
	if len(payload.Builtin) != 1 || payload.Builtin[0].FunctionName != "builtin__diagnose" {
		t.Fatalf("unexpected builtin payload: %+v", payload.Builtin)
	}
	if len(payload.Skills) != 1 || payload.Skills[0].FunctionName != "skill__alpha" {
		t.Fatalf("unexpected skill payload: %+v", payload.Skills)
	}
}

func TestFormatFunctionDescriptor_JSON(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__alpha",
		skill:        &runtimeskill.Skill{Name: "alpha", Description: "Alpha skill"},
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	output := formatFunctionDescriptor(session, "skill__alpha", true)
	var payload struct {
		FunctionName string `json:"function_name"`
		Descriptor   struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"descriptor"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("expected JSON output, got error: %v\n%s", err, output)
	}
	if payload.FunctionName != "skill__alpha" {
		t.Fatalf("unexpected function_name: %s", payload.FunctionName)
	}
	if payload.Descriptor.Name != "alpha" || payload.Descriptor.Kind != "skill" {
		t.Fatalf("unexpected descriptor: %+v", payload.Descriptor)
	}
}

func TestFormatFunctionExposurePreview_JSON(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters: map[string]interface{}{
			"type": "object",
		},
	})
	skillFn := &SkillFunction{
		functionName: "skill__alpha",
		skill:        &runtimeskill.Skill{Name: "alpha"},
	}
	catalog.RegisterSkillFunction(skillFn)

	binding := &skillsRuntimeBinding{
		exposureMode: skillExposurePrefer,
		catalog:      catalog,
		skillFunctions: map[string]*SkillFunction{
			"skill__alpha": skillFn,
		},
	}
	catalog.SetSkillsBinding(binding)

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		SkillsBinding:    binding,
		SkillsMode:       skillExposurePrefer,
	}

	output := formatFunctionExposurePreview(session, "please use skill__alpha", true)
	var payload struct {
		Prompt             string   `json:"prompt"`
		Mode               string   `json:"mode"`
		IncludeBuiltin     bool     `json:"include_builtin"`
		SkillFunctions     []string `json:"skill_functions"`
		FinalFunctionNames []string `json:"final_function_names"`
		ExplicitMentions   []string `json:"explicit_mentions"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("expected JSON output, got error: %v\n%s", err, output)
	}
	if payload.Mode != skillExposurePrefer || payload.IncludeBuiltin {
		t.Fatalf("unexpected payload mode/include_builtin: %+v", payload)
	}
	if len(payload.SkillFunctions) != 1 || payload.SkillFunctions[0] != "skill__alpha" {
		t.Fatalf("unexpected skill functions: %+v", payload.SkillFunctions)
	}
	if len(payload.FinalFunctionNames) != 1 || payload.FinalFunctionNames[0] != "skill__alpha" {
		t.Fatalf("unexpected final functions: %+v", payload.FinalFunctionNames)
	}
	if len(payload.ExplicitMentions) != 1 || payload.ExplicitMentions[0] != "skill__alpha" {
		t.Fatalf("unexpected explicit mentions: %+v", payload.ExplicitMentions)
	}
}

func TestShellCommandCaptureLimit(t *testing.T) {
	tests := []struct {
		name string
		cfg  ShellCommandConfig
		want int
	}{
		{
			name: "disable output cap",
			cfg:  ShellCommandConfig{DisableOutputCap: true, OutputBytesCap: 8192, MaxOutputSize: 4096},
			want: runtimeexecutor.DisableRetainedOutputLimit,
		},
		{
			name: "prefer explicit output bytes cap",
			cfg:  ShellCommandConfig{OutputBytesCap: 8192, MaxOutputSize: 4096},
			want: 8192,
		},
		{
			name: "fallback to max output size",
			cfg:  ShellCommandConfig{MaxOutputSize: 4096},
			want: 4096,
		},
		{
			name: "fallback to executor default",
			cfg:  ShellCommandConfig{},
			want: runtimeexecutor.DefaultRetainedOutputBytes,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellCommandCaptureLimit(tt.cfg); got != tt.want {
				t.Fatalf("shellCommandCaptureLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseShellCommandInvocation(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantCommand   string
		wantCap       int
		wantDisabled  bool
		wantErrSubstr string
	}{
		{
			name:         "plain command",
			raw:          "git status --short",
			wantCommand:  "git status --short",
			wantCap:      runtimeexecutor.DefaultRetainedOutputBytes,
			wantDisabled: false,
		},
		{
			name:         "bang command with explicit cap",
			raw:          "! --output-bytes-cap 8192 git diff --stat",
			wantCommand:  "git diff --stat",
			wantCap:      8192,
			wantDisabled: false,
		},
		{
			name:         "equals syntax",
			raw:          "--output-bytes-cap=16384 git log --oneline",
			wantCommand:  "git log --oneline",
			wantCap:      16384,
			wantDisabled: false,
		},
		{
			name:         "disable output cap",
			raw:          "--disable-output-cap git diff HEAD -- README.md",
			wantCommand:  "git diff HEAD -- README.md",
			wantCap:      runtimeexecutor.DefaultRetainedOutputBytes,
			wantDisabled: true,
		},
		{
			name:          "conflicting options",
			raw:           "--disable-output-cap --output-bytes-cap 4096 git status",
			wantErrSubstr: "不能与 disable-output-cap 同时设置",
		},
		{
			name:          "missing command after options",
			raw:           "--output-bytes-cap 4096",
			wantErrSubstr: "需要在 shell 选项后提供要执行的命令",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCommand, gotCfg, err := parseShellCommandInvocation(tt.raw)
			if tt.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErrSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseShellCommandInvocation() error = %v", err)
			}
			if gotCommand != tt.wantCommand {
				t.Fatalf("unexpected command: got %q want %q", gotCommand, tt.wantCommand)
			}
			if gotCfg.OutputBytesCap != tt.wantCap {
				t.Fatalf("unexpected output bytes cap: got %d want %d", gotCfg.OutputBytesCap, tt.wantCap)
			}
			if gotCfg.DisableOutputCap != tt.wantDisabled {
				t.Fatalf("unexpected disable flag: got %t want %t", gotCfg.DisableOutputCap, tt.wantDisabled)
			}
		})
	}
}

func TestBuildShellCommandAIInputIncludesCaptureStatus(t *testing.T) {
	result := shellCommandResult{
		ExecutedCommand: "git diff --stat",
		Output:          "diff output",
		Shell: runtimeexecutor.Shell{
			Type: runtimeexecutor.ShellTypePwsh,
			Path: `C:\Program Files\PowerShell\7\pwsh.exe`,
		},
		Config: ShellCommandConfig{
			OutputBytesCap: 4096,
		},
		Capture: runtimeexecutor.CombinedOutputCapture{
			Output:            "diff output",
			Truncated:         true,
			TotalBytes:        8192,
			RetainedBytes:     4096,
			OmittedBytes:      4096,
			CaptureLimitBytes: 4096,
		},
	}

	aiInput := buildShellCommandAIInput(result)
	for _, expected := range []string{
		"我执行了命令: git diff --stat",
		`实际执行 Shell: pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
		"命令输出捕获状态: complete=false; limit=4096B; retained=4096B; total=8192B; omitted=4096B",
		"输出如下：",
		"diff output",
	} {
		if !strings.Contains(aiInput, expected) {
			t.Fatalf("expected %q in AI input:\n%s", expected, aiInput)
		}
	}
}

func TestBuildShellCommandAIInputIncludesArtifactHintWhenTruncated(t *testing.T) {
	result := shellCommandResult{
		ExecutedCommand:       "git diff --stat",
		Output:                "diff output",
		RawOutputArtifactPath: `E:\logs\shell\001_git.txt`,
		Shell: runtimeexecutor.Shell{
			Type: runtimeexecutor.ShellTypePowerShell,
			Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		},
		Config: ShellCommandConfig{
			OutputBytesCap: 4096,
		},
		Capture: runtimeexecutor.CombinedOutputCapture{
			Output:            "diff output",
			Truncated:         true,
			TotalBytes:        8192,
			RetainedBytes:     4096,
			OmittedBytes:      4096,
			CaptureLimitBytes: 4096,
		},
	}

	aiInput := buildShellCommandAIInput(result)
	if !strings.Contains(aiInput, "完整原始输出已旁路保存: E:\\logs\\shell\\001_git.txt") {
		t.Fatalf("expected artifact hint in AI input:\n%s", aiInput)
	}
}

func TestShellCommandDebugLineIncludesSelectedShell(t *testing.T) {
	line := shellCommandDebugLine(shellCommandResult{
		ExecutedCommand: "git status --short",
		Shell: runtimeexecutor.Shell{
			Type: runtimeexecutor.ShellTypePwsh,
			Path: `C:\Program Files\PowerShell\7\pwsh.exe`,
		},
		Capture: runtimeexecutor.CombinedOutputCapture{
			Output:        "ok",
			TotalBytes:    2,
			RetainedBytes: 2,
		},
	}, nil)

	for _, expected := range []string{
		`[shell-debug] local command="git status --short"`,
		`shell_type="pwsh"`,
		`shell_path="C:\\Program Files\\PowerShell\\7\\pwsh.exe"`,
		`success=true`,
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("expected %q in debug line:\n%s", expected, line)
		}
	}
}

func TestExtractCommandArgumentOptions(t *testing.T) {
	if arg, jsonOutput := extractCommandArgumentOptions("/functions --json"); arg != "" || !jsonOutput {
		t.Fatalf("unexpected parse result: arg=%q json=%t", arg, jsonOutput)
	}
	if arg, jsonOutput := extractCommandArgumentOptions("/functions --json use skill__alpha"); arg != "use skill__alpha" || !jsonOutput {
		t.Fatalf("unexpected parse result: arg=%q json=%t", arg, jsonOutput)
	}
	if arg, jsonOutput := extractCommandArgumentOptions("/function skill__alpha --json"); arg != "skill__alpha" || !jsonOutput {
		t.Fatalf("unexpected parse result: arg=%q json=%t", arg, jsonOutput)
	}
}

func TestResolveChatReasoningEffort(t *testing.T) {
	effort, warning, err := resolveChatReasoningEffort(config.Provider{
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"*": {
				ReasoningEfforts: []string{"HIGH", "max"},
			},
		},
	}, "gpt-5.4", " HIGH ", true)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
	if effort != "HIGH" {
		t.Fatalf("expected effort HIGH, got %q", effort)
	}
}

func TestResolveChatReasoningEffort_InvalidValue(t *testing.T) {
	effort, warning, err := resolveChatReasoningEffort(config.Provider{}, "gpt-5.4", "fast", true)
	if err != nil {
		t.Fatalf("expected nil error without configured capabilities, got %v", err)
	}
	if effort != "fast" {
		t.Fatalf("expected raw effort fast, got %q", effort)
	}
	if warning != "" {
		t.Fatalf("expected no warning without configured capabilities, got %q", warning)
	}
}

func TestResolveChatReasoningEffort_OpenAIProtocol(t *testing.T) {
	effort, warning, err := resolveChatReasoningEffort(config.Provider{}, "gpt-5.4", "medium", true)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if effort != "medium" {
		t.Fatalf("expected medium effort, got %q", effort)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
}

func TestResolveChatReasoningEffort_UsesNVIDIAFallbackCapability(t *testing.T) {
	provider := config.Provider{
		Protocol: "openai",
		BaseURL:  "https://integrate.api.nvidia.com",
	}

	effort, warning, err := resolveChatReasoningEffort(provider, "z-ai/glm4.7", "max", false)
	if err != nil {
		t.Fatalf("expected nil error for stored unsupported effort, got %v", err)
	}
	if effort != "" {
		t.Fatalf("expected unsupported nvidia effort to be cleared, got %q", effort)
	}
	if !strings.Contains(warning, "已清空") {
		t.Fatalf("expected clear warning, got %q", warning)
	}

	effort, warning, err = resolveChatReasoningEffort(provider, "z-ai/glm4.7", "max", true)
	if err == nil {
		t.Fatal("expected explicit unsupported nvidia effort to fail")
	}
	if effort != "" || warning != "" {
		t.Fatalf("expected empty effort/warning on explicit error, got effort=%q warning=%q", effort, warning)
	}
}

func TestResolveChatReasoningEffort_WithoutCapabilityDoesNotInjectDefault(t *testing.T) {
	effort, warning, err := resolveChatReasoningEffort(config.Provider{}, "gpt-5.4", "", false)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if effort != "" {
		t.Fatalf("expected empty effort without capability, got %q", effort)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
}

func TestResolveChatReasoningEffort_LeavesBlankWhenNoExplicitValue(t *testing.T) {
	provider := config.Provider{
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"*": {
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
			},
		},
	}
	effort, warning, err := resolveChatReasoningEffort(provider, "gpt-5.4", "", false)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if effort != "" {
		t.Fatalf("expected empty effort without explicit value, got %q", effort)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
}

func TestResolveChatReasoningEffort_UsesDeepSeekModelCapabilities(t *testing.T) {
	provider := config.Provider{
		Protocol: "openai",
		ModelCapabilities: map[string]config.ModelCapabilitySpec{
			"*": {
				ReasoningEfforts: []string{"high", "max"},
			},
		},
	}

	effort, warning, err := resolveChatReasoningEffort(provider, "deepseek-ai/DeepSeek-V4-Pro", "", false)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if effort != "" {
		t.Fatalf("expected empty effort without explicit value, got %q", effort)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}

	effort, warning, err = resolveChatReasoningEffort(provider, "deepseek-ai/DeepSeek-V4-Pro", "max", true)
	if err != nil {
		t.Fatalf("expected nil error for deepseek max, got %v", err)
	}
	if warning != "" {
		t.Fatalf("expected empty warning for deepseek max, got %q", warning)
	}
	if effort != "max" {
		t.Fatalf("expected deepseek max effort, got %q", effort)
	}
}

func TestShouldDisplayFinalResponse(t *testing.T) {
	if shouldDisplayFinalResponse(nil, "hello") {
		t.Fatal("expected nil session to be false")
	}
	if shouldDisplayFinalResponse(&ChatSession{Stream: true}, "hello") {
		t.Fatal("expected stream session to skip final response display")
	}
	if !shouldDisplayFinalResponse(&ChatSession{Stream: true, ChatExecutor: &aicliActorChatExecutor{}}, "hello") {
		t.Fatal("expected actor-first stream session to display final response fallback")
	}
	if shouldDisplayFinalResponse(&ChatSession{Stream: false}, "   ") {
		t.Fatal("expected blank response to skip final response display")
	}
	if !shouldDisplayFinalResponse(&ChatSession{Stream: false}, "hello") {
		t.Fatal("expected non-stream response to display")
	}
}

func TestShouldPrintChatSessionPreamble(t *testing.T) {
	if shouldPrintChatSessionPreamble(nil) {
		t.Fatal("expected nil session to suppress preamble")
	}
	if shouldPrintChatSessionPreamble(&ChatSession{NoInteractive: true}) {
		t.Fatal("expected no-interactive session to suppress preamble")
	}
	if shouldPrintChatSessionPreamble(&ChatSession{JSONOutput: true}) {
		t.Fatal("expected json session to suppress preamble")
	}
	if !shouldPrintChatSessionPreamble(&ChatSession{}) {
		t.Fatal("expected interactive session to print preamble")
	}
}

func TestBuildChatResponsePayload(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}
	logger.LogResponse(aicliLogScope{TurnID: "turn-0001", RequestID: "turn-0001-req-01"}, map[string]interface{}{
		"usage": map[string]interface{}{
			"total_tokens": 42,
		},
	}, []byte(`{"usage":{"total_tokens":42}}`), false, nil, 1234)
	sessionDir := t.TempDir()
	queue := &chatInputQueue{
		lines: make(chan chatQueuedInput, 4),
		errs:  make(chan error, 1),
	}
	queue.lines <- chatQueuedInput{Text: "queued-1\n", Source: "stdin"}
	queue.lines <- chatQueuedInput{Text: "queued-2\n", Source: "stdin"}
	runtimeCapture := &chatRuntimeHTTPCapture{}
	runtimeCapture.SetArtifactDir(logger.RuntimeHTTPArtifactDir())
	runtimeCapture.RecordArtifactPath("request", filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_request_gateway_client.json"))
	runtimeCapture.RecordArtifactPath("response", filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_response_gateway_client.json"))

	payload := buildChatResponsePayload(&ChatSession{
		ProviderName:               "codex_ee",
		Provider:                   config.Provider{Protocol: "codex"},
		Model:                      "gpt-5.2-code",
		Stream:                     false,
		ReasoningEffort:            "medium",
		Logger:                     logger,
		SessionDir:                 sessionDir,
		InputQueue:                 queue,
		queuedInputDrain:           true,
		runtimeHTTPCapture:         runtimeCapture,
		lastLocalShellArtifactPath: filepath.Join(logger.LocalShellArtifactDir(), "001_git.txt"),
		RuntimeSession: &runtimechat.Session{
			ID:    "session-123",
			State: runtimechat.StateActive,
		},
	}, "hello")

	if payload.Response != "hello" || payload.Provider != "codex_ee" || payload.Protocol != "codex" {
		t.Fatalf("unexpected payload core fields: %+v", payload)
	}
	if payload.Model != "gpt-5.2-code" || payload.ReasoningEffort != "medium" {
		t.Fatalf("unexpected payload model fields: %+v", payload)
	}
	if payload.SessionID != "session-123" || payload.SessionState != "active" {
		t.Fatalf("unexpected payload session fields: %+v", payload)
	}
	if payload.SessionPath != filepath.Join(sessionDir, payload.SessionID+".json") {
		t.Fatalf("unexpected payload session path: %+v", payload)
	}
	if !strings.Contains(payload.SessionStore, sessionDir) {
		t.Fatalf("unexpected payload session store: %+v", payload)
	}
	if payload.QueuedInputCount != 2 || !payload.QueuedInputDraining {
		t.Fatalf("expected queued input state to be reported, got %+v", payload)
	}
	if payload.TotalTokens != 42 || payload.ResponseTimeMs != 1234 {
		t.Fatalf("unexpected payload summary fields: %+v", payload)
	}
	if !strings.Contains(payload.LogPath, "chat_codex_ee_codex_gpt-5.2-code_") {
		t.Fatalf("unexpected payload log path: %+v", payload)
	}
	if payload.DebugLogPath != logger.DebugLogPath() {
		t.Fatalf("unexpected payload debug log path: %+v", payload)
	}
	if payload.HTTPArtifactDir != logger.RuntimeHTTPArtifactDir() {
		t.Fatalf("unexpected payload HTTP artifact dir: %+v", payload)
	}
	if payload.LocalShellArtifactDir != logger.LocalShellArtifactDir() {
		t.Fatalf("unexpected payload local shell artifact dir: %+v", payload)
	}
	if !strings.HasSuffix(payload.LastHTTPRequestPath, "001_request_gateway_client.json") {
		t.Fatalf("unexpected payload last request artifact: %+v", payload)
	}
	if !strings.HasSuffix(payload.LastHTTPResponsePath, "001_response_gateway_client.json") {
		t.Fatalf("unexpected payload last response artifact: %+v", payload)
	}
	if !strings.HasSuffix(payload.LastLocalShellArtifactPath, "001_git.txt") {
		t.Fatalf("unexpected payload last local shell artifact: %+v", payload)
	}
}

func TestResolveChatOutputFormat(t *testing.T) {
	if format, err := resolveChatOutputFormat(false, "", false); err != nil || format != "interactive" {
		t.Fatalf("expected interactive default, got format=%q err=%v", format, err)
	}
	if format, err := resolveChatOutputFormat(true, "", false); err != nil || format != "text" {
		t.Fatalf("expected text default, got format=%q err=%v", format, err)
	}
	if format, err := resolveChatOutputFormat(true, "", true); err != nil || format != "json" {
		t.Fatalf("expected json alias, got format=%q err=%v", format, err)
	}
	if _, err := resolveChatOutputFormat(false, "json", false); err == nil {
		t.Fatal("expected interactive json format to fail")
	}
	if _, err := resolveChatOutputFormat(true, "yaml", false); err == nil {
		t.Fatal("expected invalid output format to fail")
	}
}

func TestBuildChatResponsePayload_ResolvesRelativePathsToAbsolute(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tempWD := t.TempDir()
	if err := os.Chdir(tempWD); err != nil {
		t.Fatalf("chdir temp wd: %v", err)
	}
	defer func() {
		_ = os.Chdir(originalWD)
	}()

	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	if err := logger.SetLogDir("chat-logs"); err != nil {
		t.Fatalf("set relative log dir: %v", err)
	}
	runtimeCapture := &chatRuntimeHTTPCapture{}
	requestPath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_request_gateway_client.json")
	responsePath := filepath.Join(logger.RuntimeHTTPArtifactDir(), "001_response_gateway_client.json")
	runtimeCapture.RecordArtifactPath("request", requestPath)
	runtimeCapture.RecordArtifactPath("response", responsePath)

	payload := buildChatResponsePayload(&ChatSession{
		ProviderName:               "codex_ee",
		Provider:                   config.Provider{Protocol: "codex"},
		Model:                      "gpt-5.2-code",
		Logger:                     logger,
		SessionDir:                 "sessions",
		runtimeHTTPCapture:         runtimeCapture,
		lastLocalShellArtifactPath: filepath.Join(logger.LocalShellArtifactDir(), "001_git.txt"),
		RuntimeSession: &runtimechat.Session{
			ID:    "session-123",
			State: runtimechat.StateActive,
		},
	}, "hello")

	if payload.SessionPath != resolveAbsoluteChatPath(filepath.Join("sessions", "session-123.json")) {
		t.Fatalf("expected absolute session path, got %+v", payload)
	}
	if payload.LogPath != resolveAbsoluteChatPath(logger.SessionLogPath()) {
		t.Fatalf("expected absolute log path, got %+v", payload)
	}
	if payload.DebugLogPath != resolveAbsoluteChatPath(logger.DebugLogPath()) {
		t.Fatalf("expected absolute debug log path, got %+v", payload)
	}
	if payload.HTTPArtifactDir != resolveAbsoluteChatPath(logger.RuntimeHTTPArtifactDir()) {
		t.Fatalf("expected absolute HTTP artifact dir, got %+v", payload)
	}
	if payload.LocalShellArtifactDir != resolveAbsoluteChatPath(logger.LocalShellArtifactDir()) {
		t.Fatalf("expected absolute local shell artifact dir, got %+v", payload)
	}
	if payload.LastHTTPRequestPath != resolveAbsoluteChatPath(requestPath) {
		t.Fatalf("expected absolute last request artifact path, got %+v", payload)
	}
	if payload.LastHTTPResponsePath != resolveAbsoluteChatPath(responsePath) {
		t.Fatalf("expected absolute last response artifact path, got %+v", payload)
	}
	if payload.LastLocalShellArtifactPath != resolveAbsoluteChatPath(filepath.Join(logger.LocalShellArtifactDir(), "001_git.txt")) {
		t.Fatalf("expected absolute last local shell artifact path, got %+v", payload)
	}
}

func TestChatLoggerSessionLogPath(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}

	path := logger.SessionLogPath()
	if !strings.Contains(path, "chat_codex_ee_codex_gpt-5.2-code_") {
		t.Fatalf("unexpected session log path: %s", path)
	}
	if sessionDir := logger.SessionDirPath(); sessionDir == "" || filepath.Dir(path) != sessionDir {
		t.Fatalf("unexpected session dir path: %q (log path %q)", sessionDir, path)
	}
	if debugPath := logger.DebugLogPath(); debugPath == "" || filepath.Dir(debugPath) != logger.SessionDirPath() {
		t.Fatalf("unexpected debug log path: %q", debugPath)
	}
	if artifactDir := logger.RuntimeHTTPArtifactDir(); artifactDir == "" || filepath.Dir(artifactDir) != logger.SessionDirPath() {
		t.Fatalf("unexpected runtime HTTP artifact dir: %q", artifactDir)
	}
	if artifactDir := logger.LocalShellArtifactDir(); artifactDir == "" || filepath.Dir(artifactDir) != logger.SessionDirPath() {
		t.Fatalf("unexpected local shell artifact dir: %q", artifactDir)
	}

	summary := logger.CurrentSummary()
	if summary == nil {
		t.Fatal("expected current summary")
	}
	if summary.TotalResponses != 0 || summary.TotalRequests != 0 {
		t.Fatalf("unexpected empty summary: %+v", summary)
	}
	if summary.TotalDurationMs < 0 || summary.AverageResponseTimeMs < 0 {
		t.Fatalf("unexpected summary durations: %+v at %s", summary, time.Now())
	}
}

func TestNewChatLogger_UsesDefaultChatLogDir(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	defaultDir := ResolveDefaultChatLogDir()

	if logger.logDir != defaultDir {
		t.Fatalf("expected logger default dir %q, got %q", defaultDir, logger.logDir)
	}
	if got, want := logger.SessionDirPath(), filepath.Join(defaultDir, logger.sessionID); got != want {
		t.Fatalf("unexpected session dir path: got %q want %q", got, want)
	}
	if got := logger.SessionLogPath(); got == "" || filepath.Dir(got) != logger.SessionDirPath() {
		t.Fatalf("unexpected session log path: %q", got)
	}
	if got := logger.DebugLogPath(); got == "" || filepath.Dir(got) != logger.SessionDirPath() {
		t.Fatalf("unexpected debug log path: %q", got)
	}
}

func TestChatLoggerSetLogDirEnsuresSessionArtifacts(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	logDir := t.TempDir()
	if err := logger.SetLogDir(logDir); err != nil {
		t.Fatalf("set log dir: %v", err)
	}

	for _, path := range []string{
		logger.SessionDirPath(),
		logger.RuntimeHTTPArtifactDir(),
		logger.LocalShellArtifactDir(),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected artifact directory %q to exist: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %q to be a directory", path)
		}
	}

	debugPath := logger.DebugLogPath()
	info, err := os.Stat(debugPath)
	if err != nil {
		t.Fatalf("expected debug log file %q to exist: %v", debugPath, err)
	}
	if info.IsDir() {
		t.Fatalf("expected %q to be a file", debugPath)
	}
}

func TestWriteSessionDebugInfo_PersistsFileWithoutDebugFlags(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	if err := logger.SetLogDir(t.TempDir()); err != nil {
		t.Fatalf("set log dir: %v", err)
	}

	session := &ChatSession{Logger: logger}
	writeSessionDebugInfo(session, "[tool-debug] hello world", false)

	data, err := os.ReadFile(logger.DebugLogPath())
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	if !strings.Contains(string(data), "[tool-debug] hello world") {
		t.Fatalf("expected debug log to contain the written line, got:\n%s", string(data))
	}
}

func TestChatLoggerCurrentSummaryFallsBackToRawUsage(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-code", false, "https://example.com")
	logger.sessionLog.Messages = append(logger.sessionLog.Messages, ChatLogDetail{
		MessageType:    "response",
		Duration:       321,
		RawContentJSON: []byte(`{"usage":{"total_tokens":99}}`),
	})

	summary := logger.CurrentSummary()
	if summary == nil {
		t.Fatal("expected summary")
	}
	if summary.TotalTokens != 99 {
		t.Fatalf("expected total_tokens 99, got %+v", summary)
	}
}

func TestParseChatCommandOptions(t *testing.T) {
	cmd := &cobra.Command{Use: "chat"}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("agent", "", "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("stream", false, "")
	cmd.Flags().Bool("no-interactive", false, "")
	cmd.Flags().String("message", "", "")
	cmd.Flags().String("log-dir", ResolveDefaultChatLogDir(), "")
	cmd.Flags().String("request-timeout", "", "")
	cmd.Flags().String("reasoning-effort", "", "")
	cmd.Flags().Bool("disable-tools", false, "")
	cmd.Flags().Bool("debug-http", false, "")
	cmd.Flags().Bool("fail-fast", false, "")
	cmd.Flags().StringSlice("skills-dir", nil, "")
	cmd.Flags().Int("skills-top-k", 0, "")
	cmd.Flags().String("skills-mode", "auto", "")
	cmd.Flags().Bool("skills-debug", false, "")
	cmd.Flags().String("approval-reuse", "session_readonly_shell", "")
	cmd.Flags().String("output", "", "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("envelope", false, "")
	cmd.Flags().String("session", "", "")
	cmd.Flags().Bool("resume", false, "")
	cmd.Flags().Bool("list-sessions", false, "")
	cmd.Flags().String("session-dir", "", "")
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("session-state", "", "")
	cmd.Flags().String("session-provider", "", "")
	cmd.Flags().String("session-model", "", "")
	cmd.Flags().String("session-query", "", "")
	cmd.Flags().Int("session-limit", 20, "")

	_ = cmd.Flags().Set("no-interactive", "true")
	_ = cmd.Flags().Set("json", "true")
	_ = cmd.Flags().Set("envelope", "true")
	_ = cmd.Flags().Set("session-provider", "codex_ee")

	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	if err != nil {
		t.Fatalf("parseChatCommandOptions: %v", err)
	}
	if opts.OutputFormat != "json" || !opts.JSONEnvelope || !opts.NoInteractive {
		t.Fatalf("unexpected chat options: %+v", opts)
	}
	if opts.SessionFilter.Provider != "codex_ee" {
		t.Fatalf("unexpected session filter: %+v", opts.SessionFilter)
	}
	if opts.ApprovalReuseMode != chatApprovalReuseSessionReadOnlyShell {
		t.Fatalf("unexpected approval reuse mode: %+v", opts)
	}
	if opts.LogDir != ResolveDefaultChatLogDir() {
		t.Fatalf("unexpected default log dir: %+v", opts.LogDir)
	}
}

func TestHandleCommand_PermissionModeAndApprovalReuse(t *testing.T) {
	session := &ChatSession{
		PermissionMode:    "default",
		ApprovalReuseMode: chatApprovalReuseSessionReadOnlyShell,
		ActiveTeam:        &chatTeamBinding{TeamID: "team-1", AgentID: "lead"},
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/permission-mode", false); quit {
			t.Fatal("expected permission-mode command not to exit")
		}
		if quit := handleCommand(session, "/mode", false); quit {
			t.Fatal("expected mode alias command not to exit")
		}
		if quit := handleCommand(session, "/approval-reuse off", false); quit {
			t.Fatal("expected approval-reuse command not to exit")
		}
		if quit := handleCommand(session, "/yolo", false); quit {
			t.Fatal("expected yolo command not to exit")
		}
	})

	if !strings.Contains(output, "当前 permission-mode: default") {
		t.Fatalf("expected permission mode readback, got %q", output)
	}
	if !strings.Contains(output, "提示: 已切换到 approval-reuse=off") {
		t.Fatalf("expected approval-reuse change confirmation, got %q", output)
	}
	if !strings.Contains(output, "提示: 已切换到 permission-mode=bypass_permissions（等价于 --yolo）") {
		t.Fatalf("expected yolo confirmation, got %q", output)
	}
	if session.PermissionMode != "bypass_permissions" {
		t.Fatalf("expected session permission mode to change, got %+v", session)
	}
	if session.ActiveTeam == nil || session.ActiveTeam.PermissionMode != "bypass_permissions" {
		t.Fatalf("expected active team permission mode to track session mode, got %+v", session.ActiveTeam)
	}
	if session.ApprovalReuseMode != chatApprovalReuseOff {
		t.Fatalf("expected approval reuse mode off, got %+v", session)
	}
}

func TestHandleCommand_QueueStatusAndClear(t *testing.T) {
	queue := &chatInputQueue{
		lines: make(chan chatQueuedInput, 4),
		errs:  make(chan error, 1),
	}
	queue.lines <- chatQueuedInput{Text: "queued-1\n", Source: "stdin"}
	queue.lines <- chatQueuedInput{Text: "queued-2\n", Source: "stdin"}
	session := &ChatSession{
		InputQueue:       queue,
		queuedInputDrain: true,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/queue", false); quit {
			t.Fatal("expected queue command not to exit")
		}
		if quit := handleCommand(session, "/queue clear", false); quit {
			t.Fatal("expected queue clear command not to exit")
		}
	})

	if !strings.Contains(output, "当前 queued input: 2 pending (draining)") {
		t.Fatalf("expected queue status readback, got %q", output)
	}
	if !strings.Contains(output, "已清空 queued input: 2") {
		t.Fatalf("expected queue clear confirmation, got %q", output)
	}
	if lenQueuedInteractiveInput(session) != 0 {
		t.Fatalf("expected queue to be empty after clear")
	}
}

func TestHandleCommand_ClearResetsConversationTokenUsage(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	session := &ChatSession{
		SessionManager:          manager,
		SessionUserID:           userID,
		RuntimeSession:          runtimeSession,
		TokenCount:              12345,
		ContextTokenCount:       888,
		ContextWindowTokenCount: 777,
		TurnContextTokenCount:   666,
		MsgCount:                3,
		TurnRequestCount:        2,
	}
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewSystemMessage("previous system prompt"),
		*runtimetypes.NewUserMessage("hello"),
	})

	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/clear", false); quit {
			t.Fatal("expected clear command not to exit")
		}
	})

	if !strings.Contains(output, "当前会话历史已清空") {
		t.Fatalf("expected clear confirmation, got %q", output)
	}
	if session.TokenCount != 0 {
		t.Fatalf("expected token count to reset, got %d", session.TokenCount)
	}
	if session.ContextTokenCount != 0 || session.ContextWindowTokenCount != 0 || session.TurnContextTokenCount != 0 {
		t.Fatalf("expected turn-level token usage to reset, got ctx=%d window=%d turn=%d",
			session.ContextTokenCount, session.ContextWindowTokenCount, session.TurnContextTokenCount)
	}
	if session.MsgCount != 0 || session.TurnRequestCount != 0 {
		t.Fatalf("expected message counters to reset, got msgs=%d turnRequests=%d", session.MsgCount, session.TurnRequestCount)
	}
	if got, ok := runtimeSessionContextInt(session.RuntimeSession, chatRuntimeContextTokenCount); ok {
		t.Fatalf("expected token count metadata to be removed, got %d", got)
	}
}

func TestCreateNewRuntimeConversationResetsConversationTokenUsage(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	existingSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	session := &ChatSession{
		SessionManager:          manager,
		SessionUserID:           userID,
		RuntimeSession:          existingSession,
		TokenCount:              54321,
		ContextTokenCount:       444,
		ContextWindowTokenCount: 333,
		TurnContextTokenCount:   222,
		MsgCount:                5,
		TurnRequestCount:        4,
	}

	if err := createNewRuntimeConversation(session, ""); err != nil {
		t.Fatalf("createNewRuntimeConversation: %v", err)
	}
	if session.RuntimeSession == nil {
		t.Fatal("expected runtime session to be created")
	}
	if session.TokenCount != 0 {
		t.Fatalf("expected token count to reset, got %d", session.TokenCount)
	}
	if session.ContextTokenCount != 0 || session.ContextWindowTokenCount != 0 || session.TurnContextTokenCount != 0 {
		t.Fatalf("expected turn-level token usage to reset, got ctx=%d window=%d turn=%d",
			session.ContextTokenCount, session.ContextWindowTokenCount, session.TurnContextTokenCount)
	}
	if session.MsgCount != 0 || session.TurnRequestCount != 0 {
		t.Fatalf("expected message counters to reset, got msgs=%d turnRequests=%d", session.MsgCount, session.TurnRequestCount)
	}
	if got, ok := runtimeSessionContextInt(session.RuntimeSession, chatRuntimeContextTokenCount); ok {
		t.Fatalf("expected token count metadata to be removed, got %d", got)
	}
}

func TestHandleCommand_Compact(t *testing.T) {
	originalRunner := runManualChatCompact
	defer func() {
		runManualChatCompact = originalRunner
	}()

	runManualChatCompact = func(session *ChatSession, requestedMode string) (*chatCompactReport, error) {
		return &chatCompactReport{
			RequestedMode: requestedMode,
			Result: &compactruntime.Result{
				Mode:              compactruntime.ModeRemote,
				ResolvedProvider:  "codex_ee",
				ResolvedModel:     "gpt-5",
				TokenBefore:       900,
				TokenAfter:        120,
				CompactedMessages: 4,
			},
			Status: compactruntime.Status{
				Mode:             compactruntime.ModeRemote,
				ResolvedProvider: "codex_ee",
				ResolvedModel:    "gpt-5",
			},
		}, nil
	}

	session := &ChatSession{TokenCount: 5000, TurnContextTokenCount: 999}
	output := captureStdout(t, func() {
		if quit := handleCommand(session, "/compact remote", false); quit {
			t.Fatal("expected compact command not to exit")
		}
	})
	if !strings.Contains(output, "压缩完成") || !strings.Contains(output, "mode=remote") {
		t.Fatalf("expected compact command output, got %q", output)
	}
	if session.ContextTokenCount != 120 {
		t.Fatalf("expected compact command to reset context usage to token_after, got %d", session.ContextTokenCount)
	}
	if session.TokenCount != 5000 {
		t.Fatalf("expected compact command to preserve cumulative API token count, got %d", session.TokenCount)
	}
	if session.TurnContextTokenCount != 0 {
		t.Fatalf("expected compact command to clear turn aggregate context usage, got %d", session.TurnContextTokenCount)
	}
}

func TestHandleCommand_DirectFunctionCall_JSON(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterFunction(&directMetadataFunction{
		name:   "openai_image_generate",
		output: "Generated image saved to file:///tmp/demo.png",
		metadata: map[string]interface{}{
			"provider":   "OPENAI_IMAGE",
			"saved_path": "E:/tmp/demo.png",
		},
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, `/call openai_image_generate {"prompt":"hi"} --json`, false); quit {
			t.Fatal("expected call command not to exit")
		}
	})

	var payload directFunctionInvokeReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		t.Fatalf("expected JSON output, got error: %v\n%s", err, output)
	}
	if payload.FunctionName != "openai_image_generate" {
		t.Fatalf("unexpected function name: %+v", payload)
	}
	if payload.Metadata["provider"] != "OPENAI_IMAGE" {
		t.Fatalf("unexpected metadata: %+v", payload.Metadata)
	}
}

func TestHandleCommand_DirectSkillCall_UsesPromptShortcut(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	executor := &fakeSkillExecutor{
		result: &runtimeskill.ExecuteResult{
			SkillName: "imagegen",
			Success:   true,
			Output:    "Generated image saved to file:///tmp/skill-image.png",
		},
	}
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__imagegen",
		skill: &runtimeskill.Skill{
			Name:        "imagegen",
			Description: "Generate image",
		},
		executor: executor,
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	output := captureStdout(t, func() {
		if quit := handleCommand(session, `/skill imagegen 帮我生成一张风景图`, false); quit {
			t.Fatal("expected skill command not to exit")
		}
	})

	if executor.lastReq == nil || executor.lastReq.Prompt != "帮我生成一张风景图" {
		t.Fatalf("expected prompt shortcut to reach skill executor, got %#v", executor.lastReq)
	}
	if !strings.Contains(output, "Generated image saved to") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestHandleCommand_SkillsMenu_SelectsAndExecutes(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	executor := &fakeSkillExecutor{
		result: &runtimeskill.ExecuteResult{
			SkillName: "imagegen",
			Success:   true,
			Output:    "Generated image saved to file:///tmp/skill-image.png",
		},
	}
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__imagegen",
		skill: &runtimeskill.Skill{
			Name:        "imagegen",
			Description: "Generate image",
		},
		executor: executor,
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		InputReader:      bufio.NewReader(strings.NewReader("1\n帮我生成一张风景图\n")),
	}

	stdout, stderr := captureStdoutStderr(t, func() {
		if quit := handleCommand(session, "/skills", false); quit {
			t.Fatal("expected skills menu command not to exit")
		}
	})

	if !strings.Contains(stderr, "选择 Skill") {
		t.Fatalf("expected selection menu to be shown, got stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Skill Catalog: total=1") {
		t.Fatalf("expected skill catalog listing, got stderr:\n%s", stderr)
	}
	if executor.lastReq == nil || executor.lastReq.Prompt != "帮我生成一张风景图" {
		t.Fatalf("expected prompt to reach selected skill executor, got %#v", executor.lastReq)
	}
	if !strings.Contains(stdout, "Generated image saved to") {
		t.Fatalf("unexpected stdout: %s", stdout)
	}
}

func TestHandleCommand_DirectSkillCall_WithoutArgsShowsUsage(t *testing.T) {
	output := captureStdout(t, func() {
		if quit := handleCommand(&ChatSession{}, "/skill", false); quit {
			t.Fatal("expected bare skill command not to exit")
		}
	})

	if strings.Contains(output, "未知命令") {
		t.Fatalf("expected bare /skill to be routed to the skill handler, got: %s", output)
	}
	if !strings.Contains(output, "需要指定 skill 名称") {
		t.Fatalf("expected usage hint for bare /skill, got: %s", output)
	}
}

func TestExecuteShellCommandDetailed_DangerousCommandCancelsOnEOF(t *testing.T) {
	session := &ChatSession{
		cancelCtx:  context.Background(),
		InputQueue: newChatInputQueue(bufio.NewReader(strings.NewReader(""))),
	}

	_, err := executeShellCommandDetailed(session, "rm -rf /tmp/test-shell-confirm")
	if err == nil {
		t.Fatal("expected dangerous shell confirmation to cancel on EOF")
	}
	if err.Error() != "命令已取消" {
		t.Fatalf("expected command cancellation, got %v", err)
	}
}

func TestResolveChatProviderAndModelName(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "gpt-4.1",
				},
			},
		},
	}
	opts := &chatCommandOptions{}
	if provider := resolveChatProviderName(cfg, opts, nil); provider != "alpha" {
		t.Fatalf("unexpected provider: %s", provider)
	}
	if model := resolveChatModelName(cfg.Providers.Items["alpha"], opts, nil); model != "gpt-4.1" {
		t.Fatalf("unexpected model: %s", model)
	}

	session := runtimechat.NewSession("tester")
	session.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProviderName: "alpha",
		chatRuntimeContextModel:        "restored-model",
		chatRuntimeContextStream:       true,
	}
	if provider := resolveChatProviderName(cfg, &chatCommandOptions{}, session); provider != "alpha" {
		t.Fatalf("unexpected restored provider: %s", provider)
	}
	if model := resolveChatModelName(cfg.Providers.Items["alpha"], &chatCommandOptions{}, session); model != "restored-model" {
		t.Fatalf("unexpected restored model: %s", model)
	}
	if !resolveChatStreamMode(&chatCommandOptions{}, session) {
		t.Fatal("expected restored stream mode")
	}
}

func TestPrepareChatRuntimeState(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "codex",
					BaseURL:      "https://example.com",
					DefaultModel: "gpt-5.2-code",
				},
			},
		},
	}
	opts := &chatCommandOptions{
		ProviderFlag:       "alpha",
		ModelFlag:          "gpt-5.2-code",
		NoInteractive:      true,
		OutputFormat:       "json",
		RequestTimeoutFlag: "45s",
		FailFast:           true,
	}

	state, details, err := prepareChatRuntimeState(cfg, opts, nil)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}
	if details != nil {
		t.Fatalf("expected nil details on success, got %+v", details)
	}
	if state.providerName != "alpha" || state.modelName != "gpt-5.2-code" {
		t.Fatalf("unexpected runtime state identity: %+v", state)
	}
	if state.adapter == nil || state.baseURL == "" {
		t.Fatalf("expected adapter and baseURL, got %+v", state)
	}
	if !state.retryCfg.DisableRetries || state.requestTimeout != 45*time.Second {
		t.Fatalf("unexpected runtime state retry/timeout: %+v", state)
	}
}

func TestPrepareChatRuntimeState_PreservesReasoningEffortOnRestore(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "codex",
					BaseURL:      "https://example.com",
					DefaultModel: "gpt-5.2-code",
				},
			},
		},
	}
	loaded := runtimechat.NewSession("tester")
	loaded.Metadata.Context = map[string]interface{}{
		chatRuntimeContextReasoningEffort: "  HIGH  ",
	}
	state, details, err := prepareChatRuntimeState(cfg, &chatCommandOptions{
		ProviderFlag:  "alpha",
		ModelFlag:     "gpt-5.2-code",
		NoInteractive: true,
		OutputFormat:  "json",
	}, loaded)
	if err != nil {
		t.Fatalf("prepareChatRuntimeState: %v", err)
	}
	if details != nil {
		t.Fatalf("expected nil details, got %+v", details)
	}
	if state.reasoningEffort != "HIGH" {
		t.Fatalf("expected restored reasoning effort HIGH, got %q", state.reasoningEffort)
	}
}
