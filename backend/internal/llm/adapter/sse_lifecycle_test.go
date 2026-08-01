package adapter

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestOpenAIHandleResponse_StreamResolvesMissingToolIndex(t *testing.T) {
	msg, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]},"finish_reason":null}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"id\":1}"}}]},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	calls, _ := msg["tool_calls"].([]map[string]interface{})
	if len(calls) != 1 {
		t.Fatalf("expected one tool call, got %#v", msg["tool_calls"])
	}
	function, _ := calls[0]["function"].(map[string]interface{})
	if function["name"] != "lookup" || function["arguments"] != `{"id":1}` {
		t.Fatalf("unexpected tool call: %#v", calls[0])
	}
}

func TestOpenAIHandleResponse_StreamRejectsMalformedToolArguments(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"content\":\"truncated"}}]},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "invalid_tool_arguments") {
		t.Fatalf("expected invalid_tool_arguments, got %v", err)
	}
}

func TestOpenAIHandleResponse_StreamPreservesRefusal(t *testing.T) {
	msg, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"refusal":"I cannot help with that."},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["refusal"] != "I cannot help with that." || msg["content"] != "I cannot help with that." {
		t.Fatalf("refusal was not preserved: %#v", msg)
	}
	metadata, _ := msg["metadata"].(map[string]interface{})
	if metadata["refused"] != true {
		t.Fatalf("expected structured refusal metadata, got %#v", metadata)
	}
}

func TestOpenAIHandleResponse_StreamHandlesNestedResponseFailure(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.failed",
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"E5001","type":"streaming_error","message":"stream interrupted"}}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "E5001") || !strings.Contains(err.Error(), "stream interrupted") {
		t.Fatalf("unexpected response.failed error: %v", err)
	}
}

func TestOpenAIHandleResponse_StreamRejectsEmptyErrorEvent(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader("event: error\n\n"), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "empty error event") {
		t.Fatalf("expected empty error event failure, got %v", err)
	}
}

