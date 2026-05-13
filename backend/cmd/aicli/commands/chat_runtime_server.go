package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	aicliRuntimeModeLocal  = "local"
	aicliRuntimeModeServer = "server"
	aicliRuntimeModeAuto   = "auto"

	defaultAICLIRuntimeServerURL = "http://127.0.0.1:8101"

	aicliRuntimeServerEventPollInterval    = 200 * time.Millisecond
	aicliRuntimeServerEventDrainTimeout    = 8 * time.Second
	aicliRuntimeServerEventLongPollTimeout = 5 * time.Second
)

var errRuntimeServerStreamEnded = errors.New("runtime-server stream ended before completion")

func resolveAICLIRuntimeExecution(cfg *config.Config, serverFlag, modeFlag string, serverChanged, modeChanged bool) (string, string, error) {
	mode := ""
	serverURL := ""
	if cfg != nil && cfg.AICLI != nil && cfg.AICLI.Runtime != nil {
		mode = strings.ToLower(strings.TrimSpace(cfg.AICLI.Runtime.Mode))
		serverURL = strings.TrimSpace(cfg.AICLI.Runtime.ServerURL)
	}
	if modeChanged {
		mode = strings.ToLower(strings.TrimSpace(modeFlag))
	}
	if serverChanged {
		value := strings.TrimSpace(serverFlag)
		switch strings.ToLower(value) {
		case "", aicliRuntimeModeServer:
			mode = aicliRuntimeModeServer
		case aicliRuntimeModeAuto:
			mode = aicliRuntimeModeAuto
		case aicliRuntimeModeLocal, "off", "false", "0":
			mode = aicliRuntimeModeLocal
			serverURL = ""
		default:
			serverURL = value
			if mode == "" || mode == aicliRuntimeModeLocal {
				mode = aicliRuntimeModeServer
			}
		}
	}
	if mode == "" {
		mode = aicliRuntimeModeLocal
	}
	switch mode {
	case aicliRuntimeModeLocal:
		return mode, "", nil
	case aicliRuntimeModeServer, aicliRuntimeModeAuto:
		if strings.TrimSpace(serverURL) == "" {
			serverURL = defaultAICLIRuntimeServerURL
		}
		normalized, err := normalizeAICLIRuntimeServerURL(serverURL)
		if err != nil {
			return "", "", err
		}
		return mode, normalized, nil
	default:
		return "", "", fmt.Errorf("无效的 runtime mode: %s（可选值: local|server|auto）", mode)
	}
}

func normalizeAICLIRuntimeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("runtime-server 地址不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("无效的 runtime-server 地址: %s", raw)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func runtimeServerHealthCheck(ctx context.Context, serverURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("runtime-server healthz returned %s", resp.Status)
	}
	return nil
}

func prepareRuntimeServerChatPersistence(runtimeConfig *runtimecfg.RuntimeConfig, opts *chatCommandOptions) (*runtimechat.SessionManager, string, string, bool, error) {
	if opts == nil || opts.RuntimeMode == aicliRuntimeModeLocal || strings.TrimSpace(opts.RuntimeServerURL) == "" {
		return nil, "", "", false, nil
	}
	if err := runtimeServerHealthCheck(context.Background(), opts.RuntimeServerURL); err != nil {
		if opts.RuntimeMode == aicliRuntimeModeAuto {
			fmt.Fprintf(os.Stderr, "Warning: runtime-server 不可用，会话管理已回退本地模式: %v\n", err)
			opts.RuntimeMode = aicliRuntimeModeLocal
			opts.RuntimeServerURL = ""
			return nil, "", "", false, nil
		}
		return nil, "", "", false, fmt.Errorf("runtime-server 不可用: %w", err)
	}
	userID := sessionruntime.ResolveSessionUserID(sessionruntime.IdentitySource{
		CLIUserID: strings.TrimSpace(opts.SessionUserFlag),
		Config:    runtimeConfig,
		CLILocal:  true,
	})
	sessionDir := strings.TrimSpace(opts.SessionDirFlag)
	if sessionDir == "" && runtimeConfig != nil {
		sessionDir = strings.TrimSpace(runtimeConfig.Sessions.Dir)
	}
	if sessionDir == "" {
		sessionDir = resolveDefaultChatSessionDir()
	}
	return newRuntimeServerSessionManager(opts.RuntimeServerURL), userID, sessionDir, true, nil
}

type runtimeServerSessionStorage struct {
	serverURL string
	client    *http.Client
}

func newRuntimeServerSessionManager(serverURL string) *runtimechat.SessionManager {
	cfg := runtimechat.DefaultSessionManagerConfig()
	cfg.CleanupInterval = 0
	cfg.AutoArchive = false
	return runtimechat.NewSessionManager(&runtimeServerSessionStorage{
		serverURL: strings.TrimRight(strings.TrimSpace(serverURL), "/"),
		client:    http.DefaultClient,
	}, cfg)
}

func (s *runtimeServerSessionStorage) Save(ctx context.Context, session *runtimechat.Session) error {
	if session == nil {
		return runtimechat.ErrInvalidSession
	}
	payload := map[string]interface{}{
		"user_id": session.UserID,
		"title":   strings.TrimSpace(session.Metadata.Title),
	}
	var decoded struct {
		Session *runtimechat.Session `json:"session"`
	}
	if err := s.doJSON(ctx, http.MethodPost, "/api/runtime/sessions", "", payload, &decoded); err != nil {
		return err
	}
	if decoded.Session == nil {
		return fmt.Errorf("runtime-server create session returned empty session")
	}
	*session = *decoded.Session.Clone()
	return nil
}

