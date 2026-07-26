package acp

import (
	"context"
	"fmt"
	"strings"
	"sync"
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

// Emitter sends session/update notifications to the client.
type Emitter interface {
	SessionUpdate(sessionID string, update SessionUpdate) error
}

// PermissionRequester asks the client for tool permission.
// Hosts typically obtain this from Server.PermissionRequester().
type PermissionRequester interface {
	RequestPermission(ctx context.Context, params RequestPermissionParams) (RequestPermissionResult, error)
}

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
	authMethods := []AuthMethod{}
	return InitializeResponse{
		ProtocolVersion:   version,
		AgentCapabilities: s.opts.AgentCapabilities,
		AgentInfo:         &s.opts.AgentInfo,
		AuthMethods:       authMethods,
	}, nil
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

	emit := connEmitter{conn: s.conn}
	resp, err := s.backend.Prompt(promptCtx, req, emit)
	if err != nil {
		// Cancellation must surface as stopReason=cancelled, not a JSON-RPC error.
		if isCancelError(err) {
			return PromptResponse{StopReason: StopReasonCancelled}, nil
		}
		return nil, NewRPCError(CodeInternalError, err.Error())
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

func isCancelError(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cancel") || strings.Contains(msg, "中断") || strings.Contains(msg, "interrupt")
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
	case "delete":
		return ToolKindDelete
	case "move":
		return ToolKindMove
	default:
		return ToolKindOther
	}
}
