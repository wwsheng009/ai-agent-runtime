package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// chat_residency_defaults_test.go —— S4 自动逐出默认值的端到端契约（进程内）。
//
// 切换后的分层默认：
//   - 未配置 → Enabled=true, EvictPrefix=true（越过 high-water 自动逐出）；
//   - 显式 `enabled: false` → 全关（回退入口）；
//   - 显式 `evictPrefix: false` → 只窗口化、不逐出（保守档）。
//
// 这三个语义必须共存，否则"默认开启"会吃掉回退开关或吃掉灰度开关。
func TestResidencyDefaultOnMatrix(t *testing.T) {
	cases := []struct {
		name          string
		configure     func(*runtimecfg.RuntimeConfig)
		wantEnabled   bool
		wantEvict     bool
		wantRuntime   bool
		wantBudgets   bool
		wantStoreLazy bool
		wantStoreErr  bool
	}{
		{
			name:        "未配置（走默认）",
			configure:   func(*runtimecfg.RuntimeConfig) {},
			wantEnabled: true,
			wantEvict:   true,
			wantRuntime: true,
			wantBudgets: true,
			// 短会话不建库：越过 high-water 后才打开 store。
			wantStoreLazy: true,
		},
		{
			name: "显式 enabled=false（回退）",
			configure: func(c *runtimecfg.RuntimeConfig) {
				c.UI.Residency.Enabled = boolPtr(false)
			},
			wantEnabled: false,
			wantEvict:   false,
			wantRuntime: false,
			wantBudgets: false,
		},
		{
			name: "显式 enabled=true + evictPrefix=false（保守档）",
			configure: func(c *runtimecfg.RuntimeConfig) {
				c.UI.Residency.Enabled = boolPtr(true)
				c.UI.Residency.EvictPrefix = boolPtr(false)
			},
			wantEnabled:  true,
			wantEvict:    false,
			wantRuntime:  true,
			wantBudgets:  true,
			wantStoreErr: true,
		},
		{
			name: "显式 evictPrefix=true",
			configure: func(c *runtimecfg.RuntimeConfig) {
				c.UI.Residency.EvictPrefix = boolPtr(true)
			},
			wantEnabled:   true,
			wantEvict:     true,
			wantRuntime:   true,
			wantBudgets:   true,
			wantStoreLazy: true,
		},
	}
	for _, testCase := range cases {
		dir := t.TempDir()
		resetResidencyRuntimeForTest(t)
		shutdownResidencyRuntimeForTest()
		cfg := &runtimecfg.RuntimeConfig{}
		testCase.configure(cfg)
		applyTranscriptResidencyConfig(cfg, dir)

		opts := ui.TranscriptResidencyOptionsSnapshot()
		if opts.Enabled != testCase.wantEnabled || opts.EvictPrefix != testCase.wantEvict {
			t.Fatalf("%s：选项不符 got={enabled:%v evict:%v} want={enabled:%v evict:%v}",
				testCase.name, opts.Enabled, opts.EvictPrefix, testCase.wantEnabled, testCase.wantEvict)
		}
		runtime := currentTranscriptResidencyRuntime()
		if (runtime != nil) != testCase.wantRuntime {
			t.Fatalf("%s：运行时存在性不符 got=%v want=%v", testCase.name, runtime != nil, testCase.wantRuntime)
		}
		budgets := ui.LayoutCacheBudgetsSnapshot()
		hasBudgets := budgets != (ui.LayoutCacheBudgets{})
		if hasBudgets != testCase.wantBudgets {
			t.Fatalf("%s：§15.1 预算应用不符 got=%+v want=%v", testCase.name, budgets, testCase.wantBudgets)
		}
		if runtime != nil {
			store, storePath, storeErr, lazy := runtime.storeSnapshot()
			if store != nil || storePath != "" {
				t.Fatalf("%s：启动期不得提前打开 store：path=%q", testCase.name, storePath)
			}
			if lazy != testCase.wantStoreLazy {
				t.Fatalf("%s：lazy=%v want=%v（err=%q）", testCase.name, lazy, testCase.wantStoreLazy, storeErr)
			}
			if (storeErr != "") != testCase.wantStoreErr {
				t.Fatalf("%s：storeErr=%q wantStoreErr=%v", testCase.name, storeErr, testCase.wantStoreErr)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "session_cells.sqlite")); err == nil {
			t.Fatalf("%s：未越过 high-water 不得创建 store 文件", testCase.name)
		}
		shutdownResidencyRuntimeForTest()
	}
}

// TestResidencyAutoEvictionWaitsForHighWater 锁定自动逐出的触发条件：
// 短会话不装策略、不建库；越过 keep×EvictHighWater 后才打开 store 并裁剪。
func TestResidencyAutoEvictionWaitsForHighWater(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	shutdownResidencyRuntimeForTest()
	applyTranscriptResidencyConfig(&runtimecfg.RuntimeConfig{}, dir)

	// 600 < 512×1.25=640：不装策略、不落库。
	cold := newSceneTrimTestBridgeWithSession(t, 600, "t10-below-water")
	cold.applyChangeSet(&encoding.ChangeSet{})
	if cold.sceneTrimPolicy != nil {
		t.Fatalf("未越过 high-water 不得安装裁剪策略：%+v", cold.sceneTrimPolicy)
	}
	if _, err := os.Stat(filepath.Join(dir, "session_cells.sqlite")); err == nil {
		t.Fatal("未越过 high-water 不得创建 session_cells.sqlite")
	}

	// 700 > 640：自动打开冷区 store，并把已交付前缀裁剪到 keep。
	hot := newSceneTrimTestBridgeWithSession(t, 700, "t10-above-water")
	hot.applyChangeSet(&encoding.ChangeSet{})
	if hot.sceneTrimPolicy == nil {
		t.Fatal("越过 high-water 必须自动安装裁剪策略")
	}
	if size := sceneTrimLen(hot); size > 512 {
		t.Fatalf("自动逐出未收敛：scene cells=%d（期望 <=512）", size)
	}
	if _, err := os.Stat(filepath.Join(dir, "session_cells.sqlite")); err != nil {
		t.Fatalf("自动逐出必须创建写穿 store：%v", err)
	}
	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil {
		t.Fatal("residency runtime 缺失")
	}
	if store, path, errText, lazy := runtime.storeSnapshot(); store == nil || path == "" || errText != "" || lazy {
		t.Fatalf("high-water 后 store 应就绪：path=%q err=%q lazy=%v", path, errText, lazy)
	}
	// 被逐出的旧 cell 必须仍可经冷区读回（自动退化不是丢弃）。
	evicted := 700 - sceneTrimLen(hot)
	restored, expected, ok := restoreColdCellsHeader(&ChatSession{RuntimeEventBridge: hot})
	if !ok || expected != evicted || len(restored) != evicted {
		t.Fatalf("冷区读回不完整：ok=%v expected=%d restored=%d want=%d", ok, expected, len(restored), evicted)
	}
	if !ui.TranscriptResidencyOptionsSnapshot().EvictPrefix {
		t.Fatal("默认应允许自动逐出")
	}
}
