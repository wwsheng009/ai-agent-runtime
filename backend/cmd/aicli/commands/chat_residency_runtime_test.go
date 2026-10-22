package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// chat_residency_runtime_test.go —— T8b-1/T8b-2 契约：
// 默认关闭零副作用；启用时打开写穿 store 并套用 §15.1 默认预算；
// bodyStore=off / 目录不可用 → fail-closed；bridge 自动安装裁剪策略。

// shutdownResidencyRuntimeForTest 关闭并清空运行时（store 必须先关，
// 否则 TempDir 清理会因文件仍被占用而失败）。
func shutdownResidencyRuntimeForTest() {
	if runtime := currentTranscriptResidencyRuntime(); runtime != nil && runtime.Store != nil {
		if closer, ok := runtime.Store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	transcriptResidencyRuntimeState.Store(nil)
}

func resetResidencyRuntimeForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		shutdownResidencyRuntimeForTest()
		ui.ResetLayoutCacheBudgets()
		ui.ConfigureTranscriptResidency(ui.DefaultTranscriptResidencyOptions())
	})
}

func TestResidencyRuntimeDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	applyTranscriptResidencyConfig(nil, dir)
	// T10-4：未配置（nil）→ 走默认值 = 总开关开启，但逐出不开启。
	if runtime := currentTranscriptResidencyRuntime(); runtime == nil {
		t.Fatal("默认必须安装 residency 运行时")
	} else if runtime.Options.EvictPrefix {
		t.Fatalf("默认不得开启逐出：%+v", runtime.Options)
	}
	if budgets := ui.LayoutCacheBudgetsSnapshot(); budgets != (ui.LayoutCacheBudgets{CellRowsMiB: 16, HistoryPlanMiB: 8, FoldOmissionMiB: 8}) {
		t.Fatalf("默认应套用 §15.1 预算：%+v", budgets)
	}
	if opts := ui.TranscriptResidencyOptionsSnapshot(); !opts.Enabled {
		t.Fatalf("T10-4 起默认必须开启：%+v", opts)
	}
	if enabled, _, _ := transcriptResidencyRuntimeStatus(); !enabled {
		t.Fatal("status 默认应为 enabled=true（总开关默认开启）")
	}

	// 显式 false 必须能回退（否则默认开启后就没有关闭入口了）。
	shutdownResidencyRuntimeForTest()
	off := &runtimecfg.RuntimeConfig{}
	off.UI.Residency.Enabled = boolPtr(false)
	applyTranscriptResidencyConfig(off, dir)
	if runtime := currentTranscriptResidencyRuntime(); runtime != nil {
		t.Fatalf("显式 false 必须无运行时：%+v", runtime)
	}
	if budgets := ui.LayoutCacheBudgetsSnapshot(); budgets != (ui.LayoutCacheBudgets{}) {
		t.Fatalf("显式关闭后预算必须复位：%+v", budgets)
	}
	if enabled, _, _ := transcriptResidencyRuntimeStatus(); enabled {
		t.Fatal("显式关闭后 status 必须为 enabled=false")
	}
}

func TestResidencyRuntimeEnabledOpensStoreAndAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	// 写穿 store 只在逐出开启时惰性打开（T10-4）。
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	applyTranscriptResidencyConfig(cfg, dir)

	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || runtime.Store == nil {
		t.Fatalf("启用时 store 必须打开：%+v", runtime)
	}
	wantPath := filepath.Join(dir, "session_cells.sqlite")
	if runtime.StorePath != wantPath {
		t.Fatalf("store 路径=%q，期望 %q", runtime.StorePath, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("store 文件不存在：%v", err)
	}
	// §15.1：residency 启用且未显式配置预算 → 套用 16/8/8 MiB。
	want := ui.LayoutCacheBudgets{CellRowsMiB: 16, HistoryPlanMiB: 8, FoldOmissionMiB: 8}
	if got := ui.LayoutCacheBudgetsSnapshot(); got != want {
		t.Fatalf("§15.1 默认预算未套用：got=%+v want=%+v", got, want)
	}
	if enabled, path, errText := transcriptResidencyRuntimeStatus(); !enabled || path != wantPath || errText != "" {
		t.Fatalf("status 不符：enabled=%v path=%q err=%q", enabled, path, errText)
	}
}

func TestResidencyRuntimeBodyStoreOffIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	cfg.UI.Residency.BodyStore = "off"
	applyTranscriptResidencyConfig(cfg, dir)

	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || runtime.Store != nil {
		t.Fatalf("bodyStore=off 时不得打开 store：%+v", runtime)
	}
	if !strings.Contains(runtime.StoreErr, "off") {
		t.Fatalf("应记录 fail-closed 原因：%q", runtime.StoreErr)
	}
	// bridge 不得安装裁剪策略（无 store）。
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "t8b-off"}})
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if bridge.sceneTrimPolicy != nil {
		t.Fatal("无 store 时不得安装 scene trim 策略")
	}
}

