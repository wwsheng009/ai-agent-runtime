package connectors

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// Deps 是 Service 的依赖注入点（宿主提供存储与 agent 后端）。
type Deps struct {
	Store   Store
	Backend Backend
	// Logf 可注入结构化日志；默认 log.Printf。
	Logf func(format string, args ...any)
	// DeltaInterval 流式编辑最小间隔，<=0 用默认 800ms。
	DeltaInterval time.Duration
	// QueueSize 每个 IM 会话的处理队列长度，<=0 用默认 32。
	QueueSize int
}

// Service 编排连接器：注册 Provider、按配置启动轮询、处理入站消息并投递回复。
type Service struct {
	deps Deps

	mu        sync.Mutex
	providers map[string]Provider
	queue     *chatQueue
	running   map[string]context.CancelFunc
	started   bool
}

// New 构造 Service（须提供 Store 与 Backend）。
func New(deps Deps) *Service {
	if deps.Logf == nil {
		deps.Logf = log.Printf
	}
	return &Service{
		deps:      deps,
		providers: make(map[string]Provider),
		queue:     newChatQueue(deps.QueueSize, defaultWorkerIdleTime),
		running:   make(map[string]context.CancelFunc),
	}
}

// Register 注册一个通道适配器（重复注册同名通道会被拒绝）。
func (s *Service) Register(provider Provider) error {
	if s == nil || provider == nil {
		return errors.New("connectors: nil provider")
	}
	name := strings.TrimSpace(provider.Name())
	if name == "" {
		return errors.New("connectors: provider name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.providers[name]; exists {
		return fmt.Errorf("connectors: provider %q already registered", name)
	}
	s.providers[name] = provider
	return nil
}

// Providers 返回已注册通道的 Schema（供 GET /api/connectors/schema 使用）。
func (s *Service) Providers() []api.ProviderSchema {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.ProviderSchema, 0, len(s.providers))
	for _, provider := range s.providers {
		out = append(out, provider.Schema())
	}
	return out
}

// Start 按存储中的配置启动所有启用的连接器（仅轮询/长连接通道；webhook 通道
// 由宿主的 HTTP 入口在收到请求时直接调用 HandleInbound）。
func (s *Service) Start(ctx context.Context) error {
	if s == nil || s.deps.Store == nil || s.deps.Backend == nil {
		return ErrServiceNotConfigured
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	s.mu.Unlock()

	for _, cfg := range s.deps.Store.ListConfigs() {
		if !cfg.Enabled {
			continue
		}
		s.startConnector(ctx, cfg)
	}
	return nil
}

// StartConnector 启动单个连接器（Reload 时使用）。
func (s *Service) StartConnector(ctx context.Context, id string) error {
	cfg, ok := s.deps.Store.GetConfig(id)
	if !ok {
		return fmt.Errorf("connectors: unknown connector %q", id)
	}
	if !cfg.Enabled {
		return fmt.Errorf("connectors: connector %q is disabled", id)
	}
	s.startConnector(ctx, cfg)
	return nil
}

// StopConnector 停止单个连接器（保留配置）。
func (s *Service) StopConnector(id string) {
	s.mu.Lock()
	cancel, ok := s.running[id]
	if ok {
		delete(s.running, id)
	}
	s.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

// Provider 按通道名返回适配器（管理面用于探测能力，如 WebhookProvider）。
func (s *Service) Provider(name string) (Provider, bool) {
	return s.provider(name)
}

// ReloadConnector 在配置保存/启停后应用变更：先停止旧实例；启用项重新激活并轮询。
func (s *Service) ReloadConnector(ctx context.Context, id string) error {
	if s == nil || s.deps.Store == nil {
		return ErrServiceNotConfigured
	}
	cfg, ok := s.deps.Store.GetConfig(id)
	if !ok {
		return fmt.Errorf("connectors: unknown connector %q", id)
	}
	s.StopConnector(id)
	if !cfg.Enabled {
		return nil
	}
	s.startConnector(ctx, cfg)
	return nil
}

// Stop 停止全部连接器与队列。
func (s *Service) Stop(ctx context.Context) {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.running))
	for id, cancel := range s.running {
		cancels = append(cancels, cancel)
		delete(s.running, id)
	}
	s.started = false
	s.mu.Unlock()
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
	s.queue.closeAll()
}

