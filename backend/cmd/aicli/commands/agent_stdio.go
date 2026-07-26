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

func (h *acpSessionHost) bootstrapSessionLocked(ctx context.Context) (*acpHostSession, error) {
	_ = ctx
	opts := h.opts
	if opts == nil || opts.ExecOptions == nil {
		return nil, fmt.Errorf("agent stdio options are nil")
	}

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

	chatOpts := buildExecChatOptions(opts.ExecOptions)
	// ACP never consumes stdin as human input.
	chatOpts.InputReader = nil
	chatOpts.NoInteractive = true
	chatOpts.JSONOutput = false

	profileState, err := resolveChatProfileState(h.cfg, chatOpts)
	if err != nil {
		return nil, fmt.Errorf("profile resolve failed: %w", err)
	}
	applyProfileDefaultsToChatOptions(chatOpts, profileState)
	chatOpts.SessionFeaturesRequested = strings.TrimSpace(opts.SessionDir) != "" ||
		strings.TrimSpace(opts.SessionTitle) != "" ||
		(profileState != nil && profileState.Active())

	persistenceState, err := prepareExecPersistence(h.cfg, chatOpts, opts.ExecOptions, profileState)
	if err != nil {
		return nil, err
	}
	runtimeState, _, err := prepareChatRuntimeState(h.cfg, chatOpts, persistenceState.loadedRuntimeSession)
	if err != nil {
		return nil, fmt.Errorf("runtime config failed: %w", err)
	}
	chatSession, cleanupSession, err := bootstrapChatSession(h.cfg, chatOpts, profileState, persistenceState, runtimeState)
	if err != nil {
		return nil, fmt.Errorf("session bootstrap failed: %w", err)
	}

	sessionID := currentRuntimeSessionID(chatSession)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "acp_" + generateThreadID()
	}

	bridge := newACPEventBridge(sessionID)
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
		id:         sessionID,
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
