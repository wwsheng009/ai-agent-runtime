package agentcontrol

import (
	"strconv"
	"strings"
)

const (
	SessionContextParentSessionID = "agent_parent_session_id"
	SessionContextRootSessionID   = "agent_root_session_id"
	SessionContextAgentType       = "agent_type"
	SessionContextRequestedModel  = "agent_requested_model"
	SessionContextPath            = "agent_path"
	SessionContextDepth           = "agent_depth"
	SessionContextTeamID          = "agent_team_id"
	SessionContextTeammateID      = "agent_teammate_id"
)

// ContextGetter is the small context surface shared by chat sessions and
// registry projection helpers without tying this package to a storage type.
type ContextGetter interface {
	GetContext(key string) (interface{}, bool)
}

// ContextSetter is the write side of ContextGetter.
type ContextSetter interface {
	ContextGetter
	SetContext(key string, value interface{})
}

// ContextString returns a trimmed string value from a session context.
func ContextString(session ContextGetter, key string) string {
	if session == nil || strings.TrimSpace(key) == "" {
		return ""
	}
	value, ok := session.GetContext(key)
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// SessionDepth returns the numeric depth stored on an agent session context.
func SessionDepth(session ContextGetter) int {
	if session == nil {
		return 0
	}
	value, ok := session.GetContext(SessionContextDepth)
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		depth, _ := strconv.Atoi(strings.TrimSpace(typed))
		return depth
	default:
		return 0
	}
}

// ChildDepth returns the depth for a direct child of parent.
func ChildDepth(parent ContextGetter) int {
	if parent == nil {
		return 1
	}
	return SessionDepth(parent) + 1
}

// RootSessionID resolves a session tree root from context, fallback, or the
// session's own ID supplied by the caller.
func RootSessionID(session ContextGetter, sessionID, fallback string) string {
	if root := ContextString(session, SessionContextRootSessionID); root != "" {
		return root
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	return strings.TrimSpace(sessionID)
}

// SessionPath resolves the stable agent path for a session.
func SessionPath(session ContextGetter, sessionID string, isAgent bool) string {
	if path := ContextString(session, SessionContextPath); path != "" {
		return path
	}
	if isAgent {
		return "/root/" + SanitizePathSegment(sessionID)
	}
	return "/root"
}

// ChildPath returns a stable child path under the resolved parent path.
func ChildPath(parent ContextGetter, parentSessionID, childSessionID string, parentIsAgent bool) string {
	parentPath := SessionPath(parent, parentSessionID, parentIsAgent)
	if parentPath == "" {
		parentPath = "/root"
	}
	return strings.TrimRight(parentPath, "/") + "/" + SanitizePathSegment(childSessionID)
}

// TeamTeammatePath returns the AgentRegistry projection path for a spawn_team
// teammate session.
func TeamTeammatePath(teamID, teammateID, teammateName, sessionID string) string {
	return "/root/teams/" + SanitizePathSegment(teamID) + "/" + SanitizePathSegment(firstNonEmpty(teammateID, teammateName, sessionID))
}

// SetContextIfChanged writes a context value only when it materially changes.
func SetContextIfChanged(session ContextSetter, key string, value interface{}) bool {
	if session == nil || strings.TrimSpace(key) == "" {
		return false
	}
	switch typed := value.(type) {
	case string:
		value = strings.TrimSpace(typed)
		if value == "" {
			return false
		}
	}
	if existing, ok := session.GetContext(key); ok && ContextValueEqual(existing, value) {
		return false
	}
	session.SetContext(key, value)
	return true
}

// ContextValueEqual compares context values using the loose numeric/string
// shape emitted by JSON-backed stores.
func ContextValueEqual(existing interface{}, expected interface{}) bool {
	switch expectedValue := expected.(type) {
	case string:
		if text, ok := existing.(string); ok {
			return strings.TrimSpace(text) == expectedValue
		}
	case int:
		switch typed := existing.(type) {
		case int:
			return typed == expectedValue
		case int64:
			return int(typed) == expectedValue
		case float64:
			return int(typed) == expectedValue
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			return err == nil && parsed == expectedValue
		}
	}
	return false
}

// SanitizePathSegment normalizes agent path segments shared by CLI and API
// control-plane projections.
func SanitizePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "agent"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	text := strings.Trim(b.String(), "-")
	if text == "" {
		return "agent"
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}
