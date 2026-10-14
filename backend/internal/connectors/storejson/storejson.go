// Package storejson 是 connectors.Store 的 gateway 专属实现：把通道配置（含密钥）
// 写进数据目录的 connectors.json（0600），把绑定/去重账本/轮询游标/运行归属写进
// connectors-state.json（0600）。
//
// 设计（docs/bots/03-host-integration.md §7 / docs/bots/06-target-architecture.md
// §2.2）：
//   - gateway 使用 --data-dir 作为唯一数据根（与 devices.json / push.json /
//     registry.json 同级），不读写 aicli/runtime-server 共享的 config.yaml；
//   - 密钥与配置同文件（0600，仅 owner 可读）——gateway 没有用户可编辑的
//     config.yaml，拆文件只增加同步/备份的失误面；权限即边界；
//   - 运行时状态不进配置文件（避免每次轮询重写配置、也避免游标污染配置）；
//   - 所有写入先抢数据目录锁（复用 storefile.AcquireDirLock），再「重读 → 改 →
//     临时文件 + rename 原子替换」，两个 gateway 实例指向同一 data-dir 时也不会
//     丢更新。
package storejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/statepath"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
)

const (
	// DefaultConfigFileName 是连接器配置文件名（含密钥，0600）。
	DefaultConfigFileName = "connectors.json"
	// DefaultStateFileName 是连接器运行时状态文件名（绑定/账本/游标/归属，0600）。
	DefaultStateFileName = "connectors-state.json"
	// dirLockName 是数据目录锁名（与 storefile 同名，但位于 gateway 的 data-dir）。
	dirLockName = "connectors.lock"
)

// Store 是 gateway 数据目录下的连接器存储（实现 connectors.Store）。
type Store struct {
	configPath string
	statePath  string
	// legacyConfigHints 是历史 config.yaml 路径（迁移扫描用；D1 迁移据此枚举
	// 旧 state 候选）。宿主经 WithLegacyConfigHints 注入，缺省只扫 cwd 候选。
	legacyConfigHints []string

	mu sync.Mutex
	// lockOptions 是数据目录锁参数；零值由 storefile 的默认值补齐。
	lockOptions storefile.LockOptions
}

// New 构造 gateway 存储：配置与状态都位于 dataDir 下（目录不存在时写入自动创建）。
func New(dataDir string) *Store {
	dataDir = strings.TrimSpace(dataDir)
	return &Store{
		configPath: filepath.Join(dataDir, DefaultConfigFileName),
		statePath:  filepath.Join(dataDir, DefaultStateFileName),
	}
}

// NewWithStatePath 构造存储并显式指定 state 路径（runtime-server / aicli 宿主用：
// state 经 statepath.CanonicalStatePath 锚定用户级目录，与配置文件不同位）。
// 空 statePath 退回 New(dataDir) 的同目录布局。
func NewWithStatePath(dataDir, statePath string) *Store {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return New(dataDir)
	}
	dataDir = strings.TrimSpace(dataDir)
	return &Store{
		configPath: filepath.Join(dataDir, DefaultConfigFileName),
		statePath:  filepath.Clean(statePath),
	}
}

// ConfigPath 返回配置文件路径（诊断/测试用）。
func (s *Store) ConfigPath() string {
	if s == nil {
		return ""
	}
	return s.configPath
}

// StatePath 返回状态文件路径（诊断/测试用）。
func (s *Store) StatePath() string {
	if s == nil {
		return ""
	}
	return s.statePath
}

// DirLockPath 返回数据目录锁路径（configPath 为空时为 ""，此时不抢锁）。
func (s *Store) DirLockPath() string {
	if s == nil || strings.TrimSpace(s.configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.configPath), dirLockName)
}

// WithLockOptions 覆盖数据目录锁参数（链式；测试缩短 StaleAfter 用）。
func (s *Store) WithLockOptions(opts storefile.LockOptions) *Store {
	if s != nil {
		s.lockOptions = opts
	}
	return s
}

// WithLegacyConfigHints 注入历史 config.yaml 路径（链式；D1 迁移据此枚举
// "configPath 同级 data/connectors" 候选）。
func (s *Store) WithLegacyConfigHints(configPaths ...string) *Store {
	if s != nil {
		s.legacyConfigHints = append(s.legacyConfigHints, configPaths...)
	}
	return s
}

