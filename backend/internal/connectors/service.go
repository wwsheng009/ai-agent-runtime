package connectors

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// DefaultOwnerHeartbeatInterval 是运行归属心跳的默认刷新间隔（Deps 可覆盖）。
const DefaultOwnerHeartbeatInterval = 30 * time.Second

var (
	// ErrHostIDRequired：宿主未配置 Deps.HostID，无法认领/接管运行归属。
	ErrHostIDRequired = errors.New("connectors: host id is not configured")
	// ErrConnectorDisabled：对停用连接器执行运行归属操作（如抢占）。
	ErrConnectorDisabled = errors.New("connectors: connector is disabled")
	// ErrOwnerPolicyMismatch：policy.runtime_owner 指定了其它宿主，本宿主不能接管。
	ErrOwnerPolicyMismatch = errors.New("connectors: runtime_owner belongs to another host")
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
	// LogBufferSize 是每连接器运行日志环容量（Service.Logs / 视图 runtime 读它），
	// <=0 用 DefaultLogBufferSize（200）。日志环仅存于进程内，重启清零。
	LogBufferSize int
	// RequireAllowedUsers 为 true 时启用 fail-closed 安全闸：启用中的连接器若
	// 未配置非空的 Policy.AllowedUsers，则跳过启动（不 Activate、不轮询）。
	// aicli 宿主设为 true；runtime-server 保持 false 以兼容既有部署（仅在
	// 宿主侧对「启用且白名单为空」的连接器记 warn）。
	RequireAllowedUsers bool
	// HostID 是本宿主的运行归属身份（"runtime-server" / "aicli" / "gateway"，
	// docs/bots/07-config-ux.md §5.6）：启动时据此认领、轮询期间续约、停机释放。
	// 为空时跳过归属闸（兼容旧宿主与测试），REST takeover 会明确返回错误。
	HostID string
	// OwnerStaleAfter 是运行归属心跳超时：持锁宿主超过该时长未续约即视为陈旧，
	// 后来者可接管；<=0 用 DefaultOwnerStaleAfter（90s）。测试可缩短。
	OwnerStaleAfter time.Duration
	// OwnerHeartbeatInterval 是归属心跳刷新间隔；<=0 用
	// DefaultOwnerHeartbeatInterval（30s）。测试可缩短。
	OwnerHeartbeatInterval time.Duration
	// Peers 返回当前 mesh peers 视图（P2 切片 B，docs/bots/09 §7.2）：nil/
	// 返回空 = 单机模式，全部本机执行（存量行为）。宿主从 mesh 档案装配，
	// Local/Alive 由宿主标注（connectors 只认字段语义，不摸 mesh 包）。
	Peers func() []RunnerPeer
	// RemoteDispatch 把 turn 派发到目标节点（R1 亲和 / R3 远端选举）：
	// v1 只回终态（docs/bots/09 §7.3）；nil 时即使路由选了远端也回落本机执行
	//（能力未装配不拒收消息）。返回的 Session 是 runner 侧实际会话
	//（auto-create 后含新 SessionID，调用方负责回写绑定）。
	RemoteDispatch func(ctx context.Context, req DispatchRequest) (DispatchResult, error)
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
	// logMu/logBuffers 是每连接器运行日志环的注册表（专用锁；取环后立即释放，
	// 环内数据由 connectorLog.mu 保护，绝不与 mu 形成锁序）。
	logMu      sync.Mutex
	logBuffers map[string]*connectorLog
	// runnerRoundRobin 是 round-robin 选举游标（P2 切片 B；受 mu 保护，
	// 进程内递增——重启清零可接受，轮转语义不依赖持久化）。
	runnerRoundRobin int
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
		deps:       deps,
		providers:  make(map[string]Provider),
		queue:      newChatQueue(deps.QueueSize, defaultWorkerIdleTime),
		running:    make(map[string]*connectorRun),
		logBuffers: make(map[string]*connectorLog),
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
	if ok {
		s.recordLog(LogEntry{
			Level:       LogLevelInfo,
			Direction:   LogDirectionSystem,
			ConnectorID: id,
			Summary:     "连接器已停止",
		}, logCounters{})
	}
	// 停机尽力释放本宿主认领（非本宿主持有时是 no-op；见 07 §5.6）。
	s.releaseOwner(id)
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

// TakeoverConnector 强制接管指定连接器的运行归属（管理面「抢占」，07 §5.6）：
// 停掉本宿主旧实例 → 无视心跳强制认领 → 启用项重新启动轮询（ReloadConnector）。
// 策略优先：policy.runtime_owner 显式指定其它宿主时拒绝接管（先改策略再抢占）；
// 停用连接器拒绝接管（无轮询可跑）。
// 注意边界：抢占只改写归属账本，不会直接杀死对端进程——对端的归属心跳会观察到
// held=false 并自行停止轮询（尽力围栏，不做分布式强保证）。
func (s *Service) TakeoverConnector(ctx context.Context, id string) error {
	if s == nil || s.deps.Store == nil {
		return ErrServiceNotConfigured
	}
	hostID := s.hostID()
	if hostID == "" {
		return ErrHostIDRequired
	}
	id = strings.TrimSpace(id)
	cfg, ok := s.deps.Store.GetConfig(id)
	if !ok {
		return fmt.Errorf("connectors: unknown connector %q", id)
	}
	if !cfg.Enabled {
		return fmt.Errorf("%w: %q", ErrConnectorDisabled, id)
	}
	if !HostMatchesPolicy(hostID, cfg.Policy.RuntimeOwner) {
		return fmt.Errorf("%w: policy.runtime_owner=%s", ErrOwnerPolicyMismatch, strings.TrimSpace(cfg.Policy.RuntimeOwner))
	}
	s.StopConnector(id)
	if err := s.deps.Store.ForceClaimConnectorOwner(id, hostID, time.Now().UTC()); err != nil {
		return err
	}
	return s.ReloadConnector(ctx, id)
}

// Stop 停止全部连接器与队列（取消 Service 生命周期 ctx）。
func (s *Service) Stop(ctx context.Context) {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.running))
	ids := make([]string, 0, len(s.running))
	for id, run := range s.running {
		if run != nil && run.cancel != nil {
			cancels = append(cancels, run.cancel)
		}
		ids = append(ids, id)
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
	for _, id := range ids {
		s.releaseOwner(id)
		s.recordLog(LogEntry{
			Level:       LogLevelInfo,
			Direction:   LogDirectionSystem,
			ConnectorID: id,
			Summary:     "服务停机，轮询已停止",
		}, logCounters{})
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
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Summary:     "启动跳过：policy.allowed_users 为空（fail-closed 安全闸）",
		}, logCounters{})
		return
	}
	// 运行归属闸（07 §5.6）：同一通道的轮询/WS 只能一处运行。只有策略允许且
	// 存储认领成功（先到先得，或持有者心跳已陈旧）才继续；否则只读配置、不轮询。
	if !s.acquireOwnership(cfg) {
		return
	}
	// 认领后到轮询登记之间的任何提前返回都要释放归属；登记成功后由轮询
	// goroutine 接管释放职责（代际校验避免误放新实例的认领）。
	releaseOnExit := true
	defer func() {
		if releaseOnExit {
			s.releaseOwner(cfg.ID)
		}
	}()
	provider, ok := s.provider(cfg.Provider)
	if !ok {
		s.logf("connector %s: provider %q not registered; skipped", cfg.ID, cfg.Provider)
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Summary:     fmt.Sprintf("启动跳过：provider %q 未注册", cfg.Provider),
		}, logCounters{})
		return
	}
	runCtx, cancel := context.WithCancel(s.baseContext())
	if _, err := provider.Activate(runCtx, cfg); err != nil {
		cancel()
		s.logf("connector %s: activation failed: %v", cfg.ID, err)
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Summary:     "启动失败：激活出错：" + err.Error(),
		}, logCounters{})
		return
	}
	poller, ok := provider.(PollingProvider)
	if !ok || !poller.SupportsPolling(cfg) {
		cancel()
		if s.deps.Backend != nil {
			s.logf("connector %s: provider %q has no active polling transport in this host", cfg.ID, cfg.Provider)
		}
		s.recordLog(LogEntry{
			Level:       LogLevelInfo,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Summary:     "启动跳过：当前配置无轮询传输（等待 webhook 入站）",
		}, logCounters{})
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
	releaseOnExit = false

	s.recordLog(LogEntry{
		Level:       LogLevelInfo,
		Direction:   LogDirectionSystem,
		ConnectorID: cfg.ID,
		Summary:     fmt.Sprintf("已启动轮询（provider=%s）", cfg.Provider),
	}, logCounters{})
	go s.heartbeatOwner(runCtx, cfg.ID, cancel)
	go func() {
		s.logf("connector %s: polling started (provider=%s)", cfg.ID, cfg.Provider)
		err := poller.RunPolling(runCtx, cfg, s.HandleInbound, s.deps.Store.PollingState(cfg.ID))
		if err != nil && runCtx.Err() == nil {
			s.logf("connector %s: polling stopped: %v", cfg.ID, err)
			s.recordLog(LogEntry{
				Level:       LogLevelWarn,
				Direction:   LogDirectionSystem,
				ConnectorID: cfg.ID,
				Summary:     "轮询异常停止：" + err.Error(),
			}, logCounters{})
		} else if err == nil && runCtx.Err() == nil {
			s.recordLog(LogEntry{
				Level:       LogLevelWarn,
				Direction:   LogDirectionSystem,
				ConnectorID: cfg.ID,
				Summary:     "轮询已退出（未收到停止信号）",
			}, logCounters{})
		}
		ours := false
		s.mu.Lock()
		// 仅在登记项仍属于本代际时清理；否则会误删 Reload/重启后的新登记。
		if current, ok := s.running[cfg.ID]; ok && current != nil && current.gen == run.gen {
			delete(s.running, cfg.ID)
			ours = true
		}
		s.mu.Unlock()
		if ours {
			// 轮询自然退出（非 Stop）：立即释放归属，让其它宿主可接管，
			// 不必等心跳超时。
			s.releaseOwner(cfg.ID)
		}
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

// acquireOwnership 是启动时的运行归属闸（07 §5.6），在 fail-closed 白名单闸之后：
//   - 未配置 HostID：跳过（兼容旧宿主/测试）；
//   - policy.runtime_owner 显式指定其它宿主：不认领（策略优先）；
//   - 其它情况尝试原子认领：已被存活宿主持有则返回 false，调用方只读配置、不轮询。
func (s *Service) acquireOwnership(cfg Config) bool {
	hostID := s.hostID()
	if hostID == "" || s.deps.Store == nil {
		return true
	}
	if !HostMatchesPolicy(hostID, cfg.Policy.RuntimeOwner) {
		s.logf("connector %s: 未运行（由 %s 持有；policy.runtime_owner=%s）", cfg.ID, strings.TrimSpace(cfg.Policy.RuntimeOwner), strings.TrimSpace(cfg.Policy.RuntimeOwner))
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Summary:     fmt.Sprintf("未运行（由 %s 持有；policy.runtime_owner=%s）", strings.TrimSpace(cfg.Policy.RuntimeOwner), strings.TrimSpace(cfg.Policy.RuntimeOwner)),
		}, logCounters{})
		return false
	}
	claimed, owner, err := s.deps.Store.ClaimConnectorOwner(cfg.ID, hostID, time.Now().UTC(), s.ownerStaleAfter())
	if err != nil {
		// 归属账本不可读写时宁可跳过启动：双宿主同时轮询的破坏性大于停跑。
		s.logf("connector %s: 认领运行归属失败，跳过启动: %v", cfg.ID, err)
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Summary:     "未运行：认领运行归属失败：" + err.Error(),
		}, logCounters{})
		return false
	}
	if !claimed {
		if strings.TrimSpace(owner) == "" {
			owner = "其它宿主"
		}
		s.logf("connector %s: 未运行（由 %s 持有）", cfg.ID, owner)
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Summary:     fmt.Sprintf("未运行（由 %s 持有）", owner),
		}, logCounters{})
		return false
	}
	return true
}

