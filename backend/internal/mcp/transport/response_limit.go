//go:build !win7compat

package transport

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrResponseTooLarge is returned before a response body can be decoded.
var ErrResponseTooLarge = errors.New("REMOTE_RESPONSE_TOO_LARGE: MCP response exceeds the local wire cap")

// maxRateLimitWait bounds how long a server-supplied Retry-After may hold the
// request. A hostile or misconfigured target must not be able to stall the
// client for minutes from a single header.
const maxRateLimitWait = 5 * time.Second

// rateLimitRoundTripper honors a server's Retry-After hint on 429 responses.
//
// The response is passed through **unchanged**: the MCP SDK classifies 429 as a
// transient status (isTransientHTTPStatus) and keeps the session alive with its
// own backoff. Replacing the response with an error here would remove that
// classification and tear the session down on a single refusal, which is worse
// than the opaque-error problem this wrapper solves. Waiting before returning is
// the part the SDK cannot do for itself, because it discards the header.
//
// Only 429 is affected, so authentication replays (401/403) and the response
// size cap keep their existing behavior.
type rateLimitRoundTripper struct {
	base http.RoundTripper
}

func (rt rateLimitRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return resp, err
	}
	if wait := parseRetryAfter(resp.Header.Get("Retry-After")); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-req.Context().Done():
			// The caller gave up: close the refused body and propagate the
			// cancellation instead of blocking on the server's schedule.
			_ = resp.Body.Close()
			return nil, req.Context().Err()
		}
	}
	return resp, nil
}

// parseRetryAfter reads the delay-seconds form of Retry-After. A malformed or
// negative value means "do not wait"; the HTTP-date form is not supported by the
// remote tools server, so it is treated as no hint rather than guessed at.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	wait := time.Duration(seconds * float64(time.Second))
	if wait > maxRateLimitWait {
		return maxRateLimitWait
	}
	return wait
}

type responseLimitRoundTripper struct {
	base  http.RoundTripper
	limit int64
}

func (rt responseLimitRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(req)
	if err != nil || response == nil || response.Body == nil || rt.limit <= 0 {
		return response, err
	}
	response.Body = &limitedReadCloser{inner: response.Body, remaining: rt.limit}
	return response, nil
}

type limitedReadCloser struct {
	inner     io.ReadCloser
	remaining int64
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.inner.Read(p)
	if int64(n) > r.remaining {
		return 0, ErrResponseTooLarge
	}
	r.remaining -= int64(n)
	return n, err
}

func (r *limitedReadCloser) Close() error {
	return r.inner.Close()
}

func httpClientWithLimits(cfg *Config, headers http.Header) *http.Client {
	if cfg == nil {
		return nil
	}
	needsHeaders := len(headers) > 0 || cfg.AccessToken != nil
	needsTLS := cfg.TLS != nil
	needsLimit := cfg.MaxResponseBytes > 0
	if !needsHeaders && !needsTLS && !needsLimit {
		return nil
	}
	base, err := newBaseTransport(cfg.TLS)
	if err != nil {
		// Keep the failure observable at request time instead of returning nil.
		base = http.DefaultTransport
	}
	var roundTripper http.RoundTripper = base
	// Innermost, so it observes the untouched status line and Retry-After header
	// before body-wrapping or header injection get involved. It rides on any
	// transport we already customize — which is every remote target, since the
	// client sets a response cap for remote scope and remote tools require a
	// bearer token. A bare local HTTP server with no limits keeps the SDK
	// default client and does not wait on Retry-After.
	roundTripper = rateLimitRoundTripper{base: roundTripper}
	if needsHeaders {
		roundTripper = headerRoundTripper{base: roundTripper, headers: headers, provider: cfg.AccessToken}
	}
	if needsLimit {
		roundTripper = responseLimitRoundTripper{base: roundTripper, limit: cfg.MaxResponseBytes}
	}
	return &http.Client{Transport: roundTripper}
}
