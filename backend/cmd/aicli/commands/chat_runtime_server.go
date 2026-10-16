package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextreconcile"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolsurface"
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
	var health struct {
		ExecutionCore runtimechat.RuntimeCoreDescriptor `json:"execution_core"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return fmt.Errorf("runtime-server healthz 缺少可解析的执行核心声明: %w", err)
	}
	if !runtimechat.IsSessionActorRuntimeCore(health.ExecutionCore) {
		return fmt.Errorf("runtime-server 执行核心不兼容: core=%s contract=%d",
			health.ExecutionCore.Name, health.ExecutionCore.ContractVersion)
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
	// §2.2-A：观测面可加字段。旧版 runtime-server 不返回时为 nil，
	// 消费方（TUI /tools）按"字段缺失就本地补齐"的口径降级。
	Groups   []toolsurface.Group        `json:"groups"`
	Filtered []toolsurface.FilteredTool `json:"filtered"`
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

func (e *aicliRuntimeServerChatExecutor) RuntimeDescriptor() aicliRuntimeExecutorDescriptor {
	return newAICLIActorRuntimeDescriptor(aicliRuntimeTransportHTTP)
}

// RuntimeServerURL 返回当前连接的 runtime-server 基础地址（含 scheme 与 host，
// 末尾不带 /）。未配置或非 server 模式时返回空串；/debug display 用它渲染
// Observe API 的实际可访问地址。
func (e *aicliRuntimeServerChatExecutor) RuntimeServerURL() string {
	if e == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(e.serverURL), "/")
}

func (e *aicliRuntimeServerChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (execOut string, execErr error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if strings.TrimSpace(e.serverURL) == "" {
		return "", fmt.Errorf("runtime-server 地址未配置")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// mesh：一个 turn 的边界（S2）——翻转节点档案的 session.busy。
	// gateway：同一处边界驱动 attention（completed/error）。
	releaseMeshTurn := beginChatMeshTurn(session)
	defer func() { releaseMeshTurn(execErr) }()
	output, result, err := e.executeRuntimeCommand(ctx, session, prompt)
	if err != nil {
		if isRuntimeServerCommandUnavailable(err) {
			return "", fmt.Errorf("runtime-server 不支持统一 SessionActor Runtime 命令接口，请升级服务端后重试: %w", err)
		}
		return "", err
	}
	applyRuntimeServerUsage(session, result)
	return output, nil
}

func (e *aicliRuntimeServerChatExecutor) ContinueGoal(ctx context.Context, session *ChatSession) (execOut string, execErr error) {
	return e.continueGoalWithPrompt(ctx, session, goalAutoContinuationPrompt)
}

// ContinueGoalWithPrompt 用显式提示词执行一次隐藏续跑（预算耗尽后的收尾轮）。
func (e *aicliRuntimeServerChatExecutor) ContinueGoalWithPrompt(ctx context.Context, session *ChatSession, prompt string) (execOut string, execErr error) {
	return e.continueGoalWithPrompt(ctx, session, prompt)
}

func (e *aicliRuntimeServerChatExecutor) continueGoalWithPrompt(ctx context.Context, session *ChatSession, prompt string) (execOut string, execErr error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if strings.TrimSpace(e.serverURL) == "" {
		return "", fmt.Errorf("runtime-server 地址未配置")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// mesh：goal 续写同样是一个 turn（S2）。
	// gateway：同一处边界驱动 attention（completed/error）。
	releaseMeshTurn := beginChatMeshTurn(session)
	defer func() { releaseMeshTurn(execErr) }()
	output, result, err := e.executeRuntimeContinuation(ctx, session, prompt)
	if err != nil {
		return "", err
	}
	applyRuntimeServerUsage(session, result)
	return output, nil
}

// InterruptTurn 把用户中断（Esc/Ctrl+C）转发到 runtime-server 的统一命令通道
// （type=interrupt）。空 turn_id 表示「取消当前在途回合」（服务端契约，
// 见 runtimeapi.sessionRuntimeCommandRequest.TurnID）。
//
// 无在途回合时服务端返回 cancelled=false + reason=no_active_turn —— 这是正常
// 语义（例如回合刚结束、或中断打到已完成的回合），不视为错误。只有 HTTP 层
// 失败（含旧服务端不支持命令接口）才返回错误。
func (e *aicliRuntimeServerChatExecutor) InterruptTurn(ctx context.Context, session *ChatSession) error {
	if e == nil {
		return fmt.Errorf("runtime-server executor is nil")
	}
	sessionID := currentRuntimeSessionID(session)
	if strings.TrimSpace(sessionID) == "" {
		// 还没有远端会话行（未提交过 turn），自然没有可中断的在途回合。
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload := map[string]interface{}{"type": "interrupt"}
	var decoded struct {
		OK        bool   `json:"ok"`
		Cancelled bool   `json:"cancelled"`
		Reason    string `json:"reason"`
	}
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerCommandPath(sessionID), "", payload, &decoded)
	return err
}

// runtimeServerCompact*View 对齐 runtimeapi.compactCommandPayload 的 snake_case
// 契约（status/result 两层）。这里用独立结构体而非 compactruntime.Status/Result：
// 后者没有 json tag，且 payload 有意只暴露展示所需的字段。
type runtimeServerCompactStatusView struct {
	Mode              string `json:"mode"`
	Phase             string `json:"phase"`
	Reason            string `json:"reason"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	TriggerTokenLimit int    `json:"trigger_token_limit"`
	MaxContextTokens  int    `json:"max_context_tokens"`
	TokenBefore       int    `json:"token_before"`
}