// releaseOwner 尽力释放本宿主对连接器的运行归属（幂等；非本宿主持有时 no-op）。
func (s *Service) releaseOwner(connectorID string) {
	hostID := s.hostID()
	if hostID == "" || s.deps.Store == nil {
		return
	}
	if err := s.deps.Store.ReleaseConnectorOwner(connectorID, hostID); err != nil {
		s.logf("connector %s: 释放运行归属失败: %v", connectorID, err)
	}
}

// heartbeatOwner 在轮询生命周期内周期性续约运行归属；一旦发现归属已被他人
// 接管/释放（例如管理面「抢占」或陈旧接管），立即取消本实例轮询（尽力围栏）。
func (s *Service) heartbeatOwner(ctx context.Context, connectorID string, cancel context.CancelFunc) {
	hostID := s.hostID()
	if hostID == "" || s.deps.Store == nil {
		return
	}
	ticker := time.NewTicker(s.ownerHeartbeatInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			held, err := s.deps.Store.HeartbeatConnectorOwner(connectorID, hostID, time.Now().UTC())
			if err != nil {
				s.logf("connector %s: 刷新运行归属心跳失败: %v", connectorID, err)
				s.recordLog(LogEntry{
					Level:       LogLevelWarn,
					Direction:   LogDirectionSystem,
					ConnectorID: connectorID,
					Summary:     "刷新运行归属心跳失败：" + err.Error(),
				}, logCounters{})
				continue
			}
			if !held {
				s.logf("connector %s: 运行归属已被接管，停止本宿主轮询", connectorID)
				s.recordLog(LogEntry{
					Level:       LogLevelWarn,
					Direction:   LogDirectionSystem,
					ConnectorID: connectorID,
					Summary:     "运行归属已被接管，本宿主轮询停止",
				}, logCounters{})
				if cancel != nil {
					cancel()
				}
				return
			}
		}
	}
}

