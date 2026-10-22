//go:build !win7compat

package transport

import (
	"errors"
	"io"
	"net/http"
)

// ErrResponseTooLarge is returned before a response body can be decoded.
var ErrResponseTooLarge = errors.New("REMOTE_RESPONSE_TOO_LARGE: MCP response exceeds the local wire cap")

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
	if needsHeaders {
		roundTripper = headerRoundTripper{base: roundTripper, headers: headers, provider: cfg.AccessToken}
	}
	if needsLimit {
		roundTripper = responseLimitRoundTripper{base: roundTripper, limit: cfg.MaxResponseBytes}
	}
	return &http.Client{Transport: roundTripper}
}
