package buildinfo

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
)

// DefaultOriginator is the product token used in default outbound User-Agent
// strings. It mirrors Codex's originator concept (DEFAULT_ORIGINATOR).
const DefaultOriginator = "aicli"

// OriginatorOverrideEnv is an optional process-level override for the product
// token in outbound User-Agent strings.
const OriginatorOverrideEnv = "AICLI_INTERNAL_ORIGINATOR_OVERRIDE"

var (
	defaultUserAgentOnce sync.Once
	defaultUserAgent     string
)

// Originator returns the product token used when building outbound User-Agent
// headers for remote LLM/API traffic.
func Originator() string {
	if override := strings.TrimSpace(os.Getenv(OriginatorOverrideEnv)); override != "" {
		if sanitized := sanitizeUserAgentToken(override); sanitized != "" {
			return sanitized
		}
	}
	return DefaultOriginator
}

// UserAgent returns the default outbound User-Agent for remote LLM/API calls.
//
// Format (inspired by Codex get_codex_user_agent):
//
//	{originator}/{version} ({goos}; {goarch})
//
// Example: aicli/dev (windows; amd64)
func UserAgent() string {
	defaultUserAgentOnce.Do(func() {
		defaultUserAgent = BuildUserAgent(Originator(), Backend().Version, runtime.GOOS, runtime.GOARCH)
	})
	return defaultUserAgent
}

// BuildUserAgent constructs a sanitized User-Agent string from components.
func BuildUserAgent(originator, version, goos, goarch string) string {
	originator = sanitizeUserAgentToken(originator)
	if originator == "" {
		originator = DefaultOriginator
	}

	version = sanitizeUserAgentToken(version)
	if version == "" {
		version = "dev"
	}

	goos = sanitizeUserAgentToken(goos)
	if goos == "" {
		goos = "unknown"
	}

	goarch = sanitizeUserAgentToken(goarch)
	if goarch == "" {
		goarch = "unknown"
	}

	candidate := fmt.Sprintf("%s/%s (%s; %s)", originator, version, goos, goarch)
	return sanitizeUserAgent(candidate, originator)
}

// sanitizeUserAgent keeps only printable ASCII header-safe characters.
// Invalid characters are replaced with '_'. If the result is still unusable,
// it falls back to the provided fallback token and finally DefaultOriginator.
func sanitizeUserAgent(candidate, fallback string) string {
	if isValidHTTPHeaderValue(candidate) {
		return candidate
	}

	var b strings.Builder
	b.Grow(len(candidate))
	for _, ch := range candidate {
		if ch >= 0x20 && ch <= 0x7e {
			b.WriteRune(ch)
		} else {
			b.WriteByte('_')
		}
	}
	sanitized := strings.TrimSpace(b.String())
	if sanitized != "" && isValidHTTPHeaderValue(sanitized) {
		return sanitized
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" && isValidHTTPHeaderValue(fallback) {
		return fallback
	}
	return DefaultOriginator
}

func sanitizeUserAgentToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z',
			ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9',
			ch == '.', ch == '_', ch == '-', ch == '+':
			b.WriteRune(ch)
		case ch == ' ':
			b.WriteByte('_')
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func isValidHTTPHeaderValue(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		// HTTP header values must be visible ASCII (RFC 7230) excluding DEL.
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
