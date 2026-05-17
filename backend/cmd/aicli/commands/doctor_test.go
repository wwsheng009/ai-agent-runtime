package commands

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func containsDoctorString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
