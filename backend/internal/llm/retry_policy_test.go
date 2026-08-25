package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRetryAfterFromMessage_ParsesFractionalSeconds(t *testing.T) {
	delay, ok := parseRetryAfterFromMessage("Rate limit reached. Please try again in 11.054s.")
	require.True(t, ok)
	assert.Equal(t, 11054*time.Millisecond, delay)
}

func TestClassifyRetryableLLMError_UsesRetryAfterHint(t *testing.T) {
	decision := classifyRetryableLLMError(fmt.Errorf("HTTP 429: rate limit reached, please try again in 1.5s"))
	assert.True(t, decision.Retryable)
	assert.Equal(t, 1500*time.Millisecond, decision.Delay)
	assert.Equal(t, "rate_limit", decision.Reason)
}

func TestClassifyRetryableLLMError_TreatsUnclassifiedHTTP429AsRetryable(t *testing.T) {
	decision := classifyRetryableLLMError(fmt.Errorf("HTTP 429: {\"error\":{\"code\":\"provider_resource_state\",\"message\":\"request rejected\"}}"))
	assert.True(t, decision.Retryable)
	assert.Equal(t, "http_429", decision.Reason)

	decision = classifyRetryableLLMError(fmt.Errorf("HTTP 429: {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"rate limit reached\"}}"))
	assert.True(t, decision.Retryable)
	assert.Equal(t, "rate_limit", decision.Reason)
}

func TestClassifyFailureCodeUsesVendorNeutralTaxonomy(t *testing.T) {
	assert.Equal(t, "STREAM_INTERRUPTED", ClassifyFailureCode(fmt.Errorf("stream_interrupted: connection closed")))
	assert.Equal(t, "UPSTREAM_UNAVAILABLE", ClassifyFailureCode(newProviderHTTPError(503, "temporarily unavailable", nil)))
	assert.Equal(t, "CONTEXT_BUDGET_EXCEEDED", ClassifyFailureCode(fmt.Errorf("input exceeds the context window")))
	assert.Equal(t, "PERMISSION_DENIED", ClassifyFailureCode(newProviderHTTPError(403, "forbidden", nil)))
	assert.Equal(t, "UPSTREAM_QUOTA_EXHAUSTED", ClassifyFailureCode(newProviderHTTPError(403, "insufficient_user_quota", nil)))
	assert.Equal(t, "UPSTREAM_RATE_LIMITED", ClassifyFailureCode(newProviderHTTPError(429, "rate limit exceeded", nil)))
}

func TestDiagnoseFailureRetainsCauseCodeWhenRetriesExhausted(t *testing.T) {
	// 模拟上游挂死：请求已发出，但等不到响应头。
	cause := &url.Error{
		Op:  "Post",
		URL: "https://ttai.online/v1/responses",
		Err: fmt.Errorf("http2: timeout awaiting response headers"),
	}
	exhausted := markRetryExhausted("streaming aggregate call failed after retries", 2, cause)
	diag := DiagnoseFailure(exhausted)
	assert.Equal(t, "UPSTREAM_UNAVAILABLE", diag.ErrorCode)
	assert.False(t, diag.Retryable)
	assert.Contains(t, diag.NextAction, "Automatic retries are exhausted")
	assert.NotContains(t, diag.NextAction, "Inspect the provider error")

	// quota 类错误在重试耗尽后也应保留原始分类。
	quotaExhausted := markRetryExhausted("all retry attempts failed", 2, newProviderHTTPError(403, "insufficient_user_quota", nil))
	quotaDiag := DiagnoseFailure(quotaExhausted)
	assert.Equal(t, "UPSTREAM_QUOTA_EXHAUSTED", quotaDiag.ErrorCode)
	assert.False(t, quotaDiag.Retryable)
}

