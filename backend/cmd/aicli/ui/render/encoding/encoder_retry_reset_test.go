package encoding

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func retryResetEvent() runtimeevents.Event {
	return event(runtimeevents.EventAssistantStreamReset, map[string]interface{}{
		"turn_id":         "turn-1",
		"logical_turn_id": "trace-1",
		"stream_id":       "stream-1",
		"llm_request_id":  "llm-req-1",
		"step":            1,
		"attempt":         2,
		"max_attempts":    10,
	})
}

func reasoningDelta(text string, seq uint64) runtimeevents.Event {
	return event(runtimechat.EventAssistantReasoning, map[string]interface{}{
		"turn_id":   "turn-1",
		"stream_id": "stream-1",
		"sequence":  seq,
		"mode":      "append",
		"text":      text,
	})
}

// A retry boundary must remove the previous attempt's partial assistant and
// reasoning cells even though the next attempt reuses the same request/stream
// identity and continues with a higher sequence number.
func TestAssistantStreamReset_DiscardsPartialAttemptAndRebasesSequence(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(reasoningDelta("old reasoning", 1))
	e.Encode(assistantDelta("old answer", 1))
	e.Encode(assistantDelta(" + old tail", 2))

	reset := e.Encode(retryResetEvent())
	if reset == nil || len(reset.Changes) != 2 {
		t.Fatalf("reset changes = %+v, want assistant + reasoning removals", reset)
	}
	for _, change := range reset.Changes {
		if change.Op != OpRemove {
			t.Fatalf("reset change = %+v, want OpRemove", change)
		}
	}
	if items := e.Snapshot().Items; len(items) != 0 {
		t.Fatalf("items after reset = %+v, want empty previous attempt", items)
	}

	// The next attempt starts at a much higher sequence. Without the reset
	// baseline this delta would sit in the out-of-order buffer forever.
	e.Encode(reasoningDelta("new reasoning", 50))
	e.Encode(assistantDelta("new answer", 50))
	e.Encode(assistantDelta(" complete", 51))

	items := e.Snapshot().Items
	if len(items) != 2 {
		t.Fatalf("items after retry = %+v, want reasoning + assistant", items)
	}
	if items[0].Kind != KindReasoning || items[0].Head != "new reasoning" {
		t.Fatalf("reasoning = %+v, want only new attempt reasoning", items[0])
	}
	if items[1].Kind != KindAssistant || items[1].Head != "new answer complete" {
		t.Fatalf("assistant = %+v, want only new attempt answer", items[1])
	}
}

// A late reset must not erase an already committed final answer.
func TestAssistantStreamReset_IgnoresCompletedAssistant(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(assistantDelta("final answer", 1))
	e.Encode(llmFinishedWithSnapshot("final answer"))

	e.Encode(retryResetEvent())
	items := e.Snapshot().Items
	if len(items) != 1 || items[0].Kind != KindAssistant || items[0].Head != "final answer" {
		t.Fatalf("items after late reset = %+v, want committed final answer preserved", items)
	}
}
