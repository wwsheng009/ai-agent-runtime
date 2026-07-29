package siteaccount

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout   = 5 * time.Second
	defaultDetectTimeout = 8 * time.Second
	maxResponseBodyBytes = 2 << 20
	defaultUserAgent     = "ai-agent-runtime-siteaccount/1.0"
)

type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func resolveTimeout(timeout, fallback time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return fallback
}

func newDefaultHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: resolveTimeout(timeout, defaultHTTPTimeout)}
}

type httpResult struct {
	StatusCode  int
	Body        []byte
	ContentType string
	Header      http.Header
}

func doGET(ctx context.Context, client httpClient, rawURL, accept string, headers map[string]string) (httpResult, error) {
	if client == nil {
		client = newDefaultHTTPClient(defaultHTTPTimeout)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return httpResult{}, httpError("build request failed", err)
	}
	if accept == "" {
		accept = "application/json,text/plain,*/*"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", defaultUserAgent)
	for key, value := range headers {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return httpResult{}, httpError("request failed", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxResponseBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return httpResult{}, httpError("read response failed", err)
	}
	if len(body) > maxResponseBodyBytes {
		return httpResult{}, httpError("response too large", fmt.Errorf("body exceeds %d bytes", maxResponseBodyBytes))
	}
	return httpResult{
		StatusCode:  resp.StatusCode,
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		Header:      resp.Header.Clone(),
	}, nil
}

// OriginURL returns scheme://host from a base URL, discarding path/query/fragment.
func OriginURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", invalidInput("base_url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Host == "" {
		return "", invalidInput("base_url is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", invalidInput("base_url scheme must be http or https")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// JoinURL joins baseURL and requestPath while collapsing duplicated path segments.
func JoinURL(baseURL, requestPath string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	requestPath = strings.TrimSpace(requestPath)
	if requestPath == "" {
		return baseURL
	}
	if parsed, err := url.Parse(requestPath); err == nil && parsed.IsAbs() {
		return requestPath
	}
	if baseURL == "" {
		if strings.HasPrefix(requestPath, "/") {
			return requestPath
		}
		return "/" + requestPath
	}
	pathPart, suffix := splitPathSuffix(requestPath)
	if strings.TrimSpace(pathPart) == "" {
		return baseURL + suffix
	}
	parsedBase, err := url.Parse(baseURL)
	if err != nil || parsedBase.Scheme == "" || parsedBase.Host == "" {
		return baseURL + "/" + strings.TrimLeft(requestPath, "/")
	}
	baseSegments := splitPathSegments(parsedBase.Path)
	requestSegments := splitPathSegments(pathPart)
	overlap := longestOverlap(baseSegments, requestSegments)
	finalSegments := append(append([]string(nil), baseSegments...), requestSegments[overlap:]...)
	if len(finalSegments) == 0 {
		parsedBase.Path = ""
	} else {
		parsedBase.Path = "/" + strings.Join(finalSegments, "/")
	}
	return parsedBase.String() + suffix
}

func splitPathSuffix(path string) (string, string) {
	for i, r := range path {
		if r == '?' || r == '#' {
			return path[:i], path[i:]
		}
	}
	return path, ""
}

func splitPathSegments(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func longestOverlap(base, request []string) int {
	max := len(base)
	if len(request) < max {
		max = len(request)
	}
	for n := max; n > 0; n-- {
		match := true
		for i := 0; i < n; i++ {
			if !strings.EqualFold(base[len(base)-n+i], request[i]) {
				match = false
				break
			}
		}
		if match {
			return n
		}
	}
	return 0
}

func bearerHeaders(token string) map[string]string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + token}
}