func TestOpenAIHandleResponse_NonStreamNormalizesLegacyFunctionCall(t *testing.T) {
	msg, err := (&OpenAIAdapter{}).HandleResponse(false, strings.NewReader(
		`{"choices":[{"message":{"role":"assistant","content":null,"function_call":{"name":"lookup","arguments":"{\"id\":1}"}},"finish_reason":"function_call"}]}`,
	), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	calls, _ := msg["tool_calls"].([]map[string]interface{})
	if len(calls) != 1 || calls[0]["id"] != "legacy_function_call_1" {
		t.Fatalf("legacy function call was not normalized: %#v", msg)
	}
	if msg["finish_reason"] != "function_call" {
		t.Fatalf("finish reason was not preserved: %#v", msg)
	}
}

func TestCodexHandleResponse_StreamPreservesRefusal(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.refusal.delta",
		`data: {"type":"response.refusal.delta","delta":"I cannot comply."}`,
		"",
		"event: response.refusal.done",
		`data: {"type":"response.refusal.done","refusal":"I cannot comply."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed","stop_reason":"stop"}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["refusal"] != "I cannot comply." || msg["content"] != "I cannot comply." {
		t.Fatalf("refusal was not preserved: %#v", msg)
	}
}

func TestCodexHandleResponse_NonStreamRejectsFailedStatus(t *testing.T) {
	_, err := (&CodexAdapter{}).HandleResponse(false, strings.NewReader(
		`{"status":"failed","error":{"code":"E5001","message":"upstream failed"}}`,
	), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "E5001") || !strings.Contains(err.Error(), "upstream failed") {
		t.Fatalf("unexpected failed response error: %v", err)
	}
}

func TestCodexHandleResponse_StreamPreservesMaxOutputIncompleteReason(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
		"event: response.incomplete",
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("max-output incomplete response should reach escalation logic: %v", err)
	}
	if msg["finish_reason"] != "max_output_tokens" || msg["content"] != "partial" {
		t.Fatalf("unexpected incomplete response: %#v", msg)
	}
}

func TestCodexHandleResponse_AutoDetectsBOMPrefixedSSE(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(false, strings.NewReader(strings.Join([]string{
		"\uFEFFevent: response.output_text.done",
		`data:{"type":"response.output_text.done","text":"ok"}`,
		"",
		"event: response.completed",
		`data:{"type":"response.completed","response":{"status":"completed","stop_reason":"stop"}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil || msg["content"] != "ok" {
		t.Fatalf("failed to auto-detect BOM SSE: msg=%#v err=%v", msg, err)
	}
}

func TestCodexHandleResponse_RecoversContentPartDone(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.content_part.done",
		`data: {"type":"response.content_part.done","part":{"type":"output_text","text":"recovered"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil || msg["content"] != "recovered" || msg["finish_reason"] != "stop" {
		t.Fatalf("failed to recover content part: msg=%#v err=%v", msg, err)
	}
}

func TestCodexHandleResponse_StreamsCallbacksBeforeEOF(t *testing.T) {
	reader, writer := io.Pipe()
	deltas := make(chan string, 1)
	results := make(chan error, 1)
	go func() {
		_, err := (&CodexAdapter{}).HandleResponse(true, reader, StreamCallbacks{
			OnText: func(delta string) { deltas <- delta },
		})
		results <- err
	}()

	if _, err := io.WriteString(writer, strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"live"}`,
		"",
	}, "\n")+"\n"); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	select {
	case delta := <-deltas:
		if delta != "live" {
			t.Fatalf("unexpected live delta %q", delta)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback was buffered until EOF")
	}

	_, _ = io.WriteString(writer, strings.Join([]string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")+"\n")
	_ = writer.Close()
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("HandleResponse failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleResponse did not finish after EOF")
	}
}

func TestCodexHandleResponse_RecoversCompletedResponseSnapshot(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_final","model":"gpt-5.4","status":"completed","usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7},"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"checked"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"recovered"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"id\":1}"}]}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["content"] != "recovered" || msg["reasoning_content"] != "checked" || msg["finish_reason"] != "stop" {
		t.Fatalf("final response snapshot was not recovered: %#v", msg)
	}
	calls, _ := msg["tool_calls"].([]map[string]interface{})
	fn, _ := calls[0]["function"].(map[string]interface{})
	if len(calls) != 1 || calls[0]["id"] != "call_1" || fn["name"] != "lookup" || fn["arguments"] != `{"id":1}` {
		t.Fatalf("final tool call was not recovered: %#v", calls)
	}
}

func TestCodexHandleResponse_RejectsFailureHiddenByMismatchedEventName(t *testing.T) {
	_, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "upstream_error") || !strings.Contains(err.Error(), "Upstream request failed") {
		t.Fatalf("mismatched SSE event masked the failure: %v", err)
	}
}

func TestCodexHandleResponse_RejectsFailedCompletedSnapshot(t *testing.T) {
	_, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"failed","error":{"code":"server_error","message":"final snapshot failed"}}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "server_error") || !strings.Contains(err.Error(), "final snapshot failed") {
		t.Fatalf("failed final snapshot was accepted: %v", err)
	}
}

