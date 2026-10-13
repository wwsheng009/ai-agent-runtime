package connectors

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// Store 是连接器的宿主存储接口（docs/bots/06-target-architecture.md §2.2）：
// gateway 用 data-dir JSON、aicli/runtime-server 用共享的 config.yaml + 账本文件，
// 内存实现服务于测试与本地最小运行。
type Store interface {
	// ListConfigs 返回全部通道配置（含停用项，由 Service 过滤）。
	ListConfigs() []Config
	// GetConfig 按 id 读取配置。
	GetConfig(id string) (Config, bool)
	// PutConfig 新建或覆盖配置（密钥应由调用方经 api.MergeSubmit 处理后再落盘）。
	PutConfig(cfg Config) error
	// DeleteConfig 删除配置（实现可级联清理绑定与轮询游标）。
	DeleteConfig(id string) error

	// GetBinding 读取 IM 会话 → 宿主会话绑定。
	GetBinding(connectorID, chat string) (Binding, bool)
	// PutBinding 写入/覆盖绑定。
	PutBinding(binding Binding) error

	// ClaimInbound 认领一条入站消息（幂等去重）：首次返回 true，重复返回 false。
	//
	// 键语义（Fix-2）：messageID 参数是调用方拼好的去重键，语义为
	// connector + chat + messageID —— telegram 等平台的 message_id 仅在单个
	// chat 内唯一，跨会话复用同一 ID 是常态，调用方必须把 chat 并入键；
	// 实现只按 connectorID + messageID 原样存储/查询，不解析键内容。
	ClaimInbound(connectorID, messageID string) (bool, error)

	// PollingState 返回该连接器的轮询游标视图（持久化由宿主实现决定）。
	PollingState(connectorID string) PollingState

	// ClaimConnectorOwner 原子认领连接器运行归属（docs/bots/07-config-ux.md §5.6）：
	// 「读旧值 → 判存活 → 写新值」在单次存储事务内完成（storefile 在数据目录锁内，
	// 可跨进程互斥）。
	//   - 无归属、归属已陈旧（now-HeartbeatAt > staleAfter）或归属就是 hostID：
	//     写入/刷新本宿主记录并返回 (true, hostID, nil)；
	//   - 归属为其它存活宿主：返回 (false, 当前持有者, nil)，调用方只读配置、
	//     不启动轮询。
	//
	// staleAfter<=0 用 DefaultOwnerStaleAfter；now 为零值时取当前 UTC 时间。
	ClaimConnectorOwner(connectorID, hostID string, now time.Time, staleAfter time.Duration) (claimed bool, owner string, err error)
	// ForceClaimConnectorOwner 强制覆盖旧归属（管理面「抢占」）：不判存活、不判身份，
	// 直接以 hostID 写入并刷新心跳；陈旧判断交由调用方。
	ForceClaimConnectorOwner(connectorID, hostID string, now time.Time) error
	// HeartbeatConnectorOwner 续约：仅当记录仍属于 hostID 时刷新心跳。
	// held=false 表示归属已被他人接管/释放（调用方应停止本宿主轮询）。
	HeartbeatConnectorOwner(connectorID, hostID string, now time.Time) (held bool, err error)
	// ReleaseConnectorOwner 尽力释放归属：仅当当前持有者是 hostID 时删除（幂等）。
	ReleaseConnectorOwner(connectorID, hostID string) error
	// OwnerOf 返回当前归属记录（含已陈旧记录；无记录返回 false）。只读。
	OwnerOf(connectorID string) (OwnerRecord, bool)
}

// DefaultOwnerStaleAfter 是运行归属心跳的默认超时：超过该时长未刷新即视为陈旧
// （持有宿主已退出/挂起），后来者可接管。宿主可用 Deps.OwnerStaleAfter 覆盖。
const DefaultOwnerStaleAfter = 90 * time.Second

// OwnerRecord 是一次运行归属的持久化记录（storefile 落在 state.json 的 owners）。
type OwnerRecord struct {
	// Owner 是持有运行权的宿主身份（aicli|runtime-server|gateway 或测试用标识）。
	Owner string
	// HeartbeatAt 是最近一次心跳时间；超过阈值未刷新即可被接管。
	HeartbeatAt time.Time
	// PID 是认领进程 id（诊断用）。
	PID int
}

// Expired 报告归属记录是否已超过 staleAfter 未刷新（staleAfter<=0 用默认值）。
func (r OwnerRecord) Expired(now time.Time, staleAfter time.Duration) bool {
	if staleAfter <= 0 {
		staleAfter = DefaultOwnerStaleAfter
	}
	if r.HeartbeatAt.IsZero() {
		return true
	}
	return now.Sub(r.HeartbeatAt) > staleAfter
}

// MemoryStore 是进程内实现（测试/单机最小运行）。
type MemoryStore struct {
	mu       sync.Mutex
	configs  map[string]Config
	bindings map[string]Binding
	claimed  map[string]time.Time
	states   map[string]map[string]string
	owners   map[string]OwnerRecord
}

