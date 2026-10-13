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
)

// ConfigStore 把 connectors.Store 落到三个文件：config.yaml 的 connectors: 节
// （非敏感配置）、connectors.secrets.yaml（密钥，0600）与 state.json（运行时状态）。
type ConfigStore struct {
	configPath  string
	secretsPath string
	statePath   string

	mu     sync.Mutex
	state  stateFile
	loaded bool
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
	_ = s.mutateState(func(state *stateFile) {
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
	_ = p.store.mutateState(func(state *stateFile) {
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
	}
	return connectors.Config{
		ID:       anyToString(raw["id"]),
		Provider: anyToString(raw["provider"]),
		Name:     anyToString(raw["name"]),
		Enabled:  anyToBool(raw["enabled"]),
		Mode:     anyToString(raw["mode"]),
		Settings: anyToStringMap(raw["settings"]),
		Secrets:  anyToStringMap(raw["secrets"]),
		Policy:   policy,
	}
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
	return s.mutateStateLocked(mutate)
}

// mutateStateLocked 要求调用方已持有 s.mu。
func (s *ConfigStore) mutateStateLocked(mutate func(state *stateFile)) error {
	if err := s.ensureStateLocked(); err != nil {
		return err
	}
	mutate(&s.state)
	return s.writeStateLocked()
}

func (s *ConfigStore) ensureStateLocked() error {
	if s.loaded {
		return nil
	}
	s.state = stateFile{
		Bindings: make(map[string]bindingRecord),
		Claimed:  make(map[string]time.Time),
		Cursors:  make(map[string]map[string]string),
	}
	if s.statePath == "" {
		s.loaded = true
		return nil
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.loaded = true
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
	s.loaded = true
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
