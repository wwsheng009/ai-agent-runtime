package httpclient

import (
	"errors"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
)

var errMissingBaseTransport = errors.New("httpclient: base transport not configured")

// EnsureDefaultUserAgent sets a product User-Agent when the request does not
// already carry one. This prevents Go's default "Go-http-client/1.1".
func EnsureDefaultUserAgent(req *http.Request) {
	if req == nil {
		return
	}
	if strings.TrimSpace(req.Header.Get("User-Agent")) != "" {
		return
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
}

// WithDefaultUserAgent wraps a RoundTripper so outbound requests always carry
// a structured product User-Agent unless the caller already set one.
func WithDefaultUserAgent(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if existing, ok := base.(*defaultUserAgentRoundTripper); ok {
		return existing
	}
	return &defaultUserAgentRoundTripper{base: base}
}

type defaultUserAgentRoundTripper struct {
	base http.RoundTripper
}

func (t *defaultUserAgentRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || t.base == nil {
		return nil, errMissingBaseTransport
	}
	// Clone the request before mutating headers so we do not race with the
	// caller's ownership of the original request.
	if req != nil {
		cloned := req.Clone(req.Context())
		EnsureDefaultUserAgent(cloned)
		req = cloned
	}
	return t.base.RoundTrip(req)
}