func (s *Service) hostID() string {
	return strings.TrimSpace(s.deps.HostID)
}

func (s *Service) ownerStaleAfter() time.Duration {
	if s.deps.OwnerStaleAfter > 0 {
		return s.deps.OwnerStaleAfter
	}
	return DefaultOwnerStaleAfter
}

func (s *Service) ownerHeartbeatInterval() time.Duration {
	if s.deps.OwnerHeartbeatInterval > 0 {
		return s.deps.OwnerHeartbeatInterval
	}
	return DefaultOwnerHeartbeatInterval
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
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "已拒绝非授权用户 " + msg.UserID + " 的消息（白名单未命中）",
		}, logCounters{})
		return nil
	}
	taskCtx := s.baseContext()
	dedupeKey := inboundDedupeKey(msg)
	key := cfg.ID + "|" + msg.Chat
	err := s.queue.enqueue(key, func() {
		if dedupeKey != "" {
			claimed, err := s.deps.Store.ClaimInbound(cfg.ID, dedupeKey)
			if err != nil {
				// 认领失败仅记日志；是否处理仍以 claimed 为准：claimed=true 表示
				// 本实例认领成功（账本可能未持久化，宁可能重复也不丢），
				// claimed=false 表示账本已存在该键（重复投递），必须跳过。
				s.logf("connector %s: claim inbound %q failed: %v (claimed=%v)", cfg.ID, dedupeKey, err, claimed)
			}
			if !claimed {
				// 平台重试/重复投递：幂等忽略（可观测计数与日志）。
				s.recordLog(LogEntry{
					Level:       LogLevelInfo,
					Direction:   LogDirectionIn,
					ConnectorID: cfg.ID,
					Chat:        msg.Chat,
					Summary:     "重复投递已跳过（去重键 " + dedupeKey + "）",
				}, logCounters{dedupSkipped: true})
				return
			}
		}
		s.process(taskCtx, cfg, provider, msg)
	})
	switch {
	case err == nil:
		counters := logCounters{inbound: true}
		if isCoreCommand(cfg.Policy.commandPrefix(), msg.Text) {
			// 核心本地命令（含审批/提问）不计入普通 turn 计数（FR-7）；
			// 命令处理路径自身会写 info/warn 日志。
			counters = logCounters{}
		}
		s.recordLog(LogEntry{
			Level:       LogLevelDebug,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "入站消息已入队",
		}, counters)
	case errors.Is(err, ErrQueueFull):
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "会话队列已满，入站消息被拒绝（宿主应让平台重试）",
		}, logCounters{queueFull: true})
	}
	return err
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
	if err := provider.Send(ctx, cfg, chat, msgs); err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionOut,
			ConnectorID: connectorID,
			Chat:        chat,
			Summary:     "主动发送失败：" + err.Error(),
		}, logCounters{})
		return err
	}
	return nil
}

