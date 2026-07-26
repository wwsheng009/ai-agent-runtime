package toolprotocol

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestProgressNormalizeAndPayload(t *testing.T) {
	p := Progress{
		ToolID:    "  shell  ",
		CallID:    "  call-1 ",
		SessionID: " sess ",
		TraceID:   " tr ",
		Message:   " running ",
		Percent:   150,
		Metadata:  map[string]interface{}{"phase": "exec"},
	}.Normalize()
	if p.ToolID != "shell" || p.CallID != "call-1" {
		t.Fatalf("ids not normalized: %+v", p)
	}
	if p.Kind != NotificationProgress {
		t.Fatalf("kind=%q", p.Kind)
	}
	if p.Percent != 100 {
		t.Fatalf("percent=%v", p.Percent)
	}
	payload := p.Payload()
	if payload["tool_call_id"] != "call-1" {
		t.Fatalf("payload=%v", payload)
	}
	if payload["phase"] != "exec" {
		t.Fatalf("metadata not merged: %v", payload)
	}
	if payload["percent"].(float64) != 100 {
		t.Fatalf("percent payload=%v", payload["percent"])
	}
}

func TestReporterContext(t *testing.T) {
	var mu sync.Mutex
	var got []Progress
	reporter := ReporterFunc(func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
	})
	ctx := WithReporter(context.Background(), reporter)
	Report(ctx, Progress{
		ToolID:  "download",
		CallID:  "c1",
		Message: "50%",
		Percent: 50,
	})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("got %d reports", len(got))
	}
	if got[0].Percent != 50 || got[0].ToolID != "download" {
		t.Fatalf("got=%+v", got[0])
	}
	// nop path
	Report(context.Background(), Progress{ToolID: "x"})
}

func TestProgressTimestampInPayload(t *testing.T) {
	ts := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	payload := Progress{
		ToolID:    "shell",
		CallID:    "c",
		Timestamp: ts,
		Percent:   10,
	}.Payload()
	if payload["timestamp"] != ts.Format(time.RFC3339Nano) {
		t.Fatalf("timestamp=%v", payload["timestamp"])
	}
}
