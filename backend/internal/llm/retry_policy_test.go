package llm

import (
	"context"
	"fmt"
	"net/http"
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

func TestClassifyRetryableLLMError_TreatsInsufficientQuotaAsRetryable(t *testing.T) {
	decision := classifyRetryableLLMError(fmt.Errorf("HTTP 429: {\"error\":{\"code\":\"insufficient_quota\",\"message\":\"You exceeded your current quota\"}}"))
	assert.True(t, decision.Retryable)
	assert.Equal(t, "http_429", decision.Reason)

	decision = classifyRetryableLLMError(fmt.Errorf("HTTP 429: {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"rate limit reached\"}}"))
	assert.True(t, decision.Retryable)
	assert.Equal(t, "rate_limit", decision.Reason)
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
	policy := newProviderRetryPolicy(3, RetryTuning{
		BaseDelay:  400 * time.Millisecond,
		MaxDelay:   3 * time.Second,
		Multiplier: 1.5,
	}, nil)

	assert.Equal(t, 3, policy.MaxAttempts)
	assert.Equal(t, 400*time.Millisecond, policy.BaseDelay)
	assert.Equal(t, 3*time.Second, policy.MaxDelay)
	assert.Equal(t, 1.5, policy.Multiplier)
	assert.Equal(t, 400*time.Millisecond, policy.delayForDecision(1, policy.decisionForError(fmt.Errorf("HTTP 500"))))
	assert.Equal(t, 600*time.Millisecond, policy.delayForDecision(2, policy.decisionForError(fmt.Errorf("HTTP 500"))))
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

	policy := newProviderRetryPolicy(3, RetryTuning{}, rules)
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

func TestRetryPolicy_DefaultAttemptLimitWithoutRulesRemainsUnchanged(t *testing.T) {
	providerPolicy := newProviderRetryPolicy(3, RetryTuning{}, nil)
	assert.Equal(t, 3, providerPolicy.MaxAttempts)
	assert.Equal(t, 3, providerPolicy.DefaultMaxAttempts)
	assert.Equal(t, 3, providerPolicy.maxAttemptsForDecision(providerPolicy.decisionForError(fmt.Errorf("temporary failure"))))

	runtimePolicy := newRuntimeRetryPolicy(2, RetryTuning{}, nil)
	assert.Equal(t, 3, runtimePolicy.MaxAttempts)
	assert.Equal(t, 3, runtimePolicy.DefaultMaxAttempts)
	assert.Equal(t, 3, runtimePolicy.maxAttemptsForDecision(runtimePolicy.decisionForError(fmt.Errorf("temporary failure"))))
}

func TestPrepareRetry_UsesTheLimitForTheCurrentErrorClass(t *testing.T) {
	policy := newProviderRetryPolicy(3, RetryTuning{}, []RetryRule{
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
