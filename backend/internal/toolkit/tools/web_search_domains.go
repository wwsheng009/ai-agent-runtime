package tools

import (
	"fmt"
	"net/url"
	"strings"
)

// webSearchDomainFilter is the client-side domain filter of web_search. The
// search backends have no supported "site:"-style filter that survives both
// Bing and DuckDuckGo HTML parsing, so allowed/blocked domains are enforced on
// the parsed result URLs (Claude Code parity: allowed_domains/blocked_domains).
type webSearchDomainFilter struct {
	allowed []string
	blocked []string
}

// active reports whether any domain rule is configured.
func (f webSearchDomainFilter) active() bool {
	return len(f.allowed) > 0 || len(f.blocked) > 0
}

// parseWebSearchDomainFilter reads allowed_domains/blocked_domains. The two are
// mutually exclusive (a filter that both whitelists and blacklists is almost
// always a mistake); lists accept arrays or comma/semicolon separated strings.
func parseWebSearchDomainFilter(params map[string]interface{}) (webSearchDomainFilter, error) {
	allowedRaw, hasAllowed := params["allowed_domains"]
	blockedRaw, hasBlocked := params["blocked_domains"]
	if !hasAllowed && !hasBlocked {
		return webSearchDomainFilter{}, nil
	}

	allowed := parseWebSearchDomainList(allowedRaw)
	blocked := parseWebSearchDomainList(blockedRaw)
	if hasAllowed && len(allowed) == 0 {
		return webSearchDomainFilter{}, fmt.Errorf("allowed_domains 参数无效：至少需要一个非空域名")
	}
	if hasBlocked && len(blocked) == 0 {
		return webSearchDomainFilter{}, fmt.Errorf("blocked_domains 参数无效：至少需要一个非空域名")
	}
	if len(allowed) > 0 && len(blocked) > 0 {
		return webSearchDomainFilter{}, fmt.Errorf("allowed_domains 与 blocked_domains 互斥，不能同时使用")
	}
	return webSearchDomainFilter{allowed: allowed, blocked: blocked}, nil
}

// parseWebSearchDomainList accepts []string, []interface{} or a single
// comma/semicolon separated string, normalizes every entry and drops
// duplicates while preserving order.
func parseWebSearchDomainList(raw interface{}) []string {
	var candidates []string
	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		candidates = strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n'
		})
	case []string:
		candidates = value
	case []interface{}:
		for _, item := range value {
			if text, ok := item.(string); ok {
				candidates = append(candidates, text)
			}
		}
	default:
		return nil
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		domain := normalizeWebSearchDomain(candidate)
		if domain == "" {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	return out
}

// normalizeWebSearchDomain turns a user/model supplied value into a bare
// lowercase host: "https://Example.com/docs" → "example.com", "*.example.com"
// → "example.com". Paths, ports, wildcards and trailing dots are dropped.
func normalizeWebSearchDomain(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "*.")
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
			value = parsed.Hostname()
		}
	} else if strings.ContainsAny(value, "/?#") {
		if parsed, err := url.Parse("http://" + value); err == nil && parsed.Hostname() != "" {
			value = parsed.Hostname()
		}
	}
	// 仍是 URL 形态说明 host 没能被解析出来（如裸 "https://"）：按无效丢弃，
	// 不能让后面的端口裁剪把 "https://" 变成 "https"。
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/?#") {
		return ""
	}
	if host, _, found := strings.Cut(value, ":"); found && !strings.Contains(host, "]") {
		value = host
	}
	value = strings.Trim(value, ".")
	if !isPlausibleHostname(value) {
		return ""
	}
	return value
}

// isPlausibleHostname keeps only values that can be a DNS host after
// normalization: letters, digits, dots, hyphens and underscores. This drops
// leftovers such as "https://" or paths that url.Parse refused to split.
func isPlausibleHostname(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// apply splits the provider results into kept and dropped. A result without a
// usable host is kept: the filter must not silently hide malformed URLs.
func (f webSearchDomainFilter) apply(results []DuckDuckGoResult) (kept []DuckDuckGoResult, dropped int) {
	if !f.active() {
		return results, 0
	}
	kept = make([]DuckDuckGoResult, 0, len(results))
	for _, result := range results {
		host := hostOfResultURL(result.URL)
		if host == "" || f.matchHost(host) {
			kept = append(kept, result)
			continue
		}
		dropped++
	}
	return kept, dropped
}

// matchHost reports whether a result host passes the configured rules.
func (f webSearchDomainFilter) matchHost(host string) bool {
	if host == "" {
		return true
	}
	if len(f.blocked) > 0 {
		for _, domain := range f.blocked {
			if hostMatchesDomain(host, domain) {
				return false
			}
		}
	}
	if len(f.allowed) > 0 {
		for _, domain := range f.allowed {
			if hostMatchesDomain(host, domain) {
				return true
			}
		}
		return false
	}
	return true
}

// hostOfResultURL extracts the lowercase host of a result URL; "" when the URL
// cannot be parsed or carries no host.
func hostOfResultURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.Trim(parsed.Hostname(), "."))
}

// hostMatchesDomain implements the subdomain-inclusive match: "example.com"
// matches "example.com" and "www.example.com" but not "notexample.com".
func hostMatchesDomain(host, domain string) bool {
	host = strings.ToLower(strings.Trim(host, "."))
	domain = normalizeWebSearchDomain(domain)
	if host == "" || domain == "" {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// metadata renders the active rules for the tool result.
func (f webSearchDomainFilter) metadata() map[string]interface{} {
	if !f.active() {
		return nil
	}
	out := map[string]interface{}{}
	if len(f.allowed) > 0 {
		out["allowed_domains"] = append([]string(nil), f.allowed...)
	}
	if len(f.blocked) > 0 {
		out["blocked_domains"] = append([]string(nil), f.blocked...)
	}
	return out
}
