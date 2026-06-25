package team

import (
	"regexp"
	"strings"
)

var (
	routeAuditBearerPattern   = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]+`)
	routeAuditSecretPattern   = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret)\s*[:=]\s*[^,\s;]+`)
	routeAuditOpenAIToken     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	routeAuditURLUserinfo     = regexp.MustCompile(`([a-z][a-z0-9+.-]*://)[^/@\s]+@`)
	routeAuditAuthHeaderValue = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*)[^\s,;]+(?:\s+[^\s,;]+)?`)
)

func sanitizeRouteAuditText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = routeAuditAuthHeaderValue.ReplaceAllString(value, `${1}[REDACTED]`)
	value = routeAuditBearerPattern.ReplaceAllString(value, `${1} [REDACTED]`)
	value = routeAuditSecretPattern.ReplaceAllString(value, `${1}=[REDACTED]`)
	value = routeAuditOpenAIToken.ReplaceAllString(value, `sk-[REDACTED]`)
	value = routeAuditURLUserinfo.ReplaceAllString(value, `${1}[REDACTED]@`)
	return strings.TrimSpace(value)
}