func TestResidencyBudgetOverrideWinsOverDefaults(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	cfg.UI.LayoutCache.CellRowsMiB = 4
	applyTranscriptResidencyConfig(cfg, dir)
	if got := ui.LayoutCacheBudgetsSnapshot(); got != (ui.LayoutCacheBudgets{CellRowsMiB: 4}) {
		t.Fatalf("显式配置应覆盖默认：%+v", got)
	}
}

func TestBridgeAutoInstallsSceneTrimPolicyFromRuntime(t *testing.T) {
	dir := t.TempDir()
	resetResidencyRuntimeForTest(t)
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	// 逐出策略只有显式开启 evictPrefix 才会安装（§15.1 保守档语义）。
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	applyTranscriptResidencyConfig(cfg, dir)
	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil || runtime.Store == nil {
		t.Fatalf("前置条件失败：%+v", runtime)
	}

	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "t8b-session"}})
	bridge.applyChangeSet(&encoding.ChangeSet{})
	policy := bridge.sceneTrimPolicy
	if policy == nil || !policy.Enabled {
		t.Fatal("residency 启用时 bridge 应自动安装裁剪策略")
	}
	if policy.SessionID != "t8b-session" || policy.Store == nil {
		t.Fatalf("策略装配不符：%+v", policy)
	}
	if policy.KeepLastCells != runtime.Options.MaxResidentCells || policy.TrimBatch < 32 {
		t.Fatalf("策略阈值不符：%+v（选项 %+v）", policy, runtime.Options)
	}
	// 幂等：已有策略不得被替换。
	bridge.ensureSceneTrimPolicy()
	if bridge.sceneTrimPolicy != policy {
		t.Fatal("策略安装应幂等（不得替换既有策略）")
	}

	// 关闭运行时后（模拟回退），新 bridge 不得再安装。
	shutdownResidencyRuntimeForTest()
	another := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "t8b-disabled"}})
	another.applyChangeSet(&encoding.ChangeSet{})
	if another.sceneTrimPolicy != nil {
		t.Fatal("运行时关闭后不得安装策略")
	}
}

// TestResidencyEvictPrefixFalseKeepsSceneWhole 锁定 §15.1 的保守档语义：
// `evictPrefix=false`（默认）时 residency 只做窗口化/写穿准备，**不得逐出**。
// 逐出能力由 evictPrefix 显式开启（灰度开关）；否则"默认关闭的逐出"会随
// enabled=true 静默生效，与配置语义不符。
func TestResidencyEvictPrefixFalseKeepsSceneWhole(t *testing.T) {
	// 两个独立 store 目录：先建目录再注册清理（与 T8b 测试同一顺序约定）。
	offDir := t.TempDir()
	onDir := t.TempDir()
	// 只注册一次 cleanup；中途切换运行时用 shutdown 手动关闭当前 store，
	// 避免两次 reset 各自注册 cleanup 而把"关 store"推到 TempDir 清理之后。
	t.Cleanup(func() {
		shutdownResidencyRuntimeForTest()
		ui.ResetLayoutCacheBudgets()
		ui.ConfigureTranscriptResidency(ui.DefaultTranscriptResidencyOptions())
	})
	shutdownResidencyRuntimeForTest()
	cfg := &runtimecfg.RuntimeConfig{}
	cfg.UI.Residency.Enabled = boolPtr(true)
	cfg.UI.Residency.MaxResidentCells = 4
	// evictPrefix 未设置 = false（默认保守档）。
	applyTranscriptResidencyConfig(cfg, offDir)

	bridge := newSceneTrimTestBridge(t, 40)
	bridge.applyChangeSet(&encoding.ChangeSet{})
	if size := sceneTrimLen(bridge); size != 40 {
		t.Fatalf("evictPrefix=false 时不得逐出：scene cells=%d（期望 40）", size)
	}
	if policy := bridge.sceneTrimPolicy; policy != nil && policy.Enabled {
		t.Fatalf("evictPrefix=false 时不应启用逐出策略：%+v", policy)
	}

	// 显式开启后才逐出（并受 keep 约束）。
	shutdownResidencyRuntimeForTest()
	cfg.UI.Residency.EvictPrefix = boolPtr(true)
	applyTranscriptResidencyConfig(cfg, onDir)
	// 必须带 RuntimeSession：ensureSceneTrimPolicy 需要 sessionID 才写穿
	// （无 ID 时 fail-closed 不裁剪，这是刻意的保守语义）。
	bridge2 := newSceneTrimTestBridgeWithSession(t, 40, "t10-evict")
	bridge2.applyChangeSet(&encoding.ChangeSet{})
	if size := sceneTrimLen(bridge2); size >= 40 {
		t.Fatalf("evictPrefix=true 时应收敛到窗口：scene cells=%d", size)
	}
	// 收尾：先关 store，再让 t.TempDir 清理（reset 注册的 cleanup 在本函数之后才跑）。
	shutdownResidencyRuntimeForTest()
}
