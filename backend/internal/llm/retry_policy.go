package llm

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLLMRetryBaseDelay  = 200 * time.Millisecond
	defaultLLMRetryMaxDelay   = 5 * time.Second
	defaultLLMRetryMultiplier = 2.0
)

type retryDecision struct {
	Retryable   bool
	Delay       time.Duration
	BaseDelay   time.Duration
	Reason      string
	MaxAttempts int
	Multiplier  float64
}

type RetryTuning struct {
	BaseDelay      time.Duration `yaml:"baseDelay,omitempty" json:"baseDelay,omitempty"`
	MaxDelay       time.Duration `yaml:"maxDelay,omitempty" json:"maxDelay,omitempty"`
	MaxElapsedTime time.Duration `yaml:"maxElapsedTime,omitempty" json:"maxElapsedTime,omitempty"`
	Multiplier     float64       `yaml:"multiplier,omitempty" json:"multiplier,omitempty"`
	Randomization  float64       `yaml:"randomization,omitempty" json:"randomization,omitempty"`
}

type RetryRule struct {
	Name              string                 `yaml:"name,omitempty" json:"name,omitempty"`
	Description       string                 `yaml:"description,omitempty" json:"description,omitempty"`
	Enabled           bool                   `yaml:"enabled" json:"enabled"`
	MaxRetries        int                    `yaml:"maxRetries,omitempty" json:"maxRetries,omitempty"`
	RetryDelay        time.Duration          `yaml:"retryDelay,omitempty" json:"retryDelay,omitempty"`
	BackoffMultiplier float64                `yaml:"backoffMultiplier,omitempty" json:"backoffMultiplier,omitempty"`
	Keyword           RetryKeywordMatcher    `yaml:"keyword,omitempty" json:"keyword,omitempty"`
	ErrorCode         RetryErrorCodeMatcher  `yaml:"errorCode,omitempty" json:"errorCode,omitempty"`
	StatusCode        RetryStatusCodeMatcher `yaml:"statusCode,omitempty" json:"statusCode,omitempty"`
}

