package tools

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	toolkitTools "github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// ADR-0004 §4.1/§4.3/§4.4 注册面验收：
//   - 三档注册（writer 全开 / reader 新鲜全开 / 中等仅定义类 / 过旧不注册）；
//   - 两个逃生舱（tools.enabled=false、tools.stale_reader=off）；
//   - 会话内动态切换（ListTools 前重新评估，无需重启）；
//   - 描述变体（中等档定义类追加 §4.3 提示）与 schema 恒定；
//   - resolver 暴露 IndexedAt 派生的 snapshot_ts/staleness/tier。

var codeSurfaceToolNames = []string{
	"code_search", "code_inspect", "code_navigate", "code_references", "code_callers",
}

func boolPtr(v bool) *bool { return &v }

// seedIndexedStore 建库并写入一行带指定 indexed_at 的文件记录。
func seedIndexedStore(t *testing.T, root string, indexedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeOn
	store, err := knowledge.OpenStore(ctx, knowledge.StorePathFor(cfg), false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	wsID, err := store.EnsureWorkspace(ctx, knowledge.Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if _, err := store.UpsertFile(ctx, knowledge.FileRecord{
		WorkspaceID: wsID,
		Path:        "pkg/demo.go",
		Language:    "go",
		ContentHash: "hash-demo",
		IndexedAt:   indexedAt,
	}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
}

// writeWriterLockFile 模拟本进程持有写锁（writer）：payload 口径与 owner.go 同源。
func writeWriterLockFile(t *testing.T, root string) {
	t.Helper()
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeOn
	lockPath := knowledge.StorePathFor(cfg) + ".lock"
	payload := fmt.Sprintf(
		`{"pid":%d,"host":"test","started_at_unix_ms":%d,"knowledge_version":1}`,
		os.Getpid(), time.Now().UnixMilli(),
	)
	if err := os.WriteFile(lockPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(lockPath) })
}

func newStalenessManager(t *testing.T, root string, tune func(*runtimecfg.RuntimeConfig)) *Manager {
	t.Helper()
	t.Cleanup(codeIndexCacheGlobal.closeAll)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = root
	cfg.Knowledge.Mode = knowledge.ModeOn
	cfg.Knowledge.CodeTools = knowledge.CodeToolsOn
	if tune != nil {
		tune(cfg)
	}
	return NewDefaultManagerWithRuntimeConfig(nil, cfg)
}

func listedToolNames(m *Manager) map[string]bool {
	names := map[string]bool{}
	for _, descriptor := range m.ListTools() {
		names[descriptor.Name] = true
	}
	return names
}

func listedDescriptors(m *Manager) map[string]ToolDescriptor {
	out := map[string]ToolDescriptor{}
	for _, descriptor := range m.ListTools() {
		out[descriptor.Name] = descriptor
	}
	return out
}

func TestCodeToolTiersRegisterByStaleness(t *testing.T) {
	cases := []struct {
		name            string
		seed            func(t *testing.T, root string)
		wantAll         bool
		wantDefinitions bool
	}{
		{
			name: "writer 全开（锁持有者即使快照很旧）",
			seed: func(t *testing.T, root string) {
				seedIndexedStore(t, root, time.Now().Add(-2*time.Hour))
				writeWriterLockFile(t, root)
			},
			wantAll: true,
		},
		{
			name:    "reader 新鲜（≤S_fresh）全开",
			seed:    func(t *testing.T, root string) { seedIndexedStore(t, root, time.Now().Add(-10*time.Second)) },
			wantAll: true,
		},
		{
			name:            "reader 中等（S_fresh<S≤S_max）仅定义类",
			seed:            func(t *testing.T, root string) { seedIndexedStore(t, root, time.Now().Add(-4*time.Hour)) },
			wantDefinitions: true,
		},
		{
			name: "reader 过旧 S>S_max 不注册",
			seed: func(t *testing.T, root string) { seedIndexedStore(t, root, time.Now().Add(-12*time.Hour)) },
		},
		{
			name:    "索引不可用保留降级工具面（全注册）",
			seed:    func(t *testing.T, root string) {},
			wantAll: true,
		},
	}

	definitionTools := []string{"code_search", "code_inspect", "code_navigate"}
	relationTools := []string{"code_references", "code_callers"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.seed(t, root)
			names := listedToolNames(newStalenessManager(t, root, nil))
			if !names["grep"] || !names["view"] {
				t.Fatalf("P0 工具必须始终可见（ADR §4.5）：%v", names)
			}
			switch {
			case tc.wantAll:
				for _, name := range append(append([]string{}, definitionTools...), relationTools...) {
					if !names[name] {
						t.Fatalf("%s 必须注册（全开档）：%v", name, names)
					}
				}
			case tc.wantDefinitions:
				for _, name := range definitionTools {
					if !names[name] {
						t.Fatalf("%s 必须注册（中等档定义类）：%v", name, names)
					}
				}
				for _, name := range relationTools {
					if names[name] {
						t.Fatalf("%s 不得注册（中等档关系类）：%v", name, names)
					}
				}
			default:
				for _, name := range append(append([]string{}, definitionTools...), relationTools...) {
					if names[name] {
						t.Fatalf("%s 不得注册（过旧档）：%v", name, names)
					}
				}
			}
		})
	}
}