func TestDiagnoseFailureSeparatesQuotaAndTransientFailures(t *testing.T) {
	quota := DiagnoseFailure(newProviderHTTPError(403, "insufficient_user_quota", nil))
	assert.Equal(t, "UPSTREAM_QUOTA_EXHAUSTED", quota.ErrorCode)
	assert.False(t, quota.Retryable)
	assert.Contains(t, quota.NextAction, "do not retry unchanged")
	plainQuota := DiagnoseFailure(fmt.Errorf("HTTP 403: insufficient_user_quota"))
	assert.Equal(t, "UPSTREAM_QUOTA_EXHAUSTED", plainQuota.ErrorCode)
	assert.False(t, plainQuota.Retryable)

	transient := DiagnoseFailure(newProviderHTTPError(503, "temporarily unavailable", nil))
	assert.Equal(t, "UPSTREAM_UNAVAILABLE", transient.ErrorCode)
	assert.True(t, transient.Retryable)
	assert.Contains(t, transient.NextAction, "bounded backoff")
}

func TestClassifyRetryableLLMErrorWithRules_ConfiguredStopUsesStructuredCode(t *testing.T) {
	decision := classifyRetryableLLMErrorWithRules(retryPolicyTestError{
		message: "provider rejected request",
		code:    "account_resource_exhausted",
	}, []RetryRule{
		{
			Name:    "resource_exhausted_stop",
			Enabled: true,
			Action:  RetryRuleActionStop,
			ErrorCode: RetryErrorCodeMatcher{
				Codes: []string{"account_resource_exhausted"},
			},
		},
	})

	assert.False(t, decision.Retryable)
	assert.Equal(t, "resource_exhausted_stop", decision.Reason)
}

func TestClassifyRetryableLLMError_DoesNotRetryContextOverflowBehind5xx(t *testing.T) {
	tests := []string{
		"HTTP 502: Your input exceeds the context window of this model.",
		"HTTP 502: input exceeds the context length limit",
		"HTTP 503: input is too long for the context window",
	}
	for _, message := range tests {
		t.Run(message, func(t *testing.T) {
			decision := classifyRetryableLLMError(fmt.Errorf("%s", message))
			assert.False(t, decision.Retryable)
			assert.Equal(t, "non_retryable_response", decision.Reason)
		})
	}
}

func TestIsContextWindowErrorSupportsAgentLevelRecovery(t *testing.T) {
	for _, message := range []string{
		"context_length_exceeded",
		"HTTP 502: Your input exceeds the context window of this model",
		"maximum context length is 128000 tokens",
		"prompt is too long",
	} {
		require.True(t, IsContextWindowError(fmt.Errorf("%s", message)), message)
	}
	require.False(t, IsContextWindowError(fmt.Errorf("HTTP 502: upstream unavailable")))
}

func TestParseMaxTokensLimitError_AnthropicClaudeFableStyle(t *testing.T) {
	err := fmt.Errorf(`HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 131072 > 128000, which is the maximum allowed number of output tokens for claude-fable-5"}}`)
	limit, ok := ParseMaxTokensLimitError(err)
	require.True(t, ok)
	assert.Equal(t, 128000, limit)
	assert.True(t, IsMaxTokensLimitError(err))
}

func TestApplyMaxTokensLimitRecovery_LowersBudgetOnce(t *testing.T) {
	current := 131072
	err := fmt.Errorf("max_tokens: 131072 > 128000, which is the maximum allowed number of output tokens for claude-fable-5")
	require.True(t, applyMaxTokensLimitRecovery(&current, err))
	assert.Equal(t, 128000, current)
	// Already at or below the limit — do not recover again.
	require.False(t, applyMaxTokensLimitRecovery(&current, err))
	assert.Equal(t, 128000, current)
}

func TestApplyMaxTokensLimitRecovery_IgnoresUnrelatedErrors(t *testing.T) {
	current := 131072
	require.False(t, applyMaxTokensLimitRecovery(&current, fmt.Errorf("HTTP 429: rate limit exceeded")))
	assert.Equal(t, 131072, current)
}

func TestParseMaxTokensLimitError_AlternateForms(t *testing.T) {
	for _, message := range []string{
		"max_tokens must be <= 64000",
		"max_output_tokens at most 8192",
		"maximum allowed number of output tokens for claude-sonnet-4-6 is 128000",
	} {
		limit, ok := ParseMaxTokensLimitError(fmt.Errorf("%s", message))
		require.True(t, ok, message)
		require.Greater(t, limit, 0, message)
	}
	_, ok := ParseMaxTokensLimitError(fmt.Errorf("invalid_request_error: unknown parameter"))
	require.False(t, ok)
}