type RetryKeywordMatcher struct {
	CaseSensitive bool     `yaml:"caseSensitive" json:"caseSensitive"`
	Values        []string `yaml:"values,omitempty" json:"values,omitempty"`
	Patterns      []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`
}

type RetryErrorCodeMatcher struct {
	Codes   []string `yaml:"codes,omitempty" json:"codes,omitempty"`
	Pattern string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
}

type RetryStatusCodeMatcher struct {
	Codes []int  `yaml:"codes,omitempty" json:"codes,omitempty"`
	Range string `yaml:"range,omitempty" json:"range,omitempty"`
}

type retryPolicy struct {
	MaxAttempts    int
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	MaxElapsedTime time.Duration
	Multiplier     float64
	Randomization  float64
	Rules          []RetryRule
}

type retryExhaustedError struct {
	message string
	cause   error
}

func (e *retryExhaustedError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.message) != "" {
		return e.message
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return ""
}

func (e *retryExhaustedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type retrySuppressedError struct {
	message string
	cause   error
}

func (e *retrySuppressedError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.message) != "" {
		return e.message
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return ""
}

func (e *retrySuppressedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type streamEmissionState struct {
	emittedText      bool
	emittedReasoning bool
	emittedImage     bool
}

func (s *streamEmissionState) markText(content string) {
	if s == nil || content == "" {
		return
	}
	s.emittedText = true
}

func (s *streamEmissionState) markReasoning(content string) {
	if s == nil || content == "" {
		return
	}
	s.emittedReasoning = true
}

func (s *streamEmissionState) markImage(metadata map[string]interface{}) {
	if s == nil || len(metadata) == 0 {
		return
	}
	s.emittedImage = true
}

func (s *streamEmissionState) emittedAnything() bool {
	if s == nil {
		return false
	}
	// 只有真正发出了可见正文时才算“已经输出过内容”。
	// reasoning-only 片段不应阻止后续的空回复重试，
	// 否则会把“只吐了思考过程、但最终没有正文”的场景误判为已完成。
	return s.emittedText || s.emittedImage
}

func (c RetryTuning) normalized() RetryTuning {
	if c.BaseDelay <= 0 {
		c.BaseDelay = defaultLLMRetryBaseDelay
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = defaultLLMRetryMaxDelay
	}
	if c.MaxDelay < c.BaseDelay {
		c.MaxDelay = c.BaseDelay
	}
	if c.Multiplier < 1 {
		c.Multiplier = defaultLLMRetryMultiplier
	}
	if c.Randomization < 0 {
		c.Randomization = 0
	}
	if c.Randomization > 1 {
		c.Randomization = 1
	}
	return c
}

func newRuntimeRetryPolicy(maxRetries int, tuning RetryTuning, rules []RetryRule) retryPolicy {
	attempts := maxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	tuning = tuning.normalized()
	rules = cloneRetryRules(rules)
	return retryPolicy{
		MaxAttempts:    maxRetryPolicyInt(attempts, maxRetryRuleAttempts(rules)),
		BaseDelay:      tuning.BaseDelay,
		MaxDelay:       tuning.MaxDelay,
		MaxElapsedTime: tuning.MaxElapsedTime,
		Multiplier:     tuning.Multiplier,
		Randomization:  tuning.Randomization,
		Rules:          rules,
	}
}

func newProviderRetryPolicy(maxRetries int, tuning RetryTuning, rules []RetryRule) retryPolicy {
	attempts := maxRetries
	if attempts < 1 {
		attempts = 1
	}
	tuning = tuning.normalized()
	rules = cloneRetryRules(rules)
	return retryPolicy{
		MaxAttempts:    maxRetryPolicyInt(attempts, maxRetryRuleAttempts(rules)),
		BaseDelay:      tuning.BaseDelay,
		MaxDelay:       tuning.MaxDelay,
		MaxElapsedTime: tuning.MaxElapsedTime,
		Multiplier:     tuning.Multiplier,
		Randomization:  tuning.Randomization,
		Rules:          rules,
	}
}

func (p retryPolicy) decisionForError(err error) retryDecision {
	return classifyRetryableLLMErrorWithRules(err, p.Rules)
}

func (p retryPolicy) maxAttemptsForDecision(decision retryDecision) int {
	if decision.MaxAttempts > 0 {
		return decision.MaxAttempts
	}
	return p.MaxAttempts
}

func (p retryPolicy) delayForDecision(attempt int, decision retryDecision) time.Duration {
	if decision.Delay > 0 {
		return decision.Delay
	}
	baseDelay := p.BaseDelay
	if decision.BaseDelay > 0 {
		baseDelay = decision.BaseDelay
	}
	multiplier := p.Multiplier
	if decision.Multiplier >= 1 {
		multiplier = decision.Multiplier
	}
	delay := nextRetryDelay(baseDelay, multiplier, attempt, p.MaxDelay)
	return p.randomizeDelay(delay)
}

var retryPolicyRandomFloat64 = rand.Float64

func nextRetryDelay(base time.Duration, multiplier float64, attempt int, max time.Duration) time.Duration {
	if base <= 0 {
		base = defaultLLMRetryBaseDelay
	}
	if multiplier < 1 {
		multiplier = defaultLLMRetryMultiplier
	}
	if attempt < 1 {
		attempt = 1
	}

	delay := time.Duration(float64(base) * math.Pow(multiplier, float64(attempt-1)))
	if delay <= 0 {
		delay = base
	}
	if max > 0 && delay > max {
		return max
	}
	return delay
}

func (p retryPolicy) randomizeDelay(delay time.Duration) time.Duration {
	if delay <= 0 || p.Randomization <= 0 {
		return delay
	}
	factor := 1 + ((retryPolicyRandomFloat64()*2 - 1) * p.Randomization)
	if factor < 0 {
		factor = 0
	}
	randomized := time.Duration(float64(delay) * factor)
	if p.MaxDelay > 0 && randomized > p.MaxDelay {
		return p.MaxDelay
	}
	return randomized
}

func (p retryPolicy) canRetryAfter(startedAt time.Time, now time.Time, delay time.Duration) bool {
	if p.MaxElapsedTime <= 0 {
		return true
	}
	if startedAt.IsZero() {
		return delay <= p.MaxElapsedTime
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Sub(startedAt)+delay <= p.MaxElapsedTime
}

func waitRetryDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func markRetryExhausted(prefix string, attempts int, err error) error {
	if err == nil || attempts <= 1 {
		return err
	}
	message := strings.TrimSpace(prefix)
	if message == "" {
		message = "all retry attempts failed"
	}
	return &retryExhaustedError{
		message: fmt.Sprintf("%s: %v", message, err),
		cause:   err,
	}
}

func suppressRetry(err error) error {
	if err == nil {
		return nil
	}
	return &retrySuppressedError{
		message: err.Error(),
		cause:   err,
	}
}

var retryAfterPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:retry[- ]after|try again in)\s*([0-9]+(?:\.[0-9]+)?)\s*(ms|milliseconds?|s|sec|secs|seconds?|m|min|mins|minutes?)`),
	regexp.MustCompile(`(?i)(?:retry[- ]after|try again in)\s*([0-9]+(?:\.[0-9]+)?)`),
	regexp.MustCompile(`(?i)please\s+try\s+again\s+in\s*([0-9]+(?:\.[0-9]+)?)\s*(ms|milliseconds?|s|sec|secs|seconds?|m|min|mins|minutes?)`),
}

