package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// agentStdioOptions configures the ACP stdio host bootstrap.
// It reuses ExecOptions parsing so provider/model/profile flags stay aligned.
type agentStdioOptions struct {
	*ExecOptions
}

func runAgentStdio(cmd *cobra.Command, cfg *config.Config) error {
	if cmd == nil {
		return fmt.Errorf("agent stdio command is nil")
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	execOpts, err := parseExecOptionsNoPrompt(cmd)
	if err != nil {
		return err
	}
	// agent stdio defaults: tools on unless explicitly disabled; ephemeral on
	// unless session-dir/title requested; never treat stdin as prompt text.
	if !cmd.Flags().Changed("disable-tools") && !cmd.Flags().Changed("enable-tools") {
		execOpts.DisableTools = false
		execOpts.EnableTools = true
	}
	if !cmd.Flags().Changed("ephemeral") {
		if strings.TrimSpace(execOpts.SessionDir) == "" && strings.TrimSpace(execOpts.SessionTitle) == "" {
			execOpts.Ephemeral = true
		}
	}
	opts := &agentStdioOptions{ExecOptions: execOpts}

	if len(opts.ConfigOverrides) > 0 {
		if err := applyConfigOverrides(cfg, opts.ConfigOverrides); err != nil {
			return fmt.Errorf("config override failed: %w", err)
		}
	}
	if restoreLogger := suppressChatConsoleLogger(cfg, &chatCommandOptions{
		NoInteractive: true,
		OutputFormat:  "text",
		LogDir:        opts.LogDir,
	}); restoreLogger != nil {
		defer restoreLogger()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	host := newACPSessionHost(cfg, opts)
	defer host.Close()

	conn := acp.NewConn(os.Stdin, os.Stdout)
	// The aicli ACP host implements session/list, session/delete and
	// session/close; advertise them so clients (e.g. Zed's history panel) enable
	// the corresponding UI. The server still clears any flag whose method the
	// backend does not actually implement.
	agentCaps := acp.DefaultAgentCapabilities()
	agentCaps.SessionCapabilities = &acp.SessionCapabilities{
		List:   true,
		Delete: true,
		Close:  true,
	}
	server := acp.NewServer(conn, host, acp.ServerOptions{
		AgentInfo: acp.Implementation{
			Name:    "aicli",
			Title:   "AICLI",
			Version: buildinfo.Backend().Version,
		},
		AgentCapabilities: agentCaps,
	})
	host.SetPermissionRequester(server.PermissionRequester())

	if err := server.Serve(ctx); err != nil {
		// Clean EOF / context cancel are normal shutdowns.
		if err == context.Canceled || strings.Contains(strings.ToLower(err.Error()), "eof") {
			return nil
		}
		return err
	}
	return nil
}

// acpSessionHost implements acp.SessionBackend by bootstrapping chat sessions.
type acpSessionHost struct {
	cfg  *config.Config
	opts *agentStdioOptions
	mu   sync.Mutex
	sess map[string]*acpHostSession
	perm acp.PermissionRequester
	// emit is the connection emitter handed over by the ACP server. It is used
	// for out-of-band session/update notifications sent between turns (mode
	// and config option changes), where no prompt-scoped emitter exists.
	emit acp.Emitter
	// questionRequester is the client-bound requester for the
	// session/request_question extension. It is set by the ACP server and
	// re-checks the client capability on every call.
	questionRequester acp.QuestionRequester
	// storeMgr is the lazily opened durable store backing session/list and
	// session/delete. It is independent from per-session managers so a client
	// can browse history before creating its first session.
	storeMgr    *runtimechat.SessionManager
	storeUserID string
	closed      bool
}

type acpHostSession struct {
	id         string
	chat       *ChatSession
	cleanup    func()
	sessionMgr *runtimechat.SessionManager
	bridge     *acpEventBridge
	mu         sync.Mutex
	prompting  bool
	finalError error
}

func newACPSessionHost(cfg *config.Config, opts *agentStdioOptions) *acpSessionHost {
	return &acpSessionHost{
		cfg:  cfg,
		opts: opts,
		sess: make(map[string]*acpHostSession),
	}
}

// SetPermissionRequester wires the ACP permission RPC after the server is built.
func (h *acpSessionHost) SetPermissionRequester(req acp.PermissionRequester) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.perm = req
	for _, s := range h.sess {
		if s != nil && s.bridge != nil {
			s.bridge.SetPermissionRequester(req)
		}
	}
}

// SetSessionEmitter implements acp.SessionEmitterAware: the ACP server hands
// over a connection-scoped emitter at construction time so the host can push
// session/update notifications outside a prompt turn.
func (h *acpSessionHost) SetSessionEmitter(emit acp.Emitter) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emit = emit
}

