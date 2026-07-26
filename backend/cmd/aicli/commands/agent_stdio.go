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
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
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
	server := acp.NewServer(conn, host, acp.ServerOptions{
		AgentInfo: acp.Implementation{
			Name:    "aicli",
			Title:   "AICLI",
			Version: buildinfo.Backend().Version,
		},
		AgentCapabilities: acp.DefaultAgentCapabilities(),
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
	cfg    *config.Config
	opts   *agentStdioOptions
	mu     sync.Mutex
	sess   map[string]*acpHostSession
	perm   acp.PermissionRequester
	closed bool
}

type acpHostSession struct {
	id          string
	chat        *ChatSession
	cleanup     func()
	sessionMgr  *runtimechat.SessionManager
	bridge      *acpEventBridge
	mu          sync.Mutex
	prompting   bool
	finalError  error
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
	return acp.NewSessionResponse{SessionID: hostSess.id}, nil
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

	response, err := sendMessage(chat, text)
	if err != nil {
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
		return replayACPSessionHistory(sessionID, existing, emit)
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
		return replayACPSessionHistory(sessionID, existing, emit)
	}

	hostSess, err := h.bootstrapSessionFromIDLocked(ctx, sessionID)
	if err != nil {
		return err
	}
	h.sess[hostSess.id] = hostSess
	return replayACPSessionHistory(hostSess.id, hostSess, emit)
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
	if loadExisting {
		// Durable load path: never open an empty ephemeral store for a known ID.
		execOpts.Ephemeral = false
	}
	chatOpts := buildExecChatOptions(&execOpts)
	// ACP never consumes stdin as human input.
	chatOpts.InputReader = nil
	chatOpts.NoInteractive = true
	chatOpts.JSONOutput = false
	if loadExisting {
		chatOpts.SessionIDFlag = strings.TrimSpace(sessionID)
		chatOpts.SessionFeaturesRequested = true
	}
	// Headless ACP: resolve folder trust before profile/plugin discovery.
	ensureProcessFolderTrust(chatOpts.TrustGrant, false)

	profileState, err := resolveChatProfileState(h.cfg, chatOpts)
	if err != nil {
		return nil, fmt.Errorf("profile resolve failed: %w", err)
	}
	applyProfileDefaultsToChatOptions(chatOpts, profileState)
	if !loadExisting {
		chatOpts.SessionFeaturesRequested = strings.TrimSpace(execOpts.SessionDir) != "" ||
			strings.TrimSpace(execOpts.SessionTitle) != "" ||
			(profileState != nil && profileState.Active())
	} else {
		// Keep features requested even if profile inactive — we must open the store.
		chatOpts.SessionFeaturesRequested = true
	}

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
	chatSession.ExecEventBridge = bridge

	// Pre-install runtime bridge hooks for approvals (Prompt re-binds emitters).
	rtBridge := ensureChatRuntimeEventBridge(chatSession)
	if rtBridge != nil {
		rtBridge.preferInteractiveApprovals = true
		rtBridge.askApproval = bridge.AskApproval
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

	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "user":
			text := strings.TrimSpace(message.Content)
			if text == "" {
				continue
			}
			if err := emit.SessionUpdate(sessionID, acp.UserMessageChunk(text)); err != nil {
				return err
			}
		case "assistant":
			if text := strings.TrimSpace(message.Content); text != "" {
				if err := emit.SessionUpdate(sessionID, acp.AgentMessageChunk(text)); err != nil {
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
				if err := emit.SessionUpdate(sessionID, acp.ToolCallStarted(callID, title, kind, rawInput)); err != nil {
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
			if err := emit.SessionUpdate(sessionID, acp.ToolCallFinished(callID, status, rawOutput, content)); err != nil {
				return err
			}
		default:
			// system / unknown roles are not part of the ACP transcript surface.
		}
	}
	return nil
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

func isACPCancelError(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cancel") ||
		strings.Contains(msg, "中断") ||
		strings.Contains(msg, "interrupt") ||
		strings.Contains(msg, "用户中断")
}
