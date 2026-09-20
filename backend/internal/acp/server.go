package acp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
)

// SessionBackend is the host-facing surface the ACP Server calls into.
// Implementations typically bootstrap a chat session and bridge runtime events.
type SessionBackend interface {
	// NewSession creates a conversation session. cwd may be empty.
	NewSession(ctx context.Context, req NewSessionRequest) (NewSessionResponse, error)
	// Prompt runs one prompt turn and blocks until the turn completes.
	// The backend should emit SessionUpdate via the provided Emitter while running.
	Prompt(ctx context.Context, req PromptRequest, emit Emitter) (PromptResponse, error)
	// Cancel asks the backend to abort the in-flight prompt for sessionID.
	Cancel(ctx context.Context, sessionID string) error
}

// SessionLoader is an optional SessionBackend extension for session/load.
// When AgentCapabilities.LoadSession is true and the backend implements this
// interface, Server dispatches MethodSessionLoad. Spec requires replaying the
// conversation via Emitter (session/update) before returning. The JSON-RPC
// result is an (empty) LoadSessionResponse object: ACP v1 types the response as
// an object with only optional fields, and returning null makes strict clients
// fail deserialization.
type SessionLoader interface {
	LoadSession(ctx context.Context, req LoadSessionRequest, emit Emitter) error
}

// SessionResumer is an optional SessionBackend extension for session/resume.
//
// Resume and load share the same request shape, but the contract differs in the
// one place that matters: resume MUST NOT replay the conversation. A resuming
// client still holds the transcript locally (it was detached, not restarted),
// so replaying would duplicate every message in its UI. The backend therefore
// reattaches to the stored session and stays silent; the Server answers with
// the optional state the client cannot derive locally (modes / configOptions).
//
// session/resume is advertised only when the backend implements this interface,
// so an unimplemented method is never promised.
type SessionResumer interface {
	ResumeSession(ctx context.Context, req ResumeSessionRequest) error
}

// SessionConfigOptionSetter is an optional SessionBackend extension for
// session/set_config_option. Backends that expose config options (e.g. a model
// selector) implement it to apply the change; the response must carry the full
// updated option set so the client can refresh its UI.
type SessionConfigOptionSetter interface {
	SetSessionConfigOption(ctx context.Context, req SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error)
}

// SessionConfigOptionProvider is an optional SessionBackend extension used to
// attach config options to a session/load response. session/new carries them
// inline in NewSessionResponse; session/load has no payload of its own, so the
// Server asks the backend once the history replay succeeds.
type SessionConfigOptionProvider interface {
	SessionConfigOptions(ctx context.Context, sessionID string) ([]SessionConfigOption, error)
}

// SessionModeSetter is an optional SessionBackend extension for the legacy
// session/set_mode method. ACP v1 supersedes session modes with a config
// option whose category is "mode", so backends that already implement
// SessionConfigOptionSetter may also implement this to keep clients driving
// the legacy selector working. When the backend does not implement it the
// Server answers method-not-found instead of pretending the switch happened.
type SessionModeSetter interface {
	SetSessionMode(ctx context.Context, req SetSessionModeRequest) (SetSessionModeResponse, error)
}

// SetSessionModeResponse is the result for session/set_mode. ACP v1 types it
// as an object with only optional fields; returning an empty object (never
// JSON null) keeps strict clients happy.
type SetSessionModeResponse struct{}

// SessionEmitterAware is an optional SessionBackend extension. The Server
// hands the backend an Emitter it may use outside a prompt turn, so an
// out-of-band state change (a config option or legacy mode switch applied
// while no prompt is running) can be pushed to the client as a session/update
// instead of leaving the client's pickers stale until the next turn.
type SessionEmitterAware interface {
	SetSessionEmitter(emit Emitter)
}

// SessionModeProvider is an optional SessionBackend extension used to attach
// the legacy modes state to a session/load response. session/new carries it
// inline in NewSessionResponse; session/load has no payload of its own, so the
// Server asks the backend once the history replay succeeds.
type SessionModeProvider interface {
	SessionModes(ctx context.Context, sessionID string) (*SessionModeState, error)
}

// SessionLister is an optional SessionBackend extension for session/list. The
// Server advertises sessionCapabilities.list only when the backend implements
// it, so a client can trust the capability flag.
type SessionLister interface {
	ListSessions(ctx context.Context, req SessionListRequest) (SessionListResponse, error)
}

