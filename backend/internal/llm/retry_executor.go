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

	result.Decision = policy.decisionForError(err)
	result.MaxAttempts = policy.maxAttemptsForDecision(result.Decision)
	if !result.Decision.Retryable {
		return result, nil
	}
	if attempt >= result.MaxAttempts {
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
		RetryDelayMS: result.Delay.Milliseconds(),
	})
	reportRetryEvent(ctx, RetryEvent{
		Source:       meta.Source,
		Provider:     meta.Provider,
		Protocol:     meta.Protocol,
		Model:        meta.Model,
		Attempt:      attempt,
		MaxAttempts:  result.MaxAttempts,
		Error:        err.Error(),
		RetryReason:  result.Decision.Reason,
		RetryDelayMS: result.Delay.Milliseconds(),
	})

	if waitErr := waitRetryDelay(ctx, result.Delay); waitErr != nil {
		return result, waitErr
	}

	result.Retry = true
	return result, nil
}
