package acp

import "encoding/json"

// ProtocolVersion is the major ACP version this agent advertises.
// Official ACP uses a single integer major version (currently 1).
const ProtocolVersion = 1

// Agent-side methods (client → agent).
const (
	MethodInitialize    = "initialize"
	MethodSessionNew    = "session/new"
	MethodSessionPrompt = "session/prompt"
	MethodSessionCancel = "session/cancel"
	MethodSessionLoad   = "session/load" // advertised only when loadSession=true
)

// Client-side methods (agent → client).
const (
	MethodSessionUpdate            = "session/update"
	MethodSessionRequestPermission = "session/request_permission"
)

// Stop reasons returned by session/prompt.
const (
	StopReasonEndTurn         = "end_turn"
	StopReasonMaxTokens       = "max_tokens"
	StopReasonMaxTurnRequests = "max_turn_requests"
	StopReasonRefusal         = "refusal"
	StopReasonCancelled       = "cancelled"
)

// Session update kinds (sessionUpdate field).
const (
	SessionUpdateAgentMessageChunk = "agent_message_chunk"
	SessionUpdateUserMessageChunk  = "user_message_chunk"
	SessionUpdateToolCall          = "tool_call"
	SessionUpdateToolCallUpdate    = "tool_call_update"
	SessionUpdatePlan              = "plan"
	SessionUpdateUsage             = "usage_update"
)

// Tool call status values.
const (
	ToolCallStatusPending    = "pending"
	ToolCallStatusInProgress = "in_progress"
	ToolCallStatusCompleted  = "completed"
	ToolCallStatusFailed     = "failed"
	ToolCallStatusCancelled  = "cancelled"
)

// Tool kinds (ACP taxonomy).
const (
	ToolKindRead    = "read"
	ToolKindEdit    = "edit"
	ToolKindDelete  = "delete"
	ToolKindMove    = "move"
	ToolKindSearch  = "search"
	ToolKindExecute = "execute"
	ToolKindThink   = "think"
	ToolKindFetch   = "fetch"
	ToolKindOther   = "other"
)

// Permission option kinds.
const (
	PermissionKindAllowOnce    = "allow_once"
	PermissionKindAllowAlways  = "allow_always"
	PermissionKindRejectOnce   = "reject_once"
	PermissionKindRejectAlways = "reject_always"
)

// Permission outcome kinds.
const (
	PermissionOutcomeSelected  = "selected"
	PermissionOutcomeCancelled = "cancelled"
)

// Implementation describes clientInfo / agentInfo.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// ClientCapabilities is a subset of ACP client capabilities.
type ClientCapabilities struct {
	FS          *FileSystemCapabilities `json:"fs,omitempty"`
	Terminal    bool                    `json:"terminal,omitempty"`
	Elicitation json.RawMessage         `json:"elicitation,omitempty"`
}

// FileSystemCapabilities describes client fs/* support.
type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// AgentCapabilities is the MVP agent capability advertisement.
type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession,omitempty"`
	PromptCapabilities  *PromptCapabilities `json:"promptCapabilities,omitempty"`
	MCPCapabilities     *MCPCapabilities    `json:"mcpCapabilities,omitempty"`
	SessionCapabilities json.RawMessage     `json:"sessionCapabilities,omitempty"`
}

// PromptCapabilities advertises which ContentBlock types are accepted.
type PromptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

// MCPCapabilities advertises agent-side MCP transport support.
type MCPCapabilities struct {
	HTTP bool `json:"http,omitempty"`
	SSE  bool `json:"sse,omitempty"`
}

// AuthMethod is advertised during initialize (empty for MVP).
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// InitializeRequest is the params for initialize.
type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

// InitializeResponse is the result for initialize.
type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AuthMethods       []AuthMethod      `json:"authMethods"`
}

// NewSessionRequest is the params for session/new.
type NewSessionRequest struct {
	Cwd        string          `json:"cwd"`
	MCPServers json.RawMessage `json:"mcpServers,omitempty"`
}

// NewSessionResponse is the result for session/new.
type NewSessionResponse struct {
	SessionID string `json:"sessionId"`
}