type runtimeServerCompactResultView struct {
	Mode                  string                      `json:"mode"`
	Phase                 string                      `json:"phase"`
	Provider              string                      `json:"provider"`
	Model                 string                      `json:"model"`
	TriggerTokenLimit     int                         `json:"trigger_token_limit"`
	MaxContextTokens      int                         `json:"max_context_tokens"`
	TokenBefore           int                         `json:"token_before"`
	TokenAfter            int                         `json:"token_after"`
	CompactedMessages     int                         `json:"compacted_messages"`
	CheckpointIDs         []string                    `json:"checkpoint_ids"`
	UsageSource           string                      `json:"usage_source"`
	Usage                 *runtimetypes.TokenUsage    `json:"usage"`
	ReplacementHistoryLen int                         `json:"replacement_history_len"`
	Reconciliation        *runtimeServerReconcileView `json:"reconciliation"`
}

type runtimeServerReconcileView struct {
	DriftCount     int  `json:"drift_count"`
	CorrectionMade bool `json:"correction_made"`
}

// CompactSession 通过统一命令通道触发 runtime-server 侧的手动 compact
// （type=compact）。服务端返回的 status/result 会映射回本地 chatCompactReport：
// server 模式 CLI 不重建替换历史的消息体，只保真条数（HistoryMessages）。
func (e *aicliRuntimeServerChatExecutor) CompactSession(ctx context.Context, session *ChatSession, requestedMode string) (*chatCompactReport, error) {
	if e == nil {
		return nil, fmt.Errorf("runtime-server executor is nil")
	}
	if session == nil || session.RuntimeSession == nil {
		return nil, fmt.Errorf("当前没有可压缩的持久化会话")
	}
	sessionID := currentRuntimeSessionID(session)
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("runtime-server 会话 ID 为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var decoded struct {
		Status runtimeServerCompactStatusView  `json:"status"`
		Result *runtimeServerCompactResultView `json:"result"`
	}
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerCommandPath(sessionID), "", map[string]interface{}{
		"type": "compact",
		"mode": requestedMode,
	}, &decoded)
	if err != nil {
		return nil, err
	}
	status := compactruntime.Status{
		Mode:              decoded.Status.Mode,
		Phase:             decoded.Status.Phase,
		Reason:            decoded.Status.Reason,
		ResolvedProvider:  decoded.Status.Provider,
		ResolvedModel:     decoded.Status.Model,
		TriggerTokenLimit: decoded.Status.TriggerTokenLimit,
		MaxContextTokens:  decoded.Status.MaxContextTokens,
		TokenBefore:       decoded.Status.TokenBefore,
	}
	report := &chatCompactReport{RequestedMode: requestedMode, Status: status}
	if decoded.Result != nil {
		var reconciliation *contextreconcile.Report
		if decoded.Result.Reconciliation != nil {
			reconciliation = &contextreconcile.Report{
				DriftCount:     decoded.Result.Reconciliation.DriftCount,
				CorrectionMade: decoded.Result.Reconciliation.CorrectionMade,
			}
		}
		report.Result = &compactruntime.Result{
			Mode:              decoded.Result.Mode,
			Phase:             decoded.Result.Phase,
			ResolvedProvider:  decoded.Result.Provider,
			ResolvedModel:     decoded.Result.Model,
			TriggerTokenLimit: decoded.Result.TriggerTokenLimit,
			MaxContextTokens:  decoded.Result.MaxContextTokens,
			TokenBefore:       decoded.Result.TokenBefore,
			TokenAfter:        decoded.Result.TokenAfter,
			CompactedMessages: decoded.Result.CompactedMessages,
			CheckpointIDs:     decoded.Result.CheckpointIDs,
			Usage:             decoded.Result.Usage,
			UsageSource:       decoded.Result.UsageSource,
			Reconciliation:    reconciliation,
		}
		report.HistoryMessages = decoded.Result.ReplacementHistoryLen
	}
	// server 侧已完成 history 重写：先把权威会话拉回本地，再刷新 token 统计
	// 与谱系（generation / root title），与本地 compact 路径的收尾语义对齐。
	if err := refreshChatSessionFromRuntimeServer(session, sessionID); err != nil {
		return report, err
	}
	applyChatCompactContextUsage(session, report.Result, report.Status, true)
	attachChatCompactLineage(report, session)
	return report, nil
}