func parseRetryAfterFromMessage(msg string) (time.Duration, bool) {
	for _, pattern := range retryAfterPatterns {
		matches := pattern.FindStringSubmatch(msg)
		if len(matches) < 2 {
			continue
		}
		unit := ""
		if len(matches) >= 3 {
			unit = matches[2]
		}
		if duration, ok := parseRetryAfterDuration(matches[1], unit); ok {
			return duration, true
		}
	}
	return 0, false
}

func parseRetryAfterDuration(rawValue string, rawUnit string) (time.Duration, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(rawValue), 64)
	if err != nil || value <= 0 {
		return 0, false
	}

	switch strings.ToLower(strings.TrimSpace(rawUnit)) {
	case "", "s", "sec", "secs", "second", "seconds":
		return time.Duration(value * float64(time.Second)), true
	case "ms", "millisecond", "milliseconds":
		return time.Duration(value * float64(time.Millisecond)), true
	case "m", "min", "mins", "minute", "minutes":
		return time.Duration(value * float64(time.Minute)), true
	default:
		return 0, false
	}
}

type retryErrorCoder interface {
	RetryErrorCode() string
}

type retryDelayHinter interface {
	RetryAfterDelay() time.Duration
}

func classifyRetryableLLMError(err error) retryDecision {
	return classifyRetryableLLMErrorWithRules(err, nil)
}