// SessionDeleter is an optional SessionBackend extension for session/delete.
// Backends must refuse to delete a session that is still live in this process
// instead of silently dropping it out from under an active prompt.
type SessionDeleter interface {
	DeleteSession(ctx context.Context, req SessionDeleteRequest) error
}

// SessionCloser is an optional SessionBackend extension for session/close. It
// releases one live session's resources while leaving its stored history
// intact (unlike session/delete).
type SessionCloser interface {
	CloseSession(ctx context.Context, req SessionCloseRequest) error
}

// Emitter sends session/update notifications to the client.
type Emitter interface {
	SessionUpdate(sessionID string, update SessionUpdate) error
}

// PermissionRequester asks the client for tool permission.
// Hosts typically obtain this from Server.PermissionRequester().
type PermissionRequester interface {
	RequestPermission(ctx context.Context, params RequestPermissionParams) (RequestPermissionResult, error)
}

// QuestionRequester asks the client to answer an ask_user_question prompt.
// It is an extension: hosts must obtain it from Server.QuestionRequester(),
// which returns nil for clients that never advertised the capability.
type QuestionRequester interface {
	RequestQuestion(ctx context.Context, params RequestQuestionParams) (RequestQuestionResult, error)
}

// QuestionRequesterAware is an optional SessionBackend extension. The Server
// hands the backend a requester so headless hosts (ACP stdio) can route
// ask_user_question prompts to the client panel instead of failing the turn.
type QuestionRequesterAware interface {
	SetQuestionRequester(requester QuestionRequester)
}

// ElicitationRequester asks the client for structured input through the
// standard ACP v1 elicitation/create method. Hosts must obtain it from
// Server.ElicitationRequester(), which returns nil for clients that never
// advertised form-mode elicitation.
type ElicitationRequester interface {
	CreateElicitation(ctx context.Context, params ElicitationRequestParams) (ElicitationResult, error)
}

// ElicitationRequesterAware is an optional SessionBackend extension, the
// standard-protocol counterpart of QuestionRequesterAware.
type ElicitationRequesterAware interface {
	SetElicitationRequester(requester ElicitationRequester)
}

// ErrClientQuestionsUnsupported reports that the connected client did not
// advertise the session/request_question extension. Hosts treat it as "no
// panel available" and fall back to their non-interactive default.
var ErrClientQuestionsUnsupported = errors.New("acp: client does not support session/request_question")

// ErrClientElicitationUnsupported reports that the connected client did not
// advertise form-mode elicitation/create.
var ErrClientElicitationUnsupported = errors.New("acp: client does not support elicitation/create (form mode)")

// ErrClientQuestionUnsupported reports that the client advertised neither
// form-mode elicitation/create nor the legacy session/request_question
// extension, so no interactive panel is available at all.
var ErrClientQuestionUnsupported = errors.New("acp: client does not support elicitation/create (form mode) or session/request_question")

// ServerOptions configures an ACP Server.
type ServerOptions struct {
	AgentInfo         Implementation
	AgentCapabilities AgentCapabilities
	// ProtocolVersion overrides the negotiated major version (default ProtocolVersion).
	ProtocolVersion int
}

// Server dispatches ACP methods over a Conn to a SessionBackend.
type Server struct {
	conn     *Conn
	backend  SessionBackend
	opts     ServerOptions
	initOnce bool
	client   *Implementation
	// clientCaps keeps the negotiated client capabilities so extension RPCs
	// (session/request_question) are only attempted with clients that
	// advertised support for them.
	// initialize runs on the Serve goroutine while backends may emit
	// out-of-band updates from their own goroutines, so the field is guarded.
	clientCapsMu sync.RWMutex
	clientCaps   ClientCapabilities

	// promptCancels maps sessionID -> *promptCancelEntry.
	// Entries use a pointer so we can CompareAndDelete without comparing funcs.
	promptCancels sync.Map
}

type promptCancelEntry struct {
	cancel context.CancelFunc
}

