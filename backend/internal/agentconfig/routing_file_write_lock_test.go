package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 路由文件写锁（方案 §3.4/M11、§12 R4）回归：workspace（chat-prefs.yaml）与
// config（.aicli/config.yaml）两层的写入都是「读-改-写」，必须跨写者串行化——
// aicli TUI 与内嵌 runtime-server 的 API handler 调用的是同一对函数，两个绑定同一
// 工作区的会话也会落到同一份 chat-prefs.yaml；不串行化则后写者带旧快照落盘，静默
// 丢掉前者的字段（丢更新）。

func TestLockRoutingFileWriteBlocksUntilReleased(t *testing.T) {
	unlock := LockRoutingFileWrite(`C:\tmp\routing-prefs.yaml`)

	started := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(started)
		// Windows 下大小写不同但指向同一文件：必须命中同一把锁。
		release := LockRoutingFileWrite(`c:\tmp\routing-prefs.yaml`)
		close(acquired)
		release()
	}()
	<-started

	select {
	case <-acquired:
		t.Fatal("同一路径的第二个写者未等待锁释放（串行化失效）")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()

	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("锁释放后第二个写者仍未获得锁")
	}

	// 释放函数幂等；空路径为 no-op。
	unlock()
	LockRoutingFileWrite("")()
}

func TestUpdateWorkspaceRoutingSectionSerializesConcurrentWriters(t *testing.T) {
	workspacePath := t.TempDir()
	prefsPath := WorkspacePrefsPathForPath(workspacePath)
	require.NotEmpty(t, prefsPath, "workspace 偏好路径必须可解析")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(prefsPath)) })

	// 写者 A 先落 hard 档（等价于 TUI 端已完成的写入）。
	require.NoError(t, UpdateWorkspaceRoutingSection(workspacePath, &SessionRoutingPatch{
		MainAgent: &AICLISessionMainAgentRoutingOverride{
			Profiles: map[string]AICLISessionRouteProfileOverride{"hard": {Model: routingTestStr("alpha-model")}},
		},
	}))

	// 锁被占住期间（写者 A 的「读-改-写」尚未结束），写者 B 必须等待。
	unlock := LockRoutingFileWrite(prefsPath)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- UpdateWorkspaceRoutingSection(workspacePath, &SessionRoutingPatch{
			MainAgent: &AICLISessionMainAgentRoutingOverride{
				Profiles: map[string]AICLISessionRouteProfileOverride{"normal": {Model: routingTestStr("beta-model")}},
			},
		})
	}()
	<-started

	select {
	case err := <-done:
		t.Fatalf("并发写未串行化：写者 B 在锁释放前完成（err=%v）", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("锁释放后写者 B 仍未完成")
	}

	// 串行化 + 锁内重读的直接后果：两个写者的字段都必须保留。
	prefs, err := LoadWorkspaceRoutingPreferencesForPath(workspacePath)
	require.NoError(t, err)
	require.NotNil(t, prefs)
	require.NotNil(t, prefs.MainAgent)
	assert.Equal(t, "alpha-model", prefs.MainAgent.Profiles["hard"].Model)
	assert.Equal(t, "beta-model", prefs.MainAgent.Profiles["normal"].Model)
}

func TestUpdateAICLIRoutingSectionSerializesConcurrentWriters(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	// 先落一份带无关节的既有配置：路由写入不得丢掉它。
	require.NoError(t, writeFileAtomic(configPath, []byte("aicli:\n  chat:\n    default_model: keep-me\n")))

	require.NoError(t, UpdateAICLIRoutingSection(configPath, &AICLIMainAgentRoutingConfig{
		Profiles: map[string]AICLISubagentRouteProfile{"hard": {Model: "alpha-model"}},
	}, true, nil, false))

	unlock := LockRoutingFileWrite(configPath)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- UpdateAICLIRoutingSection(configPath, &AICLIMainAgentRoutingConfig{
			Profiles: map[string]AICLISubagentRouteProfile{"normal": {Model: "beta-model"}},
		}, true, nil, false)
	}()
	<-started

	select {
	case err := <-done:
		t.Fatalf("并发写未串行化：config 写者在锁释放前完成（err=%v）", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("锁释放后 config 写者仍未完成")
	}

	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "beta-model")
	assert.Contains(t, string(raw), "keep-me", "路由写入不得丢掉配置文件中的无关节")
}

// routingTestStr 返回字符串指针（会话/工作区覆盖的字段级语义：nil=继承）。
func routingTestStr(value string) *string {
	return &value
}
