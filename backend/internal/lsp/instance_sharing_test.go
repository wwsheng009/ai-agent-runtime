package lsp

import (
	"context"
	"testing"
	"time"
)

// instance_sharing_test.go 覆盖"同一种语言在一个进程里只有一个 server 实例"的
// 宿主侧出口：Bridge.SharedClient。
//
// 这组断言的价值在于**不变量**而不是功能：一旦有人把 SharedClient 改成新起一个
// client，单实例约束就静默失效——失效表征是两个 gopls 同时常驻（内存翻倍），
// 不报错、不降级、其它测试也不会红。

// newSharingBridge 构造一个带单个名为 alpha 的假 server 的桥。
func newSharingBridge(t *testing.T) *Bridge {
	t.Helper()
	dir := t.TempDir()
	fake := newFakeServer(t)
	cfg := Config{
		Enabled: true,
		Servers: []ServerSpec{{
			Name:       "alpha",
			Command:    "fake-lsp",
			Extensions: []string{".go"},
		}},
		Diagnostics: DiagnosticsConfig{Scope: ScopeAll, WaitMS: 2000, StartWaitMS: 1000},
	}
	bridge := NewBridge(cfg, dir, nil, fake.dial())
	t.Cleanup(func() { bridge.Stop(context.Background()) })
	return bridge
}

func TestSharedClientRejectsUnknownServer(t *testing.T) {
	bridge := newSharingBridge(t)
	if _, ok := bridge.SharedClient(context.Background(), "not-a-server"); ok {
		t.Fatal("未知 server 名不得返回 client（否则语义通道会误以为借到了进程）")
	}
}

func TestSharedClientNilWhenDisabled(t *testing.T) {
	// 未启用诊断池 = 宿主不持有任何进程：借用缝必须报"没有"，让调用方走自建
	// （自建是这里唯一不产生重复实例的选择）。
	bridge := NewBridge(Config{Enabled: false}, ".", nil, nil)
	if _, ok := bridge.SharedClient(context.Background(), "alpha"); ok {
		t.Fatal("诊断池未启用时不得借出 client")
	}
}

func TestSharedClientReturnsSameClientForRepeatedCalls(t *testing.T) {
	bridge := newSharingBridge(t)
	bridge.StartAll(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, ok := bridge.SharedClient(ctx, "alpha")
	if !ok || first == nil {
		t.Fatalf("借用失败：ok=%v client=%v", ok, first)
	}
	second, ok := bridge.SharedClient(ctx, "alpha")
	if !ok || second == nil {
		t.Fatal("第二次借用失败")
	}
	// 同一个 server 的两次借用必须是同一个 client 对象：借用是"拿句柄"，
	// 不是"再要一个进程"。
	if first != second {
		t.Fatalf("两次借用得到不同 client（%p vs %p）——单实例约束已被破坏", first, second)
	}
}

func TestServerByNameIsCaseInsensitive(t *testing.T) {
	bridge := newSharingBridge(t)
	registry := bridge.registry
	if server := registry.ServerByName("ALPHA"); server == nil {
		t.Fatal("server 名查找必须大小写不敏感（调用方与配置的写法不应有大小写耦合）")
	}
	if server := registry.ServerByName("  alpha  "); server == nil {
		t.Fatal("server 名查找必须容忍空白")
	}
	if server := registry.ServerByName(""); server != nil {
		t.Fatal("空名不得匹配任何 server（否则会命中第一个成员）")
	}
}

func TestSharedClientHonorsCallerDeadline(t *testing.T) {
	bridge := newSharingBridge(t)
	// 调用方的 deadline 必须优先于池的启动预算：语义查询不能因为池慢而超自己的时限。
	// 这里故意不 StartAll：pending 成员会让 readyClient 一直等到预算耗尽。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, ok := bridge.SharedClient(ctx, "alpha"); ok {
		t.Skip("server 已就绪，跳过 deadline 断言")
	}
	elapsed := time.Since(start)
	// 池的 StartWaitMS 是 1000ms：若调用方 deadline 未生效，这里会接近 1s。
	if elapsed > 700*time.Millisecond {
		t.Fatalf("借用等待了 %v：调用方 deadline 未生效（应 ≤ 池预算且远小于 1s）", elapsed)
	}
}