// NewServer builds a Server. Call Serve to start the read loop.
func NewServer(conn *Conn, backend SessionBackend, opts ServerOptions) *Server {
	if opts.ProtocolVersion == 0 {
		opts.ProtocolVersion = ProtocolVersion
	}
	if opts.AgentCapabilities.PromptCapabilities == nil &&
		opts.AgentCapabilities.MCPCapabilities == nil &&
		!opts.AgentCapabilities.LoadSession {
		// If caller left capabilities zero-value, install MVP defaults.
		// Explicit empty PromptCapabilities still counts as "set" if non-nil pointer
		// is provided; here we only replace a fully empty struct.
		opts.AgentCapabilities = DefaultAgentCapabilities()
	}
	if opts.AgentInfo.Name == "" {
		opts.AgentInfo.Name = "aicli"
	}
	s := &Server{
		conn:    conn,
		backend: backend,
		opts:    opts,
	}
	if conn != nil {
		conn.SetHandler(s.handle)
	}
	if aware, ok := backend.(SessionEmitterAware); ok && aware != nil {
		// Out-of-band updates (mode / config option changes applied between
		// turns) need an emitter the backend can use without a prompt.
		aware.SetSessionEmitter(s.SessionEmitter())
	}
	if aware, ok := backend.(QuestionRequesterAware); ok && aware != nil {
		// The client capability that gates session/request_question is only
		// known after initialize, so the injected requester re-checks it on
		// every call instead of being resolved once at construction time.
		aware.SetQuestionRequester(serverQuestionRequester{server: s})
	}
	if aware, ok := backend.(ElicitationRequesterAware); ok && aware != nil {
		// Same late-binding rationale as the question requester: the client
		// capabilities that gate elicitation/create are only known after
		// initialize.
		aware.SetElicitationRequester(serverElicitationRequester{server: s})
	}
	return s
}

// Serve runs the connection read loop until EOF or error.
func (s *Server) Serve(ctx context.Context) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("acp: server not configured")
	}
	return s.conn.Serve(ctx)
}

// PermissionRequester returns a requester bound to this server's connection.
func (s *Server) PermissionRequester() PermissionRequester {
	return permissionRequester{conn: s.conn}
}

// SessionEmitter returns an emitter bound to this server's connection for
// session/update notifications sent outside a prompt turn.
func (s *Server) SessionEmitter() Emitter {
	return serverConfigOptionEmitter{server: s}
}

// clientCapabilities returns the client capabilities negotiated during
// initialize. It is a copy, so callers cannot mutate the stored value.
func (s *Server) clientCapabilities() ClientCapabilities {
	if s == nil {
		return ClientCapabilities{}
	}
	s.clientCapsMu.RLock()
	defer s.clientCapsMu.RUnlock()
	return s.clientCaps
}

// allowsBooleanConfigOptions reports whether the client advertised
// clientCapabilities.session.configOptions.boolean. ACP makes boolean config
// options opt-in: select options are the baseline every v1 client renders, but
// a boolean option sent to a client that did not claim support may be dropped
// or rejected by its UI layer.
func (s *Server) allowsBooleanConfigOptions() bool {
	return s.clientCapabilities().SupportsBooleanConfigOptions()
}

// filterClientConfigOptions drops option kinds the negotiated client
// capabilities do not cover. Nil stays nil so existing "advertised nothing"
// handling (which marshals to an empty array) is unaffected.
func (s *Server) filterClientConfigOptions(options []SessionConfigOption) []SessionConfigOption {
	if len(options) == 0 || s.allowsBooleanConfigOptions() {
		return options
	}
	filtered := make([]SessionConfigOption, 0, len(options))
	for _, option := range options {
		if option.Type == SessionConfigOptionTypeBoolean {
			continue
		}
		filtered = append(filtered, option)
	}
	return filtered
}

// serverConfigOptionEmitter forwards session/update notifications to the
// connection while enforcing the config-option client capability. The
// capability is resolved per emission (not at construction time) because the
// backend receives its emitter before initialize has been answered.
type serverConfigOptionEmitter struct {
	server *Server
}

func (e serverConfigOptionEmitter) SessionUpdate(sessionID string, update SessionUpdate) error {
	if e.server == nil {
		return fmt.Errorf("acp: no server for session update")
	}
	if update.SessionUpdate == SessionUpdateConfigOptionUpdate && !e.server.allowsBooleanConfigOptions() {
		filtered := e.server.filterClientConfigOptions(update.ConfigOptions)
		if len(filtered) == 0 && len(update.ConfigOptions) > 0 {
			// Every advertised option was boolean and the client never claimed
			// support for the kind: emitting an empty set would wipe the
			// client's catalog, so the update is dropped instead.
			return nil
		}
		update.ConfigOptions = filtered
	}
	return connEmitter{conn: e.server.conn}.SessionUpdate(sessionID, update)
}