// NotifySession 把消息推送给该宿主会话绑定的全部 IM 会话（注意力出站助手，
// docs/bots/01-requirements.md FR-8）。宿主在自己的注意力/监督事件里调用。
//
// 语义：
//   - 通过 Store.ListBindings 反查 session 的全部绑定（可跨多个连接器）；
//   - 逐个 SendOutbound；无绑定或 session 为零值时返回 (0, nil)；
//   - 单个连接器失败不阻断其它绑定，错误用 errors.Join 聚合返回；
//   - delivered 是成功投递的绑定数（不是消息条数）。
//
// 去重/降级不在本方法：同一事件是否已投递、是否与 Web Push/ntfy/收件箱重复，
// 由调用方（或后续 notify 切片）负责，核心只做「找绑定 → 出站」。
func (s *Service) NotifySession(ctx context.Context, session SessionRef, msgs []OutboundMessage) (int, error) {
	if s == nil || s.deps.Store == nil {
		return 0, ErrServiceNotConfigured
	}
	if session.IsZero() || len(msgs) == 0 {
		return 0, nil
	}
	delivered := 0
	var errs []error
	for _, binding := range s.deps.Store.ListBindings("") {
		if binding.Session != session {
			continue
		}
		if err := s.SendOutbound(ctx, binding.ConnectorID, binding.Chat, msgs); err != nil {
			errs = append(errs, fmt.Errorf("connector %s chat %s: %w", binding.ConnectorID, binding.Chat, err))
			continue
		}
		delivered++
	}
	return delivered, errors.Join(errs...)
}

// NotifyAttention 是 NotifySession 的轻量封装：把一条注意力通知
// （approval/question/completion/error）推给目标会话的全部 IM 绑定。
//
// 仅做「找绑定 → 出站」：优先级/免打扰/合并/跨通道去重由调用方（或后续 notify
// 切片）负责，本方法不记录投递账本；Kind 只用于日志（docs/bots/01-requirements.md
// FR-8）。
func (s *Service) NotifyAttention(ctx context.Context, notice AttentionNotice) error {
	if notice.Session.IsZero() {
		return errors.New("connectors: attention notice requires a session")
	}
	text := strings.TrimSpace(notice.Text)
	if text == "" {
		return errors.New("connectors: attention notice text is required")
	}
	delivered, err := s.NotifySession(ctx, notice.Session, []OutboundMessage{{Text: text}})
	if err == nil {
		s.logf("connector attention %s: delivered to %d binding(s)", strings.TrimSpace(notice.Kind), delivered)
	}
	return err
}

