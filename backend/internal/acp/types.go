package acp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	MethodSessionClose = "session/close"
	// MethodSessionResume reattaches to a stored session WITHOUT replaying its
	// history (the difference from session/load). Advertised via
	// agentCapabilities.sessionCapabilities.resume.
	MethodSessionResume = "session/resume"
	MethodCancelRequest = "$/cancel_request"
)

// Client-side methods (agent → client).
const (
	MethodSessionUpdate            = "session/update"
	MethodSessionRequestPermission = "session/request_permission"
	// MethodElicitationCreate is the ACP v1 standard method for requesting
	// structured input from the user. ask_user_question prefers it whenever the
	// client advertises form-mode elicitation; session/request_question stays as
	// a fallback extension for clients that only advertise "questions".
	MethodElicitationCreate = "elicitation/create"
	// MethodSessionRequestQuestion is the legacy aicli agent→client extension
	// used to render ask_user_question prompts in the client UI. It is gated
	// behind the "questions" client capability: clients that do not advertise it
	// never receive the call and hosts fall back to their non-interactive
	// default answer.
	MethodSessionRequestQuestion = "session/request_question"
)

// ClientCapabilityQuestions is the clientCapabilities key a client sets to true
// when it can display and answer session/request_question prompts.
const ClientCapabilityQuestions = "questions"

