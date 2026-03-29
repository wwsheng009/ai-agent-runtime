package policy

import (
	"context"
	"encoding/json"
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
}

// ApprovalHandler handles interactive approval requests.
type ApprovalHandler interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}
