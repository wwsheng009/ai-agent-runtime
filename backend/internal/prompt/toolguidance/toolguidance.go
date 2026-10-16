// Package toolguidance derives tool-visibility facts for prompt guidance from
// a runtime tool policy, so host prompt composition can withhold guidance
// sections whose subject tools are not model-visible (profile denylist /
// read-only stripping / empty child allowlists).
//
// The mapping lives here instead of internal/prompt to keep the prompt package
// policy-agnostic; it is consumed by both hosts (runtimeapi agent context and
// the aicli chat session composer).
package toolguidance

import (
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// FactsFromPolicy derives ToolGuidanceFacts from a resolved execution policy.
// It returns nil when the policy does not narrow the tool surface (no read-only
// stripping, no allowlist, no explicit denies): hosts then keep the legacy
// unconditional guidance sections byte-for-byte.
func FactsFromPolicy(policy *runtimepolicy.ToolExecutionPolicy) *runtimeprompt.ToolGuidanceFacts {
	if policy == nil {
		return nil
	}
	if !policy.ReadOnly && !policy.AllowlistEnabled && len(policy.DeniedTools) == 0 {
		return nil
	}
	facts := &runtimeprompt.ToolGuidanceFacts{}
	if policy.AllowlistEnabled {
		// 通配名单（"*" / "prefix_*"）不构成"only 这些工具"的语义（面被通配
		// 放大，列出字面项会误导渲染器把别的工具当缺席），此时 Only 保持 nil
		// = "有名单但不收窄到字面名单"。
		if !toolregistry.ListDeclaresWildcard(allowedToolPatternNames(policy.AllowedTools)) {
			only := make(map[string]bool, len(policy.AllowedTools))
			for name, allowed := range policy.AllowedTools {
				if !allowed {
					continue
				}
				only[strings.ToLower(strings.TrimSpace(name))] = true
			}
			facts.Only = only
		}
	}
	if len(policy.DeniedTools) > 0 || policy.ReadOnly {
		denied := make(map[string]bool, len(policy.DeniedTools)+len(runtimeprompt.WriteLikeTools))
		for name, deniedValue := range policy.DeniedTools {
			if !deniedValue {
				continue
			}
			denied[strings.ToLower(strings.TrimSpace(name))] = true
		}
		if policy.ReadOnly {
			for _, name := range runtimeprompt.WriteLikeTools {
				denied[strings.ToLower(name)] = true
			}
		}
		facts.Denied = denied
	}
	return facts
}

// allowedToolPatternNames flattens the allowlist map into a name slice for
// wildcard detection.
func allowedToolPatternNames(allowed map[string]bool) []string {
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	return names
}
