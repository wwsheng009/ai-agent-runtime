package ui

import (
	"testing"
	"time"
)

func TestPasteBurstFlushesAtDeadline(t *testing.T) {
	start := time.Unix(1, 0)
	var burst PasteBurst

	if decision := burst.OnPlainChar('a', start); decision.Kind != CharDecisionRetainFirstChar {
		t.Fatalf("expected first character to be retained, got %#v", decision)
	}
	secondAt := start.Add(time.Millisecond)
	if decision := burst.OnPlainChar('b', secondAt); decision.Kind != CharDecisionBeginBufferFromPending {
		t.Fatalf("expected second character to start the buffer, got %#v", decision)
	}
	burst.AppendCharToBuffer('b', secondAt)

	result := burst.FlushIfDue(secondAt.Add(pasteBurstActiveIdleTimeout))
	if result.Kind != FlushResultPaste || result.Text != "ab" {
		t.Fatalf("expected buffered paste to flush exactly at its deadline, got %#v", result)
	}
}