// broadcastSessionUpdate sends an out-of-band session/update notification.
// Failing to notify must never fail the state change that triggered it: the
// client still receives the authoritative option set in the RPC response, so a
// missed notification only means a stale picker until the next refresh.
func (h *acpSessionHost) broadcastSessionUpdate(sessionID string, update acp.SessionUpdate) {
	if h == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	h.mu.Lock()
	emit := h.emit
	h.mu.Unlock()
	if emit == nil {
		return
	}
	_ = emit.SessionUpdate(sessionID, update)
}

// Close finalizes all sessions.
func (h *acpSessionHost) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id, s := range h.sess {
		h.closeSessionLocked(s)
		delete(h.sess, id)
	}
	if h.storeMgr != nil {
		h.storeMgr.Stop()
		h.storeMgr = nil
	}
}

func (h *acpSessionHost) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if h == nil {
		return acp.NewSessionResponse{}, fmt.Errorf("acp host is nil")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return acp.NewSessionResponse{}, fmt.Errorf("acp host is closed")
	}

	cwd := strings.TrimSpace(req.Cwd)
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
		if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
			return acp.NewSessionResponse{}, fmt.Errorf("invalid cwd %q: must be an existing directory", req.Cwd)
		}
		if err := os.Chdir(cwd); err != nil {
			return acp.NewSessionResponse{}, fmt.Errorf("chdir to cwd %q: %w", cwd, err)
		}
	}

	hostSess, err := h.bootstrapSessionLocked(ctx)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	h.sess[hostSess.id] = hostSess
	// Record the workspace so session/list?cwd=<workspace> can narrow the
	// history panel to this project. Best-effort: never fails session/new.
	recordACPSessionWorkspace(ctx, hostSess, acpResolveSessionWorkspace(cwd))
	// Advertise the command catalog + initial session_info without waiting for
	// a prompt: the client renders the slash-command panel from session/new.
	emitACPSessionCatalog(h.emit, hostSess.id, hostSess.chat)
	return acp.NewSessionResponse{
		SessionID: hostSess.id,
		// Advertise the model selector inline: ACP v1 session/new carries
		// configOptions, and category=model is what makes clients render the
		// model picker and route session/set_config_option back to us.
		ConfigOptions: acpConfigOptionsForChat(hostSess.chat),
		// Also carry the legacy modes state: ACP v1 supersedes it with the
		// "mode" config option above, but clients still reading `modes` get a
		// working permission-mode selector instead of none. Both channels are
		// driven from the same session state.
		Modes: acpModesForChat(hostSess.chat),
	}, nil
}

