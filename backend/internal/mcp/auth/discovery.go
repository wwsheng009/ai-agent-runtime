package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ResourceMetadata 是 RFC 9728 的受保护资源 metadata（子集）。
type ResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
	BearerMethods        []string `json:"bearer_methods_supported"`
}

// AuthorizationServerMetadata 是 RFC 8414 / OIDC Discovery 的授权服务器 metadata（子集）。
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// Discovery 汇总一次授权所需的发现结果。
type Discovery struct {
	// Resource 受保护资源 metadata（可能为空：服务器未提供 well-known）。
	Resource *ResourceMetadata
	// ResourceMetadataURL 实际命中的受保护资源 metadata 地址（诊断用）。
	ResourceMetadataURL string
	// AuthServer 授权服务器 metadata。
	AuthServer *AuthorizationServerMetadata
	// AuthServerURL 实际使用的授权服务器地址（issuer 或覆盖值）。
	AuthServerURL string
	// ChallengeScope 来自 401 WWW-Authenticate 的 scope 建议。
	ChallengeScope string
	// ScopesSupported 可用 scope 列表（资源 + 授权服务器合并）。
	ScopesSupported []string
}

var errNotFound = errors.New("not found")

// Discover 按 MCP 文档链路发现授权服务器：
//
//	探测 MCP 端点的 401 WWW-Authenticate → RFC 9728 受保护资源 metadata
//	→ RFC 8414（或 OIDC）授权服务器 metadata。
//
// overrideAuthServer 非空时跳过资源 metadata 的授权服务器选择。
func Discover(ctx context.Context, client *http.Client, serverURL, overrideAuthServer string) (*Discovery, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return nil, fmt.Errorf("MCP server URL 为空，无法执行 OAuth 发现")
	}
	if client == nil {
		client = &http.Client{}
	}

	discovery := &Discovery{}

	challenge, probeErr := probeResource(ctx, client, serverURL)
	if challenge != nil {
		discovery.ChallengeScope = challenge.scope
	}

	resourceMeta, resourceMetaURL, err := fetchResourceMetadata(ctx, client, serverURL, challenge)
	if err == nil && resourceMeta != nil {
		discovery.Resource = resourceMeta
		discovery.ResourceMetadataURL = resourceMetaURL
		discovery.ScopesSupported = append(discovery.ScopesSupported, resourceMeta.ScopesSupported...)
	} else if err != nil && !errors.Is(err, errNotFound) {
		// 网络/解析错误不阻断：仍可回退到 origin 级发现或用户显式覆盖。
		discovery.ResourceMetadataURL = ""
	}

	authServerURL := strings.TrimSpace(overrideAuthServer)
	if authServerURL == "" && discovery.Resource != nil && len(discovery.Resource.AuthorizationServers) > 0 {
		authServerURL = strings.TrimSpace(discovery.Resource.AuthorizationServers[0])
	}
	if authServerURL == "" {
		origin, originErr := url.Parse(serverURL)
		if originErr != nil {
			return nil, fmt.Errorf("解析 MCP server URL 失败: %w", originErr)
		}
		authServerURL = fmt.Sprintf("%s://%s", origin.Scheme, origin.Host)
	}

	meta, usedURL, metaErr := fetchAuthServerMetadata(ctx, client, authServerURL)
	if metaErr != nil {
		if probeErr != nil {
			return nil, fmt.Errorf("OAuth 发现失败：%v；且探测 MCP 端点出错: %v。%s", metaErr, probeErr, manualFallbackHint)
		}
		return nil, fmt.Errorf("OAuth 发现失败：%v。%s", metaErr, manualFallbackHint)
	}
	if strings.TrimSpace(meta.AuthorizationEndpoint) == "" || strings.TrimSpace(meta.TokenEndpoint) == "" {
		return nil, fmt.Errorf("授权服务器 %s 的 metadata 缺少 authorization_endpoint/token_endpoint。%s", usedURL, manualFallbackHint)
	}
	discovery.AuthServer = meta
	// 记录 issuer（而非 well-known 候选地址），刷新令牌时可直接复用发现链路。
	discovery.AuthServerURL = strings.TrimRight(strings.TrimSpace(firstNonEmpty(meta.Issuer, authServerURL)), "/")
	if discovery.AuthServerURL == "" {
		discovery.AuthServerURL = strings.TrimRight(usedURL, "/")
	}
	discovery.ScopesSupported = mergeScopes(discovery.ScopesSupported, meta.ScopesSupported)
	return discovery, nil
}

type authChallenge struct {
	resourceMetadata string
	scope            string
}

// probeResource 用一个最小 MCP initialize 请求探测 401 挑战。
func probeResource(ctx context.Context, client *http.Client, serverURL string) (*authChallenge, error) {
	const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"aicli","version":"1.0.0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(initializeBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil, nil
	}
	for _, value := range resp.Header.Values("WWW-Authenticate") {
		if challenge := parseWWWAuthenticate(value); challenge != nil {
			return challenge, nil
		}
	}
	return &authChallenge{}, nil
}