func TestParseRetryAfterHeaderValue_ParsesSecondsAndHTTPDate(t *testing.T) {
	now := time.Date(2026, time.April, 26, 10, 0, 0, 0, time.UTC)

	delay, ok := parseRetryAfterHeaderValue("0.25", now)
	require.True(t, ok)
	assert.Equal(t, 250*time.Millisecond, delay)

	delay, ok = parseRetryAfterHeaderValue(now.Add(2*time.Second).Format(http.TimeFormat), now)
	require.True(t, ok)
	assert.Equal(t, 2*time.Second, delay)
}

func TestDecisionDelayFromServerHint_PrefersRetryAfterHeaderHint(t *testing.T) {
	err := newProviderHTTPError(http.StatusTooManyRequests, `{"error":{"message":"rate limit reached"}}`, http.Header{
		"Retry-After": []string{"0.05"},
	})
	assert.Equal(t, 50*time.Millisecond, decisionDelayFromServerHint(err))
}

func TestDecisionDelayFromServerHint_UsesHTTPBodyRetryAfterHint(t *testing.T) {
	err := newProviderHTTPError(http.StatusTooManyRequests, `{"error":{"retry_after_ms":125}}`, nil)
	assert.Equal(t, 125*time.Millisecond, decisionDelayFromServerHint(err))
}

func TestRetryAfterDelayFromHeader_ParsesRetryAfterMillisecondsHeader(t *testing.T) {
	delay, ok := retryAfterDelayFromHeader(http.Header{
		"Retry-After-Ms": []string{"125"},
	}, time.Time{})
	require.True(t, ok)
	assert.Equal(t, 125*time.Millisecond, delay)
}

func TestRetryAfterDelayFromBody_ParsesNestedRetryAfterFields(t *testing.T) {
	delay, ok := retryAfterDelayFromBody(`{"error":{"retry_after_ms":125}}`)
	require.True(t, ok)
	assert.Equal(t, 125*time.Millisecond, delay)

	delay, ok = retryAfterDelayFromBody(`{"error":{"details":{"retry_after":"0.25"}}}`)
	require.True(t, ok)
	assert.Equal(t, 250*time.Millisecond, delay)
}

func TestNewProviderRetryPolicy_UsesConfiguredTuning(t *testing.T) {
	// Randomization: -1 显式关闭抖动（默认已改为 codex 式 ±10%），
	// 精确断言保持可读。
	policy := newProviderRetryPolicy(3, 0, RetryTuning{
		BaseDelay:     400 * time.Millisecond,
		MaxDelay:      3 * time.Second,
		Multiplier:    1.5,
		Randomization: -1,
	}, nil)

	assert.Equal(t, 3, policy.MaxAttempts)
	assert.Equal(t, 400*time.Millisecond, policy.BaseDelay)
	assert.Equal(t, 3*time.Second, policy.MaxDelay)
	assert.Equal(t, 1.5, policy.Multiplier)
	assert.Equal(t, 400*time.Millisecond, policy.delayForDecision(1, policy.decisionForError(fmt.Errorf("HTTP 500"))))
	assert.Equal(t, 600*time.Millisecond, policy.delayForDecision(2, policy.decisionForError(fmt.Errorf("HTTP 500"))))
}

func TestRetryPolicy_UsesConfiguredStaircaseAndRepeatsFinalDelay(t *testing.T) {
	schedule := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute}
	policy := newProviderRetryPolicy(-1, 0, RetryTuning{
		Schedule:      schedule,
		MaxDelay:      6 * time.Minute,
		Randomization: -1, // 关闭默认抖动，精确断言 schedule 值
	}, nil)
	decision := policy.decisionForError(fmt.Errorf("HTTP 503: upstream unavailable"))

	require.Zero(t, policy.MaxAttempts)
	require.Zero(t, policy.DefaultMaxAttempts)
	for index, expected := range schedule {
		require.Equal(t, expected, policy.delayForDecision(index+1, decision))
	}
	require.Equal(t, 5*time.Minute, policy.delayForDecision(6, decision))
	require.True(t, retryAttemptAllowed(policy.MaxAttempts, 1_000_000))
}