// NewMemoryStore 构造内存存储，可预置配置。
func NewMemoryStore(configs ...Config) *MemoryStore {
	store := &MemoryStore{
		configs:  make(map[string]Config, len(configs)),
		bindings: make(map[string]Binding),
		claimed:  make(map[string]time.Time),
		states:   make(map[string]map[string]string),
		owners:   make(map[string]OwnerRecord),
	}
	for _, cfg := range configs {
		store.configs[cfg.ID] = cfg
	}
	return store
}

// PutConfig 写入/覆盖配置（宿主保存配置时调用；内存实现直接替换）。
func (s *MemoryStore) PutConfig(cfg Config) error {
	if strings.TrimSpace(cfg.ID) == "" {
		return errors.New("connectors: config id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[cfg.ID] = cfg
	return nil
}

// DeleteConfig 删除配置并级联清理该连接器的绑定与轮询游标。
func (s *MemoryStore) DeleteConfig(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.configs, id)
	delete(s.states, id)
	delete(s.owners, id)
	prefix := strings.TrimSpace(id) + "|"
	for key := range s.bindings {
		if strings.HasPrefix(key, prefix) {
			delete(s.bindings, key)
		}
	}
	return nil
}

func (s *MemoryStore) ListConfigs() []Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Config, 0, len(s.configs))
	for _, cfg := range s.configs {
		out = append(out, cfg)
	}
	return out
}

func (s *MemoryStore) GetConfig(id string) (Config, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, ok := s.configs[id]
	return cfg, ok
}

func (s *MemoryStore) GetBinding(connectorID, chat string) (Binding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[bindingKey(connectorID, chat)]
	return binding, ok
}

func (s *MemoryStore) PutBinding(binding Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if binding.UpdatedAt.IsZero() {
		binding.UpdatedAt = time.Now()
	}
	s.bindings[bindingKey(binding.ConnectorID, binding.Chat)] = binding
	return nil
}

func (s *MemoryStore) ClaimInbound(connectorID, messageID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := bindingKey(connectorID, messageID)
	if _, exists := s.claimed[key]; exists {
		return false, nil
	}
	s.claimed[key] = time.Now()
	return true, nil
}

func (s *MemoryStore) PollingState(connectorID string) PollingState {
	s.mu.Lock()
	defer s.mu.Unlock()
	values, ok := s.states[connectorID]
	if !ok {
		values = make(map[string]string)
		s.states[connectorID] = values
	}
	return &memoryPollingState{store: s, connectorID: connectorID}
}

// ClaimConnectorOwner 原子认领运行归属（内存实现；语义见 Store 接口）。
func (s *MemoryStore) ClaimConnectorOwner(connectorID, hostID string, now time.Time, staleAfter time.Duration) (bool, string, error) {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return false, "", errors.New("connectors: connector id and host id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, exists := s.owners[connectorID]; exists && record.Owner != hostID && !record.Expired(now, staleAfter) {
		return false, record.Owner, nil
	}
	s.owners[connectorID] = OwnerRecord{Owner: hostID, HeartbeatAt: now, PID: os.Getpid()}
	return true, hostID, nil
}

// ForceClaimConnectorOwner 强制覆盖归属（内存实现）。
func (s *MemoryStore) ForceClaimConnectorOwner(connectorID, hostID string, now time.Time) error {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return errors.New("connectors: connector id and host id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owners[connectorID] = OwnerRecord{Owner: hostID, HeartbeatAt: now, PID: os.Getpid()}
	return nil
}

// HeartbeatConnectorOwner 续约（内存实现）。
func (s *MemoryStore) HeartbeatConnectorOwner(connectorID, hostID string, now time.Time) (bool, error) {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return false, errors.New("connectors: connector id and host id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.owners[connectorID]
	if !exists || record.Owner != hostID {
		return false, nil
	}
	record.HeartbeatAt = now
	record.PID = os.Getpid()
	s.owners[connectorID] = record
	return true, nil
}

// ReleaseConnectorOwner 释放归属（内存实现；仅持有者可释放，幂等）。
func (s *MemoryStore) ReleaseConnectorOwner(connectorID, hostID string) error {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, exists := s.owners[connectorID]; exists && record.Owner == hostID {
		delete(s.owners, connectorID)
	}
	return nil
}

// OwnerOf 返回当前归属记录（内存实现）。
func (s *MemoryStore) OwnerOf(connectorID string) (OwnerRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.owners[strings.TrimSpace(connectorID)]
	return record, ok
}

func bindingKey(left, right string) string {
	return strings.TrimSpace(left) + "|" + strings.TrimSpace(right)
}

type memoryPollingState struct {
	store       *MemoryStore
	connectorID string
}

func (m *memoryPollingState) Get(key string) (string, bool) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	values := m.store.states[m.connectorID]
	value, ok := values[key]
	return value, ok
}

func (m *memoryPollingState) Set(key, value string) error {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	values := m.store.states[m.connectorID]
	if values == nil {
		values = make(map[string]string)
		m.store.states[m.connectorID] = values
	}
	values[key] = value
	return nil
}
