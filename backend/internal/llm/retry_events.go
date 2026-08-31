package llm

import "context"

// RetryEvent captures a structured retry lifecycle event emitted by LLM runtime components.
type RetryEvent struct {
	Source            string `json:"source,omitempty"`
	Provider          string `json:"provider,omitempty"`
	Protocol          string `json:"protocol,omitempty"`
	Model             string `json:"model,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	MaxAttempts       int    `json:"max_attempts,omitempty"`
	Error             string `json:"error,omitempty"`
	RetryReason       string `json:"retry_reason,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	RetryDelayMS      int64  `json:"retry_delay_ms,omitempty"`
	LogicalTurnID     string `json:"logical_turn_id,omitempty"`
	LLMRequestID      string `json:"llm_request_id,omitempty"`
	RetryAttemptID    string `json:"retry_attempt_id,omitempty"`
	ProviderRequestID string `json:"provider_request_id,omitempty"`
	StreamID          string `json:"stream_id,omitempty"`
	// PartialOutput marks a retry issued after the previous streaming attempt
	// already emitted user-visible content. The replay regenerates the full
	// response; consumers may use the flag to annotate or clear partial text.
	PartialOutput bool `json:"partial_output,omitempty"`
}

// RetryEventReporter consumes structured retry events emitted by runtime LLM providers.
type RetryEventReporter func(RetryEvent)

type retryEventReporterContextKey struct{}

// WithRetryEventReporter attaches a retry event reporter to the context.
func WithRetryEventReporter(ctx context.Context, reporter RetryEventReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, _ := ctx.Value(retryEventReporterContextKey{}).(RetryEventReporter); existing != nil {
		reporter = composeRetryEventReporter(existing, reporter)
	}
	return context.WithValue(ctx, retryEventReporterContextKey{}, reporter)
}

func composeRetryEventReporter(left RetryEventReporter, right RetryEventReporter) RetryEventReporter {
	switch {
	case left == nil:
		return right
	case right == nil:
		return left
	default:
		return func(event RetryEvent) {
			left(event)
			right(event)
		}
	}
}

func reportRetryEvent(ctx context.Context, event RetryEvent) {
	if ctx == nil {
		return
	}
	if state, ok := ctx.Value(httpDebugRetryAttemptContextKey{}).(httpDebugRetryAttemptState); ok {
		if event.Attempt <= 0 {
			event.Attempt = state.Attempt
		}
		if event.MaxAttempts <= 0 {
			event.MaxAttempts = state.MaxAttempts
		}
		if event.LogicalTurnID == "" {
			event.LogicalTurnID = state.LogicalTurnID
		}
		if event.LLMRequestID == "" {
			event.LLMRequestID = state.LLMRequestID
		}
		if event.RetryAttemptID == "" {
			event.RetryAttemptID = state.RetryAttemptID
		}
		if event.ProviderRequestID == "" {
			event.ProviderRequestID = state.ProviderRequestID
		}
		if event.StreamID == "" {
			event.StreamID = state.StreamID
		}
	}
	reporter, _ := ctx.Value(retryEventReporterContextKey{}).(RetryEventReporter)
	if reporter == nil {
		return
	}
	reporter(event)
}
