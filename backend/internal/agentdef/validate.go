package agentdef

import (
	"fmt"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// Validate checks a normalized definition for required fields and enum values.
func Validate(def *Definition) error {
	if def == nil {
		return fmt.Errorf("agentdef: definition is nil")
	}
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("agentdef: name is required")
	}
	if strings.ContainsAny(def.Name, `/\`) {
		return fmt.Errorf("agentdef: name %q must not contain path separators", def.Name)
	}

	switch def.PromptMode {
	case "", PromptModeExtend, PromptModeFull:
	default:
		return fmt.Errorf("agentdef: invalid promptMode %q (want extend|full)", def.PromptMode)
	}

	switch def.CompletionRequirement {
	case "", CompletionNone, CompletionCompleteTask:
	default:
		return fmt.Errorf("agentdef: invalid completionRequirement %q (want none|complete_task)", def.CompletionRequirement)
	}

	if mode := strings.TrimSpace(def.PermissionMode); mode != "" {
		switch runtimepolicy.Mode(strings.ToLower(mode)) {
		case runtimepolicy.ModeDefault,
			runtimepolicy.ModeAcceptEdits,
			runtimepolicy.ModePlan,
			runtimepolicy.ModeBypassPermissions:
			// ok
		case "dont_ask":
			// accepted alias; runtime may map later
		default:
			return fmt.Errorf("agentdef: invalid permissionMode %q", def.PermissionMode)
		}
	}

	switch def.Sandbox {
	case "", "off", "workspace", "read-only", "readonly", "strict":
	default:
		return fmt.Errorf("agentdef: invalid sandbox %q (want off|workspace|read-only|strict)", def.Sandbox)
	}

	return nil
}