func TestCodeToolEscapeHatches(t *testing.T) {
	t.Run("knowledge.tools.enabled=false：索引照跑但不注册工具", func(t *testing.T) {
		root := t.TempDir()
		seedIndexedStore(t, root, time.Now()) // 新鲜索引，仍必须全关
		m := newStalenessManager(t, root, func(cfg *runtimecfg.RuntimeConfig) {
			cfg.Knowledge.Tools.Enabled = boolPtr(false)
		})
		names := listedToolNames(m)
		for _, name := range codeSurfaceToolNames {
			if names[name] {
				t.Fatalf("%s 不得注册（tools.enabled=false）：%v", name, names)
			}
		}
		if !names["grep"] || !names["view"] {
			t.Fatalf("P0 工具不受逃生舱影响：%v", names)
		}
	})

	t.Run("knowledge.tools.stale_reader=off：reader 也按 writer 策略全开", func(t *testing.T) {
		root := t.TempDir()
		seedIndexedStore(t, root, time.Now().Add(-2*time.Hour))
		m := newStalenessManager(t, root, func(cfg *runtimecfg.RuntimeConfig) {
			cfg.Knowledge.Tools.StaleReader = knowledge.StaleReaderOff
		})
		names := listedToolNames(m)
		for _, name := range codeSurfaceToolNames {
			if !names[name] {
				t.Fatalf("%s 必须注册（stale_reader=off 逃生舱）：%v", name, names)
			}
		}
	})
}

func TestCodeToolSurfaceSwitchesWithoutRestart(t *testing.T) {
	root := t.TempDir()
	seedIndexedStore(t, root, time.Now().Add(-4*time.Hour))
	m := newStalenessManager(t, root, nil)

	names := listedToolNames(m)
	if !names["code_search"] || names["code_references"] {
		t.Fatalf("中等档期望仅定义类：%v", names)
	}

	// writer 提交推进 indexed_at → 同一管理器无需重启即恢复全开。
	codeIndexCacheGlobal.closeAll()
	seedIndexedStore(t, root, time.Now())
	names = listedToolNames(m)
	if !names["code_references"] || !names["code_callers"] {
		t.Fatalf("新鲜档应恢复全开（无需重启）：%v", names)
	}

	// 快照再次变旧 → 自动收回到不注册。
	codeIndexCacheGlobal.closeAll()
	seedIndexedStore(t, root, time.Now().Add(-12*time.Hour))
	names = listedToolNames(m)
	for _, name := range codeSurfaceToolNames {
		if names[name] {
			t.Fatalf("过旧档应全部收回（无需重启）：%v", names)
		}
	}
}

