package connectors

import (
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

	// GetBinding 读取 IM 会话 → 宿主会话绑定。
	GetBinding(connectorID, chat string) (Binding, bool)
	// PutBinding 写入/覆盖绑定。
	PutBinding(binding Binding) error

	// ClaimInbound 认领一条入站消息（幂等去重）：首次返回 true，重复返回 false。
	ClaimInbound(connectorID, messageID string) (bool, error)

	// PollingState 返回该连接器的轮询游标视图（持久化由宿主实现决定）。
	PollingState(connectorID string) PollingState
}

// MemoryStore 是进程内实现（测试/单机最小运行）。
type MemoryStore struct {
	mu       sync.Mutex
	configs  map[string]Config
	bindings map[string]Binding
	claimed  map[string]time.Time
	states   map[string]map[string]string
}

// NewMemoryStore 构造内存存储，可预置配置。
func NewMemoryStore(configs ...Config) *MemoryStore {
	store := &MemoryStore{
		configs:  make(map[string]Config, len(configs)),
		bindings: make(map[string]Binding),
		claimed:  make(map[string]time.Time),
		states:   make(map[string]map[string]string),
	}
	for _, cfg := range configs {
		store.configs[cfg.ID] = cfg
	}
	return store
}

// PutConfig 写入/覆盖配置（宿主保存配置时调用；内存实现直接替换）。
func (s *MemoryStore) PutConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[cfg.ID] = cfg
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