// Elicitation modes and response actions (ACP v1 elicitation/create).
const (
	ElicitationModeForm = "form"
	ElicitationModeURL  = "url"

	ElicitationActionAccept  = "accept"
	ElicitationActionDecline = "decline"
	ElicitationActionCancel  = "cancel"

	ElicitationPropertyTypeString = "string"
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

// Plan entry priorities and statuses (ACP plan session update).
// priority is required by the schema, so entries always carry one of the three
// enum values; status reuses the todos tool vocabulary.
const (
	PlanPriorityHigh   = "high"
	PlanPriorityMedium = "medium"
	PlanPriorityLow    = "low"

	PlanStatusPending    = "pending"
	PlanStatusInProgress = "in_progress"
	PlanStatusCompleted  = "completed"
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
	FS       *FileSystemCapabilities `json:"fs,omitempty"`
	Terminal bool                    `json:"terminal,omitempty"`
	// Elicitation advertises the ACP v1 elicitation/create modes. Form support
	// exists only when form is present and non-null ({} advertises support);
	// omitted or null means the mode is unsupported.
	Elicitation *ElicitationCapabilities `json:"elicitation,omitempty"`
	// Questions advertises the legacy session/request_question extension. It is
	// the fallback signal used when the client has no form-mode elicitation.
	Questions bool `json:"questions,omitempty"`
	// Session carries session-scoped client capabilities. Boolean config
	// options are gated on Session.ConfigOptions.Boolean: select options are
	// baseline, boolean ones are not.
	Session *ClientSessionCapabilities `json:"session,omitempty"`
	// Meta carries custom capability advertisements per ACP extensibility. The
	// legacy questions flag is also accepted here (flat, namespaced or nested)
	// so spec-conformant clients can opt in without a non-spec top-level key.
	Meta map[string]json.RawMessage `json:"_meta,omitempty"`
}

// FileSystemCapabilities describes client fs/* support.
type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// ClientSessionCapabilities is the ACP v1 `clientCapabilities.session` object.
type ClientSessionCapabilities struct {
	ConfigOptions *SessionConfigOptionsCapabilities `json:"configOptions,omitempty"`
	Meta          map[string]json.RawMessage        `json:"_meta,omitempty"`
}

// SessionConfigOptionsCapabilities advertises which session config-option
// types the client can render. Select options are baseline; boolean options
// require an explicit advertisement.
type SessionConfigOptionsCapabilities struct {
	Boolean *BooleanConfigOptionCapabilities `json:"boolean,omitempty"`
}

// BooleanConfigOptionCapabilities advertises boolean config-option rendering.
// ACP v1: supplying `{}` means the agent may include `type: "boolean"` options
// in configOptions payloads.
type BooleanConfigOptionCapabilities struct{}

// ElicitationCapabilities describes the client's elicitation/create support.
// A present non-null Form/URL pointer advertises that mode; empty structs are
// valid capability objects (ACP v1: `{}` means supported).
type ElicitationCapabilities struct {
	Form *ElicitationFormCapabilities `json:"form,omitempty"`
	URL  *ElicitationURLCapabilities  `json:"url,omitempty"`
	Meta map[string]json.RawMessage   `json:"_meta,omitempty"`
}

// ElicitationFormCapabilities advertises form-mode elicitation support.
type ElicitationFormCapabilities struct{}

// ElicitationURLCapabilities advertises URL-mode elicitation support.
type ElicitationURLCapabilities struct{}

// SupportsQuestions reports whether the client advertised the legacy
// session/request_question extension, either through the top-level "questions"
// key or through a "_meta" advertisement.
func (c ClientCapabilities) SupportsQuestions() bool {
	if c.Questions {
		return true
	}
	return metaFlag(c.Meta, ClientCapabilityQuestions)
}

// SupportsFormElicitation reports whether the client advertised ACP v1
// elicitation/create form mode. Per the ACP v1 schema, support exists only when
// "form" is present and non-null; an empty object ({}) advertises support,
// while omitted or null means the mode is unsupported.
func (c ClientCapabilities) SupportsFormElicitation() bool {
	return c.Elicitation != nil && c.Elicitation.Form != nil
}

// SupportsBooleanConfigOptions reports whether the client may be sent
// `type: "boolean"` session config options (ACP v1 client capability
// `session.configOptions.boolean`). Omitted or null at any level means the
// client does not advertise support, so agents must fall back to a select
// option instead of sending a boolean one.
func (c ClientCapabilities) SupportsBooleanConfigOptions() bool {
	return c.Session != nil && c.Session.ConfigOptions != nil && c.Session.ConfigOptions.Boolean != nil
}

// metaFlag reports whether a "_meta" map advertises a truthy value under key.
// It accepts booleans, capability objects with a truthy flag
// (enabled/supported/available) and one level of namespacing, so all of these
// opt in: {"questions":true}, {"questions":{"enabled":true}},
// {"aicli.dev":{"questions":true}}, {"aicli.dev/questions":true}.
func metaFlag(raw map[string]json.RawMessage, key string) bool {
	for k, v := range raw {
		if metaKeyMatches(k, key) {
			if truthyFlag(v) {
				return true
			}
			continue
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(v, &nested) != nil {
			continue
		}
		for nk, nv := range nested {
			if metaKeyMatches(nk, key) && truthyFlag(nv) {
				return true
			}
		}
	}
	return false
}

// metaKeyMatches reports whether a "_meta" key names the flag directly or via a
// vendor namespace ("aicli.dev/questions", "aicli.dev.questions").
func metaKeyMatches(key, flag string) bool {
	if key == flag {
		return true
	}
	if idx := strings.LastIndexAny(key, "/."); idx >= 0 {
		return key[idx+1:] == flag
	}
	return false
}

// truthyFlag accepts JSON booleans and capability objects that look like flags.
func truthyFlag(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for _, name := range []string{"enabled", "supported", "available", "questions"} {
		v, ok := obj[name]
		if !ok {
			continue
		}
		if truthyFlag(v) {
			return true
		}
	}
	return false
}

// AgentCapabilities is the MVP agent capability advertisement.
type AgentCapabilities struct {
	LoadSession         bool                 `json:"loadSession,omitempty"`
	PromptCapabilities  *PromptCapabilities  `json:"promptCapabilities,omitempty"`
	MCPCapabilities     *MCPCapabilities     `json:"mcpCapabilities,omitempty"`
	SessionCapabilities *SessionCapabilities `json:"sessionCapabilities,omitempty"`
	// Auth advertises authentication-related capabilities. ACP v1 types the
	// object and each flag inside it as capability objects, not booleans.
	Auth *AgentAuthCapabilities `json:"auth,omitempty"`
}

// AgentAuthCapabilities is the ACP v1 `agentCapabilities.auth` object. Only the
// members that are present count as advertised: a client MUST NOT call logout
// unless Logout is non-nil.
type AgentAuthCapabilities struct {
	// Logout is the `logout` capability object. Nil means the method is not
	// available, which is the case for this agent: it has no login state.
	Logout *LogoutCapabilities        `json:"logout,omitempty"`
	Meta   map[string]json.RawMessage `json:"_meta,omitempty"`
}

// LogoutCapabilities advertises the logout method. ACP v1 requires a
// capability object (`{}` means supported), never a boolean.
type LogoutCapabilities struct{}

// SessionCapabilities advertises the session-lifecycle methods this agent
// implements. ACP v1 types every entry as a capability OBJECT: the key is
// present (serialized as {}) only when the method is implemented, so a client
// never sees a method advertised that would answer method-not-found.
type SessionCapabilities struct {
	List   *SessionListCapabilities   `json:"list,omitempty"`
	Delete *SessionDeleteCapabilities `json:"delete,omitempty"`
	Resume *SessionResumeCapabilities `json:"resume,omitempty"`
	Close  *SessionCloseCapabilities  `json:"close,omitempty"`
}

// The per-method capability objects are empty structs: their presence is the
// advertisement. ACP v1 spells an advertised method as an empty object, which
// is also why these cannot be booleans.
type (
	SessionListCapabilities   struct{}
	SessionDeleteCapabilities struct{}
	SessionResumeCapabilities struct{}
	SessionCloseCapabilities  struct{}
)

// SupportsList reports whether session/list is advertised.
func (c SessionCapabilities) SupportsList() bool { return c.List != nil }

// SupportsDelete reports whether session/delete is advertised.
func (c SessionCapabilities) SupportsDelete() bool { return c.Delete != nil }

// SupportsResume reports whether session/resume is advertised.
func (c SessionCapabilities) SupportsResume() bool { return c.Resume != nil }

// SupportsClose reports whether session/close is advertised.
func (c SessionCapabilities) SupportsClose() bool { return c.Close != nil }

// SetList advertises (on) or withdraws (off) session/list.
func (c *SessionCapabilities) SetList(on bool) {
	if on {
		c.List = &SessionListCapabilities{}
		return
	}
	c.List = nil
}

// SetDelete advertises (on) or withdraws (off) session/delete.
func (c *SessionCapabilities) SetDelete(on bool) {
	if on {
		c.Delete = &SessionDeleteCapabilities{}
		return
	}
	c.Delete = nil
}

// SetResume advertises (on) or withdraws (off) session/resume.
func (c *SessionCapabilities) SetResume(on bool) {
	if on {
		c.Resume = &SessionResumeCapabilities{}
		return
	}
	c.Resume = nil
}

// SetClose advertises (on) or withdraws (off) session/close.
func (c *SessionCapabilities) SetClose(on bool) {
	if on {
		c.Close = &SessionCloseCapabilities{}
		return
	}
	c.Close = nil
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

// AuthMethod is one entry of initialize's authMethods. ACP v1 defines a tagged
// union: the default (no Type) is an "agent" method whose flow runs through
// `authenticate`; Type="terminal" tells the client to run the agent program
// interactively instead. Name is required for agent methods, so it is only
// omitted when empty (which only happens for never-sent placeholders).
//
// This agent advertises no methods: credentials come from the local provider
// configuration, so there is no login flow for a client to drive.
type AuthMethod struct {
	ID          string                     `json:"id"`
	Name        string                     `json:"name,omitempty"`
	Description string                     `json:"description,omitempty"`
	Type        string                     `json:"type,omitempty"`
	Args        []string                   `json:"args,omitempty"`
	Env         map[string]string          `json:"env,omitempty"`
	Meta        map[string]json.RawMessage `json:"_meta,omitempty"`
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
//
// Cwd is REQUIRED by ACP v1 and must be an absolute path, so it is never
// omitted: a producer that cannot recover a session's recorded workspace must
// fall back to the agent's own working directory instead of sending "".
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
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

// ValueIDValue builds the session/set_config_option payload for a value_id
// (every select-type option). Hosts use it when the agent itself applies a
// config change, e.g. a slash command carried as prompt text.
func ValueIDValue(id string) SessionConfigOptionValue {
	encoded, err := json.Marshal(id)
	if err != nil {
		// json.Marshal only fails on unsupported types; a string never does.
		encoded = []byte(`""`)
	}
	return SessionConfigOptionValue{Value: encoded}
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

// ResumeSessionRequest is the params for session/resume. It mirrors
// session/load, but the agent MUST NOT replay the conversation history before
// responding: resume reattaches a client that still owns the transcript.
type ResumeSessionRequest struct {
	SessionID             string          `json:"sessionId"`
	Cwd                   string          `json:"cwd"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
	MCPServers            json.RawMessage `json:"mcpServers,omitempty"`
}

// ResumeSessionResponse is the result for session/resume: ACP allows the
// initial modes / configOptions state and nothing else (no replay payload).
type ResumeSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
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

// ElicitationRequestParams is the params for the ACP v1 elicitation/create
// method. aicli only issues form mode with a session scope; ToolCallID is part
// of the schema for completeness.
type ElicitationRequestParams struct {
	SessionID       string                     `json:"sessionId"`
	ToolCallID      string                     `json:"toolCallId,omitempty"`
	Mode            string                     `json:"mode"`
	Message         string                     `json:"message"`
	RequestedSchema ElicitationRequestedSchema `json:"requestedSchema"`
}

// ElicitationRequestedSchema is the primitive-typed JSON Schema subset that ACP
// form elicitation accepts.
type ElicitationRequestedSchema struct {
	Type       string                               `json:"type"`
	Title      string                               `json:"title,omitempty"`
	Properties map[string]ElicitationPropertySchema `json:"properties"`
	Required   []string                             `json:"required,omitempty"`
}

// ElicitationPropertySchema describes one form field. Only the string variant
// is emitted by aicli today.
type ElicitationPropertySchema struct {
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// ElicitationResult is the client's response to elicitation/create.
type ElicitationResult struct {
	Action  string                 `json:"action"`
	Content map[string]interface{} `json:"content,omitempty"`
}

// Accepted reports whether the user accepted the elicitation.
func (r ElicitationResult) Accepted() bool {
	return r.Action == ElicitationActionAccept
}

// Answer returns the "answer" form value of an accepted elicitation. Empty
// means the user declined, cancelled, or submitted no usable text; callers
// treat that as "no answer" instead of an error.
func (r ElicitationResult) Answer() string {
	if !r.Accepted() {
		return ""
	}
	return stringifyElicitationValue(r.Content["answer"])
}

// NewFormElicitationParams builds a session-scoped form elicitation that asks a
// single free-form "answer" question. Suggestions are carried in the field
// description rather than enum/oneOf: ask_user_question treats them as hints,
// and an enum field would stop clients from submitting anything else.
func NewFormElicitationParams(sessionID, prompt string, suggestions []string, required bool) ElicitationRequestParams {
	property := ElicitationPropertySchema{
		Type:  ElicitationPropertyTypeString,
		Title: "Answer",
	}
	cleaned := make([]string, 0, len(suggestions))
	for _, s := range suggestions {
		if s = strings.TrimSpace(s); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) > 0 {
		property.Description = "Suggestions: " + strings.Join(cleaned, "; ")
	}
	params := ElicitationRequestParams{
		SessionID: strings.TrimSpace(sessionID),
		Mode:      ElicitationModeForm,
		Message:   prompt,
		RequestedSchema: ElicitationRequestedSchema{
			Type:       "object",
			Title:      "Question",
			Properties: map[string]ElicitationPropertySchema{"answer": property},
		},
	}
	if required {
		params.RequestedSchema.Required = []string{"answer"}
	}
	return params
}

// stringifyElicitationValue converts a form content value to display text.
func stringifyElicitationValue(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case []string:
		return strings.TrimSpace(strings.Join(t, ", "))
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.TrimSpace(strings.Join(parts, ", "))
	case bool, float64, int, int64:
		return strings.TrimSpace(fmt.Sprint(t))
	default:
		return ""
	}
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
	// The spec requires used <= size. Token counters can momentarily overshoot
	// the configured window (e.g. right after a model switch to a smaller
	// context), and a client that renders the raw pair would show a >100% meter,
	// so clamp the pair here, at the single wire-shaping choke point.
	if used > size {
		used = size
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

// ConfigOptionUpdate builds the out-of-band refresh a client needs when the
// agent (not the client) changed a config option: the change has no RPC
// response to carry the authoritative option set, so the full set is pushed as
// a notification. options must already contain every advertised option.
func ConfigOptionUpdate(options []SessionConfigOption) SessionUpdate {
	if options == nil {
		options = []SessionConfigOption{}
	}
	return SessionUpdate{
		SessionUpdate: SessionUpdateConfigOptionUpdate,
		ConfigOptions: options,
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

// PlanUpdate builds a plan session update: the agent's current task list.
//
// ACP replaces the client-side plan wholesale on every update, so the full list
// is always sent and no diffing is needed. Entries are normalized to the schema
// enums: blank content and unknown statuses are dropped, unknown priorities
// fall back to medium (priority is a required field). An empty list is still a
// valid update and clears the client-side plan, because MarshalJSON emits
// "entries": [] instead of omitting or nulling the field.
func PlanUpdate(entries []PlanEntry) SessionUpdate {
	normalized := make([]PlanEntry, 0, len(entries))
	for _, entry := range entries {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(entry.Status))
		switch status {
		case PlanStatusPending, PlanStatusInProgress, PlanStatusCompleted:
		default:
			continue
		}
		priority := strings.ToLower(strings.TrimSpace(entry.Priority))
		switch priority {
		case PlanPriorityHigh, PlanPriorityMedium, PlanPriorityLow:
		default:
			priority = PlanPriorityMedium
		}
		normalized = append(normalized, PlanEntry{
			Content:  content,
			Status:   status,
			Priority: priority,
		})
	}
	return SessionUpdate{
		SessionUpdate: SessionUpdatePlan,
		Entries:       normalized,
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

// PromptImage is one inbound image attachment decoded from a prompt content
// block: the raw bytes plus the declared media type.
type PromptImage struct {
	Data     []byte
	MIMEType string
	// Name / URI are best-effort labels carried by embedded resource blobs.
	Name string
	URI  string
}

// PromptContent is the agent-consumable view of a session/prompt payload.
type PromptContent struct {
	// Text joins text, resource_link and embedded text resources.
	Text string
	// Images holds image content blocks and image-typed embedded resource blobs
	// in arrival order.
	Images []PromptImage
}

// ExtractPromptContent decodes a session/prompt payload according to the
// advertised promptCapabilities contract:
//   - text / resource_link / resource(text) blocks flatten into Text;
//   - image blocks and image-typed embedded resource blobs become Images;
//   - audio blocks are rejected (the host never advertises audio);
//   - unknown block types are rejected instead of being silently dropped.
func ExtractPromptContent(blocks []ContentBlock) (PromptContent, error) {
	return extractPromptContent(blocks, true)
}

// ExtractText joins the text-bearing ContentBlocks of a prompt into a single
// string. Unsupported blocks are skipped: this is the lenient view used by
// callers that only care about the textual part of a payload.
func ExtractText(blocks []ContentBlock) string {
	content, _ := extractPromptContent(blocks, false)
	return content.Text
}

func extractPromptContent(blocks []ContentBlock, strict bool) (PromptContent, error) {
	var content PromptContent
	if len(blocks) == 0 {
		return content, nil
	}
	parts := make([]string, 0, len(blocks))
	for index, block := range blocks {
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
			text, image, err := extractEmbeddedPromptResource(block.Resource)
			if err != nil {
				if strict {
					return PromptContent{}, fmt.Errorf("prompt content block %d: %w", index, err)
				}
				continue
			}
			if text != "" {
				parts = append(parts, text)
			}
			if image != nil {
				content.Images = append(content.Images, *image)
			}
		case "image":
			image, err := decodePromptImage(block.Data, block.MIMEType, block.Name, block.URI)
			if err != nil {
				if strict {
					return PromptContent{}, fmt.Errorf("prompt content block %d: %w", index, err)
				}
				continue
			}
			content.Images = append(content.Images, *image)
		case "audio":
			if strict {
				return PromptContent{}, fmt.Errorf("prompt content block %d: audio prompts are not supported", index)
			}
		default:
			if strict {
				return PromptContent{}, fmt.Errorf("prompt content block %d: unsupported content type %q", index, block.Type)
			}
		}
	}
	content.Text = strings.Join(parts, "\n\n")
	return content, nil
}

// embeddedPromptResource mirrors the two EmbeddedResource variants of ACP v1:
// TextResourceContents (text) and BlobResourceContents (blob + mimeType).
type embeddedPromptResource struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
	Text     string `json:"text"`
	Blob     string `json:"blob"`
}

func extractEmbeddedPromptResource(raw json.RawMessage) (string, *PromptImage, error) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	var embedded embeddedPromptResource
	if err := json.Unmarshal(raw, &embedded); err != nil {
		return "", nil, fmt.Errorf("decode embedded resource: %w", err)
	}
	if strings.TrimSpace(embedded.Text) != "" {
		return embedded.Text, nil, nil
	}
	if strings.TrimSpace(embedded.Blob) != "" {
		if isPromptImageMIMEType(embedded.MIMEType) {
			image, err := decodePromptImage(embedded.Blob, embedded.MIMEType, "", embedded.URI)
			if err != nil {
				return "", nil, err
			}
			return "", image, nil
		}
		// A binary resource the agent cannot render still has to reach the
		// model as a labeled placeholder instead of vanishing.
		label := strings.TrimSpace(embedded.URI)
		if label == "" {
			label = strings.TrimSpace(embedded.MIMEType)
		}
		if label == "" {
			label = "binary"
		}
		return fmt.Sprintf("[embedded binary resource: %s]", label), nil, nil
	}
	if uri := strings.TrimSpace(embedded.URI); uri != "" {
		return uri, nil, nil
	}
	return "", nil, nil
}

// decodePromptImage validates one image payload. Data is the protocol's base64
// string; a data URL is tolerated because some clients inline the media type.
func decodePromptImage(data, mimeType, name, uri string) (*PromptImage, error) {
	payload := strings.TrimSpace(data)
	mimeType = strings.TrimSpace(mimeType)
	if payload == "" {
		return nil, fmt.Errorf("image block has no data")
	}
	if strings.HasPrefix(strings.ToLower(payload), "data:") {
		comma := strings.Index(payload, ",")
		if comma < 0 {
			return nil, fmt.Errorf("image block data URL is malformed")
		}
		header := payload[len("data:"):comma]
		if headerMime := strings.TrimSpace(strings.SplitN(header, ";", 2)[0]); headerMime != "" && mimeType == "" {
			mimeType = headerMime
		}
		payload = payload[comma+1:]
	}
	if !isPromptImageMIMEType(mimeType) {
		return nil, fmt.Errorf("image block mimeType %q is not a supported image type", mimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(stripBase64Whitespace(payload))
	if err != nil {
		return nil, fmt.Errorf("image block data is not valid base64: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("image block data is empty")
	}
	return &PromptImage{Data: decoded, MIMEType: strings.ToLower(mimeType), Name: name, URI: uri}, nil
}

func stripBase64Whitespace(payload string) string {
	if !strings.ContainsAny(payload, " \t\r\n") {
		return payload
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, payload)
}

func isPromptImageMIMEType(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if semi := strings.Index(mimeType, ";"); semi >= 0 {
		mimeType = strings.TrimSpace(mimeType[:semi])
	}
	return strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml"
}

// FullPromptCapabilities returns the prompt capabilities the aicli ACP host
// advertises: inbound image blocks are staged as local attachments for the
// turn, and embedded context (editor selection / branch diff text resources)
// is inlined into the prompt text. Audio stays off because the runtime has no
// audio input path — per the ACP contract a client must not send blocks the
// agent did not advertise.
func FullPromptCapabilities() *PromptCapabilities {
	return &PromptCapabilities{
		Image:           true,
		Audio:           false,
		EmbeddedContext: true,
	}
}

// DefaultAgentCapabilities returns the conservative capability baseline (text
// prompts only). Hosts that consume more content types override
// PromptCapabilities, e.g. with FullPromptCapabilities.
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
		// Auth is an empty capability object: the agent accepts no
		// authenticate/login state, so it must not advertise logout either.
		Auth: &AgentAuthCapabilities{},
	}
}