func TestCodeToolDescriptionVariantAndSchemaConstant(t *testing.T) {
	root := t.TempDir()
	seedIndexedStore(t, root, time.Now().Add(-4*time.Hour))
	m := newStalenessManager(t, root, nil)

	medium := listedDescriptors(m)
	for _, name := range []string{"code_search", "code_inspect", "code_navigate"} {
		description := medium[name].Description
		if !strings.Contains(description, "可能落后约") || !strings.Contains(description, "grep 复核") {
			t.Fatalf("%s 中等档描述缺少 ADR §4.3 提示：%q", name, description)
		}
	}
	if _, ok := medium["code_references"]; ok {
		t.Fatalf("中等档不得出现关系类工具")
	}
	mediumSchema := medium["code_search"].Parameters

	// 新鲜档：描述变体消失，同一工具 schema 完全一致（D4）。
	codeIndexCacheGlobal.closeAll()
	seedIndexedStore(t, root, time.Now())
	fresh := listedDescriptors(m)
	if strings.Contains(fresh["code_search"].Description, "可能落后约") {
		t.Fatalf("新鲜档不应携带陈旧提示：%q", fresh["code_search"].Description)
	}
	if !reflect.DeepEqual(fresh["code_search"].Parameters, mediumSchema) {
		t.Fatalf("schema 必须恒定：\nmedium=%v\nfresh=%v", mediumSchema, fresh["code_search"].Parameters)
	}
	for _, name := range []string{"code_search", "code_inspect", "code_navigate"} {
		if !reflect.DeepEqual(fresh[name].Parameters, medium[name].Parameters) {
			t.Fatalf("%s schema 随模式变化（违反 D4）", name)
		}
	}
}

func TestCodeToolExecuteRejectsOutOfTier(t *testing.T) {
	root := t.TempDir()
	seedIndexedStore(t, root, time.Now().Add(-4*time.Hour))
	m := newStalenessManager(t, root, nil)

	_, err := m.Execute(context.Background(), "code_references", map[string]interface{}{"symbol": "Alpha"})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("中等档 code_references 必须拒绝执行，got err=%v", err)
	}
}

func TestCodeIndexResolverExposesSnapshotAndTier(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(codeIndexCacheGlobal.closeAll)
	indexedAt := time.Now().Add(-4 * time.Hour)
	seedIndexedStore(t, root, indexedAt)

	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeOn
	resolver := newCodeIndexResolver(cfg, root)

	handle, ok := resolver(context.Background())
	if !ok || handle == nil {
		t.Fatal("索引应可用")
	}
	if handle.Writer {
		t.Fatal("无锁时不得判为 writer")
	}
	if handle.SnapshotTS != indexedAt.Unix() {
		t.Fatalf("snapshot_ts = %d, want %d（IndexedAt 秒口径）", handle.SnapshotTS, indexedAt.Unix())
	}
	if handle.StalenessSeconds < 14399 || handle.StalenessSeconds > 14402 {
		t.Fatalf("staleness_seconds = %d, want ~14400", handle.StalenessSeconds)
	}
	if handle.Tier != toolkitTools.CodeIndexTierDefinitions {
		t.Fatalf("tier = %q, want definitions", handle.Tier)
	}

	// writer（本进程持锁）：S=0、tier=all，即使快照很旧。
	writeWriterLockFile(t, root)
	codeIndexCacheGlobal.closeAll()
	handle, ok = resolver(context.Background())
	if !ok || handle == nil {
		t.Fatal("writer 下索引应可用")
	}
	if !handle.Writer || handle.StalenessSeconds != 0 || handle.Tier != toolkitTools.CodeIndexTierAll {
		t.Fatalf("writer 句柄 = writer=%v staleness=%d tier=%q", handle.Writer, handle.StalenessSeconds, handle.Tier)
	}
}
