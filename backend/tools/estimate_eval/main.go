// Command estimate_eval measures the local token estimator against a captured
// provider request wrapper (e.g. from ~/.aicli/chat-logs/.../http/*_request_provider_wrapper.json).
//
// Usage: go run ./tools/estimate_eval <wrapper.json>
//
// Purpose (2026-10-11 incident evaluation): quantify the gap between the
// send-gate's parsed-content estimate and the provider's wire-payload count,
// and check the estimator against the serialized JSON body. Keep this tool
// read-only and dependency-free; it is an offline evaluation aid, not part of
// any runtime path.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/tokenestimate"
)

type wrapper struct {
	BodyBytes         int    `json:"body_bytes"`
	BodyCapturedBytes int    `json:"body_captured_bytes"`
	BodyTruncated     bool   `json:"body_truncated"`
	BodyText          string `json:"body_text"`
	// Provider-reported token count for the full request, when known. The
	// 2026-10-11 incident request was counted at 262,948 tokens by the
	// provider; pass -provider-tokens to override.
	ProviderTokens int `json:"-"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: estimate_eval <wrapper.json> [provider_tokens]")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var payload wrapper
	if err := json.Unmarshal(raw, &payload); err != nil {
		panic(err)
	}
	providerFullTokens := payload.ProviderTokens
	if len(os.Args) >= 3 {
		if _, err := fmt.Sscanf(os.Args[2], "%d", &providerFullTokens); err != nil {
			panic(err)
		}
	}
	if providerFullTokens <= 0 {
		fmt.Println("note: no provider token count supplied; pass it as the second argument for ratio output")
	}

	text := payload.BodyText
	capturedBytes := len(text)
	capturedRunes := utf8.RuneCountInString(text)
	byteRatio := float64(capturedBytes) / float64(payload.BodyBytes)
	charRatio := float64(capturedRunes) / float64(payload.BodyBytes)

	fmt.Printf("body_bytes=%d captured_bytes=%d truncated=%t capture_byte_ratio=%.4f\n",
		payload.BodyBytes, capturedBytes, payload.BodyTruncated, byteRatio)
	fmt.Printf("captured_runes=%d capture_rune_ratio=%.4f\n", capturedRunes, charRatio)
	if providerFullTokens > 0 {
		fmt.Printf("provider_full_tokens=%d provider_prefix_tokens~=%.0f\n",
			providerFullTokens, float64(providerFullTokens)*byteRatio)
	}

	for _, profile := range []tokenestimate.Profile{
		tokenestimate.ProfileGeneric,
		tokenestimate.ProfileO200k,
		tokenestimate.ProfileCL100k,
		tokenestimate.ProfileAnthropic,
	} {
		estimate := tokenestimate.Estimate(text, profile)
		impliedByBytes := float64(estimate) / byteRatio
		impliedByChars := float64(estimate) / charRatio
		if providerFullTokens > 0 {
			fmt.Printf("profile=%-9s prefix_estimate=%d implied_full(byte-ratio)=%.0f implied_full(char-ratio)=%.0f provider_ratio=%.3f..%.3f\n",
				profile.Name, estimate, impliedByBytes, impliedByChars,
				impliedByBytes/float64(providerFullTokens), impliedByChars/float64(providerFullTokens))
			continue
		}
		fmt.Printf("profile=%-9s prefix_estimate=%d implied_full(byte-ratio)=%.0f implied_full(char-ratio)=%.0f\n",
			profile.Name, estimate, impliedByBytes, impliedByChars)
	}
}
