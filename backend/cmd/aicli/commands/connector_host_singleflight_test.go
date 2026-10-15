// 连接器宿主的进程单例装配回归（2026-10-15 qq-bot1 重复投递复盘）：
// chat 启动路径（startAICLIChatConnectors）、TUI /bots 按需路径
// （startLocalConnectorsOnDemand）与 web 懒自举（bootstrapLocalConnectorsForWeb）
// 会在启动窗口内并发到达；此前 check-then-act 无锁，竞态会装配出两套宿主。
// 每套宿主各自注册 attention/content/stream 订阅，同一 assistant_message 与
// completion 事件被两套订阅各投递一次——IM 侧表现为同一条正文/通知收到两份，
// 且陈旧宿主的 L3（IM 回合）登记器看不到进行中的回合，回复路径与镜像双发。
package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureLocalConnectorHostIsProcessSingleton(t *testing.T) {
	// 与其他用例共享进程级引用：进入前清理，退出后回收，避免跨用例串状态。
	if existing := currentLocalConnectorHost(); existing != nil {
		existing.Close()
	}
	setLocalConnectorHost(nil)
	setLocalConnectorHandlers(nil, nil)
	t.Cleanup(func() {
		if host := currentLocalConnectorHost(); host != nil {
			host.Close()
		}
		setLocalConnectorHost(nil)
		setLocalConnectorHandlers(nil, nil)
	})

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("connectors: []\n"), 0o644))

	var mu sync.Mutex
	startCount := 0
	logf := func(format string, args ...any) {
		if strings.Contains(format, "aicli connectors: started") {
			mu.Lock()
			startCount++
			mu.Unlock()
		}
	}

	const workers = 4
	hosts := make([]*LocalConnectorHost, workers)
	errs := make([]error, workers)
	reused := make([]bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hosts[idx], reused[idx], errs[idx] = ensureLocalConnectorHost(
				context.Background(), &localChatRuntimeHost{}, configPath, logf)
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		require.NoError(t, errs[i])
		require.NotNil(t, hosts[i])
		require.Same(t, hosts[0], hosts[i], "并发装配必须复用同一宿主")
	}
	mu.Lock()
	got := startCount
	mu.Unlock()
	require.Equal(t, 1, got, "连接器宿主只允许装配一次（重复装配会注册第二套事件订阅）")

	// 装配闸之外的后续调用（chat 启动 / /bots / web 懒自举）同样必须命中复用。
	require.Same(t, hosts[0], currentLocalConnectorHost())
	host, wasReused, err := ensureLocalConnectorHost(context.Background(), &localChatRuntimeHost{}, configPath, logf)
	require.NoError(t, err)
	require.True(t, wasReused)
	require.Same(t, hosts[0], host)
	mu.Lock()
	require.Equal(t, 1, startCount, "复用路径不得再次装配")
	mu.Unlock()
}

// TestEnsureLocalConnectorHostReclaimsClosedHost 覆盖生命周期协作：宿主关闭后
// 全局引用被清空（Close 语义），下一次装配闸调用应重新装配一套新宿主。
func TestEnsureLocalConnectorHostReclaimsClosedHost(t *testing.T) {
	if existing := currentLocalConnectorHost(); existing != nil {
		existing.Close()
	}
	setLocalConnectorHost(nil)
	setLocalConnectorHandlers(nil, nil)
	t.Cleanup(func() {
		if host := currentLocalConnectorHost(); host != nil {
			host.Close()
		}
		setLocalConnectorHost(nil)
		setLocalConnectorHandlers(nil, nil)
	})

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("connectors: []\n"), 0o644))

	first, wasReused, err := ensureLocalConnectorHost(context.Background(), &localChatRuntimeHost{}, configPath, func(string, ...any) {})
	require.NoError(t, err)
	require.False(t, wasReused)
	require.NotNil(t, first)
	first.Close()
	require.Nil(t, currentLocalConnectorHost())

	second, wasReused, err := ensureLocalConnectorHost(context.Background(), &localChatRuntimeHost{}, configPath, func(string, ...any) {})
	require.NoError(t, err)
	require.False(t, wasReused)
	require.NotNil(t, second)
	require.NotSame(t, first, second)
}
