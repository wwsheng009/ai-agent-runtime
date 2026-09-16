package policy

import "strings"

// Mode describes a permission mode.
type Mode string

const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "accept_edits"
	ModePlan              Mode = "plan"
	ModeBypassPermissions Mode = "bypass_permissions"
)

func normalizeMode(mode Mode) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case ModeAcceptEdits, ModePlan, ModeBypassPermissions:
		return Mode(strings.ToLower(strings.TrimSpace(string(mode))))
	default:
		return ModeDefault
	}
}

// SupportedModes returns the backend-supported permission modes in display order.
func SupportedModes() []Mode {
	return []Mode{ModeDefault, ModeAcceptEdits, ModePlan, ModeBypassPermissions}
}

// ParseMode parses a raw permission mode value. ok is false when the value is
// not supported; callers must reject the request instead of silently falling
// back so a typo cannot weaken the effective policy.
func ParseMode(raw string) (Mode, bool) {
	normalized := Mode(strings.ToLower(strings.TrimSpace(raw)))
	switch normalized {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypassPermissions:
		return normalized, true
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
