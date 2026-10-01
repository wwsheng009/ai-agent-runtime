package toolctx

import (
	"context"
	"testing"
)

func TestToolCallAndTurnIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	if ToolCallID(ctx) != "" || TurnID(ctx) != "" {
		t.Fatal("unbound context must yield empty ids")
	}
	ctx = WithToolCallID(ctx, " call-1 ")
	ctx = WithTurnID(ctx, " turn-1 ")
	if got := ToolCallID(ctx); got != "call-1" {
		t.Fatalf("ToolCallID = %q, want call-1", got)
	}
	if got := TurnID(ctx); got != "turn-1" {
		t.Fatalf("TurnID = %q, want turn-1", got)
	}
	if ToolCallID(nil) != "" || TurnID(nil) != "" {
		t.Fatal("nil context must yield empty ids")
	}
}
