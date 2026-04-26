package llm

import (
	"fmt"
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
	assert.Equal(t, "http_429", decision.Reason)
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
