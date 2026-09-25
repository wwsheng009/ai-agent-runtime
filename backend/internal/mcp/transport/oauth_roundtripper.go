//go:build !win7compat

package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
)

// AccessTokenProvider 提供按需刷新的 OAuth 访问令牌。
//
// 与 config.AccessTokenProvider 方法集一致：Go 的接口赋值按方法集匹配，
// 因此 config 侧的实现可直接注入，无需 transport import config。
type AccessTokenProvider interface {
	AccessToken(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context) (string, error)
	NeedsAuth() (bool, string)
}

// maxBufferedBodyForRetry 超过此大小的请求体不缓冲（放弃 401 重试，直接返回原响应）。
const maxBufferedBodyForRetry = 1 << 20

// cloneRequestWithHeaders 克隆请求并覆盖配置的静态头。
func cloneRequestWithHeaders(req *http.Request, headers http.Header) *http.Request {
	cloned := req.Clone(req.Context())
	for key, values := range headers {
		for _, value := range values {
			cloned.Header.Set(key, value)
		}
	}
	return cloned
}

// hasStaticAuthorization 判断静态头里是否已有 Authorization（手动 header 兜底优先）。
func hasStaticAuthorization(headers http.Header) bool {
	if len(headers) == 0 {
		return false
	}
	return strings.TrimSpace(headers.Get("Authorization")) != ""
}

// roundTripWithAccessToken 注入 Bearer 令牌；遇到 401/403 时强制刷新并重试一次。
//
// 重试要求请求体可重放（http.NewRequest 会为常见 body 设置 GetBody）；
// 不可重放时保留原始 401 响应，让上层把 server 标记为「需认证」。
func roundTripWithAccessToken(base http.RoundTripper, original *http.Request, headers http.Header, provider AccessTokenProvider) (*http.Response, error) {
	token, err := provider.AccessToken(original.Context())
	if err != nil {
		return nil, err
	}

	attempt := cloneRequestWithHeaders(original, headers)
	if token != "" {
		attempt.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := base.RoundTrip(attempt)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return resp, nil
	}

	body, replayable := replayableBody(original)
	if !replayable {
		return resp, nil
	}
	fresh, refreshErr := provider.ForceRefresh(original.Context())
	if refreshErr != nil {
		// 无法刷新：保留 401/403 响应让调用方看到真实失败；provider 已记录需认证。
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()

	retry := cloneRequestWithHeaders(original, headers)
	retry.Body = io.NopCloser(bytes.NewReader(body))
	retry.ContentLength = int64(len(body))
	retry.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	if fresh != "" {
		retry.Header.Set("Authorization", "Bearer "+fresh)
	}
	return base.RoundTrip(retry)
}

// replayableBody 通过 GetBody 复制请求体，供 401 重试使用。
func replayableBody(req *http.Request) ([]byte, bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, true
	}
	if req.ContentLength > maxBufferedBodyForRetry {
		return nil, false
	}
	if req.GetBody == nil {
		return nil, false
	}
	reader, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, maxBufferedBodyForRetry+1))
	if err != nil || len(data) > maxBufferedBodyForRetry {
		return nil, false
	}
	return data, true
}