// NotifyAttentionScoped 是 D5 scope=all 的完整投递（2026-10-14 修复，docs/bots/09
// §7.4.1）：先按事件会话精确投递（绑定场景——该会话的绑定 chats，旧语义不变）；
// 未命中任何绑定（"其它会话"的广播事件）时，回退把通知广播给各接受连接器
// （connectorIDs）的全部绑定 chats——修复前该场景过滤通过但投递 0 目标（静默丢失）。
// 接受连接器集合由 SubscribeAttention 的过滤循环给出（与入站准入同一次判定）。
func (s *Service) NotifyAttentionScoped(ctx context.Context, connectorIDs []string, notice AttentionNotice) error {
	if notice.Session.IsZero() {
		return errors.New("connectors: attention notice requires a session")
	}
	text := strings.TrimSpace(notice.Text)
	if text == "" {
		return errors.New("connectors: attention notice text is required")
	}
	msgs := []OutboundMessage{{Text: text}}
	if delivered, err := s.NotifySession(ctx, notice.Session, msgs); err != nil || delivered > 0 {
		if err == nil {
			s.logf("connector attention %s: delivered to %d binding(s) (session-bound)", strings.TrimSpace(notice.Kind), delivered)
		}
		return err
	}
	// 未绑定（scope=all 广播）：投给接受连接器的全部绑定 chats（去重防同 chat 双发）。
	if s == nil || s.deps.Store == nil {
		return ErrServiceNotConfigured
	}
	seen := make(map[string]struct{})
	delivered := 0
	var errs []error
	for _, connectorID := range connectorIDs {
		for _, binding := range s.deps.Store.ListBindings(connectorID) {
			key := binding.ConnectorID + "\x00" + binding.Chat
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if err := s.SendOutbound(ctx, binding.ConnectorID, binding.Chat, msgs); err != nil {
				errs = append(errs, fmt.Errorf("connector %s chat %s: %w", binding.ConnectorID, binding.Chat, err))
				continue
			}
			delivered++
		}
	}
	if len(errs) == 0 {
		s.logf("connector attention %s: broadcast to %d chat(s) of %d connector(s)", strings.TrimSpace(notice.Kind), delivered, len(connectorIDs))
	}
	return errors.Join(errs...)
}

func (s *Service) process(ctx context.Context, cfg Config, provider Provider, msg InboundMessage) {
	if err := s.processMessage(ctx, cfg, provider, msg); err != nil {
		s.logf("connector %s: message %s failed: %v", cfg.ID, msg.MessageID, err)
	}
}