// parseWWWAuthenticate 解析 `Bearer resource_metadata="...", scope="a b"` 形式的挑战。
func parseWWWAuthenticate(header string) *authChallenge {
	value := strings.TrimSpace(header)
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower, "bearer") {
		return nil
	}
	value = strings.TrimSpace(value[len("bearer"):])
	if value == "" {
		return &authChallenge{}
	}
	params := map[string]string{}
	for _, part := range splitAuthParams(value) {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"`)
		if key != "" {
			params[key] = val
		}
	}
	return &authChallenge{
		resourceMetadata: params["resource_metadata"],
		scope:            params["scope"],
	}
}

// splitAuthParams 以逗号切分，但忽略引号内的逗号。
func splitAuthParams(input string) []string {
	var parts []string
	var b strings.Builder
	inQuotes := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
			b.WriteByte(c)
		case c == ',' && !inQuotes:
			parts = append(parts, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if trimmed := strings.TrimSpace(b.String()); trimmed != "" {
		parts = append(parts, trimmed)
	}
	return parts
}

// fetchResourceMetadata 依次尝试：挑战里的 resource_metadata → RFC 9728 well-known 变体。
func fetchResourceMetadata(ctx context.Context, client *http.Client, serverURL string, challenge *authChallenge) (*ResourceMetadata, string, error) {
	candidates := make([]string, 0, 3)
	if challenge != nil && strings.TrimSpace(challenge.resourceMetadata) != "" {
		candidates = append(candidates, strings.TrimSpace(challenge.resourceMetadata))
	}
	candidates = append(candidates, protectedResourceWellKnown(serverURL)...)

	var lastErr error
	for _, candidate := range candidates {
		var meta ResourceMetadata
		if err := fetchJSON(ctx, client, candidate, &meta); err != nil {
			if errors.Is(err, errNotFound) {
				lastErr = err
				continue
			}
			return nil, candidate, err
		}
		return &meta, candidate, nil
	}
	if lastErr == nil {
		lastErr = errNotFound
	}
	return nil, "", lastErr
}

// protectedResourceWellKnown 生成 RFC 9728 的候选地址（路径变体优先）。
func protectedResourceWellKnown(serverURL string) []string {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Host == "" {
		return nil
	}
	origin := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	path := strings.Trim(parsed.Path, "/")
	out := make([]string, 0, 2)
	if path != "" {
		out = append(out, origin+"/.well-known/oauth-protected-resource/"+path)
	}
	out = append(out, origin+"/.well-known/oauth-protected-resource")
	return out
}

// fetchAuthServerMetadata 依次尝试 RFC 8414 与 OIDC discovery 的候选地址。
func fetchAuthServerMetadata(ctx context.Context, client *http.Client, authServerURL string) (*AuthorizationServerMetadata, string, error) {
	candidates := authServerWellKnown(authServerURL)
	if len(candidates) == 0 {
		return nil, "", fmt.Errorf("授权服务器地址无效: %q", authServerURL)
	}
	var lastErr error
	for _, candidate := range candidates {
		var meta AuthorizationServerMetadata
		if err := fetchJSON(ctx, client, candidate, &meta); err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(meta.Issuer) == "" {
			meta.Issuer = strings.TrimRight(authServerURL, "/")
		}
		return &meta, candidate, nil
	}
	if lastErr == nil {
		lastErr = errNotFound
	}
	return nil, "", lastErr
}

func authServerWellKnown(authServerURL string) []string {
	parsed, err := url.Parse(strings.TrimSpace(authServerURL))
	if err != nil || parsed.Host == "" {
		return nil
	}
	base := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	path := strings.Trim(parsed.Path, "/")
	out := make([]string, 0, 4)
	if path != "" {
		out = append(out, base+"/.well-known/oauth-authorization-server/"+path)
	}
	out = append(out, strings.TrimRight(authServerURL, "/")+"/.well-known/oauth-authorization-server")
	out = append(out, strings.TrimRight(authServerURL, "/")+"/.well-known/openid-configuration")
	if path != "" {
		out = append(out, base+"/.well-known/openid-configuration/"+path)
	}
	return dedupeStrings(out)
}

func fetchJSON(ctx context.Context, client *http.Client, target string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("GET %s 返回 %d", target, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取 %s 响应失败: %w", target, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 %s 响应失败: %w", target, err)
	}
	return nil
}

func mergeScopes(groups ...[]string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, group := range groups {
		for _, scope := range group {
			scope = strings.TrimSpace(scope)
			if scope == "" {
				continue
			}
			if _, ok := seen[scope]; ok {
				continue
			}
			seen[scope] = struct{}{}
			out = append(out, scope)
		}
	}
	return out
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
