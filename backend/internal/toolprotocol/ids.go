package toolprotocol

import "strings"

// ToolID is a stable tool identity string (toolkit/broker/mcp tool name).
type ToolID string

// CallID is the model-facing tool_call_id for one invocation.
type CallID string

// NormalizeToolID trims and lowercases a tool identity for map keys.
// Display names keep original casing via ToolID(raw); use this only for lookup.
func NormalizeToolID(value string) ToolID {
	return ToolID(strings.ToLower(strings.TrimSpace(value)))
}

// String returns the raw tool id.
func (id ToolID) String() string {
	return string(id)
}

// IsEmpty reports whether the tool id is blank.
func (id ToolID) IsEmpty() bool {
	return strings.TrimSpace(string(id)) == ""
}

// String returns the raw call id.
func (id CallID) String() string {
	return string(id)
}

// IsEmpty reports whether the call id is blank.
func (id CallID) IsEmpty() bool {
	return strings.TrimSpace(string(id)) == ""
}

// NormalizeCallID trims a call id without case folding (IDs are opaque).
func NormalizeCallID(value string) CallID {
	return CallID(strings.TrimSpace(value))
}