func classifyRetryableLLMErrorWithRules(err error, rules []RetryRule) retryDecision {
	if err == nil {
		return retryDecision{}
	}

	var exhaustedErr *retryExhaustedError
	if stderrs.As(err, &exhaustedErr) {
		return retryDecision{Retryable: false, Reason: "retry_exhausted"}
	}
	var suppressedErr *retrySuppressedError
	if stderrs.As(err, &suppressedErr) {
		return retryDecision{Retryable: false, Reason: "retry_suppressed"}
	}

	if stderrs.Is(err, context.Canceled) {
		return retryDecision{Retryable: false, Reason: "context_canceled"}
	}

	if decision, ok := decisionFromRetryRules(err, rules); ok {
		return decision
	}

	if !isRetryableProviderResponseError(err) {
		return retryDecision{Retryable: false, Reason: "non_retryable_response"}
	}

	lower := strings.ToLower(err.Error())
	if statusCode, ok := providerCallHTTPStatus(err); ok {
		switch statusCode {
		case http.StatusRequestTimeout, http.StatusConflict:
			return retryDecision{
				Retryable: true,
				Delay:     decisionDelayFromServerHint(err),
				Reason:    fmt.Sprintf("http_%d", statusCode),
			}
		case http.StatusTooManyRequests:
			if containsAny(lower, "rate limit", "rate_limit_exceeded", "too many requests", "slow_down") {
				return retryDecision{
					Retryable: true,
					Delay:     decisionDelayFromServerHint(err),
					Reason:    "rate_limit",
				}
			}
			return retryDecision{
				Retryable: true,
				Delay:     decisionDelayFromServerHint(err),
				Reason:    "http_429",
			}
		case http.StatusBadRequest:
			if containsAny(lower, "data_inspection_failed", "content_inspection_failed", "inappropriate content") {
				return retryDecision{
					Retryable: false,
					Reason:    "content_inspection_failed",
				}
			}
			if containsAny(lower, "invalid_request_error", "missing required parameter", "unsupported parameter", "unrecognized request argument", "unknown parameter", "unexpected parameter") {
				return retryDecision{
					Retryable: false,
					Reason:    "invalid_request",
				}
			}
			return retryDecision{Retryable: false, Reason: fmt.Sprintf("http_%d", statusCode)}
		default:
			if statusCode >= 500 {
				return retryDecision{
					Retryable: true,
					Delay:     decisionDelayFromServerHint(err),
					Reason:    fmt.Sprintf("http_%d", statusCode),
				}
			}
			return retryDecision{Retryable: false, Reason: fmt.Sprintf("http_%d", statusCode)}
		}
	}

	if containsAny(lower, "data_inspection_failed", "content_inspection_failed", "inappropriate content") {
		return retryDecision{
			Retryable: false,
			Reason:    "content_inspection_failed",
		}
	}
	if containsAny(lower, "truncated_tool_call", "incomplete tool call markup", "truncated before completing a tool call") {
		return retryDecision{
			Retryable: false,
			Reason:    "truncated_tool_call",
		}
	}
	if containsAny(lower, "reasoning_only_empty_reply", "reasoning-only empty reply", "reasoning only empty reply") {
		return retryDecision{
			Retryable: true,
			Delay:     decisionDelayFromServerHint(err),
			Reason:    "reasoning_only_empty_reply",
		}
	}
	if containsAny(lower, "stream_interrupted", "stream disconnected before completion", "stream closed before response.completed", "stream closed before completion") {
		return retryDecision{
			Retryable: true,
			Delay:     decisionDelayFromServerHint(err),
			Reason:    "stream_interrupted",
		}
	}
	if containsAny(lower, "empty_reply", "empty_stream_response", "stream ended without substantive output") {
		return retryDecision{
			Retryable: true,
			Delay:     decisionDelayFromServerHint(err),
			Reason:    "empty_reply",
		}
	}
	if isRetryableTransportError(err, lower) {
		return retryDecision{
			Retryable: true,
			Delay:     decisionDelayFromServerHint(err),
			Reason:    "transport",
		}
	}

	if containsAny(lower,
		"rate limit",
		"rate_limit_exceeded",
		"too many requests",
		"slow_down",
		"quota exceeded",
	) {
		return retryDecision{
			Retryable: true,
			Delay:     decisionDelayFromServerHint(err),
			Reason:    "rate_limit",
		}
	}

	if containsAny(lower,
		"empty_stream_response",
		"only ping events",
		"heartbeat timeout",
		"request timeout",
		"timed out",
		"timeout awaiting response headers",
		"stream disconnected before completion",
		"stream closed before response.completed",
		"stream closed before completion",
		"connection reset by peer",
		"temporary upstream failure",
		"unexpected eof",
	) {
		return retryDecision{
			Retryable: true,
			Delay:     decisionDelayFromServerHint(err),
			Reason:    "transient_stream_or_server",
		}
	}

	return retryDecision{
		Retryable: true,
		Delay:     decisionDelayFromServerHint(err),
		Reason:    "default_retryable",
	}
}