// SupportsQuestions reports whether the client advertised the
// session/request_question extension during initialize.
func (s *Server) SupportsQuestions() bool {
	if s == nil {
		return false
	}
	return s.clientCapabilities().SupportsQuestions()
}

// SupportsElicitation reports whether the client advertised ACP v1
// elicitation/create form mode during initialize.
func (s *Server) SupportsElicitation() bool {
	if s == nil {
		return false
	}
	return s.clientCapabilities().SupportsFormElicitation()
}

// QuestionRequester returns a requester bound to this server's connection, or
// nil when the client cannot answer questions. Returning nil (instead of a
// requester that always errors) lets hosts pick their non-interactive default
// without an extra round trip.
func (s *Server) QuestionRequester() QuestionRequester {
	if s == nil || s.conn == nil || !s.SupportsQuestions() {
		return nil
	}
	return questionRequester{conn: s.conn}
}

// ElicitationRequester returns a requester bound to this server's connection,
// or nil when the client cannot render form elicitations.
func (s *Server) ElicitationRequester() ElicitationRequester {
	if s == nil || s.conn == nil || !s.SupportsElicitation() {
		return nil
	}
	return elicitationRequester{conn: s.conn}
}

type questionRequester struct {
	conn *Conn
}

// serverQuestionRequester gates the extension on the negotiated client
// capabilities and forwards to the live connection when the client opted in.
type serverQuestionRequester struct {
	server *Server
}

func (r serverQuestionRequester) RequestQuestion(ctx context.Context, params RequestQuestionParams) (RequestQuestionResult, error) {
	if r.server == nil || !r.server.SupportsQuestions() {
		return RequestQuestionResult{}, ErrClientQuestionsUnsupported
	}
	return questionRequester{conn: r.server.conn}.RequestQuestion(ctx, params)
}

func (q questionRequester) RequestQuestion(ctx context.Context, params RequestQuestionParams) (RequestQuestionResult, error) {
	var result RequestQuestionResult
	if q.conn == nil {
		return result, fmt.Errorf("acp: no connection for question request")
	}
	if err := q.conn.Call(ctx, MethodSessionRequestQuestion, params, &result); err != nil {
		return result, err
	}
	return result, nil
}

// serverElicitationRequester gates elicitation/create on the negotiated client
// capabilities and forwards to the live connection when the client opted in.
type serverElicitationRequester struct {
	server *Server
}

func (r serverElicitationRequester) CreateElicitation(ctx context.Context, params ElicitationRequestParams) (ElicitationResult, error) {
	if r.server == nil || !r.server.SupportsElicitation() {
		return ElicitationResult{}, ErrClientElicitationUnsupported
	}
	return elicitationRequester{conn: r.server.conn}.CreateElicitation(ctx, params)
}

type elicitationRequester struct {
	conn *Conn
}

func (e elicitationRequester) CreateElicitation(ctx context.Context, params ElicitationRequestParams) (ElicitationResult, error) {
	var result ElicitationResult
	if e.conn == nil {
		return result, fmt.Errorf("acp: no connection for elicitation request")
	}
	if params.Mode == "" {
		params.Mode = ElicitationModeForm
	}
	if err := e.conn.Call(ctx, MethodElicitationCreate, params, &result); err != nil {
		return result, err
	}
	return result, nil
}

type permissionRequester struct {
	conn *Conn
}

func (p permissionRequester) RequestPermission(ctx context.Context, params RequestPermissionParams) (RequestPermissionResult, error) {
	var result RequestPermissionResult
	if p.conn == nil {
		return result, fmt.Errorf("acp: no connection for permission request")
	}
	if err := p.conn.Call(ctx, MethodSessionRequestPermission, params, &result); err != nil {
		return result, err
	}
	return result, nil
}

type connEmitter struct {
	conn *Conn
}

func (e connEmitter) SessionUpdate(sessionID string, update SessionUpdate) error {
	if e.conn == nil {
		return fmt.Errorf("acp: no connection for session update")
	}
	return e.conn.Notify(MethodSessionUpdate, SessionUpdateNotification{
		SessionID: sessionID,
		Update:    update,
	})
}