// PromptRequest is the params for session/prompt.
type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// PromptResponse is the result for session/prompt.
type PromptResponse struct {
	StopReason string `json:"stopReason"`
}

// CancelNotification is the params for session/cancel.
type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// ContentBlock is a discriminated content unit in prompts / updates.
// Only type=text is required for the MVP; other types are preserved as raw
// fields so future capabilities can be added without breaking decode.
type ContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`
	MIMEType string          `json:"mimeType,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Name     string          `json:"name,omitempty"`
	Resource json.RawMessage `json:"resource,omitempty"`
	Meta     json.RawMessage `json:"_meta,omitempty"`
}

// SessionUpdateNotification wraps a session/update notification params.
type SessionUpdateNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// SessionUpdate is a flexible session update payload.
// sessionUpdate discriminates the variant; optional fields are filled per kind.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`

	// agent_message_chunk / user_message_chunk
	MessageID string        `json:"messageId,omitempty"`
	Content   *ContentBlock `json:"content,omitempty"`

	// tool_call / tool_call_update
	ToolCallID  string             `json:"toolCallId,omitempty"`
	Title       string             `json:"title,omitempty"`
	Kind        string             `json:"kind,omitempty"`
	Status      string             `json:"status,omitempty"`
	RawInput    interface{}        `json:"rawInput,omitempty"`
	RawOutput   interface{}        `json:"rawOutput,omitempty"`
	Locations   []ToolCallLocation `json:"locations,omitempty"`
	ToolContent []ToolCallContent  `json:"-"` // encoded as "content" for tool updates

	// usage_update
	Used int64 `json:"used,omitempty"`
	Size int64 `json:"size,omitempty"`
}

// MarshalJSON encodes SessionUpdate with the correct content field shape.
// Message chunks use a single ContentBlock; tool updates use a content array.
func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	switch u.SessionUpdate {
	case SessionUpdateAgentMessageChunk, SessionUpdateUserMessageChunk:
		aux := struct {
			SessionUpdate string        `json:"sessionUpdate"`
			MessageID     string        `json:"messageId,omitempty"`
			Content       *ContentBlock `json:"content,omitempty"`
		}{
			SessionUpdate: u.SessionUpdate,
			MessageID:     u.MessageID,
			Content:       u.Content,
		}
		return json.Marshal(aux)
	case SessionUpdateToolCall, SessionUpdateToolCallUpdate:
		aux := struct {
			SessionUpdate string             `json:"sessionUpdate"`
			ToolCallID    string             `json:"toolCallId,omitempty"`
			Title         string             `json:"title,omitempty"`
			Kind          string             `json:"kind,omitempty"`
			Status        string             `json:"status,omitempty"`
			Content       []ToolCallContent  `json:"content,omitempty"`
			Locations     []ToolCallLocation `json:"locations,omitempty"`
			RawInput      interface{}        `json:"rawInput,omitempty"`
			RawOutput     interface{}        `json:"rawOutput,omitempty"`
		}{
			SessionUpdate: u.SessionUpdate,
			ToolCallID:    u.ToolCallID,
			Title:         u.Title,
			Kind:          u.Kind,
			Status:        u.Status,
			Content:       u.ToolContent,
			Locations:     u.Locations,
			RawInput:      u.RawInput,
			RawOutput:     u.RawOutput,
		}
		return json.Marshal(aux)
	default:
		aux := struct {
			SessionUpdate string `json:"sessionUpdate"`
			Used          int64  `json:"used,omitempty"`
			Size          int64  `json:"size,omitempty"`
		}{
			SessionUpdate: u.SessionUpdate,
			Used:          u.Used,
			Size:          u.Size,
		}
		return json.Marshal(aux)
	}
}

// ToolCallContent is content produced by a tool call.
type ToolCallContent struct {
	Type    string        `json:"type"` // "content" | "diff" | "terminal"
	Content *ContentBlock `json:"content,omitempty"`
	Path    string        `json:"path,omitempty"`
	OldText *string       `json:"oldText,omitempty"`
	NewText string        `json:"newText,omitempty"`
}