func decisionFromRetryRules(err error, rules []RetryRule) (retryDecision, bool) {
	if err == nil || len(rules) == 0 {
		return retryDecision{}, false
	}

	statusCode, _ := providerCallHTTPStatus(err)
	errorCode := extractRetryErrorCode(err)
	message := err.Error()

	for _, rule := range rules {
		if !rule.Enabled || !retryRuleHasMatcher(rule) {
			continue
		}
		if !retryRuleMatches(rule, statusCode, errorCode, message) {
			continue
		}
		decision := retryDecision{
			Retryable:   true,
			Delay:       decisionDelayFromServerHint(err),
			BaseDelay:   rule.RetryDelay,
			Reason:      strings.TrimSpace(rule.Name),
			MaxAttempts: rule.MaxRetries,
			Multiplier:  rule.BackoffMultiplier,
		}
		if decision.Reason == "" {
			decision.Reason = "configured_retry_rule"
		}
		return decision, true
	}

	return retryDecision{}, false
}

func decisionDelayFromServerHint(err error) time.Duration {
	if delay, ok := errorRetryAfterDelay(err); ok {
		return delay
	}
	if delay, ok := parseRetryAfterFromMessage(err.Error()); ok {
		return delay
	}
	return 0
}

func errorRetryAfterDelay(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var hinter retryDelayHinter
	if stderrs.As(err, &hinter) {
		if delay := hinter.RetryAfterDelay(); delay > 0 {
			return delay, true
		}
	}
	return 0, false
}

func retryAfterDelayFromHeader(header http.Header, now time.Time) (time.Duration, bool) {
	if len(header) == 0 {
		return 0, false
	}
	if delay, ok := parseRetryAfterMillisecondsValue(header.Get("Retry-After-Ms")); ok {
		return delay, true
	}
	return parseRetryAfterHeaderValue(header.Get("Retry-After"), now)
}

func parseRetryAfterHeaderValue(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if delay, ok := parseRetryAfterDuration(value, ""); ok {
		return delay, true
	}
	parsedTime, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	delay := parsedTime.Sub(now)
	if delay <= 0 {
		return 0, false
	}
	return delay, true
}

func parseRetryAfterMillisecondsValue(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration, true
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return 0, false
	}
	return time.Duration(number * float64(time.Millisecond)), true
}

func retryAfterDelayFromBody(body string) (time.Duration, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return 0, false
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var payload interface{}
	if err := decoder.Decode(&payload); err != nil {
		return 0, false
	}
	return findRetryAfterDelayInValue(payload)
}

func findRetryAfterDelayInValue(value interface{}) (time.Duration, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for _, key := range []string{"retry_after_ms", "retryAfterMs", "retry_after_milliseconds"} {
			if delay, ok := parseRetryDelayValue(typed[key], true); ok {
				return delay, true
			}
		}
		for _, key := range []string{"retry_after", "retryAfter"} {
			if delay, ok := parseRetryDelayValue(typed[key], false); ok {
				return delay, true
			}
		}
		for _, nested := range typed {
			if delay, ok := findRetryAfterDelayInValue(nested); ok {
				return delay, true
			}
		}
	case []interface{}:
		for _, nested := range typed {
			if delay, ok := findRetryAfterDelayInValue(nested); ok {
				return delay, true
			}
		}
	}
	return 0, false
}

func parseRetryDelayValue(value interface{}, milliseconds bool) (time.Duration, bool) {
	if value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err != nil || number <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(number, milliseconds), true
	case float64:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(typed, milliseconds), true
	case float32:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(float64(typed), milliseconds), true
	case int:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(float64(typed), milliseconds), true
	case int64:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(float64(typed), milliseconds), true
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0, false
		}
		if duration, err := time.ParseDuration(text); err == nil && duration > 0 {
			return duration, true
		}
		if milliseconds {
			return parseRetryAfterMillisecondsValue(text)
		}
		return parseRetryAfterHeaderValue(text, time.Time{})
	default:
		return 0, false
	}
}

