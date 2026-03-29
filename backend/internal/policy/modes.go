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
