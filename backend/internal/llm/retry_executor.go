package llm

import (
	"context"
	"time"
)

type retryExecutionMeta struct {
	Source   string
	Provider string
	Protocol string
	Model    string
	// PartialOutput flags retries issued after a prior streaming attempt
	// already emitted user-visible content (partial-output replay).
	PartialOutput bool
}

type retryExecutionResult struct {
	Decision    retryDecision
	MaxAttempts int
	Delay       time.Duration
	Retry       bool
}

func prepareRetry(ctx context.Context, policy retryPolicy, startedAt time.Time, attempt int, err error, meta retryExecutionMeta) (retryExecutionResult, error) {
	result := retryExecutionResult{}
	if err == nil {
		return result, nil
	}
	if ctx != nil && ctx.Err() != nil {
		return result, ctx.Err()
	}

	// An inner provider may have ended an unlimited retry burst at a fast-fail
	// guard. Reclassify only an explicitly marked handoff for this finite outer
	// execution loop so a fresh request can still be attempted. Final failure
	// diagnostics intentionally remain terminal via decisionForError.
	result.Decision = policy.decisionForRetry(err)
	result.MaxAttempts = policy.maxAttemptsForDecision(result.Decision)
	if !result.Decision.Retryable {
		return result, nil
	}
	if result.MaxAttempts > 0 && attempt >= result.MaxAttempts {
		return result, nil
	}

	result.Delay = policy.delayForDecision(attempt, result.Decision)
	if !policy.canRetryAfter(startedAt, time.Now(), result.Delay) {
		return result, nil
	}

	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:       meta.Source,
		Phase:        "retry",
		Provider:     meta.Provider,
		Protocol:     meta.Protocol,
		Model:        meta.Model,
		Attempt:      attempt,
		MaxAttempts:  result.MaxAttempts,
		Error:        err.Error(),
		RetryReason:  result.Decision.Reason,
		ErrorCode:    classifyLLMFailureCode(err, result.Decision),
		RetryDelayMS: result.Delay.Milliseconds(),
	})
	retryEvent := RetryEvent{
		Source:        meta.Source,
		Provider:      meta.Provider,
		Protocol:      meta.Protocol,
		Model:         meta.Model,
		Attempt:       attempt,
		MaxAttempts:   result.MaxAttempts,
		Error:         err.Error(),
		RetryReason:   result.Decision.Reason,
		ErrorCode:     classifyLLMFailureCode(err, result.Decision),
		RetryDelayMS:  result.Delay.Milliseconds(),
		PartialOutput: meta.PartialOutput,
	}
	if state, ok := retryAttemptStateFromContext(ctx); ok {
		retryEvent.LogicalTurnID = state.LogicalTurnID
		retryEvent.LLMRequestID = state.LLMRequestID
		retryEvent.RetryAttemptID = state.RetryAttemptID
		retryEvent.ProviderRequestID = state.ProviderRequestID
		retryEvent.StreamID = state.StreamID
	}
	reportRetryEvent(ctx, retryEvent)

	if waitErr := waitRetryDelay(ctx, result.Delay); waitErr != nil {
		return result, waitErr
	}

	result.Retry = true
	return result, nil
}

func retryAttemptStateFromContext(ctx context.Context) (httpDebugRetryAttemptState, bool) {
	if ctx == nil {
		return httpDebugRetryAttemptState{}, false
	}
	state, ok := ctx.Value(httpDebugRetryAttemptContextKey{}).(httpDebugRetryAttemptState)
	return state, ok
}
