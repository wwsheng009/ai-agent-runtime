package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// ---------------------------------------------------------------------------
// Official API providers (P2-4).
//
// Each API backend is one webSearchProviderRegistry entry (P2-1) with a
// credential-gated Available() check. Requests go through the shared
// fetchWebSearchAPI helper: sandbox check, redirect rejection, bounded read,
// and status→webSearchStatusError wrapping (Retry-After included). The default
// scrape chain is untouched: an API provider participates only when listed in
// the chain order and its credential resolves.
// ---------------------------------------------------------------------------

// webSearchAPIProviderConfig is one API provider's resolved settings.
type webSearchAPIProviderConfig struct {
	name       string
	apiKey     string
	baseURL    string
	maxResults int
}

// available is a cheap local check (credential + endpoint), never network I/O.
func (c webSearchAPIProviderConfig) available() bool {
	if strings.TrimSpace(c.apiKey) == "" {
		return false
	}
	parsed, err := url.Parse(c.baseURL)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

// effectiveCount applies the backend's per-request cap.
func (c webSearchAPIProviderConfig) effectiveCount(count int) int {
	if c.maxResults > 0 && count > c.maxResults {
		return c.maxResults
	}
	return count
}

// webSearchAPIConfigFromRuntime reads the API section nil-safely.
func webSearchAPIConfigFromRuntime(cfg *runtimecfg.RuntimeConfig) runtimecfg.WebSearchAPIConfig {
	if cfg == nil {
		return runtimecfg.WebSearchAPIConfig{}
	}
	return cfg.WebSearch.API
}

// resolveWebSearchAPIProviderConfig resolves one API provider's settings:
// literal apiKey > env(apiKeyEnv or the provider default) ; baseURL override >
// provider default endpoint; 0 maxResults keeps the provider default.
func resolveWebSearchAPIProviderConfig(
	name string,
	spec runtimecfg.WebSearchAPIProviderConfig,
	defaultBaseURL string,
	defaultKeyEnv string,
	maxResultsFallback int,
) webSearchAPIProviderConfig {
	apiKey := strings.TrimSpace(spec.APIKey)
	if apiKey == "" {
		envName := strings.TrimSpace(spec.APIKeyEnv)
		if envName == "" {
			envName = defaultKeyEnv
		}
		apiKey = strings.TrimSpace(os.Getenv(envName))
	}
	baseURL := strings.TrimSpace(spec.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	maxResults := spec.MaxResults
	if maxResults <= 0 {
		maxResults = maxResultsFallback
	}
	return webSearchAPIProviderConfig{name: name, apiKey: apiKey, baseURL: baseURL, maxResults: maxResults}
}

// fetchWebSearchAPI performs one API-provider request through the tool's
// client: sandbox check, redirect rejection, bounded read, and non-200 →
// webSearchStatusError (label appears in the diagnostic message).
func (w *WebSearchTool) fetchWebSearchAPI(ctx context.Context, method, endpoint, label string, headers map[string]string, body []byte) ([]byte, error) {
	if err := w.checkURL(endpoint); err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := *w.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	recordWebSearchFinalHost(ctx, resp)

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, webSearchHTTPStatusError(resp.StatusCode, resp.Header,
			fmt.Sprintf("%s 错误 (状态码 %d): %s", label, resp.StatusCode, strings.TrimSpace(string(payload))))
	}
	return payload, nil
}

// apiSearchLanguage normalizes the tool's language option for API providers:
// empty and "auto" omit the parameter (auto's CJK detection is a Bing-locale
// concern; API backends receive an explicit hint only).
func apiSearchLanguage(language string) string {
	trimmed := strings.TrimSpace(language)
	if trimmed == "" || strings.EqualFold(trimmed, "auto") {
		return ""
	}
	return trimmed
}