// ── backtrack（user-turn rewind）───────────────────────────────────────────
//
// 统一命令通道之外，runtime-server 还提供会话级 backtrack API：
//
//	GET  /api/runtime/sessions/{id}/turns             （user turn 锚点）
//	GET  /api/runtime/sessions/{id}/backtrack/audit   （tombstone 审计）
//	POST /api/runtime/sessions/{id}/backtrack/preview （预览，只读）
//	POST /api/runtime/sessions/{id}/backtrack         （应用截断，可含代码恢复）
//
// 请求/响应体与 CLI 共用的 runtimechat.Backtrack* 类型 JSON 同构。

func (e *aicliRuntimeServerChatExecutor) backtrackSessionID(session *ChatSession) (string, error) {
	if e == nil {
		return "", fmt.Errorf("runtime-server executor is nil")
	}
	if session == nil || session.RuntimeSession == nil {
		return "", fmt.Errorf("当前没有可回退的持久化会话")
	}
	sessionID := strings.TrimSpace(currentRuntimeSessionID(session))
	if sessionID == "" {
		return "", fmt.Errorf("runtime-server 会话 ID 为空")
	}
	return sessionID, nil
}

func runtimeServerSessionAPIPath(sessionID, suffix string) string {
	return "/api/runtime/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) + "/" + suffix
}

func (e *aicliRuntimeServerChatExecutor) ListBacktrackTurns(ctx context.Context, session *ChatSession) ([]runtimechat.UserTurn, error) {
	sessionID, err := e.backtrackSessionID(session)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Turns []runtimechat.UserTurn `json:"turns"`
	}
	if _, err := e.doRuntimeServerJSON(ctx, session, http.MethodGet, runtimeServerSessionAPIPath(sessionID, "turns"), "", nil, &decoded); err != nil {
		return nil, err
	}
	if decoded.Turns == nil {
		return []runtimechat.UserTurn{}, nil
	}
	return decoded.Turns, nil
}