func (s *Server) handle(ctx context.Context, msg Message) (interface{}, *RPCError) {
	method := strings.TrimSpace(msg.Method)
	switch method {
	case MethodInitialize:
		return s.handleInitialize(msg)
	case MethodSessionNew:
		return s.handleSessionNew(ctx, msg)
	case MethodSessionPrompt:
		return s.handleSessionPrompt(ctx, msg)
	case MethodSessionCancel:
		return s.handleSessionCancel(ctx, msg)
	case MethodSessionLoad:
		return s.handleSessionLoad(ctx, msg)
	case MethodSessionResume:
		return s.handleSessionResume(ctx, msg)
	case MethodSessionSetConfigOption:
		return s.handleSessionSetConfigOption(ctx, msg)
	case MethodSessionSetMode:
		return s.handleSessionSetMode(ctx, msg)
	case MethodSessionList:
		return s.handleSessionList(ctx, msg)
	case MethodSessionDelete:
		return s.handleSessionDelete(ctx, msg)
	case MethodSessionClose:
		return s.handleSessionClose(ctx, msg)
	default:
		if msg.IsNotification() {
			// Ignore unknown notifications.
			return nil, nil
		}
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+method)
	}
}

func (s *Server) handleInitialize(msg Message) (interface{}, *RPCError) {
	var req InitializeRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	version := s.opts.ProtocolVersion
	if req.ProtocolVersion > 0 && req.ProtocolVersion < version {
		// Client asked for an older major; if we only support current, return ours.
		// Spec: if agent supports requested version, echo it; else return latest supported.
		// MVP only supports ProtocolVersion.
	}
	if req.ProtocolVersion == version {
		// echo
	} else if req.ProtocolVersion > 0 {
		// Client may support newer; we respond with our latest.
	}
	s.initOnce = true
	s.client = req.ClientInfo
	s.clientCapsMu.Lock()
	s.clientCaps = req.ClientCapabilities
	s.clientCapsMu.Unlock()
	authMethods := []AuthMethod{}
	return InitializeResponse{
		ProtocolVersion:   version,
		AgentCapabilities: s.effectiveAgentCapabilities(),
		AgentInfo:         &s.opts.AgentInfo,
		AuthMethods:       authMethods,
	}, nil
}

// effectiveAgentCapabilities narrows the session-management flags to the
// methods the backend actually implements. A client that sees
// sessionCapabilities.list=true must never get method-not-found from
// session/list, so the advertisement is derived from the backend type rather
// than hard-coded in the option defaults.
func (s *Server) effectiveAgentCapabilities() AgentCapabilities {
	caps := s.opts.AgentCapabilities
	sessionCaps := SessionCapabilities{}
	if caps.SessionCapabilities != nil {
		sessionCaps = *caps.SessionCapabilities
	}
	if _, ok := s.backend.(SessionLister); !ok {
		sessionCaps.List = nil
	}
	if _, ok := s.backend.(SessionDeleter); !ok {
		sessionCaps.Delete = nil
	}
	if _, ok := s.backend.(SessionCloser); !ok {
		sessionCaps.Close = nil
	}
	if _, ok := s.backend.(SessionResumer); !ok {
		sessionCaps.Resume = nil
	}
	caps.SessionCapabilities = &sessionCaps
	return caps
}

func (s *Server) handleSessionNew(ctx context.Context, msg Message) (interface{}, *RPCError) {
	if s.backend == nil {
		return nil, NewRPCError(CodeInternalError, "no session backend")
	}
	var req NewSessionRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	resp, err := s.backend.NewSession(ctx, req)
	if err != nil {
		return nil, NewRPCError(CodeInternalError, err.Error())
	}
	if strings.TrimSpace(resp.SessionID) == "" {
		return nil, NewRPCError(CodeInternalError, "backend returned empty sessionId")
	}
	// Boolean config options are opt-in for the client; filter before they
	// reach a UI that never claimed support for the kind.
	resp.ConfigOptions = s.filterClientConfigOptions(resp.ConfigOptions)
	return resp, nil
}