func (s *Service) processMessage(ctx context.Context, cfg Config, provider Provider, msg InboundMessage) error {
	s.recordLog(LogEntry{
		Level:       LogLevelDebug,
		Direction:   LogDirectionIn,
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Summary:     "开始处理入站消息",
	}, logCounters{})

	prefix := cfg.Policy.commandPrefix()
	if cmd, ok := parseCommand(prefix, msg.Text); ok {
		handled, err := s.handleCommand(ctx, cfg, provider, msg, cmd)
		if err != nil || handled {
			return err
		}
	}

	binding, _ := s.deps.Store.GetBinding(cfg.ID, msg.Chat)

	// P2 切片 B/C（docs/bots/09 §7.2）：按绑定状态决定执行位置（R1 亲和派发 /
	// R2a 接管 / R3 选举）。Peers/RemoteDispatch 未装配 = 单机模式，
	// 决策恒本机，行为与历史完全一致。
	decision, rrNext := s.decideRoute(cfg, binding.Session)
	if decision.Remote {
		if s.deps.RemoteDispatch != nil {
			return s.dispatchRemote(ctx, cfg, provider, msg, binding.Session, decision, rrNext)
		}
		// RemoteDispatch 未装配：回落本机执行（能力未装配不拒收）。
	} else {
		s.mu.Lock()
		s.runnerRoundRobin = rrNext
		s.mu.Unlock()
	}
	// R2a 接管语义（D3 前半，切片 C）：本机接管继续服务该绑定会话；执行成功
	// 后回写 NodeID 并 IM 提示一次（failoverNotice 置位后只发一次）。
	failoverNotice := decision.Failover && !decision.Remote

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
		Policy:      cfg.Policy,
	}, sink)
	if err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "后端处理失败：" + err.Error(),
		}, logCounters{})
		if streamer != nil {
			if failErr := streamer.Fail(ctx, failureText(err)); failErr != nil {
				s.recordLog(LogEntry{
					Level:       LogLevelError,
					Direction:   LogDirectionOut,
					ConnectorID: cfg.ID,
					Chat:        msg.Chat,
					Summary:     "失败回执发送失败：" + failErr.Error(),
				}, logCounters{})
			}
		}
		return err
	}

	if !result.Session.IsZero() && binding.Session != result.Session {
		if err := s.deps.Store.PutBinding(Binding{ConnectorID: cfg.ID, Chat: msg.Chat, Session: result.Session}); err != nil {
			s.logf("connector %s: persist binding failed: %v", cfg.ID, err)
		}
	}
	// R2a 接管完成（D3 前半，切片 C）：执行体已转移到本机——回写绑定 NodeID
	// 为本宿主身份（亲和转移，绑定记录不变）并在 IM 提示一次。回写只在
	// backend 未自行改 Session（改了则上面的常规回写已带新值）且原绑定确实
	// 指向别节点时进行。
	if failoverNotice && result.Session.NodeID != s.deps.HostID {
		updated := result.Session
		if updated.IsZero() {
			updated = binding.Session
		}
		updated.NodeID = s.deps.HostID
		if updated != binding.Session {
			if err := s.deps.Store.PutBinding(Binding{ConnectorID: cfg.ID, Chat: msg.Chat, Session: updated}); err != nil {
				s.logf("connector %s: persist failover binding failed: %v", cfg.ID, err)
			}
		}
		notice := "ℹ️ 原执行节点已下线，本节点接管继续服务该会话。"
		if err := provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: notice}}); err != nil {
			s.logf("connector %s: failover notice failed: %v", cfg.ID, err)
		}
		s.recordLog(LogEntry{
			Level:       LogLevelInfo,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "R2a 接管：原节点 " + strings.TrimSpace(binding.Session.NodeID) + " 掉线，本机继续服务（已回写 NodeID）",
		}, logCounters{})
	}
	if len(result.Reply) == 0 {
		if streamer != nil {
			if err := streamer.Final(ctx, nil); err != nil {
				s.recordLog(LogEntry{
					Level:       LogLevelError,
					Direction:   LogDirectionOut,
					ConnectorID: cfg.ID,
					Chat:        msg.Chat,
					Summary:     "回复投递失败：" + err.Error(),
				}, logCounters{})
				return err
			}
		}
		s.recordLog(LogEntry{
			Level:       LogLevelInfo,
			Direction:   LogDirectionOut,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "处理完成（无回复）",
		}, logCounters{})
		return nil
	}
	if streamer != nil {
		if err := streamer.Final(ctx, result.Reply); err != nil {
			s.recordLog(LogEntry{
				Level:       LogLevelError,
				Direction:   LogDirectionOut,
				ConnectorID: cfg.ID,
				Chat:        msg.Chat,
				Summary:     "回复投递失败：" + err.Error(),
			}, logCounters{})
			return err
		}
	} else if err := provider.Send(ctx, cfg, msg.Chat, result.Reply); err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionOut,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "回复投递失败：" + err.Error(),
		}, logCounters{})
		return err
	}
	s.recordLog(LogEntry{
		Level:       LogLevelInfo,
		Direction:   LogDirectionOut,
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Summary:     fmt.Sprintf("处理完成（回复 %d 字符）", replyTextLength(result.Reply)),
	}, logCounters{outbound: true})
	return nil
}

// replyTextLength 统计出站回复的字符数（rune），用于成功日志摘要。
func replyTextLength(msgs []OutboundMessage) int {
	total := 0
	for _, msg := range msgs {
		total += utf8.RuneCountInString(msg.Text)
	}
	return total
}

// decideRoute 包装 DecideRoute：取当前 mesh peers 视图与偏好配置，回写
// round-robin 游标。Peers 未装配 = 单机模式（peers 为空 → 决策恒本机）。
func (s *Service) decideRoute(cfg Config, binding SessionRef) (routeDecision, int) {
	var peers []RunnerPeer
	if s.deps.Peers != nil {
		peers = s.deps.Peers()
	}
	s.mu.Lock()
	cursor := s.runnerRoundRobin
	s.mu.Unlock()
	decision, rrNext := DecideRoute(cfg.Setting(RunnerPreferenceSettingKey, RunnerPreferenceDefault), peers, s.deps.HostID, binding, cursor)
	if !decision.Remote {
		s.mu.Lock()
		s.runnerRoundRobin = rrNext
		s.mu.Unlock()
	}
	return decision, rrNext
}

