package modelcard

import (
	"path"
	"regexp"
	"strings"
)

func cardMatchScore(ctx Context, modelID string, card Card) (int, bool) {
	modelScore, ok := modelMatcherScore(modelID, card.Match)
	if !ok {
		return 0, false
	}
	score := modelScore
	if len(card.Match.Protocols) > 0 {
		if !matchAnyFold(card.Match.Protocols, ctx.RuntimeProtocol, ctx.LoginProtocol) {
			return 0, false
		}
		score += 20
	}
	if len(card.Match.ProviderNames) > 0 {
		if !matchAnyFold(card.Match.ProviderNames, ctx.ProviderName) {
			return 0, false
		}
		score += 20
	}
	if len(card.Match.BaseURLContains) > 0 {
		if !containsAnyFold(ctx.BaseURL, card.Match.BaseURLContains) {
			return 0, false
		}
		score += 10
	}
	return score, true
}

func cardRecommendationScore(ctx Context, modelID string, card Card) (int, bool) {
	modelScore, ok := modelMatcherScore(modelID, card.Match)
	if !ok {
		return 0, false
	}
	score := modelScore
	if len(card.Match.ProviderNames) > 0 {
		if !matchAnyFold(card.Match.ProviderNames, ctx.ProviderName) {
			return 0, false
		}
		score += 20
	}
	if len(card.Match.BaseURLContains) > 0 {
		if !containsAnyFold(ctx.BaseURL, card.Match.BaseURLContains) {
			return 0, false
		}
		score += 10
	}
	return score, true
}

func fallbackCardMatchScore(ctx Context, card Card) (int, bool) {
	if !card.Fallback {
		return 0, false
	}
	score := 0
	if strings.TrimSpace(card.ProviderTemplate) != "" {
		if strings.TrimSpace(ctx.ProviderTemplate) == "" {
			return 0, false
		}
		if !strings.EqualFold(strings.TrimSpace(card.ProviderTemplate), strings.TrimSpace(ctx.ProviderTemplate)) {
			return 0, false
		}
		score += 30
	}
	if len(card.Match.Protocols) > 0 {
		if !matchAnyFold(card.Match.Protocols, ctx.RuntimeProtocol, ctx.LoginProtocol) {
			return 0, false
		}
		score += 20
	}
	if len(card.Match.ProviderNames) > 0 {
		if !matchAnyFold(card.Match.ProviderNames, ctx.ProviderName) {
			return 0, false
		}
		score += 20
	}
	if len(card.Match.BaseURLContains) > 0 {
		if !containsAnyFold(ctx.BaseURL, card.Match.BaseURLContains) {
			return 0, false
		}
		score += 10
	}
	return score, true
}

func modelMatcherScore(modelID string, match MatchSpec) (int, bool) {
	if matchAnyFold(match.ModelIDs, modelID) {
		return 100, true
	}
	if fuzzyMatchAnyModelID(match.ModelIDs, modelID) {
		return 90, true
	}
	if matchAnyFold(match.Aliases, modelID) {
		return 70, true
	}
	if fuzzyMatchAnyModelID(match.Aliases, modelID) {
		return 60, true
	}
	for _, pattern := range match.ModelPatterns {
		if matchPatternFold(pattern, modelID) {
			return 30, true
		}
	}
	return 0, false
}

func matchAnyFold(values []string, candidates ...string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(value, strings.TrimSpace(candidate)) {
				return true
			}
		}
	}
	return false
}

func fuzzyMatchAnyModelID(values []string, candidate string) bool {
	candidateVariants := modelIDVariants(candidate)
	if len(candidateVariants) == 0 {
		return false
	}
	for _, value := range values {
		valueVariants := modelIDVariants(value)
		for _, valueVariant := range valueVariants {
			if valueVariant == "" {
				continue
			}
			for _, candidateVariant := range candidateVariants {
				if valueVariant == candidateVariant {
					return true
				}
			}
		}
	}
	return false
}

func modelIDVariants(value string) []string {
	normalized := normalizeModelID(value)
	if normalized == "" {
		return nil
	}
	out := []string{normalized}
	add := func(candidate string) {
		candidate = normalizeModelID(candidate)
		if candidate == "" {
			return
		}
		for _, existing := range out {
			if existing == candidate {
				return
			}
		}
		out = append(out, candidate)
	}

	add(strings.TrimPrefix(normalized, "anthropic."))
	add(strings.TrimPrefix(normalized, "models/"))
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 && idx+1 < len(normalized) {
		add(normalized[idx+1:])
	}
	if idx := strings.LastIndex(normalized, "."); idx >= 0 && idx+1 < len(normalized) {
		add(normalized[idx+1:])
	}
	for _, variant := range append([]string(nil), out...) {
		withoutVersionSuffix := strings.TrimSuffix(variant, ":0")
		add(withoutVersionSuffix)
		add(strings.TrimSuffix(withoutVersionSuffix, "-v1"))
		add(strings.TrimSuffix(variant, "-v1"))
	}
	return out
}

func normalizeModelID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, `"'`)
	value = strings.TrimPrefix(value, "models/")
	return value
}

func containsAnyFold(value string, needles []string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func matchPatternFold(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" || value == "" {
		return false
	}
	lowerPattern := strings.ToLower(pattern)
	lowerValue := strings.ToLower(value)
	if ok, err := path.Match(lowerPattern, lowerValue); err == nil && ok {
		return true
	}
	if re, err := regexp.Compile("(?i)" + pattern); err == nil {
		return re.MatchString(value)
	}
	return false
}