func retryDelayFromFloat(value float64, milliseconds bool) time.Duration {
	if milliseconds {
		return time.Duration(value * float64(time.Millisecond))
	}
	return time.Duration(value * float64(time.Second))
}

func retryRuleHasMatcher(rule RetryRule) bool {
	return retryKeywordMatcherConfigured(rule.Keyword) ||
		retryErrorCodeMatcherConfigured(rule.ErrorCode) ||
		retryStatusCodeMatcherConfigured(rule.StatusCode)
}

func retryKeywordMatcherConfigured(matcher RetryKeywordMatcher) bool {
	return len(matcher.Values) > 0 || len(matcher.Patterns) > 0
}

func retryErrorCodeMatcherConfigured(matcher RetryErrorCodeMatcher) bool {
	return len(matcher.Codes) > 0 || strings.TrimSpace(matcher.Pattern) != ""
}

func retryStatusCodeMatcherConfigured(matcher RetryStatusCodeMatcher) bool {
	return len(matcher.Codes) > 0 || strings.TrimSpace(matcher.Range) != ""
}

func retryRuleMatches(rule RetryRule, statusCode int, errorCode string, message string) bool {
	if retryStatusCodeMatcherConfigured(rule.StatusCode) && !retryStatusCodeMatches(rule.StatusCode, statusCode) {
		return false
	}
	if retryErrorCodeMatcherConfigured(rule.ErrorCode) && !retryErrorCodeMatches(rule.ErrorCode, errorCode) {
		return false
	}
	if retryKeywordMatcherConfigured(rule.Keyword) && !retryKeywordMatches(rule.Keyword, message) {
		return false
	}
	return true
}

func retryStatusCodeMatches(matcher RetryStatusCodeMatcher, statusCode int) bool {
	if statusCode == 0 {
		return false
	}
	for _, code := range matcher.Codes {
		if code == statusCode {
			return true
		}
	}
	rangeSpec := strings.TrimSpace(matcher.Range)
	if rangeSpec == "" {
		return len(matcher.Codes) > 0
	}
	parts := strings.SplitN(rangeSpec, "-", 2)
	if len(parts) == 2 {
		minValue, minErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		maxValue, maxErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if minErr == nil && maxErr == nil && statusCode >= minValue && statusCode <= maxValue {
			return true
		}
		return false
	}
	exact, err := strconv.Atoi(rangeSpec)
	return err == nil && statusCode == exact
}

