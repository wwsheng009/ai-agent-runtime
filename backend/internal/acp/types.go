package acp

import (
	"bytes"
	"encoding/json"
	"strings"
)

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
	// MethodSessionSetConfigOption changes one session config option (model
	// picker, mode, reasoning level, ...). ACP v1 replaced the removed
	// session/set_model channel with config options; select-type options are
	// baseline client support (no capability advertisement required).
	MethodSessionSetConfigOption = "session/set_config_option"
	// MethodSessionSetMode is the legacy session mode selector channel
	// (params: {sessionId, modeId}). ACP v1 supersedes it with a config option
	// whose category is "mode" (see SessionConfigOptionCategoryMode) and notes
	// the dedicated method "will be removed in a future version of the
	// protocol". Both are served so clients that still drive the legacy mode
	// selector keep working; the state is the same session permission mode.
	MethodSessionSetMode = "session/set_mode"
	// MethodSessionList enumerates stored sessions for the client's history
	// panel. Advertised via agentCapabilities.sessionCapabilities.list.
	MethodSessionList = "session/list"
	// MethodSessionDelete removes one stored session. Advertised via
	// agentCapabilities.sessionCapabilities.delete.
	MethodSessionDelete = "session/delete"
	// MethodSessionClose releases one live session. Advertised via
	// agentCapabilities.sessionCapabilities.close.
	MethodSessionClose  = "session/close"
	MethodCancelRequest = "$/cancel_request"
)

// Client-side methods (agent → client).
const (
	MethodSessionUpdate            = "session/update"
	MethodSessionRequestPermission = "session/request_permission"
	// MethodSessionRequestQuestion is an agent→client extension used to render
	// ask_user_question prompts in the client UI. ACP v1 has no question RPC, so
	// it is gated behind the "questions" client capability: clients that do not
	// advertise it never receive the call and hosts fall back to their
	// non-interactive default answer.
	MethodSessionRequestQuestion = "session/request_question"
)

// ClientCapabilityQuestions is the clientCapabilities key a client sets to true
// when it can display and answer session/request_question prompts.
const ClientCapabilityQuestions = "questions"

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
	SessionUpdateAgentThoughtChunk = "agent_thought_chunk"
	SessionUpdateToolCall          = "tool_call"
	SessionUpdateToolCallUpdate    = "tool_call_update"
	SessionUpdatePlan              = "plan"
	SessionUpdateUsage             = "usage_update"
	// SessionUpdateSessionInfo carries session metadata (currently the
	// auto-generated title) so clients can render session lists and headers.
	SessionUpdateSessionInfo = "session_info_update"
	// SessionUpdateAvailableCommands carries the slash-command catalog the
	// client renders in its "/" menu.
	SessionUpdateAvailableCommands = "available_commands_update"
	// SessionUpdateConfigOptionUpdate is the session/update kind agents send
	// when config options change outside a set_config_option response.
	SessionUpdateConfigOptionUpdate = "config_option_update"
	// SessionUpdateCurrentModeUpdate reports an agent-initiated mode change on
	// the legacy modes channel (update body: {sessionUpdate, currentModeId}).
	// It is the modes-API counterpart of config_option_update and is only sent
	// to clients that were offered the legacy `modes` state.
	SessionUpdateCurrentModeUpdate = "current_mode_update"
)

// SessionConfigOptionCategory values. category is a client-side UX hint:
// "model" tells clients (e.g. Zed) to wire the model picker and its
// "Change Model" keybindings.
const (
	SessionConfigOptionCategoryMode         = "mode"
	SessionConfigOptionCategoryModel        = "model"
	SessionConfigOptionCategoryModelConfig  = "model_config"
	SessionConfigOptionCategoryThoughtLevel = "thought_level"
	SessionConfigOptionCategoryOther        = "other"
	// SessionConfigOptionCategoryProvider is a custom (non-spec) category:
	// ACP reserves categories starting with "_" for extensions, so clients
	// that do not know it still render the option as a generic select.
	SessionConfigOptionCategoryProvider = "_provider"
)

// SessionConfigOption type discriminators.
const (
	SessionConfigOptionTypeSelect  = "select"
	SessionConfigOptionTypeBoolean = "boolean"
)

// Tool call status values.
const (
	ToolCallStatusPending    = "pending"
	ToolCallStatusInProgress = "in_progress"
	ToolCallStatusCompleted  = "completed"
	ToolCallStatusFailed     = "failed"
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
	ToolKindSwitch  = "switch_mode"
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
	// Questions advertises the session/request_question extension. It is the
	// only signal that makes the agent route ask_user_question to the client UI.
	Questions bool `json:"questions,omitempty"`
}