func (h *acpSessionHost) Prompt(ctx context.Context, req acp.PromptRequest, emit acp.Emitter) (acp.PromptResponse, error) {
	if h == nil {
		return acp.PromptResponse{}, fmt.Errorf("acp host is nil")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	h.mu.Lock()
	hostSess := h.sess[sessionID]
	h.mu.Unlock()
	if hostSess == nil || hostSess.chat == nil {
		return acp.PromptResponse{}, fmt.Errorf("unknown sessionId %q", sessionID)
	}

	hostSess.mu.Lock()
	if hostSess.prompting {
		hostSess.mu.Unlock()
		return acp.PromptResponse{}, fmt.Errorf("session %q already has an in-flight prompt", sessionID)
	}
	hostSess.prompting = true
	hostSess.mu.Unlock()
	defer func() {
		hostSess.mu.Lock()
		hostSess.prompting = false
		hostSess.mu.Unlock()
	}()

	text := strings.TrimSpace(acp.ExtractText(req.Prompt))
	if text == "" {
		return acp.PromptResponse{}, fmt.Errorf("prompt has no text content")
	}

	// Wire per-prompt emitter + permission requester onto the event bridge.
	if hostSess.bridge != nil {
		hostSess.bridge.BeginPrompt(sessionID, emit)
		defer hostSess.bridge.EndPrompt()
	}
	h.mu.Lock()
	perm := h.perm
	h.mu.Unlock()
	if hostSess.bridge != nil && perm != nil {
		hostSess.bridge.SetPermissionRequester(perm)
	}

	// Bind prompt context so cancel/interrupt aborts the turn.
	chat := hostSess.chat
	chat.ResetInterrupt()
	base := ctx
	if base == nil {
		base = context.Background()
	}
	base = runtimeexecution.WithCancelSource(base, "acp_prompt")
	promptCtx, cancel := context.WithCancel(base)
	defer cancel()
	chat.cancelCtx = promptCtx
	chat.cancelFunc = cancel
	// Cancel may also arrive via $/cancel_request (conn aborts the inbound
	// handler ctx) without ever calling backend.Cancel; forward that onto the
	// chat session's interrupt machinery so the in-flight stream actually
	// aborts instead of running to end_turn.
	stopInterruptWatch := context.AfterFunc(promptCtx, func() {
		chat.Interrupt()
	})
	defer stopInterruptWatch()
	// Bind the prompt context so a pending permission RPC is aborted on cancel.
	if hostSess.bridge != nil {
		hostSess.bridge.BeginPromptCtx(promptCtx)
	}

	// Ensure runtime event bridge is live and prefers ACP approvals.
	// Stdout is reserved for NDJSON; silence console writers for this turn.
	rtBridge := ensureChatRuntimeEventBridge(chat)
	if rtBridge != nil {
		silenceChatRuntimeBridgeWriters(rtBridge)
		rtBridge.preferInteractiveApprovals = true
		if hostSess.bridge != nil {
			rtBridge.askApproval = hostSess.bridge.AskApproval
		}
	}

	// An approval persisted by a previous process has no waiter here: nothing
	// would ever emit session/request_permission for it, so the actor would stay
	// in waiting_approval and this prompt would fail with the actor-ready
	// timeout. Re-ask the client (or resolve it locally under yolo) first, then
	// let the resumed run settle before submitting the new prompt.
	if reconcileACPRestoredApproval(promptCtx, hostSess, chat) {
		waitForACPRecoveredRun(promptCtx, chat)
	}

	response, err := sendMessage(chat, text)
	if err != nil {
		// Close any tool calls left open by the cancelled turn so the client
		// never sees a dangling in_progress tool_call.
		if hostSess.bridge != nil {
			hostSess.bridge.EmitCancelledToolTerminals()
		}
		hostSess.finalError = err
		if isACPCancelError(err) || chat.IsInterrupted() || promptCtx.Err() != nil {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}

	// If no streaming deltas were emitted, surface the final assistant text.
	if hostSess.bridge != nil && !hostSess.bridge.HasEmittedAssistant() {
		if trimmed := strings.TrimSpace(response); trimmed != "" {
			_ = hostSess.bridge.EmitAssistant(trimmed)
		}
	}
	// Refresh the context meter and the (possibly auto-generated) title after
	// every completed turn so the client never shows stale session metadata.
	emitACPSessionUsage(emit, sessionID, chat)
	emitACPSessionInfo(emit, sessionID, chat)
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (h *acpSessionHost) Cancel(ctx context.Context, sessionID string) error {
	_ = ctx
	if h == nil {
		return nil
	}
	h.mu.Lock()
	hostSess := h.sess[strings.TrimSpace(sessionID)]
	h.mu.Unlock()
	if hostSess == nil || hostSess.chat == nil {
		return nil
	}
	hostSess.chat.Interrupt()
	return nil
}

// LoadSession implements acp.SessionLoader (R6).
// Spec: replay conversation history via session/update, then return nil so the
// client can continue with session/prompt as if the session was never interrupted.
//
// Resolution order:
//  1. In-memory host session already attached to this process
//  2. Durable session store (when not ephemeral / session-dir available)
func (h *acpSessionHost) LoadSession(ctx context.Context, req acp.LoadSessionRequest, emit acp.Emitter) error {
	if h == nil {
		return fmt.Errorf("acp host is nil")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("acp host is closed")
	}
	existing := h.sess[sessionID]
	h.mu.Unlock()

	if existing != nil {
		if err := replayACPSessionHistory(sessionID, existing, emit); err != nil {
			return err
		}
		emitACPSessionCatalog(emit, sessionID, existing.chat)
		return nil
	}

	// Apply cwd before durable bootstrap, same as session/new.
	cwd := strings.TrimSpace(req.Cwd)
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
		if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
			return fmt.Errorf("invalid cwd %q: must be an existing directory", req.Cwd)
		}
		if err := os.Chdir(cwd); err != nil {
			return fmt.Errorf("chdir to cwd %q: %w", cwd, err)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return fmt.Errorf("acp host is closed")
	}
	// Re-check under lock in case another load raced in.
	if existing = h.sess[sessionID]; existing != nil {
		if err := replayACPSessionHistory(sessionID, existing, emit); err != nil {
			return err
		}
		emitACPSessionCatalog(emit, sessionID, existing.chat)
		return nil
	}

	hostSess, err := h.bootstrapSessionFromIDLocked(ctx, sessionID)
	if err != nil {
		return err
	}
	h.sess[hostSess.id] = hostSess
	if err := replayACPSessionHistory(hostSess.id, hostSess, emit); err != nil {
		return err
	}
	emitACPSessionCatalog(emit, hostSess.id, hostSess.chat)
	return nil
}

func (h *acpSessionHost) bootstrapSessionLocked(ctx context.Context) (*acpHostSession, error) {
	return h.bootstrapSessionWithIDLocked(ctx, "")
}

// bootstrapSessionFromIDLocked loads a durable runtime session by ID for session/load.
// Ephemeral-only hosts cannot open a disk store; callers should prefer in-memory reattach.
func (h *acpSessionHost) bootstrapSessionFromIDLocked(ctx context.Context, sessionID string) (*acpHostSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	return h.bootstrapSessionWithIDLocked(ctx, sessionID)
}

func (h *acpSessionHost) bootstrapSessionWithIDLocked(ctx context.Context, sessionID string) (*acpHostSession, error) {
	_ = ctx
	opts := h.opts
	if opts == nil || opts.ExecOptions == nil {
		return nil, fmt.Errorf("agent stdio options are nil")
	}
	loadExisting := strings.TrimSpace(sessionID) != ""

	runtimeMode, runtimeServerURL, err := resolveAICLIRuntimeExecution(
		h.cfg,
		opts.RuntimeServerFlag,
		opts.RuntimeModeFlag,
		strings.TrimSpace(opts.RuntimeServerFlag) != "",
		strings.TrimSpace(opts.RuntimeModeFlag) != "",
	)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime mode: %w", err)
	}
	opts.RuntimeMode = runtimeMode
	opts.RuntimeServerURL = runtimeServerURL

	// Clone exec options so session/load can force durable resume without mutating
	// the host-wide ephemeral defaults used by subsequent session/new calls.
	execOpts := *opts.ExecOptions
	// ACP sessions must be durable so session/load can resume them across
	// process restarts. Force non-ephemeral for both session/new and session/load.
	execOpts.Ephemeral = false
	chatOpts := buildExecChatOptions(&execOpts)
	// ACP never consumes stdin as human input.
	chatOpts.InputReader = nil
	chatOpts.NoInteractive = true
	chatOpts.JSONOutput = false
	// ACP advertises session/load and must never silently fall back to an
	// in-memory session when the durable store cannot be opened.  The regular
	// headless `exec` path intentionally treats session persistence as
	// optional, but returning an ACP sessionId before it is durable creates a
	// "ghost" session that Zed will immediately fail to load after a restart.
	chatOpts.SessionFeaturesRequested = true
	if loadExisting {
		chatOpts.SessionIDFlag = strings.TrimSpace(sessionID)
	}
	// Headless ACP: resolve folder trust before profile/plugin discovery.
	ensureProcessFolderTrust(chatOpts.TrustGrant, false)

	profileState, err := resolveChatProfileState(h.cfg, chatOpts)
	if err != nil {
		return nil, fmt.Errorf("profile resolve failed: %w", err)
	}
	applyProfileDefaultsToChatOptions(chatOpts, profileState)

	persistenceState, err := prepareExecPersistence(h.cfg, chatOpts, &execOpts, profileState)
	if err != nil {
		return nil, err
	}
	if loadExisting {
		if persistenceState == nil || persistenceState.loadedRuntimeSession == nil {
			return nil, fmt.Errorf("session %q not found", sessionID)
		}
		loadedID := strings.TrimSpace(persistenceState.loadedRuntimeSession.ID)
		if loadedID != "" && !strings.EqualFold(loadedID, strings.TrimSpace(sessionID)) {
			return nil, fmt.Errorf("session %q not found", sessionID)
		}
	}
	runtimeState, _, err := prepareChatRuntimeState(h.cfg, chatOpts, persistenceState.loadedRuntimeSession)
	if err != nil {
		return nil, fmt.Errorf("runtime config failed: %w", err)
	}
	chatSession, cleanupSession, err := bootstrapChatSession(h.cfg, chatOpts, profileState, persistenceState, runtimeState)
	if err != nil {
		return nil, fmt.Errorf("session bootstrap failed: %w", err)
	}

	// Persist the session to disk immediately so session/load can find it
	// across process restarts. Without this, a session created via session/new
	// stays in-memory (runtimeSessionUnpersisted=true) until the first prompt
	// sync, and Zed will get "session not found" if it restarts before sending
	// a prompt.
	if !loadExisting {
		if err := ensureChatRuntimeSessionPersisted(chatSession); err != nil {
			if cleanupSession != nil {
				cleanupSession()
			}
			if persistenceState.runtimeSessionManager != nil {
				persistenceState.runtimeSessionManager.Stop()
			}
			return nil, fmt.Errorf("persist ACP session/new before returning sessionId: %w", err)
		}
	}

	resolvedID := currentRuntimeSessionID(chatSession)
	if strings.TrimSpace(resolvedID) == "" {
		if loadExisting {
			resolvedID = strings.TrimSpace(sessionID)
		} else {
			resolvedID = "acp_" + generateThreadID()
		}
	}

	bridge := newACPEventBridge(resolvedID)
	if h.perm != nil {
		bridge.SetPermissionRequester(h.perm)
	}
	bridge.SetQuestionRequester(h.questionRequester)
	chatSession.ExecEventBridge = bridge

	// Pre-install runtime bridge hooks for approvals (Prompt re-binds emitters).
	rtBridge := ensureChatRuntimeEventBridge(chatSession)
	if rtBridge != nil {
		rtBridge.preferInteractiveApprovals = true
		rtBridge.askApproval = bridge.AskApproval
		// Headless ACP sessions are NoInteractive, so ask_user_question would
		// otherwise fail the turn. Route it to the client panel when the client
		// advertised the extension; the hook itself degrades when it did not.
		rtBridge.askQuestionHeadless = bridge.AskQuestion
		silenceChatRuntimeBridgeWriters(rtBridge)
	}

	hostSess := &acpHostSession{
		id:         resolvedID,
		chat:       chatSession,
		sessionMgr: persistenceState.runtimeSessionManager,
		bridge:     bridge,
		cleanup: func() {
			if chatSession != nil {
				finalizeChatSessionWithError(chatSession, nil)
			}
			if cleanupSession != nil {
				cleanupSession()
			}
			if persistenceState.runtimeSessionManager != nil {
				persistenceState.runtimeSessionManager.Stop()
			}
		},
	}
	return hostSess, nil
}

// replayACPSessionHistory emits prior turns as session/update notifications so an
// IDE host can reconstruct the transcript before the next session/prompt.
// MCPServers on load requests are ignored (not supported by this host).
func replayACPSessionHistory(sessionID string, hostSess *acpHostSession, emit acp.Emitter) error {
	if hostSess == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}
	if emit == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(hostSess.id)
	}
	messages := collectVisibleChatHistory(hostSess.chat)
	if len(messages) == 0 {
		return nil
	}
	toolNames := make(map[string]string)

	for index, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		// Replayed chunks carry a message id just like the live path so clients
		// can group transcript blocks after a session/load refresh. Prefer the
		// durable metadata id; fall back to a deterministic replay-local id.
		messageID := replayMessageID(message, index)
		switch role {
		case "user":
			text := strings.TrimSpace(message.Content)
			if text == "" {
				continue
			}
			if err := emit.SessionUpdate(sessionID, acp.WithMessageID(acp.UserMessageChunk(text), messageID)); err != nil {
				return err
			}
		case "assistant":
			if text := strings.TrimSpace(message.Content); text != "" {
				if err := emit.SessionUpdate(sessionID, acp.WithMessageID(acp.AgentMessageChunk(text), messageID)); err != nil {
					return err
				}
			}
			for _, call := range message.ToolCalls {
				callID := strings.TrimSpace(call.ID)
				if callID == "" {
					continue
				}
				title := strings.TrimSpace(call.Name)
				if title == "" {
					title = "tool"
				}
				kind := acpToolKindForName(call.Name)
				var rawInput interface{}
				if len(call.Args) > 0 {
					rawInput = call.Args
				}
				toolNames[callID] = call.Name
				if err := emit.SessionUpdate(sessionID, acp.ToolCallStarted(callID, call.Name, title, kind, rawInput)); err != nil {
					return err
				}
			}
		case "tool":
			callID := strings.TrimSpace(message.ToolCallID)
			if callID == "" {
				continue
			}
			output, toolErr := splitChatHistoryToolResult(message)
			status := acp.ToolCallStatusCompleted
			var rawOutput interface{}
			var content []acp.ToolCallContent
			if strings.TrimSpace(toolErr) != "" {
				status = acp.ToolCallStatusFailed
				rawOutput = strings.TrimSpace(toolErr)
				content = []acp.ToolCallContent{acp.TextToolContent(truncateForACP(toolErr, 4000))}
			} else {
				text := strings.TrimSpace(output)
				if text == "" {
					text = strings.TrimSpace(message.Content)
				}
				if text != "" {
					rawOutput = text
					content = []acp.ToolCallContent{acp.TextToolContent(truncateForACP(text, 4000))}
				}
			}
			if err := emit.SessionUpdate(sessionID, acp.ToolCallFinished(callID, toolNames[callID], status, rawOutput, content)); err != nil {
				return err
			}
		default:
			// system / unknown roles are not part of the ACP transcript surface.
		}
	}
	return nil
}

// replayMessageID returns the client-visible message id for a replayed history
// message. Durable metadata ids keep grouping stable across refreshes; messages
// persisted before message identity existed fall back to a deterministic id
// that stays unique within one replay pass.
func replayMessageID(message runtimetypes.Message, index int) string {
	if id := strings.TrimSpace(runtimetypes.MessageID(message)); id != "" {
		return id
	}
	return fmt.Sprintf("replay_%d", index)
}

func (h *acpSessionHost) closeSessionLocked(s *acpHostSession) {
	if s == nil {
		return
	}
	if s.chat != nil && s.chat.IsInterrupted() == false {
		// Best-effort interrupt if a prompt is still running.
		if s.prompting {
			s.chat.Interrupt()
		}
	}
	if s.cleanup != nil {
		s.cleanup()
		s.cleanup = nil
	}
}

// isACPCancelError reports whether err is a real cancellation. Detection is
// typed (context sentinels / runtime cancellation codes); error text is never
// inspected, so diagnostic errors that merely mention "cancel"/"中断" still
// surface to the client instead of being silently mapped to stopReason
// cancelled.
func isACPCancelError(err error) bool {
	return runtimeexecution.IsCancellation(err)
}
