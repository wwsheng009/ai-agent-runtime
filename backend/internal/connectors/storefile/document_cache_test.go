package storefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"gopkg.in/yaml.v3"
)

// TestConfigStoreDocumentCacheInvalidatesOnExternalEdit 锁定文档缓存的核心
// 不变量：外部直接修改 config.yaml 后，读路径必须看到新内容。
//
// 背景：loadDocument 现在缓存 YAML AST（pprof 实测 streamMirror 每个 delta 都
// 全量解析 150 KB 配置，占累计分配 41.6%）。缓存一旦不失效，连接器列表会
// 永久显示旧配置。
func TestConfigStoreDocumentCacheInvalidatesOnExternalEdit(t *testing.T) {
	store, configPath, _ := newTestStore(t)
	seed := "model: demo\nconnectors:\n  - id: alpha\n    enabled: true\n"
	if err := os.WriteFile(configPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	first := store.ListConfigs()
	if len(first) != 1 || first[0].ID != "alpha" {
		t.Fatalf("first list = %+v", first)
	}

	// 命中缓存：同一份文档不应被重新读取（这里只验证结果一致）。
	if again := store.ListConfigs(); len(again) != 1 || again[0].ID != "alpha" {
		t.Fatalf("cached list = %+v", again)
	}

	// 外部编辑（不同长度，确保 mtime/size 都变化）。
	updated := "model: demo\nconnectors:\n  - id: beta\n    enabled: true\n"
	if err := os.WriteFile(configPath, []byte(updated), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	// Windows 文件时间分辨率可能较粗，显式推进一次，避免同刻度覆盖。
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(configPath, future, future)

	got := store.ListConfigs()
	if len(got) != 1 || got[0].ID != "beta" {
		t.Fatalf("after external edit = %+v, want beta", got)
	}
}

// TestConfigStoreWriteDoesNotPolluteCachedDocument 锁定写路径使用私有副本：
// PutConfig 之后再 ListConfigs，必须只看到写入后的内容，且不能因为共享 AST
// 被就地修改而重复追加。
func TestConfigStoreWriteDoesNotPolluteCachedDocument(t *testing.T) {
	store, configPath, _ := newTestStore(t)
	if err := os.WriteFile(configPath, []byte("model: demo\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 先触发一次读，把文档放进缓存。
	if got := store.ListConfigs(); len(got) != 0 {
		t.Fatalf("initial connectors = %+v", got)
	}

	for _, id := range []string{"alpha", "beta"} {
		cfg := connectors.Config{
			ID:       id,
			Provider: "telegram",
			Name:     id,
			Enabled:  true,
			Mode:     "polling",
		}
		if err := store.PutConfig(cfg); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}

	got := store.ListConfigs()
	if len(got) != 2 {
		t.Fatalf("connectors = %d (%+v), want 2", len(got), got)
	}
	seen := map[string]bool{}
	for _, cfg := range got {
		seen[cfg.ID] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("ids = %+v", got)
	}

	// 磁盘上也必须是两条，不能因为共享 AST 被改写而重复。
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "id: alpha"); n != 1 {
		t.Fatalf("alpha appears %d times on disk:\n%s", n, raw)
	}
	if n := strings.Count(string(raw), "id: beta"); n != 1 {
		t.Fatalf("beta appears %d times on disk:\n%s", n, raw)
	}
}

// TestCloneYAMLNodeIsDeep 是 cloneYAMLNode 的直接不变量测试：改副本不能影响原树。
func TestCloneYAMLNodeIsDeep(t *testing.T) {
	store, configPath, _ := newTestStore(t)
	if err := os.WriteFile(configPath, []byte("connectors:\n  - id: alpha\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	original, err := store.loadDocument()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	before := len(documentRoot(original).Content)

	cloned := cloneYAMLNode(original)
	root := documentRoot(cloned)
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "injected"})

	if got := len(documentRoot(original).Content); got != before {
		t.Fatalf("original mutated: content %d -> %d", before, got)
	}
}

// TestConfigStoreCachedDocumentSurvivesRepeatedReads 验证热路径不再重复解析：
// 连续多次 ListConfigs 之后，缓存指针保持不变（说明没有走 yaml.Unmarshal）。
func TestConfigStoreCachedDocumentSurvivesRepeatedReads(t *testing.T) {
	store, configPath, _ := newTestStore(t)
	if err := os.WriteFile(configPath, []byte("connectors:\n  - id: alpha\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	first, err := store.loadDocument()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.loadDocument(); err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
		store.ListConfigs()
		store.RunnerPreference()
	}
	cached, err := store.loadDocument()
	if err != nil {
		t.Fatalf("load cached: %v", err)
	}
	if cached != first {
		t.Fatalf("document was re-parsed: cache miss on a stable file")
	}
	if !filepath.IsAbs(configPath) {
		t.Fatalf("unexpected config path %q", configPath)
	}
}

// TestConfigStoreStateCacheSeesExternalBindingWrite 锁定状态缓存的不变量：
// 另一实例（或外部编辑）写了 state.json 后，本进程的 ListBindings 必须立刻
// 看到新绑定。
//
// 这是 streamMirror 热路径的语义前提：缓存只是为了不再每 delta 重读，
// 绝不能牺牲「改了绑定立即生效」。
func TestConfigStoreStateCacheSeesExternalBindingWrite(t *testing.T) {
	store, _, statePath := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}

	// 先写入一条绑定并触发一次读，让状态进入缓存。
	first := connectors.Binding{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{SessionID: "sess-1"},
	}
	if err := store.PutBinding(first); err != nil {
		t.Fatalf("put binding: %v", err)
	}
	if got := store.ListBindings("c1"); len(got) != 1 || got[0].Session.SessionID != "sess-1" {
		t.Fatalf("initial bindings = %+v", got)
	}

	// 模拟另一宿主写入：直接改文件（长度必须变化，确保 size 指纹失配）。
	// 键是 bindingKey(connectorID, chat) = connectorID + "\x00" + chat，
	// 必须用真正的 NUL 字节。
	key := "c1" + "\x00" + "chat-1"
	payload := stateFile{
		Bindings: map[string]bindingRecord{
			key: {
				ConnectorID: "c1",
				Chat:        "chat-1",
				SessionID:   "sess-2",
				UpdatedAt:   time.Now().Add(time.Hour).UTC(),
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode state: %v", err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(statePath, future, future)

	got := store.ListBindings("c1")
	if len(got) != 1 || got[0].Session.SessionID != "sess-2" {
		t.Fatalf("after external write = %+v, want sess-2", got)
	}
}

// TestConfigStoreStateCacheRefreshesAfterOwnWrite 验证本进程写入后缓存立即
// 反映新值：PutBinding 之后不经过任何重读也能读到新绑定。
func TestConfigStoreStateCacheRefreshesAfterOwnWrite(t *testing.T) {
	store, _, _ := newTestStore(t)

	if got := store.ListBindings(""); len(got) != 0 {
		t.Fatalf("initial bindings = %+v", got)
	}
	first := connectors.Binding{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{SessionID: "sess-1"},
	}
	if err := store.PutBinding(first); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := store.ListBindings(""); len(got) != 1 || got[0].Session.SessionID != "sess-1" {
		t.Fatalf("after put = %+v", got)
	}
	// 重绑必须立即生效（streamMirror 的同 chat 重绑语义依赖这一点）。
	rebound := connectors.Binding{
		ConnectorID: "c1",
		Chat:        "chat-1",
		Session:     connectors.SessionRef{SessionID: "sess-2"},
	}
	if err := store.PutBinding(rebound); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if got := store.ListBindings("c1"); len(got) != 1 || got[0].Session.SessionID != "sess-2" {
		t.Fatalf("after rebind = %+v, want sess-2", got)
	}
}