func retryErrorCodeMatches(matcher RetryErrorCodeMatcher, errorCode string) bool {
	errorCode = strings.TrimSpace(errorCode)
	if errorCode == "" {
		return false
	}
	for _, code := range matcher.Codes {
		if strings.EqualFold(strings.TrimSpace(code), errorCode) {
			return true
		}
	}
	pattern := strings.TrimSpace(matcher.Pattern)
	if pattern == "" {
		return len(matcher.Codes) > 0
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(errorCode)
}

func retryKeywordMatches(matcher RetryKeywordMatcher, message string) bool {
	haystack := message
	if !matcher.CaseSensitive {
		haystack = strings.ToLower(message)
	}
	for _, value := range matcher.Values {
		needle := value
		if !matcher.CaseSensitive {
			needle = strings.ToLower(value)
		}
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	for _, pattern := range matcher.Patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(message) {
			return true
		}
	}
	return false
}

func extractRetryErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var coder retryErrorCoder
	if stderrs.As(err, &coder) {
		return strings.TrimSpace(coder.RetryErrorCode())
	}

	message := err.Error()
	if idx := strings.Index(message, ":"); idx > 0 {
		prefix := strings.ToLower(strings.TrimSpace(message[:idx]))
		if isRetryCodePrefix(prefix) {
			return prefix
		}
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:error_?code|code)\b["'=:\s\[]+([a-z0-9_]+)`),
		regexp.MustCompile(`(?i)\b(request_timeout|rate_limit_exceeded|slow_down|context_length_exceeded|invalid_request_error|missing_required_parameter)\b`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(message)
		if len(matches) >= 2 {
			return strings.TrimSpace(matches[1])
		}
	}
	return ""
}

func isRetryCodePrefix(value string) bool {
	if value == "" || !strings.Contains(value, "_") {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

func isRetryableTransportError(err error, lower string) bool {
	var netErr net.Error
	if stderrs.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if stderrs.As(err, &urlErr) {
		return true
	}
	if stderrs.Is(err, io.EOF) || stderrs.Is(err, io.ErrUnexpectedEOF) || stderrs.Is(err, context.DeadlineExceeded) {
		return true
	}
	return containsAny(lower,
		"connection refused",
		"connection reset",
		"connection reset by peer",
		"dial tcp",
		"no such host",
		"server closed idle connection",
		"timeout",
		"temporarily unavailable",
		"tls handshake timeout",
		"unexpected eof",
	)
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func cloneRetryRules(input []RetryRule) []RetryRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]RetryRule, len(input))
	for i, rule := range input {
		cloned := rule
		cloned.Keyword.Values = append([]string(nil), rule.Keyword.Values...)
		cloned.Keyword.Patterns = append([]string(nil), rule.Keyword.Patterns...)
		cloned.ErrorCode.Codes = append([]string(nil), rule.ErrorCode.Codes...)
		cloned.StatusCode.Codes = append([]int(nil), rule.StatusCode.Codes...)
		output[i] = cloned
	}
	return output
}

func maxRetryRuleAttempts(rules []RetryRule) int {
	maxAttempts := 0
	for _, rule := range rules {
		if !rule.Enabled || rule.MaxRetries <= 0 {
			continue
		}
		if rule.MaxRetries > maxAttempts {
			maxAttempts = rule.MaxRetries
		}
	}
	return maxAttempts
}

func maxRetryPolicyInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func validateStreamingAggregateResponse(protocol string, responseBody []byte, assistantMsg map[string]interface{}) error {
	body := strings.TrimSpace(string(responseBody))
	if body == "" {
		return fmt.Errorf("stream_interrupted: empty response body")
	}

	if detail, ok := streamBodyHasContentInspectionFailure(body); ok {
		return fmt.Errorf("content_inspection_failed: %s", detail)
	}

	if streamBodyLooksIncomplete(protocol, body) {
		if strings.EqualFold(strings.TrimSpace(protocol), "codex") {
			return fmt.Errorf("stream_interrupted: stream closed before response.completed")
		}
		return fmt.Errorf("stream_interrupted: stream disconnected before completion")
	}

	if assistantMessageHasReasoningOnlyOutput(assistantMsg) {
		return fmt.Errorf("reasoning_only_empty_reply: stream ended with reasoning only and no substantive output")
	}

	if assistantMessageHasTruncatedToolCall(assistantMsg) {
		return fmt.Errorf("truncated_tool_call: incomplete tool call markup in aggregated assistant response")
	}

	if !assistantMessageHasSubstantiveOutput(assistantMsg) {
		return fmt.Errorf("empty_reply: stream ended without substantive output")
	}

	return nil
}

func streamBodyLooksIncomplete(protocol string, body string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "codex":
		return !containsAny(body, "response.completed", "response.failed", "response.incomplete")
	case "openai":
		return !strings.Contains(body, "\"finish_reason\"") && !strings.Contains(body, "[DONE]")
	case "anthropic":
		return !strings.Contains(body, "event: message_stop")
	case "gemini":
		return !strings.Contains(body, "\"finishReason\"") && !strings.Contains(body, "\"finish_reason\"")
	default:
		return false
	}
}