func (e *aicliRuntimeServerChatExecutor) ListBacktrackAudit(ctx context.Context, session *ChatSession) ([]runtimechat.BacktrackTombstone, error) {
	sessionID, err := e.backtrackSessionID(session)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Entries []runtimechat.BacktrackTombstone `json:"entries"`
	}
	if _, err := e.doRuntimeServerJSON(ctx, session, http.MethodGet, runtimeServerSessionAPIPath(sessionID, "backtrack/audit"), "", nil, &decoded); err != nil {
		return nil, err
	}
	if decoded.Entries == nil {
		return []runtimechat.BacktrackTombstone{}, nil
	}
	return decoded.Entries, nil
}

func (e *aicliRuntimeServerChatExecutor) PreviewBacktrack(ctx context.Context, session *ChatSession, req runtimechat.BacktrackRequest) (*runtimechat.BacktrackResult, error) {
	// 服务端 preview 端点会强制 PreviewOnly/AutoSubmit=false；这里显式对齐，
	// 保证请求语义不依赖服务端版本。
	req.PreviewOnly = true
	req.AutoSubmit = false
	return e.runRuntimeServerBacktrack(ctx, session, req, "backtrack/preview")
}

func (e *aicliRuntimeServerChatExecutor) ApplyBacktrack(ctx context.Context, session *ChatSession, req runtimechat.BacktrackRequest) (*runtimechat.BacktrackResult, error) {
	req.PreviewOnly = false
	return e.runRuntimeServerBacktrack(ctx, session, req, "backtrack")
}

func (e *aicliRuntimeServerChatExecutor) runRuntimeServerBacktrack(ctx context.Context, session *ChatSession, req runtimechat.BacktrackRequest, suffix string) (*runtimechat.BacktrackResult, error) {
	sessionID, err := e.backtrackSessionID(session)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Result *runtimechat.BacktrackResult `json:"result"`
	}
	if _, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerSessionAPIPath(sessionID, suffix), "", req, &decoded); err != nil {
		return decoded.Result, err
	}
	return decoded.Result, nil
}

func (e *aicliRuntimeServerChatExecutor) executeRuntimeCommand(ctx context.Context, session *ChatSession, prompt string) (string, map[string]interface{}, error) {
	sessionID, err := ensureRuntimeServerSessionID(session)
	if err != nil {
		return "", nil, err
	}
	if err := syncRuntimeSessionFromChat(session); err != nil {
		return "", nil, fmt.Errorf("同步 runtime-server 会话上下文失败: %w", err)
	}
	// 本地图片附件：先上传到 runtime-server 的附件目录，再把服务端接受的
	// 路径随 submit_prompt.images 一起提交（服务端会做路径边界与格式二次校验）。
	imagePaths, imageNotes, err := e.uploadRuntimeServerImages(ctx, session)
	if err != nil {
		return "", nil, err
	}
	reportRuntimeServerImageNotes(session, imageNotes)
	afterSeq, err := e.currentRuntimeServerEventSeq(ctx, session, sessionID)
	if err != nil {
		return "", nil, err
	}

	bridge := ensureChatRuntimeEventBridge(session)
	if bridge != nil {
		bridge.startProcessor()
		previousApprove := bridge.approveTool
		previousAnswer := bridge.answerQuestion
		previousExplain := bridge.explainApproval
		bridge.approveTool = func(ctx context.Context, eventSessionID, requestID string, allow bool, feedback, rememberScope string) error {
			return e.approveRuntimeServerTool(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), requestID, allow, feedback, rememberScope)
		}
		bridge.answerQuestion = func(ctx context.Context, eventSessionID, questionID, answer string) error {
			return e.answerRuntimeServerQuestion(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), questionID, answer)
		}
		bridge.explainApproval = func(ctx context.Context, requestID string) (runtimeServerApprovalExplanation, error) {
			return e.explainRuntimeServerApproval(ctx, session, sessionID, requestID)
		}
		defer func() {
			bridge.approveTool = previousApprove
			bridge.answerQuestion = previousAnswer
			bridge.explainApproval = previousExplain
		}()
		bridge.PrepareRunPrompt(prompt)
		bridge.BeginRun()
		defer bridge.EndRun()
	}

	commandResp, statusCode, err := e.submitRuntimeServerPrompt(ctx, session, sessionID, prompt, imagePaths)
	if err != nil {
		return "", nil, err
	}
	if commandResp == nil {
		commandResp = &runtimeServerRuntimeCommandResponse{}
	}
	waitForCompletion := statusCode == http.StatusAccepted || commandResp.Pending
	pollResult, pollErr := e.waitRuntimeServerEvents(ctx, session, sessionID, afterSeq, bridge, waitForCompletion)
	if pollErr != nil {
		_ = refreshChatSessionFromRuntimeServer(session, sessionID)
		return "", commandResp.Result, humanizeActorExecutorError(session, pollErr)
	}

	output := runtimeServerResultOutput(commandResp.Result)
	if strings.TrimSpace(output) == "" {
		output = pollResult.Output
	}
	if err := refreshChatSessionFromRuntimeServer(session, sessionID); err != nil {
		return "", commandResp.Result, err
	}
	if strings.TrimSpace(output) == "" {
		output = latestAssistantResponseText(session)
	}
	return output, commandResp.Result, nil
}

