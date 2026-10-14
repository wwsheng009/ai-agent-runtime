// Package storefile 是 connectors.Store 的文件实现：通道配置写进宿主共享的
// config.yaml 的 connectors: 节，绑定/去重账本/轮询游标写进独立 state.json。
//
// 设计（docs/bots/06-target-architecture.md §2.2 / §5.1）：
//   - aicli 与 runtime-server 共用同一份 config.yaml，两个宿主看到同一配置；
//   - 运行时状态不进用户配置文件（避免每次轮询重写 YAML、也避免把游标提交
//     混进用户可编辑的文件）；
//   - YAML 用 yaml.v3 Node 往返，保留注释与其它键；写入走临时文件 + rename。
package storefile

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

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/statepath"
)

// ConfigStore 把 connectors.Store 落到三个文件：config.yaml 的 connectors: 节
// （非敏感配置）、connectors.secrets.yaml（密钥，0600）与 state.json（运行时状态）。
type ConfigStore struct {
	configPath  string
	secretsPath string
	statePath   string

	mu    sync.Mutex
	state stateFile
	// lockOptions 是数据目录锁参数（写 state.json/config.yaml/secrets 前先抢锁）；
	// 零值由 LockOptions.withDefaults 补齐。
	lockOptions LockOptions
}

// NewConfigStore 构造文件存储；configPath 为共享 config.yaml，statePath 为宿主
// 数据目录下的运行时状态文件（父目录不存在时自动创建）。
func NewConfigStore(configPath, statePath string) *ConfigStore {
	configPath = strings.TrimSpace(configPath)
	return &ConfigStore{
		configPath:  configPath,
		secretsPath: filepath.Join(filepath.Dir(configPath), defaultSecretsFileName),
		statePath:   strings.TrimSpace(statePath),
	}
}

// defaultSecretsFileName 是密钥文件默认名（与 config.yaml 同目录，0600）。
const defaultSecretsFileName = "connectors.secrets.yaml"

// WithSecretsPath 覆盖密钥文件位置（默认 config.yaml 同目录的
// connectors.secrets.yaml）。密钥不进 config.yaml，避免随配置被同步/分享。
func (s *ConfigStore) WithSecretsPath(path string) *ConfigStore {
	if s != nil {
		s.secretsPath = strings.TrimSpace(path)
	}
	return s
}

type stateFile struct {
	Bindings map[string]bindingRecord     `json:"bindings"`
	Claimed  map[string]time.Time         `json:"claimed"`
	Cursors  map[string]map[string]string `json:"cursors"`
	// Owners 是连接器运行归属账本（owner/heartbeat_at/pid）；旧文件缺该键时
	// 视为空表（向后兼容）。
	Owners map[string]ownerRecord `json:"owners,omitempty"`
}