func TestRetryPolicy_ServerHintCanExtendButNotShortenSchedule(t *testing.T) {
	policy := newProviderRetryPolicy(-1, 0, RetryTuning{
		Schedule:      []time.Duration{30 * time.Second, time.Minute},
		MaxDelay:      2 * time.Minute,
		Randomization: -1, // 关闭默认抖动，精确断言 schedule 值
	}, nil)

	require.Equal(t, 30*time.Second, policy.delayForDecision(1, retryDecision{Delay: time.Second}))
	require.Equal(t, 90*time.Second, policy.delayForDecision(1, retryDecision{Delay: 90 * time.Second}))
}

func TestRetryTuning_UnsetRandomizationDefaultsToCodexStyleJitter(t *testing.T) {
	// 对齐 codex-rs backoff()（retry.rs）的固定 ±10% jitter：未配置时默认 0.1。
	assert.Equal(t, 0.1, RetryTuning{}.normalized().Randomization)
	assert.Equal(t, 0.1, RetryTuning{BaseDelay: 400 * time.Millisecond}.normalized().Randomization)
	// 显式配置被尊重。
	assert.Equal(t, 0.25, RetryTuning{Randomization: 0.25}.normalized().Randomization)
	// 显式负值是关闭开关。
	assert.Zero(t, RetryTuning{Randomization: -1}.normalized().Randomization)
	// 超过 1 被钳制。
	assert.Equal(t, 1.0, RetryTuning{Randomization: 1.5}.normalized().Randomization)
}

func TestRetryPolicy_DefaultJitterBoundsBackoffWithinTenPercent(t *testing.T) {
	policy := newProviderRetryPolicy(3, 0, RetryTuning{
		BaseDelay:  400 * time.Millisecond,
		MaxDelay:   10 * time.Second,
		Multiplier: 2.0,
	}, nil)
	decision := policy.decisionForError(fmt.Errorf("HTTP 500: upstream unavailable"))

	original := retryPolicyRandomFloat64
	defer func() { retryPolicyRandomFloat64 = original }()

	// factor = 1 - 0.1 = 0.9 → 360ms。
	retryPolicyRandomFloat64 = func() float64 { return 0 }
	assert.Equal(t, 360*time.Millisecond, policy.delayForDecision(1, decision))

	// factor = 1 + 0.1 = 1.1 → 440ms。
	retryPolicyRandomFloat64 = func() float64 { return 1 }
	assert.Equal(t, 440*time.Millisecond, policy.delayForDecision(1, decision))
}

func TestRetryPolicy_NegativeRandomizationDisablesJitter(t *testing.T) {
	policy := newProviderRetryPolicy(3, 0, RetryTuning{
		BaseDelay:     400 * time.Millisecond,
		MaxDelay:      10 * time.Second,
		Multiplier:    2.0,
		Randomization: -1,
	}, nil)
	decision := policy.decisionForError(fmt.Errorf("HTTP 500: upstream unavailable"))

	original := retryPolicyRandomFloat64
	defer func() { retryPolicyRandomFloat64 = original }()
	// 若抖动仍启用，rand=1 会把 400ms 放大到 440ms。
	retryPolicyRandomFloat64 = func() float64 { return 1 }
	assert.Equal(t, 400*time.Millisecond, policy.delayForDecision(1, decision))
}