func (e *aicliRuntimeServerChatExecutor) executeRuntimeContinuation(ctx context.Context, session *ChatSession, prompt string) (string, map[string]interface{}, error) {
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
		previousExplain := bridge.explainApproval
		bridge.approveTool = func(ctx context.Context, eventSessionID, requestID string, allow bool, feedback, rememberScope string) error {
			return e.approveRuntimeServerTool(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), requestID, allow, feedback, rememberScope)
		}
		bridge.answerQuestion = func(ctx context.Context, eventSessionID, questionID, answer string) error {
			return e.answerRuntimeServerQuestion(ctx, session, firstNonEmptyChatValue(eventSessionID, sessionID), questionID, answer)
		}
		bridge.explainApproval = func(ctx context.Context, requestID string) (runtimeServerApprovalExplanation, error) {
			return e.explainRuntimeServerApproval(ctx, session, sessionID, requestID)
		}
		defer func() {
			bridge.approveTool = previousApprove
			bridge.answerQuestion = previousAnswer
			bridge.explainApproval = previousExplain
		}()
		bridge.PrepareRunPrompt("")
		bridge.BeginRun()
		defer bridge.EndRun()
	}

	commandResp, statusCode, err := e.submitRuntimeServerContinue(ctx, session, sessionID, prompt)
	if err != nil {
		return "", nil, err
	}
	if commandResp == nil {
		commandResp = &runtimeServerRuntimeCommandResponse{}
	}
	waitForCompletion := statusCode == http.StatusAccepted || commandResp.Pending
	pollResult, pollErr := e.waitRuntimeServerEvents(ctx, session, sessionID, afterSeq, bridge, waitForCompletion)
	if pollErr != nil {
		_ = refreshChatSessionFromRuntimeServer(session, sessionID)
		return "", commandResp.Result, humanizeActorExecutorError(session, pollErr)
	}

	output := runtimeServerResultOutput(commandResp.Result)
	if strings.TrimSpace(output) == "" {
		output = pollResult.Output
	}
	if err := refreshChatSessionFromRuntimeServer(session, sessionID); err != nil {
		return "", commandResp.Result, err
	}
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
	payload, err := e.listRuntimeServerToolsPayload(ctx, session, sessionID)
	if err != nil {
		return nil, err
	}
	return payload.Tools, nil
}

