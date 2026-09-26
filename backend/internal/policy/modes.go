package policy

import "strings"

// Mode describes a permission mode.
type Mode string

const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "accept_edits"
	ModePlan              Mode = "plan"
	ModeBypassPermissions Mode = "bypass_permissions"
	// ModeDontAsk is the fail-closed unattended mode: every request that would
	// otherwise ask is denied instead of prompting. Reads, read-only shell
	// commands, remembered grants, and allow rules keep running, which makes it
	// the CI/CD counterpart of bypass_permissions. It is intentionally not
	// part of the shift+tab cycle and must be selected explicitly.
	ModeDontAsk Mode = "dont_ask"
)

func normalizeMode(mode Mode) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ModeAcceptEdits, ModePlan, ModeBypassPermissions:
		return Mode(strings.ToLower(strings.TrimSpace(string(mode))))
	case ModeDontAsk, "dont-ask":
		return ModeDontAsk
	default:
		return ModeDefault
	}
}

// SupportedModes returns the backend-supported permission modes in display order.
func SupportedModes() []Mode {
	return []Mode{ModeDefault, ModeAcceptEdits, ModePlan, ModeBypassPermissions, ModeDontAsk}
}

// ParseMode parses a raw permission mode value. ok is false when the value is
// not supported; callers must reject the request instead of silently falling
// back so a typo cannot weaken the effective policy.
func ParseMode(raw string) (Mode, bool) {
	normalized := Mode(strings.ToLower(strings.TrimSpace(raw)))
	switch normalized {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypassPermissions:
		return normalized, true
	case ModeDontAsk, "dont-ask":
		return ModeDontAsk, true
	default:
		return ModeDefault, false
	}
}

func modeDecision(mode Mode, caps []Capability) DecisionType {
	switch normalizeMode(mode) {
	case ModeBypassPermissions:
		return DecisionAllow
	case ModePlan:
		if hasCapability(caps, CapAskUser) || hasCapability(caps, CapReadOnly) {
			return DecisionAllow
		}
		return DecisionDeny
	case ModeAcceptEdits:
		if hasCapability(caps, CapExecShell) || hasCapability(caps, CapNetwork) || hasCapability(caps, CapExternalSideEffect) || hasCapability(caps, CapBackgroundTask) {
			return DecisionAsk
		}
		return DecisionAllow
	case ModeDontAsk:
		// Fail closed: anything the default mode would ask about is denied.
		if hasCapability(caps, CapWriteFS) || hasCapability(caps, CapExecShell) || hasCapability(caps, CapNetwork) || hasCapability(caps, CapExternalSideEffect) || hasCapability(caps, CapBackgroundTask) {
			return DecisionDeny
		}
		return DecisionAllow
	default:
		if hasCapability(caps, CapWriteFS) || hasCapability(caps, CapExecShell) || hasCapability(caps, CapNetwork) || hasCapability(caps, CapExternalSideEffect) || hasCapability(caps, CapBackgroundTask) {
			return DecisionAsk
		}
		return DecisionAllow
	}
}

func hasCapability(caps []Capability, target Capability) bool {
	for _, cap := range caps {
		if cap == target {
			return true
		}
	}
	return false
}