func (s *Server) handleSessionPrompt(ctx context.Context, msg Message) (interface{}, *RPCError) {
	if s.backend == nil {
		return nil, NewRPCError(CodeInternalError, "no session backend")
	}
	var req PromptRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "sessionId is required")
	}
	// Per-session cancel so session/cancel can abort this prompt even when the
	// parent Serve ctx is still live. Replaces any prior in-flight cancel for
	// the same sessionId (one prompt at a time per session is the MVP model).
	promptCtx, cancel := context.WithCancel(ctx)
	entry := &promptCancelEntry{cancel: cancel}
	if prev, ok := s.promptCancels.Swap(req.SessionID, entry); ok {
		if prevEntry, ok := prev.(*promptCancelEntry); ok && prevEntry != nil && prevEntry.cancel != nil {
			prevEntry.cancel()
		}
	}
	defer func() {
		cancel()
		// Only clear the map slot if we still own it.
		s.promptCancels.CompareAndDelete(req.SessionID, entry)
	}()

	// The server-scoped emitter also enforces the config-option client
	// capability, so a backend cannot leak a boolean option to a client that
	// never advertised support for the kind.
	emit := s.SessionEmitter()
	resp, err := s.backend.Prompt(promptCtx, req, emit)
	if err != nil {
		// Cancellation must surface as stopReason=cancelled, not a JSON-RPC error.
		if isCancelError(err) {
			return PromptResponse{StopReason: StopReasonCancelled}, nil
		}
		// A prompt for a session that was closed or deleted in the meantime is a
		// resource lookup failure (-32002), not an agent bug; genuine backend
		// failures still map to -32603 through the same classifier.
		return nil, sessionLookupRPCError(err)
	}
	if strings.TrimSpace(resp.StopReason) == "" {
		resp.StopReason = StopReasonEndTurn
	}
	return resp, nil
}

func (s *Server) handleSessionCancel(ctx context.Context, msg Message) (interface{}, *RPCError) {
	if s.backend == nil {
		return nil, nil
	}
	var req CancelNotification
	if err := DecodeParams(msg, &req); err != nil {
		// Notifications cannot return errors usefully; ignore bad params.
		return nil, nil
	}
	if strings.TrimSpace(req.SessionID) != "" {
		if v, ok := s.promptCancels.Load(req.SessionID); ok {
			if entry, ok := v.(*promptCancelEntry); ok && entry != nil && entry.cancel != nil {
				entry.cancel()
			}
		}
	}
	_ = s.backend.Cancel(ctx, req.SessionID)
	return nil, nil
}

func (s *Server) handleSessionSetConfigOption(ctx context.Context, msg Message) (interface{}, *RPCError) {
	if s.backend == nil {
		return nil, NewRPCError(CodeInternalError, "no session backend")
	}
	setter, ok := s.backend.(SessionConfigOptionSetter)
	if !ok || setter == nil {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionSetConfigOption)
	}
	var req SetSessionConfigOptionRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "sessionId is required")
	}
	if strings.TrimSpace(req.ConfigID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "configId is required")
	}
	resp, err := setter.SetSessionConfigOption(ctx, req)
	if err != nil {
		if isCancelError(err) {
			return nil, NewRPCError(CodeInternalError, "set config option cancelled")
		}
		return nil, sessionLookupRPCError(err)
	}
	if resp.ConfigOptions == nil {
		// ACP types configOptions as a required array; JSON null breaks strict
		// clients (e.g. Zed deserializes it into Vec<SessionConfigOption>).
		resp.ConfigOptions = []SessionConfigOption{}
	}
	resp.ConfigOptions = s.filterClientConfigOptions(resp.ConfigOptions)
	return resp, nil
}

// handleSessionSetMode serves the legacy session/set_mode channel. It is
// advertised through the `modes` state on session/new and session/load; when
// the backend does not implement SessionModeSetter we answer method-not-found
// rather than silently accepting a mode the session will not actually use.
func (s *Server) handleSessionSetMode(ctx context.Context, msg Message) (interface{}, *RPCError) {
	if s.backend == nil {
		return nil, NewRPCError(CodeInternalError, "no session backend")
	}
	setter, ok := s.backend.(SessionModeSetter)
	if !ok || setter == nil {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionSetMode)
	}
	var req SetSessionModeRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "sessionId is required")
	}
	if strings.TrimSpace(req.ModeID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "modeId is required")
	}
	resp, err := setter.SetSessionMode(ctx, req)
	if err != nil {
		if isCancelError(err) {
			return nil, NewRPCError(CodeInternalError, "set session mode cancelled")
		}
		return nil, sessionLookupRPCError(err)
	}
	return resp, nil
}