// FileSystemCapabilities describes client fs/* support.
type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// AgentCapabilities is the MVP agent capability advertisement.
type AgentCapabilities struct {
	LoadSession         bool                 `json:"loadSession,omitempty"`
	PromptCapabilities  *PromptCapabilities  `json:"promptCapabilities,omitempty"`
	MCPCapabilities     *MCPCapabilities     `json:"mcpCapabilities,omitempty"`
	SessionCapabilities *SessionCapabilities `json:"sessionCapabilities,omitempty"`
	Auth                json.RawMessage      `json:"auth,omitempty"`
}

// SessionCapabilities advertises the session-management methods this agent
// implements. Each field must match a real backend implementation: the Server
// only sets the ones the backend actually satisfies, so clients never see a
// method advertised that would answer method-not-found.
type SessionCapabilities struct {
	List   bool `json:"list,omitempty"`
	Delete bool `json:"delete,omitempty"`
	Close  bool `json:"close,omitempty"`
	Resume bool `json:"resume,omitempty"`
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
	Cwd                   string          `json:"cwd"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
	MCPServers            json.RawMessage `json:"mcpServers,omitempty"`
}

// NewSessionResponse is the result for session/new.
type NewSessionResponse struct {
	SessionID string `json:"sessionId"`
	// ConfigOptions advertises session configuration (e.g. the model
	// selector). Select-type options are baseline for ACP v1 clients.
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	// Modes is the legacy session mode selector state. ACP v1 prefers a
	// config option with category "mode" (which we also send), but clients
	// that still read `modes` get a working selector instead of none.
	Modes *SessionModeState `json:"modes,omitempty"`
}

// SessionMode is one entry of the legacy `modes` selector state.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState is the legacy `modes` payload carried by session/new and
// session/load results and refreshed via current_mode_update.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SetSessionModeRequest is the params for the legacy session/set_mode method.
// ModeID must be one of SessionModeState.AvailableModes ids.
type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SessionListRequest is the params for session/list. Both fields are optional:
// Cwd narrows the list to sessions created for one workspace, Cursor resumes
// pagination at the position a previous response returned.
type SessionListRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
}

// SessionSummary is one row of a session/list response. The fields mirror what
// a client needs to render a history panel without loading each session.
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// SessionListResponse is the result for session/list.
type SessionListResponse struct {
	Sessions   []SessionSummary `json:"sessions"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// SessionDeleteRequest is the params for session/delete.
type SessionDeleteRequest struct {
	SessionID string `json:"sessionId"`
}

// SessionDeleteResponse is the (empty) result for session/delete. ACP v1 types
// it as an object with only optional fields; returning {} (never JSON null)
// keeps strict clients happy.
type SessionDeleteResponse struct{}

// SessionCloseRequest is the params for session/close.
type SessionCloseRequest struct {
	SessionID string `json:"sessionId"`
}

// SessionCloseResponse is the (empty) result for session/close.
type SessionCloseResponse struct{}

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

// CancelRequestParams is the params for the JSON-RPC $/cancel_request
// notification used to cancel an in-flight request by id (both directions).
type CancelRequestParams struct {
	// RequestID is the raw JSON id of the request being cancelled.
	RequestID json.RawMessage `json:"requestId,omitempty"`
}

// SessionConfigSelectOption is one choice of a select-type config option.
type SessionConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionConfigOption is one configurable session option.
//
// ACP v1 types the option as a union discriminated by "type": select options
// carry {currentValue, options}, boolean options carry {currentValue}. This
// host only emits select options (the baseline every v1 client supports);
// boolean options additionally require the client to advertise
// clientCapabilities.session.configOptions.boolean.
type SessionConfigOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Category is a UX hint; "model" marks the model selector.
	Category string `json:"category,omitempty"`
	// Type is one of the SessionConfigOptionType* constants.
	Type string `json:"type"`
	// CurrentValue is the selected value id for select options.
	CurrentValue string                      `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options,omitempty"`
}