// listRuntimeServerToolsPayload 取 runtime-server 会话级工具面观测 payload（§2.2-A/§2.2-C）。
//
// TUI /tools 直接消费这里的 groups[]/filtered[]，与 web 端点同一份响应（不再本地
// 二次 Build/二次分类）——这是"TUI 观测面与 /api/runtime/tools 同源"的落点。
func (e *aicliRuntimeServerChatExecutor) listRuntimeServerToolsPayload(ctx context.Context, session *ChatSession, sessionID string) (runtimeServerRuntimeToolsResponse, error) {
	sessionID = strings.TrimSpace(sessionID)
	if e == nil {
		return runtimeServerRuntimeToolsResponse{}, fmt.Errorf("runtime-server executor is nil")
	}
	if sessionID == "" {
		return runtimeServerRuntimeToolsResponse{}, fmt.Errorf("runtime-server session id is required")
	}
	var decoded runtimeServerRuntimeToolsResponse
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodGet, runtimeServerToolsPath(sessionID), "", nil, &decoded)
	if err != nil {
		return runtimeServerRuntimeToolsResponse{}, err
	}
	if decoded.Tools == nil {
		decoded.Tools = []runtimetypes.ToolDefinition{}
	}
	return decoded, nil
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

func (e *aicliRuntimeServerChatExecutor) submitRuntimeServerPrompt(ctx context.Context, session *ChatSession, sessionID, prompt string, images []string) (*runtimeServerRuntimeCommandResponse, int, error) {
	payload := map[string]interface{}{
		"type":   "submit_prompt",
		"prompt": prompt,
	}
	if len(images) > 0 {
		payload["images"] = append([]string(nil), images...)
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

func (e *aicliRuntimeServerChatExecutor) submitRuntimeServerContinue(ctx context.Context, session *ChatSession, sessionID, prompt string) (*runtimeServerRuntimeCommandResponse, int, error) {
	payload := map[string]interface{}{
		"type":                  "continue",
		"prompt":                prompt,
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

func (e *aicliRuntimeServerChatExecutor) approveRuntimeServerTool(ctx context.Context, session *ChatSession, sessionID, requestID string, allow bool, feedback, rememberScope string) error {
	payload := map[string]interface{}{
		"type":       "approve_tool",
		"request_id": requestID,
		"allow":      allow,
	}
	// feedback 与 runtimeapi 的 approve_tool 请求体同名字段（json:"feedback"）。
	// 普通允许/拒绝保持请求体逐字节不变，仅非空理由才追加。
	if feedback = strings.TrimSpace(feedback); feedback != "" {
		payload["feedback"] = feedback
	}
	// §4.8：remember_scope 只在允许且非空时下发（服务端校验 once|session|project，
	// 未知值 400；具体能记住什么仍由引擎按调用参数派生，宿主无法放宽范围）。
	if rememberScope = strings.ToLower(strings.TrimSpace(rememberScope)); allow && rememberScope != "" {
		payload["remember_scope"] = rememberScope
	}
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerCommandPath(sessionID), "", payload, nil)
	return err
}

// runtimeServerApprovalExplanation 对齐 runtimeapi 的 explain 响应体
// (internal/api/runtimeapi/session_approval_explain.go:43-51)：
// explanation/source(model|rules)/model/mode/cached。
type runtimeServerApprovalExplanation struct {
	Explanation string `json:"explanation"`
	Source      string `json:"source"`
	Model       string `json:"model,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Cached      bool   `json:"cached,omitempty"`
}

// explainRuntimeServerApproval 调用 §4.13 的按需解释端点（纯 UI、只读）：
// POST /api/runtime/sessions/{id}/runtime/approvals/{request_id}/explain。
// 语义与 runtimeapi 一致：审批已不 pending → 409；模型不可用 → 200 + rules。
func (e *aicliRuntimeServerChatExecutor) explainRuntimeServerApproval(ctx context.Context, session *ChatSession, sessionID, requestID string) (runtimeServerApprovalExplanation, error) {
	var decoded runtimeServerApprovalExplanation
	sessionID = strings.TrimSpace(sessionID)
	requestID = strings.TrimSpace(requestID)
	if sessionID == "" {
		return decoded, fmt.Errorf("runtime-server session id is required")
	}
	if requestID == "" {
		return decoded, fmt.Errorf("runtime-server approval request id is required")
	}
	_, err := e.doRuntimeServerJSON(ctx, session, http.MethodPost, runtimeServerApprovalExplainPath(sessionID, requestID), "", nil, &decoded)
	if err != nil {
		return runtimeServerApprovalExplanation{}, err
	}
	return decoded, nil
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

func runtimeServerApprovalExplainPath(sessionID, requestID string) string {
	return "/api/runtime/sessions/" + url.PathEscape(strings.TrimSpace(sessionID)) +
		"/runtime/approvals/" + url.PathEscape(strings.TrimSpace(requestID)) + "/explain"
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
			if !bridge.WaitForCurrentEvents(aicliRuntimeServerEventDrainTimeout) {
				writeSessionDebugInfo(session, "[runtime-event] runtime server poll drain timeout", false)
			}
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
				if !bridge.WaitForCurrentEvents(aicliRuntimeServerEventDrainTimeout) {
					writeSessionDebugInfo(session, "[runtime-event] runtime server stream drain timeout", false)
				}
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

// runtimeServerUpload*View 对齐 POST /api/runtime/uploads 的响应契约
// （runtimeapi.attachment_upload_handlers.go：{ok,accepted,attachments:[...]}）。
type runtimeServerUploadEntryView struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Note    string `json:"note"`
	Skipped bool   `json:"skipped"`
}

type runtimeServerUploadResponseView struct {
	OK          bool                           `json:"ok"`
	Accepted    int                            `json:"accepted"`
	Attachments []runtimeServerUploadEntryView `json:"attachments"`
	Error       string                         `json:"error"`
	Reason      string                         `json:"reason"`
}

// runtimeServerUploadBatchSize 与 runtimeapi.maxRuntimeUploadFiles 对齐：
// 服务端单次请求最多 8 张，超过时按批上传后合并路径。
const runtimeServerUploadBatchSize = 8

// uploadRuntimeServerImages 把会话当前的本地图片附件上传到 runtime-server，
// 返回服务端接受的文件路径（供 submit_prompt.images 使用）与被拒附件的说明。
// 全部被拒时返回错误（不静默降级成纯文本发送，语义与 Web 端一致）。
func (e *aicliRuntimeServerChatExecutor) uploadRuntimeServerImages(ctx context.Context, session *ChatSession) ([]string, []string, error) {
	if e == nil {
		return nil, nil, fmt.Errorf("runtime-server executor is nil")
	}
	if session == nil || len(session.ImagePaths) == 0 {
		return nil, nil, nil
	}
	paths := make([]string, 0, len(session.ImagePaths))
	for _, raw := range session.ImagePaths {
		if path := strings.TrimSpace(raw); path != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, nil, nil
	}
	accepted := make([]string, 0, len(paths))
	notes := make([]string, 0)
	for start := 0; start < len(paths); start += runtimeServerUploadBatchSize {
		end := start + runtimeServerUploadBatchSize
		if end > len(paths) {
			end = len(paths)
		}
		batchAccepted, batchNotes, err := e.uploadRuntimeServerImageBatch(ctx, session, paths[start:end])
		if err != nil {
			return nil, notes, err
		}
		accepted = append(accepted, batchAccepted...)
		notes = append(notes, batchNotes...)
	}
	if len(accepted) == 0 {
		if len(notes) == 0 {
			notes = append(notes, "没有可用附件")
		}
		return nil, notes, fmt.Errorf("runtime-server 拒绝了全部图片附件: %s", strings.Join(notes, "; "))
	}
	return accepted, notes, nil
}

func (e *aicliRuntimeServerChatExecutor) uploadRuntimeServerImageBatch(ctx context.Context, session *ChatSession, paths []string) ([]string, []string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, nil, fmt.Errorf("读取图片附件失败 (%s): %w", path, err)
		}
		part, err := writer.CreateFormFile("file", filepath.Base(path))
		if err != nil {
			file.Close()
			return nil, nil, fmt.Errorf("构建附件上传表单失败: %w", err)
		}
		if _, err := io.Copy(part, file); err != nil {
			file.Close()
			return nil, nil, fmt.Errorf("读取图片附件失败 (%s): %w", path, err)
		}
		file.Close()
	}
	if err := writer.Close(); err != nil {
		return nil, nil, fmt.Errorf("构建附件上传表单失败: %w", err)
	}
	var decoded runtimeServerUploadResponseView
	if err := e.doRuntimeServerMultipart(ctx, session, "/api/runtime/uploads", &body, writer.FormDataContentType(), &decoded); err != nil {
		return nil, nil, err
	}
	accepted := make([]string, 0, len(decoded.Attachments))
	notes := make([]string, 0, len(decoded.Attachments))
	for _, entry := range decoded.Attachments {
		if path := strings.TrimSpace(entry.Path); path != "" {
			accepted = append(accepted, path)
			continue
		}
		note := strings.TrimSpace(entry.Note)
		if note == "" {
			note = "附件被服务端拒绝"
		}
		notes = append(notes, entry.Name+": "+note)
	}
	if len(accepted) == 0 && len(notes) == 0 {
		notes = append(notes, "服务端未接受任何附件")
	}
	return accepted, notes, nil
}

// reportRuntimeServerImageNotes 把被拒附件说明回显给用户：交互式会话走
// 统一 surface（RenderLocalSupplement），其它路径复用命令输出兼容边界
// printChatCommandOutput（禁止新增终端直写点，见 direct-writer inventory）。
func reportRuntimeServerImageNotes(session *ChatSession, notes []string) {
	if session == nil || len(notes) == 0 {
		return
	}
	notice := "图片附件提示: " + strings.Join(notes, "; ")
	if session.Interaction != nil {
		session.Interaction.RenderLocalSupplement(notice)
		return
	}
	printChatCommandOutput(session, notice)
}

// doRuntimeServerMultipart 发送 multipart/form-data 请求（附件上传用）。
// 错误语义与 doRuntimeServerJSON 对齐：非 2xx 统一返回 runtimeServerHTTPError。
func (e *aicliRuntimeServerChatExecutor) doRuntimeServerMultipart(ctx context.Context, session *ChatSession, path string, body io.Reader, contentType string, target interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(e.serverURL) == "" {
		return fmt.Errorf("runtime-server 地址未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.serverURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)
	client := http.DefaultClient
	if session != nil && session.HTTPClient != nil {
		client = session.HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("runtime-server 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &runtimeServerHTTPError{
			Method:     http.MethodPost,
			Path:       path,
			Status:     resp.Status,
			StatusCode: resp.StatusCode,
			Body:       string(data),
		}
	}
	if target == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("解析 runtime-server 响应失败: %w", err)
	}
	return nil
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

func refreshChatSessionFromRuntimeServer(session *ChatSession, sessionID string) error {
	if session == nil {
		return fmt.Errorf("chat session is nil")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("runtime-server session id is empty")
	}
	if session.SessionManager == nil {
		return fmt.Errorf("runtime-server session manager is not configured")
	}
	runtimeSession, err := session.SessionManager.Get(context.Background(), sessionID)
	if err != nil {
		return fmt.Errorf("refresh runtime-server session %s: %w", sessionID, err)
	}
	if runtimeSession == nil {
		return fmt.Errorf("refresh runtime-server session %s: session not found", sessionID)
	}
	if err := restoreChatStateFromRuntimeSession(session, runtimeSession); err != nil {
		return fmt.Errorf("restore runtime-server session %s: %w", sessionID, err)
	}
	return nil
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
	session.ActorFirstReady = true
	session.LocalRuntimeHost = nil
	return true, nil
}

// runtimeServerChatExecutorConfigured 只做「是否由 runtime-server 接管」的判定，
// **不写 session**。
//
// configureRuntimeServerChatExecutor 会顺手把 executor 挂到 session 上，因此只能
// 留在主 goroutine；而本地能力面的发现阶段需要先知道「要不要发现」，否则
// runtime-server 模式会白扫一遍技能目录。两者用同一套判定条件，避免两条路径
// 对「什么算 configured」产生分歧。
func runtimeServerChatExecutorConfigured(ctx context.Context, opts *chatCommandOptions, session *ChatSession) (bool, error) {
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
	return true, nil
}