// dispatchRemote 执行远程派发（R1 亲和 / R3 远端）：v1 只回终态——
// 不走本地 streaming/typing（那两条只在 runner 侧存在）；回复与绑定回写
// 在本函数内完成，语义与本地路径一致。
func (s *Service) dispatchRemote(ctx context.Context, cfg Config, provider Provider, msg InboundMessage, binding SessionRef, decision routeDecision, rrNext int) error {
	s.mu.Lock()
	s.runnerRoundRobin = rrNext
	s.mu.Unlock()

	result, err := s.deps.RemoteDispatch(ctx, DispatchRequest{
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Text:        msg.Text,
		UserID:      msg.UserID,
		UserName:    msg.UserName,
		MessageID:   msg.MessageID,
		Target:      decision.Peer,
	})
	if err != nil {
		// 派发失败不静默吞消息：记日志后回落本机执行（可用性优先——远程
		// 节点瞬时故障不应让 IM 停摆；failover 语义与 gateway_node 一致）。
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionSystem,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "远程派发失败，回落本机执行：" + err.Error(),
		}, logCounters{})
		return s.processMessageLocal(ctx, cfg, provider, msg, Binding{ConnectorID: cfg.ID, Chat: msg.Chat, Session: binding})
	}
	// 回写绑定（runner 侧 auto-create 后 Session 会更新；R1 亲和时通常不变）。
	if !result.Session.IsZero() && binding != result.Session {
		if err := s.deps.Store.PutBinding(Binding{ConnectorID: cfg.ID, Chat: msg.Chat, Session: result.Session}); err != nil {
			s.logf("connector %s: persist dispatched binding failed: %v", cfg.ID, err)
		}
	}
	if len(result.Reply) > 0 {
		if err := provider.Send(ctx, cfg, msg.Chat, result.Reply); err != nil {
			s.recordLog(LogEntry{
				Level:       LogLevelError,
				Direction:   LogDirectionOut,
				ConnectorID: cfg.ID,
				Chat:        msg.Chat,
				Summary:     "回复投递失败：" + err.Error(),
			}, logCounters{})
			return err
		}
	}
	s.recordLog(LogEntry{
		Level:       LogLevelInfo,
		Direction:   LogDirectionOut,
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Summary:     "远程派发完成（runner=" + decision.Peer.NodeID + "，回复 " + strconv.Itoa(len(result.Reply)) + " 条）",
	}, logCounters{})
	return nil
}

// processMessageLocal 是原本地执行路径（远程派发失败回落用）；与
// processMessage 相同但跳过路由决策（防递归派发）。
func (s *Service) processMessageLocal(ctx context.Context, cfg Config, provider Provider, msg InboundMessage, binding Binding) error {
	prefix := cfg.Policy.commandPrefix()
	if cmd, ok := parseCommand(prefix, msg.Text); ok {
		handled, err := s.handleCommand(ctx, cfg, provider, msg, cmd)
		if err != nil || handled {
			return err
		}
	}
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
		Policy:      cfg.Policy,
	}, sink)
	if err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "后端处理失败：" + err.Error(),
		}, logCounters{})
		if streamer != nil {
			if failErr := streamer.Fail(ctx, failureText(err)); failErr != nil {
				s.recordLog(LogEntry{
					Level:       LogLevelError,
					Direction:   LogDirectionOut,
					ConnectorID: cfg.ID,
					Chat:        msg.Chat,
					Summary:     "失败回执发送失败：" + failErr.Error(),
				}, logCounters{})
			}
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
			if err := streamer.Final(ctx, nil); err != nil {
				return err
			}
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

// Logs 返回连接器最近的运行日志（倒序，最新在前）：
//   - limit<=0 用 DefaultLogQueryLimit（50），>MaxLogQueryLimit 收敛到 200；
//   - 未知连接器返回 (nil, false)；已知但尚无日志返回空切片（非 nil）。
//
// 日志环仅存于进程内（见 logs.go），删除连接器配置不会清理已有日志环。
func (s *Service) Logs(connectorID string, limit int) ([]LogEntry, bool) {
	if s == nil {
		return nil, false
	}
	id := strings.TrimSpace(connectorID)
	if id == "" || !s.connectorKnown(id) {
		return nil, false
	}
	if limit <= 0 {
		limit = DefaultLogQueryLimit
	}
	if limit > MaxLogQueryLimit {
		limit = MaxLogQueryLimit
	}
	ring := s.connectorLogRing(id)
	if ring == nil {
		return []LogEntry{}, true
	}
	return ring.snapshot(limit), true
}

// Stats 返回连接器的累计运行计数与最近错误；未知连接器返回 (零值, false)。
func (s *Service) Stats(connectorID string) (ConnectorStats, bool) {
	if s == nil {
		return ConnectorStats{}, false
	}
	id := strings.TrimSpace(connectorID)
	if id == "" || !s.connectorKnown(id) {
		return ConnectorStats{}, false
	}
	ring := s.connectorLogRing(id)
	if ring == nil {
		return ConnectorStats{}, true
	}
	return ring.statsSnapshot(), true
}

// connectorKnown 报告连接器 id 是否已配置（Store 未接线时退化为「日志环已注册」）。
func (s *Service) connectorKnown(id string) bool {
	if s.deps.Store != nil {
		_, ok := s.deps.Store.GetConfig(id)
		return ok
	}
	return s.connectorLogRing(id) != nil
}

func (s *Service) connectorLogRing(id string) *connectorLog {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return s.logBuffers[id]
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

// isCoreCommand 报告文本是否为核心本地命令：这些命令在 handleCommand 内闭环
// （help/new/status/approve/deny/answer），不进入 agent turn，因此不计入
// 普通 turn 计数（stats 的 inbound/outbound）。未知 slash 命令仍交还给
// Backend.Handle（可能是 agent 自己的命令），按普通消息计数。
func isCoreCommand(prefix, text string) bool {
	cmd, ok := parseCommand(prefix, text)
	if !ok {
		return false
	}
	switch cmd.name {
	case "help", "start", "new", "status", "approve", "deny", "answer":
		return true
	default:
		return false
	}
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
	case "approve", "deny", "answer":
		// 审批/提问命令（FR-7）：转给宿主 CommandBackend 闭环。
		// 命令路径不计入普通 turn 计数（stats 的 inbound/outbound 不递增）。
		return true, s.dispatchHostCommand(ctx, cfg, provider, msg, cmd)
	default:
		return false, nil
	}
}

// dispatchHostCommand 把 /approve、/deny、/answer 转给宿主（CommandBackend）。
//
// 语义（docs/bots/01-requirements.md FR-7 / 03-host-integration.md §6）：
//   - 核心只解析命令名与参数、补齐当前绑定会话；request_id/序号格式由宿主解释；
//   - 无参数 = “当前挂起项”语义，原样（空 Args）透传给宿主；
//   - 宿主未实现 CommandBackend 时回明确提示（不静默），命令不会落入普通 turn；
//   - 宿主返回 Handled=false 时也回明确提示，而不是让用户以为已处理。
func (s *Service) dispatchHostCommand(ctx context.Context, cfg Config, provider Provider, msg InboundMessage, cmd command) error {
	backend, ok := s.deps.Backend.(CommandBackend)
	if !ok {
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "审批/提问命令未闭环：宿主 Backend 未实现 CommandBackend（/" + cmd.name + "）",
		}, logCounters{})
		return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: commandUnavailableText(cmd.name)}})
	}
	binding, _ := s.deps.Store.GetBinding(cfg.ID, msg.Chat)
	req := CommandRequest{
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Session:     binding.Session,
		UserID:      msg.UserID,
		UserName:    msg.UserName,
		MessageID:   msg.MessageID,
		Command:     cmd.name,
		Args:        splitCommandArgs(cmd.args),
		Text:        strings.TrimSpace(cmd.args),
	}
	var (
		result CommandResult
		err    error
	)
	if cmd.name == "answer" {
		result, err = backend.AnswerQuestion(ctx, req)
	} else {
		result, err = backend.DecideApproval(ctx, req)
	}
	if err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "审批/提问命令处理失败（/" + cmd.name + "）：" + err.Error(),
		}, logCounters{})
		return provider.Send(ctx, cfg, msg.Chat, []OutboundMessage{{Text: failureText(err)}})
	}
	if !result.Handled {
		s.recordLog(LogEntry{
			Level:       LogLevelWarn,
			Direction:   LogDirectionIn,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "宿主未处理审批/提问命令（/" + cmd.name + "）：无挂起项或参数无法识别",
		}, logCounters{})
		if len(result.Reply) == 0 {
			result.Reply = []OutboundMessage{{Text: commandNotHandledText(cmd.name)}}
		}
	}
	if len(result.Reply) == 0 {
		s.recordLog(LogEntry{
			Level:       LogLevelInfo,
			Direction:   LogDirectionOut,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "审批/提问命令已由宿主处理（/" + cmd.name + "，无回复）",
		}, logCounters{})
		return nil
	}
	if err := provider.Send(ctx, cfg, msg.Chat, result.Reply); err != nil {
		s.recordLog(LogEntry{
			Level:       LogLevelError,
			Direction:   LogDirectionOut,
			ConnectorID: cfg.ID,
			Chat:        msg.Chat,
			Summary:     "审批/提问命令回复投递失败：" + err.Error(),
		}, logCounters{})
		return err
	}
	s.recordLog(LogEntry{
		Level:       LogLevelInfo,
		Direction:   LogDirectionOut,
		ConnectorID: cfg.ID,
		Chat:        msg.Chat,
		Summary:     "审批/提问命令已闭环（/" + cmd.name + "）",
	}, logCounters{})
	return nil
}