func (s *runtimeServerSessionStorage) Load(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, runtimechat.ErrSessionNotFound
	}
	var decoded struct {
		Session *runtimechat.Session `json:"session"`
	}
	if err := s.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(sessionID), "", nil, &decoded); err != nil {
		return nil, err
	}
	if decoded.Session == nil {
		return nil, runtimechat.ErrSessionNotFound
	}
	return decoded.Session, nil
}

func (s *runtimeServerSessionStorage) Delete(ctx context.Context, sessionID string) error {
	return s.doJSON(ctx, http.MethodDelete, "/api/runtime/sessions/"+url.PathEscape(strings.TrimSpace(sessionID)), "", nil, nil)
}

func (s *runtimeServerSessionStorage) List(ctx context.Context, userID string) ([]*runtimechat.Session, error) {
	values := url.Values{}
	if strings.TrimSpace(userID) != "" {
		values.Set("user_id", strings.TrimSpace(userID))
	}
	var decoded struct {
		Sessions []*runtimechat.Session `json:"sessions"`
	}
	if err := s.doJSON(ctx, http.MethodGet, "/api/runtime/sessions", values.Encode(), nil, &decoded); err != nil {
		return nil, err
	}
	return decoded.Sessions, nil
}

func (s *runtimeServerSessionStorage) ListWithState(ctx context.Context, userID string, state runtimechat.SessionState) ([]*runtimechat.Session, error) {
	sessions, err := s.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	filtered := make([]*runtimechat.Session, 0, len(sessions))
	for _, session := range sessions {
		if session != nil && session.State == state {
			filtered = append(filtered, session)
		}
	}
	return filtered, nil
}

func (s *runtimeServerSessionStorage) ListByTags(ctx context.Context, userID string, tags []string) ([]*runtimechat.Session, error) {
	sessions, err := s.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	filtered := make([]*runtimechat.Session, 0, len(sessions))
	for _, session := range sessions {
		if sessionHasAllTags(session, tags) {
			filtered = append(filtered, session)
		}
	}
	return filtered, nil
}

func (s *runtimeServerSessionStorage) Update(ctx context.Context, session *runtimechat.Session) error {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return runtimechat.ErrInvalidSession
	}
	payload := map[string]interface{}{}
	title := strings.TrimSpace(session.Metadata.Title)
	if title != "" {
		payload["title"] = title
	}
	if session.State != "" {
		payload["state"] = string(session.State)
	}
	if len(session.Metadata.Tags) > 0 {
		payload["tags_add"] = append([]string(nil), session.Metadata.Tags...)
	}
	if len(session.Metadata.Context) > 0 {
		payload["context"] = cloneStringInterfaceMap(session.Metadata.Context)
	}
	if len(payload) == 0 {
		return nil
	}
	return s.doJSON(ctx, http.MethodPatch, "/api/runtime/sessions/"+url.PathEscape(session.ID), "", payload, nil)
}

func (s *runtimeServerSessionStorage) AddMessage(context.Context, string, interface{}) error {
	return fmt.Errorf("runtime-server session storage does not support direct message append; use runtime session commands")
}