// ownerRecord 是运行归属在 state.json 中的形状（docs/bots/07-config-ux.md §5.6）。
type ownerRecord struct {
	Owner       string    `json:"owner"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	PID         int       `json:"pid,omitempty"`
}

type bindingRecord struct {
	ConnectorID string    `json:"connector_id"`
	Chat        string    `json:"chat"`
	NodeID      string    `json:"node_id,omitempty"`
	SessionID   string    `json:"session_id"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListConfigs 返回 config.yaml `connectors:` 节的全部条目（含停用项）。
func (s *ConfigStore) ListConfigs() []connectors.Config {
	if s == nil || s.configPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.loadDocument()
	if err != nil {
		return nil
	}
	seq, _ := mappingEntry(documentRoot(doc), connectorsSectionKey)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	secrets, err := s.loadSecretsLocked()
	if err != nil {
		return nil
	}
	out := make([]connectors.Config, 0, len(seq.Content))
	for _, entry := range seq.Content {
		cfg, err := configFromNode(entry)
		if err != nil || strings.TrimSpace(cfg.ID) == "" {
			continue
		}
		if merged, ok := secrets[cfg.ID]; ok && len(merged) > 0 {
			cfg.Secrets = merged
		}
		out = append(out, cfg)
	}
	return out
}

// GetConfig 按 id 读取配置。
func (s *ConfigStore) GetConfig(id string) (connectors.Config, bool) {
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

// PutConfig 新建或覆盖一条配置（按 id 去重），并原子写回 config.yaml。
func (s *ConfigStore) PutConfig(cfg connectors.Config) error {
	if s == nil || s.configPath == "" {
		return errors.New("storefile: config path is not configured")
	}
	cfg.ID = strings.TrimSpace(cfg.ID)
	if cfg.ID == "" {
		return errors.New("storefile: connector id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 写 config.yaml/secrets 与 state.json 共用同一把数据目录锁：
	// 「读旧文档 → 改 → 写回」必须整体互斥，否则两个宿主并发保存会丢更新。
	unlockDir, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir()

	doc, err := s.loadDocument()
	if err != nil {
		return err
	}
	root := documentRoot(doc)
	seq, _ := mappingEntry(root, connectorsSectionKey)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setMappingEntry(root, connectorsSectionKey, seq)
	}
	secrets, err := s.loadSecretsLocked()
	if err != nil {
		return err
	}
	if len(cfg.Secrets) > 0 {
		secrets[cfg.ID] = cfg.Secrets
	} else {
		delete(secrets, cfg.ID)
	}
	entry, err := nodeFromConfig(cfg)
	if err != nil {
		return err
	}
	for i, existing := range seq.Content {
		current, decodeErr := configFromNode(existing)
		if decodeErr == nil && current.ID == cfg.ID {
			seq.Content[i] = entry
			if err := s.writeDocument(doc); err != nil {
				return err
			}
			return s.writeSecretsLocked(secrets)
		}
	}
	seq.Content = append(seq.Content, entry)
	if err := s.writeDocument(doc); err != nil {
		return err
	}
	return s.writeSecretsLocked(secrets)
}

// DeleteConfig 删除一条配置，并级联清理其绑定与轮询游标。
func (s *ConfigStore) DeleteConfig(id string) error {
	id = strings.TrimSpace(id)
	if s == nil || id == "" {
		return errors.New("storefile: connector id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	unlockDir, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir()

	doc, err := s.loadDocument()
	if err != nil {
		return err
	}
	if seq, _ := mappingEntry(documentRoot(doc), connectorsSectionKey); seq != nil && seq.Kind == yaml.SequenceNode {
		kept := make([]*yaml.Node, 0, len(seq.Content))
		for _, entry := range seq.Content {
			cfg, decodeErr := configFromNode(entry)
			if decodeErr == nil && cfg.ID == id {
				continue
			}
			kept = append(kept, entry)
		}
		seq.Content = kept
		if writeErr := s.writeDocument(doc); writeErr != nil {
			return writeErr
		}
	}
	secrets, err := s.loadSecretsLocked()
	if err != nil {
		return err
	}
	if _, exists := secrets[id]; exists {
		delete(secrets, id)
		if err := s.writeSecretsLocked(secrets); err != nil {
			return err
		}
	}
	// 已持有 s.mu（本方法开头加锁），这里只能走 locked 版本，否则自锁死锁。
	return s.mutateStateLocked(func(state *stateFile) {
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
	})
}

// GetBinding 读取 IM 会话 → 宿主会话绑定。
func (s *ConfigStore) GetBinding(connectorID, chat string) (connectors.Binding, bool) {
	key := bindingKey(connectorID, chat)
	if key == "" {
		return connectors.Binding{}, false
	}
	var (
		record bindingRecord
		found  bool
	)
	_ = s.observeState(func(state *stateFile) {
		record, found = state.Bindings[key]
	})
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
func (s *ConfigStore) PutBinding(binding connectors.Binding) error {
	key := bindingKey(binding.ConnectorID, binding.Chat)
	if key == "" {
		return errors.New("storefile: binding key is required")
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

// CompareAndSwapBinding 实现 connectors.Store（锁内读改写；mutateState 已在
// 数据目录锁内执行「重读→改→原子写」，天然满足 CAS 语义）。
func (s *ConfigStore) CompareAndSwapBinding(connectorID, chat string, expected time.Time, next connectors.Binding) (connectors.Binding, bool, error) {
	key := bindingKey(connectorID, chat)
	if key == "" {
		return connectors.Binding{}, false, errors.New("storefile: binding key is required")
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
// 整体重读 state.json 后全表扫描，用于注意力出站的反向查询（FR-8），
// 不要在热路径上高频调用。
func (s *ConfigStore) ListBindings(connectorID string) []connectors.Binding {
	connectorID = strings.TrimSpace(connectorID)
	out := make([]connectors.Binding, 0)
	_ = s.observeState(func(state *stateFile) {
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
	})
	connectors.SortBindings(out)
	return out
}

// ClaimInbound 认领一条入站消息（幂等去重）：首次返回 true。
func (s *ConfigStore) ClaimInbound(connectorID, messageID string) (bool, error) {
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
func (s *ConfigStore) PollingState(connectorID string) connectors.PollingState {
	return &pollingState{store: s, connectorID: strings.TrimSpace(connectorID)}
}

type pollingState struct {
	store       *ConfigStore
	connectorID string
}

func (p *pollingState) Get(key string) (string, bool) {
	if p == nil || p.store == nil || p.connectorID == "" {
		return "", false
	}
	var value string
	var found bool
	_ = p.store.observeState(func(state *stateFile) {
		cursors, exists := state.Cursors[p.connectorID]
		if !exists {
			return
		}
		value, found = cursors[strings.TrimSpace(key)]
	})
	return value, found
}

func (p *pollingState) Set(key, value string) error {
	if p == nil || p.store == nil || p.connectorID == "" {
		return errors.New("storefile: polling state is not configured")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("storefile: polling state key is required")
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
// 运行归属（state.json 的 owners 表；docs/bots/07-config-ux.md §5.6）

// ClaimConnectorOwner 原子认领运行归属：在数据目录锁内「读旧值 → 判存活 → 写新值」。
func (s *ConfigStore) ClaimConnectorOwner(connectorID, hostID string, now time.Time, staleAfter time.Duration) (bool, string, error) {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return false, "", errors.New("storefile: connector id and host id are required")
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
func (s *ConfigStore) ForceClaimConnectorOwner(connectorID, hostID string, now time.Time) error {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return errors.New("storefile: connector id and host id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.mutateState(func(state *stateFile) {
		state.Owners[connectorID] = ownerRecord{Owner: hostID, HeartbeatAt: now, PID: os.Getpid()}
	})
}

// HeartbeatConnectorOwner 续约：仅当记录仍属于 hostID 时刷新心跳。
func (s *ConfigStore) HeartbeatConnectorOwner(connectorID, hostID string, now time.Time) (bool, error) {
	connectorID = strings.TrimSpace(connectorID)
	hostID = strings.TrimSpace(hostID)
	if connectorID == "" || hostID == "" {
		return false, errors.New("storefile: connector id and host id are required")
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
func (s *ConfigStore) ReleaseConnectorOwner(connectorID, hostID string) error {
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
func (s *ConfigStore) OwnerOf(connectorID string) (connectors.OwnerRecord, bool) {
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" {
		return connectors.OwnerRecord{}, false
	}
	record := connectors.OwnerRecord{}
	found := false
	_ = s.observeState(func(state *stateFile) {
		raw, ok := state.Owners[connectorID]
		if !ok {
			return
		}
		record = connectors.OwnerRecord{Owner: raw.Owner, HeartbeatAt: raw.HeartbeatAt, PID: raw.PID}
		found = true
	})
	return record, found
}

// expired 把 ownerRecord 交给 connectors.OwnerRecord 做统一的陈旧判断。
func (r ownerRecord) expired(now time.Time, staleAfter time.Duration) bool {
	return connectors.OwnerRecord{Owner: r.Owner, HeartbeatAt: r.HeartbeatAt, PID: r.PID}.Expired(now, staleAfter)
}

const connectorsSectionKey = "connectors"

func bindingKey(connectorID, chat string) string {
	connectorID = strings.TrimSpace(connectorID)
	chat = strings.TrimSpace(chat)
	if connectorID == "" || chat == "" {
		return ""
	}
	return connectorID + "\x00" + chat
}

// ---------------------------------------------------------------------------
// config.yaml 读写

func (s *ConfigStore) loadDocument() (*yaml.Node, error) {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return emptyDocument(), nil
		}
		return nil, fmt.Errorf("storefile: read config: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return emptyDocument(), nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("storefile: parse config %s: %w", s.configPath, err)
	}
	return &doc, nil
}

func (s *ConfigStore) writeDocument(doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("storefile: encode config: %w", err)
	}
	return writeFileAtomic(s.configPath, out, 0o644)
}

// loadSecretsLocked 读取密钥文件（要求调用方已持有 s.mu）。缺失/空文件视为空表。
func (s *ConfigStore) loadSecretsLocked() (map[string]map[string]string, error) {
	out := make(map[string]map[string]string)
	if s.secretsPath == "" {
		return out, nil
	}
	data, err := os.ReadFile(s.secretsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("storefile: read secrets: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}
	// 先解码为 any 再转 string：手写的无引号 token（如纯数字）也能读。
	var raw map[string]map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("storefile: parse secrets %s: %w", s.secretsPath, err)
	}
	for id, values := range raw {
		if len(values) == 0 {
			continue
		}
		converted := make(map[string]string, len(values))
		for key, value := range values {
			converted[key] = anyToString(value)
		}
		out[id] = converted
	}
	return out, nil
}

// writeSecretsLocked 原子写回密钥文件（0600）；无密钥时删除该文件。
func (s *ConfigStore) writeSecretsLocked(secrets map[string]map[string]string) error {
	if s.secretsPath == "" {
		return nil
	}
	if len(secrets) == 0 {
		if err := os.Remove(s.secretsPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("storefile: remove secrets: %w", err)
		}
		return nil
	}
	data, err := yaml.Marshal(secrets)
	if err != nil {
		return fmt.Errorf("storefile: encode secrets: %w", err)
	}
	return writeFileAtomic(s.secretsPath, data, 0o600)
}

func emptyDocument() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}},
	}
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return emptyDocument().Content[0]
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		}
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			root.Kind = yaml.MappingNode
			root.Tag = "!!map"
			root.Content = nil
		}
		return root
	}
	return doc
}

func mappingEntry(root *yaml.Node, key string) (*yaml.Node, int) {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, -1
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i+1], i
		}
	}
	return nil, -1
}

func setMappingEntry(root *yaml.Node, key string, value *yaml.Node) {
	if _, idx := mappingEntry(root, key); idx >= 0 {
		root.Content[idx+1] = value
		return
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func nodeFromConfig(cfg connectors.Config) (*yaml.Node, error) {
	payload, err := json.Marshal(configToMap(cfg))
	if err != nil {
		return nil, fmt.Errorf("storefile: encode connector config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(payload, &doc); err != nil {
		return nil, fmt.Errorf("storefile: build connector node: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("storefile: empty connector node")
	}
	// JSON 解析出的节点带 FlowStyle，落盘会变成一行 JSON；配置是给人看的，
	// 统一切回块状风格（yaml 发射器仍会自行决定标量的引号）。
	toBlockStyle(doc.Content[0])
	return doc.Content[0], nil
}

// toBlockStyle 清除节点的 FlowStyle，让 YAML 以块状输出。
func toBlockStyle(node *yaml.Node) {
	if node == nil {
		return
	}
	node.Style = 0
	for _, child := range node.Content {
		toBlockStyle(child)
	}
}

func configFromNode(node *yaml.Node) (connectors.Config, error) {
	var raw map[string]interface{}
	if err := node.Decode(&raw); err != nil {
		return connectors.Config{}, fmt.Errorf("storefile: decode connector entry: %w", err)
	}
	return configFromMap(raw), nil
}

// configToMap 是 config.yaml 的稳定落盘形状（snake_case，与 docs/bots/07-config-ux.md
// §5.1 示例一致）。字段与 connectors.Config 一一对应。
func configToMap(cfg connectors.Config) map[string]interface{} {
	out := map[string]interface{}{
		"id":       strings.TrimSpace(cfg.ID),
		"provider": strings.TrimSpace(cfg.Provider),
		"enabled":  cfg.Enabled,
	}
	if name := strings.TrimSpace(cfg.Name); name != "" {
		out["name"] = name
	}
	if mode := strings.TrimSpace(cfg.Mode); mode != "" {
		out["mode"] = mode
	}
	// 时间戳：旧数据零值省略（读取方容忍缺省）；RFC3339 UTC 保持文件可读。
	if !cfg.CreatedAt.IsZero() {
		out["created_at"] = cfg.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !cfg.UpdatedAt.IsZero() {
		out["updated_at"] = cfg.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if len(cfg.Settings) > 0 {
		out["settings"] = stringMapToAny(cfg.Settings)
	}
	// Secrets 不写进 config.yaml：单独进 connectors.secrets.yaml（0600，见
	// docs/bots/03-host-integration.md §7）。
	policy := map[string]interface{}{}
	if v := strings.TrimSpace(cfg.Policy.CommandPrefix); v != "" {
		policy["command_prefix"] = v
	}
	if len(cfg.Policy.AllowedUsers) > 0 {
		allowed := make([]interface{}, 0, len(cfg.Policy.AllowedUsers))
		for _, user := range cfg.Policy.AllowedUsers {
			allowed = append(allowed, user)
		}
		policy["allowed_users"] = allowed
	}
	if v := strings.TrimSpace(cfg.Policy.SessionMode); v != "" {
		policy["session_mode"] = v
	}
	if v := strings.TrimSpace(cfg.Policy.ReplyMode); v != "" {
		policy["reply_mode"] = v
	}
	if v := strings.TrimSpace(cfg.Policy.RuntimeOwner); v != "" {
		policy["runtime_owner"] = v
	}
	// D6 连接器级模型路由：零值不落盘。
	if cfg.Policy.Route.HasOverride() {
		route := map[string]interface{}{}
		if v := strings.TrimSpace(cfg.Policy.Route.Provider); v != "" {
			route["provider"] = v
		}
		if v := strings.TrimSpace(cfg.Policy.Route.Model); v != "" {
			route["model"] = v
		}
		if v := strings.TrimSpace(cfg.Policy.Route.ReasoningEffort); v != "" {
			route["reasoning_effort"] = v
		}
		policy["route"] = route
	}
	// D5（docs/bots/09 §7.4.1）：notify 缺省零值不落盘（保持文件最小噪音）。
	if cfg.Policy.Notify.Enabled {
		notify := map[string]interface{}{"enabled": true}
		if len(cfg.Policy.Notify.Kinds) > 0 {
			kinds := make([]interface{}, 0, len(cfg.Policy.Notify.Kinds))
			for _, kind := range cfg.Policy.Notify.Kinds {
				kinds = append(kinds, kind)
			}
			notify["kinds"] = kinds
		}
		if scope := strings.TrimSpace(cfg.Policy.Notify.Scope); scope != "" {
			notify["scope"] = scope
		}
		policy["notify"] = notify
	}
	if len(policy) > 0 {
		out["policy"] = policy
	}
	return out
}

func configFromMap(raw map[string]interface{}) connectors.Config {
	if raw == nil {
		return connectors.Config{}
	}
	policy := connectors.Policy{}
	if policyRaw, ok := raw["policy"].(map[string]interface{}); ok {
		policy.CommandPrefix = anyToString(policyRaw["command_prefix"])
		policy.AllowedUsers = anyToStringSlice(policyRaw["allowed_users"])
		policy.SessionMode = anyToString(policyRaw["session_mode"])
		policy.ReplyMode = anyToString(policyRaw["reply_mode"])
		policy.RuntimeOwner = anyToString(policyRaw["runtime_owner"])
		if routeRaw, ok := policyRaw["route"].(map[string]interface{}); ok {
			policy.Route.Provider = anyToString(routeRaw["provider"])
			policy.Route.Model = anyToString(routeRaw["model"])
			policy.Route.ReasoningEffort = anyToString(routeRaw["reasoning_effort"])
		}
		if notifyRaw, ok := policyRaw["notify"].(map[string]interface{}); ok {
			policy.Notify.Enabled = anyToBool(notifyRaw["enabled"])
			policy.Notify.Kinds = anyToStringSlice(notifyRaw["kinds"])
			policy.Notify.Scope = anyToString(notifyRaw["scope"])
		}
	}
	return connectors.Config{
		ID:        anyToString(raw["id"]),
		Provider:  anyToString(raw["provider"]),
		Name:      anyToString(raw["name"]),
		Enabled:   anyToBool(raw["enabled"]),
		Mode:      anyToString(raw["mode"]),
		CreatedAt: anyToTime(raw["created_at"]),
		UpdatedAt: anyToTime(raw["updated_at"]),
		Settings:  anyToStringMap(raw["settings"]),
		Secrets:   anyToStringMap(raw["secrets"]),
		Policy:    policy,
	}
}

// anyToTime 解析 RFC3339 时间戳（空串/非法格式返回零值——旧数据容忍缺省）。
func anyToTime(raw interface{}) time.Time {
	text := strings.TrimSpace(anyToString(raw))
	if text == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func stringMapToAny(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func anyToString(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func anyToBool(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true
		}
	}
	return false
}

func anyToStringMap(value interface{}) map[string]string {
	raw, ok := value.(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, item := range raw {
		out[key] = anyToString(item)
	}
	return out
}

func anyToStringSlice(value interface{}) []string {
	raw, ok := value.([]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := anyToString(item); text != "" {
			out = append(out, text)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// state.json 读写

func (s *ConfigStore) mutateState(mutate func(state *stateFile)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlockDir, err := s.lockDir()
	if err != nil {
		return err
	}
	defer unlockDir()
	return s.mutateStateLocked(mutate)
}

// mutateStateLocked 要求调用方已持有 s.mu 与数据目录锁；写前重新读盘，
// 使「读旧值 → 判存活 → 写新值」在跨进程锁内原子完成（另一实例的写入不会被覆盖）。
func (s *ConfigStore) mutateStateLocked(mutate func(state *stateFile)) error {
	if err := s.reloadStateLocked(); err != nil {
		return err
	}
	mutate(&s.state)
	return s.writeStateLocked()
}

// observeState 是只读路径：重新读盘但不写回、不抢目录锁（写侧用临时文件 +
// rename 原子替换，读到的要么是旧值要么是新值，不会是半截文件）。
func (s *ConfigStore) observeState(observe func(state *stateFile)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadStateLocked(); err != nil {
		return err
	}
	observe(&s.state)
	return nil
}

// reloadStateLocked 从磁盘重读 state.json（要求调用方已持有 s.mu）；文件缺失或
// 为空时视为空表，旧文件缺 owners 键时补空表（向后兼容）。
func (s *ConfigStore) reloadStateLocked() error {
	s.state = stateFile{
		Bindings: make(map[string]bindingRecord),
		Claimed:  make(map[string]time.Time),
		Cursors:  make(map[string]map[string]string),
		Owners:   make(map[string]ownerRecord),
	}
	if s.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// 规范文件缺失：做一次旧位置并集迁移（docs/bots/09 §4.2 D1），
			// 迁移后重读本文件；失败静默按空表处理（下次打开重试）。
			if migrated, migrateErr := s.migrateLegacyStateLocked(); migrateErr == nil && migrated {
				return s.reloadStateLocked()
			} else if migrateErr != nil {
				// 迁移失败可见（诊断用）：写路径失败多为权限/磁盘，静默会变成
				// "迁移永远不发生"。宿主日志走调用方 logf；这里返回错误太重，
				// 保持空表但下次打开重试。
				_ = migrateErr
			}
			return nil
		}
		return fmt.Errorf("storefile: read state: %w", err)
	}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &s.state); err != nil {
			return fmt.Errorf("storefile: parse state %s: %w", s.statePath, err)
		}
	}
	if s.state.Bindings == nil {
		s.state.Bindings = make(map[string]bindingRecord)
	}
	if s.state.Claimed == nil {
		s.state.Claimed = make(map[string]time.Time)
	}
	if s.state.Cursors == nil {
		s.state.Cursors = make(map[string]map[string]string)
	}
	if s.state.Owners == nil {
		s.state.Owners = make(map[string]ownerRecord)
	}
	// 持续并集迁移（docs/bots/09 §4.2 D1）：规范文件可能先由另一宿主创建，
	// 本宿主的旧位置数据仍需并入——所以迁移不只在"文件缺失"时跑一次，而是
	// 每次重载都扫描旧候选；仅在带来**规范文件没有的键**时才写回（幂等，
	// 无新键零写放大）。全部旧文件成功并入后按 .bak 归档。
	return s.mergeLegacyCandidatesLocked()
}

// migrateLegacyStateLocked 把旧位置（statepath.LegacyCandidates + 宿主注入的
// configPath 提示）的 state 并集迁移到规范 statePath（要求调用方已持有 s.mu
// 与数据目录锁）。返回是否发生了迁移。
//
// 合并规则（docs/bots/09 §4.2）：bindings 按 updated_at 新者胜；claimed 取
// 较大时间戳；cursors/owners 首个非空候选并入。迁移成功后旧文件改名
// .bak-<unix>（best-effort）。
func (s *ConfigStore) migrateLegacyStateLocked() (bool, error) {
	hints := make([]string, 0, 1)
	if s.configPath != "" {
		hints = append(hints, s.configPath)
	}
	merged := stateFile{
		Bindings: make(map[string]bindingRecord),
		Claimed:  make(map[string]time.Time),
		Cursors:  make(map[string]map[string]string),
		Owners:   make(map[string]ownerRecord),
	}
	changed := false
	for _, candidate := range statepath.LegacyCandidates(hints...) {
		raw, err := os.ReadFile(candidate.Path)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var legacy stateFile
		if err := json.Unmarshal(raw, &legacy); err != nil {
			continue // 半写入/非法 JSON：跳过
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
		return false, nil
	}
	s.state = merged
	if err := s.writeStateLocked(); err != nil {
		return false, fmt.Errorf("storefile: migrate legacy state: %w", err)
	}
	stamp := time.Now().Unix()
	for _, candidate := range statepath.LegacyCandidates(hints...) {
		_ = os.Rename(candidate.Path, candidate.Path+fmt.Sprintf(".bak-%d", stamp))
	}
	return true, nil
}

// mergeLegacyCandidatesLocked 是持续形态的迁移：每次 state 重载后扫描旧候选，
// 只并入规范文件缺失的键（bindings/claimed/cursors/owners 任一维度），带新键的
// 候选全部成功并入后才归档该候选；无新键零写放大。要求已持有 s.mu + 目录锁
// （reloadStateLocked 的调用语境）。
func (s *ConfigStore) mergeLegacyCandidatesLocked() error {
	hints := make([]string, 0, 1)
	if s.configPath != "" {
		hints = append(hints, s.configPath)
	}
	candidates := statepath.LegacyCandidates(hints...)
	if len(candidates) == 0 {
		return nil
	}

	type candidateResult struct {
		path       string
		broughtNew bool
	}
	results := make([]candidateResult, 0, len(candidates))
	changed := false
	for _, candidate := range candidates {
		raw, err := os.ReadFile(candidate.Path)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var legacy stateFile
		if err := json.Unmarshal(raw, &legacy); err != nil {
			continue // 半写入/非法 JSON：跳过
		}
		result := candidateResult{path: candidate.Path}
		for key, record := range legacy.Bindings {
			if _, ok := s.state.Bindings[key]; !ok {
				s.state.Bindings[key] = record
				result.broughtNew = true
			}
		}
		for key, claimedAt := range legacy.Claimed {
			if _, ok := s.state.Claimed[key]; !ok {
				s.state.Claimed[key] = claimedAt
				result.broughtNew = true
			}
		}
		for connectorID, cursors := range legacy.Cursors {
			if _, ok := s.state.Cursors[connectorID]; !ok {
				s.state.Cursors[connectorID] = cursors
				result.broughtNew = true
			}
		}
		for connectorID, owner := range legacy.Owners {
			if _, ok := s.state.Owners[connectorID]; !ok {
				s.state.Owners[connectorID] = owner
				result.broughtNew = true
			}
		}
		if result.broughtNew {
			changed = true
		}
		results = append(results, result)
	}
	if !changed {
		return nil
	}
	if err := s.writeStateLocked(); err != nil {
		return fmt.Errorf("storefile: merge legacy state: %w", err)
	}
	stamp := time.Now().Unix()
	for _, result := range results {
		if result.broughtNew {
			_ = os.Rename(result.path, result.path+fmt.Sprintf(".bak-%d", stamp))
		}
	}
	return nil
}

func (s *ConfigStore) writeStateLocked() error {
	if s.statePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("storefile: encode state: %w", err)
	}
	return writeFileAtomic(s.statePath, data, 0o600)
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storefile: create dir %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("storefile: create temp file: %w", err)
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return fmt.Errorf("storefile: write temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("storefile: close temp file: %w", err)
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("storefile: chmod temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("storefile: replace %s: %w", path, err)
	}
	return nil
}