func TestRetryPolicy_KeepsRuleAttemptLimitsErrorSpecific(t *testing.T) {
	rules := []RetryRule{
		{
			Name:       "rate_limit_retry",
			Enabled:    true,
			MaxRetries: 10,
			Keyword: RetryKeywordMatcher{
				Values: []string{"rate limit"},
			},
		},
		{
			Name:       "http_5xx_retry",
			Enabled:    true,
			MaxRetries: 3,
			StatusCode: RetryStatusCodeMatcher{
				Range: "500-504",
			},
		},
	}

	policy := newProviderRetryPolicy(3, 0, RetryTuning{}, rules)
	assert.Equal(t, 10, policy.MaxAttempts)
	assert.Equal(t, 3, policy.DefaultMaxAttempts)
	assert.Equal(t, 3, policy.initialMaxAttempts())

	transportDecision := policy.decisionForError(fmt.Errorf("Post https://example.test: net/http: TLS handshake timeout"))
	require.True(t, transportDecision.Retryable)
	assert.Equal(t, "transport", transportDecision.Reason)
	assert.Equal(t, 3, policy.maxAttemptsForDecision(transportDecision))

	rateLimitDecision := policy.decisionForError(fmt.Errorf("HTTP 429: rate limit reached"))
	require.True(t, rateLimitDecision.Retryable)
	assert.Equal(t, "rate_limit_retry", rateLimitDecision.Reason)
	assert.Equal(t, 10, policy.maxAttemptsForDecision(rateLimitDecision))

	serverDecision := policy.decisionForError(fmt.Errorf("HTTP 503: upstream unavailable"))
	require.True(t, serverDecision.Retryable)
	assert.Equal(t, "http_5xx_retry", serverDecision.Reason)
	assert.Equal(t, 3, policy.maxAttemptsForDecision(serverDecision))
}

func TestApplyRequestRetryPolicy_DisablesConfiguredAndRuleRetries(t *testing.T) {
	policy := newProviderRetryPolicy(3, 0, RetryTuning{}, []RetryRule{{
		Name:       "rate_limit_retry",
		Enabled:    true,
		MaxRetries: 10,
		Keyword: RetryKeywordMatcher{
			Values: []string{"rate limit"},
		},
	}})

	policy = applyRequestRetryPolicy(policy, map[string]interface{}{MetadataKeyDisableRetries: true})
	require.Equal(t, 1, policy.MaxAttempts)
	require.Equal(t, 1, policy.DefaultMaxAttempts)
	require.Empty(t, policy.Rules)

	result, err := prepareRetry(context.Background(), policy, time.Now(), 1, fmt.Errorf("HTTP 429: rate limit reached"), retryExecutionMeta{})
	require.NoError(t, err)
	require.False(t, result.Retry)
	require.Equal(t, 1, result.MaxAttempts)
}

func TestRetryPolicy_DefaultAttemptLimitWithoutRulesRemainsUnchanged(t *testing.T) {
	providerPolicy := newProviderRetryPolicy(3, 0, RetryTuning{}, nil)
	assert.Equal(t, 3, providerPolicy.MaxAttempts)
	assert.Equal(t, 3, providerPolicy.DefaultMaxAttempts)
	assert.Equal(t, 3, providerPolicy.maxAttemptsForDecision(providerPolicy.decisionForError(fmt.Errorf("temporary failure"))))

	runtimePolicy := newRuntimeRetryPolicy(2, 0, RetryTuning{}, nil)
	assert.Equal(t, 3, runtimePolicy.MaxAttempts)
	assert.Equal(t, 3, runtimePolicy.DefaultMaxAttempts)
	assert.Equal(t, 3, runtimePolicy.maxAttemptsForDecision(runtimePolicy.decisionForError(fmt.Errorf("temporary failure"))))
}

func TestPrepareRetry_UsesTheLimitForTheCurrentErrorClass(t *testing.T) {
	policy := newProviderRetryPolicy(3, 0, RetryTuning{}, []RetryRule{
		{
			Name:       "rate_limit_retry",
			Enabled:    true,
			MaxRetries: 10,
			Keyword: RetryKeywordMatcher{
				Values: []string{"rate limit"},
			},
		},
	})

	transportResult, err := prepareRetry(context.Background(), policy, time.Now(), 3, fmt.Errorf("TLS handshake timeout"), retryExecutionMeta{})
	require.NoError(t, err)
	assert.Equal(t, 3, transportResult.MaxAttempts)
	assert.False(t, transportResult.Retry)

	rateLimitResult, err := prepareRetry(context.Background(), policy, time.Now(), 3, fmt.Errorf("HTTP 429: rate limit reached"), retryExecutionMeta{})
	require.NoError(t, err)
	assert.Equal(t, 10, rateLimitResult.MaxAttempts)
	assert.True(t, rateLimitResult.Retry)
}

