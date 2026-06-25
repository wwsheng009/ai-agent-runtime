package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestDoctorProviderCasesUseExplicitConfigAndModes(t *testing.T) {
	opts := doctorProviderOptions{
		Message:         "只回复 OK",
		RequestTimeout:  "30s",
		Timeout:         45 * time.Second,
		IncludeYolo:     true,
		IncludeToolChat: true,
	}
	cases := doctorProviderCases(`C:\Users\vince\.aicli\config.yaml`, "mimo_anthropic", "mimo-v2.5-pro", opts)
	names := make([]string, 0, len(cases))
	for _, item := range cases {
		names = append(names, item.Name)
		if len(item.Args) < 3 || item.Args[0] != "--config" {
			t.Fatalf("%s did not put root --config before subcommand: %#v", item.Name, item.Args)
		}
		if !containsDoctorString(item.Args, "mimo_anthropic") || !containsDoctorString(item.Args, "mimo-v2.5-pro") {
			t.Fatalf("%s missing explicit provider/model: %#v", item.Name, item.Args)
		}
	}
	for _, want := range []string{"test-direct", "exec-disable-tools", "chat-disable-tools", "chat-with-tools", "exec-yolo"} {
		if !containsDoctorString(names, want) {
			t.Fatalf("missing case %s in %#v", want, names)
		}
	}
	if !containsDoctorString(cases[1].Args, "--disable-tools") {
		t.Fatalf("exec-disable-tools missing --disable-tools: %#v", cases[1].Args)
	}
	if containsDoctorString(cases[3].Args, "--disable-tools") {
		t.Fatalf("chat-with-tools should expose tools: %#v", cases[3].Args)
	}
	if !containsDoctorString(cases[4].Args, "--yolo") {
		t.Fatalf("exec-yolo missing --yolo: %#v", cases[4].Args)
	}
}

func TestSummarizeDoctorHTTPArtifactRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "001_request_provider_wrapper.json")
	raw := `{
  "phase": "request",
  "protocol": "anthropic",
  "model": "mimo-v2.5-pro",
  "attempt": 1,
  "max_attempts": 1,
  "method": "POST",
  "url": "https://example.test/anthropic/v1/messages",
  "body_bytes": 123,
  "body_format": "json",
  "body_json": {
    "model": "mimo-v2.5-pro",
    "stream": true,
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "hi"}],
    "system": "abc",
    "output_config": {"effort": "max"},
    "thinking": {"type": "adaptive", "effort": "max"},
    "tools": [{"name": "bash"}, {"name": "aicli_exec"}]
  }
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got := summarizeDoctorHTTPArtifact(path)
	if got == nil {
		t.Fatal("summary is nil")
	}
	if got.BodyModel != "mimo-v2.5-pro" || got.ToolCount != 2 || got.MessageCount != 1 {
		t.Fatalf("unexpected body summary: %#v", got)
	}
	if got.Stream == nil || !*got.Stream {
		t.Fatalf("expected stream=true, got %#v", got.Stream)
	}
	if got.OutputEffort != "max" || got.ThinkingEffort != "max" {
		t.Fatalf("expected reasoning effort summaries, got %#v", got)
	}
}

func TestSummarizeDoctorChatDebugLogRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.log")
	raw := `[2026-05-17 22:50:52.822] [http-debug] POST https://example.test/anthropic/v1/messages