// SessionConfigOptionValue is the value_id | boolean union carried by
// session/set_config_option. ACP encodes the scalar directly (JSON string for
// value_id, JSON bool for boolean); tagged {"type","value"} objects are also
// accepted so early client builds stay compatible.
type SessionConfigOptionValue struct {
	Type  string          `json:"type,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// UnmarshalJSON accepts "model-id", true, and {"type":"...","value":...}.
func (v *SessionConfigOptionValue) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '{' {
		// Tagged object form; aliasing avoids recursing into this method.
		type taggedValue SessionConfigOptionValue
		var aux taggedValue
		if err := json.Unmarshal(trimmed, &aux); err != nil {
			return err
		}
		*v = SessionConfigOptionValue(aux)
		return nil
	}
	// Scalar form: keep type empty and store the raw scalar.
	v.Type = ""
	v.Value = append(v.Value[:0], trimmed...)
	return nil
}

// ValueID returns the value_id payload. The type discriminator is optional
// and defaults to value_id when absent.
func (v SessionConfigOptionValue) ValueID() (string, bool) {
	if t := strings.TrimSpace(v.Type); t != "" && t != "value_id" {
		return "", false
	}
	if len(v.Value) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err != nil {
		return "", false
	}
	return s, true
}

// Boolean returns the boolean payload.
func (v SessionConfigOptionValue) Boolean() (bool, bool) {
	if t := strings.TrimSpace(v.Type); t != "" && t != "boolean" {
		return false, false
	}
	if len(v.Value) == 0 {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(v.Value, &b); err != nil {
		return false, false
	}
	return b, true
}

// SetSessionConfigOptionRequest is the params for session/set_config_option.
type SetSessionConfigOptionRequest struct {
	SessionID string                   `json:"sessionId"`
	ConfigID  string                   `json:"configId"`
	Value     SessionConfigOptionValue `json:"value"`
}

// SetSessionConfigOptionResponse is the result for session/set_config_option.
// ACP requires the full (updated) option set so clients can refresh their UI.
type SetSessionConfigOptionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

// LoadSessionRequest is the params for session/load.
// Spec: agent replays conversation via session/update, then returns null result.
type LoadSessionRequest struct {
	SessionID             string          `json:"sessionId"`
	Cwd                   string          `json:"cwd,omitempty"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
	MCPServers            json.RawMessage `json:"mcpServers,omitempty"`
}

// LoadSessionResponse is the result for session/load.
// ACP v1 defines it as an object with only optional fields (modes,
// configOptions, _meta); an empty object is the minimal valid response.
// Returning JSON null is NOT valid and breaks strict clients such as Zed.
type LoadSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	// Modes mirrors NewSessionResponse.Modes so an attached client gets the
	// legacy mode selector without another round trip.
	Modes *SessionModeState `json:"modes,omitempty"`
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

// PlanEntry is one entry of a plan session update.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"` // high | medium | low
	Status   string `json:"status"`   // pending | in_progress | completed
}

// UsageCost is the optional cost payload of usage_update.
type UsageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// AvailableCommand is one entry of the available_commands_update catalog.
// Input is the optional argument hint the client renders next to the name.
type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

// AvailableCommandInput describes the free-form argument a command accepts.
type AvailableCommandInput struct {
	Hint string `json:"hint,omitempty"`
}

// SessionUpdate is a flexible session update payload.
// sessionUpdate discriminates the variant; optional fields are filled per kind.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`

	// agent_message_chunk / user_message_chunk / agent_thought_chunk
	MessageID string        `json:"messageId,omitempty"`
	Content   *ContentBlock `json:"content,omitempty"`

	// tool_call / tool_call_update
	ToolCallID  string             `json:"toolCallId,omitempty"`
	Name        string             `json:"name,omitempty"`
	Title       string             `json:"title,omitempty"`
	Kind        string             `json:"kind,omitempty"`
	Status      string             `json:"status,omitempty"`
	RawInput    interface{}        `json:"rawInput,omitempty"`
	RawOutput   interface{}        `json:"rawOutput,omitempty"`
	Locations   []ToolCallLocation `json:"locations,omitempty"`
	ToolContent []ToolCallContent  `json:"-"` // encoded as "content" for tool updates

	// plan
	Entries []PlanEntry `json:"entries,omitempty"`

	// usage_update
	Used int64      `json:"used,omitempty"`
	Size int64      `json:"size,omitempty"`
	Cost *UsageCost `json:"cost,omitempty"`

	// config_option_update
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`

	// session_info_update (Title is shared with the tool_call variant above;
	// the custom marshaler picks the right fields per sessionUpdate kind)
	UpdatedAt string `json:"updatedAt,omitempty"`

	// available_commands_update
	AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`

	// current_mode_update (legacy modes API)
	CurrentModeID string `json:"currentModeId,omitempty"`
}