// splitCommandArgs 把命令参数切成空白分词；Text 保留原文，供宿主还原带空格的
// 回答内容（/answer 的 Args[0] 是目标，其余分词拼回即是内容）。
func splitCommandArgs(args string) []string {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil
	}
	return strings.Fields(args)
}

// commandUnavailableText 是宿主未实现 CommandBackend 时的明确提示（不静默）。
func commandUnavailableText(name string) string {
	if name == "answer" {
		return "当前宿主未接入提问回答命令（/answer）：请在 Web 控制台回答，或升级宿主后重试。"
	}
	return "当前宿主未接入审批命令（/" + name + "）：请在 Web 控制台处理，或升级宿主后重试。"
}

// commandNotHandledText 是宿主明确未处理（无挂起项/参数无法识别）时的提示。
func commandNotHandledText(name string) string {
	if name == "answer" {
		return "没有待回答的提问（或该序号/request_id 已失效）。"
	}
	return "没有待处理的审批（或该 request_id 已失效）。"
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
		prefix + "approve [request_id] 批准挂起的审批（缺省 = 当前挂起项）",
		prefix + "deny [request_id] 拒绝挂起的审批（缺省 = 当前挂起项）",
		prefix + "answer <序号|request_id> <内容> 回答挂起的提问",
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
