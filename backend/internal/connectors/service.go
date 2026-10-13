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
	// RequireAllowedUsers 为 true 时启用 fail-closed 安全闸：启用中的连接器若
	// 未配置非空的 Policy.AllowedUsers，则跳过启动（不 Activate、不轮询）。
	// aicli 宿主设为 true；runtime-server 保持 false 以兼容既有部署（仅在
	// 宿主侧对「启用且白名单为空」的连接器记 warn）。
	RequireAllowedUsers bool
}

// Service 编排连接器：注册 Provider、按配置启动轮询、处理入站消息并投递回复。
type Service struct {
	deps Deps

	mu        sync.Mutex
	providers map[string]Provider
	queue     *chatQueue
	// running 记录已启动的轮询实例；gen 用于代际校验，避免旧实例退出时
	// 误删 Reload/重启后登记的新实例（Fix-4）。
	running map[string]*connectorRun
	// gen 单调递增的轮询实例代际号（受 mu 保护）。
	gen uint64
	// baseCtx/baseCancel 是 Service 生命周期 ctx（Start 建立、Stop 取消）：
	// 轮询与异步任务都基于它派生，绝不绑定调用方请求 ctx（Fix-1）。
	baseCtx    context.Context
	baseCancel context.CancelFunc
	started    bool
}

// connectorRun 是一次轮询实例的登记项。
type connectorRun struct {
	gen    uint64
	cancel context.CancelFunc
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
		running:   make(map[string]*connectorRun),
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
	s.baseCtx, s.baseCancel = context.WithCancel(ctx)
	// Stop 会 closeAll 队列；重启时复位关闭标记，否则入站会一直被 ErrQueueClosed 拒绝。
	s.queue.reopen()
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
	run, ok := s.running[id]
	if ok {
		delete(s.running, id)
	}
	s.mu.Unlock()
	if ok && run != nil && run.cancel != nil {
		run.cancel()
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

// Stop 停止全部连接器与队列（取消 Service 生命周期 ctx）。
func (s *Service) Stop(ctx context.Context) {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.running))
	for id, run := range s.running {
		if run != nil && run.cancel != nil {
			cancels = append(cancels, run.cancel)
		}
		delete(s.running, id)
	}
	if s.baseCancel != nil {
		cancels = append(cancels, s.baseCancel)
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

// startConnector 启动一个连接器的轮询实例。调用方传入的 ctx（Start/Reload 的
// 请求 ctx）只用于同步控制流，不参与生命周期：Activate 与轮询都基于
// Service.baseCtx 派生（未 Start 时回落 context.Background()），保证
// REST handler 返回后请求 ctx 取消不会静默停掉轮询（Fix-1）。
func (s *Service) startConnector(_ context.Context, cfg Config) {
	// fail-closed 安全闸（Fix-S2）：未配置白名单的启用连接器意味着任意平台
	// 用户都能驱动 agent。要求宿主显式开启本闸时，宁可跳过启动，也不带着
	// 「对所有人开放」的语义默默上线。
	if s.deps.RequireAllowedUsers && len(cfg.Policy.AllowedUsers) == 0 {
		s.logf("connector %s: policy.allowed_users 为空，fail-closed 安全闸已跳过启动；需在策略中配置 allowed_users，或显式关闭该闸（RequireAllowedUsers=false）", cfg.ID)
		return
	}
	provider, ok := s.provider(cfg.Provider)
	if !ok {
		s.logf("connector %s: provider %q not registered; skipped", cfg.ID, cfg.Provider)
		return
	}
	runCtx, cancel := context.WithCancel(s.baseContext())
	if _, err := provider.Activate(runCtx, cfg); err != nil {
		cancel()
		s.logf("connector %s: activation failed: %v", cfg.ID, err)
		return
	}
	poller, ok := provider.(PollingProvider)
	if !ok || !poller.SupportsPolling(cfg) {
		cancel()
		if s.deps.Backend != nil {
			s.logf("connector %s: provider %q has no active polling transport in this host", cfg.ID, cfg.Provider)
		}
		return
	}
	s.mu.Lock()
	if old, exists := s.running[cfg.ID]; exists {
		if old != nil && old.cancel != nil {
			old.cancel()
		}
	}
	s.gen++
	run := &connectorRun{gen: s.gen, cancel: cancel}
	s.running[cfg.ID] = run
	s.mu.Unlock()

	go func() {
		s.logf("connector %s: polling started (provider=%s)", cfg.ID, cfg.Provider)
		err := poller.RunPolling(runCtx, cfg, s.HandleInbound, s.deps.Store.PollingState(cfg.ID))
		if err != nil && runCtx.Err() == nil {
			s.logf("connector %s: polling stopped: %v", cfg.ID, err)
		}
		s.mu.Lock()
		// 仅在登记项仍属于本代际时清理；否则会误删 Reload/重启后的新登记。
		if current, ok := s.running[cfg.ID]; ok && current != nil && current.gen == run.gen {
			delete(s.running, cfg.ID)
		}
		s.mu.Unlock()
	}()
}

// baseContext 返回 Service 生命周期 ctx；未 Start（或已 Stop 且未重启）时回落
// context.Background()，使调用方请求 ctx 不成为轮询/异步任务的取消源。
func (s *Service) baseContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

// HandleInbound 是 webhook 与轮询共用的入站入口：
// 校验配置/白名单 → 按会话串行入队（队列满返回 ErrQueueFull）→ 任务内幂等认领。
//
// 顺序说明（Fix-5）：认领（ClaimInbound）在入队成功之后、由 worker 任务执行，
// 因此 ErrQueueFull（宿主回 429）时消息不会被账本吞掉，平台重试仍可再次入队；
// 去重键含 chat（telegram 的 message_id 仅在 chat 内唯一，见 inboundDedupeKey）。
// 任务 ctx 基于 Service.baseCtx，而非入站请求 ctx：webhook 返回 202 / REST
// handler 返回后请求 ctx 取消不影响异步处理（Fix-1）。
func (s *Service) HandleInbound(_ context.Context, msg InboundMessage) error {
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
	taskCtx := s.baseContext()
	dedupeKey := inboundDedupeKey(msg)
	key := cfg.ID + "|" + msg.Chat
	return s.queue.enqueue(key, func() {
		if dedupeKey != "" {
			claimed, err := s.deps.Store.ClaimInbound(cfg.ID, dedupeKey)
			if err != nil {
				// 认领失败仅记日志；是否处理仍以 claimed 为准：claimed=true 表示
				// 本实例认领成功（账本可能未持久化，宁可能重复也不丢），
				// claimed=false 表示账本已存在该键（重复投递），必须跳过。
				s.logf("connector %s: claim inbound %q failed: %v (claimed=%v)", cfg.ID, dedupeKey, err, claimed)
			}
			if !claimed {
				return // 平台重试/重复投递：幂等忽略
			}
		}
		s.process(taskCtx, cfg, provider, msg)
	})
}

// inboundDedupeKey 构造幂等去重键：connector + chat + messageID。
// telegram 等平台的 message_id 仅在单个 chat 内唯一，跨会话复用同一 ID 是常态，
// 因此必须把 chat 并入键；chat 为空时回落为裸 MessageID，MessageID 为空时返回
// 空串（调用方跳过认领）。
func inboundDedupeKey(msg InboundMessage) string {
	messageID := strings.TrimSpace(msg.MessageID)
	if messageID == "" {
		return ""
	}
	if chat := strings.TrimSpace(msg.Chat); chat != "" {
		return chat + "|" + messageID
	}
	return messageID
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