func TestClassifyRetryableLLMErrorWithRules_MatchesKeywordRule(t *testing.T) {
	decision := classifyRetryableLLMErrorWithRules(fmt.Errorf("stream closed before response.completed"), []RetryRule{
		{
			Name:              "codex_request_timeout_retry",
			Enabled:           true,
			MaxRetries:        4,
			RetryDelay:        1200 * time.Millisecond,
			BackoffMultiplier: 1.7,
			Keyword: RetryKeywordMatcher{
				Values: []string{"stream closed before response.completed"},
			},
		},
	})

	assert.True(t, decision.Retryable)
	assert.Equal(t, "codex_request_timeout_retry", decision.Reason)
	assert.Equal(t, 4, decision.MaxAttempts)
	assert.Equal(t, 1200*time.Millisecond, decision.BaseDelay)
	assert.Equal(t, 1.7, decision.Multiplier)
}

func TestClassifyRetryableLLMErrorWithRules_MatchesStatusCodeRule(t *testing.T) {
	decision := classifyRetryableLLMErrorWithRules(fmt.Errorf("HTTP 503: upstream temporarily unavailable"), []RetryRule{
		{
			Name:       "http_5xx_retry",
			Enabled:    true,
			MaxRetries: 3,
			StatusCode: RetryStatusCodeMatcher{
				Range: "500-504",
			},
		},
	})

	assert.True(t, decision.Retryable)
	assert.Equal(t, "http_5xx_retry", decision.Reason)
	assert.Equal(t, 3, decision.MaxAttempts)
}

func TestClassifyRetryableLLMErrorWithRules_MatchesErrorCodeRule(t *testing.T) {
	decision := classifyRetryableLLMErrorWithRules(retryPolicyTestError{message: "upstream requested retry", code: "rate_limit_exceeded"}, []RetryRule{
		{
			Name:       "rate_limit_retry",
			Enabled:    true,
			MaxRetries: 10,
			ErrorCode: RetryErrorCodeMatcher{
				Codes: []string{"rate_limit_exceeded"},
			},
		},
	})

	assert.True(t, decision.Retryable)
	assert.Equal(t, "rate_limit_retry", decision.Reason)
	assert.Equal(t, 10, decision.MaxAttempts)
}

