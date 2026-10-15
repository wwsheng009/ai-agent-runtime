package connectors

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// ── 持有者自应用（配置/凭据变更自动重载）与凭据替换立即生效（2026-10-15 复盘）──

// TestServiceAutoReloadOnConfigChange 覆盖持有者自应用：配置在**本机之外**
// 被更新（模拟另一宿主保存）后，心跳循环发现指纹变化并自动重载，无需人工
// 触发——消灭「保存成功但轮询者不生效」的静默失联。
func TestServiceAutoReloadOnConfigChange(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	provider := &fakeProvider{polling: true}
	service, logs := newOwnerTestService(t, store, provider, "runtime-server", 10*time.Millisecond, time.Minute)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Stop(context.Background()) })

	waitFor(t, "first polling start", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})

	// 模拟其它宿主的保存：更新运行相关配置（Settings），不改动 UpdatedAt 也应生效。
	cfg, ok := store.GetConfig("c1")
	if !ok {
		t.Fatal("config c1 missing")
	}
	cfg.Settings = map[string]string{"poll_interval_seconds": "5"}
	if err := store.PutConfig(cfg); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "auto reload after config change", func() bool {
		starts, exits := provider.pollCounts()
		return starts == 2 && exits >= 1
	})
	if !strings.Contains(logs.joined(), "持有者自动重新加载") {
		t.Fatalf("expected auto-reload log, got:\n%s", logs.joined())
	}
	entries, _ := service.Logs("c1", 20)
	if !containsSummary(entries, "检测到配置/凭据更新，已自动重新加载") {
		t.Fatalf("expected auto-reload ring entry, got %+v", entries)
	}
}

// TestServiceAutoStopOnDisabledConfigChange 覆盖自应用的另一半：配置被停用后
// 持有者停止轮询并释放归属（不遗留"停用仍在跑"的幽灵实例）。
func TestServiceAutoStopOnDisabledConfigChange(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	provider := &fakeProvider{polling: true}
	service, _ := newOwnerTestService(t, store, provider, "runtime-server", 10*time.Millisecond, time.Minute)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Stop(context.Background()) })
	waitFor(t, "first polling start", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})

	cfg, _ := store.GetConfig("c1")
	cfg.Enabled = false
	if err := store.PutConfig(cfg); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "polling exit after disable", func() bool {
		_, exits := provider.pollCounts()
		return exits >= 1
	})
	waitFor(t, "owner released after disable", func() bool {
		_, held := store.OwnerOf("c1")
		return !held
	})
}

// TestServiceApplyCredentialReplacementTakesOverLiveOwner 覆盖「扫码后立即生效」：
// 凭据替换发生在非持有宿主时，强制接管运行归属并以最新配置启动轮询，而不是
// 只记一条「未运行（由 X 持有）」后让 bot 继续失联。
func TestServiceApplyCredentialReplacementTakesOverLiveOwner(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	if _, _, err := store.ClaimConnectorOwner("c1", "other-host", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{polling: true}
	service, logs := newOwnerTestService(t, store, provider, "aicli:node-1", time.Minute, time.Minute)

	if err := service.ApplyCredentialReplacement(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "new host polling after credential replacement", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
	record, ok := store.OwnerOf("c1")
	if !ok || record.Owner != "aicli:node-1" {
		t.Fatalf("owner after credential replacement = %+v ok=%v, want aicli:node-1", record, ok)
	}
	if !strings.Contains(logs.joined(), "凭据已替换，自动接管运行归属（原持有者 other-host）") {
		t.Fatalf("expected takeover log, got:\n%s", logs.joined())
	}
	entries, _ := service.Logs("c1", 20)
	if !containsSummary(entries, "凭据已替换，自动接管运行归属（原持有者 other-host）") {
		t.Fatalf("expected takeover ring entry, got %+v", entries)
	}
}

// TestServiceApplyCredentialReplacementPolicyFallback 覆盖策略边界：本宿主不满足
// policy.runtime_owner 时不强制接管（回落普通重载、记录冲突原因），持有者将在
// 心跳周期内自应用。
func TestServiceApplyCredentialReplacementPolicyFallback(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.RuntimeOwner = "gateway"
	}))
	if _, _, err := store.ClaimConnectorOwner("c1", "gateway", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{polling: true}
	service, logs := newOwnerTestService(t, store, provider, "aicli", time.Minute, time.Minute)

	if err := service.ApplyCredentialReplacement(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if starts, _ := provider.pollCounts(); starts != 0 {
		t.Fatalf("policy-mismatched host must not poll (starts=%d)", starts)
	}
	record, _ := store.OwnerOf("c1")
	if record.Owner != "gateway" {
		t.Fatalf("owner = %q, want gateway (policy must win)", record.Owner)
	}
	if !strings.Contains(logs.joined(), "policy.runtime_owner=gateway") {
		t.Fatalf("expected policy conflict log, got:\n%s", logs.joined())
	}
}

// pollLogProvider 在轮询循环内向日志汇写告警（第 2 条与第 1 条完全相同，验证
// 去重；第 3 条文案不同，验证不同错误仍可见）。
type pollLogProvider struct {
	starts int32
}

func (p *pollLogProvider) Name() string { return "poll-log" }

func (p *pollLogProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{Provider: "poll-log", Label: "Poll Log"}
}

func (p *pollLogProvider) Activate(context.Context, Config) (Activation, error) {
	return Activation{}, nil
}

func (p *pollLogProvider) Deactivate(context.Context, Config) error { return nil }

func (p *pollLogProvider) Send(context.Context, Config, string, []OutboundMessage) error {
	return nil
}

func (p *pollLogProvider) SupportsPolling(Config) bool { return true }

func (p *pollLogProvider) RunPolling(ctx context.Context, _ Config, _ InboundHandler, _ PollingState) error {
	atomic.AddInt32(&p.starts, 1)
	PollingLogf(ctx, LogLevelWarn, "轮询测试告警：%s", "同一条")
	PollingLogf(ctx, LogLevelWarn, "轮询测试告警：%s", "同一条") // 相同文案 → 去重
	PollingLogf(ctx, LogLevelWarn, "轮询测试告警：%s", "另一条")
	<-ctx.Done()
	return nil
}

// TestServicePollingLogSinkWritesRingWithDedupe 覆盖错误观测性：provider 经
// PollingLogf 汇报的运行期错误写入连接器日志环（页面可见），完全相同的持续
// 故障在去重窗口内只记一次，不同文案照常记录。
func TestServicePollingLogSinkWritesRingWithDedupe(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "poll-log"))
	provider := &pollLogProvider{}
	service := newTestService(t, store, &fakeBackend{}, provider)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Stop(context.Background()) })

	waitFor(t, "two distinct polling log entries", func() bool {
		entries, _ := service.Logs("c1", 50)
		return countSummaries(entries, "轮询测试告警：") == 2
	})
	entries, _ := service.Logs("c1", 50)
	if got := countSummaries(entries, "轮询测试告警：同一条"); got != 1 {
		t.Fatalf("identical summary should be deduped to 1 entry, got %d (%+v)", got, entries)
	}
	if got := countSummaries(entries, "轮询测试告警：另一条"); got != 1 {
		t.Fatalf("distinct summary = %d, want 1", got)
	}
}

func containsSummary(entries []LogEntry, substr string) bool {
	return countSummaries(entries, substr) > 0
}

func countSummaries(entries []LogEntry, substr string) int {
	count := 0
	for _, entry := range entries {
		if strings.Contains(entry.Summary, substr) {
			count++
		}
	}
	return count
}