[http-debug] disable_retries=true attempts=1 final_status=200
[http-debug] request_body_bytes=123
[http-debug] request_body={"model":"mimo-v2.5-pro","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"system":"abc","output_config":{"effort":"max"},"thinking":{"type":"adaptive","effort":"max"}}
[http-debug] request_headers={"X-Api-Key":["tp-***"]}
[http-debug] attempt=1 status=200 duration_ms=10 response_bytes=99 error="" preview="ok"`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	req, resp := summarizeDoctorChatDebugLog(path)
	if req == nil || resp == nil {
		t.Fatalf("expected request and response summaries, got req=%#v resp=%#v", req, resp)
	}
	if req.URL != "https://example.test/anthropic/v1/messages" || req.BodyModel != "mimo-v2.5-pro" {
		t.Fatalf("unexpected request summary: %#v", req)
	}
	if req.Stream == nil || !*req.Stream || req.OutputEffort != "max" || req.ThinkingEffort != "max" {
		t.Fatalf("unexpected request mode summary: %#v", req)
	}
	if resp.ResponseStatusCode != 200 || resp.BodyBytes != 99 {
		t.Fatalf("unexpected response summary: %#v", resp)
	}
}

func TestRunDoctorSubagentRouteUsesConfiguredHardRoute(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		ConfigFilePath: "config.yaml",
		Providers: config.ProvidersConfig{
			DefaultProvider: "parent",
			Items: map[string]config.Provider{
				"parent": {
					Enabled:      true,
					DefaultModel: "parent-model",
					MaxToken:     2048,
				},
				"strong": {
					Enabled:         true,
					DefaultModel:    "strong-default",
					SupportedModels: []string{"strong-model"},
					ModelCapabilities: map[string]config.ModelCapabilitySpec{
						"strong-model": {
							ReasoningModel:   true,
							ReasoningEfforts: []string{"high"},
						},
					},
				},
			},
		},
		AICLI: &config.AICLIConfig{
			Subagents: &config.AICLISubagentsConfig{
				Routing: &config.AICLISubagentRoutingConfig{
					Enabled: &enabled,
					Levels: map[string]config.AICLISubagentRouteProfile{
						"hard": {
							Provider:        "strong",
							Model:           "strong-model",
							ReasoningEffort: "high",
							MaxTokens:       12000,
							Timeout:         5 * time.Minute,
						},
					},
				},
			},
		},
	}

	report, _, err := runDoctorSubagentRoute(cfg, doctorSubagentRouteOptions{
		Role:       "researcher",
		Goal:       "Analyze provider protocol migration.",
		Difficulty: "hard",
		ReadOnly:   true,
	})
	if err != nil {
		t.Fatalf("runDoctorSubagentRoute failed: %v", err)
	}
	if !report.RoutingEnabled {
		t.Fatal("expected routing enabled")
	}
	if report.Decision.Provider != "strong" ||
		report.Decision.Model != "strong-model" ||
		report.Decision.ReasoningEffort != "high" ||
		report.Decision.Source != "difficulty_level" {
		t.Fatalf("unexpected decision: %#v", report.Decision)
	}
	if report.Decision.MaxTokens != 12000 || report.Decision.Timeout != "5m0s" {
		t.Fatalf("unexpected route budget: %#v", report.Decision)
	}
}

func TestRunDoctorSubagentRouteSpawnTeamWritePathPreview(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "parent",
			Items: map[string]config.Provider{
				"parent": {Enabled: true, DefaultModel: "parent-model"},
				"strong": {Enabled: true, DefaultModel: "strong-model"},
			},
		},
		AICLI: &config.AICLIConfig{
			Subagents: &config.AICLISubagentsConfig{
				Routing: &config.AICLISubagentRoutingConfig{
					Enabled: &enabled,
					Roles: map[string]map[string]config.AICLISubagentRouteProfile{
						"writer": {
							"hard": {Provider: "strong", Model: "strong-model", ReasoningEffort: "high"},
						},
					},
				},
			},
		},
	}

	report, _, err := runDoctorSubagentRoute(cfg, doctorSubagentRouteOptions{
		Workflow:   "spawn_team",
		TeamID:     "team-1",
		Teammate:   "member-1",
		TaskID:     "task-1",
		Difficulty: "hard",
		WritePaths: []string{"src/foo.go", "src/foo.go"},
		ReadOnly:   true,
	})
	if err != nil {
		t.Fatalf("runDoctorSubagentRoute failed: %v", err)
	}
	if report.Request.Workflow != "spawn_team" ||
		report.Request.TeamID != "team-1" ||
		report.Request.Teammate != "member-1" ||
		report.Request.TaskID != "task-1" {
		t.Fatalf("unexpected spawn_team request context: %#v", report.Request)
	}
	if report.Request.Role != "writer" || report.Request.ReadOnly {
		t.Fatalf("expected write-path spawn_team preview to infer writer writable task, got %#v", report.Request)
	}
	if len(report.Request.WritePaths) != 1 || report.Request.WritePaths[0] != "src/foo.go" {
		t.Fatalf("unexpected write paths: %#v", report.Request.WritePaths)
	}
	if report.Decision.Provider != "strong" ||
		report.Decision.Model != "strong-model" ||
		report.Decision.ReasoningEffort != "high" ||
		report.Decision.Source != "role_override" {
		t.Fatalf("unexpected spawn_team route decision: %#v", report.Decision)
	}
}

func TestRunDoctorSubagentRouteUsesChatDefaultsForParent(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "global-parent",
			Items: map[string]config.Provider{
				"global-parent": {
					Enabled:      true,
					DefaultModel: "global-model",
				},
				"chat-parent": {
					Enabled:      true,
					DefaultModel: "provider-fallback-model",
					MaxToken:     4096,
				},
			},
		},
		AICLI: &config.AICLIConfig{
			Chat: &config.AICLIChatConfig{
				DefaultProvider: "chat-parent",
				DefaultModel:    "chat-model",
				ReasoningEffort: "medium",
			},
			Subagents: &config.AICLISubagentsConfig{
				Routing: &config.AICLISubagentRoutingConfig{
					Enabled: &enabled,
				},
			},
		},
	}

	report, _, err := runDoctorSubagentRoute(cfg, doctorSubagentRouteOptions{
		Difficulty: "normal",
		ReadOnly:   true,
	})
	if err != nil {
		t.Fatalf("runDoctorSubagentRoute failed: %v", err)
	}
	if report.Parent.Provider != "chat-parent" ||
		report.Parent.Model != "chat-model" ||
		report.Parent.ReasoningEffort != "medium" ||
		report.Parent.MaxTokens != 4096 {
		t.Fatalf("expected parent defaults from aicli.chat, got %#v", report.Parent)
	}
}

func TestDoctorSubagentRouteCommandFlagsJSON(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		ConfigFilePath: "config.yaml",
		Providers: config.ProvidersConfig{
			DefaultProvider: "parent",
			Items: map[string]config.Provider{
				"parent": {
					Enabled:      true,
					DefaultModel: "parent-model",
				},
				"strong": {
					Enabled:         true,
					DefaultModel:    "strong-default",
					SupportedModels: []string{"strong-model"},
					ModelCapabilities: map[string]config.ModelCapabilitySpec{
						"strong-model": {
							ReasoningModel:   true,
							ReasoningEfforts: []string{"high"},
						},
					},
				},
			},
		},
		AICLI: &config.AICLIConfig{
			Subagents: &config.AICLISubagentsConfig{
				Routing: &config.AICLISubagentRoutingConfig{
					Enabled: &enabled,
					Levels: map[string]config.AICLISubagentRouteProfile{
						"hard": {
							Provider:        "strong",
							Model:           "strong-model",
							ReasoningEffort: "high",
						},
					},
				},
			},
		},
	}
	cmd := NewDoctorCommand(func() *config.Config { return cfg })
	cmd.SetArgs([]string{"subagent-route", "--role", "writer", "--difficulty", "hard", "--json"})

	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("doctor subagent-route failed: %v", err)
		}
	})
	for _, expected := range []string{
		`"routing_enabled":true`,
		`"role":"writer"`,
		`"difficulty":"hard"`,
		`"provider":"strong"`,
		`"model":"strong-model"`,
		`"reasoning_effort":"high"`,
		`"source":"difficulty_level"`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected JSON output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestRunDoctorSubagentRouteDisabledPreservesLegacyModelOverride(t *testing.T) {
	disabled := false
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "parent",
			Items: map[string]config.Provider{
				"parent": {
					Enabled:      true,
					DefaultModel: "parent-model",
					MaxToken:     2048,
				},
			},
		},
		AICLI: &config.AICLIConfig{
			Subagents: &config.AICLISubagentsConfig{
				Routing: &config.AICLISubagentRoutingConfig{
					Enabled:           &disabled,
					DefaultDifficulty: "not-a-real-difficulty",
				},
			},
		},
	}

	report, _, err := runDoctorSubagentRoute(cfg, doctorSubagentRouteOptions{
		Provider:        "ignored-provider",
		Model:           "child-model",
		ReasoningEffort: "high",
		ReadOnly:        true,
	})
	if err != nil {
		t.Fatalf("runDoctorSubagentRoute failed: %v", err)
	}
	if report.RoutingEnabled {
		t.Fatal("expected routing disabled")
	}
	if report.Decision.Provider != "parent" ||
		report.Decision.Model != "child-model" ||
		report.Decision.ReasoningEffort != "" ||
		report.Decision.Source != "disabled" {
		t.Fatalf("unexpected disabled decision: %#v", report.Decision)
	}
}

func containsDoctorString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