func TestCodexHandleResponse_PreservesAnnotationsAndUnknownEventDiagnostics(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.output_text.annotation.added",
		`data: {"type":"response.output_text.annotation.added","annotation":{"type":"url_citation","url":"https://example.com"}}`,
		"",
		"event: response.future_progress",
		`data: {"type":"response.future_progress","progress":0.5}`,
		"",
		"event: response.output_text.done",
		`data: {"type":"response.output_text.done","text":"answer"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	metadata, _ := msg["metadata"].(map[string]interface{})
	annotations, _ := metadata["annotations"].([]map[string]interface{})
	if len(annotations) != 1 || annotations[0]["type"] != "url_citation" {
		t.Fatalf("annotations were not preserved: %#v", metadata)
	}
	unknown, _ := metadata["sse_unknown_events"].(map[string]interface{})
	if unknown["response.future_progress"] != 1 {
		t.Fatalf("unknown event diagnostics missing: %#v", metadata)
	}
}

func TestCodexHandleResponse_DeduplicatesImageTerminalPhases(t *testing.T) {
	var phases []string
	_, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"image_generation_call","id":"img_1"}}`,
		"",
		"event: response.image_generation_call.completed",
		`data: {"type":"response.image_generation_call.completed","output_index":0,"id":"img_1"}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","id":"img_1","status":"completed"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")), StreamCallbacks{OnImage: func(metadata map[string]interface{}) {
		phases = append(phases, asCodexString(metadata["phase"]))
	}})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if got := strings.Join(phases, ","); got != "started,completed" {
		t.Fatalf("duplicate image phases were emitted: %s", got)
	}
}

func TestCodexHandleResponse_RecognizesCompatibilityDoneMarkers(t *testing.T) {
	for name, stream := range map[string]string{
		"done event": strings.Join([]string{
			"event: response.output_text.done",
			`data: {"type":"response.output_text.done","text":"ok"}`,
			"",
			"event: done",
			"",
		}, "\n"),
		"done sentinel": strings.Join([]string{
			"event: response.output_text.done",
			`data: {"type":"response.output_text.done","text":"ok"}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(stream), StreamCallbacks{})
			if err != nil || msg["content"] != "ok" || msg["finish_reason"] != "stop" {
				t.Fatalf("compatibility done marker failed: msg=%#v err=%v", msg, err)
			}
		})
	}
}

func TestCodexHandleResponse_RejectsEmptyFailureLifecycleEvent(t *testing.T) {
	_, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader("event: response.failed\n\n"), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "empty response.failed event") {
		t.Fatalf("empty failure event was accepted: %v", err)
	}
}

func TestCodexHandleResponse_RejectsMalformedFunctionArguments(t *testing.T) {
	_, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"write","arguments":"{\"content\":\"truncated"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil || !strings.Contains(err.Error(), "invalid_tool_arguments") {
		t.Fatalf("malformed Codex tool arguments were accepted: %v", err)
	}
}

func TestCodexHandleResponse_ResolvesFunctionCallWithoutOutputIndex(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"item_1","call_id":"call_1","delta":"{\"id\":"}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"lookup","arguments":""}}`,
		"",
		"event: response.function_call_arguments.done",
		`data: {"type":"response.function_call_arguments.done","item_id":"item_1","call_id":"call_1","name":"lookup","arguments":"{\"id\":1}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	calls, _ := msg["tool_calls"].([]map[string]interface{})
	fn, _ := calls[0]["function"].(map[string]interface{})
	if len(calls) != 1 || calls[0]["id"] != "call_1" || fn["name"] != "lookup" || fn["arguments"] != `{"id":1}` {
		t.Fatalf("indexless function call was not assembled: %#v", calls)
	}
}

func TestCodexHandleResponse_FinalSnapshotDoesNotDuplicateMultipleTextItems(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"first"}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","output_index":1,"delta":"second"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"first"}]},{"type":"message","content":[{"type":"output_text","text":"second"}]}]}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("HandleResponse failed: %v", err)
	}
	if msg["content"] != "firstsecond" {
		t.Fatalf("final response snapshot duplicated text: %#v", msg["content"])
	}
}

func TestCodexHandleResponse_IncompleteSnapshotRecoversOutputForEscalation(t *testing.T) {
	msg, err := (&CodexAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		"event: response.incomplete",
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial answer"}]}]}}`,
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("max-output incomplete response should reach escalation logic: %v", err)
	}
	if msg["content"] != "partial answer" || msg["finish_reason"] != "max_output_tokens" {
		t.Fatalf("incomplete response snapshot was not recovered: %#v", msg)
	}
}
