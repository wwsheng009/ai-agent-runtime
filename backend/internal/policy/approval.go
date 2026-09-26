package policy

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ApprovalRequest defines an interactive approval request for tool execution.
type ApprovalRequest struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name"`
	ArgsJSON   json.RawMessage `json:"args_json,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	RiskLevel  string          `json:"risk_level,omitempty"`
	ExpiresAt  time.Time       `json:"expires_at,omitempty"`
}

// ApprovalResponse captures the resolution of an approval request.
type ApprovalResponse struct {
	Allowed     bool            `json:"allowed"`
	Reason      string          `json:"reason,omitempty"`
	PatchedArgs json.RawMessage `json:"patched_args,omitempty"`
	// Remember requests that an allow decision be stored as a grant (ignored for dangerous tools).
	Remember bool `json:"remember,omitempty"`
	// RememberScope selects how long an allow decision is remembered (§4.8):
	// "once" (no store), "session" (in-memory grant store, the legacy default
	// when Remember is true) or "project" (durable
	// <project>/.aicli/grants.json, effective for new sessions too). An empty
	// value keeps the legacy meaning: Remember=true → session.
	RememberScope string `json:"remember_scope,omitempty"`
	// RememberPattern optionally overrides the derived grant pattern. Patterns
	// use the §4.1 specifier shape (cmd:/path:/host:/exact:); empty derives one
	// from the tool call.
	RememberPattern string `json:"remember_pattern,omitempty"`
	// Feedback carries the user's free-text note when they reject a call (or
	// annotate an allow). It is surfaced to the model as part of the decision
	// reason so a rejection can redirect the run instead of only blocking it.
	Feedback string `json:"feedback,omitempty"`
}

// Remember scope values for ApprovalResponse.RememberScope.
const (
	RememberScopeOnce    = "once"
	RememberScopeSession = "session"
	RememberScopeProject = "project"
)

// NormalizedRememberScope resolves Remember/RememberScope into one of
// RememberScopeOnce|Session|Project. Unknown scopes fall back to session
// (never project): an unrecognized value must not silently create durable
// grants.
func (r ApprovalResponse) NormalizedRememberScope() string {
	if !r.Allowed {
		// A rejection never remembers; keep the scope answer consistent with
		// ShouldRemember so callers cannot read "session" off a deny.
		return RememberScopeOnce
	}
	switch strings.ToLower(strings.TrimSpace(r.RememberScope)) {
	case RememberScopeOnce:
		return RememberScopeOnce
	case RememberScopeProject:
		return RememberScopeProject
	case RememberScopeSession:
		return RememberScopeSession
	}
	if r.Remember {
		return RememberScopeSession
	}
	return RememberScopeOnce
}

// ShouldRemember reports whether the response asks for a grant. An explicit
// non-once scope implies the request even when Remember is unset; Remember=true
// without a scope keeps the legacy session-scoped behavior.
func (r ApprovalResponse) ShouldRemember() bool {
	if !r.Allowed {
		return false
	}
	scope := strings.ToLower(strings.TrimSpace(r.RememberScope))
	switch scope {
	case RememberScopeSession, RememberScopeProject:
		return true
	case RememberScopeOnce:
		return false
	}
	return r.Remember
}

// ApprovalHandler handles interactive approval requests.
type ApprovalHandler interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}