// MarshalJSON encodes SessionUpdate with the correct content field shape.
// Message chunks use a single ContentBlock; tool updates use a content array.
func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	switch u.SessionUpdate {
	case SessionUpdateAgentMessageChunk, SessionUpdateUserMessageChunk, SessionUpdateAgentThoughtChunk:
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
	case SessionUpdatePlan:
		aux := struct {
			SessionUpdate string      `json:"sessionUpdate"`
			Entries       []PlanEntry `json:"entries"`
		}{
			SessionUpdate: u.SessionUpdate,
			Entries:       u.Entries,
		}
		if aux.Entries == nil {
			aux.Entries = []PlanEntry{}
		}
		return json.Marshal(aux)
	case SessionUpdateUsage:
		aux := struct {
			SessionUpdate string     `json:"sessionUpdate"`
			Used          int64      `json:"used"`
			Size          int64      `json:"size"`
			Cost          *UsageCost `json:"cost,omitempty"`
		}{
			SessionUpdate: u.SessionUpdate,
			Used:          u.Used,
			Size:          u.Size,
			Cost:          u.Cost,
		}
		return json.Marshal(aux)
	case SessionUpdateSessionInfo:
		aux := struct {
			SessionUpdate string `json:"sessionUpdate"`
			Title         string `json:"title,omitempty"`
			UpdatedAt     string `json:"updatedAt,omitempty"`
		}{
			SessionUpdate: u.SessionUpdate,
			Title:         u.Title,
			UpdatedAt:     u.UpdatedAt,
		}
		return json.Marshal(aux)
	case SessionUpdateAvailableCommands:
		aux := struct {
			SessionUpdate     string             `json:"sessionUpdate"`
			AvailableCommands []AvailableCommand `json:"availableCommands"`
		}{
			SessionUpdate:     u.SessionUpdate,
			AvailableCommands: u.AvailableCommands,
		}
		if aux.AvailableCommands == nil {
			aux.AvailableCommands = []AvailableCommand{}
		}
		return json.Marshal(aux)
	case SessionUpdateConfigOptionUpdate:
		aux := struct {
			SessionUpdate string                `json:"sessionUpdate"`
			ConfigOptions []SessionConfigOption `json:"configOptions"`
		}{
			SessionUpdate: u.SessionUpdate,
			ConfigOptions: u.ConfigOptions,
		}
		if aux.ConfigOptions == nil {
			aux.ConfigOptions = []SessionConfigOption{}
		}
		return json.Marshal(aux)
	case SessionUpdateCurrentModeUpdate:
		aux := struct {
			SessionUpdate string `json:"sessionUpdate"`
			CurrentModeID string `json:"currentModeId"`
		}{
			SessionUpdate: u.SessionUpdate,
			CurrentModeID: u.CurrentModeID,
		}
		return json.Marshal(aux)
	case SessionUpdateToolCall, SessionUpdateToolCallUpdate:
		aux := struct {
			SessionUpdate string             `json:"sessionUpdate"`
			ToolCallID    string             `json:"toolCallId,omitempty"`
			Name          string             `json:"name,omitempty"`
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
			Name:          u.Name,
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

// RequestQuestionParams is the params for the session/request_question
// extension. Prompt/Suggestions/Required mirror the ask_user_question tool
// arguments so a client can render the same panel it shows for approvals.
type RequestQuestionParams struct {
	SessionID   string   `json:"sessionId"`
	QuestionID  string   `json:"questionId,omitempty"`
	Prompt      string   `json:"prompt"`
	Suggestions []string `json:"suggestions,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

// RequestQuestionResult is the client's answer to session/request_question.
// Declined=true means the user dismissed the panel without answering; Answer
// then stays empty and the agent must continue with its own best judgment
// instead of failing the turn.
type RequestQuestionResult struct {
	Answer   string `json:"answer,omitempty"`
	Declined bool   `json:"declined,omitempty"`
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

// UserMessageChunk builds a session update for replayed user text.
func UserMessageChunk(text string) SessionUpdate {
	block := TextContent(text)
	return SessionUpdate{
		SessionUpdate: SessionUpdateUserMessageChunk,
		Content:       &block,
	}
}

// AgentMessageChunk builds a session update for assistant text.
func AgentMessageChunk(text string) SessionUpdate {
	block := TextContent(text)
	return SessionUpdate{
		SessionUpdate: SessionUpdateAgentMessageChunk,
		Content:       &block,
	}
}

// AgentThoughtChunk builds a session update for streamed model reasoning.
// ACP clients render these as a collapsible "thinking" section instead of the
// final answer, so reasoning never has to be mixed into agent_message_chunk.
func AgentThoughtChunk(text string) SessionUpdate {
	block := TextContent(text)
	return SessionUpdate{
		SessionUpdate: SessionUpdateAgentThoughtChunk,
		Content:       &block,
	}
}

// WithMessageID tags a message/thought chunk with a client-visible message id.
// Clients group consecutive chunks that share an id into one rendered message,
// which is how they tell a new assistant turn apart from a continuation.
func WithMessageID(update SessionUpdate, messageID string) SessionUpdate {
	if strings.TrimSpace(messageID) == "" {
		return update
	}
	switch update.SessionUpdate {
	case SessionUpdateAgentMessageChunk, SessionUpdateUserMessageChunk, SessionUpdateAgentThoughtChunk:
		update.MessageID = messageID
	}
	return update
}

// UsageUpdate builds the context-window usage notification clients render as a
// context meter. used/size are token counts; both are always emitted (even at
// zero) because the spec marks them required for this variant.
func UsageUpdate(used, size int64) SessionUpdate {
	if used < 0 {
		used = 0
	}
	if size < 0 {
		size = 0
	}
	return SessionUpdate{
		SessionUpdate: SessionUpdateUsage,
		Used:          used,
		Size:          size,
	}
}

// SessionInfoUpdate builds the session metadata notification used for the
// client's session header / history list. updatedAt is an RFC3339 timestamp
// and is omitted when empty.
func SessionInfoUpdate(title, updatedAt string) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: SessionUpdateSessionInfo,
		Title:         strings.TrimSpace(title),
		UpdatedAt:     strings.TrimSpace(updatedAt),
	}
}

// AvailableCommandsUpdate builds the slash-command catalog notification.
func AvailableCommandsUpdate(commands []AvailableCommand) SessionUpdate {
	if commands == nil {
		commands = []AvailableCommand{}
	}
	return SessionUpdate{
		SessionUpdate:     SessionUpdateAvailableCommands,
		AvailableCommands: commands,
	}
}

// CurrentModeUpdate builds the legacy modes-API notification an agent sends
// when it changes mode on its own (e.g. entering plan mode). Clients that were
// offered a `modes` selector use it to refresh the active entry.
func CurrentModeUpdate(modeID string) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: SessionUpdateCurrentModeUpdate,
		CurrentModeID: modeID,
	}
}

// ToolCallStarted builds a pending tool_call update.
// name is the programmatic tool name (e.g. "read_file"); title is a
// human-readable description shown in the UI.
func ToolCallStarted(toolCallID, name, title, kind string, rawInput interface{}) SessionUpdate {
	if kind == "" {
		kind = ToolKindOther
	}
	return SessionUpdate{
		SessionUpdate: SessionUpdateToolCall,
		ToolCallID:    toolCallID,
		Name:          name,
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

// ToolCallProgressContent builds a tool_call_update with in-progress content
// (e.g. mid-call terminal stream chunks).
func ToolCallProgressContent(toolCallID, status string, content []ToolCallContent) SessionUpdate {
	if status == "" {
		status = ToolCallStatusInProgress
	}
	return SessionUpdate{
		SessionUpdate: SessionUpdateToolCallUpdate,
		ToolCallID:    toolCallID,
		Status:        status,
		ToolContent:   content,
	}
}

// ToolCallFinished builds a completed/failed tool_call_update.
func ToolCallFinished(toolCallID, name, status string, rawOutput interface{}, content []ToolCallContent) SessionUpdate {
	return SessionUpdate{
		SessionUpdate: SessionUpdateToolCallUpdate,
		ToolCallID:    toolCallID,
		Name:          name,
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
		// R6: advertise session/load so IDE hosts can resume conversations.
		// Server still returns method-not-found when the backend lacks SessionLoader.
		LoadSession: true,
		PromptCapabilities: &PromptCapabilities{
			Image:           false,
			Audio:           false,
			EmbeddedContext: false,
		},
		MCPCapabilities: &MCPCapabilities{
			HTTP: false,
			SSE:  false,
		},
		// sessionCapabilities starts empty. The Server fills in exactly the
		// methods the backend implements (SessionLister / SessionDeleter /
		// SessionCloser), so the advertisement can never promise a method
		// that would answer method-not-found.
		SessionCapabilities: &SessionCapabilities{},
		Auth:                json.RawMessage("{}"),
	}
}
