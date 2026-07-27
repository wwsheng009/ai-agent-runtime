package commands

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTailCaptureBufferRetainsFixedTail(t *testing.T) {
	buffer := newTailCaptureBuffer(1024)
	for index := 0; index < 256; index++ {
		payload := bytes.Repeat([]byte{byte(index)}, 4096)
		written, err := buffer.Write(payload)
		if err != nil || written != len(payload) {
			t.Fatalf("write failed: written=%d err=%v", written, err)
		}
	}
	if got := len(buffer.Bytes()); got != 1024 {
		t.Fatalf("expected 1024 retained bytes, got %d", got)
	}
	if got := buffer.TotalBytes(); got != 256*4096 {
		t.Fatalf("unexpected total byte count: %d", got)
	}
	if !bytes.Equal(buffer.Bytes(), bytes.Repeat([]byte{255}, 1024)) {
		t.Fatalf("buffer did not retain the newest tail")
	}
}

func TestTruncateUTF8ByteSliceReturnsValidBoundedText(t *testing.T) {
	data := append(bytes.Repeat([]byte("a"), 31), []byte("界tail")...)
	preview := truncateUTF8ByteSlice(data, 33)
	if len(preview) > 33 || !utf8.ValidString(preview) {
		t.Fatalf("expected valid bounded preview, got %q", preview)
	}
}

func TestChatLoggerBoundsDetailsAndPreservesCumulativeSummary(t *testing.T) {
	logger := NewChatLogger("provider", "openai", "model", true, "")
	large := strings.Repeat("payload", 10*1024)
	requestCount := chatLogRetainedMessages/2 + 100
	for index := 0; index < requestCount; index++ {
		scope := aicliLogScope{TurnID: "turn", RequestID: "request"}
		logger.LogRequest(scope, large)
		logger.LogResponse(scope, large, []byte(large), true, nil, 10)
	}
	if got := len(logger.sessionLog.Messages); got != chatLogRetainedMessages {
		t.Fatalf("expected %d retained details, got %d", chatLogRetainedMessages, got)
	}
	summary := logger.CurrentSummary()
	if summary.TotalRequests != requestCount || summary.TotalResponses != requestCount {
		t.Fatalf("cumulative summary lost dropped details: %+v", summary)
	}
	if want := requestCount*2 - chatLogRetainedMessages; logger.sessionLog.DroppedMessages != want {
		t.Fatalf("expected %d dropped details to be recorded, got %d", want, logger.sessionLog.DroppedMessages)
	}
	if summary.AverageResponseTimeMs != 10 {
		t.Fatalf("unexpected response average: %+v", summary)
	}
	for _, detail := range logger.sessionLog.Messages {
		if len(detail.RawContent) > chatLogRawMaxBytes {
			t.Fatalf("raw log detail exceeded bound: %d", len(detail.RawContent))
		}
	}
	logger.SetInitialMessage(large)
	if len(logger.sessionLog.InitialMessage) > chatLogContentMaxBytes {
		t.Fatalf("initial message exceeded bound: %d", len(logger.sessionLog.InitialMessage))
	}
}

func TestBoundChatLogContentPreservesDiagnosticEnvelope(t *testing.T) {
	payload := map[string]interface{}{
		"event_type":              "llm.request.finished",
		"llm_request_id":          "llm-1",
		"trace_id":                "trace-1",
		"success":                 true,
		"usage_prompt_tokens":     120,
		"usage_completion_tokens": 10,
		"usage_total_tokens":      130,
		"usage_source":            "provider_reported",
		"response_body":           strings.Repeat("x", chatLogContentMaxBytes*2),
		"error":                   "sensitive upstream response",
	}
	bounded, ok := boundChatLogContent(payload).(map[string]interface{})
	if !ok || bounded["truncated"] != true {
		t.Fatalf("expected bounded diagnostic map, got %#v", bounded)
	}
	if bounded["usage_total_tokens"] != float64(130) && bounded["usage_total_tokens"] != 130 {
		t.Fatalf("usage envelope was not preserved: %#v", bounded)
	}
	if bounded["llm_request_id"] != "llm-1" || bounded["diagnostic_envelope_preserved"] != true {
		t.Fatalf("request identity was not preserved: %#v", bounded)
	}
	if _, leaked := bounded["response_body"]; leaked {
		t.Fatal("large response body leaked into diagnostic envelope")
	}
	if _, leaked := bounded["error"]; leaked {
		t.Fatal("raw error leaked into diagnostic envelope")
	}
	if bounded["error_present"] != true {
		t.Fatal("expected non-sensitive error presence marker")
	}
}