func assistantMessageHasSubstantiveOutput(assistantMsg map[string]interface{}) bool {
	if len(assistantMsg) == 0 {
		return false
	}
	if content, ok := assistantMsg["content"].(string); ok && strings.TrimSpace(content) != "" {
		return true
	}
	switch toolCalls := assistantMsg["tool_calls"].(type) {
	case []interface{}:
		return len(toolCalls) > 0
	case []map[string]interface{}:
		return len(toolCalls) > 0
	}
	for _, item := range assistantMessageOutputItems(assistantMsg) {
		if assistantOutputItemHasSubstantiveOutput(item) {
			return true
		}
	}
	return false
}

func assistantMessageHasTruncatedToolCall(assistantMsg map[string]interface{}) bool {
	if len(assistantMsg) == 0 {
		return false
	}
	if content, ok := assistantMsg["content"].(string); ok && hasIncompleteToolCallMarkup(content) {
		return true
	}
	finishReason, _ := assistantMsg["finish_reason"].(string)
	if !strings.EqualFold(strings.TrimSpace(finishReason), "length") {
		return false
	}
	switch toolCalls := assistantMsg["tool_calls"].(type) {
	case []interface{}:
		return len(toolCalls) > 0
	case []map[string]interface{}:
		return len(toolCalls) > 0
	}
	return false
}

func hasIncompleteToolCallMarkup(content string) bool {
	if !strings.Contains(content, "<tool_call>") {
		return false
	}
	return !strings.Contains(content, "</tool_call>")
}

func assistantMessageHasReasoningOnlyOutput(assistantMsg map[string]interface{}) bool {
	if len(assistantMsg) == 0 {
		return false
	}
	if assistantMessageHasSubstantiveOutput(assistantMsg) {
		return false
	}
	if reasoning := assistantMessageReasoningText(assistantMsg); strings.TrimSpace(reasoning) != "" {
		return true
	}
	outputItems := assistantMessageOutputItems(assistantMsg)
	if len(outputItems) == 0 {
		return false
	}
	for _, item := range outputItems {
		if !strings.EqualFold(strings.TrimSpace(stringValue(item["type"])), "reasoning") {
			return false
		}
	}
	return true
}

func assistantMessageOutputItems(assistantMsg map[string]interface{}) []map[string]interface{} {
	if len(assistantMsg) == 0 {
		return nil
	}
	return canonicalizeCodexOutputItems(extractCodexOutputItems(assistantMsg))
}

func assistantMessageReasoningText(assistantMsg map[string]interface{}) string {
	if len(assistantMsg) == 0 {
		return ""
	}
	if reasoning, ok := assistantMsg["reasoning"].(string); ok && strings.TrimSpace(reasoning) != "" {
		return strings.TrimSpace(reasoning)
	}
	if reasoning, ok := assistantMsg["reasoning_content"].(string); ok && strings.TrimSpace(reasoning) != "" {
		return strings.TrimSpace(reasoning)
	}
	if metadata := decodeMapAny(assistantMsg["metadata"]); metadata != nil {
		if reasoning, ok := metadata["reasoning_content"].(string); ok && strings.TrimSpace(reasoning) != "" {
			return strings.TrimSpace(reasoning)
		}
	}
	return ""
}

func assistantOutputItemHasSubstantiveOutput(item map[string]interface{}) bool {
	if len(item) == 0 {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(stringValue(item["type"]))) {
	case "message":
		switch content := item["content"].(type) {
		case string:
			return strings.TrimSpace(content) != ""
		case []interface{}:
			return len(content) > 0
		case []map[string]interface{}:
			return len(content) > 0
		}
		return false
	case "function_call", "custom_tool_call", "image_generation_call":
		return true
	}
	return false
}

func streamBodyHasContentInspectionFailure(body string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(body))
	if lower == "" {
		return "", false
	}
	if !containsAny(lower, "data_inspection_failed", "content_inspection_failed", "inappropriate content") {
		return "", false
	}
	switch {
	case containsAny(lower, "input data may contain inappropriate content", "input may contain inappropriate content"):
		return "input data may contain inappropriate content", true
	case containsAny(lower, "output data may contain inappropriate content", "output may contain inappropriate content"):
		return "output data may contain inappropriate content", true
	default:
		return "data may contain inappropriate content", true
	}
}