func (s *Server) handleSessionLoad(ctx context.Context, msg Message) (interface{}, *RPCError) {
	if !s.opts.AgentCapabilities.LoadSession {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionLoad)
	}
	loader, ok := s.backend.(SessionLoader)
	if !ok || loader == nil {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionLoad)
	}
	var req LoadSessionRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "sessionId is required")
	}
	emit := s.SessionEmitter()
	if err := loader.LoadSession(ctx, req, emit); err != nil {
		if isCancelError(err) {
			return nil, NewRPCError(CodeInternalError, "session load cancelled")
		}
		return nil, sessionLookupRPCError(err)
	}
	// ACP v1 types LoadSessionResponse as an object whose fields are all
	// optional (modes / configOptions / _meta). Never return JSON null here:
	// Zed deserializes the result into a struct and fails with
	// "invalid type: null, expected struct LoadSessionResponse", which aborts
	// session attach. An empty object is the valid minimal response.
	resp := LoadSessionResponse{}
	if provider, ok := s.backend.(SessionConfigOptionProvider); ok && provider != nil {
		// Config options are a client-side nicety; failing to list them must
		// not fail the load after the history replay already succeeded.
		if options, err := provider.SessionConfigOptions(ctx, req.SessionID); err == nil {
			resp.ConfigOptions = s.filterClientConfigOptions(options)
		}
	}
	if provider, ok := s.backend.(SessionModeProvider); ok && provider != nil {
		// Same contract as config options: a missing mode list degrades the
		// picker, it must not fail an otherwise successful attach.
		if modes, err := provider.SessionModes(ctx, req.SessionID); err == nil {
			resp.Modes = modes
		}
	}
	return resp, nil
}

// handleSessionResume serves session/resume. It mirrors session/load but never
// replays history: the backend reattaches silently and the response carries
// only the state a resuming client cannot derive locally (configOptions /
// modes). Emitting the transcript here would duplicate every message in a
// client that only detached, which is exactly what ACP forbids.
func (s *Server) handleSessionResume(ctx context.Context, msg Message) (interface{}, *RPCError) {
	resumer, ok := s.backend.(SessionResumer)
	if !ok || resumer == nil {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionResume)
	}
	var req ResumeSessionRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "sessionId is required")
	}
	// Unlike session/load, ACP v1 makes cwd required for session/resume and
	// types it as an absolute path. A relative value cannot be matched against
	// the recorded workspace, so it is rejected before the backend sees it.
	if strings.TrimSpace(req.Cwd) == "" {
		return nil, NewRPCError(CodeInvalidParams, "cwd is required")
	}
	if !filepath.IsAbs(req.Cwd) {
		return nil, NewRPCError(CodeInvalidParams, "cwd must be an absolute path")
	}
	if err := resumer.ResumeSession(ctx, req); err != nil {
		if isCancelError(err) {
			return nil, NewRPCError(CodeInternalError, "session resume cancelled")
		}
		return nil, sessionLookupRPCError(err)
	}
	// Never JSON null: ACP types ResumeSessionResponse as an object with only
	// optional fields, and strict clients fail to deserialize null.
	resp := ResumeSessionResponse{}
	if provider, ok := s.backend.(SessionConfigOptionProvider); ok && provider != nil {
		// Config options are a client-side nicety; failing to list them must
		// not fail the resume after the session was already reattached.
		if options, err := provider.SessionConfigOptions(ctx, req.SessionID); err == nil {
			resp.ConfigOptions = s.filterClientConfigOptions(options)
		}
	}
	if provider, ok := s.backend.(SessionModeProvider); ok && provider != nil {
		// Same contract as config options: a missing mode list degrades the
		// picker, it must not fail an otherwise successful reattach.
		if modes, err := provider.SessionModes(ctx, req.SessionID); err == nil {
			resp.Modes = modes
		}
	}
	return resp, nil
}

// handleSessionList serves session/list. Capability and implementation are
// checked together so the method is only reachable when the backend really
// enumerates sessions.
func (s *Server) handleSessionList(ctx context.Context, msg Message) (interface{}, *RPCError) {
	lister, ok := s.backend.(SessionLister)
	if !ok || lister == nil {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionList)
	}
	var req SessionListRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	resp, err := lister.ListSessions(ctx, req)
	if err != nil {
		// A malformed cursor is a client error (the host wraps it with
		// acp.InvalidParams); a broken store stays an internal error.
		return nil, sessionLookupRPCError(err)
	}
	if resp.Sessions == nil {
		// The spec types sessions as an array; emit [] rather than null.
		resp.Sessions = []SessionSummary{}
	}
	return resp, nil
}

// handleSessionDelete serves session/delete.
func (s *Server) handleSessionDelete(ctx context.Context, msg Message) (interface{}, *RPCError) {
	deleter, ok := s.backend.(SessionDeleter)
	if !ok || deleter == nil {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionDelete)
	}
	var req SessionDeleteRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "sessionId is required")
	}
	if err := deleter.DeleteSession(ctx, req); err != nil {
		return nil, sessionLookupRPCError(err)
	}
	return SessionDeleteResponse{}, nil
}