// ToolCallLocation points at a file the tool is working with.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// RequestPermissionParams is the params for session/request_permission.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallPermission `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// ToolCallPermission carries tool-call identity/details for permission requests.
type ToolCallPermission struct {
	ToolCallID string      `json:"toolCallId"`
	Title      string      `json:"title,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Status     string      `json:"status,omitempty"`
	RawInput   interface{} `json:"rawInput,omitempty"`
}

// PermissionOption is one choice presented to the client.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// RequestPermissionResult is the result of session/request_permission.
type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is either selected or cancelled.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// DefaultPermissionOptions returns the MVP allow/reject pair plus allow-always.
func DefaultPermissionOptions() []PermissionOption {
	return []PermissionOption{
		{OptionID: "allow-once", Name: "Allow once", Kind: PermissionKindAllowOnce},
		{OptionID: "allow-always", Name: "Allow always", Kind: PermissionKindAllowAlways},
		{OptionID: "reject-once", Name: "Reject", Kind: PermissionKindRejectOnce},
	}
}

// IsAllowOption reports whether the selected option grants permission.
func IsAllowOption(optionID string) bool {
	switch optionID {
	case "allow-once", "allow-always":
		return true
	default:
		return false
	}
}

// IsRememberOption reports whether the selected option should be remembered.
func IsRememberOption(optionID string) bool {
	return optionID == "allow-always"
}

// TextContent builds a text ContentBlock.
func TextContent(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: text}
}

// AgentMessageChunk builds a session update for assistant text.
func AgentMessageChunk(text string) SessionUpdate {
	block := TextContent(text)
	return SessionUpdate{
		SessionUpdate: SessionUpdateAgentMessageChunk,
		Content:       &block,
	}
}

// ToolCallStarted builds a pending tool_call update.
func ToolCallStarted(toolCallID, title, kind string, rawInput interface{}) SessionUpdate {
	if kind == "" {
		kind = ToolKindOther
	}
	return SessionUpdate{
		SessionUpdate: SessionUpdateToolCall,
		ToolCallID:    toolCallID,
		Title:         title,
		Kind:          kind,
		Status:        ToolCallStatusPending,
		RawInput:      rawInput,
	}
}

// ToolCallProgress builds a tool_call_update with a new status.
func ToolCallProgress(toolCallID, status string) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: SessionUpdateToolCallUpdate,
		ToolCallID:    toolCallID,
		Status:        status,
	}
}

// ToolCallFinished builds a completed/failed tool_call_update.
func ToolCallFinished(toolCallID, status string, rawOutput interface{}, content []ToolCallContent) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: SessionUpdateToolCallUpdate,
		ToolCallID:    toolCallID,
		Status:        status,
		RawOutput:     rawOutput,
		ToolContent:   content,
	}
}

// TextToolContent wraps plain text as tool call content.
func TextToolContent(text string) ToolCallContent {
	block := TextContent(text)
	return ToolCallContent{Type: "content", Content: &block}
}

// ExtractText joins text ContentBlocks from a prompt into a single string.
func ExtractText(blocks []ContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "", "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "resource_link":
			if block.URI != "" {
				if block.Name != "" {
					parts = append(parts, block.Name+" ("+block.URI+")")
				} else {
					parts = append(parts, block.URI)
				}
			}
		case "resource":
			if len(block.Resource) > 0 {
				var embedded struct {
					Text string `json:"text"`
					URI  string `json:"uri"`
				}
				if err := json.Unmarshal(block.Resource, &embedded); err == nil {
					if embedded.Text != "" {
						parts = append(parts, embedded.Text)
					} else if embedded.URI != "" {
						parts = append(parts, embedded.URI)
					}
				}
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += "\n\n" + parts[i]
	}
	return out
}

// DefaultAgentCapabilities returns MVP capabilities (text prompts only).
func DefaultAgentCapabilities() AgentCapabilities {
	return AgentCapabilities{
		LoadSession: false,
		PromptCapabilities: &PromptCapabilities{
			Image:           false,
			Audio:           false,
			EmbeddedContext: false,
		},
		MCPCapabilities: &MCPCapabilities{
			HTTP: false,
			SSE:  false,
		},
	}
}