func (s *Service) startConnector(ctx context.Context, cfg Config) {
	provider, ok := s.provider(cfg.Provider)
	if !ok {
		s.logf("connector %s: provider %q not registered; skipped", cfg.ID, cfg.Provider)
		return
	}
	if _, err := provider.Activate(ctx, cfg); err != nil {
		s.logf("connector %s: activation failed: %v", cfg.ID, err)
		return
	}
	poller, ok := provider.(PollingProvider)
	if !ok || !poller.SupportsPolling(cfg) {
		if s.deps.Backend != nil {
			s.logf("connector %s: provider %q has no active polling transport in this host", cfg.ID, cfg.Provider)
		}
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if old, exists := s.running[cfg.ID]; exists {
		old()
	}
	s.running[cfg.ID] = cancel
	s.mu.Unlock()

	go func() {
		s.logf("connector %s: polling started (provider=%s)", cfg.ID, cfg.Provider)
		err := poller.RunPolling(runCtx, cfg, s.HandleInbound, s.deps.Store.PollingState(cfg.ID))
		if err != nil && runCtx.Err() == nil {
			s.logf("connector %s: polling stopped: %v", cfg.ID, err)
		}
		s.mu.Lock()
		if current, ok := s.running[cfg.ID]; ok {
			_ = current
			delete(s.running, cfg.ID)
		}
		s.mu.Unlock()
	}()
}

// HandleInbound 是 webhook 与轮询共用的入站入口：
// 校验配置/白名单 → 幂等去重 → 按会话串行入队（队列满返回 ErrQueueFull）。
func (s *Service) HandleInbound(ctx context.Context, msg InboundMessage) error {
	if s == nil || s.deps.Store == nil || s.deps.Backend == nil {
		return ErrServiceNotConfigured
	}
	cfg, ok := s.deps.Store.GetConfig(msg.ConnectorID)
	if !ok {
		return fmt.Errorf("connectors: unknown connector %q", msg.ConnectorID)
	}
	if !cfg.Enabled {
		return nil // 停用期间静默（不认领账本，恢复后可重放）
	}
	provider, ok := s.provider(cfg.Provider)
	if !ok {
		return fmt.Errorf("connectors: provider %q not registered", cfg.Provider)
	}
	if !cfg.Policy.AllowsUser(msg.UserID) {
		s.logf("connector %s: rejected message from unauthorized user %q", cfg.ID, msg.UserID)
		return nil
	}
	if strings.TrimSpace(msg.MessageID) != "" {
		claimed, err := s.deps.Store.ClaimInbound(cfg.ID, msg.MessageID)
		if err != nil {
			return err
		}
		if !claimed {
			return nil // 平台重试/重复投递：幂等忽略
		}
	}
	key := cfg.ID + "|" + msg.Chat
	return s.queue.enqueue(key, func() { s.process(ctx, cfg, provider, msg) })
}

// SendOutbound 供宿主主动投递（例如把注意力提醒发到已绑定的 IM 会话）。
func (s *Service) SendOutbound(ctx context.Context, connectorID, chat string, msgs []OutboundMessage) error {
	if s == nil || s.deps.Store == nil {
		return ErrServiceNotConfigured
	}
	cfg, ok := s.deps.Store.GetConfig(connectorID)
	if !ok {
		return fmt.Errorf("connectors: unknown connector %q", connectorID)
	}
	provider, ok := s.provider(cfg.Provider)
	if !ok {
		return fmt.Errorf("connectors: provider %q not registered", cfg.Provider)
	}
	return provider.Send(ctx, cfg, chat, msgs)
}

func (s *Service) process(ctx context.Context, cfg Config, provider Provider, msg InboundMessage) {
	if err := s.processMessage(ctx, cfg, provider, msg); err != nil {
		s.logf("connector %s: message %s failed: %v", cfg.ID, msg.MessageID, err)
	}
}

func (s *Service) processMessage(ctx context.Context, cfg Config, provider Provider, msg InboundMessage) error {
	prefix := cfg.Policy.commandPrefix()
	if cmd, ok := parseCommand(prefix, msg.Text); ok {
		handled, err := s.handleCommand(ctx, cfg, provider, msg, cmd)
		if err != nil || handled {
			return err
		}
	}

	binding, _ := s.deps.Store.GetBinding(cfg.ID, msg.Chat)

	streamer, err := s.startStreaming(ctx, cfg, provider, msg.Chat)
	if err != nil {
		return err
	}
	typing := s.startTyping(ctx, cfg, provider, msg.Chat)
	defer func() {
		if typing != nil {
			_ = typing.Stop(ctx)
		}
	}()

	sink := &replySink{provider: provider, cfg: cfg, chat: msg.Chat, session: streamer,
		throttle: newDeltaThrottle(s.deltaInterval())}

	result, err := s.deps.Backend.Handle(ctx, BackendRequest{
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Session:     binding.Session,
		SessionMode: cfg.Policy.SessionMode,
		Text:        msg.Text,
		UserID:      msg.UserID,
		UserName:    msg.UserName,
		MessageID:   msg.MessageID,
	}, sink)
	if err != nil {
		if streamer != nil {
			_ = streamer.Fail(ctx, failureText(err))
			return err
		}
		return err
	}

	if !result.Session.IsZero() && binding.Session != result.Session {
		if err := s.deps.Store.PutBinding(Binding{ConnectorID: cfg.ID, Chat: msg.Chat, Session: result.Session}); err != nil {
			s.logf("connector %s: persist binding failed: %v", cfg.ID, err)
		}
	}
	if len(result.Reply) == 0 {
		if streamer != nil {
			return streamer.Final(ctx, nil)
		}
		return nil
	}
	if streamer != nil {
		return streamer.Final(ctx, result.Reply)
	}
	return provider.Send(ctx, cfg, msg.Chat, result.Reply)
}

func (s *Service) startStreaming(ctx context.Context, cfg Config, provider Provider, chat string) (StreamingSession, error) {
	if !cfg.Policy.streamingEnabled() {
		return nil, nil
	}
	streaming, ok := provider.(StreamingProvider)
	if !ok {
		return nil, nil
	}
	session, err := streaming.StartStreaming(ctx, cfg, chat)
	if err != nil {
		s.logf("connector %s: start streaming failed (fallback to final reply): %v", cfg.ID, err)
		return nil, nil
	}
	return session, nil
}

func (s *Service) startTyping(ctx context.Context, cfg Config, provider Provider, chat string) TypingSession {
	typist, ok := provider.(TypingProvider)
	if !ok {
		return nil
	}
	session, err := typist.StartTyping(ctx, cfg, chat)
	if err != nil || session == nil {
		return nil
	}
	return session
}

func (s *Service) provider(name string) (Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider, ok := s.providers[name]
	return provider, ok
}

func (s *Service) deltaInterval() time.Duration {
	if s.deps.DeltaInterval > 0 {
		return s.deps.DeltaInterval
	}
	return 800 * time.Millisecond
}

func (s *Service) logf(format string, args ...any) {
	if s.deps.Logf != nil {
		s.deps.Logf(format, args...)
	}
}

type command struct {
	name string
	args string
}

func parseCommand(prefix, text string) (command, bool) {
	text = strings.TrimSpace(text)
	if prefix == "" || !strings.HasPrefix(text, prefix) {
		return command{}, false
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	if trimmed == "" {
		return command{}, false
	}
	fields := strings.SplitN(trimmed, " ", 2)
	cmd := command{name: strings.ToLower(fields[0])}
	if len(fields) == 2 {
		cmd.args = strings.TrimSpace(fields[1])
	}
	return cmd, true
}

// handleCommand 处理核心通用命令；返回 handled=false 时交还给 Backend
// （可能是 agent 自己的 slash 命令）。
func (s *Service) handleCommand(ctx context.Context, cfg Config, provider Provider, msg InboundMessage, cmd command) (bool, error) {
	switch cmd.name {
	case "help", "start":
		return true, provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: helpText(cfg)}})
	case "new":
		if err := s.deps.Store.PutBinding(Binding{ConnectorID: cfg.ID, Chat: msg.Chat}); err != nil {
			return true, err
		}
		return true, provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: "已开启新会话，请直接发送你的问题。"}})
	case "status":
		binding, ok := s.deps.Store.GetBinding(cfg.ID, msg.Chat)
		text := "当前未绑定会话（发送任意消息即自动创建）。"
		if ok && !binding.Session.IsZero() {
			text = "当前会话：" + describeSession(binding.Session)
			if !binding.UpdatedAt.IsZero() {
				text += "（" + binding.UpdatedAt.Format("15:04:05") + " 绑定）"
			}
		}
		return true, provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: text}})
	default:
		return false, nil
	}
}

func describeSession(ref SessionRef) string {
	if ref.NodeID == "" {
		return ref.SessionID
	}
	return ref.NodeID + "/" + ref.SessionID
}

func helpText(cfg Config) string {
	prefix := cfg.Policy.commandPrefix()
	return strings.Join([]string{
		"可用命令：",
		prefix + "help 显示本帮助",
		prefix + "new 开启新会话",
		prefix + "status 查看当前绑定会话",
		"",
		"直接发送文本即可与 agent 对话。",
	}, "\n")
}

func failureText(err error) string {
	reason := "内部错误"
	if err != nil {
		reason = strings.TrimSpace(err.Error())
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return "本次处理失败：" + reason + "\n可稍后重试，或查看控制台日志。"
}

// replySink 把 Backend 的流式片段经节流后写入 StreamingSession。
type replySink struct {
	provider Provider
	cfg      Config
	chat     string
	session  StreamingSession
	throttle *deltaThrottle
}

func (s *replySink) Delta(ctx context.Context, text string) error {
	if s == nil || s.session == nil {
		return nil // 不支持流式：终态回复会一次性发出
	}
	if !s.throttle.shouldEmit(text) {
		return nil
	}
	return s.session.Update(ctx, strings.TrimSpace(text))
}