func TestValidateStreamingAggregateResponse_ClassifiesReasoningOnlyContentInspectionAndEmptyReply(t *testing.T) {
	reasoningOnlyErr := validateStreamingAggregateResponse("openai", []byte(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"先确认上下文。"},"finish_reason":"stop"}]}`,
		"data: [DONE]",
	}, "\n\n")), map[string]interface{}{
		"reasoning_content": "先确认上下文。",
	})
	require.Error(t, reasoningOnlyErr)
	assert.Contains(t, reasoningOnlyErr.Error(), "reasoning_only_empty_reply")
	assert.True(t, classifyRetryableLLMError(reasoningOnlyErr).Retryable)
	assert.Equal(t, "reasoning_only_empty_reply", classifyRetryableLLMError(reasoningOnlyErr).Reason)

	codexReasoningOnlyErr := validateStreamingAggregateResponse("codex", []byte(strings.Join([]string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","stop_reason":"stop"}}`,
	}, "\n\n")), map[string]interface{}{
		"reasoning": "先确认上下文。",
	})
	require.Error(t, codexReasoningOnlyErr)
	assert.Contains(t, codexReasoningOnlyErr.Error(), "reasoning_only_empty_reply")
	assert.True(t, classifyRetryableLLMError(codexReasoningOnlyErr).Retryable)
	assert.Equal(t, "reasoning_only_empty_reply", classifyRetryableLLMError(codexReasoningOnlyErr).Reason)

	codexImageGenerationErr := validateStreamingAggregateResponse("codex", []byte(strings.Join([]string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_img_1","stop_reason":"stop"}}`,
	}, "\n\n")), map[string]interface{}{
		"response_output_items": []map[string]interface{}{
			{
				"type":           "image_generation_call",
				"id":             "img:1",
				"status":         "completed",
				"revised_prompt": "a tiny robot",
			},
		},
	})
	require.NoError(t, codexImageGenerationErr)

	streamInterruptedErr := validateStreamingAggregateResponse("openai", []byte(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{}}]}`,
	}, "\n\n")), map[string]interface{}{})
	require.Error(t, streamInterruptedErr)
	assert.Contains(t, streamInterruptedErr.Error(), "stream_interrupted")
	assert.True(t, classifyRetryableLLMError(streamInterruptedErr).Retryable)
	assert.Equal(t, "stream_interrupted", classifyRetryableLLMError(streamInterruptedErr).Reason)

	nullFinishReasonErr := validateStreamingAggregateResponse("openai", []byte(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`,
		`event: error`,
		`data: {"error":{"message":"Upstream request failed","type":"upstream_error"}}`,
	}, "\n\n")), map[string]interface{}{
		"content": "partial",
	})
	require.Error(t, nullFinishReasonErr)
	assert.Contains(t, nullFinishReasonErr.Error(), "stream_interrupted")
	assert.True(t, classifyRetryableLLMError(nullFinishReasonErr).Retryable)
	assert.Equal(t, "stream_interrupted", classifyRetryableLLMError(nullFinishReasonErr).Reason)

	finishReasonWithoutDoneErr := validateStreamingAggregateResponse("openai", []byte(
		`data: {"choices":[{"index":0,"delta":{"content":"complete"},"finish_reason":"stop"}]}`,
	), map[string]interface{}{
		"content":       "complete",
		"finish_reason": "stop",
	})
	require.NoError(t, finishReasonWithoutDoneErr)

	contentInspectionErr := validateStreamingAggregateResponse("openai", []byte(strings.Join([]string{
		`data: {"error":{"code":"data_inspection_failed","message":"Output data may contain inappropriate content."}}`,
	}, "\n\n")), map[string]interface{}{})
	require.Error(t, contentInspectionErr)
	assert.Contains(t, contentInspectionErr.Error(), "content_inspection_failed")
	assert.False(t, classifyRetryableLLMError(contentInspectionErr).Retryable)
	assert.Equal(t, "content_inspection_failed", classifyRetryableLLMError(contentInspectionErr).Reason)

	emptyReplyErr := validateStreamingAggregateResponse("openai", []byte(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"data: [DONE]",
	}, "\n\n")), map[string]interface{}{})
	require.Error(t, emptyReplyErr)
	assert.Contains(t, emptyReplyErr.Error(), "empty_reply")
	assert.True(t, classifyRetryableLLMError(emptyReplyErr).Retryable)
	assert.Equal(t, "empty_reply", classifyRetryableLLMError(emptyReplyErr).Reason)

	truncatedToolCallErr := validateStreamingAggregateResponse("openai", []byte(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"<tool_call>write<arg_key>file_path</arg_key><arg_value>C:\\temp\\chapter7.md</arg_value><arg_key>content</arg_key><arg_value># 第7章"},"finish_reason":"length"}]}`,
		"data: [DONE]",
	}, "\n\n")), map[string]interface{}{
		"content":       `<tool_call>write<arg_key>file_path</arg_key><arg_value>C:\temp\chapter7.md</arg_value><arg_key>content</arg_key><arg_value># 第7章`,
		"finish_reason": "length",
	})
	require.Error(t, truncatedToolCallErr)
	assert.Contains(t, truncatedToolCallErr.Error(), "truncated_tool_call")
	assert.False(t, classifyRetryableLLMError(truncatedToolCallErr).Retryable)
	assert.Equal(t, "truncated_tool_call", classifyRetryableLLMError(truncatedToolCallErr).Reason)
}

func TestOpenAIStreamBodyHasCompletionRecognizesNamedTerminalEvents(t *testing.T) {
	for _, eventType := range []string{"done", "response.completed", "response.done"} {
		body := "event: " + eventType + "\ndata: {}\n\n"
		assert.True(t, openAIStreamBodyHasCompletion(body), eventType)
	}
	assert.False(t, openAIStreamBodyHasCompletion("event: error\ndata: {\"error\":{}}\n\n"))
}

