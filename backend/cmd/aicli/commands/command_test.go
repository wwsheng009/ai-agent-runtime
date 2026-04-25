package commands

import (
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
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

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
	effort, warning, err := resolveChatReasoningEffort("codex", " HIGH ", true)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
	if effort != "high" {
		t.Fatalf("expected effort high, got %q", effort)
	}
}

func TestResolveChatReasoningEffort_InvalidValue(t *testing.T) {
	_, _, err := resolveChatReasoningEffort("codex", "fast", true)
	if err == nil {
		t.Fatal("expected invalid reasoning-effort error")
	}
	if !strings.Contains(err.Error(), "low|medium|high|xhigh") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveChatReasoningEffort_OpenAIProtocol(t *testing.T) {
	effort, warning, err := resolveChatReasoningEffort("openai", "medium", true)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if effort != "medium" {
		t.Fatalf("expected medium effort for openai protocol, got %q", effort)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
}

func TestResolveChatReasoningEffort_DefaultsToMediumForCodex(t *testing.T) {
	effort, warning, err := resolveChatReasoningEffort("codex", "", false)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
	if effort != "medium" {
		t.Fatalf("expected default effort medium, got %q", effort)
	}
}

func TestResolveChatReasoningEffort_DefaultsToMediumForOpenAI(t *testing.T) {
	effort, warning, err := resolveChatReasoningEffort("openai", "", false)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if warning != "" {
		t.Fatalf("expected empty warning, got %q", warning)
	}
	if effort != "medium" {
		t.Fatalf("expected medium effort, got %q", effort)
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
		ProviderName:       "codex_ee",
		Provider:           config.Provider{Protocol: "codex"},
		Model:              "gpt-5.2-code",
		Stream:             false,
		ReasoningEffort:    "medium",
		Logger:             logger,
		SessionDir:         sessionDir,
		InputQueue:         queue,
		queuedInputDrain:   true,
		runtimeHTTPCapture: runtimeCapture,
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
	if !strings.HasSuffix(payload.LastHTTPRequestPath, "001_request_gateway_client.json") {
		t.Fatalf("unexpected payload last request artifact: %+v", payload)
	}
	if !strings.HasSuffix(payload.LastHTTPResponsePath, "001_response_gateway_client.json") {
		t.Fatalf("unexpected payload last response artifact: %+v", payload)
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
		ProviderName:       "codex_ee",
		Provider:           config.Provider{Protocol: "codex"},
		Model:              "gpt-5.2-code",
		Logger:             logger,
		SessionDir:         "sessions",
		runtimeHTTPCapture: runtimeCapture,
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
	if payload.LastHTTPRequestPath != resolveAbsoluteChatPath(requestPath) {
		t.Fatalf("expected absolute last request artifact path, got %+v", payload)
	}
	if payload.LastHTTPResponsePath != resolveAbsoluteChatPath(responsePath) {
		t.Fatalf("expected absolute last response artifact path, got %+v", payload)
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
	cmd.Flags().String("log-dir", "", "")
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

	output := captureStdout(t, func() {
		if quit := handleCommand(&ChatSession{}, "/compact remote", false); quit {
			t.Fatal("expected compact command not to exit")
		}
	})
	if !strings.Contains(output, "压缩完成") || !strings.Contains(output, "mode=remote") {
		t.Fatalf("expected compact command output, got %q", output)
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
