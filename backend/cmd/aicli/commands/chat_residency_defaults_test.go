package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// chat_residency_defaults_test.go —— T10-4 默认值切换的端到端契约（进程内）。
//
// 切换后的分层默认：
//   - 未配置 → Enabled=true, EvictPrefix=false（窗口化生效、逐出不生效）；
//   - 显式 `enabled: false` → 全关（回退入口）；
//   - 显式 `evictPrefix: true` → 打开逐出（canary）。
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
		wantStore     bool
	}{
		{
			name:        "未配置（走默认）",
			configure:   func(*runtimecfg.RuntimeConfig) {},
			wantEnabled: true,
			wantEvict:   false,
			wantRuntime: true,
			wantBudgets: true,
			// 默认档不逐出 → 惰性 store：完全不碰磁盘。
			wantStore: false,
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
			wantStore:   false,
		},
		{
			name: "显式 enabled=true + evictPrefix=true（canary）",
			configure: func(c *runtimecfg.RuntimeConfig) {
				c.UI.Residency.Enabled = boolPtr(true)
				c.UI.Residency.EvictPrefix = boolPtr(true)
			},
			wantEnabled: true,
			wantEvict:   true,
			wantRuntime: true,
			wantBudgets: true,
			wantStore:   true,
		},
		{
			name: "只开 evictPrefix（enabled 未配置=默认开）",
			configure: func(c *runtimecfg.RuntimeConfig) {
				c.UI.Residency.EvictPrefix = boolPtr(true)
			},
			wantEnabled: true,
			wantEvict:   true,
			wantRuntime: true,
			wantBudgets: true,
			wantStore:   true,
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
		if testCase.wantStore {
			if runtime == nil || runtime.Store == nil {
				t.Fatalf("%s：逐出开启时 store 应打开：%+v", testCase.name, runtime)
			}
			if runtime.StorePath != filepath.Join(dir, "session_cells.sqlite") {
				t.Fatalf("%s：store 路径不符 %q", testCase.name, runtime.StorePath)
			}
		} else if runtime != nil && runtime.StorePath != "" {
			t.Fatalf("%s：未逐出时不得创建 store 文件：%q", testCase.name, runtime.StorePath)
		}
		if _, err := os.Stat(filepath.Join(dir, "session_cells.sqlite")); err == nil != testCase.wantStore {
			t.Fatalf("%s：磁盘上 store 文件存在性与期望不符（want=%v）", testCase.name, testCase.wantStore)
		}
		shutdownResidencyRuntimeForTest()
	}
}

// TestResidencyDefaultEvictionStaysOff 单独锁定"默认不逐出"：
// 即使 scene 远超窗口，默认配置下也不得裁剪（逐出必须显式开启）。
func TestResidencyDefaultEvictionStaysOff(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	shutdownResidencyRuntimeForTest()
	applyTranscriptResidencyConfig(&runtimecfg.RuntimeConfig{}, dir)

	bridge := newSceneTrimTestBridgeWithSession(t, 100, "t10-default-off")
	for rounds := 0; rounds < 8; rounds++ {
		bridge.applyChangeSet(&encoding.ChangeSet{})
	}
	if size := sceneTrimLen(bridge); size != 100 {
		t.Fatalf("默认（evictPrefix=false）不得逐出：scene cells=%d（期望 100）", size)
	}
	if bridge.sceneTrimPolicy != nil {
		t.Fatalf("默认不得安装裁剪策略：%+v", bridge.sceneTrimPolicy)
	}
	// 但总开关是开的（窗口化/预算生效）。
	if !ui.TranscriptResidencyOptionsSnapshot().Enabled {
		t.Fatal("默认总开关应为开启")
	}
}