// ---------------------------------------------------------------------------
// 配置（connectors.json：配置 + 密钥同文件，0600）

type configFile struct {
	Configs map[string]connectorRecord `json:"configs"`
}

// connectorRecord 是配置文件中的一条通道记录（字段与 docs/bots/07-config-ux.md
// §5.1 的 snake_case 形状一致）。
type connectorRecord struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Name     string `json:"name,omitempty"`
	Enabled  bool   `json:"enabled"`
	Mode     string `json:"mode,omitempty"`
	// 生命周期时间戳（RFC3339 UTC；零值省略 = 旧数据未打戳）。
	CreatedAt time.Time         `json:"created_at,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
	Settings  map[string]string `json:"settings,omitempty"`
	// Secrets 与配置同文件（0600）：gateway 无独立密钥文件，注释见包文档。
	Secrets map[string]string `json:"secrets,omitempty"`
	Policy  policyRecord      `json:"policy,omitempty"`
}

type policyRecord struct {
	CommandPrefix string   `json:"command_prefix,omitempty"`
	AllowedUsers  []string `json:"allowed_users,omitempty"`
	SessionMode   string   `json:"session_mode,omitempty"`
	ReplyMode     string   `json:"reply_mode,omitempty"`
	RuntimeOwner  string   `json:"runtime_owner,omitempty"`
	Route         *routeRecord `json:"route,omitempty"`
	Notify        *notifyRecord `json:"notify,omitempty"`
}

// routeRecord 是 D6 连接器级模型路由（docs/bots/09 §5.5）。
type routeRecord struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// notifyRecord 是 D5 事件通知接收配置（docs/bots/09 §7.4.1）。
type notifyRecord struct {
	Enabled bool     `json:"enabled"`
	Kinds   []string `json:"kinds,omitempty"`
	Scope   string   `json:"scope,omitempty"`
}

func recordFromConfig(cfg connectors.Config) connectorRecord {
	record := connectorRecord{
		ID:        strings.TrimSpace(cfg.ID),
		Provider:  strings.TrimSpace(cfg.Provider),
		Name:      strings.TrimSpace(cfg.Name),
		Enabled:   cfg.Enabled,
		Mode:      strings.TrimSpace(cfg.Mode),
		CreatedAt: cfg.CreatedAt,
		UpdatedAt: cfg.UpdatedAt,
		Settings:  cfg.Settings,
		Secrets:   cfg.Secrets,
	}
	if len(cfg.Policy.AllowedUsers) > 0 {
		record.Policy.AllowedUsers = append([]string(nil), cfg.Policy.AllowedUsers...)
	}
	record.Policy.CommandPrefix = strings.TrimSpace(cfg.Policy.CommandPrefix)
	record.Policy.SessionMode = strings.TrimSpace(cfg.Policy.SessionMode)
	record.Policy.ReplyMode = strings.TrimSpace(cfg.Policy.ReplyMode)
	record.Policy.RuntimeOwner = strings.TrimSpace(cfg.Policy.RuntimeOwner)
	if cfg.Policy.Route.HasOverride() {
		record.Policy.Route = &routeRecord{
			Provider:        strings.TrimSpace(cfg.Policy.Route.Provider),
			Model:           strings.TrimSpace(cfg.Policy.Route.Model),
			ReasoningEffort: strings.TrimSpace(cfg.Policy.Route.ReasoningEffort),
		}
	}
	if cfg.Policy.Notify.Enabled {
		record.Policy.Notify = &notifyRecord{
			Enabled: true,
			Kinds:   append([]string(nil), cfg.Policy.Notify.Kinds...),
			Scope:   strings.TrimSpace(cfg.Policy.Notify.Scope),
		}
	}
	return record
}

func (r connectorRecord) toConfig() connectors.Config {
	config := connectors.Config{
		ID:        strings.TrimSpace(r.ID),
		Provider:  strings.TrimSpace(r.Provider),
		Name:      strings.TrimSpace(r.Name),
		Enabled:   r.Enabled,
		Mode:      strings.TrimSpace(r.Mode),
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
		Settings:  r.Settings,
		Secrets:   r.Secrets,
		Policy: connectors.Policy{
			CommandPrefix: strings.TrimSpace(r.Policy.CommandPrefix),
			AllowedUsers:  r.Policy.AllowedUsers,
			SessionMode:   strings.TrimSpace(r.Policy.SessionMode),
			ReplyMode:     strings.TrimSpace(r.Policy.ReplyMode),
			RuntimeOwner:  strings.TrimSpace(r.Policy.RuntimeOwner),
		},
	}
	if r.Policy.Route != nil {
		config.Policy.Route = connectors.RoutePolicy{
			Provider:        strings.TrimSpace(r.Policy.Route.Provider),
			Model:           strings.TrimSpace(r.Policy.Route.Model),
			ReasoningEffort: strings.TrimSpace(r.Policy.Route.ReasoningEffort),
		}
	}
	if r.Policy.Notify != nil {
		config.Policy.Notify = connectors.NotifyPolicy{
			Enabled: r.Policy.Notify.Enabled,
			Kinds:   r.Policy.Notify.Kinds,
			Scope:   strings.TrimSpace(r.Policy.Notify.Scope),
		}
	}
	return config
}

// ListConfigs 返回全部通道配置（含停用项；密钥按存储原样返回）。
func (s *Store) ListConfigs() []connectors.Config {
	if s == nil || strings.TrimSpace(s.configPath) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.loadConfigLocked()
	if err != nil {
		return nil
	}
	out := make([]connectors.Config, 0, len(file.Configs))
	for _, record := range file.Configs {
		if cfg := record.toConfig(); cfg.ID != "" {
			out = append(out, cfg)
		}
	}
	return out
}

// GetConfig 按 id 读取配置。
func (s *Store) GetConfig(id string) (connectors.Config, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return connectors.Config{}, false
	}
	for _, cfg := range s.ListConfigs() {
		if cfg.ID == id {
			return cfg, true
		}
	}
	return connectors.Config{}, false
}

// PutConfig 新建或覆盖配置（密钥随配置同文件落盘，0600）。
func (s *Store) PutConfig(cfg connectors.Config) error {
	if s == nil || strings.TrimSpace(s.configPath) == "" {
		return errors.New("storejson: config path is not configured")
	}
	cfg.ID = strings.TrimSpace(cfg.ID)
	if cfg.ID == "" {
		return errors.New("storejson: connector id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlockDir, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir()
	// 锁内重读：另一个实例刚写入的配置不能被本次「整表写回」覆盖。
	file, err := s.loadConfigLocked()
	if err != nil {
		return err
	}
	file.Configs[cfg.ID] = recordFromConfig(cfg)
	return s.writeConfigLocked(file)
}

// DeleteConfig 删除配置，并级联清理该连接器的绑定/游标/归属/去重账本。
func (s *Store) DeleteConfig(id string) error {
	id = strings.TrimSpace(id)
	if s == nil || id == "" {
		return errors.New("storejson: connector id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlockDir, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir()
	file, err := s.loadConfigLocked()
	if err != nil {
		return err
	}
	if _, exists := file.Configs[id]; exists {
		delete(file.Configs, id)
		if err := s.writeConfigLocked(file); err != nil {
			return err
		}
	}
	state, err := s.loadStateLocked()
	if err != nil {
		return err
	}
	for key, record := range state.Bindings {
		if record.ConnectorID == id {
			delete(state.Bindings, key)
		}
	}
	delete(state.Cursors, id)
	delete(state.Owners, id)
	prefix := id + "\x00"
	for key := range state.Claimed {
		if strings.HasPrefix(key, prefix) {
			delete(state.Claimed, key)
		}
	}
	return s.writeStateLocked(state)
}

// ---------------------------------------------------------------------------
// 运行时状态（connectors-state.json：绑定/去重账本/游标/运行归属）

type stateFile struct {
	Bindings map[string]bindingRecord     `json:"bindings"`
	Claimed  map[string]time.Time         `json:"claimed"`
	Cursors  map[string]map[string]string `json:"cursors"`
	Owners   map[string]ownerRecord       `json:"owners,omitempty"`
}

type bindingRecord struct {
	ConnectorID string    `json:"connector_id"`
	Chat        string    `json:"chat"`
	NodeID      string    `json:"node_id,omitempty"`
	SessionID   string    `json:"session_id"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ownerRecord struct {
	Owner       string    `json:"owner"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	PID         int       `json:"pid,omitempty"`
}

func (r ownerRecord) expired(now time.Time, staleAfter time.Duration) bool {
	return connectors.OwnerRecord{Owner: r.Owner, HeartbeatAt: r.HeartbeatAt, PID: r.PID}.Expired(now, staleAfter)
}

// GetBinding 读取 IM 会话 → 宿主会话绑定。
func (s *Store) GetBinding(connectorID, chat string) (connectors.Binding, bool) {
	key := bindingKey(connectorID, chat)
	if key == "" {
		return connectors.Binding{}, false
	}
	var (
		record bindingRecord
		found  bool
	)
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, err := s.loadStateLocked(); err == nil {
		record, found = state.Bindings[key]
	}
	if !found {
		return connectors.Binding{}, false
	}
	return connectors.Binding{
		ConnectorID: record.ConnectorID,
		Chat:        record.Chat,
		Session:     connectors.SessionRef{NodeID: record.NodeID, SessionID: record.SessionID},
		UpdatedAt:   record.UpdatedAt,
	}, true
}

// PutBinding 写入/覆盖绑定。
func (s *Store) PutBinding(binding connectors.Binding) error {
	key := bindingKey(binding.ConnectorID, binding.Chat)
	if key == "" {
		return errors.New("storejson: binding key is required")
	}
	updated := binding.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	return s.mutateState(func(state *stateFile) {
		state.Bindings[key] = bindingRecord{
			ConnectorID: strings.TrimSpace(binding.ConnectorID),
			Chat:        strings.TrimSpace(binding.Chat),
			NodeID:      strings.TrimSpace(binding.Session.NodeID),
			SessionID:   strings.TrimSpace(binding.Session.SessionID),
			UpdatedAt:   updated,
		}
	})
}

// CompareAndSwapBinding 实现 connectors.Store（mutateState 已在数据目录锁内
// 「重读→改→原子写」，天然满足 CAS 语义）。
func (s *Store) CompareAndSwapBinding(connectorID, chat string, expected time.Time, next connectors.Binding) (connectors.Binding, bool, error) {
	key := bindingKey(connectorID, chat)
	if key == "" {
		return connectors.Binding{}, false, errors.New("storejson: binding key is required")
	}
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = time.Now().UTC()
	}
	var (
		current   connectors.Binding
		swapped   bool
		mutateErr error
	)
	mutateErr = s.mutateState(func(state *stateFile) {
		record, exists := state.Bindings[key]
		current = connectors.Binding{
			ConnectorID: record.ConnectorID,
			Chat:        record.Chat,
			Session:     connectors.SessionRef{NodeID: record.NodeID, SessionID: record.SessionID},
			UpdatedAt:   record.UpdatedAt,
		}
		if expected.IsZero() {
			if exists {
				return // 创建语义：已存在 → 未命中
			}
		} else if !exists || !record.UpdatedAt.Equal(expected) {
			return // 乐观锁未命中
		}
		// 时间戳严格递增（同 tick 内连续 CAS 可区分，见 memory 实现同注）。
		stamp := connectors.NextStampAtLeast(record.UpdatedAt)
		state.Bindings[key] = bindingRecord{
			ConnectorID: strings.TrimSpace(next.ConnectorID),
			Chat:        strings.TrimSpace(next.Chat),
			NodeID:      strings.TrimSpace(next.Session.NodeID),
			SessionID:   strings.TrimSpace(next.Session.SessionID),
			UpdatedAt:   stamp,
		}
		swapped = true
		current = connectors.Binding{
			ConnectorID: strings.TrimSpace(next.ConnectorID),
			Chat:        strings.TrimSpace(next.Chat),
			Session:     connectors.SessionRef{NodeID: strings.TrimSpace(next.Session.NodeID), SessionID: strings.TrimSpace(next.Session.SessionID)},
			UpdatedAt:   stamp,
		}
	})
	if mutateErr != nil {
		return connectors.Binding{}, false, mutateErr
	}
	return current, swapped, nil
}

// ListBindings 返回绑定（只读；connectorID 为空 = 全部）。
//
// 规模上限/使用场景见 connectors.Store 接口：绑定数通常几十~几百，本实现
// 整体重读 connectors-state.json 后全表扫描，用于注意力出站的反向查询
// （FR-8），不要在热路径上高频调用。
func (s *Store) ListBindings(connectorID string) []connectors.Binding {
	connectorID = strings.TrimSpace(connectorID)
	out := make([]connectors.Binding, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, err := s.loadStateLocked(); err == nil {
		out = make([]connectors.Binding, 0, len(state.Bindings))
		for _, record := range state.Bindings {
			if connectorID != "" && record.ConnectorID != connectorID {
				continue
			}
			out = append(out, connectors.Binding{
				ConnectorID: record.ConnectorID,
				Chat:        record.Chat,
				Session:     connectors.SessionRef{NodeID: record.NodeID, SessionID: record.SessionID},
				UpdatedAt:   record.UpdatedAt,
			})
		}
	}
	connectors.SortBindings(out)
	return out
}

// ClaimInbound 认领一条入站消息（幂等去重）：首次返回 true，重复返回 false。
func (s *Store) ClaimInbound(connectorID, messageID string) (bool, error) {
	connectorID = strings.TrimSpace(connectorID)
	messageID = strings.TrimSpace(messageID)
	if connectorID == "" || messageID == "" {
		return true, nil
	}
	key := connectorID + "\x00" + messageID
	claimed := false
	err := s.mutateState(func(state *stateFile) {
		if _, exists := state.Claimed[key]; exists {
			return
		}
		state.Claimed[key] = time.Now().UTC()
		claimed = true
	})
	return claimed, err
}

// PollingState 返回该连接器的轮询游标视图（写操作即时落盘）。
func (s *Store) PollingState(connectorID string) connectors.PollingState {
	return &pollingState{store: s, connectorID: strings.TrimSpace(connectorID)}
}

type pollingState struct {
	store       *Store
	connectorID string
}

func (p *pollingState) Get(key string) (string, bool) {
	if p == nil || p.store == nil || p.connectorID == "" {
		return "", false
	}
	var (
		value string
		found bool
	)
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if state, err := p.store.loadStateLocked(); err == nil {
		if cursors, ok := state.Cursors[p.connectorID]; ok {
			value, found = cursors[strings.TrimSpace(key)]
		}
	}
	return value, found
}

func (p *pollingState) Set(key, value string) error {
	if p == nil || p.store == nil || p.connectorID == "" {
		return errors.New("storejson: polling state is not configured")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("storejson: polling state key is required")
	}
	return p.store.mutateState(func(state *stateFile) {
		cursors, exists := state.Cursors[p.connectorID]
		if !exists {
			cursors = make(map[string]string)
			state.Cursors[p.connectorID] = cursors
		}
		cursors[key] = value
	})
}

// ---------------------------------------------------------------------------
// 运行归属（状态文件的 owners 表；语义见 connectors.Store 与 07 §5.6）

// ClaimConnectorOwner 原子认领运行归属：在数据目录锁内「读旧值 → 判存活 → 写新值」。
func (s *Store) ClaimConnectorOwner(connectorID, hostID string, now time.Time, staleAfter time.Duration) (bool, string, error) {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return false, "", errors.New("storejson: connector id and host id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	claimed := false
	owner := ""
	err := s.mutateState(func(state *stateFile) {
		if record, exists := state.Owners[connectorID]; exists && record.Owner != hostID {
			if !record.expired(now, staleAfter) {
				owner = record.Owner
				return
			}
			// 记录陈旧（持有宿主已退出/挂起）：接管并改写。
		}
		state.Owners[connectorID] = ownerRecord{Owner: hostID, HeartbeatAt: now, PID: os.Getpid()}
		claimed, owner = true, hostID
	})
	return claimed, owner, err
}

// ForceClaimConnectorOwner 强制覆盖旧归属（管理面「抢占」；不判存活）。
func (s *Store) ForceClaimConnectorOwner(connectorID, hostID string, now time.Time) error {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return errors.New("storejson: connector id and host id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.mutateState(func(state *stateFile) {
		state.Owners[connectorID] = ownerRecord{Owner: hostID, HeartbeatAt: now, PID: os.Getpid()}
	})
}

// HeartbeatConnectorOwner 续约：仅当记录仍属于 hostID 时刷新心跳。
func (s *Store) HeartbeatConnectorOwner(connectorID, hostID string, now time.Time) (bool, error) {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return false, errors.New("storejson: connector id and host id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	held := false
	err := s.mutateState(func(state *stateFile) {
		record, exists := state.Owners[connectorID]
		if !exists || record.Owner != hostID {
			return
		}
		record.HeartbeatAt = now
		record.PID = os.Getpid()
		state.Owners[connectorID] = record
		held = true
	})
	return held, err
}

// ReleaseConnectorOwner 尽力释放归属（仅当前持有者可释放，幂等）。
func (s *Store) ReleaseConnectorOwner(connectorID, hostID string) error {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return nil
	}
	// 先只读判断：非本宿主持有时不写文件。
	if record, ok := s.OwnerOf(connectorID); !ok || record.Owner != hostID {
		return nil
	}
	return s.mutateState(func(state *stateFile) {
		if record, exists := state.Owners[connectorID]; exists && record.Owner == hostID {
			delete(state.Owners, connectorID)
		}
	})
}

// OwnerOf 返回当前归属记录（只读；含已陈旧记录）。
func (s *Store) OwnerOf(connectorID string) (connectors.OwnerRecord, bool) {
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" {
		return connectors.OwnerRecord{}, false
	}
	record := connectors.OwnerRecord{}
	found := false
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, err := s.loadStateLocked(); err == nil {
		if raw, ok := state.Owners[connectorID]; ok {
			record = connectors.OwnerRecord{Owner: raw.Owner, HeartbeatAt: raw.HeartbeatAt, PID: raw.PID}
			found = true
		}
	}
	return record, found
}

// ---------------------------------------------------------------------------
// 文件读写

func bindingKey(connectorID, chat string) string {
	connectorID = strings.TrimSpace(connectorID)
	chat = strings.TrimSpace(chat)
	if connectorID == "" || chat == "" {
		return ""
	}
	return connectorID + "\x00" + chat
}

func emptyState() *stateFile {
	return &stateFile{
		Bindings: make(map[string]bindingRecord),
		Claimed:  make(map[string]time.Time),
		Cursors:  make(map[string]map[string]string),
		Owners:   make(map[string]ownerRecord),
	}
}

// mutateState 是状态写入路径：持有 s.mu 与数据目录锁，锁内重读 → 改 → 原子写。
func (s *Store) mutateState(mutate func(state *stateFile)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlockDir, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir()
	state, err := s.loadStateLocked()
	if err != nil {
		return err
	}
	mutate(state)
	return s.writeStateLocked(state)
}

// lockDir 获取数据目录锁并返回释放函数；路径不可推导时返回 no-op。
// 复用 storefile.AcquireDirLock（Mkdir 原子性 + 陈旧窃取，见其文档）。
func (s *Store) lockDir() (func(), error) {
	path := s.DirLockPath()
	if path == "" {
		return func() {}, nil
	}
	return storefile.AcquireDirLock(path, s.lockOptions)
}

// loadConfigLocked 读取 connectors.json（要求调用方已持有 s.mu）；缺失/空文件视为空表。
func (s *Store) loadConfigLocked() (*configFile, error) {
	file := &configFile{Configs: make(map[string]connectorRecord)}
	if strings.TrimSpace(s.configPath) == "" {
		return file, nil
	}
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return file, nil
		}
		return nil, fmt.Errorf("storejson: read config: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return file, nil
	}
	if err := json.Unmarshal(data, file); err != nil {
		return nil, fmt.Errorf("storejson: parse config %s: %w", s.configPath, err)
	}
	if file.Configs == nil {
		file.Configs = make(map[string]connectorRecord)
	}
	return file, nil
}

func (s *Store) writeConfigLocked(file *configFile) error {
	if strings.TrimSpace(s.configPath) == "" {
		return nil
	}
	if file == nil {
		file = &configFile{}
	}
	if file.Configs == nil {
		file.Configs = make(map[string]connectorRecord)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("storejson: encode config: %w", err)
	}
	// 0600：密钥与配置同文件，权限即边界（docs/bots/03-host-integration.md §7）。
	return writeFileAtomic(s.configPath, data, 0o600)
}

// loadStateLocked 读取 connectors-state.json（要求调用方已持有 s.mu）；缺失/空文件
// 视为空表，旧文件缺 owners 键时补空表（与 storefile 的向后兼容口径一致）。
func (s *Store) loadStateLocked() (*stateFile, error) {
	state := emptyState()
	if strings.TrimSpace(s.statePath) == "" {
		return state, nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 规范文件缺失：先做一次旧位置并集迁移（D1，docs/bots/09 §4.2），
			// 失败仅静默按空表处理，不阻塞读路径；下次打开重试。
			if migrated, migrateErr := s.migrateLegacyStateLocked(); migrateErr == nil && migrated != nil {
				return migrated, nil
			}
			return state, nil
		}
		return nil, fmt.Errorf("storejson: read state: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("storejson: parse state %s: %w", s.statePath, err)
	}
	if state.Bindings == nil {
		state.Bindings = make(map[string]bindingRecord)
	}
	if state.Claimed == nil {
		state.Claimed = make(map[string]time.Time)
	}
	if state.Cursors == nil {
		state.Cursors = make(map[string]map[string]string)
	}
	if state.Owners == nil {
		state.Owners = make(map[string]ownerRecord)
	}
	return state, nil
}

// migrateLegacyStateLocked 把旧位置（statepath.LegacyCandidates）的 state 并集
// 迁移到规范 statePath（要求调用方已持有 s.mu）。规范文件已有内容时不会走到
// 这里（调用方仅在文件缺失时触发，天然幂等）。
//
// 合并规则（docs/bots/09 §4.2）：bindings 按 updated_at 新者胜；claimed 取时间戳
// 较大者（幂等账本只在乎"已处理"）；cursors/owners 整键并入，冲突保留首个非空
// 候选。迁移成功后旧文件改名 .bak-<unix>（best-effort）。
func (s *Store) migrateLegacyStateLocked() (*stateFile, error) {
	candidates := statepath.LegacyCandidates(s.legacyConfigHints...)
	merged := emptyState()
	changed := false
	for _, candidate := range candidates {
		raw, err := os.ReadFile(candidate.Path)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue // 缺失/空/不可读的候选静默跳过
		}
		var legacy stateFile
		if err := json.Unmarshal(raw, &legacy); err != nil {
			continue // 半写入/非法 JSON：跳过（与外部改动容错口径一致）
		}
		changed = true
		for key, record := range legacy.Bindings {
			if existing, ok := merged.Bindings[key]; !ok || record.UpdatedAt.After(existing.UpdatedAt) {
				merged.Bindings[key] = record
			}
		}
		for key, claimedAt := range legacy.Claimed {
			if existing, ok := merged.Claimed[key]; !ok || claimedAt.After(existing) {
				merged.Claimed[key] = claimedAt
			}
		}
		for connectorID, cursors := range legacy.Cursors {
			if _, ok := merged.Cursors[connectorID]; !ok {
				merged.Cursors[connectorID] = cursors
			}
		}
		for connectorID, owner := range legacy.Owners {
			if _, ok := merged.Owners[connectorID]; !ok {
				merged.Owners[connectorID] = owner
			}
		}
	}
	if !changed {
		return nil, nil
	}
	if err := s.writeStateLocked(merged); err != nil {
		return nil, fmt.Errorf("storejson: migrate legacy state: %w", err)
	}
	stamp := time.Now().Unix()
	for _, candidate := range candidates {
		_ = os.Rename(candidate.Path, candidate.Path+fmt.Sprintf(".bak-%d", stamp))
	}
	return merged, nil
}

func (s *Store) writeStateLocked(state *stateFile) error {
	if strings.TrimSpace(s.statePath) == "" {
		return nil
	}
	if state == nil {
		state = emptyState()
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("storejson: encode state: %w", err)
	}
	return writeFileAtomic(s.statePath, data, 0o600)
}

// writeFileAtomic 写临时文件 → chmod → rename 原子替换（同目录，跨进程读到
// 的要么是旧文件要么是新文件，不会是半截内容）。
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storejson: create dir %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("storejson: create temp file: %w", err)
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return fmt.Errorf("storejson: write temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("storejson: close temp file: %w", err)
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("storejson: chmod temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("storejson: replace %s: %w", path, err)
	}
	return nil
}

// 编译期断言：Store 完整实现 connectors.Store 契约。
var _ connectors.Store = (*Store)(nil)
