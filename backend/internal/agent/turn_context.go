package agent

import (
	"context"
	"strings"
)

type turnIDContextKey struct{}

// WithTurnID annotates an agent run with the durable chat actor turn identity.
// The ReAct trace ID is intentionally separate and must not be used to isolate
// delayed runtime events across chat turns.
func WithTurnID(ctx context.Context, turnID string) context.Context {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnIDContextKey{}, turnID)
}

// TurnIDFromContext returns the durable chat actor turn identity, when present.
func TurnIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	turnID, _ := ctx.Value(turnIDContextKey{}).(string)
	return strings.TrimSpace(turnID)
}

func runtimeEventPayloadWithTurnID(ctx context.Context, payload map[string]interface{}) map[string]interface{} {
	turnID := TurnIDFromContext(ctx)
	if turnID == "" {
		return payload
	}
	if payload == nil {
		payload = make(map[string]interface{})
	}
	payload["turn_id"] = turnID
	return payload
}