func TestCodexStreamBodyHasCompletionParsesLifecycleEvents(t *testing.T) {
	for _, eventType := range []string{
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
	} {
		body := "event: " + eventType + "\ndata: {\"type\":\"" + eventType + "\"}\n\n"
		assert.True(t, codexStreamBodyHasCompletion(body), eventType)
	}
	assert.True(t, codexStreamBodyHasCompletion("data: {\"type\":\"response.completed\"}\n\n"))
	assert.False(t, codexStreamBodyHasCompletion("data: {\"type\":\"response.output_text.delta\",\"delta\":\"response.completed\"}\n\n"))
}

func TestValidateAssistantMessageSemanticsRejectsUnsafeToolCallsAndClassifiesFinishReasons(t *testing.T) {
	err := validateAssistantMessageSemantics(map[string]interface{}{
		"finish_reason": "tool_calls",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing_tool_call")
	assert.True(t, classifyRetryableLLMError(err).Retryable)
	assert.Equal(t, "malformed_tool_call", classifyRetryableLLMError(err).Reason)

	err = validateAssistantMessageSemantics(map[string]interface{}{
		"finish_reason": "tool_calls",
		"tool_calls": []map[string]interface{}{{
			"id": "call_1",
			"function": map[string]interface{}{
				"name":      "write",
				"arguments": `{"content":"truncated`,
			},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_tool_arguments")
	// invalid_tool_arguments 不可重试：重放同一请求无法修复非法 JSON 参数，
	// 恢复通道是执行层降级 re-prompt（附 schema 让模型按 schema 重发）。
	assert.False(t, classifyRetryableLLMError(err).Retryable)
	assert.Equal(t, "malformed_tool_call", classifyRetryableLLMError(err).Reason)

	contentFilterErr := validateAssistantMessageSemantics(map[string]interface{}{
		"finish_reason": "content_filter",
	})
	require.Error(t, contentFilterErr)
	assert.False(t, classifyRetryableLLMError(contentFilterErr).Retryable)
	assert.Equal(t, "content_filter", classifyRetryableLLMError(contentFilterErr).Reason)
	assert.Equal(t, "CONTENT_FILTERED", ClassifyFailureCode(contentFilterErr))

	resourceErr := validateAssistantMessageSemantics(map[string]interface{}{
		"finish_reason": "insufficient_system_resource",
	})
	require.Error(t, resourceErr)
	assert.True(t, classifyRetryableLLMError(resourceErr).Retryable)
	assert.Equal(t, "insufficient_system_resource", classifyRetryableLLMError(resourceErr).Reason)
}

type retryPolicyTestError struct {
	message string
	code    string
}

func (e retryPolicyTestError) Error() string {
	return e.message
}

func (e retryPolicyTestError) RetryErrorCode() string {
	return e.code
}

// TestRetryAttemptsCeiling mirrors codex-rs MAX_REQUEST_MAX_RETRIES=100: any
// positive budget above the ceiling is clamped, while unlimited (-1) and
// disabled (0) budgets pass through unchanged.
func TestRetryAttemptsCeiling(t *testing.T) {
	require.Equal(t, -1, clampRetryAttempts(-1))
	require.Equal(t, 0, clampRetryAttempts(0))
	require.Equal(t, 100, clampRetryAttempts(100))
	require.Equal(t, 100, clampRetryAttempts(500))

	policy := newProviderRetryPolicy(500, 4, RetryTuning{}, nil)
	require.Equal(t, 100, policy.MaxAttempts, "business budget must be clamped to 100")
	require.Equal(t, 100, policy.DefaultMaxAttempts)
	require.Equal(t, 4, policy.MaxTransportAttempts)

	policy = newProviderRetryPolicy(10, 500, RetryTuning{}, nil)
	require.Equal(t, 10, policy.MaxAttempts)
	require.Equal(t, 100, policy.MaxTransportAttempts, "transport budget must be clamped to 100")

	policy = newProviderRetryPolicy(10, 0, RetryTuning{}, nil)
	require.Equal(t, 0, policy.MaxTransportAttempts, "0 means the transport budget is not enabled")

	policy = newProviderRetryPolicy(10, -1, RetryTuning{}, nil)
	require.Equal(t, 0, policy.MaxTransportAttempts, "-1 means unlimited transport retries")
}
