package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestNormalizeOutputFormat(t *testing.T) {
	format, err := normalizeOutputFormat("", "text", "text", "json")
	if err != nil || format != "text" {
		t.Fatalf("expected text fallback, got format=%q err=%v", format, err)
	}

	format, err = normalizeOutputFormat("JSON", "text", "text", "json")
	if err != nil || format != "json" {
		t.Fatalf("expected json normalization, got format=%q err=%v", format, err)
	}

	if _, err := normalizeOutputFormat("yaml", "text", "text", "json"); err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestResolveStructuredOutputOptions(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("output", "", "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("envelope", false, "")

	options, err := resolveStructuredOutputOptions(cmd, "pretty", "pretty", "json", "raw", "text")
	if err != nil || options.Format != "pretty" || options.Envelope {
		t.Fatalf("unexpected default options: %+v err=%v", options, err)
	}

	if err := cmd.Flags().Set("output", "text"); err != nil {
		t.Fatalf("set output: %v", err)
	}
	if err := cmd.Flags().Set("envelope", "true"); err != nil {
		t.Fatalf("set envelope: %v", err)
	}
	options, err = resolveStructuredOutputOptions(cmd, "pretty", "pretty", "json", "raw", "text")
	if err != nil || options.Format != "text" || !options.Envelope {
		t.Fatalf("unexpected explicit output options: %+v err=%v", options, err)
	}

	if err := cmd.Flags().Set("output", ""); err != nil {
		t.Fatalf("reset output: %v", err)
	}
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json alias: %v", err)
	}
	options, err = resolveStructuredOutputOptions(cmd, "pretty", "pretty", "json", "raw", "text")
	if err != nil || options.Format != "json" {
		t.Fatalf("unexpected json alias options: %+v err=%v", options, err)
	}
}

func TestExtractSimpleResponseText(t *testing.T) {
	openAI := []byte(`{"choices":[{"message":{"content":"hello world"}}],"usage":{"total_tokens":12}}`)
	if text := extractSimpleResponseText(openAI); text != "hello world" {
		t.Fatalf("unexpected openai response text: %q", text)
	}

	codex := []byte(`{"output":[{"content":[{"text":"hello codex"}]}]}`)
	if text := extractSimpleResponseText(codex); text != "hello codex" {
		t.Fatalf("unexpected codex response text: %q", text)
	}

	raw := []byte("plain text")
	if text := extractSimpleResponseText(raw); text != "plain text" {
		t.Fatalf("unexpected raw text response: %q", text)
	}

	nullError := []byte(`{"error":null,"output":[{"content":[{"text":"hello from codex"}]}]}`)
	if text := extractSimpleResponseText(nullError); text != "hello from codex" {
		t.Fatalf("unexpected null-error response text: %q", text)
	}

	sse := []byte("event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n" +
		"event: response.output_text.done\n" +
		"data: {\"type\":\"response.output_text.done\",\"text\":\"OK\"}\n")
	if text := extractSimpleResponseText(sse); text != "OK" {
		t.Fatalf("unexpected sse response text: %q", text)
	}
}

func TestBuildPipeResponsePayload(t *testing.T) {
	payload := buildPipeResponsePayload(&PipeSession{
		ProviderName: "codex_ee",
		Provider:     config.Provider{Protocol: "codex"},
		Model:        "gpt-5.2-code",
		Prompt:       "reply briefly",
	}, &pipeCommandResult{
		Response:   "hello from pipe",
		StatusCode: 200,
		DurationMs: 123,
		Usage: map[string]interface{}{
			"total_tokens": 44,
		},
		Raw: json.RawMessage(`{"usage":{"total_tokens":44}}`),
	})

	if payload.Response != "hello from pipe" || payload.StatusCode != 200 || payload.DurationMs != 123 {
		t.Fatalf("unexpected pipe payload core fields: %+v", payload)
	}
	if payload.Provider != "codex_ee" || payload.Protocol != "codex" || payload.Model != "gpt-5.2-code" {
		t.Fatalf("unexpected pipe payload identity fields: %+v", payload)
	}
	if payload.Prompt != "reply briefly" || payload.Usage["total_tokens"] != 44 {
		t.Fatalf("unexpected pipe payload usage fields: %+v", payload)
	}
}

func TestExtractUsageFromAnyResponseBody(t *testing.T) {
	sse := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"total_tokens\":32}}}\n")
	usage := extractUsageFromAnyResponseBody(sse)
	if usage == nil || usage["total_tokens"] != 32 {
		t.Fatalf("unexpected usage from sse: %#v", usage)
	}
}

func TestExtractCompletionTokensFromResponseBody(t *testing.T) {
	openAI := []byte(`{"usage":{"completion_tokens":17}}`)
	if tokens := extractCompletionTokensFromResponseBody(openAI); tokens != 17 {
		t.Fatalf("unexpected openai completion tokens: %d", tokens)
	}

	codex := []byte(`{"usage":{"output_tokens":23}}`)
	if tokens := extractCompletionTokensFromResponseBody(codex); tokens != 23 {
		t.Fatalf("unexpected codex output tokens: %d", tokens)
	}
}

func TestEmitCommandErrorJSON(t *testing.T) {
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	emitCommandError("chat", "json", errors.New("boom"), map[string]interface{}{"provider": "codex_ee"})

	_ = w.Close()
	os.Stdout = stdout
	data, _ := io.ReadAll(r)
	_ = r.Close()

	var payload map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(data), &payload); err != nil {
		t.Fatalf("expected json payload, got %q (%v)", string(data), err)
	}
	if payload["ok"] != false || payload["command"] != "chat" || payload["error"] != "boom" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestFormatCommandJSONOutputEnvelope(t *testing.T) {
	data := formatCommandJSONOutput("test", true, map[string]interface{}{"value": 1})
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["ok"] != true || payload["command"] != "test" {
		t.Fatalf("unexpected envelope payload: %#v", payload)
	}
	dataField, ok := payload["data"].(map[string]interface{})
	if !ok || dataField["value"] != float64(1) {
		t.Fatalf("unexpected envelope data: %#v", payload["data"])
	}
}