// handleSessionClose serves session/close.
func (s *Server) handleSessionClose(ctx context.Context, msg Message) (interface{}, *RPCError) {
	closer, ok := s.backend.(SessionCloser)
	if !ok || closer == nil {
		return nil, NewRPCError(CodeMethodNotFound, "method not found: "+MethodSessionClose)
	}
	var req SessionCloseRequest
	if err := DecodeParams(msg, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, NewRPCError(CodeInvalidParams, "sessionId is required")
	}
	if err := closer.CloseSession(ctx, req); err != nil {
		return nil, sessionLookupRPCError(err)
	}
	return SessionCloseResponse{}, nil
}

// InvalidParamsError marks a backend error that should surface to clients as
// JSON-RPC invalid-params (bad sessionId/configId/value) instead of an
// internal error. Backends wrap validation failures with InvalidParams.
type InvalidParamsError struct {
	Err error
}

func (e *InvalidParamsError) Error() string {
	if e == nil || e.Err == nil {
		return "invalid params"
	}
	return e.Err.Error()
}

func (e *InvalidParamsError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// InvalidParams wraps err so sessionLookupRPCError classifies it as a client
// error (CodeInvalidParams) rather than an agent bug.
func InvalidParams(err error) error {
	if err == nil {
		return nil
	}
	return &InvalidParamsError{Err: err}
}

// NotFoundError marks a backend error as "the resource behind a well-formed
// request does not exist", which is an ACP-specific error class rather than a
// JSON-RPC parse/param failure.
type NotFoundError struct {
	Err error
}

func (e *NotFoundError) Error() string {
	if e == nil || e.Err == nil {
		return "not found"
	}
	return e.Err.Error()
}

func (e *NotFoundError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NotFound wraps err so sessionLookupRPCError classifies it as
// CodeResourceNotFound (-32002). Hosts use it when a session-scoped request
// names a session that is unknown, closed or deleted: the request itself is
// well-formed, the resource is gone.
func NotFound(err error) error {
	if err == nil {
		return nil
	}
	return &NotFoundError{Err: err}
}

// sessionLookupRPCError maps backend errors to a stable RPC error class.
// Explicit wrappers win in this order, so a host that wraps a message with
// NotFound/InvalidParams is never reclassified by substring heuristics:
//
//   - NotFoundError (and legacy "session not found" style messages) →
//     CodeResourceNotFound, so clients can tell a missing session apart from a
//     malformed request;
//   - InvalidParamsError → CodeInvalidParams (bad cursor, unsupported config
//     value, ...);
//   - everything else → CodeInternalError (an agent bug, not a client error).
func sessionLookupRPCError(err error) *RPCError {
	if err == nil {
		return nil
	}
	var notFound *NotFoundError
	if errors.As(err, &notFound) {
		return NewRPCError(CodeResourceNotFound, err.Error())
	}
	var invalid *InvalidParamsError
	if errors.As(err, &invalid) {
		return NewRPCError(CodeInvalidParams, err.Error())
	}
	msgText := strings.ToLower(err.Error())
	if strings.Contains(msgText, "not found") ||
		strings.Contains(msgText, "unknown session") ||
		strings.Contains(msgText, "no such session") {
		return NewRPCError(CodeResourceNotFound, err.Error())
	}
	return NewRPCError(CodeInternalError, err.Error())
}

// isCancelError reports whether err is a typed cancellation. Substring matching
// is intentionally avoided: diagnostic errors may contain words like
// "cancel"/"中断" without being cancellations, and silently rewriting them to
// stopReason=cancelled hides the real failure from the client.
func isCancelError(err error) bool {
	return runtimeexecution.IsCancellation(err)
}

// MapToolKind maps internal tool taxonomy kinds onto ACP tool kinds.
func MapToolKind(internalKind string) string {
	switch strings.ToLower(strings.TrimSpace(internalKind)) {
	case "read":
		return ToolKindRead
	case "search":
		return ToolKindSearch
	case "edit":
		return ToolKindEdit
	case "exec", "execute":
		return ToolKindExecute
	case "network", "fetch":
		return ToolKindFetch
	case "think":
		return ToolKindThink
	case "switch_mode", "mode":
		return ToolKindSwitch
	case "delete":
		return ToolKindDelete
	case "move":
		return ToolKindMove
	default:
		return ToolKindOther
	}
}