func (s *runtimeServerSessionStorage) GetMessages(ctx context.Context, sessionID string) ([]interface{}, error) {
	session, err := s.Load(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	history := session.GetMessages()
	messages := make([]interface{}, 0, len(history))
	for index := range history {
		messages = append(messages, history[index])
	}
	return messages, nil
}

func (s *runtimeServerSessionStorage) Close(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	return s.doJSON(ctx, http.MethodPost, "/api/runtime/sessions/"+url.PathEscape(sessionID)+"/close", "", nil, nil)
}

func (s *runtimeServerSessionStorage) Cleanup(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (s *runtimeServerSessionStorage) GetStatistics(ctx context.Context, userID string) (*runtimechat.SessionStatistics, error) {
	values := url.Values{}
	if strings.TrimSpace(userID) != "" {
		values.Set("user_id", strings.TrimSpace(userID))
	}
	var decoded struct {
		Stats *runtimechat.SessionStatistics `json:"stats"`
	}
	if err := s.doJSON(ctx, http.MethodGet, "/api/runtime/sessions/stats", values.Encode(), nil, &decoded); err != nil {
		return nil, err
	}
	if decoded.Stats == nil {
		return &runtimechat.SessionStatistics{}, nil
	}
	return decoded.Stats, nil
}

func (s *runtimeServerSessionStorage) doJSON(ctx context.Context, method, path, rawQuery string, body interface{}, target interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(s.serverURL) == "" {
		return fmt.Errorf("runtime-server 地址未配置")
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	endpoint := s.serverURL + path
	if strings.TrimSpace(rawQuery) != "" {
		endpoint += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := s.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return runtimechat.ErrSessionNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("runtime-server %s %s returned %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if target == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("解析 runtime-server 响应失败: %w", err)
	}
	return nil
}

func sessionHasAllTags(session *runtimechat.Session, tags []string) bool {
	if session == nil {
		return false
	}
	if len(tags) == 0 {
		return true
	}
	existing := make(map[string]bool, len(session.Metadata.Tags))
	for _, tag := range session.Metadata.Tags {
		existing[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !existing[tag] {
			return false
		}
	}
	return true
}

func cloneStringInterfaceMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

type aicliRuntimeServerChatExecutor struct {
	serverURL            string
	toolSurfaceMu        sync.Mutex
	toolSurfaceBySession map[string]map[string]bool
}

type runtimeServerAgentChatResponse struct {
	SessionID string                 `json:"session_id"`
	AgentID   string                 `json:"agent_id"`
	Result    map[string]interface{} `json:"result"`
	Source    string                 `json:"source"`
	Status    string                 `json:"status"`
	TraceID   string                 `json:"trace_id,omitempty"`
}

type runtimeServerRuntimeCommandResponse struct {
	OK      bool                      `json:"ok"`
	Pending bool                      `json:"pending"`
	State   *runtimechat.RuntimeState `json:"state,omitempty"`
	Result  map[string]interface{}    `json:"result,omitempty"`
}

type runtimeServerRuntimeEventsResponse struct {
	Events    []runtimeServerRuntimeEventView `json:"events"`
	Count     int                             `json:"count"`
	LatestSeq int64                           `json:"latest_seq"`
}

type runtimeServerRuntimeToolsResponse struct {
	SessionID string                        `json:"session_id"`
	Tools     []runtimetypes.ToolDefinition `json:"tools"`
	Count     int                           `json:"count"`
	Source    string                        `json:"source"`
}

type runtimeServerRuntimeEventView struct {
	Type      string                 `json:"type"`
	TraceID   string                 `json:"trace_id,omitempty"`
	AgentName string                 `json:"agent_name,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

func (v runtimeServerRuntimeEventView) Event() runtimeevents.Event {
	return runtimeevents.Event{
		Type:      strings.TrimSpace(v.Type),
		TraceID:   strings.TrimSpace(v.TraceID),
		AgentName: strings.TrimSpace(v.AgentName),
		SessionID: strings.TrimSpace(v.SessionID),
		ToolName:  strings.TrimSpace(v.ToolName),
		Payload:   cloneStringInterfaceMap(v.Payload),
		Timestamp: v.Timestamp,
	}
}

type runtimeServerHTTPError struct {
	Method     string
	Path       string
	Status     string
	StatusCode int
	Body       string
}

func (e *runtimeServerHTTPError) Error() string {
	if e == nil {
		return ""
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("runtime-server %s %s returned %s", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("runtime-server %s %s returned %s: %s", e.Method, e.Path, e.Status, body)
}

func newAICLIRuntimeServerChatExecutor(serverURL string) aicliChatExecutor {
	return &aicliRuntimeServerChatExecutor{serverURL: strings.TrimRight(strings.TrimSpace(serverURL), "/")}
}

func (e *aicliRuntimeServerChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if strings.TrimSpace(e.serverURL) == "" {
		return "", fmt.Errorf("runtime-server 地址未配置")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(session.ImagePaths) > 0 {
		return "", fmt.Errorf("aicli runtime-server 模式暂不支持本地图片附件，请改用本地模式或移除 --image")
	}
	output, result, err := e.executeRuntimeCommand(ctx, session, prompt)
	if err == nil {
		applyRuntimeServerUsage(session, result)
		return output, nil
	}
	if isRuntimeServerCommandUnavailable(err) {
		return e.executeAgentChat(ctx, session, prompt)
	}
	return "", err
}

func (e *aicliRuntimeServerChatExecutor) ContinueGoal(ctx context.Context, session *ChatSession) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if strings.TrimSpace(e.serverURL) == "" {
		return "", fmt.Errorf("runtime-server 地址未配置")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	output, result, err := e.executeRuntimeContinuation(ctx, session)
	if err != nil {
		return "", err
	}
	applyRuntimeServerUsage(session, result)
	return output, nil
}

func (e *aicliRuntimeServerChatExecutor) executeAgentChat(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	body := map[string]interface{}{
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"session_id":       currentRuntimeSessionID(session),
		"user_id":          strings.TrimSpace(session.SessionUserID),
		"profile":          firstNonEmptyChatValue(session.ProfileReference, session.ProfileName),
		"agent":            strings.TrimSpace(session.ProfileAgent),
		"provider":         strings.TrimSpace(session.ProviderName),
		"model":            strings.TrimSpace(session.Model),
		"reasoning_effort": strings.TrimSpace(session.ReasoningEffort),
		"workspace_path":   resolveLocalWorkspacePath(loadRuntimeToolConfig(session.Config, session), session),
		"enable_react":     !session.DisableTools,
		"stream":           false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.serverURL+"/api/agent/chat", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := session.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("runtime-server 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("runtime-server 返回 %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var decoded runtimeServerAgentChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", fmt.Errorf("解析 runtime-server 响应失败: %w", err)
	}
	output := runtimeServerResultOutput(decoded.Result)
	if strings.TrimSpace(decoded.SessionID) != "" {
		refreshChatSessionFromRuntimeServer(session, decoded.SessionID, prompt, output)
	} else {
		appendRuntimeServerFallbackTurn(session, prompt, output)
	}
	applyRuntimeServerUsage(session, decoded.Result)
	return output, nil
}

func (e *aicliRuntimeServerChatExecutor) executeRuntimeCommand(ctx context.Context, session *ChatSession, prompt string) (string, map[string]interface{}, error) {
	sessionID, err := ensureRuntimeServerSessionID(session)
	if err != nil {
		return "", nil, err
	}
	if err := syncRuntimeSessionFromChat(session); err != nil {
		return "", nil, fmt.Errorf("同步 runtime-server 会话上下文失败: %w", err)
	}
	afterSeq, err := e.currentRuntimeServerEventSeq(ctx, session, sessionID)
	if err != nil {
		return "", nil, err
	}

	bridge := ensureChatRuntimeEventBridge(session)
	if bridge != nil {
		bridge.startProcessor()
		previousApprove := bridge.approveTool
		previousAnswer := bridge.answerQuestion
		bridge.approveTool = func(ctx context.Context, eventSessionID, requestID string, allow bool) error {
			return e.approveRuntimeServerTool(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), requestID, allow)
		}
		bridge.answerQuestion = func(ctx context.Context, eventSessionID, questionID, answer string) error {
			return e.answerRuntimeServerQuestion(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), questionID, answer)
		}
		defer func() {
			bridge.approveTool = previousApprove
			bridge.answerQuestion = previousAnswer
		}()
		bridge.PrepareRunPrompt(prompt)
		bridge.BeginRun()
		defer bridge.EndRun()
	}

	commandResp, statusCode, err := e.submitRuntimeServerPrompt(ctx, session, sessionID, prompt)
	if err != nil {
		return "", nil, err
	}
	if commandResp == nil {
		commandResp = &runtimeServerRuntimeCommandResponse{}
	}
	waitForCompletion := statusCode == http.StatusAccepted || commandResp.Pending
	pollResult, pollErr := e.waitRuntimeServerEvents(ctx, session, sessionID, afterSeq, bridge, waitForCompletion)
	if pollErr != nil {
		refreshChatSessionFromRuntimeServer(session, sessionID, prompt, pollResult.Output)
		return "", commandResp.Result, humanizeActorExecutorError(session, pollErr)
	}

	output := runtimeServerResultOutput(commandResp.Result)
	if strings.TrimSpace(output) == "" {
		output = pollResult.Output
	}
	refreshChatSessionFromRuntimeServer(session, sessionID, prompt, output)
	if strings.TrimSpace(output) == "" {
		output = latestAssistantResponseText(session)
	}
	return output, commandResp.Result, nil
}

func (e *aicliRuntimeServerChatExecutor) executeRuntimeContinuation(ctx context.Context, session *ChatSession) (string, map[string]interface{}, error) {
	sessionID, err := ensureRuntimeServerSessionID(session)
	if err != nil {
		return "", nil, err
	}
	if err := syncRuntimeSessionFromChat(session); err != nil {
		return "", nil, fmt.Errorf("同步 runtime-server 会话上下文失败: %w", err)
	}
	afterSeq, err := e.currentRuntimeServerEventSeq(ctx, session, sessionID)
	if err != nil {
		return "", nil, err
	}

	bridge := ensureChatRuntimeEventBridge(session)
	if bridge != nil {
		bridge.startProcessor()
		previousApprove := bridge.approveTool
		previousAnswer := bridge.answerQuestion
		bridge.approveTool = func(ctx context.Context, eventSessionID, requestID string, allow bool) error {
			return e.approveRuntimeServerTool(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), requestID, allow)
		}
		bridge.answerQuestion = func(ctx context.Context, eventSessionID, questionID, answer string) error {
			return e.answerRuntimeServerQuestion(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), questionID, answer)
		}
		defer func() {
			bridge.approveTool = previousApprove
			bridge.answerQuestion = previousAnswer
		}()
		bridge.PrepareRunPrompt("")
		bridge.BeginRun()
		defer bridge.EndRun()
	}

	commandResp, statusCode, err := e.submitRuntimeServerContinue(ctx, session, sessionID)
	if err != nil {
		return "", nil, err
	}
	if commandResp == nil {
		commandResp = &runtimeServerRuntimeCommandResponse{}
	}
	waitForCompletion := statusCode == http.StatusAccepted || commandResp.Pending
	pollResult, pollErr := e.waitRuntimeServerEvents(ctx, session, sessionID, afterSeq, bridge, waitForCompletion)
	if pollErr != nil {
		refreshChatSessionFromRuntimeServer(session, sessionID, "", pollResult.Output)
		return "", commandResp.Result, humanizeActorExecutorError(session, pollErr)
	}

	output := runtimeServerResultOutput(commandResp.Result)
	if strings.TrimSpace(output) == "" {
		output = pollResult.Output
	}
	refreshChatSessionFromRuntimeServer(session, sessionID, "", output)
	if strings.TrimSpace(output) == "" {
		output = latestAssistantResponseText(session)
	}
	return output, commandResp.Result, nil
}

func ensureRuntimeServerSessionID(session *ChatSession) (string, error) {
	sessionID := currentRuntimeSessionID(session)
	if sessionID != "" {
		return sessionID, nil
	}
	if session == nil || session.SessionManager == nil {
		return "", fmt.Errorf("runtime session is not configured")
	}
	if err := createNewRuntimeConversation(session, ""); err != nil {
		return "", err
	}
	sessionID = currentRuntimeSessionID(session)
	if sessionID == "" {
		return "", fmt.Errorf("runtime-server 会话 ID 为空")
	}
	return sessionID, nil
}

func (e *aicliRuntimeServerChatExecutor) toolAvailable(ctx context.Context, session *ChatSession, toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	sessionID := currentRuntimeSessionID(session)
	if e == nil || toolName == "" || sessionID == "" {
		return false
	}
	names, err := e.runtimeServerToolNames(ctx, session, sessionID)
	if err != nil {
		return false
	}
	return names[toolName]
}

func (e *aicliRuntimeServerChatExecutor) runtimeServerToolNames(ctx context.Context, session *ChatSession, sessionID string) (map[string]bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if e == nil || sessionID == "" {
		return map[string]bool{}, nil
	}

	e.toolSurfaceMu.Lock()
	if e.toolSurfaceBySession != nil {
		if names, ok := e.toolSurfaceBySession[sessionID]; ok {
			e.toolSurfaceMu.Unlock()
			return cloneRuntimeServerToolNameSet(names), nil
		}
	}
	e.toolSurfaceMu.Unlock()

	tools, err := e.listRuntimeServerTools(ctx, session, sessionID)
	if err != nil {
		if isRuntimeServerToolSurfaceUnavailable(err) {
			names := map[string]bool{}
			e.storeRuntimeServerToolNames(sessionID, names)
			return names, nil
		}
		return nil, err
	}

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name != "" {
			names[name] = true
		}
	}
	e.storeRuntimeServerToolNames(sessionID, names)
	return cloneRuntimeServerToolNameSet(names), nil
}

func (e *aicliRuntimeServerChatExecutor) storeRuntimeServerToolNames(sessionID string, names map[string]bool) {
	if e == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	e.toolSurfaceMu.Lock()
	defer e.toolSurfaceMu.Unlock()
	if e.toolSurfaceBySession == nil {
		e.toolSurfaceBySession = make(map[string]map[string]bool)
	}
	e.toolSurfaceBySession[sessionID] = cloneRuntimeServerToolNameSet(names)
}

func (e *aicliRuntimeServerChatExecutor) listRuntimeServerTools(ctx context.Context, session *ChatSession, sessionID string) ([]runtimetypes.ToolDefinition, error) {
	sessionID = strings.TrimSpace(sessionID)
	if e == nil {
		return nil, fmt.Errorf("runtime-server executor is nil")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("runtime-server session id is required")
	}
	var decoded runtimeServerRuntimeToolsResponse
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodGet, runtimeServerToolsPath(sessionID), "", nil, &decoded)
	if err != nil {
		return nil, err
	}
	if decoded.Tools == nil {
		return []runtimetypes.ToolDefinition{}, nil
	}
	return decoded.Tools, nil
}

func runtimeServerToolsPath(sessionID string) string {
	return "/api/runtime/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/runtime/tools"
}

func cloneRuntimeServerToolNameSet(input map[string]bool) map[string]bool {
	if len(input) == 0 {
		return map[string]bool{}
	}
	cloned := make(map[string]bool, len(input))
	for name, ok := range input {
		cloned[name] = ok
	}
	return cloned
}

func (e *aicliRuntimeServerChatExecutor) submitRuntimeServerPrompt(ctx context.Context, session *ChatSession, sessionID, prompt string) (*runtimeServerRuntimeCommandResponse, int, error) {
	payload := map[string]interface{}{
		"type":   "submit_prompt",
		"prompt": prompt,
	}
	if runMeta := currentRunMetaForSession(session); runMeta != nil {
		payload["run_meta"] = runMeta
	}
	var decoded runtimeServerRuntimeCommandResponse
	statusCode, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerCommandPath(sessionID), "", payload, &decoded)
	if err != nil {
		return nil, statusCode, err
	}
	return &decoded, statusCode, nil
}

func (e *aicliRuntimeServerChatExecutor) submitRuntimeServerContinue(ctx context.Context, session *ChatSession, sessionID string) (*runtimeServerRuntimeCommandResponse, int, error) {
	payload := map[string]interface{}{
		"type":                  "continue",
		"prompt":                goalAutoContinuationPrompt,
		"continuation_metadata": map[string]interface{}{goalContinuationMetadataKey: true},
		"strip_metadata_keys":   []string{goalContinuationMetadataKey},
	}
	if runMeta := currentRunMetaForSession(session); runMeta != nil {
		payload["run_meta"] = runMeta
	}
	var decoded runtimeServerRuntimeCommandResponse
	statusCode, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerCommandPath(sessionID), "", payload, &decoded)
	if err != nil {
		return nil, statusCode, err
	}
	return &decoded, statusCode, nil
}

func (e *aicliRuntimeServerChatExecutor) approveRuntimeServerTool(ctx context.Context, session *ChatSession, sessionID, requestID string, allow bool) error {
	payload := map[string]interface{}{
		"type":       "approve_tool",
		"request_id": requestID,
		"allow":      allow,
	}
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerCommandPath(sessionID), "", payload, nil)
	return err
}

func (e *aicliRuntimeServerChatExecutor) answerRuntimeServerQuestion(ctx context.Context, session *ChatSession, sessionID, questionID, answer string) error {
	payload := map[string]interface{}{
		"type":        "answer_question",
		"question_id": questionID,
		"answer":      answer,
	}
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerCommandPath(sessionID), "", payload, nil)
	return err
}

func runtimeServerCommandPath(sessionID string) string {
	return "/api/runtime/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/runtime/commands"
}

type runtimeServerEventPollResult struct {
	Output    string
	Complete  bool
	EventSeq  int64
	EventErr  error
	EventSeen bool
}

func (e *aicliRuntimeServerChatExecutor) waitRuntimeServerEvents(ctx context.Context, session *ChatSession, sessionID string, afterSeq int64, bridge *chatRuntimeEventBridge, waitForCompletion bool) (runtimeServerEventPollResult, error) {
	if waitForCompletion {
		streamResult, streamErr := e.streamRuntimeServerEvents(ctx, session, sessionID, afterSeq, bridge)
		if streamErr == nil {
			return streamResult, nil
		}
		if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
			return streamResult, streamErr
		}
		if streamResult.EventErr != nil {
			return streamResult, streamResult.EventErr
		}
		if streamResult.EventSeq > afterSeq || streamResult.EventSeen {
			afterSeq = streamResult.EventSeq
		}
		if !isRuntimeServerStreamFallbackError(streamErr) {
			return streamResult, streamErr
		}
	}
	return e.pollRuntimeServerEvents(ctx, session, sessionID, afterSeq, bridge, waitForCompletion)
}

func (e *aicliRuntimeServerChatExecutor) pollRuntimeServerEvents(ctx context.Context, session *ChatSession, sessionID string, afterSeq int64, bridge *chatRuntimeEventBridge, waitForCompletion bool) (runtimeServerEventPollResult, error) {
	result := runtimeServerEventPollResult{EventSeq: afterSeq}
	for {
		waitTimeout := time.Duration(0)
		if waitForCompletion {
			waitTimeout = aicliRuntimeServerEventLongPollTimeout
		}
		events, err := e.listRuntimeServerEvents(ctx, session, sessionID, result.EventSeq, waitTimeout)
		if err != nil {
			return result, err
		}
		for _, event := range events {
			processRuntimeServerEvent(&result, event, bridge)
		}
		if bridge != nil {
			bridge.WaitForCurrentEvents(aicliRuntimeServerEventDrainTimeout)
			if runErr := bridge.RunError(); runErr != nil {
				return result, runErr
			}
		}
		if result.EventErr != nil {
			return result, result.EventErr
		}
		if result.Complete || !waitForCompletion {
			return result, nil
		}
		state, stateErr := e.getRuntimeServerState(ctx, session, sessionID)
		if stateErr != nil && !isRuntimeServerStateUnavailable(stateErr) {
			return result, stateErr
		}
		if runtimeServerStateComplete(state) {
			result.Complete = true
			return result, nil
		}
		if waitTimeout > 0 {
			continue
		}
		timer := time.NewTimer(aicliRuntimeServerEventPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

func processRuntimeServerEvent(result *runtimeServerEventPollResult, event runtimeevents.Event, bridge *chatRuntimeEventBridge) {
	if result == nil {
		return
	}
	result.EventSeen = true
	if seq := runtimeServerEventSeq(event); seq > result.EventSeq {
		result.EventSeq = seq
	}
	result.observe(event)
	if bridge != nil {
		bridge.Handle(event)
	}
}

func (r *runtimeServerEventPollResult) observe(event runtimeevents.Event) {
	if r == nil {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case runtimechat.EventAssistantMessage:
		if content := payloadStringValue(event.Payload["content"]); content != "" {
			r.Output = content
		}
	case runtimechat.EventSessionEnd:
		r.Complete = true
		success, hasSuccess := event.Payload["success"].(bool)
		if hasSuccess && !success {
			errText := payloadStringValue(event.Payload["error"])
			if errText == "" {
				errText = "runtime-server 会话执行失败"
			}
			r.EventErr = fmt.Errorf("%s", errText)
		}
	case runtimechat.EventSessionInterrupted:
		r.Complete = true
		reason := payloadStringValue(event.Payload["reason"])
		if reason == "" {
			reason = "interrupted"
		}
		r.EventErr = fmt.Errorf("runtime-server 会话已中断: %s", reason)
	}
}

func (e *aicliRuntimeServerChatExecutor) streamRuntimeServerEvents(ctx context.Context, session *ChatSession, sessionID string, afterSeq int64, bridge *chatRuntimeEventBridge) (runtimeServerEventPollResult, error) {
	result := runtimeServerEventPollResult{EventSeq: afterSeq}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(e.serverURL) == "" {
		return result, fmt.Errorf("runtime-server 地址未配置")
	}

	values := url.Values{}
	if afterSeq > 0 {
		values.Set("after", strconv.FormatInt(afterSeq, 10))
	}
	values.Set("poll_ms", strconv.FormatInt(aicliRuntimeServerEventLongPollTimeout.Milliseconds(), 10))

	path := "/api/runtime/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/runtime/stream"
	endpoint := e.serverURL + path + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "text/event-stream")

	client := http.DefaultClient
	if session != nil && session.HTTPClient != nil {
		client = session.HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("runtime-server stream 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return result, &runtimeServerHTTPError{
			Method:     http.MethodGet,
			Path:       path,
			Status:     resp.Status,
			StatusCode: resp.StatusCode,
			Body:       string(data),
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	currentEvent := ""
	dataLines := make([]string, 0, 1)
	flush := func() error {
		if len(dataLines) == 0 {
			currentEvent = ""
			return nil
		}
		eventName := strings.TrimSpace(currentEvent)
		data := strings.Join(dataLines, "\n")
		currentEvent = ""
		dataLines = dataLines[:0]
		if data == "" || data == "[DONE]" {
			return nil
		}
		if eventName == "" {
			eventName = "runtime_event"
		}
		switch eventName {
		case "runtime_event":
			event, err := decodeRuntimeServerStreamEvent(data, sessionID)
			if err != nil {
				return err
			}
			processRuntimeServerEvent(&result, event, bridge)
			if bridge != nil {
				bridge.WaitForCurrentEvents(aicliRuntimeServerEventDrainTimeout)
				if runErr := bridge.RunError(); runErr != nil {
					return runErr
				}
			}
			if result.EventErr != nil {
				return result.EventErr
			}
			return nil
		case "error":
			return decodeRuntimeServerStreamError(data)
		default:
			return nil
		}
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return result, err
			}
			if result.Complete {
				return result, nil
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if err := flush(); err != nil {
		return result, err
	}
	if result.Complete {
		return result, nil
	}
	return result, errRuntimeServerStreamEnded
}

func decodeRuntimeServerStreamEvent(data string, sessionID string) (runtimeevents.Event, error) {
	var view runtimeServerRuntimeEventView
	if err := json.Unmarshal([]byte(data), &view); err != nil {
		return runtimeevents.Event{}, fmt.Errorf("解析 runtime-server stream 事件失败: %w", err)
	}
	event := view.Event()
	if strings.TrimSpace(event.SessionID) == "" {
		event.SessionID = strings.TrimSpace(sessionID)
	}
	return event, nil
}

func decodeRuntimeServerStreamError(data string) error {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return fmt.Errorf("runtime-server stream error: %s", strings.TrimSpace(data))
	}
	if message := payloadStringValue(payload["error"]); message != "" {
		return fmt.Errorf("runtime-server stream error: %s", message)
	}
	return fmt.Errorf("runtime-server stream error")
}

func isRuntimeServerStreamFallbackError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errRuntimeServerStreamEnded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var httpErr *runtimeServerHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusNotImplemented, http.StatusServiceUnavailable:
			return true
		default:
			return false
		}
	}
	return false
}

func (e *aicliRuntimeServerChatExecutor) currentRuntimeServerEventSeq(ctx context.Context, session *ChatSession, sessionID string) (int64, error) {
	decoded, err := e.listRuntimeServerEventsResponse(ctx, session, sessionID, 0, 1, 0)
	if err != nil {
		return 0, err
	}
	seq := decoded.LatestSeq
	for _, view := range decoded.Events {
		event := view.Event()
		if current := runtimeServerEventSeq(event); current > seq {
			seq = current
		}
	}
	return seq, nil
}

func (e *aicliRuntimeServerChatExecutor) listRuntimeServerEvents(ctx context.Context, session *ChatSession, sessionID string, afterSeq int64, waitTimeout time.Duration) ([]runtimeevents.Event, error) {
	return e.listRuntimeServerEventsWithLimit(ctx, session, sessionID, afterSeq, 100, waitTimeout)
}

func (e *aicliRuntimeServerChatExecutor) listRuntimeServerEventsWithLimit(ctx context.Context, session *ChatSession, sessionID string, afterSeq int64, limit int, waitTimeout time.Duration) ([]runtimeevents.Event, error) {
	decoded, err := e.listRuntimeServerEventsResponse(ctx, session, sessionID, afterSeq, limit, waitTimeout)
	if err != nil {
		return nil, err
	}
	events := make([]runtimeevents.Event, 0, len(decoded.Events))
	for _, view := range decoded.Events {
		event := view.Event()
		if strings.TrimSpace(event.SessionID) == "" {
			event.SessionID = strings.TrimSpace(sessionID)
		}
		events = append(events, event)
	}
	return events, nil
}

func (e *aicliRuntimeServerChatExecutor) listRuntimeServerEventsResponse(ctx context.Context, session *ChatSession, sessionID string, afterSeq int64, limit int, waitTimeout time.Duration) (*runtimeServerRuntimeEventsResponse, error) {
	values := url.Values{}
	if afterSeq > 0 {
		values.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if waitTimeout > 0 {
		values.Set("wait_ms", strconv.FormatInt(waitTimeout.Milliseconds(), 10))
	}
	var decoded runtimeServerRuntimeEventsResponse
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/runtime/events", values.Encode(), nil, &decoded)
	if err != nil {
		return nil, err
	}
	if decoded.Events == nil {
		decoded.Events = []runtimeServerRuntimeEventView{}
	}
	return &decoded, nil
}

func (e *aicliRuntimeServerChatExecutor) getRuntimeServerState(ctx context.Context, session *ChatSession, sessionID string) (*runtimechat.RuntimeState, error) {
	var decoded struct {
		State *runtimechat.RuntimeState `json:"state"`
	}
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodGet, "/api/runtime/sessions/"+url.PathEscape(strings.TrimSpace(sessionID))+"/runtime/state", "", nil, &decoded)
	if err != nil {
		return nil, err
	}
	return decoded.State, nil
}

func runtimeServerStateComplete(state *runtimechat.RuntimeState) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case runtimechat.SessionIdle, runtimechat.SessionStopped:
		return true
	default:
		return false
	}
}

func runtimeServerEventSeq(event runtimeevents.Event) int64 {
	if event.Payload == nil {
		return 0
	}
	return int64FromRuntimeServerValue(event.Payload["seq"])
}

func int64FromRuntimeServerValue(value interface{}) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func (e *aicliRuntimeServerChatExecutor) doRuntimeServerJSON(ctx context.Context, session *ChatSession, method, path, rawQuery string, body interface{}, target interface{}) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(e.serverURL) == "" {
		return 0, fmt.Errorf("runtime-server 地址未配置")
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(payload)
	}
	endpoint := e.serverURL + path
	if strings.TrimSpace(rawQuery) != "" {
		endpoint += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.DefaultClient
	if session != nil && session.HTTPClient != nil {
		client = session.HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("runtime-server 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &runtimeServerHTTPError{
			Method:     method,
			Path:       path,
			Status:     resp.Status,
			StatusCode: resp.StatusCode,
			Body:       string(data),
		}
	}
	if target == nil || len(data) == 0 {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return resp.StatusCode, fmt.Errorf("解析 runtime-server 响应失败: %w", err)
	}
	return resp.StatusCode, nil
}

func isRuntimeServerCommandUnavailable(err error) bool {
	var httpErr *runtimeServerHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusMethodNotAllowed
}

func isRuntimeServerStateUnavailable(err error) bool {
	var httpErr *runtimeServerHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusServiceUnavailable
}

func isRuntimeServerToolSurfaceUnavailable(err error) bool {
	var httpErr *runtimeServerHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch httpErr.StatusCode {
	case http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusNotAcceptable,
		http.StatusNotImplemented,
		http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

func runtimeServerResultOutput(result map[string]interface{}) string {
	if len(result) == 0 {
		return ""
	}
	if output, ok := result["output"].(string); ok {
		return output
	}
	if content, ok := result["content"].(string); ok {
		return content
	}
	return ""
}

func refreshChatSessionFromRuntimeServer(session *ChatSession, sessionID, prompt, output string) {
	if session == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if session.SessionManager != nil {
		if runtimeSession, err := session.SessionManager.Get(context.Background(), sessionID); err == nil && runtimeSession != nil {
			_ = restoreChatStateFromRuntimeSession(session, runtimeSession)
			return
		}
	}
	appendRuntimeServerFallbackTurn(session, prompt, output)
}

func appendRuntimeServerFallbackTurn(session *ChatSession, prompt, output string) {
	if session == nil {
		return
	}
	messages := cloneRuntimeMessages(session.Messages)
	if strings.TrimSpace(prompt) != "" {
		messages = append(messages, *runtimetypes.NewUserMessage(prompt))
	}
	if strings.TrimSpace(output) != "" {
		messages = append(messages, *runtimetypes.NewAssistantMessage(output))
	}
	_ = replaceRuntimeMessages(session, messages)
	warnIfChatSessionSyncFails(session, "runtime-server fallback sync", syncRuntimeSessionFromChat(session))
}

func applyRuntimeServerUsage(session *ChatSession, result map[string]interface{}) {
	if session == nil || len(result) == 0 {
		return
	}
	usage, ok := result["usage"].(map[string]interface{})
	if !ok {
		return
	}
	if total := intFromRuntimeServerUsage(usage, "total_tokens", "totalTokens", "total"); total > 0 {
		session.TokenCount = total
	}
	if promptTokens := intFromRuntimeServerUsage(usage, "prompt_tokens", "input_tokens", "inputTokens"); promptTokens > 0 {
		session.ContextTokenCount = promptTokens
	}
	if completionTokens := intFromRuntimeServerUsage(usage, "completion_tokens", "output_tokens", "outputTokens"); completionTokens > 0 && session.TokenCount == 0 {
		session.TokenCount = session.ContextTokenCount + completionTokens
	}
}

func intFromRuntimeServerUsage(values map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		switch value := values[key].(type) {
		case int:
			return value
		case int64:
			return int(value)
		case float64:
			return int(value)
		case json.Number:
			if parsed, err := value.Int64(); err == nil {
				return int(parsed)
			}
		}
	}
	return 0
}

func configureRuntimeServerChatExecutor(ctx context.Context, opts *chatCommandOptions, session *ChatSession) (bool, error) {
	if opts == nil || session == nil || opts.RuntimeMode == aicliRuntimeModeLocal {
		return false, nil
	}
	serverURL := strings.TrimSpace(opts.RuntimeServerURL)
	if serverURL == "" {
		return false, nil
	}
	if err := runtimeServerHealthCheck(ctx, serverURL); err != nil {
		if opts.RuntimeMode == aicliRuntimeModeAuto {
			fmt.Fprintf(os.Stderr, "Warning: runtime-server 不可用，已回退本地模式: %v\n", err)
			return false, nil
		}
		return false, fmt.Errorf("runtime-server 不可用: %w", err)
	}
	session.ChatExecutor = newAICLIRuntimeServerChatExecutor(serverURL)
	session.ActorFirstReady = false
	session.LocalRuntimeHost = nil
	return true, nil
}
